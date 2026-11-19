package application

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

const executionValidityVersion = "object-preconditions-v1"

// loadValidityProgram reads each original occurrence. Fingerprint grouping is
// deliberately excluded from ordering and object-state evaluation.
func loadValidityProgram(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel) (ValidityProgram, map[int32]string, error) {
	var rows []struct {
		Ordinal int32
		Typed   []byte
		GroupID string
	}
	err := db.WithContext(ctx).Raw(`
		select s.ordinal, s.typed_shape::text as typed, g.id::text as group_id
		from sql_statements s
		join sql_fingerprint_groups g on g.review_run_id = s.review_run_id
		 and g.canonical_fingerprint_hmac = s.canonical_fingerprint_hmac
		 and g.dialect_fingerprint_hmac = s.dialect_fingerprint_hmac
		where s.review_run_id = ? order by s.ordinal`, run.ID).Scan(&rows).Error
	if err != nil {
		return ValidityProgram{}, nil, err
	}
	program := ValidityProgram{Complete: len(rows) == int(run.StatementCount)}
	groups := make(map[int32]string, len(rows))
	for i, row := range rows {
		var shape struct {
			Validity fingerprint.StatementValidity `json:"execution_validity"`
		}
		if err := json.Unmarshal(row.Typed, &shape); err != nil {
			return ValidityProgram{}, nil, err
		}
		if row.Ordinal != int32(i+1) || row.GroupID == "" {
			program.Complete = false
		}
		program.Statements = append(program.Statements, StatementValidityAtOrdinal{Ordinal: row.Ordinal, Facts: shape.Validity})
		groups[row.Ordinal] = row.GroupID
	}
	return program, groups, nil
}

func evaluateStageValidity(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel, binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string, refs []provider.NormalizedEvidenceRef) (ValidityResult, []mappedFinding, map[string]any, error) {
	program, groups, err := loadValidityProgram(ctx, db, run)
	if err != nil {
		return ValidityResult{}, nil, nil, err
	}
	return evaluateValidityProgram(ctx, program, groups, binding, mapper, engine, locale, refs)
}

// evaluateValidityProgram is the pure-compute side: program/group mappings are supplied by the caller
// (DB path via loadValidityProgram; ephemeral Run constructed from in-memory models — RCP-20261101).
func evaluateValidityProgram(ctx context.Context, program ValidityProgram, groups map[int32]string,
	binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string, refs []provider.NormalizedEvidenceRef,
) (ValidityResult, []mappedFinding, map[string]any, error) {
	program, _ = bindValidityProgram(program, binding, mapper)
	runtimeRefs := bindValidityEvidence(refs, binding, mapper)
	facts := validityFactIndex(program, runtimeRefs)
	result, err := NewPreconditionEvaluator().Evaluate(ctx, ValidityInput{Engine: engine, Program: program, Facts: facts})
	if err != nil {
		return ValidityResult{}, nil, nil, err
	}
	programJSON, err := json.Marshal(program)
	if err != nil {
		return ValidityResult{}, nil, nil, err
	}
	factsJSON, err := json.Marshal(runtimeRefs)
	if err != nil {
		return ValidityResult{}, nil, nil, err
	}
	var bindingSet any
	if binding != nil {
		// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: bindings no longer carry logical-to-physical mappings;
		// the evidence summary pins the allowed physical schema set (deterministic map-key serialization).
		bindingSet = binding.schemas
	}
	bindingJSON, err := json.Marshal(bindingSet)
	if err != nil {
		return ValidityResult{}, nil, nil, err
	}
	proof := map[string]any{
		"version":               executionValidityVersion,
		"scope":                 "modeled_object_existence_preconditions",
		"status":                result.Status,
		"program_digest":        sha256Hex(programJSON),
		"fact_digest":           sha256Hex(factsJSON),
		"schema_binding_digest": sha256Hex(bindingJSON),
	}
	var findings []mappedFinding
	for _, violation := range result.Violations {
		gid := groups[violation.Ordinal]
		if gid == "" {
			return ValidityResult{}, nil, nil, domain.ErrCoverageIncomplete
		}
		findings = append(findings, executionValidityFinding(violation, gid, locale))
	}
	return result, findings, proof, nil
}

// executionValidityFinding constructs a deterministic finding for an execution precondition violation.
// The title states directly "which object had what problem and which statement therefore cannot execute"
// (owner ruling 2026-10-30: titles must not stop at abstract labels like "execution precondition not met"
// that require reading the body to understand), the message preserves the full facts of "required state /
// proven state / source", and the recommendation provides a clear next-step path. Severity, category,
// cause, and evidence bindings remain invariant to wording changes.
func executionValidityFinding(v PreconditionViolation, groupID, locale string) mappedFinding {
	states := map[string]string{"present": "\u5b58\u5728", "absent": "\u4e0d\u5b58\u5728", "unknown": "\u672a\u77e5"}
	var title, message, suggestion string
	if locale == "en-US" {
		// Titles lead with the concrete defect; the generic shape only
		// remains for state combinations outside present/absent/unknown
		// fast paths (kept future-proof for new validity states).
		switch {
		case v.Required == fingerprint.ValidityRequirementPresent && v.Observed == ValidityFactAbsent:
			title = fmt.Sprintf("Object %s does not exist; statement %d cannot execute", v.Object.Token, v.Ordinal)
		case v.Required == fingerprint.ValidityRequirementPresent && v.Observed == ValidityFactUnknown:
			title = fmt.Sprintf("Object %s state is unknown; statement %d cannot execute safely", v.Object.Token, v.Ordinal)
		default:
			title = fmt.Sprintf("Statement %d cannot execute: object %s must be %s, observed %s", v.Ordinal, v.Object.Token, v.Required, v.Observed)
		}
		message = fmt.Sprintf("Statement %d requires object %s to be %s. Observed metadata and preceding statement effects prove it is %s, so the statement cannot execute.", v.Ordinal, v.Object.Token, v.Required, v.Observed)
		suggestion = "Confirm the target object and statement order, then correct the SQL, create the missing object or reorder statements, and run the review again."
	} else {
		switch {
		case v.Required == fingerprint.ValidityRequirementPresent && v.Observed == ValidityFactAbsent:
			title = fmt.Sprintf("\u5bf9\u8c61 %s \u4e0d\u5b58\u5728\uff0c\u7b2c %d \u6761\u8bed\u53e5\u65e0\u6cd5\u6267\u884c", v.Object.Token, v.Ordinal)
		case v.Required == fingerprint.ValidityRequirementPresent && v.Observed == ValidityFactUnknown:
			title = fmt.Sprintf("\u5bf9\u8c61 %s \u72b6\u6001\u672a\u77e5\uff0c\u7b2c %d \u6761\u8bed\u53e5\u65e0\u6cd5\u6267\u884c", v.Object.Token, v.Ordinal)
		default:
			title = fmt.Sprintf("\u7b2c %d \u6761\u8bed\u53e5\u65e0\u6cd5\u6267\u884c\uff1a\u5bf9\u8c61 %s \u9700\u4e3a\u201c%s\u201d\uff0c\u5b9e\u9645\u4e3a\u201c%s\u201d", v.Ordinal, v.Object.Token, states[string(v.Required)], states[string(v.Observed)])
		}
		message = fmt.Sprintf("\u7b2c %d \u6761\u8bed\u53e5\u7684\u5bf9\u8c61 %s \u8981\u6c42\u72b6\u6001\u4e3a\u201c%s\u201d\uff0c\u771f\u5b9e\u5143\u6570\u636e\u53ca\u6b64\u524d\u8bed\u53e5\u6548\u679c\u8bc1\u660e\u72b6\u6001\u4e3a\u201c%s\u201d\uff0c\u65e0\u6cd5\u6267\u884c\u3002", v.Ordinal, v.Object.Token, states[string(v.Required)], states[string(v.Observed)])
		suggestion = "\u8bf7\u5148\u786e\u8ba4\u76ee\u6807\u5bf9\u8c61\u4e0e\u8bed\u53e5\u987a\u5e8f\u662f\u5426\u7b26\u5408\u9884\u671f\uff1a\u4fee\u6b63 SQL\u3001\u8865\u5efa\u7f3a\u5931\u5bf9\u8c61\u6216\u8c03\u6574\u8bed\u53e5\u987a\u5e8f\u540e\uff0c\u91cd\u65b0\u8fd0\u884c AI \u9884\u5ba1\u3002"
	}
	return mappedFinding{cause: "proven_invalid_execution", groupID: groupID, category: "correctness", severity: domain.SeverityCritical,
		title: title, message: message, suggest: suggestion, evidence: append([]string(nil), v.EvidenceIDs...)}
}
