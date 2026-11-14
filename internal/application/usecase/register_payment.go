// Package usecase is the orchestrator of the register-payment saga.
package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const (
	maxDetailRunes = 500
	reversalReason = "The payment could not be recorded in the reservation"
)

type RegisterPayment struct {
	store        out.Store
	cash         out.Cash
	reservations out.Reservations
	log          *slog.Logger
	newID        func() string
	now          func() time.Time
	bound        time.Duration // longest a started saga may run, independent of the caller
}

func NewRegisterPayment(store out.Store, cash out.Cash, reservations out.Reservations,
	log *slog.Logger, newID func() string, now func() time.Time, bound time.Duration) *RegisterPayment {
	return &RegisterPayment{store, cash, reservations, log, newID, now, bound}
}

var _ in.RegisterPayment = (*RegisterPayment)(nil)

// Start runs the saga to its end. The caller leaving must not cancel a compensation half way, so the saga runs on a
// context that keeps the caller's values but not its cancellation, bounded by r.bound.
func (r *RegisterPayment) Start(ctx context.Context, cmd in.StartCommand) (saga.Saga, bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.bound)
	defer cancel()
	now := r.now()
	// A payment is recorded once: another saga that holds it (another key) is refused before the cash is touched. The saga
	// of this same key is a replay and falls through to Create.
	if holder, found, err := r.store.ForPayment(ctx, cmd.TenantID, cmd.Payment.PaymentID); err != nil {
		return saga.Saga{}, false, err
	} else if found && holder.IdempotencyKey != cmd.IdempotencyKey {
		return saga.Saga{}, false, saga.ErrPaymentTaken
	}
	fresh := saga.Saga{ID: r.newID(), IdempotencyKey: cmd.IdempotencyKey, Status: saga.Running, TenantID: cmd.TenantID,
		OnBehalfOf: cmd.OnBehalfOf, Payment: cmd.Payment, CreatedAt: now, UpdatedAt: now}
	s, created, err := r.store.Create(ctx, fresh)
	if err != nil {
		return s, false, err
	}
	if !created {
		if !s.SameStart(cmd.TenantID, cmd.Payment) {
			// the key belongs to another tenant or to another body: nothing of that saga is handed over
			return saga.Saga{}, false, saga.ErrKeyReuse
		}
		return s, false, nil
	}
	return s, true, r.run(ctx, &s)
}

func (r *RegisterPayment) Get(ctx context.Context, id string) (saga.Saga, error) {
	return r.store.Get(ctx, id)
}

// Resume carries on the RUNNING sagas that have not advanced since before. Everything a step needs was saved with the
// saga, and every step call is idempotent by its key, so a resumed saga runs again from the first step not recorded.
//
// The stale sagas are claimed first, so only one actor takes each; a saga another actor moved meanwhile is left to
// that actor, and one that ended meanwhile is done (neither saga.ErrConflict nor saga.ErrEnded is a failure).
func (r *RegisterPayment) Resume(ctx context.Context, before time.Time) error {
	stale, err := r.store.ClaimStale(ctx, before, r.now())
	if err != nil {
		return err
	}
	var failures []error
	for i := range stale {
		err := r.run(ctx, &stale[i])
		if errors.Is(err, saga.ErrConflict) || errors.Is(err, saga.ErrEnded) {
			r.log.InfoContext(ctx, "saga moved or ended by another actor, left to it", r.fields(stale[i])...)
			continue
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// run executes the steps not yet recorded, in order, saving the state after each one. Compensation:
//
//	RECORD_CASH_INCOME        none: a failure ends FAILED, nothing was recorded
//	RECORD_VALIDATED_PAYMENT  a definitive rejection reverses the income; an unknown outcome ends FAILED, because the
//	                          payment may exist and a person has to decide
//
// Crash window: if the process stops after a participant answered and before the save that follows, the step is not
// recorded; the resume repeats it with the same key, which the participant answers as a replay.
func (r *RegisterPayment) run(ctx context.Context, s *saga.Saga) error {
	call := func(step saga.Step) out.Call { return out.Call{Key: s.StepKey(step), OnBehalfOf: s.OnBehalfOf} }

	if !s.Done(saga.RecordCashIncome) {
		movementID, err := r.cash.RecordIncome(ctx, call(saga.RecordCashIncome), s.TenantID, s.Payment)
		if err != nil {
			s.End(saga.Failed, saga.RecordCashIncome, sanitize(err))
			return r.finish(ctx, s)
		}
		s.CashMovementID = movementID
		s.Record(saga.RecordCashIncome)
		if err := r.save(ctx, s); err != nil {
			return err
		}
	}
	if !s.Done(saga.RecordValidatedPayment) {
		if err := r.reservations.RecordValidatedPayment(ctx, call(saga.RecordValidatedPayment), s.TenantID, s.Payment); err != nil {
			return r.abort(ctx, s, err)
		}
		s.Record(saga.RecordValidatedPayment)
	}
	s.End(saga.Completed, "", "")
	return r.finish(ctx, s)
}

// abort ends the saga after step 2 did not succeed. Only a definitive rejection is compensated; a compensation that
// fails also ends FAILED.
func (r *RegisterPayment) abort(ctx context.Context, s *saga.Saga, cause error) error {
	detail := sanitize(cause)
	if !definitive(cause) {
		s.End(saga.Failed, saga.RecordValidatedPayment, detail)
		return r.finish(ctx, s)
	}
	call := out.Call{Key: s.StepKey(saga.RecordCashIncome) + ":undo", OnBehalfOf: s.OnBehalfOf}
	if err := r.cash.ReverseIncome(ctx, call, s.TenantID, s.CashMovementID, reversalReason); err != nil {
		code := codeOf(err)
		r.log.WarnContext(ctx, "compensation failed", append(r.fields(*s), "step", saga.RecordCashIncome, "code", code)...)
		s.End(saga.Failed, saga.RecordValidatedPayment, detail+"; compensation of "+string(saga.RecordCashIncome)+" failed: "+code)
		return r.finish(ctx, s)
	}
	s.MarkCompensated(saga.RecordCashIncome)
	s.End(saga.Compensated, saga.RecordValidatedPayment, detail)
	return r.finish(ctx, s)
}

func (r *RegisterPayment) save(ctx context.Context, s *saga.Saga) error {
	s.UpdatedAt = r.now()
	return r.store.Save(ctx, s)
}

func (r *RegisterPayment) finish(ctx context.Context, s *saga.Saga) error {
	if err := r.save(ctx, s); err != nil {
		return err
	}
	r.log.InfoContext(ctx, "saga ended", append(r.fields(*s), "status", s.Status, "failedStep", s.FailedStep)...)
	return nil
}

// fields are the attributes every log line of a saga carries: tenant and saga. The correlation id is added by the log handler
// from the context (correlation.LogHandler), so the core stays free of the adapter (AC-WF2-08).
func (r *RegisterPayment) fields(s saga.Saga) []any {
	return []any{"tenantId", s.TenantID, "sagaId", s.ID}
}

func definitive(err error) bool {
	var participant *saga.ParticipantError
	return errors.As(err, &participant) && participant.Definitive
}

func codeOf(err error) string {
	var participant *saga.ParticipantError
	if errors.As(err, &participant) {
		return participant.Code
	}
	return "UNKNOWN"
}

// sanitize keeps a participant's code and message and drops any other error, whose text can carry connection strings
// or tokens.
func sanitize(err error) string {
	var participant *saga.ParticipantError
	detail := "PARTICIPANT_ERROR: the participant call failed"
	if errors.As(err, &participant) {
		detail = participant.Error()
	}
	if r := []rune(detail); len(r) > maxDetailRunes {
		detail = string(r[:maxDetailRunes])
	}
	return detail
}
