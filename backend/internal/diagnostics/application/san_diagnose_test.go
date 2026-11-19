package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

type fakeStatsCollector struct {
	cols map[string][]database.MetadataColumn
	idxs map[string][]database.MetadataIndex
}

func (f *fakeStatsCollector) Probe(context.Context, DatasourceRow) (*database.StatsProbeResult, error) {
	return &database.StatsProbeResult{}, nil
}

func (f *fakeStatsCollector) ReadSnapshot(context.Context, DatasourceRow, int) ([]database.NativeStatementStatRow, error) {
	return nil, nil
}

func (f *fakeStatsCollector) ReadTemplate(context.Context, DatasourceRow, string, string) (string, error) {
	return "", nil
}

func (f *fakeStatsCollector) ListColumns(_ context.Context, _ DatasourceRow, schema, table string) ([]database.MetadataColumn, error) {
	return f.cols[schema+"."+table], nil
}

func (f *fakeStatsCollector) ListIndexes(_ context.Context, _ DatasourceRow, schema, table string) ([]database.MetadataIndex, error) {
	return f.idxs[schema+"."+table], nil
}

func TestDiagnoseDualTrack_DesensitizeMessagesAndPostValidation(t *testing.T) {
	ctx := context.Background()
	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()

	dbName := "ledger_db"
	ds := &DatasourceRow{
		ID:                "ds-1",
		Name:              "prod-ledger",
		Engine:            "mysql",
		CompatibilityMode: "mysql",
		DatabaseName:      &dbName,
	}
	svc := &Services{
		Collector: &fakeStatsCollector{
			cols: map[string][]database.MetadataColumn{
				"ledger_db.transfers": {
					{ColumnName: "id", DatabaseType: "bigint"},
					{ColumnName: "account_no", DatabaseType: "varchar(64)"},
					{ColumnName: "created_at", DatabaseType: "datetime"},
				},
			},
			idxs: map[string][]database.MetadataIndex{
				"ledger_db.transfers": {
					{IndexName: "PRIMARY", Primary: true, ColumnNames: []string{"id"}},
					{IndexName: "idx_transfers_account", NonUnique: true, ColumnNames: []string{"account_no"}},
				},
			},
		},
	}

	longLiteral := strings.Repeat("K", 305)
	rawSQL := "SELECT id, account_no FROM ledger_db.transfers WHERE account_no = '" + longLiteral + "' ORDER BY created_at DESC"
	desensitized, err := svc.DesensitizeStatementTemplate(ctx, priv, ds, "ledger_db", rawSQL)
	if err != nil {
		t.Fatalf("DesensitizeStatementTemplate error: %v", err)
	}

	// Default track summary MUST remain 100% anonymized.
	for _, secret := range []string{"ledger_db", "transfers", "account_no", "idx_transfers_account", longLiteral} {
		if strings.Contains(desensitized.AnonymizedSummary, secret) || strings.Contains(desensitized.AnonymizedTemplate, secret) {
			t.Fatalf("anonymized fields leaked raw term %q: summary=%q template=%q", secret, desensitized.AnonymizedSummary, desensitized.AnonymizedTemplate)
		}
	}
	// In-memory SAN fields keep real schema/table/column/index metadata and truncate >256-char literals.
	if strings.Contains(desensitized.RawTemplate, longLiteral) || !strings.Contains(desensitized.RawTemplate, "<truncated:305_chars>") {
		t.Fatalf("expected truncated >256-char literal in RawTemplate, got %q", desensitized.RawTemplate)
	}
	for _, rawTerm := range []string{"ledger_db.transfers", "account_no:varchar(64)", "idx_transfers_account(account_no)"} {
		if !strings.Contains(desensitized.RawSchemaMetadata, rawTerm) {
			t.Fatalf("expected %q in RawSchemaMetadata, got %q", rawTerm, desensitized.RawSchemaMetadata)
		}
	}

	// 1. SAN endpoint produces exact 225B diagnose_normal prompt + raw sql_template/schema_metadata payload.
	sanSys, sanUser, err := buildDiagnoseMessagesForEndpoint(
		provider.Endpoint{BaseURL: "http://192.168.10.20:8000/v1", ModelName: "san-4b"},
		priv, ds, desensitized, "last_1h", "exact", 120, 4500.5, 37.5, 50000, 12, 0.25, false,
	)
	if err != nil {
		t.Fatalf("buildDiagnoseMessagesForEndpoint SAN error: %v", err)
	}
	sum := sha256.Sum256([]byte(sanSys))
	const wantDiagnoseHash = "73ea4d35fd6330932dc3ebc30de8b106aefc3fa846c658ae58b126a6ad56145f"
	if len(sanSys) != 225 || hex.EncodeToString(sum[:]) != wantDiagnoseHash {
		t.Fatalf("SAN diagnose system prompt drifted: len=%d hash=%s\nprompt=%q", len(sanSys), hex.EncodeToString(sum[:]), sanSys)
	}
	var sanMap map[string]any
	if err := json.Unmarshal([]byte(sanUser), &sanMap); err != nil {
		t.Fatalf("unmarshal SAN user payload: %v", err)
	}
	if _, hasAnon := sanMap["desensitized_template"]; hasAnon {
		t.Fatalf("expected SAN payload without desensitized_template key, got %v", sanMap)
	}
	if tmpl, _ := sanMap["sql_template"].(string); !strings.Contains(tmpl, "ledger_db.transfers") || !strings.Contains(tmpl, "<truncated:305_chars>") {
		t.Fatalf("unexpected SAN sql_template: %q", tmpl)
	}
	if meta, _ := sanMap["schema_metadata"].(string); !strings.Contains(meta, "idx_transfers_account(account_no)") {
		t.Fatalf("unexpected SAN schema_metadata: %q", meta)
	}

	// 2. Legacy v3.1 endpoint (xmodel-v3.1 / san-v3.1) stays on default anonymized track with ZERO raw leak.
	for _, legacyModel := range []string{"xmodel-v3.1", "san-v3.1", "SAN-4B-v3.1", "san:v3.1"} {
		defSys, defUser, err := buildDiagnoseMessagesForEndpoint(
			provider.Endpoint{BaseURL: "http://10.0.1.30:8000/v1", ModelName: legacyModel},
			priv, ds, desensitized, "last_1h", "exact", 120, 4500.5, 37.5, 50000, 12, 0.25, true,
		)
		if err != nil {
			t.Fatalf("legacy model %s default build error: %v", legacyModel, err)
		}
		if strings.Contains(defSys, "[SAN_HEAD=") {
			t.Fatalf("legacy model %s mistakenly got SAN head: %q", legacyModel, defSys)
		}
		for _, secret := range []string{"ledger_db", "transfers", "account_no", "idx_transfers_account", longLiteral} {
			if strings.Contains(defUser, secret) || strings.Contains(defSys, secret) {
				t.Fatalf("legacy model %s leaked raw term %q: user=%q", legacyModel, secret, defUser)
			}
		}
		if !strings.Contains(defUser, "desensitized_template") || !strings.Contains(defUser, "desensitized_metadata") {
			t.Fatalf("legacy model %s missing anonymized keys: %q", legacyModel, defUser)
		}
	}

	// 3. Post-response handling: SAN skips token restoration while keeping ValidateNoHallucinatedPlanFacts active.
	sanRespJSON := `{
		"observed_facts": [{"metric": "avg_exec_time_ms", "value": "37.5 ms", "window": "last_1h"}],
		"root_cause_hypotheses": [{"hypothesis": "Missing composite index on ledger_db.transfers(account_no, created_at)", "confidence": "high", "evidence_basis": "50,000 rows scanned vs 12 returned without idx_transfers_account covering created_at"}],
		"optimization_recommendations": [{"kind": "index_ddl", "title": "Add composite index on ledger_db.transfers", "rationale": "Match WHERE account_no and ORDER BY created_at", "applicable_conditions": ["Low write amplification"], "candidate_sql": "CREATE INDEX idx_transfers_acct_created ON ledger_db.transfers (account_no, created_at)", "risk_notes": ["Lock table during creation"]}],
		"verification_steps": ["Run EXPLAIN in staging on ledger_db.transfers"]
	}`
	payloadSAN, candSQL, candKind, ok := parseAndFinalizeDiagnoseResponse(sanRespJSON, true, priv, "last_1h")
	if !ok || payloadSAN == nil || candSQL == nil || candKind == nil {
		t.Fatalf("expected SAN diagnosis parse success, got ok=%v payload=%+v", ok, payloadSAN)
	}
	if !strings.Contains(*candSQL, "CREATE INDEX idx_transfers_acct_created ON ledger_db.transfers") || *candKind != "ddl" {
		t.Fatalf("unexpected SAN candidate SQL/kind: sql=%q kind=%q", *candSQL, *candKind)
	}

	// Default track restores anonymized tokens deterministically.
	defRespJSON := strings.NewReplacer(
		"ledger_db", "schema_1",
		"transfers", "table_1",
		"account_no", "column_2",
	).Replace(sanRespJSON)
	payloadDef, candSQLDef, _, okDef := parseAndFinalizeDiagnoseResponse(defRespJSON, false, priv, "last_1h")
	if !okDef || payloadDef == nil || candSQLDef == nil {
		t.Fatalf("expected default diagnosis parse success, got ok=%v", okDef)
	}
	if !strings.Contains(*candSQLDef, "schema_1") && !strings.Contains(*candSQLDef, "ledger_db.transfers") {
		t.Fatalf("unexpected candidate SQL restoration: %q", *candSQLDef)
	}
	if !strings.Contains(*candSQLDef, "ledger_db.transfers (account_no, created_at)") {
		t.Fatalf("expected default track to restore table_1/column_2 tokens, got %q", *candSQLDef)
	}

	// Anti-hallucination gate must still reject hallucinated execution-plan assertions on SAN!
	hallucinatedSAN := `{
		"observed_facts": [{"metric": "scan", "value": "execution plan confirmed full table scan", "window": "last_1h"}],
		"root_cause_hypotheses": [{"hypothesis": "Sequential scan", "confidence": "high", "evidence_basis": "rows scanned"}],
		"optimization_recommendations": [{"kind": "index_ddl", "title": "Add index", "rationale": "Speed up", "applicable_conditions": ["test"], "candidate_sql": null, "risk_notes": ["none"]}],
		"verification_steps": ["Verify later"]
	}`
	if _, _, _, okHal := parseAndFinalizeDiagnoseResponse(hallucinatedSAN, true, priv, "last_1h"); okHal {
		t.Fatal("expected ValidateNoHallucinatedPlanFacts to reject hallucinated SAN report")
	}
}

func TestDiagnoseSAN_AdditionalEdgeBranches(t *testing.T) {
	ctx := context.Background()
	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()

	dbName := "analytics_db"
	ds := &DatasourceRow{
		ID:                "ds-edge",
		Name:              "pg-analytics",
		Engine:            "postgresql",
		CompatibilityMode: "postgresql",
		DatabaseName:      &dbName,
	}
	svc := &Services{
		Collector: &fakeStatsCollector{
			cols: map[string][]database.MetadataColumn{
				"analytics_db.events": {
					{ColumnName: "id", DatabaseType: "bigint"},
				},
			},
			idxs: map[string][]database.MetadataIndex{
				"analytics_db.events": {
					{IndexName: "idx_events_expr_only", NonUnique: true, ColumnNames: nil},
				},
			},
		},
	}

	desensitized, err := svc.DesensitizeStatementTemplate(ctx, priv, ds, "analytics_db", "SELECT id FROM analytics_db.events WHERE id = 7")
	if err != nil {
		t.Fatalf("DesensitizeStatementTemplate error: %v", err)
	}
	if !strings.Contains(desensitized.RawSchemaMetadata, "idx_events_expr_only") {
		t.Fatalf("expected columnless expression index name in RawSchemaMetadata, got %q", desensitized.RawSchemaMetadata)
	}

	// SAN English locale branch and fallback when RawTemplate is empty.
	emptyRaw := *desensitized
	emptyRaw.RawTemplate = ""
	sanSysEn, sanUserEn, err := buildDiagnoseMessagesForEndpoint(
		provider.Endpoint{BaseURL: "http://192.168.10.20:8000/v1", ModelName: "san-4b"},
		priv, ds, &emptyRaw, "last_24h", "approx", 5, 12.5, 2.5, 100, 1, 0, true,
	)
	if err != nil {
		t.Fatalf("buildDiagnoseMessagesForEndpoint EN SAN error: %v", err)
	}
	if !strings.Contains(sanSysEn, "en-US") || !strings.Contains(sanUserEn, desensitized.AnonymizedTemplate) {
		t.Fatalf("unexpected EN SAN output: sys=%q user=%q", sanSysEn, sanUserEn)
	}

	// Default track zh-CN locale and AssertZeroLeak guard.
	defSysZh, _, err := buildDiagnoseMessagesForEndpoint(
		provider.Endpoint{BaseURL: "http://10.0.1.30:8000/v1", ModelName: "xmodel-v3.1"},
		priv, ds, desensitized, "last_24h", "approx", 5, 12.5, 2.5, 100, 1, 0, false,
	)
	if err != nil || !strings.Contains(defSysZh, "Simplified Chinese (zh-CN)") {
		t.Fatalf("unexpected zh-CN default prompt: err=%v sys=%q", err, defSysZh)
	}
	leaking := *desensitized
	leaking.AnonymizedSummary = "analytics_db.events(id)"
	if _, _, err := buildDiagnoseMessagesForEndpoint(
		provider.Endpoint{BaseURL: "http://10.0.1.30:8000/v1", ModelName: "xmodel-v3.1"},
		priv, ds, &leaking, "last_24h", "approx", 5, 12.5, 2.5, 100, 1, 0, true,
	); err == nil {
		t.Fatal("expected AssertZeroLeak to fail when anonymized metadata leaks raw table/schema name")
	}

	// parseAndFinalizeDiagnoseResponse edge cases.
	if _, _, _, ok := parseAndFinalizeDiagnoseResponse("   ", true, priv, "last_1h"); ok {
		t.Fatal("expected empty response to return false")
	}
	if _, _, _, ok := parseAndFinalizeDiagnoseResponse("{bad-json", true, priv, "last_1h"); ok {
		t.Fatal("expected malformed JSON to return false")
	}
	if _, _, _, ok := parseAndFinalizeDiagnoseResponse(`{"observed_facts":[]}`, true, priv, "last_1h"); ok {
		t.Fatal("expected incomplete sections to return false")
	}

	normResp := `{
		"observed_facts": [{"metric": "calls", "value": "5", "window": ""}],
		"root_cause_hypotheses": [{"hypothesis": "No index covers filter", "confidence": "uncertain", "evidence_basis": "5 calls"}],
		"optimization_recommendations": [{"kind": "index_ddl", "title": "Inspect index", "rationale": "Improve lookup", "applicable_conditions": ["High selectivity"], "candidate_sql": "null", "risk_notes": ["Minor write cost"]}],
		"verification_steps": ["Check EXPLAIN"]
	}`
	parsed, candSQL, candKind, ok := parseAndFinalizeDiagnoseResponse(normResp, true, priv, "last_24h")
	if !ok || parsed == nil || candSQL != nil || candKind != nil {
		t.Fatalf("expected normalized parse success with nil candidate SQL, got ok=%v cand=%v kind=%v", ok, candSQL, candKind)
	}
	if parsed.ObservedFacts[0].Window != "last_24h" || parsed.RootCauseHypotheses[0].Confidence != "medium" {
		t.Fatalf("expected default window and normalized medium confidence, got %+v", parsed)
	}

	// invokeLLMDiagnosis nil guards.
	if _, _, _, err := svc.invokeLLMDiagnosis(ctx, priv, ds, desensitized, "last_1h", "exact", 1, 1, 1, 1, 1, 0, true); err == nil {
		t.Fatal("expected invokeLLMDiagnosis to fail when Review is nil")
	} else {
		var diagErr *domain.Error
		if !errors.As(err, &diagErr) || diagErr.Code != domain.CodeAIProviderFailed {
			t.Fatalf("expected AI_PROVIDER_FAILED (2011), got %v", err)
		}
	}
}
