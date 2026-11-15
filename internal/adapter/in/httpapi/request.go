package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const maxBodyBytes = 16 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// startRequest has no field for the tenant or the approver: both come from the token (ADR-003, ADR-004 D5).
// amountCents is a number kept raw, so a decimal or a string is refused instead of rounded.
type startRequest struct {
	ReservationID string          `json:"reservationId"`
	PaymentID     string          `json:"paymentId"`
	PaymentMedium string          `json:"paymentMedium"`
	AmountCents   json.RawMessage `json:"amountCents"`
	BusinessDate  string          `json:"businessDate"`
	Concept       string          `json:"concept"`
	// Reason is optional: why the verified amount differs from the one reported. Kept raw so a null or a number is refused.
	Reason json.RawMessage `json:"reason"`
}

type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// decodeStart reads the body strictly: one JSON object, no field the contract does not declare. The messages name the
// field and the rule, never the value.
func decodeStart(r *http.Request, w http.ResponseWriter) (saga.Payment, []fieldError) {
	var req startRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return saga.Payment{}, []fieldError{{"body", "must be one JSON object with only the declared fields"}}
	}
	var problems []fieldError
	check := func(ok bool, field, message string) {
		if !ok {
			problems = append(problems, fieldError{field, message})
		}
	}
	var amount int64
	amountErr := json.Unmarshal(req.AmountCents, &amount)
	_, dateErr := time.Parse(time.DateOnly, req.BusinessDate)
	check(uuidPattern.MatchString(req.ReservationID), "reservationId", "must be a lower case UUID")
	check(uuidPattern.MatchString(req.PaymentID), "paymentId", "must be a lower case UUID")
	check(req.PaymentMedium == string(saga.MediumCash) || req.PaymentMedium == string(saga.MediumTransfer), "paymentMedium", "must be CASH or TRANSFER")
	check(amountErr == nil && amount >= 1, "amountCents", "must be a whole number of cents, at least 1")
	check(dateErr == nil, "businessDate", "must be a date, YYYY-MM-DD")
	n := utf8.RuneCountInString(req.Concept)
	check(n >= 1 && n <= 200 && strings.TrimSpace(req.Concept) != "", "concept", "must be 1 to 200 characters")
	reason, reasonOK := decodeReason(req.Reason)
	check(reasonOK, "reason", "when present, must be 1 to 500 characters")
	if problems != nil {
		return saga.Payment{}, problems
	}
	return saga.Payment{ReservationID: req.ReservationID, PaymentID: req.PaymentID, Medium: saga.Medium(req.PaymentMedium),
		AmountCents: amount, BusinessDate: req.BusinessDate, Concept: req.Concept, Reason: reason}, nil
}

// decodeReason accepts an absent reason (empty result) or a string of 1 to 500 characters after trimming; anything else,
// null and a blank included, is refused. The trimmed text is what travels, as the commercial route compares it.
func decodeReason(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || string(raw) == "null" {
		return "", false
	}
	text = strings.TrimSpace(text)
	n := utf8.RuneCountInString(text)
	return text, n >= 1 && n <= 500
}
