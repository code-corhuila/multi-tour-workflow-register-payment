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
	if problems != nil {
		return saga.Payment{}, problems
	}
	return saga.Payment{ReservationID: req.ReservationID, PaymentID: req.PaymentID, Medium: saga.Medium(req.PaymentMedium),
		AmountCents: amount, BusinessDate: req.BusinessDate, Concept: req.Concept}, nil
}
