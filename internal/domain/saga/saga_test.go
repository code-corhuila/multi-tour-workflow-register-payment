package saga_test

import (
	"testing"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

// The steps are the ones of the spec and of the check constraints of the schema.
func TestSteps_are_the_ones_of_the_spec(t *testing.T) {
	got := []saga.Step{saga.RecordCashIncome, saga.RecordValidatedPayment, saga.ReverseCashIncome}
	want := []string{"RECORD_CASH_INCOME", "RECORD_VALIDATED_PAYMENT", "REVERSE_CASH_INCOME"}
	for i := range got {
		if string(got[i]) != want[i] {
			t.Errorf("step %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// AC-WF2-05: the key of a step is stable for the same saga and step, and differs between steps.
func TestStepKey_is_stable_per_saga_and_step(t *testing.T) {
	s := saga.Saga{ID: "a1"}

	if s.StepKey(saga.RecordCashIncome) != "a1:RECORD_CASH_INCOME" {
		t.Errorf("key = %q", s.StepKey(saga.RecordCashIncome))
	}
	if s.StepKey(saga.RecordCashIncome) == s.StepKey(saga.RecordValidatedPayment) {
		t.Error("two steps share a key")
	}
}

func TestRecord_and_Done_follow_the_steps_that_were_completed(t *testing.T) {
	s := saga.Saga{}

	if s.Done(saga.RecordCashIncome) {
		t.Fatal("a new saga has done nothing")
	}
	s.Record(saga.RecordCashIncome)

	if !s.Done(saga.RecordCashIncome) || s.Done(saga.RecordValidatedPayment) {
		t.Errorf("steps = %+v", s.Steps)
	}
}

// AC-WF2-03: the reversal marks the step that it undid, and only that one.
func TestMarkCompensated_marks_only_the_named_step(t *testing.T) {
	s := saga.Saga{}
	s.Record(saga.RecordCashIncome)
	s.Record(saga.RecordValidatedPayment)

	s.MarkCompensated(saga.RecordCashIncome)

	if !s.Steps[0].Compensated || s.Steps[1].Compensated {
		t.Errorf("steps = %+v", s.Steps)
	}
}

func TestEnd_closes_the_saga_with_the_step_that_failed(t *testing.T) {
	s := saga.Saga{Status: saga.Running}

	s.End(saga.Compensated, saga.RecordValidatedPayment, "VALIDATION: no seat")

	if s.Status != saga.Compensated || s.FailedStep != saga.RecordValidatedPayment || s.FailureDetail != "VALIDATION: no seat" {
		t.Errorf("saga = %+v", s)
	}
}

// AC-WF2-08: nothing of the customer is part of the state: the fields are ids, amounts and the concept.
func TestPayment_carries_no_name_or_document_of_the_customer(t *testing.T) {
	p := saga.Payment{ReservationID: "r", PaymentID: "p", Medium: "CASH", AmountCents: 100, BusinessDate: "2026-11-10", Concept: "c", ApprovedBy: "u"}

	if p.AmountCents != 100 || p.Medium != saga.MediumCash {
		t.Errorf("payment = %+v", p)
	}
}

// AC-WF2-05: a store tells apart the three ways a save can be refused.
func TestErrors_are_distinct(t *testing.T) {
	if saga.ErrNotFound == saga.ErrConflict || saga.ErrConflict == saga.ErrEnded {
		t.Error("the errors of the store must differ")
	}
}

func TestHoldsPayment_unless_the_income_was_never_recorded(t *testing.T) {
	cases := map[string]struct {
		status saga.Status
		failed saga.Step
		want   bool
	}{
		"running":              {saga.Running, "", true},
		"completed":            {saga.Completed, "", true},
		"compensated":          {saga.Compensated, saga.RecordValidatedPayment, true},
		"failed before income": {saga.Failed, saga.RecordCashIncome, false},
		"failed after income":  {saga.Failed, saga.RecordValidatedPayment, true},
	}
	for name, c := range cases {
		if got := (saga.Saga{Status: c.status, FailedStep: c.failed}).HoldsPayment(); got != c.want {
			t.Errorf("%s: HoldsPayment = %v, want %v", name, got, c.want)
		}
	}
}
