// SQLStatementModel corresponds to sql_statements (insert_only; contains no plaintext literals).
package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func validResponse(runID string) Response {
	return Response{
		SchemaVersion: 1, ReviewRunID: runID, Outcome: "complete",
		Summary: "ok",
		Findings: []Finding{{
			FindingKey: "unbounded.update", Category: "correctness", Severity: "high",
			Title: "t", Message: "m", Suggestion: "s",
			FingerprintGroupIDs: []string{"11111111-1111-1111-1111-111111111111"},
			EvidenceIDs:         []string{}, FactBasis: "model_only",
		}},
		ToolRequests: []ToolRequest{},
	}
}

func TestValidateOutputAcceptsPositive(t *testing.T) {
	r := validResponse("run-1")
	if err := ValidateOutput(&r, "run-1"); err != nil {
		t.Fatalf("positive rejected: %v", err)
	}
}

func TestValidateOutputKeywordNegatives(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Response)
	}{
		{"schema_version", func(r *Response) { r.SchemaVersion = 2 }},
		{"run_mismatch", func(r *Response) { r.ReviewRunID = "other" }},
		{"outcome_enum", func(r *Response) { r.Outcome = "done" }},
		{"summary_empty", func(r *Response) { r.Summary = "" }},
		{"summary_long", func(r *Response) { r.Summary = strings.Repeat("x", 4001) }},
		{"finding_key_pattern", func(r *Response) { r.Findings[0].FindingKey = "1bad" }},
		{"finding_key_duplicate", func(r *Response) {
			r.Findings = append(r.Findings, r.Findings[0])
		}},
		{"category_enum", func(r *Response) { r.Findings[0].Category = "style" }},
		{"severity_enum", func(r *Response) { r.Findings[0].Severity = "extreme" }},
		{"title_empty", func(r *Response) { r.Findings[0].Title = "" }},
		{"message_long", func(r *Response) { r.Findings[0].Message = strings.Repeat("x", 4001) }},
		{"no_group_ref", func(r *Response) { r.Findings[0].FingerprintGroupIDs = []string{} }},
		{"group_dup", func(r *Response) {
			r.Findings[0].FingerprintGroupIDs = []string{"a", "a"}
		}},
		{"evidence_basis_without_ids", func(r *Response) {
			r.Findings[0].FactBasis = "evidence"
			r.Findings[0].EvidenceIDs = []string{}
		}},
		{"model_only_with_ids", func(r *Response) {
			r.Findings[0].FactBasis = "model_only"
			r.Findings[0].EvidenceIDs = []string{"22222222-2222-2222-2222-222222222222"}
		}},
		{"fact_basis_enum", func(r *Response) { r.Findings[0].FactBasis = "guess" }},
		{"tool_requests_over_limit", func(r *Response) {
			for i := 0; i < 101; i++ {
				r.ToolRequests = append(r.ToolRequests, ToolRequest{ToolCallID: "x", ToolName: "n"})
			}
		}},
		{"tool_call_id_not_uuid", func(r *Response) {
			r.ToolRequests = append(r.ToolRequests, ToolRequest{ToolCallID: "not-a-uuid", ToolName: "n"})
		}},
		{"tool_call_id_duplicate", func(r *Response) {
			r.ToolRequests = append(r.ToolRequests,
				ToolRequest{ToolCallID: "11111111-1111-1111-1111-111111111111", ToolName: "n"},
				ToolRequest{ToolCallID: "11111111-1111-1111-1111-111111111111", ToolName: "n2"})
		}},
		{"tool_name_pattern", func(r *Response) {
			r.ToolRequests = append(r.ToolRequests, ToolRequest{ToolCallID: "11111111-1111-1111-1111-111111111111", ToolName: "1Bad"})
		}},
		{"findings_over_limit", func(r *Response) {
			for i := 0; i < 1001; i++ {
				r.Findings = append(r.Findings, r.Findings[0])
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validResponse("run-1")
			tc.mutate(&r)
			if err := ValidateOutput(&r, "run-1"); err == nil {
				t.Fatalf("negative %s accepted", tc.name)
			}
		})
	}
}

// SQLFingerprintGroupModel corresponds to sql_fingerprint_groups (insert_only).
func TestFakeAdapterRejectsUnknownFields(t *testing.T) {
	adapter := NewLocalFakeAdapter(Endpoint{
		ProviderID: "p1", Name: "fake", BaseURL: "https://api.example.com",
		ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
	}, func(req Request) ([]byte, int, error) {
		raw, _ := json.Marshal(map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "s", "findings": []any{}, "tool_requests": []any{},
			"rogue_field": "instruction override",
		})
		return raw, 200, nil
	})
	_, err := adapter.ReviewStructured(t.Context(), Request{ReviewRunID: "run-9"})
	if err == nil {
		t.Fatal("unknown response field accepted")
	}
}

// StageReviewSnapshotModel corresponds to stage_review_snapshots
// (mutable_aggregate; unique per Run and stage_position, R003 frozen reuse unit).
func TestFakeAdapterRejectsFreeForm(t *testing.T) {
	adapter := NewLocalFakeAdapter(Endpoint{
		ProviderID: "p1", Name: "fake", BaseURL: "https://api.example.com",
		ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
	}, func(req Request) ([]byte, int, error) {
		return []byte("this is not json at all"), 200, nil
	})
	_, err := adapter.ReviewStructured(t.Context(), Request{ReviewRunID: "run-9"})
	if err == nil {
		t.Fatal("free-form response accepted")
	}
}
