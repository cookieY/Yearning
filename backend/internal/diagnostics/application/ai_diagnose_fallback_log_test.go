package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestInvokeLLMDiagnosisLogsStructuredWarnOnFailureAndReturnsError(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	prev := zap.ReplaceGlobals(zap.New(core))
	defer prev()

	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()

	ds := &DatasourceRow{ID: "ds-diag-1", Name: "prod-mysql", Engine: "mysql"}
	desensitized := &DesensitizeTemplateResult{
		RawTemplate:        "SELECT secret_col FROM sensitive_orders WHERE customer_name = 'alice'",
		AnonymizedTemplate: "SELECT col_1 FROM table_1 WHERE col_2 = ?",
	}

	diagBudgetOnce.Do(func() {
		sql.Register("diagnostics_budget_stub", diagBudgetDriver{})
	})
	dsn := "diag-fallback-" + uuid.NewString()
	diagBudgetMu.Lock()
	diagBudgetMap[dsn] = &diagBudgetStubState{}
	diagBudgetMu.Unlock()
	t.Cleanup(func() {
		diagBudgetMu.Lock()
		delete(diagBudgetMap, dsn)
		diagBudgetMu.Unlock()
	})
	sqlDB, err := sql.Open("diagnostics_budget_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB, PreferSimpleProtocol: true, WithoutReturning: true,
	}), &gorm.Config{DisableAutomaticPing: true, Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}

	before := AIDiagnosisLLMFailureTotal()
	svc := &Services{DB: gdb, Review: &reviewapp.Services{DB: gdb}}
	_, _, _, err = svc.invokeLLMDiagnosis(context.Background(), priv, ds, desensitized, "last_1h", "exact", 10, 500, 50, 1000, 10, 0, true)
	if err == nil {
		t.Fatal("expected invokeLLMDiagnosis to fail when provider chain is unavailable")
	}
	var diagErr *domain.Error
	if !errors.As(err, &diagErr) || diagErr.Code != domain.CodeAIProviderFailed {
		t.Fatalf("expected AI_PROVIDER_FAILED (2011), got %v", err)
	}
	if AIDiagnosisLLMFailureTotal() != before+1 {
		t.Fatalf("expected failure counter to increment by 1")
	}

	entries := logs.FilterMessage("ai_diagnosis_llm_failed").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 warn log entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["datasource_id"] != "ds-diag-1" {
		t.Fatalf("expected datasource_id=ds-diag-1, got %v", fields["datasource_id"])
	}
	summary, _ := fields["error_summary"].(string)
	if summary == "" || strings.Contains(summary, "sensitive_orders") || strings.Contains(summary, "alice") {
		t.Fatalf("error_summary must be non-empty and free of sensitive SQL plaintext, got %q", summary)
	}

	// Verify logLLMDiagnosisFailure records endpoint identity, HTTP status, and parsed overflow metrics.
	ep := provider.Endpoint{ProviderID: "prov-san-1", Name: "vllm-san", ModelName: "san"}
	httpErr := &provider.HTTPStatusError{
		Code:     http.StatusBadRequest,
		Snapshot: "This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion).",
	}
	logLLMDiagnosisFailure("ds-diag-1", ep, httpErr)
	if AIDiagnosisLLMFailureTotal() != before+2 {
		t.Fatalf("expected failure counter to increment to %d", before+2)
	}
	entries = logs.FilterMessage("ai_diagnosis_llm_failed").All()
	if len(entries) != 2 {
		t.Fatalf("expected 2 warn log entries, got %d", len(entries))
	}
	f2 := entries[1].ContextMap()
	if f2["provider_id"] != "prov-san-1" || f2["provider_name"] != "vllm-san" || f2["model_name"] != "san" {
		t.Fatalf("unexpected endpoint fields in warn log: %+v", f2)
	}
	if f2["http_status"] != int64(http.StatusBadRequest) {
		t.Fatalf("expected http_status=400, got %v", f2["http_status"])
	}
	if s2, _ := f2["error_summary"].(string); s2 != "context_length_overflow max_ctx=8192 prompt_tokens=5312" {
		t.Fatalf("expected sanitized numeric overflow summary in warn log, got %q", s2)
	}
}
