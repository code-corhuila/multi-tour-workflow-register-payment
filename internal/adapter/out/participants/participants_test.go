package participants_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/adapter/out/participants"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/correlation"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const (
	token  = "s3cret-service-token"
	tenant = "11111111-1111-4111-8111-111111111111"
	person = "22222222-2222-4222-8222-222222222222"
)

var cfg = participants.Config{Token: token, Timeout: time.Second, Attempts: 3}

// stub stands for a participant: the routes of the cash api (docs #840) and of the commercial api (HU-COM-010 plan) may not be
// on their develop yet. It answers the scripted statuses in order and remembers the last call.
type stub struct {
	statuses []int
	body     string
	calls    atomic.Int32
	method   string
	path     string
	raw      string
	header   http.Header
	sent     map[string]any
}

func newStub(t *testing.T, body string, statuses ...int) (*httptest.Server, *stub) {
	t.Helper()
	f := &stub{statuses: statuses, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1))
		raw, _ := io.ReadAll(r.Body)
		f.sent = map[string]any{}
		_ = json.Unmarshal(raw, &f.sent)
		f.method, f.path, f.raw, f.header = r.Method, r.URL.Path, r.URL.EscapedPath(), r.Header.Clone()
		status := http.StatusOK
		if n <= len(f.statuses) {
			status = f.statuses[n-1]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status < 300 {
			_, _ = w.Write([]byte(f.body))
			return
		}
		_, _ = w.Write([]byte(`{"error":"NO_OPEN_CASH","message":"The cash is not open","traceId":"t"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func ctx() context.Context { return correlation.With(context.Background(), "run-1234") }

func payment() saga.Payment {
	return saga.Payment{ReservationID: "r-1", PaymentID: "p-1", Medium: saga.MediumTransfer, AmountCents: 150000,
		BusinessDate: "2026-11-10", Concept: "Abono", ApprovedBy: person}
}

var call = out.Call{Key: "saga-1:RECORD_CASH_INCOME", OnBehalfOf: person}

// AC-WF2-01, -05, -06: the income goes to the internal route of the tenant, with the key, the credential, the correlation id
// and onBehalfOf in the body; the id of the movement comes back.
func TestI_the_income_is_posted_to_the_internal_route(t *testing.T) {
	srv, stub := newStub(t, `{"cashMovementId":"mov-1"}`, http.StatusCreated)

	id, err := participants.NewCash(srv.URL, cfg).RecordIncome(ctx(), call, tenant, payment())

	if err != nil || id != "mov-1" {
		t.Fatalf("id %q err %v", id, err)
	}
	if stub.method != http.MethodPost || stub.path != "/api/v1/cash-reporting/internal/tenants/"+tenant+"/cash-movements" {
		t.Errorf("asked %s %s", stub.method, stub.path)
	}
	if stub.header.Get("Authorization") != "Bearer "+token || stub.header.Get("Idempotency-Key") != call.Key ||
		stub.header.Get("X-Correlation-Id") != "run-1234" {
		t.Errorf("headers = %v", stub.header)
	}
	want := map[string]any{"businessDate": "2026-11-10", "paymentMedium": "TRANSFER", "amountCents": float64(150000),
		"reservationId": "r-1", "concept": "Abono", "onBehalfOf": person}
	for k, v := range want {
		if stub.sent[k] != v {
			t.Errorf("body %s = %v, want %v", k, stub.sent[k], v)
		}
	}
	if len(stub.sent) != len(want) {
		t.Errorf("body = %v", stub.sent)
	}
}

// AC-WF2-02: a 4xx is a definitive rejection with the code of the cash, without the text of any other body.
func TestI_a_4xx_is_a_definitive_rejection(t *testing.T) {
	srv, stub := newStub(t, `{}`, http.StatusUnprocessableEntity)

	_, err := participants.NewCash(srv.URL, cfg).RecordIncome(ctx(), call, tenant, payment())

	var p *saga.ParticipantError
	if !errors.As(err, &p) || !p.Definitive || p.Code != "NO_OPEN_CASH" || stub.calls.Load() != 1 {
		t.Errorf("err %v calls %d", err, stub.calls.Load())
	}
}

// AC-WF2-04: network errors, 429 and 5xx are retried a bounded number of times and then are an unknown outcome.
func TestI_5xx_is_retried_and_then_unknown(t *testing.T) {
	srv, stub := newStub(t, `{"cashMovementId":"mov-1"}`, 503, 429, 201)
	if id, err := participants.NewCash(srv.URL, cfg).RecordIncome(ctx(), call, tenant, payment()); err != nil || id != "mov-1" || stub.calls.Load() != 3 {
		t.Errorf("id %q err %v calls %d", id, err, stub.calls.Load())
	}

	srv, stub = newStub(t, `{}`, 503, 503, 503)
	_, err := participants.NewCash(srv.URL, cfg).RecordIncome(ctx(), call, tenant, payment())
	var p *saga.ParticipantError
	if !errors.As(err, &p) || p.Definitive || stub.calls.Load() != 3 || strings.Contains(err.Error(), token) {
		t.Errorf("err %v calls %d", err, stub.calls.Load())
	}
}

// A 2xx without the id of the movement is an unknown outcome: the compensation could not name what to reverse.
func TestI_an_answer_without_the_movement_id_is_unknown(t *testing.T) {
	srv, _ := newStub(t, `{}`, http.StatusCreated)

	_, err := participants.NewCash(srv.URL, cfg).RecordIncome(ctx(), call, tenant, payment())

	var p *saga.ParticipantError
	if !errors.As(err, &p) || p.Definitive {
		t.Errorf("err %v", err)
	}
}

// AC-WF2-03: the reversal names the movement in the path and sends the reason and onBehalfOf.
func TestI_the_reversal_is_posted_to_the_movement(t *testing.T) {
	srv, stub := newStub(t, `{"cashMovementId":"mov-2"}`, http.StatusCreated)

	err := participants.NewCash(srv.URL, cfg).ReverseIncome(ctx(), out.Call{Key: "saga-1:RECORD_CASH_INCOME:undo", OnBehalfOf: person}, tenant, "mov-1", "why")

	if err != nil || stub.path != "/api/v1/cash-reporting/internal/tenants/"+tenant+"/cash-movements/mov-1/reversals" {
		t.Fatalf("err %v path %s", err, stub.path)
	}
	if stub.sent["reason"] != "why" || stub.sent["onBehalfOf"] != person || stub.header.Get("Idempotency-Key") != "saga-1:RECORD_CASH_INCOME:undo" {
		t.Errorf("sent %v headers %v", stub.sent, stub.header)
	}
}

// AC-WF2-01: step 2 posts approvedBy and the verified amount to the payment of the tenant.
func TestI_the_validated_payment_is_posted_to_the_payment(t *testing.T) {
	srv, stub := newStub(t, `{}`, http.StatusOK)

	err := participants.NewReservations(srv.URL, cfg).RecordValidatedPayment(ctx(), out.Call{Key: "saga-1:RECORD_VALIDATED_PAYMENT", OnBehalfOf: person}, tenant, payment())

	if err != nil || stub.path != "/api/v1/commercial-reservation/internal/tenants/"+tenant+"/payment-proofs/p-1/validated-payment" {
		t.Fatalf("err %v path %s", err, stub.path)
	}
	if stub.sent["approvedBy"] != person || stub.sent["verifiedAmountCents"] != float64(150000) || len(stub.sent) != 2 {
		t.Errorf("sent %v", stub.sent)
	}
}

// HU-COM-010 DP-11: the reason of the approval goes in the body of step 2 under the name the commercial route reads
// (ValidatedPayment.reason); without one the body keeps its two fields.
func TestI_the_validated_payment_carries_the_reason_only_when_there_is_one(t *testing.T) {
	srv, stub := newStub(t, `{}`, http.StatusOK)
	p := payment()
	p.Reason = "Amount verified in the bank"

	if err := participants.NewReservations(srv.URL, cfg).RecordValidatedPayment(ctx(), out.Call{Key: "k"}, tenant, p); err != nil {
		t.Fatal(err)
	}
	if stub.sent["reason"] != "Amount verified in the bank" || stub.sent["approvedBy"] != person || stub.sent["verifiedAmountCents"] != float64(150000) || len(stub.sent) != 3 {
		t.Errorf("sent %v", stub.sent)
	}
	p.Reason = ""
	if err := participants.NewReservations(srv.URL, cfg).RecordValidatedPayment(ctx(), out.Call{Key: "k"}, tenant, p); err != nil {
		t.Fatal(err)
	}
	if _, has := stub.sent["reason"]; has || len(stub.sent) != 2 {
		t.Errorf("sent %v", stub.sent)
	}
}

// An id of the path that is not a plain id cannot change the route.
func TestI_the_ids_of_the_path_are_escaped(t *testing.T) {
	srv, stub := newStub(t, `{}`, http.StatusOK)
	p := payment()
	p.PaymentID = "../x"

	_ = participants.NewReservations(srv.URL, cfg).RecordValidatedPayment(ctx(), call, tenant, p)

	if strings.Contains(stub.raw, "/../") || !strings.Contains(stub.raw, "%2F") {
		t.Errorf("path = %s", stub.raw)
	}
}

// A redirect is an unknown outcome and is never followed: it would replay the body and the credential elsewhere.
func TestI_a_redirect_is_not_followed(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	err := participants.NewReservations(srv.URL, cfg).RecordValidatedPayment(ctx(), call, tenant, payment())

	var p *saga.ParticipantError
	if !errors.As(err, &p) || p.Definitive || hits.Load() != 0 {
		t.Errorf("err %v hits %d", err, hits.Load())
	}
}
