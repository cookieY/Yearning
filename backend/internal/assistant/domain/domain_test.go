package domain

import (
	"strings"
	"testing"
)

func TestErrorsAndTypesCoverage(t *testing.T) {
	var nilErr *Error
	if nilErr.Error() != "" {
		t.Fatalf("expected empty string for nil error, got %q", nilErr.Error())
	}

	emptyMsgErr := &Error{Code: CodeValidationFailed, Name: "VALIDATION_FAILED", Message: ""}
	if !strings.Contains(emptyMsgErr.Error(), "VALIDATION_FAILED") {
		t.Fatalf("unexpected error string: %q", emptyMsgErr.Error())
	}

	nonEmptyMsgErr := ErrValidation("custom validation error")
	if !strings.Contains(nonEmptyMsgErr.Error(), "custom validation error") {
		t.Fatalf("unexpected error string: %q", nonEmptyMsgErr.Error())
	}
	if ErrValidation("").Message == "" {
		t.Fatal("expected default validation message")
	}

	if ErrNotFound().Code != CodeResourceNotFound {
		t.Fatal("unexpected code for ErrNotFound")
	}
	if ErrConcurrent().Code != CodeConcurrentModification {
		t.Fatal("unexpected code for ErrConcurrent")
	}
	if ErrInvalidState("").Code != CodeInvalidStateTransition || ErrInvalidState("custom").Message != "custom" {
		t.Fatal("unexpected ErrInvalidState behavior")
	}
	if CodeIdempotencyConflict != 1007 {
		t.Fatalf("CodeIdempotencyConflict = %d, want 1007 (IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD)", CodeIdempotencyConflict)
	}
	if CodeIdempotencyConflict == 1011 {
		t.Fatal("CodeIdempotencyConflict must not collide with 1011 (PRECONDITION_REQUIRED)")
	}
	if ErrIdempotencyConflict("").Code != CodeIdempotencyConflict || ErrIdempotencyConflict("custom").Message != "custom" {
		t.Fatal("unexpected ErrIdempotencyConflict behavior")
	}
	if ErrCapacityExhausted("").Code != CodeSystemCapacity || ErrCapacityExhausted("custom").Message != "custom" {
		t.Fatal("unexpected ErrCapacityExhausted behavior")
	}
	if ErrBudgetExhausted().Code != CodeAIBudgetExhausted {
		t.Fatal("unexpected ErrBudgetExhausted code")
	}
	if ErrProviderFailed("").Code != CodeAIProviderFailed || ErrProviderFailed("custom").Message != "custom" {
		t.Fatal("unexpected ErrProviderFailed behavior")
	}
	if ErrOutOfScope("").Code != CodeAssistantOutOfScope || ErrOutOfScope("custom").Message != "custom" {
		t.Fatal("unexpected ErrOutOfScope behavior")
	}
	if ErrDatasourceForbidden().Code != CodeAssistantDatasourceForbidden {
		t.Fatal("unexpected ErrDatasourceForbidden code")
	}
	if ErrAnonymizationFailed("").Code != CodeAssistantAnonymizationFailed || ErrAnonymizationFailed("r1").Meta["reason"] != "r1" {
		t.Fatal("unexpected ErrAnonymizationFailed behavior")
	}
	if ErrSQLRestorationInvalid("").Code != CodeAssistantSQLRestorationInvalid || ErrSQLRestorationInvalid("r2").Meta["reason"] != "r2" {
		t.Fatal("unexpected ErrSQLRestorationInvalid behavior")
	}
	if ErrDraftRevisionStale("").Code != CodeAssistantDraftRevisionStale || ErrDraftRevisionStale("r3").Meta["reason"] != "r3" {
		t.Fatal("unexpected ErrDraftRevisionStale behavior")
	}
	if ErrContextBudgetExceeded("").Code != CodeAssistantContextBudgetExceeded || ErrContextBudgetExceeded("custom").Message != "custom" {
		t.Fatal("unexpected ErrContextBudgetExceeded behavior")
	}
	if ErrStreamInProgress().Code != CodeAssistantStreamInProgress {
		t.Fatal("unexpected ErrStreamInProgress code")
	}

	for _, c := range []Capability{CapabilityPlatformHelp, CapabilitySQLGenerate, CapabilitySQLOptimize, CapabilityOutOfScope} {
		if !ValidCapability(string(c)) {
			t.Fatalf("expected valid capability %q", c)
		}
	}
	if ValidCapability("unknown_capability") {
		t.Fatal("expected invalid capability for unknown_capability")
	}

	if cat, ok := RiskCategoryOf(StmtSelect); !ok || cat != RiskReadOnly {
		t.Fatalf("expected read_only for select, got %q %v", cat, ok)
	}
	for _, k := range []StatementKind{StmtInsert, StmtUpdate, StmtDelete} {
		if cat, ok := RiskCategoryOf(k); !ok || cat != RiskDML {
			t.Fatalf("expected dml for %q, got %q %v", k, cat, ok)
		}
	}
	for _, k := range []StatementKind{StmtCreateTable, StmtAlterTable, StmtCreateIndex, StmtDropIndex} {
		if cat, ok := RiskCategoryOf(k); !ok || cat != RiskDDL {
			t.Fatalf("expected ddl for %q, got %q %v", k, cat, ok)
		}
	}
	if cat, ok := RiskCategoryOf("drop_table"); ok || cat != "" {
		t.Fatalf("expected false for unsupported statement kind, got %q %v", cat, ok)
	}
}
