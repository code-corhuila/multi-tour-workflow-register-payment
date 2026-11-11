// Package out holds the ports the orchestrator needs: one per participant, and the saga store.
package out

import (
	"context"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

// Call is what every participant call carries besides its data.
type Call struct {
	Key        string // Idempotency-Key: <saga>:<step>
	OnBehalfOf string // the `sub` of the person who started the saga (ADR-004 D5)
}

// Cash is the participant of step 1 and of its compensation (cash-reporting, internal routes). An error that is not a
// *saga.ParticipantError is treated as an unknown failure.
type Cash interface {
	RecordIncome(ctx context.Context, call Call, tenantID string, p saga.Payment) (movementID string, err error)
	// ReverseIncome records a compensating movement; the income itself is never updated or deleted.
	ReverseIncome(ctx context.Context, call Call, tenantID, movementID, reason string) error
}

// Reservations is the participant of step 2 (commercial-reservation, internal route). It has no compensation.
type Reservations interface {
	RecordValidatedPayment(ctx context.Context, call Call, tenantID string, p saga.Payment) error
}

// Store persists the saga. Create inserts a RUNNING saga or, when the idempotency key exists, returns that one
// with created=false.
type Store interface {
	Create(ctx context.Context, s saga.Saga) (stored saga.Saga, created bool, err error)
	// Save writes the saga if it is still the stored copy (same Version) and raises s.Version; otherwise it answers
	// saga.ErrConflict (moved by another actor), saga.ErrEnded or saga.ErrNotFound.
	Save(ctx context.Context, s *saga.Saga) error
	Get(ctx context.Context, id string) (saga.Saga, error)
	// ClaimStale takes the RUNNING sagas not updated since before: it stamps them with now and raises their Version in
	// one step, so no other caller takes the same saga, and returns them as taken.
	ClaimStale(ctx context.Context, before, now time.Time) ([]saga.Saga, error)
}
