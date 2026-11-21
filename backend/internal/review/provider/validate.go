// validate.go performs Go-side schema validation of structured AI review outputs against
// ai-review-output.schema.json (free-form responses are never accepted as review success).
package provider

import (
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Controlled output vocabularies matching ai-review-output.schema.json enums.
var (
	severitySet = map[string]bool{"info": true, "low": true, "medium": true, "high": true, "critical": true}
	categorySet = map[string]bool{
		"correctness": true, "performance": true, "availability": true,
		"security": true, "governance": true, "compatibility": true, "operability": true,
	}
	factBasisSet = map[string]bool{"evidence": true, "model_only": true}
)

// ValidateOutput verifies all required fields, bounds, unique keys, and fact_basis invariants on r.
func ValidateOutput(r *Response, expectRunID string) error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1, got %d", r.SchemaVersion)
	}
	if r.ReviewRunID != expectRunID {
		return fmt.Errorf("review_run_id mismatch")
	}
	if r.Outcome != "complete" && r.Outcome != "partial" {
		return fmt.Errorf("outcome %q invalid", r.Outcome)
	}
	if n := utf8.RuneCountInString(r.Summary); n < 1 || n > 4000 {
		return fmt.Errorf("summary length %d out of range", n)
	}
	if len(r.Findings) > 1000 {
		return fmt.Errorf("findings exceed 1000")
	}
	seenKeys := map[string]bool{}
	for i, f := range r.Findings {
		if !validFindingKey(f.FindingKey) {
			return fmt.Errorf("findings[%d].finding_key %q invalid", i, f.FindingKey)
		}
		if seenKeys[f.FindingKey] {
			return fmt.Errorf("findings[%d].finding_key duplicate", i)
		}
		seenKeys[f.FindingKey] = true
		if !categorySet[f.Category] {
			return fmt.Errorf("findings[%d].category %q invalid", i, f.Category)
		}
		if !severitySet[f.Severity] {
			return fmt.Errorf("findings[%d].severity %q invalid", i, f.Severity)
		}
		if n := utf8.RuneCountInString(f.Title); n < 1 || n > 200 {
			return fmt.Errorf("findings[%d].title length out of range", i)
		}
		if n := utf8.RuneCountInString(f.Message); n < 1 || n > 4000 {
			return fmt.Errorf("findings[%d].message length out of range", i)
		}
		if utf8.RuneCountInString(f.Suggestion) > 4000 {
			return fmt.Errorf("findings[%d].suggestion too long", i)
		}
		if len(f.FingerprintGroupIDs) < 1 || len(f.FingerprintGroupIDs) > 1000 || !uniqueStrings(f.FingerprintGroupIDs) {
			return fmt.Errorf("findings[%d].fingerprint_group_ids invalid", i)
		}
		if len(f.EvidenceIDs) > 100 || !uniqueStrings(f.EvidenceIDs) {
			return fmt.Errorf("findings[%d].evidence_ids invalid", i)
		}
		if !factBasisSet[f.FactBasis] {
			return fmt.Errorf("findings[%d].fact_basis %q invalid", i, f.FactBasis)
		}
		if f.FactBasis == "evidence" && len(f.EvidenceIDs) < 1 {
			return fmt.Errorf("findings[%d] evidence basis without evidence_ids", i)
		}
		if f.FactBasis == "model_only" && len(f.EvidenceIDs) != 0 {
			return fmt.Errorf("findings[%d] model_only basis cites evidence", i)
		}
	}
	if len(r.ToolRequests) > 100 {
		return fmt.Errorf("tool_requests exceed 100")
	}
	seenCallIDs := map[string]bool{}
	for i, tr := range r.ToolRequests {
		if _, err := uuid.Parse(tr.ToolCallID); err != nil {
			return fmt.Errorf("tool_requests[%d].tool_call_id not a uuid", i)
		}
		if seenCallIDs[tr.ToolCallID] {
			return fmt.Errorf("tool_requests[%d].tool_call_id duplicate", i)
		}
		seenCallIDs[tr.ToolCallID] = true
		if !validToolName(tr.ToolName) {
			return fmt.Errorf("tool_requests[%d].tool_name %q invalid", i, tr.ToolName)
		}
	}
	return nil
}

// validToolName checks lowercase identifier syntax (<= 128 bytes) for requested tool names.
func validToolName(n string) bool {
	if len(n) == 0 || len(n) > 128 {
		return false
	}
	if n[0] < 'a' || n[0] > 'z' {
		return false
	}
	for i := 1; i < len(n); i++ {
		c := n[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validFindingKey(k string) bool {
	if len(k) == 0 || len(k) > 128 {
		return false
	}
	if k[0] < 'a' || k[0] > 'z' {
		return false
	}
	for i := 1; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func uniqueStrings(ss []string) bool {
	seen := map[string]bool{}
	for _, s := range ss {
		if seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
