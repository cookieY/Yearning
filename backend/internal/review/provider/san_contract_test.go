package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

const pinnedSANContractDigest = "7cf573a08aef075e2580a8828684bb28896c1e878407fbc3026a4b8fd9018f6d"

func TestSANContractDigestPinned(t *testing.T) {
	t.Parallel()

	if SANWireContractVersion != "san-wire-v2" {
		t.Fatalf(
			"SANWireContractVersion=%q, expected \"san-wire-v2\"; update pinnedSANContractDigest and follow docs/contracts/san-wire-contract.md change process when bumping version",
			SANWireContractVersion,
		)
	}
	got := SANContractDigest()
	if got != pinnedSANContractDigest {
		t.Fatalf(
			"SAN wire contract digest changed while SANWireContractVersion remained %q:\n got:  %s\n want: %s\nFollow the mandatory change control process in docs/contracts/san-wire-contract.md: bump SANWireContractVersion (e.g. san-wire-v2), create testdata/san_wire_contract/v2/ while preserving v1/, update version history, and sync xmodel before deploying to SAN environments.",
			SANWireContractVersion,
			got,
			pinnedSANContractDigest,
		)
	}
}

func TestSANStatisticsKeySchemaConsistency(t *testing.T) {
	t.Parallel()

	if d1, d2 := SANContractDigest(), SANContractDigest(); d1 != d2 || len(d1) != 64 {
		t.Fatalf("SANContractDigest is not deterministic 64-hex: %q vs %q", d1, d2)
	}

	forbidden := make(map[string]bool, len(SANStatisticsKeySchema.ForbiddenKeys))
	for _, k := range SANStatisticsKeySchema.ForbiddenKeys {
		if forbidden[k] {
			t.Fatalf("duplicate forbidden key %q", k)
		}
		forbidden[k] = true
	}

	universal := make(map[string]bool, len(SANStatisticsKeySchema.UniversalKeys))
	for _, k := range SANStatisticsKeySchema.UniversalKeys {
		if universal[k.Name] {
			t.Fatalf("duplicate universal key %q", k.Name)
		}
		if forbidden[k.Name] {
			t.Fatalf("universal key %q overlaps forbidden list", k.Name)
		}
		universal[k.Name] = true
	}

	seenBranches := make(map[string]bool, len(SANStatisticsKeySchema.Branches))
	for _, b := range SANStatisticsKeySchema.Branches {
		if seenBranches[b.Branch] {
			t.Fatalf("duplicate statistics branch %q", b.Branch)
		}
		seenBranches[b.Branch] = true
		seenBranchKeys := make(map[string]bool, len(b.Keys))
		for _, k := range b.Keys {
			if seenBranchKeys[k.Name] {
				t.Fatalf("branch %s: duplicate key %q", b.Branch, k.Name)
			}
			if universal[k.Name] {
				t.Fatalf("branch %s: key %q duplicates universal key", b.Branch, k.Name)
			}
			if forbidden[k.Name] {
				t.Fatalf("branch %s: key %q overlaps forbidden list", b.Branch, k.Name)
			}
			seenBranchKeys[k.Name] = true
		}
	}

	baseStats := func() map[string]any {
		return map[string]any{
			"algorithm_version":    fingerprint.AlgorithmVersion,
			"in_value_count_max":   float64(0),
			"max_statement_bytes":  int64(64),
			"unbounded_mutation":   false,
			"values_row_count_max": int64(1),
		}
	}

	valid := baseStats()
	valid["dml_has_limit"] = false
	valid["dml_has_order_by"] = false
	valid["dml_has_subselect"] = false
	valid["where_literal_null"] = false
	if err := ValidateSANGroupStatistics(SANStatBranchDMLUpdateDelete, valid); err != nil {
		t.Fatalf("expected valid update/delete statistics, got %v", err)
	}

	missingReq := baseStats()
	if err := ValidateSANGroupStatistics(SANStatBranchDMLUpdateDelete, missingReq); err == nil {
		t.Fatal("expected missing required branch keys to fail")
	}

	extraKey := baseStats()
	extraKey["dml_has_limit"] = false
	extraKey["dml_has_order_by"] = false
	extraKey["dml_has_subselect"] = false
	extraKey["where_literal_null"] = false
	extraKey["unexpected_extra_key"] = true
	if err := ValidateSANGroupStatistics(SANStatBranchDMLUpdateDelete, extraKey); err == nil {
		t.Fatal("expected unknown extra key to fail")
	}

	for _, fk := range SANStatisticsKeySchema.ForbiddenKeys {
		withForbidden := baseStats()
		withForbidden[fk] = "x"
		if err := ValidateSANGroupStatistics(SANStatBranchDMLOther, withForbidden); err == nil {
			t.Fatalf("expected forbidden key %q to fail validation", fk)
		}
	}

	badType := baseStats()
	badType["in_value_count_max"] = 1.5
	if err := ValidateSANGroupStatistics(SANStatBranchDMLOther, badType); err == nil {
		t.Fatal("expected fractional float64 for int64 key to fail")
	}

	badEnum := baseStats()
	badEnum["ddl_drop_target"] = "tablespace"
	if err := ValidateSANGroupStatistics(SANStatBranchDDLDrop, badEnum); err == nil {
		t.Fatal("expected unlisted ddl_drop_target enum value to fail")
	}

	if err := ValidateSANGroupStatistics("nonexistent_branch", baseStats()); err == nil {
		t.Fatal("expected unknown branch to fail")
	}
}

func TestSANWireMessagePrefixAndEmptyTools(t *testing.T) {
	t.Parallel()

	var rawBody []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		rawBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"schema_version\":1,\"review_run_id\":\"00000000-0000-4000-8000-000000000000\",\"outcome\":\"complete\",\"summary\":\"ok\",\"findings\":[],\"tool_requests\":[]}"}}]}`)
	}))
	defer srv.Close()

	adapter, err := NewOpenAIAdapter(Endpoint{
		ProviderID:          "prov-san-prefix",
		Name:                "san-local",
		ProviderKind:        "openai_compatible",
		BaseURL:             srv.URL,
		ModelName:           "san-4b",
		PrivacyContractHash: PrivacyContractHashSAN,
		OutputSchemaHash:    OutputSchemaHash,
		APIKey:              "k",
		Configuration: map[string]any{
			"system_prompt_profile": "san",
			ConfigKeyOutputMode:     OutputModeJSONObject,
		},
	}, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	req := ProbeRequestSAN()
	req.AllowedToolDescriptors = []ToolDescriptor{{
		Name:        "schema_metadata",
		Kind:        "builtin_database_fact",
		Description: "should be stripped on SAN wire",
	}}
	if _, err := adapter.ReviewStructured(context.Background(), req); err != nil {
		t.Fatalf("ReviewStructured failed: %v", err)
	}

	var envelope struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		t.Fatalf("unmarshal chat envelope: %v", err)
	}
	if len(envelope.Messages) != 2 || envelope.Messages[1].Role != "user" {
		t.Fatalf("unexpected messages structure: %+v", envelope.Messages)
	}
	userContent := envelope.Messages[1].Content
	if !strings.HasPrefix(userContent, SANWireUserMessagePrefix) {
		t.Fatalf("messages[1].content does not start with SANWireUserMessagePrefix: %q", userContent)
	}

	innerJSON := strings.TrimPrefix(userContent, SANWireUserMessagePrefix)
	var wire sanWireRequest
	if err := json.Unmarshal([]byte(innerJSON), &wire); err != nil {
		t.Fatalf("unmarshal inner sanWireRequest: %v", err)
	}
	if wire.AllowedToolDescriptors == nil || len(wire.AllowedToolDescriptors) != 0 {
		t.Fatalf("expected non-nil empty AllowedToolDescriptors, got %#v", wire.AllowedToolDescriptors)
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(innerJSON), &rawMap); err != nil {
		t.Fatalf("unmarshal inner raw map: %v", err)
	}
	if string(rawMap["allowed_tool_descriptors"]) != "[]" {
		t.Fatalf("expected raw allowed_tool_descriptors == [], got %s", rawMap["allowed_tool_descriptors"])
	}
}
