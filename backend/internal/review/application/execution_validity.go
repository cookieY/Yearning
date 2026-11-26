package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

const executionValidityVersion = "object-preconditions-v1"

// evaluateValidityProgram is the pure-compute side: program/group mappings are supplied by the caller
// from in-memory ephemeral Run models.
func evaluateValidityProgram(ctx context.Context, program ValidityProgram, groups map[int32]string,
	binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string, refs []provider.NormalizedEvidenceRef,
) (ValidityResult, []mappedFinding, map[string]any, error) {
	return evaluateValidityProgramWithGroupLookup(ctx, program, func(ord int32) string {
		return groups[ord]
	}, binding, mapper, engine, locale, refs)
}

func evaluateValidityProgramWithGroupLookup(ctx context.Context, program ValidityProgram, groupOf func(int32) string,
	binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string, refs []provider.NormalizedEvidenceRef,
) (ValidityResult, []mappedFinding, map[string]any, error) {
	program, _ = bindValidityProgram(program, binding, mapper)
	runtimeRefs := bindValidityEvidence(refs, binding, mapper)
	facts := validityFactIndex(program, runtimeRefs)
	result, err := NewPreconditionEvaluator().Evaluate(ctx, ValidityInput{Engine: engine, Program: program, Facts: facts})
	if err != nil {
		return ValidityResult{}, nil, nil, err
	}
	programDigest, err := sha256ValidityProgram(program)
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
		"program_digest":        programDigest,
		"fact_digest":           hashutil.SHA256Hex(factsJSON),
		"schema_binding_digest": hashutil.SHA256Hex(bindingJSON),
	}
	var findings []mappedFinding
	for _, violation := range result.Violations {
		gid := groupOf(violation.Ordinal)
		if gid == "" {
			return ValidityResult{}, nil, nil, domain.ErrCoverageIncomplete
		}
		findings = append(findings, executionValidityFinding(violation, gid, locale))
	}
	return result, findings, proof, nil
}

// sha256ValidityProgram streams the exact JSON serialization of program directly into SHA-256 while caching
// marshaled StatementValidity fragments across repeated statements (avoiding multi-megabyte buffer allocations).
func sha256ValidityProgram(program ValidityProgram) (string, error) {
	h := sha256.New()
	_, _ = h.Write([]byte(`{"complete":`))
	if program.Complete {
		_, _ = h.Write([]byte("true"))
	} else {
		_, _ = h.Write([]byte("false"))
	}
	if program.Statements == nil {
		_, _ = h.Write([]byte(`,"statements":null}`))
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	_, _ = h.Write([]byte(`,"statements":[`))
	factsCache := make(map[validityMemoKey][]byte, min(len(program.Statements), 64))
	var numBuf [16]byte
	for i := range program.Statements {
		if i > 0 {
			_, _ = h.Write([]byte{','})
		}
		st := program.Statements[i]
		key := statementValidityMemoKey(st.Facts)
		factsBytes, ok := factsCache[key]
		if !ok {
			b, err := json.Marshal(st.Facts)
			if err != nil {
				return "", err
			}
			factsBytes = b
			factsCache[key] = factsBytes
		}
		_, _ = h.Write([]byte(`{"ordinal":`))
		_, _ = h.Write(strconv.AppendInt(numBuf[:0], int64(st.Ordinal), 10))
		_, _ = h.Write([]byte(`,"facts":`))
		_, _ = h.Write(factsBytes)
		_, _ = h.Write([]byte{'}'})
	}
	_, _ = h.Write([]byte(`]}`))
	return hex.EncodeToString(h.Sum(nil)), nil
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
