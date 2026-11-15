package participants

import (
	"context"
	"encoding/json"
	"errors"
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

var _ out.ValidatedPaymentLookup = (*Reservations)(nil)

// FindValidatedPayment reads whether the payment is validated (getValidatedPayment, docs #1039; G-WF2-7 of HU-WFL-002). A 200
// with the stored result means validated. Only a 404 with the NOT_FOUND code of the commercial api means "not validated"; any
// other refusal, a 200 without the validated value or a read that keeps failing is no answer, so the saga never reverses an
// income whose payment may be recorded.
func (r *Reservations) FindValidatedPayment(ctx context.Context, tenantID, paymentID string) (bool, error) {
	body, err := r.c.get(ctx, request{
		path: "/api/v1/commercial-reservation/internal/tenants/" + url.PathEscape(tenantID) + "/payment-proofs/" +
			url.PathEscape(paymentID) + "/validated-payment",
		ok: []int{http.StatusOK},
	})
	var refused *saga.ParticipantError
	if errors.As(err, &refused) && refused.Definitive && refused.Code == "NOT_FOUND" {
		return false, nil
	}
	if err == nil {
		var result struct {
			ValidatedValueCents *int64 `json:"validatedValueCents"`
		}
		if err = json.Unmarshal(body, &result); err == nil && result.ValidatedValueCents != nil {
			return true, nil
		}
	}
	return false, unknown("PAYMENT_LOOKUP_FAILED", "the read of the validated payment gave no answer: "+codeOf(err))
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
