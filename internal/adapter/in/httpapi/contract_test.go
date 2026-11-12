package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/golang-jwt/jwt/v5"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

// Every answer is validated against the published contract, and the last check fails when a response the contract declares
// was never produced: the gate toward qa (ADR-004 D7).
func TestC_EveryResponseOfTheContractIsProducedAndValid(t *testing.T) {
	ctx := context.Background()
	doc, err := openapi3.NewLoader().LoadFromFile("../../../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("the contract is not valid: %v", err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	admin := token(t, signer, claims(nil))
	customer := token(t, signer, claims(func(c jwt.MapClaims) { c["roles"] = []string{"FINAL_CUSTOMER"} }))
	okUC := func(created bool) *fakeUseCase {
		return &fakeUseCase{started: completed(), created: created, found: completed()}
	}

	type scenario struct {
		name string
		do   func(h http.Handler) *httptest.ResponseRecorder
		uc   *fakeUseCase
		want int
	}
	startWith := func(bearer, key, body string) func(http.Handler) *httptest.ResponseRecorder {
		return func(h http.Handler) *httptest.ResponseRecorder { return post(h, bearer, key, body) }
	}
	readWith := func(bearer string) func(http.Handler) *httptest.ResponseRecorder {
		return func(h http.Handler) *httptest.ResponseRecorder { return get(h, bearer, sagaID) }
	}
	scenarios := []scenario{
		{"created", startWith(admin, goodKey, goodBody), okUC(true), http.StatusCreated},
		{"repeated key", startWith(admin, goodKey, goodBody), okUC(false), http.StatusOK},
		{"no token", startWith("", goodKey, goodBody), okUC(true), http.StatusUnauthorized},
		{"not allowed", startWith(customer, goodKey, goodBody), okUC(true), http.StatusForbidden},
		{"no key", startWith(admin, "", goodBody), okUC(true), http.StatusBadRequest},
		{"unknown field", startWith(admin, goodKey, strings.Replace(goodBody, `"concept"`, `"extra":1,"concept"`, 1)), okUC(true), http.StatusBadRequest},
		{"use case fails", startWith(admin, goodKey, goodBody), &fakeUseCase{startErr: errors.New("pq: password authentication failed for host=db-internal")}, http.StatusInternalServerError},
		{"read", readWith(admin), okUC(true), http.StatusOK},
		{"read without token", readWith(""), okUC(true), http.StatusUnauthorized},
		{"read unknown", readWith(admin), &fakeUseCase{getErr: saga.ErrNotFound}, http.StatusNotFound},
		{"read store failure", readWith(admin), &fakeUseCase{getErr: errors.New("connection refused host=db-internal")}, http.StatusInternalServerError},
	}
	exercised := map[string]bool{}
	for _, s := range scenarios {
		rec := s.do(server(s.uc))
		if rec.Code != s.want {
			t.Errorf("%s: status %d, want %d (%s)", s.name, rec.Code, s.want, rec.Body)
			continue
		}
		if strings.Contains(rec.Body.String(), "db-internal") || strings.Contains(rec.Body.String(), "SECRET-DETAIL") {
			t.Errorf("%s: the body leaks internal detail: %s", s.name, rec.Body)
		}
		method, path := http.MethodPost, route
		if strings.HasPrefix(s.name, "read") {
			method, path = http.MethodGet, route+"/"+sagaID
		}
		req := httptest.NewRequest(method, path, nil)
		found, params, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("%s %s is not in the contract: %v", method, path, err)
		}
		in := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: found},
			Status:                 rec.Code, Header: rec.Header(), Options: &openapi3filter.Options{IncludeResponseStatus: true},
		}
		in.SetBodyBytes(rec.Body.Bytes())
		if err := openapi3filter.ValidateResponse(ctx, in); err != nil {
			t.Errorf("%s: answer against the contract: %v", s.name, err)
		}
		exercised[method+" "+found.Path+" "+strconv.Itoa(rec.Code)] = true
	}
	for path, item := range doc.Paths.Map() {
		for method, operation := range item.Operations() {
			for status := range operation.Responses.Map() {
				if !exercised[method+" "+path+" "+status] {
					t.Errorf("the response %s of %s %s was never produced", status, method, path)
				}
			}
		}
	}
}

// The body of the contract has no tenant and no approver: both come from the token (ADR-003).
func TestC_the_request_of_the_contract_has_no_tenant_and_no_approver(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["StartRegisterPaymentRequest"].Value
	for _, forbidden := range []string{"tenantId", "approvedBy", "onBehalfOf"} {
		if _, found := schema.Properties[forbidden]; found {
			t.Errorf("the request declares %s", forbidden)
		}
	}
	if schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has {
		t.Error("the request accepts fields the contract does not declare")
	}
}

// HU-COM-010 DP-11: the request declares an optional reason of 1 to 500 characters, so the contract and the validation agree.
func TestC_the_request_declares_the_optional_reason(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["StartRegisterPaymentRequest"].Value
	reason := schema.Properties["reason"]
	if reason == nil || reason.Value.Type.Slice()[0] != "string" || reason.Value.MinLength != 1 || reason.Value.MaxLength == nil || *reason.Value.MaxLength != 500 {
		t.Fatalf("reason = %+v", reason)
	}
	for _, name := range schema.Required {
		if name == "reason" {
			t.Error("reason must stay optional")
		}
	}
}
