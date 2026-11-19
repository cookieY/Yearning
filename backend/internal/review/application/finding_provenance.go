package application

// findingProvenance is encrypted with the stage context and bound to the
// existing finding ID. It records the runtime classification without extending
// the public finding schema or storing model-supplied source identifiers.
func findingProvenance(id string, f mappedFinding) map[string]any {
	cause, sourceKey := f.cause, ""
	switch cause {
	case "policy_finding":
		sourceKey = f.sourceKey // assigned only after frozen mandatory-key lookup
	case "proven_invalid_execution":
	default:
		cause = "agent_observation"
	}
	return map[string]any{"finding_id": id, "cause": cause, "source_key": sourceKey,
		"fingerprint_group_id": f.groupID, "evidence_ids": append([]string(nil), f.evidence...)}
}
