package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/usecase"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const (
	tenant = "11111111-1111-4111-8111-111111111111"
	person = "22222222-2222-4222-8222-222222222222"
)

var clock = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// memoryStore keeps the sagas by id and counts versions like the real store.
type memoryStore struct {
	sagas map[string]saga.Saga
	keys  map[string]string // by tenant and key, as the real store (DD-23)
}

func newStore() *memoryStore {
	return &memoryStore{sagas: map[string]saga.Saga{}, keys: map[string]string{}}
}

func (m *memoryStore) Create(_ context.Context, s saga.Saga) (saga.Saga, bool, error) {
	if id, ok := m.keys[s.TenantID+"|"+s.IdempotencyKey]; ok {
		return m.sagas[id], false, nil
	}
	// as the real store: the key of the tenant is answered first, then a payment held by another saga is refused
	if _, held, _ := m.ForPayment(context.Background(), s.TenantID, s.Payment.PaymentID); held {
		return saga.Saga{}, false, saga.ErrPaymentTaken
	}
	m.keys[s.TenantID+"|"+s.IdempotencyKey], m.sagas[s.ID] = s.ID, s
	return s, true, nil
}

func (m *memoryStore) ForPayment(_ context.Context, tenantID, paymentID string) (saga.Saga, bool, error) {
	for _, s := range m.sagas {
		if s.TenantID == tenantID && s.Payment.PaymentID == paymentID && s.HoldsPayment() {
			return s, true, nil
		}
	}
	return saga.Saga{}, false, nil
}

func (m *memoryStore) Save(_ context.Context, s *saga.Saga) error {
	stored, ok := m.sagas[s.ID]
	switch {
	case !ok:
		return saga.ErrNotFound
	case stored.Version != s.Version:
		return saga.ErrConflict
	}
	s.Version++
	m.sagas[s.ID] = *s
	return nil
}

func (m *memoryStore) Get(_ context.Context, id string) (saga.Saga, error) {
	s, ok := m.sagas[id]
	if !ok {
		return saga.Saga{}, saga.ErrNotFound
	}
	return s, nil
}

func (m *memoryStore) ClaimStale(_ context.Context, before, now time.Time) ([]saga.Saga, error) {
	var taken []saga.Saga
	for id, s := range m.sagas {
		if s.Status == saga.Running && s.UpdatedAt.Before(before) {
			s.UpdatedAt, s.Version = now, s.Version+1
			m.sagas[id] = s
			taken = append(taken, s)
		}
	}
	return taken, nil
}

// fakeCash and fakeReservations remember every call in one shared journal, so a test can check the order.
type fakeCash struct {
	journal    *[]string
	calls      []out.Call
	incomeErr  error
	reverseErr error
}

func (f *fakeCash) RecordIncome(_ context.Context, call out.Call, tenantID string, p saga.Payment) (string, error) {
	*f.journal = append(*f.journal, "income")
	f.calls = append(f.calls, call)
	if tenantID != tenant || p.AmountCents != 150000 {
		return "", fmt.Errorf("unexpected income %s %d", tenantID, p.AmountCents)
	}
	return "mov-1", f.incomeErr
}

func (f *fakeCash) ReverseIncome(_ context.Context, call out.Call, _, movementID, _ string) error {
	*f.journal = append(*f.journal, "reverse:"+movementID)
	f.calls = append(f.calls, call)
	return f.reverseErr
}

// fakeLookup answers the read of the income after an unknown step 1 and writes its calls in the journal.
type fakeLookup struct {
	journal    *[]string
	movementID string
	found      bool
	err        error
}

func (f *fakeLookup) FindIncome(_ context.Context, tenantID, paymentID string) (string, bool, error) {
	*f.journal = append(*f.journal, "lookup:"+paymentID)
	if tenantID != tenant {
		return "", false, fmt.Errorf("unexpected lookup tenant %s", tenantID)
	}
	return f.movementID, f.found, f.err
}

type fakeReservations struct {
	journal  *[]string
	calls    []out.Call
	payments []saga.Payment
	err      error
}

func (f *fakeReservations) RecordValidatedPayment(_ context.Context, call out.Call, _ string, p saga.Payment) error {
	*f.journal = append(*f.journal, "payment")
	f.calls = append(f.calls, call)
	f.payments = append(f.payments, p)
	return f.err
}

type rig struct {
	store    *memoryStore
	cash     *fakeCash
	lookup   *fakeLookup
	bookings *fakeReservations
	journal  *[]string
	log      *bytes.Buffer
	job      *usecase.RegisterPayment
}

func newRig() *rig {
	journal := &[]string{}
	r := &rig{store: newStore(), journal: journal, log: &bytes.Buffer{}}
	r.cash, r.bookings = &fakeCash{journal: journal}, &fakeReservations{journal: journal}
	r.lookup = &fakeLookup{journal: journal}
	n := 0
	r.job = usecase.NewRegisterPayment(r.store, r.cash, r.lookup, r.bookings, slog.New(slog.NewJSONHandler(r.log, nil)),
		func() string { n++; return fmt.Sprintf("saga-%d", n) }, func() time.Time { return clock }, time.Minute)
	return r
}

func command() in.StartCommand {
	return in.StartCommand{IdempotencyKey: "key-register-0001", TenantID: tenant, OnBehalfOf: person, Payment: saga.Payment{
		ReservationID: "r-1", PaymentID: "p-1", Medium: saga.MediumCash, AmountCents: 150000, BusinessDate: "2026-11-10",
		Concept: "Abono", ApprovedBy: person}}
}

func rejection(code string) error {
	return &saga.ParticipantError{Code: code, Message: "refused", Definitive: true}
}

// AC-WF2-01 and AC-WF2-06: income first, then the payment; every call carries its stable key and the person.
func TestA_records_the_income_then_the_payment(t *testing.T) {
	r := newRig()

	s, created, err := r.job.Start(context.Background(), command())

	if err != nil || !created || s.Status != saga.Completed {
		t.Fatalf("saga %+v created %v err %v", s, created, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income,payment" {
		t.Errorf("order = %s", got)
	}
	if r.cash.calls[0].Key != "saga-1:RECORD_CASH_INCOME" || r.bookings.calls[0].Key != "saga-1:RECORD_VALIDATED_PAYMENT" {
		t.Errorf("keys = %v %v", r.cash.calls, r.bookings.calls)
	}
	if r.cash.calls[0].OnBehalfOf != person || r.bookings.calls[0].OnBehalfOf != person {
		t.Error("the calls do not carry the person who started the saga")
	}
	if s.CashMovementID != "mov-1" || !s.Done(saga.RecordCashIncome) || !s.Done(saga.RecordValidatedPayment) {
		t.Errorf("state = %+v", s)
	}
}

// HU-COM-010 DP-11: the reason of the approval reaches step 2 untouched and the saga completes as before.
func TestA_the_reason_reaches_the_payment_step(t *testing.T) {
	r := newRig()
	cmd := command()
	cmd.Payment.Reason = "Amount verified in the bank"

	s, _, err := r.job.Start(context.Background(), cmd)

	if err != nil || s.Status != saga.Completed || len(r.bookings.payments) != 1 || r.bookings.payments[0].Reason != "Amount verified in the bank" {
		t.Fatalf("saga %+v err %v payments %+v", s, err, r.bookings.payments)
	}
}

// The compensation does not change with a reason: a refusal of step 2 still reverses the income.
func TestA_a_refused_payment_with_a_reason_still_reverses_the_income(t *testing.T) {
	r := newRig()
	r.bookings.err = rejection("REASON_REQUIRED")
	cmd := command()
	cmd.Payment.Reason = "Amount verified in the bank"

	s, _, err := r.job.Start(context.Background(), cmd)

	if err != nil || s.Status != saga.Compensated || s.FailedStep != saga.RecordValidatedPayment {
		t.Fatalf("saga %+v err %v", s, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income,payment,reverse:mov-1" {
		t.Errorf("calls = %s", got)
	}
}

// AC-WF2-02: a refused income stops before step 2 and leaves nothing to undo.
func TestA_a_refused_income_stops_before_step_2(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = rejection("NO_OPEN_CASH")

	s, _, err := r.job.Start(context.Background(), command())

	if err != nil || s.Status != saga.Failed || s.FailedStep != saga.RecordCashIncome {
		t.Fatalf("saga %+v err %v", s, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income" {
		t.Errorf("calls = %s", got)
	}
}

// AC-WF2-03: a payment the reservation refuses reverses the income, and the saga ends COMPENSATED.
func TestA_a_failed_payment_reverses_the_income(t *testing.T) {
	r := newRig()
	r.bookings.err = rejection("NO_AVAILABILITY")

	s, _, err := r.job.Start(context.Background(), command())

	if err != nil || s.Status != saga.Compensated || s.FailedStep != saga.RecordValidatedPayment {
		t.Fatalf("saga %+v err %v", s, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income,payment,reverse:mov-1" {
		t.Errorf("calls = %s", got)
	}
	if last := r.cash.calls[len(r.cash.calls)-1]; last.Key != "saga-1:RECORD_CASH_INCOME:undo" {
		t.Errorf("reversal key = %q", last.Key)
	}
	if !s.Steps[0].Compensated {
		t.Errorf("steps = %+v", s.Steps)
	}
}

// An unknown outcome of step 2 may have recorded the payment: the income is not reversed and a person decides.
func TestA_an_unknown_payment_outcome_is_not_reversed(t *testing.T) {
	r := newRig()
	r.bookings.err = errors.New("timeout")

	s, _, _ := r.job.Start(context.Background(), command())

	if s.Status != saga.Failed || strings.Contains(strings.Join(*r.journal, ","), "reverse") {
		t.Errorf("saga %+v calls %v", s, *r.journal)
	}
}

// AC-WF2-04: a reversal that fails leaves the saga FAILED, with the code and no internal detail.
func TestA_a_failed_compensation_leaves_the_saga_failed(t *testing.T) {
	r := newRig()
	r.bookings.err = rejection("NO_AVAILABILITY")
	r.cash.reverseErr = rejection("ALREADY_CLOSED")

	s, _, _ := r.job.Start(context.Background(), command())

	if s.Status != saga.Failed || s.FailedStep != saga.RecordValidatedPayment || !strings.Contains(s.FailureDetail, "ALREADY_CLOSED") {
		t.Errorf("saga %+v", s)
	}
}

// AC-WF2-05: the same key starts nothing twice.
func TestA_a_second_start_is_a_replay(t *testing.T) {
	r := newRig()
	r.job.Start(context.Background(), command())
	before := len(*r.journal)

	s, created, err := r.job.Start(context.Background(), command())

	if err != nil || created || s.ID != "saga-1" || len(*r.journal) != before {
		t.Errorf("created %v err %v calls %v", created, err, *r.journal)
	}
}

// AC-WF2-07: a stale saga is claimed and finished from its saved state, without calling step 1 again.
func TestA_a_stale_saga_is_resumed_once(t *testing.T) {
	r := newRig()
	old := clock.Add(-10 * time.Minute)
	r.store.sagas["s-9"] = saga.Saga{ID: "s-9", Status: saga.Running, TenantID: tenant, OnBehalfOf: person, UpdatedAt: old,
		Payment: command().Payment, CashMovementID: "mov-9", Steps: []saga.StepState{{Name: saga.RecordCashIncome}}}

	if err := r.job.Resume(context.Background(), clock.Add(-2*time.Minute)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := r.job.Resume(context.Background(), clock.Add(-2*time.Minute)); err != nil {
		t.Fatalf("second resume: %v", err)
	}

	got := r.store.sagas["s-9"]
	if got.Status != saga.Completed || strings.Join(*r.journal, ",") != "payment" {
		t.Errorf("saga %+v calls %v", got, *r.journal)
	}
}

// AC-WF2-08: an error that is not a participant's never reaches the state or the answer.
func TestA_logs_and_state_carry_no_secret(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = errors.New("dial postgres://user:secret-password@db:5432: refused")

	s, _, _ := r.job.Start(context.Background(), command())

	if strings.Contains(s.FailureDetail, "secret-password") || strings.Contains(r.log.String(), "secret-password") {
		t.Errorf("a secret leaked: %q %s", s.FailureDetail, r.log.String())
	}
}

func TestA_get_reads_the_saga_of_the_store(t *testing.T) {
	r := newRig()
	started, _, _ := r.job.Start(context.Background(), command())

	got, err := r.job.Get(context.Background(), started.ID)

	if err != nil || got.ID != started.ID {
		t.Errorf("got %+v err %v", got, err)
	}
}

// D2 of the audit of 2026-11-13: a second saga with ANOTHER key over the same payment must not record the income again
// (evidence 5b w3/033: the second saga b97cfb2f ended COMPLETED).
func TestA_a_second_saga_for_the_same_payment_records_no_second_income(t *testing.T) {
	r := newRig()
	r.job.Start(context.Background(), command())
	before := len(*r.journal)
	again := command()
	again.IdempotencyKey = "key-register-0002"

	_, created, err := r.job.Start(context.Background(), again)

	if !errors.Is(err, saga.ErrPaymentTaken) || created || len(*r.journal) != before {
		t.Errorf("created %v err %v calls %v", created, err, *r.journal)
	}
}

// AC-WF2-05 (signature note 3, Annex C): a repeated key of the same tenant starts nothing and answers the saga already
// created, whatever the body: another amount, or another payment that a third saga holds.
func TestA_a_repeated_key_answers_the_saga_whatever_the_body(t *testing.T) {
	r := newRig()
	first, _, _ := r.job.Start(context.Background(), command())
	held := command()
	held.IdempotencyKey, held.Payment.PaymentID = "key-register-0002", "p-2"
	r.store.sagas["s-held"] = saga.Saga{ID: "s-held", IdempotencyKey: held.IdempotencyKey, Status: saga.Completed,
		TenantID: tenant, Payment: held.Payment}
	otherAmount := command()
	otherAmount.Payment.AmountCents = 99
	otherPayment := command()
	otherPayment.Payment.PaymentID = "p-2"
	before := len(*r.journal)

	for name, again := range map[string]in.StartCommand{"another amount": otherAmount, "a payment held by another saga": otherPayment} {
		got, created, err := r.job.Start(context.Background(), again)

		if err != nil || created || got.ID != first.ID || len(*r.journal) != before {
			t.Errorf("%s: created %v err %v saga %q calls %v", name, created, err, got.ID, *r.journal)
		}
	}
}

// AC-WF2-05 amended (DD-23): the key is unique per tenant. Another tenant with the same key starts a saga of its own and
// learns nothing of the first one.
func TestA_another_tenant_with_the_same_key_starts_its_own_saga(t *testing.T) {
	r := newRig()
	first, _, _ := r.job.Start(context.Background(), command())
	again := command()
	again.TenantID = "33333333-3333-4333-8333-333333333333"

	got, created, err := r.job.Start(context.Background(), again)

	if err != nil || !created || got.ID == first.ID || got.TenantID != again.TenantID {
		t.Errorf("created %v err %v saga %+v", created, err, got)
	}
}

func TestA_a_replay_with_the_same_tenant_and_body_still_answers_the_saga(t *testing.T) {
	r := newRig()
	first, _, _ := r.job.Start(context.Background(), command())

	got, created, err := r.job.Start(context.Background(), command())

	if err != nil || created || got.ID != first.ID {
		t.Errorf("created %v err %v saga %q", created, err, got.ID)
	}
}

// AC-WF2-08: the line that ends a saga carries the tenant and the saga (the correlation id is added by correlation.LogHandler).
func TestA_the_end_of_a_saga_is_logged_with_tenant_and_saga(t *testing.T) {
	r := newRig()
	s, _, _ := r.job.Start(context.Background(), command())

	line := r.log.String()
	for _, want := range []string{`"tenantId":"` + tenant + `"`, `"sagaId":"` + s.ID + `"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log lacks %s: %s", want, line)
		}
	}
}

// AC-WF2-11 (option B, replaces the resume of D6): an unknown outcome of step 1 asks the cash; the income exists, so step 1
// counts as done with the movement of the read and the saga goes on to step 2.
func TestA_an_unknown_income_that_exists_goes_on_to_step_2(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = errors.New("timeout awaiting the cash")
	r.lookup.movementID, r.lookup.found = "mov-found", true

	s, created, err := r.job.Start(context.Background(), command())

	if err != nil || !created || s.Status != saga.Completed || s.CashMovementID != "mov-found" || !s.Done(saga.RecordCashIncome) {
		t.Fatalf("saga %+v created %v err %v", s, created, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income,lookup:p-1,payment" {
		t.Errorf("calls = %s", got)
	}
}

// AC-WF2-11: the income does not exist, so nothing was recorded and nothing is undone: FAILED at step 1 as AC-WF2-02, and
// the payment is free for a new start. No internal text reaches the state or the log.
func TestA_an_unknown_income_that_does_not_exist_ends_failed_and_frees_the_payment(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = errors.New("context deadline exceeded: postgres://user:secret-password@db")

	s, _, err := r.job.Start(context.Background(), command())

	if err != nil || s.Status != saga.Failed || s.FailedStep != saga.RecordCashIncome || s.HoldsPayment() {
		t.Fatalf("saga %+v err %v", s, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income,lookup:p-1" {
		t.Errorf("calls = %s, want the income and the read only", got)
	}
	if strings.Contains(s.FailureDetail, "secret-password") || strings.Contains(r.log.String(), "secret-password") {
		t.Errorf("a secret leaked: %q", s.FailureDetail)
	}
}

// AC-WF2-11 and AC-WF2-02: a definitive 4xx of step 1 ends FAILED directly; the cash is not asked.
func TestA_a_refused_income_is_not_looked_up(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = rejection("NO_OPEN_CASH")

	s, _, err := r.job.Start(context.Background(), command())

	if err != nil || s.Status != saga.Failed || s.FailedStep != saga.RecordCashIncome {
		t.Fatalf("saga %+v err %v", s, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income" {
		t.Errorf("calls = %s, want no read", got)
	}
}

// AC-WF2-11 and norm section 5.8: the read fails too, so the saga does not guess. It ends FAILED for a person, and the
// payment stays held because the income may exist (AC-WF2-10).
func TestA_an_unknown_income_whose_lookup_fails_ends_failed_and_holds_the_payment(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = errors.New("timeout awaiting the cash")
	r.lookup.err = &saga.ParticipantError{Code: "PARTICIPANT_UNAVAILABLE", Message: "no answer from the participant"}

	s, _, err := r.job.Start(context.Background(), command())

	if err != nil || s.Status != saga.Failed || s.FailedStep != saga.RecordCashIncome || !s.HoldsPayment() {
		t.Fatalf("saga %+v err %v", s, err)
	}
	if !strings.HasPrefix(s.FailureDetail, "INCOME_OUTCOME_UNKNOWN:") {
		t.Errorf("detail = %q", s.FailureDetail)
	}
	if got := strings.Join(*r.journal, ","); got != "income,lookup:p-1" {
		t.Errorf("calls = %s", got)
	}
	again := command()
	again.IdempotencyKey = "key-register-0002"
	if _, _, err := r.job.Start(context.Background(), again); !errors.Is(err, saga.ErrPaymentTaken) {
		t.Errorf("a second start over the held payment: err %v", err)
	}
}
