package participants

import (
	"context"
	"net/http"
	"net/url"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

// Reservations implements out.Reservations over the internal route of commercial-reservation (HU-COM-010 plan, PROPOSED).
type Reservations struct{ c *client }

var _ out.Reservations = (*Reservations)(nil)

func NewReservations(baseURL string, cfg Config) *Reservations {
	return &Reservations{newClient(baseURL, cfg)}
}

// RecordValidatedPayment is idempotent by state: repeating it for a payment already validated answers 200.
func (r *Reservations) RecordValidatedPayment(ctx context.Context, call out.Call, tenantID string, p saga.Payment) error {
	_, err := r.c.post(ctx, request{
		path: "/api/v1/commercial-reservation/internal/tenants/" + url.PathEscape(tenantID) + "/payment-proofs/" +
			url.PathEscape(p.PaymentID) + "/validated-payment",
		key:  call.Key,
		body: map[string]any{"approvedBy": p.ApprovedBy, "verifiedAmountCents": p.AmountCents},
		ok:   []int{http.StatusOK},
	})
	return err
}
