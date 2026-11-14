package participants

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const cashPrefix = "/api/v1/cash-reporting/internal/tenants/"

// Cash implements out.Cash over the internal routes of cash-reporting (contract of docs #840). The tenant is the one of the
// path; onBehalfOf travels in the body, where the contract accepts it from a service token only (ADR-004 D5).
type Cash struct{ c *client }

var _ out.Cash = (*Cash)(nil)

func NewCash(baseURL string, cfg Config) *Cash { return &Cash{newClient(baseURL, cfg)} }

// RecordIncome returns the id of the movement that the compensation will name.
func (c *Cash) RecordIncome(ctx context.Context, call out.Call, tenantID string, p saga.Payment) (string, error) {
	body, err := c.c.post(ctx, request{
		path: cashPrefix + url.PathEscape(tenantID) + "/cash-movements",
		key:  call.Key,
		body: map[string]any{"businessDate": p.BusinessDate, "paymentMedium": string(p.Medium), "amountCents": p.AmountCents,
			"reservationId": p.ReservationID, "paymentId": p.PaymentID, "concept": p.Concept, "onBehalfOf": call.OnBehalfOf},
		ok: []int{http.StatusCreated, http.StatusOK},
	})
	if err != nil {
		return "", err
	}
	return decode(body, "cashMovementId")
}

var _ out.CashIncomeLookup = (*Cash)(nil)

// FindIncome reads the income of a payment (getInternalCashIncomeOfPayment, docs #1035; G-WF2-6 of HU-WFL-002). Only a 404
// with the NOT_FOUND code of the cash means "no income"; any other refusal, a 200 without the movement id or a read that
// keeps failing is no answer, so the saga never frees a payment whose income may exist.
func (c *Cash) FindIncome(ctx context.Context, tenantID, paymentID string) (string, bool, error) {
	body, err := c.c.get(ctx, request{
		path: cashPrefix + url.PathEscape(tenantID) + "/payments/" + url.PathEscape(paymentID) + "/cash-income",
		ok:   []int{http.StatusOK},
	})
	var refused *saga.ParticipantError
	if errors.As(err, &refused) && refused.Definitive && refused.Code == "NOT_FOUND" {
		return "", false, nil
	}
	if err == nil {
		var id string
		if id, err = decode(body, "cashMovementId"); err == nil {
			return id, true, nil
		}
	}
	return "", false, unknown("INCOME_LOOKUP_FAILED", "the read of the income gave no answer: "+codeOf(err))
}

func codeOf(err error) string {
	var participant *saga.ParticipantError
	if errors.As(err, &participant) {
		return participant.Code
	}
	return "UNKNOWN"
}

// ReverseIncome records the compensating movement; the income is never updated or deleted.
func (c *Cash) ReverseIncome(ctx context.Context, call out.Call, tenantID, movementID, reason string) error {
	_, err := c.c.post(ctx, request{
		path: cashPrefix + url.PathEscape(tenantID) + "/cash-movements/" + url.PathEscape(movementID) + "/reversals",
		key:  call.Key,
		body: map[string]string{"reason": reason, "onBehalfOf": call.OnBehalfOf},
		ok:   []int{http.StatusCreated, http.StatusOK},
	})
	return err
}
