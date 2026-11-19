package legacy

import (
	"encoding/json"
	"fmt"
	"os"
)

// StatusEvidence mirrors api/migration/legacy-current-status-evidence.json.
// This file is valid strictly against the frozen legacy source commit; mappings derive from recorded evidence rather than guesswork.
type StatusEvidence struct {
	ContractVersion int             `json:"contract_version"`
	SourceCommit    string          `json:"source_commit"`
	CoreSQLOrders   []StatusMapping `json:"core_sql_orders"`
	CoreQueryOrders []StatusMapping `json:"core_query_orders"`
}

// StatusMapping represents the evidence-backed mapping for a single legacy status code.
type StatusMapping struct {
	LegacyValue          int      `json:"legacy_value"`
	LegacyLabel          string   `json:"legacy_label"`
	Activity             string   `json:"activity"` // terminal | active
	TargetValue          string   `json:"target_value"`
	Confidence           string   `json:"confidence"` // verified | ambiguous
	Evidence             []string `json:"evidence"`
	VerificationRequired string   `json:"verification_required,omitempty"`
}

// LoadStatusEvidence loads and parses the status evidence contract file.
func LoadStatusEvidence(path string) (*StatusEvidence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read status evidence: %w", err)
	}
	var ev StatusEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("parse status evidence: %w", err)
	}
	return &ev, nil
}

// StatusOutcome is the classification result for a single legacy ticket's status.
type StatusOutcome struct {
	// Kind: mapped (deterministic terminal state), active (in-flight work), unknown (status outside recorded evidence),
	// or ambiguous (terminal state requiring row-level evidence to adjudicate).
	Kind   string
	State  string // v4 terminal state when Kind == mapped
	Reason string // Brief classification rationale (contains no business data)
}

const (
	StatusMapped    = "mapped"
	StatusActive    = "active"
	StatusUnknown   = "unknown"
	StatusAmbiguous = "ambiguous"
)

// ClassifySQLOrder classifies a legacy SQL ticket status using rules frozen from legacy-current-status-evidence.json:
// active states (2/3/5) always block Apply; verified terminal states (0/6) map directly;
// ambiguous terminal states (1/4/7) must be adjudicated using row-level evidence, otherwise the row is marked ambiguous and blocks Apply.
func (e *StatusEvidence) ClassifySQLOrder(status int64) StatusOutcome {
	for _, m := range e.CoreSQLOrders {
		if int64(m.LegacyValue) != status {
			continue
		}
		if m.Activity == "active" {
			return StatusOutcome{Kind: StatusActive, Reason: fmt.Sprintf("sql status %d is active work", status)}
		}
		if m.Confidence == "verified" {
			return StatusOutcome{Kind: StatusMapped, State: m.TargetValue, Reason: fmt.Sprintf("sql status %d verified as %s", status, m.LegacyLabel)}
		}
		return StatusOutcome{Kind: StatusAmbiguous, Reason: fmt.Sprintf("sql status %d (%s) needs row evidence", status, m.LegacyLabel)}
	}
	return StatusOutcome{Kind: StatusUnknown, Reason: fmt.Sprintf("sql status %d outside evidence", status)}
}

// ClassifyQueryOrder classifies a legacy query order status.
func (e *StatusEvidence) ClassifyQueryOrder(status int64) StatusOutcome {
	for _, m := range e.CoreQueryOrders {
		if int64(m.LegacyValue) != status {
			continue
		}
		if m.Activity == "active" {
			return StatusOutcome{Kind: StatusActive, Reason: fmt.Sprintf("query status %d is active work", status)}
		}
		// Legacy query status 3 carries "legacy_ambiguous_reason" semantics in the evidence: maps to a terminal state while preserving the ambiguity reason.
		if m.Confidence == "verified" {
			return StatusOutcome{Kind: StatusMapped, State: m.TargetValue, Reason: fmt.Sprintf("query status %d verified as %s", status, m.LegacyLabel)}
		}
		return StatusOutcome{Kind: StatusAmbiguous, Reason: fmt.Sprintf("query status %d (%s) needs row evidence", status, m.LegacyLabel)}
	}
	return StatusOutcome{Kind: StatusUnknown, Reason: fmt.Sprintf("query status %d outside evidence", status)}
}

// MappingHash computes the canonical JCS hash of the status mappings for inclusion in the Manifest.
func (e *StatusEvidence) MappingHash() (string, error) {
	sqlList := make([]any, 0, len(e.CoreSQLOrders))
	for _, m := range e.CoreSQLOrders {
		sqlList = append(sqlList, statusMappingJCS(m))
	}
	queryList := make([]any, 0, len(e.CoreQueryOrders))
	for _, m := range e.CoreQueryOrders {
		queryList = append(queryList, statusMappingJCS(m))
	}
	return JCSHash(map[string]any{"core_sql_orders": sqlList, "core_query_orders": queryList})
}

func statusMappingJCS(m StatusMapping) map[string]any {
	return map[string]any{
		"legacy_value": int64(m.LegacyValue),
		"legacy_label": m.LegacyLabel,
		"activity":     m.Activity,
		"target_value": m.TargetValue,
		"confidence":   m.Confidence,
	}
}

// ManifestStatusEntry is an element of the Manifest.status_mappings array.
type ManifestStatusEntry struct {
	LegacyValue          any      `json:"legacy_value"`
	TargetValue          string   `json:"target_value"`
	Evidence             []string `json:"evidence"`
	Confidence           string   `json:"confidence"`
	LegacyReasonFallback *string  `json:"legacy_reason_fallback,omitempty"`
}

// ManifestStatusMappings summarizes the status mappings across both domains along with their canonical hash.
type ManifestStatusMappings struct {
	CoreSQLOrders   []ManifestStatusEntry `json:"core_sql_orders"`
	CoreQueryOrders []ManifestStatusEntry `json:"core_query_orders"`
	MappingHash     string                `json:"mapping_hash"`
}

// BuildManifestStatusMappings constructs Manifest status mapping entries from the loaded evidence.
func (e *StatusEvidence) ManifestStatusMappings() (ManifestStatusMappings, error) {
	out := ManifestStatusMappings{}
	for _, m := range e.CoreSQLOrders {
		target := m.TargetValue
		if m.Activity == "active" {
			target = "blocked_active_work"
		}
		out.CoreSQLOrders = append(out.CoreSQLOrders, ManifestStatusEntry{
			LegacyValue: int64(m.LegacyValue), TargetValue: target,
			Evidence: m.Evidence, Confidence: m.Confidence,
		})
	}
	for _, m := range e.CoreQueryOrders {
		target := m.TargetValue
		if m.Activity == "active" {
			target = "blocked_active_work"
		}
		out.CoreQueryOrders = append(out.CoreQueryOrders, ManifestStatusEntry{
			LegacyValue: int64(m.LegacyValue), TargetValue: target,
			Evidence: m.Evidence, Confidence: m.Confidence,
		})
	}
	hash, err := e.MappingHash()
	if err != nil {
		return out, err
	}
	out.MappingHash = hash
	return out, nil
}
