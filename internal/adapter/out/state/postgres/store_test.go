package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/adapter/out/state/postgres"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/adapter/out/state/postgres/pgtest"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ctx    = context.Background()
	t0     = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tenant = "00000000-0000-4000-8000-0000000000a1"
	person = "00000000-0000-4000-8000-0000000000b1"
)

func sagaID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func fresh(n int, key string, at time.Time) saga.Saga {
	return saga.Saga{ID: sagaID(n), IdempotencyKey: key, Status: saga.Running, TenantID: tenant, OnBehalfOf: person, CreatedAt: at, UpdatedAt: at,
		Payment: saga.Payment{ReservationID: "00000000-0000-4000-8000-0000000000c1", PaymentID: "00000000-0000-4000-8000-0000000000d1",
			Medium: saga.MediumCash, AmountCents: 150000, BusinessDate: "2026-11-10", Concept: "Abono", ApprovedBy: person}}
}

func newStore(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.Open(t)
	return postgres.NewStore(pool), pool
}

func mustCreate(t *testing.T, s *postgres.Store, sg saga.Saga) {
	t.Helper()
	if _, created, err := s.Create(ctx, sg); err != nil || !created {
		t.Fatalf("Create(%s) = created %v, err %v", sg.ID, created, err)
	}
}

// AC-WF2-05, AC-WF2-07: what a resume needs is stored with the saga and read back, the approver being the person who started it.
func TestI_Create_stores_the_payment_and_Get_reads_it_back(t *testing.T) {
	s, _ := newStore(t)
	want := fresh(1, "key-register-0001", t0)

	got, created, err := s.Create(ctx, want)
	if err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	if got.Status != saga.Running || got.Payment != want.Payment || got.OnBehalfOf != person || got.TenantID != tenant {
		t.Errorf("created = %+v", got)
	}
	read, err := s.Get(ctx, want.ID)
	if err != nil || read.Payment != want.Payment || read.OnBehalfOf != person || read.TenantID != tenant || len(read.Steps) != 0 {
		t.Errorf("Get = %+v, %v", read, err)
	}
}

// HU-COM-010 DP-11: the reason is kept with the saga so a resume can send it again; without one the column stays NULL and
// the saga reads back an empty reason.
func TestI_the_reason_is_stored_and_read_back_or_stays_empty(t *testing.T) {
	s, pool := newStore(t)
	with := fresh(1, "key-register-0001", t0)
	with.Payment.Reason = "Amount verified in the bank"
	mustCreate(t, s, with)
	mustCreate(t, s, fresh(2, "key-register-0002", t0))

	read, err := s.Get(ctx, with.ID)
	if err != nil || read.Payment.Reason != "Amount verified in the bank" || read.Payment != with.Payment {
		t.Errorf("Get = %+v, %v", read.Payment, err)
	}
	stale, err := s.ClaimStale(ctx, t0.Add(time.Hour), t0.Add(2*time.Hour))
	if err != nil || len(stale) != 2 || stale[0].Payment.Reason != "Amount verified in the bank" || stale[1].Payment.Reason != "" {
		t.Errorf("ClaimStale = %+v, %v", stale, err)
	}
	var nulls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_register_payment.register_payment_saga WHERE reason IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("rows without reason = %d, %v", nulls, err)
	}
}

// The database refuses a blank reason and one over 500 characters, whatever the code says.
func TestI_the_database_bounds_the_reason(t *testing.T) {
	_, pool := newStore(t)
	insert := func(n int, reason string) error {
		_, err := pool.Exec(ctx, `INSERT INTO workflow_register_payment.register_payment_saga (saga_id, idempotency_key, reason)
			VALUES ($1::text::uuid, $2, $3)`, sagaID(n), fmt.Sprintf("key-reason-%04d", n), reason)
		return err
	}
	for n, reason := range map[int]string{1: "", 2: "   ", 3: strings.Repeat("r", 501)} {
		var pg *pgconn.PgError
		if err := insert(n, reason); !errors.As(err, &pg) || pg.ConstraintName != "chk_register_payment_saga_reason" {
			t.Errorf("reason of %d characters: %v", len(reason), err)
		}
	}
	if err := insert(4, strings.Repeat("r", 500)); err != nil {
		t.Errorf("500 characters: %v", err)
	}
}

func TestI_Create_with_an_existing_key_returns_the_existing_saga_not_created(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, fresh(1, "key-register-0001", t0))

	got, created, err := s.Create(ctx, fresh(2, "key-register-0001", t0.Add(time.Hour)))

	if err != nil || created || got.ID != sagaID(1) {
		t.Errorf("created %v err %v saga %s", created, err, got.ID)
	}
}

func TestI_Create_under_concurrency_inserts_once(t *testing.T) {
	s, pool := newStore(t)
	var wg sync.WaitGroup
	createds := make([]bool, 16)
	for i := range createds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, createds[i], _ = s.Create(ctx, fresh(i+1, "key-register-0001", t0))
		}()
	}
	wg.Wait()
	winners := 0
	for _, c := range createds {
		if c {
			winners++
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_register_payment.register_payment_saga`).Scan(&rows); err != nil || rows != 1 || winners != 1 {
		t.Errorf("rows %d winners %d err %v", rows, winners, err)
	}
}

// AC-WF2-03: the movement of step 1, the steps and their compensation are saved and read back in order.
func TestI_Save_writes_the_movement_the_steps_and_the_failure(t *testing.T) {
	s, _ := newStore(t)
	sg := fresh(1, "key-register-0001", t0)
	mustCreate(t, s, sg)
	sg.CashMovementID = "00000000-0000-4000-8000-0000000000e1"
	sg.Record(saga.RecordCashIncome)
	sg.Record(saga.RecordValidatedPayment)
	sg.MarkCompensated(saga.RecordCashIncome)
	sg.End(saga.Compensated, saga.RecordValidatedPayment, "NO_AVAILABILITY: refused")
	sg.UpdatedAt = t0.Add(time.Second)

	if err := s.Save(ctx, &sg); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, sg.ID)

	if err != nil || got.Status != saga.Compensated || got.CashMovementID != sg.CashMovementID || got.FailedStep != saga.RecordValidatedPayment ||
		got.FailureDetail != "NO_AVAILABILITY: refused" || len(got.Steps) != 2 || got.Steps[0].Name != saga.RecordCashIncome || !got.Steps[0].Compensated || got.Steps[1].Compensated {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if sg.Version != 1 || got.Version != 1 {
		t.Errorf("versions %d %d", sg.Version, got.Version)
	}
}

// AC-WF2-07: a save from an older copy is refused, an ended saga never changes, an unknown one is not found.
func TestI_Save_refuses_an_older_copy_an_ended_saga_and_an_unknown_one(t *testing.T) {
	s, _ := newStore(t)
	sg := fresh(1, "key-register-0001", t0)
	mustCreate(t, s, sg)
	older := sg
	if err := s.Save(ctx, &sg); err != nil {
		t.Fatal(err)
	}

	if err := s.Save(ctx, &older); !errors.Is(err, saga.ErrConflict) {
		t.Errorf("older copy: %v", err)
	}
	sg.End(saga.Completed, "", "")
	if err := s.Save(ctx, &sg); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, &sg); !errors.Is(err, saga.ErrEnded) {
		t.Errorf("ended saga: %v", err)
	}
	ghost := fresh(9, "key-register-0009", t0)
	if err := s.Save(ctx, &ghost); !errors.Is(err, saga.ErrNotFound) {
		t.Errorf("unknown saga: %v", err)
	}
	if _, err := s.Get(ctx, "not-an-id"); !errors.Is(err, saga.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}

// AC-WF2-07: a stale RUNNING saga is claimed once, with its version raised, and an ended or a fresh one is not.
func TestI_ClaimStale_takes_each_stale_saga_once(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, fresh(1, "key-register-0001", t0))
	mustCreate(t, s, fresh(2, "key-register-0002", t0.Add(10*time.Minute)))
	done := fresh(3, "key-register-0003", t0)
	mustCreate(t, s, done)
	done.End(saga.Completed, "", "")
	if err := s.Save(ctx, &done); err != nil {
		t.Fatal(err)
	}

	first, err := s.ClaimStale(ctx, t0.Add(5*time.Minute), t0.Add(20*time.Minute))
	second, err2 := s.ClaimStale(ctx, t0.Add(5*time.Minute), t0.Add(21*time.Minute))

	if err != nil || err2 != nil || len(first) != 1 || first[0].ID != sagaID(1) || first[0].Version != 1 || first[0].Payment.AmountCents != 150000 {
		t.Fatalf("first %+v err %v", first, err)
	}
	if len(second) != 0 {
		t.Errorf("the saga was claimed twice: %+v", second)
	}
}

// What the service can do with the grants of db/03_dcl: write and read, never delete (the state is the audit).
func TestI_the_service_role_cannot_delete_a_saga(t *testing.T) {
	pool := pgtest.OpenAsApp(t)
	s := postgres.NewStore(pool)
	mustCreate(t, s, fresh(1, "key-register-0001", t0))

	_, err := pool.Exec(ctx, `DELETE FROM register_payment_saga`)

	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "42501" {
		t.Errorf("delete: %v", err)
	}
}
