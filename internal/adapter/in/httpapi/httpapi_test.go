package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/adapter/in/httpapi"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/in"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const (
	route    = "/api/v1/sagas/register-payment"
	sagaID   = "33333333-3333-4333-8333-333333333333"
	tenantID = "11111111-1111-4111-8111-111111111111"
	otherID  = "99999999-9999-4999-8999-999999999999"
	person   = "22222222-2222-4222-8222-222222222222"
	goodKey  = "key-0001-abcdef"
	goodBody = `{"reservationId":"44444444-4444-4444-8444-444444444444","paymentId":"55555555-5555-4555-8555-555555555555",` +
		`"paymentMedium":"TRANSFER","amountCents":150000,"businessDate":"2026-11-10","concept":"Abono"}`
)

var signer = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

func token(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func claims(mutate func(jwt.MapClaims)) jwt.MapClaims {
	c := jwt.MapClaims{"sub": person, "tenantId": tenantID, "roles": []string{"TENANT_ADMINISTRATOR"}, "exp": time.Now().Add(time.Hour).Unix()}
	if mutate != nil {
		mutate(c)
	}
	return c
}

type fakeUseCase struct {
	started  saga.Saga
	created  bool
	startErr error
	cmd      in.StartCommand
	found    saga.Saga
	getErr   error
	panics   bool
}

func (f *fakeUseCase) Start(_ context.Context, cmd in.StartCommand) (saga.Saga, bool, error) {
	if f.panics {
		panic("boom postgres://user:secret@db")
	}
	f.cmd = cmd
	return f.started, f.created, f.startErr
}

func (f *fakeUseCase) Get(context.Context, string) (saga.Saga, error) { return f.found, f.getErr }

func completed() saga.Saga {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return saga.Saga{ID: sagaID, Status: saga.Completed, TenantID: tenantID, CreatedAt: at, UpdatedAt: at, FailureDetail: "SECRET-DETAIL",
		Steps: []saga.StepState{{Name: saga.RecordCashIncome}, {Name: saga.RecordValidatedPayment}}}
}

func server(uc *fakeUseCase) http.Handler {
	return httpapi.New(uc, httpapi.NewVerifier(&signer.PublicKey), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
}

func post(h http.Handler, bearer, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, bearer, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, route+"/"+id, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error, TraceID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.TraceID == "" {
		t.Fatalf("body %q is not the error envelope: %v", rec.Body.String(), err)
	}
	return body.Error
}

// AC-WF2-01, -06, -09: the tenant and the person come from the token, never from the body; 201 with Location.
func TestA_an_administrator_starts_the_saga_with_the_tenant_and_the_person_of_the_token(t *testing.T) {
	uc := &fakeUseCase{started: completed(), created: true}

	rec := post(server(uc), token(t, signer, claims(nil)), goodKey, goodBody)

	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != route+"/"+sagaID {
		t.Fatalf("status %d location %q body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	c := uc.cmd
	if c.TenantID != tenantID || c.OnBehalfOf != person || c.IdempotencyKey != goodKey || c.Payment.ApprovedBy != person ||
		c.Payment.AmountCents != 150000 || c.Payment.Medium != saga.MediumTransfer || c.Payment.BusinessDate != "2026-11-10" {
		t.Errorf("command = %+v", c)
	}
}

// AC-WF2-05: the same key answers 200 with the saga of the first start.
func TestA_a_repeated_key_answers_200(t *testing.T) {
	uc := &fakeUseCase{started: completed(), created: false}

	rec := post(server(uc), token(t, signer, claims(nil)), goodKey, goodBody)

	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Errorf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// AC-WF2-08: the answer names the steps and the failed step, and has no field for the detail of a failure.
func TestA_the_answer_has_no_failure_detail(t *testing.T) {
	failed := completed()
	failed.Status, failed.FailedStep = saga.Failed, saga.RecordCashIncome
	rec := post(server(&fakeUseCase{started: failed, created: true}), token(t, signer, claims(nil)), goodKey, goodBody)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "SECRET-DETAIL") || body["failedStep"] != "RECORD_CASH_INCOME" || body["status"] != "FAILED" {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestA_no_token_an_expired_token_and_another_algorithm_are_401(t *testing.T) {
	h := server(&fakeUseCase{})
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	for name, bearer := range map[string]string{
		"none":       "",
		"expired":    token(t, signer, claims(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })),
		"wrong key":  token(t, other, claims(nil)),
		"no subject": token(t, signer, claims(func(c jwt.MapClaims) { delete(c, "sub") })),
	} {
		if rec := post(h, bearer, goodKey, goodBody); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "UNAUTHORIZED" {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
}

// A token with no tenant (a service or a platform account) or for a customer cannot start it.
func TestA_a_token_without_tenant_or_with_the_wrong_role_is_403(t *testing.T) {
	h := server(&fakeUseCase{})
	for name, mutate := range map[string]func(jwt.MapClaims){
		"no tenant":  func(c jwt.MapClaims) { delete(c, "tenantId") },
		"customer":   func(c jwt.MapClaims) { c["roles"] = []string{"FINAL_CUSTOMER"} },
		"service":    func(c jwt.MapClaims) { c["roles"] = []string{"SERVICE"} },
		"restricted": func(c jwt.MapClaims) { c["access"] = "restricted" },
	} {
		if rec := post(h, token(t, signer, claims(mutate)), goodKey, goodBody); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
}

// A collaborator needs the permission of the medium: the saga uses a service credential, so the entry is where it is checked.
func TestA_a_collaborator_needs_the_permission_of_the_medium(t *testing.T) {
	uc := &fakeUseCase{started: completed(), created: true}
	h := server(uc)
	collaborator := func(permission string) string {
		return token(t, signer, claims(func(c jwt.MapClaims) {
			c["roles"], c["permissions"] = []string{"OPERATIONAL_COLLABORATOR"}, []string{permission}
		}))
	}
	cashBody := strings.Replace(goodBody, "TRANSFER", "CASH", 1)

	for name, c := range map[string]struct {
		bearer, body string
		want         int
	}{
		"transfer with validate": {collaborator("VALIDATE_PAYMENT_PROOFS"), goodBody, http.StatusCreated},
		"transfer with cash":     {collaborator("MANAGE_CASH"), goodBody, http.StatusForbidden},
		"cash with manage cash":  {collaborator("MANAGE_CASH"), cashBody, http.StatusCreated},
		"cash with validate":     {collaborator("VALIDATE_PAYMENT_PROOFS"), cashBody, http.StatusForbidden},
	} {
		if rec := post(h, c.bearer, goodKey, c.body); rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, c.want)
		}
	}
}

// AC-WF2-05: the key and the body are validated; a field the contract does not declare (the tenant, for one) is refused.
func TestA_the_key_and_the_body_are_validated(t *testing.T) {
	h := server(&fakeUseCase{started: completed(), created: true})
	bearer := token(t, signer, claims(nil))
	bodies := map[string]string{
		"tenant in the body": strings.Replace(goodBody, `"concept"`, `"tenantId":"`+otherID+`","concept"`, 1),
		"approver in body":   strings.Replace(goodBody, `"concept"`, `"approvedBy":"x","concept"`, 1),
		"bad medium":         strings.Replace(goodBody, "TRANSFER", "CHEQUE", 1),
		"zero amount":        strings.Replace(goodBody, "150000", "0", 1),
		"decimal amount":     strings.Replace(goodBody, "150000", "1500.5", 1),
		"bad date":           strings.Replace(goodBody, "2026-11-10", "10/11/2026", 1),
		"bad reservation id": strings.Replace(goodBody, "44444444-4444-4444-8444-444444444444", "r-1", 1),
		"empty concept":      strings.Replace(goodBody, `"Abono"`, `""`, 1),
		"two objects":        goodBody + goodBody,
	}
	for name, body := range bodies {
		if rec := post(h, bearer, goodKey, body); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "VALIDATION_ERROR" {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := post(h, bearer, "short", goodBody); rec.Code != http.StatusBadRequest {
		t.Errorf("short key: status %d", rec.Code)
	}
}

// AC-WF2-09: another tenant gets 404, as an unknown id does.
func TestA_a_saga_of_another_tenant_is_404(t *testing.T) {
	own := completed()
	foreign := completed()
	foreign.TenantID = otherID
	bearer := token(t, signer, claims(nil))

	if rec := get(server(&fakeUseCase{found: own}), bearer, sagaID); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"completedSteps":["RECORD_CASH_INCOME","RECORD_VALIDATED_PAYMENT"]`) {
		t.Errorf("own: status %d body %s", rec.Code, rec.Body)
	}
	if rec := get(server(&fakeUseCase{found: foreign}), bearer, sagaID); rec.Code != http.StatusNotFound {
		t.Errorf("foreign: status %d", rec.Code)
	}
	if rec := get(server(&fakeUseCase{getErr: saga.ErrNotFound}), bearer, sagaID); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: status %d", rec.Code)
	}
	if rec := get(server(&fakeUseCase{}), bearer, "not-an-id"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
}

// AC-WF2-08: a failure or a panic answers the generic 500 with the trace id, and no cause leaves.
func TestA_a_failure_and_a_panic_answer_a_generic_500(t *testing.T) {
	bearer := token(t, signer, claims(nil))
	for name, uc := range map[string]*fakeUseCase{
		"error": {startErr: errors.New("dial postgres://user:secret@db")},
		"panic": {panics: true},
	} {
		rec := post(server(uc), bearer, goodKey, goodBody)
		if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "INTERNAL_ERROR" || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body)
		}
	}
}

func TestA_the_correlation_id_is_reused_or_made_and_health_needs_no_token(t *testing.T) {
	h := server(&fakeUseCase{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Correlation-Id", "run-1234")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Correlation-Id") != "run-1234" {
		t.Errorf("status %d id %q", rec.Code, rec.Header().Get("X-Correlation-Id"))
	}
	rec = get(h, "", sagaID)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("X-Correlation-Id") == "" {
		t.Errorf("status %d id %q", rec.Code, rec.Header().Get("X-Correlation-Id"))
	}
}
