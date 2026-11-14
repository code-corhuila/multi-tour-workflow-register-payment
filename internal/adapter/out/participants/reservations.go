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

// NoValidatedPaymentLookup stands in for the read of a validated payment, which the commercial api does not serve yet
// (G-WF2-7 of HU-WFL-002, docs #1039, PROPOSED). It always gives no answer, so an unclear step 2 ends FAILED for a person
// and is never reversed on a guess. It is replaced by the real adapter when the route is in the commercial api.
type NoValidatedPaymentLookup struct{}

var _ out.ValidatedPaymentLookup = NoValidatedPaymentLookup{}

func (NoValidatedPaymentLookup) FindValidatedPayment(context.Context, string, string) (bool, error) {
	return false, unknown("PAYMENT_LOOKUP_UNAVAILABLE", "the commercial api has no read of a validated payment yet")
}

// RecordValidatedPayment is idempotent by state: repeating it for a payment already validated answers 200.
func (r *Reservations) RecordValidatedPayment(ctx context.Context, call out.Call, tenantID string, p saga.Payment) error {
	body := map[string]any{"approvedBy": p.ApprovedBy, "verifiedAmountCents": p.AmountCents}
	if p.Reason != "" {
		body["reason"] = p.Reason // ValidatedPayment.reason of the commercial route; required there when the amount differs (DP-06)
	}
	_, err := r.c.post(ctx, request{
		path: "/api/v1/commercial-reservation/internal/tenants/" + url.PathEscape(tenantID) + "/payment-proofs/" +
			url.PathEscape(p.PaymentID) + "/validated-payment",
		key:  call.Key,
		body: body,
		ok:   []int{http.StatusOK},
	})
	return err
}
