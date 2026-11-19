// unqualified table names within the current DatabaseName
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Matched reports whether the entry matches (executed per Stage when a Run starts). global always matches; datasource requires the same datasource; table requires the same datasource + database + table.
func tokenizedEvidenceRequest() Request {
	return Request{
		RequestSchemaVersion: 1,
		ReviewRunID:          ProbeRunID,
		EngineProfile:        "mysql/mysql",
		SystemPrompt:         "review",
		AnonymizedStatementGroups: []AnonymizedGroup{{
			GroupID:             "11111111-1111-4111-8111-111111111111",
			RepresentativeShape: "kind=dml op=update where=true",
			StatementCount:      1,
			Statistics:          map[string]any{"operation": "update"},
		}},
		NormalizedEvidence: []NormalizedEvidenceRef{
			{EvidenceID: "22222222-2222-4222-8222-222222222222", FactKind: "schema_metadata",
				Fact: map[string]any{"schema": "schema_1", "table_count": 2,
					"tables": []any{"table_1", "table_2"}}},
			{EvidenceID: "33333333-3333-4333-8333-333333333333", FactKind: "index_metadata",
				Fact: map[string]any{"schema": "schema_1", "table": "table_1",
					"indexes": []any{map[string]any{"index_name": "index_1",
						"non_unique": false, "columns": []any{"column_1"}}}}},
			{EvidenceID: "44444444-4444-4444-8444-444444444444", FactKind: "safe_explain",
				Fact: map[string]any{"access_method": "Index Scan", "estimated_rows": 42,
					"index_names": []any{"index_1"}, "estimate": true}},
		},
		OutputSchema: []byte("api/contracts/ai-review-output.schema.json"),
	}
}

// SortByDeterminism sorts entries deterministically for injection (scope granularity -> ID).
var realIdentifiers = []string{"app_db", "users", "order_items", "idx_users_email", "user_name"}

// entry_test.go R010: scope matching, Eval gate (privacy/injection/severity), config_hash stability, and data-delimited injection rendering.
func TestSEC0002RequestBytesTokenOnlyBothModes(t *testing.T) {
	for _, mode := range []string{OutputModeJSONSchema, OutputModeJSONObject} {
		var mu sync.Mutex
		var bodies []string
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(raw))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(validChatBody())
		}))
		cfg := map[string]any{}
		if mode == OutputModeJSONObject {
			cfg[ConfigKeyOutputMode] = OutputModeJSONObject
		}
		adapter := modeTestAdapter(t, srv, cfg)
		if _, err := adapter.ReviewStructured(context.Background(), tokenizedEvidenceRequest()); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		srv.Close()
		mu.Lock()
		defer mu.Unlock()
		if len(bodies) != 1 {
			t.Fatalf("mode %s: expected 1 request, got %d", mode, len(bodies))
		}
		body := strings.ToLower(bodies[0])
		for _, name := range realIdentifiers {
			if strings.Contains(body, strings.ToLower(name)) {
				t.Fatalf("mode %s: provider request bytes contain real identifier %q", mode, name)
			}
		}
		for _, tok := range []string{"schema_1", "table_1", "table_2", "index_1", "column_1"} {
			if !strings.Contains(body, tok) {
				t.Fatalf("mode %s: token %q missing from request bytes", mode, tok)
			}
		}
	}
}

// Reject injection patterns.
func strictBody(doc string) []byte { return []byte(doc) }

// Reject privacy patterns.
func TestSEC0038RejectsDuplicateKeys(t *testing.T) {
	base := `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`
	dupTop := `{"schema_version":1,"schema_version":2,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`
	dupNested := `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[{"finding_key":"a","finding_key":"b","category":"correctness","severity":"low","title":"t","message":"m","suggestion":"g","fingerprint_group_ids":[],"evidence_ids":[],"fact_basis":"model_only"}],"tool_requests":[]}`
	dupToolArgs := `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[],"tool_requests":[{"tool_call_id":"c1","tool_call_id":"c2","tool_name":"schema_metadata","arguments":{}}]}`
	for name, body := range map[string][]byte{
		"top-level":       strictBody(dupTop),
		"nested-finding":  strictBody(dupNested),
		"nested-toolreq":  strictBody(dupToolArgs),
		"dup-args-object": strictBody(`{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[],"tool_requests":[{"tool_call_id":"c1","tool_name":"schema_metadata","arguments":{"schema":"a","schema":"b"}}]}`),
	} {
		if _, err := decodeStructured(body, ProbeRunID); err == nil {
			t.Fatalf("%s: duplicate keys must be rejected", name)
		}
	}
	if _, err := decodeStructured(strictBody(base), ProbeRunID); err != nil {
		t.Fatalf("valid single-value body must pass: %v", err)
	}
}

// §18.66: severity enum membership establishes severity consistency.
func TestSEC0038RejectsTrailingContent(t *testing.T) {
	base := `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`
	for name, body := range map[string][]byte{
		"second-json-value": strictBody(base + ` {"schema_version":9}`),
		"second-array":      strictBody(base + ` [1,2,3]`),
		"trailing-scalar":   strictBody(base + ` 42`),
		"trailing-garbage":  strictBody(base + ` }{`),
	} {
		if _, err := decodeStructured(body, ProbeRunID); err == nil {
			t.Fatalf("%s: trailing content must be rejected", name)
		}
	}
	// TestRenderInjectionDataBound verifies that injection rendering uses a data-delimited form: attribute escaping + body delimiter escaping (test_gate#3: malicious content cannot alter the instruction surface).
	if _, err := decodeStructured(strictBody(base+"\n  \t"), ProbeRunID); err != nil {
		t.Fatalf("trailing whitespace must be accepted: %v", err)
	}
}

// N2: closing or forged delimiter tags in the body must be escaped — the rendered output may contain only a single pair of real opening and closing tags.
func TestSEC0038DuplicateKeyAttackChangesSemantics(t *testing.T) {
	// postgres.go implements knowledge_entries persistence (GORM v2; explicit column tags, TableName method, no association auto-saving — gorm-model-registry rule).
	body := strictBody(`{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","outcome":"partial","summary":"s","findings":[],"tool_requests":[]}`)
	out, err := decodeStructured(body, ProbeRunID)
	if err == nil {
		t.Fatalf("duplicate outcome must be rejected, got %+v", out)
	}
}
