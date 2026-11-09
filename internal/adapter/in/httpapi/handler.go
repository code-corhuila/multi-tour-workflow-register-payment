// Package httpapi is the entry of the register-payment saga: a Tenant Administrator or a Collaborator with the right starts
// it and reads its state. Every error uses the single envelope; the detail of a failure stays in the saga state and the
// log, and the answer names only the step that failed.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/correlation"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const (
	route         = "/api/v1/sagas/register-payment"
	minKeyLength  = 8
	maxKeyLength  = 128
	correlationHd = "X-Correlation-Id"
)

type handler struct {
	uc       in.RegisterPayment
	verifier *Verifier
	log      *slog.Logger
}

func New(uc in.RegisterPayment, verifier *Verifier, log *slog.Logger) http.Handler {
	h := &handler{uc, verifier, log}
	mux := http.NewServeMux()
	mux.HandleFunc(route, h.collection)
	mux.HandleFunc(route+"/{id}", h.item)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found")
	})
	return h.withCorrelation(h.recovering(mux))
}

// recovering turns a panic below it into the generic 500 envelope, logged under the trace id.
func (h *handler) recovering(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				h.log.Error("entry route panicked", "traceId", correlation.From(r.Context()), "panic", fmt.Sprint(recovered))
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withCorrelation reuses a well-formed X-Correlation-Id or generates one, returns it and puts it in the context.
func (h *handler) withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationHd)
		if !correlation.Valid(id) {
			id = correlation.NewID()
		}
		w.Header().Set(correlationHd, id)
		next.ServeHTTP(w, r.WithContext(correlation.With(r.Context(), id)))
	})
}

// authenticate answers 401 itself and reports whether the caller has a valid token with a tenant.
func (h *handler) authenticate(w http.ResponseWriter, r *http.Request) (Claims, bool) {
	scheme, raw, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || raw == "" {
		return Claims{}, deny(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	}
	claims, err := h.verifier.Verify(strings.TrimSpace(raw))
	if err != nil {
		h.log.Debug("token refused", "traceId", correlation.From(r.Context()), "reason", err.Error())
		return Claims{}, deny(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	}
	return claims, true
}

func deny(w http.ResponseWriter, r *http.Request, status int, code, message string) bool {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeError(w, r, status, code, message)
	return false
}

func (h *handler) collection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < minKeyLength || len(key) > maxKeyLength {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Idempotency-Key is required, 8 to 128 characters",
			fieldError{"Idempotency-Key", "must be 8 to 128 characters"})
		return
	}
	if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(mediaType) != "application/json" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Content-Type must be application/json")
		return
	}
	payment, problems := decodeStart(r, w)
	if problems != nil {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "the request is not valid", problems...)
		return
	}
	if !claims.mayRegister(payment.Medium) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "you may not register this payment")
		return
	}
	payment.ApprovedBy = claims.Subject

	started, created, err := h.uc.Start(r.Context(), in.StartCommand{IdempotencyKey: key, TenantID: claims.TenantID,
		OnBehalfOf: claims.Subject, Payment: payment})
	if err != nil {
		h.fail(w, r, "start the saga", err)
		return
	}
	if !created {
		writeJSON(w, http.StatusOK, toResponse(started))
		return
	}
	w.Header().Set("Location", route+"/"+started.ID)
	writeJSON(w, http.StatusCreated, toResponse(started))
}

func (h *handler) item(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) || claims.TenantID == "" {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "saga not found")
		return
	}
	found, err := h.uc.Get(r.Context(), id)
	switch {
	case errors.Is(err, saga.ErrNotFound), err == nil && found.TenantID != claims.TenantID:
		// a saga of another tenant is answered as an unknown one (ADR-003)
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "saga not found")
	case err != nil:
		h.fail(w, r, "read the saga", err)
	default:
		writeJSON(w, http.StatusOK, toResponse(found))
	}
}

// fail logs the cause under the trace id and answers a generic 500: no driver text, host or trace leaves.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("entry route failed", "traceId", correlation.From(r.Context()), "action", what, "error", err.Error())
	writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error")
}

type errorResponse struct {
	Error   string       `json:"error"`
	Message string       `json:"message"`
	Details []fieldError `json:"details,omitempty"`
	TraceID string       `json:"traceId"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details ...fieldError) {
	writeJSON(w, status, errorResponse{code, message, details, correlation.From(r.Context())})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// sagaResponse is the Saga of the contract. It has no field for the failure detail on purpose.
type sagaResponse struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	CompletedSteps []string  `json:"completedSteps"`
	FailedStep     string    `json:"failedStep,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func toResponse(s saga.Saga) sagaResponse {
	steps := make([]string, 0, len(s.Steps))
	for _, st := range s.Steps {
		steps = append(steps, string(st.Name))
	}
	return sagaResponse{ID: s.ID, Status: string(s.Status), CompletedSteps: steps, FailedStep: string(s.FailedStep),
		CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC()}
}
