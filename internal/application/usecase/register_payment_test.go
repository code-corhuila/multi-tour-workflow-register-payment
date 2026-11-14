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
	keys  map[string]string
}

func newStore() *memoryStore {
	return &memoryStore{sagas: map[string]saga.Saga{}, keys: map[string]string{}}
}

func (m *memoryStore) Create(_ context.Context, s saga.Saga) (saga.Saga, bool, error) {
	if id, ok := m.keys[s.IdempotencyKey]; ok {
		return m.sagas[id], false, nil
	}
	m.keys[s.IdempotencyKey], m.sagas[s.ID] = s.ID, s
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
	bookings *fakeReservations
	journal  *[]string
	log      *bytes.Buffer
	job      *usecase.RegisterPayment
}

func newRig() *rig {
	journal := &[]string{}
	r := &rig{store: newStore(), journal: journal, log: &bytes.Buffer{}}
	r.cash, r.bookings = &fakeCash{journal: journal}, &fakeReservations{journal: journal}
	n := 0
	r.job = usecase.NewRegisterPayment(r.store, r.cash, r.bookings, slog.New(slog.NewJSONHandler(r.log, nil)),
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

// D1 of the audit: a replay of a key is the same start only when the tenant and the body are the same.
func TestA_a_replay_of_a_key_with_another_tenant_or_another_body_is_refused(t *testing.T) {
	otherBody := command()
	otherBody.Payment.AmountCents = 99
	otherTenant := command()
	otherTenant.TenantID = "33333333-3333-4333-8333-333333333333"
	for name, again := range map[string]in.StartCommand{"another body": otherBody, "another tenant": otherTenant} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			first, _, _ := r.job.Start(context.Background(), command())
			before := len(*r.journal)

			got, created, err := r.job.Start(context.Background(), again)

			if !errors.Is(err, saga.ErrKeyReuse) || created || got.ID == first.ID || len(*r.journal) != before {
				t.Errorf("created %v err %v saga %q calls %v", created, err, got.ID, *r.journal)
			}
		})
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

// D6: step 1 with an unknown outcome (the cash may have recorded it) ends FAILED at that step; the saga neither goes on to
// step 2 nor reverses what it does not know, and the answer carries no internal text.
func TestA_an_unknown_income_outcome_stops_without_step_2_or_reversal(t *testing.T) {
	r := newRig()
	r.cash.incomeErr = errors.New("context deadline exceeded: postgres://user:secret-password@db")

	s, created, err := r.job.Start(context.Background(), command())

	if err != nil || !created || s.Status != saga.Failed || s.FailedStep != saga.RecordCashIncome {
		t.Fatalf("saga %+v created %v err %v", s, created, err)
	}
	if got := strings.Join(*r.journal, ","); got != "income" {
		t.Errorf("calls = %s, want only the income", got)
	}
	if strings.Contains(s.FailureDetail, "secret-password") {
		t.Errorf("detail leaks: %q", s.FailureDetail)
	}
}
