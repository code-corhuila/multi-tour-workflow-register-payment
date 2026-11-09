// Package in holds the use case the entry adapter calls.
package in

import (
	"context"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

// StartCommand is what the entry route hands over: the tenant and the person come from the token, never from the body.
type StartCommand struct {
	IdempotencyKey string
	TenantID       string
	OnBehalfOf     string // the `sub` of the person who started the saga
	Payment        saga.Payment
}

type RegisterPayment interface {
	// Start runs the saga; created is false when the key already started one (nothing runs again).
	Start(ctx context.Context, cmd StartCommand) (s saga.Saga, created bool, err error)
	Get(ctx context.Context, id string) (saga.Saga, error)
}
