package application

import "testing"

func TestFindingProvenanceClosedKindsAndAnonymousSources(t *testing.T) {
	for _, tc := range []struct{ kind, key, wantKind, wantKey string }{
		{"policy_finding", "skill.safe-id", "policy_finding", "skill.safe-id"},
		{"proven_invalid_execution", "private_table", "proven_invalid_execution", ""},
		{"agent_observation", "private_table", "agent_observation", ""},
		{"invented", "private_table", "agent_observation", ""},
	} {
		f := mappedFinding{cause: tc.kind, sourceKey: tc.key, groupID: "group-id", evidence: []string{"evidence-id"}}
		p := findingProvenance("finding-id", f)
		if p["cause"] != tc.wantKind || p["source_key"] != tc.wantKey || p["finding_id"] != "finding-id" {
			t.Fatalf("unsafe provenance: %+v", p)
		}
		p["evidence_ids"].([]string)[0] = "changed"
		if f.evidence[0] != "evidence-id" {
			t.Fatal("provenance mutated evidence")
		}
	}
}
