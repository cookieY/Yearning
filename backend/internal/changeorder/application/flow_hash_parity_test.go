package application

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	reviewedFlowSnapshotDatabaseJSON = `{"flow_id":"00000000-0000-0000-0000-000000000001","flow_version":7,"stages":[{"position":1,"datasource_id":"00000000-0000-0000-0000-000000000002","datasource_engine":"postgresql","datasource_compatibility_mode":"postgresql","datasource_host":"db.internal","datasource_port":5432,"datasource_database_name":"analytics","datasource_identity_fingerprint":"fingerprint-a","approval_step_actors":["user-a","user-b"],"execution_actors":null}]}`
	reviewedFlowSnapshotDatabaseHash = "e741e6479beb59a56adad4c4a465f6734eaadd007176ba7924df1eff97e693ca"

	reviewedFlowSnapshotNilDatabaseJSON = `{"flow_id":"00000000-0000-0000-0000-000000000001","flow_version":7,"stages":[{"position":1,"datasource_id":"00000000-0000-0000-0000-000000000002","datasource_engine":"postgresql","datasource_compatibility_mode":"postgresql","datasource_host":"db.internal","datasource_port":5432,"datasource_database_name":"","datasource_identity_fingerprint":"fingerprint-a","approval_step_actors":["user-a","user-b"],"execution_actors":null}]}`
	reviewedFlowSnapshotNilDatabaseHash = "e083878ace9165301ada2c5b04417704463e873430e6d80cd6b7588eb25b7f9c"

	reviewedFlowSnapshotModeChangedHash     = "6458871952f2cfcf86dec59aff36a963dd8f5ab7f104dd0f6c928375e2190ae1"
	reviewedFlowSnapshotHostChangedHash     = "a893a39dfef7296a92b64e325eea3a00a7d34a8ed6816fd60b0c53e157b63f3f"
	reviewedFlowSnapshotPortChangedHash     = "8e574e5e55fc9d52fb2eb11640a81c19af6f1fe2cff0b59fcd7cf0a5066466cd"
	reviewedFlowSnapshotDatabaseChangedHash = "da9844607ba3dcf272f56e84fe98db63893ca77444f765987efa4cbc37e0ebfb"
)

func TestComputeFlowSnapshotHashMatchesReviewedFrozenBytes(t *testing.T) {
	if got := frozenFixtureHash(reviewedFlowSnapshotDatabaseJSON); got != reviewedFlowSnapshotDatabaseHash {
		t.Fatalf("frozen database fixture hash = %s, want %s", got, reviewedFlowSnapshotDatabaseHash)
	}
	if got := frozenFixtureHash(reviewedFlowSnapshotNilDatabaseJSON); got != reviewedFlowSnapshotNilDatabaseHash {
		t.Fatalf("frozen nil-database fixture hash = %s, want %s",
			got, reviewedFlowSnapshotNilDatabaseHash)
	}

	for _, tc := range []struct {
		name         string
		databaseName *string
		want         string
	}{
		{
			name:         "database name",
			databaseName: stringPointerForHashParity("analytics"),
			want:         reviewedFlowSnapshotDatabaseHash,
		},
		{
			name: "nil database name",
			want: reviewedFlowSnapshotNilDatabaseHash,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := reviewedHashParityStage()
			stage.DatabaseName = tc.databaseName
			got, err := computeFlowSnapshotHash(
				reviewedFlowIDForHashParity, 7, []stageFact{stage},
				map[int][]string{1: {"user-a", "user-b"}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("flow hash = %s, want reviewed frozen hash %s", got, tc.want)
			}
		})
	}
}

func TestComputeFlowSnapshotHashIncludesEveryStageConfigField(t *testing.T) {
	baseStage := reviewedHashParityStage()
	approval := map[int][]string{1: {"user-a", "user-b"}}
	base, err := computeFlowSnapshotHash(
		reviewedFlowIDForHashParity, 7, []stageFact{baseStage}, approval,
	)
	if err != nil {
		t.Fatal(err)
	}
	if base != reviewedFlowSnapshotDatabaseHash {
		t.Fatalf("base flow hash = %s, want %s", base, reviewedFlowSnapshotDatabaseHash)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*stageFact)
		want   string
	}{
		{
			name: "compatibility mode",
			mutate: func(stage *stageFact) {
				stage.CompatibilityMode = "mysql"
			},
			want: reviewedFlowSnapshotModeChangedHash,
		},
		{
			name: "host",
			mutate: func(stage *stageFact) {
				stage.Host = "replica.internal"
			},
			want: reviewedFlowSnapshotHostChangedHash,
		},
		{
			name: "port",
			mutate: func(stage *stageFact) {
				stage.Port = 6543
			},
			want: reviewedFlowSnapshotPortChangedHash,
		},
		{
			name: "database name",
			mutate: func(stage *stageFact) {
				stage.DatabaseName = stringPointerForHashParity("warehouse")
			},
			want: reviewedFlowSnapshotDatabaseChangedHash,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := reviewedHashParityStage()
			tc.mutate(&stage)
			got, err := computeFlowSnapshotHash(
				reviewedFlowIDForHashParity, 7, []stageFact{stage}, approval,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("%s change did not alter flow hash %s", tc.name, got)
			}
			if got != tc.want {
				t.Fatalf("%s flow hash = %s, want %s", tc.name, got, tc.want)
			}
		})
	}
}

const reviewedFlowIDForHashParity = "00000000-0000-0000-0000-000000000001"

func reviewedHashParityStage() stageFact {
	return stageFact{
		StageID:           "00000000-0000-0000-0000-000000000003",
		Position:          1,
		DatasourceID:      "00000000-0000-0000-0000-000000000002",
		Engine:            "postgresql",
		CompatibilityMode: "postgresql",
		Host:              "db.internal",
		Port:              5432,
		DatabaseName:      stringPointerForHashParity("analytics"),
		IdentityFP:        stringPointerForHashParity("fingerprint-a"),
		Name:              "analytics-primary",
	}
}

func stringPointerForHashParity(value string) *string {
	return &value
}

func frozenFixtureHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum)
}

func TestEffectiveChangeReviewToolHashMatchesFrozenPolicyContract(t *testing.T) {
	got := effectiveChangeReviewToolHash(
		[]string{"tool-b", "tool-a"},
		[]knowledgePolicyIdentity{
			{ID: "k-2", ConfigHash: strings.Repeat("b", 64)},
			{ID: "k-1", ConfigHash: strings.Repeat("a", 64)},
		},
	)
	const want = "888384165d619bd3796b1c34087ed2ccbd90f1584b089218d75bb1093ea25a80"
	if got != want {
		t.Fatalf("submission tool hash = %s, want frozen-policy hash %s", got, want)
	}
}

func TestBuildFrozenFlowIncludesFlowAndStageNames(t *testing.T) {
	svc := &Services{}
	stage := reviewedHashParityStage()
	doc, err := svc.buildFrozenFlow(
		reviewedFlowIDForHashParity,
		"release-flow",
		7,
		nil,
		[]stageFact{stage},
		map[int][]flowStepActors{1: {{Position: 1, Actors: []string{"user-a"}}}},
		map[string][]string{stage.StageID: {"user-exec"}},
	)
	if err != nil {
		t.Fatalf("buildFrozenFlow failed: %v", err)
	}
	if doc.FlowName != "release-flow" || len(doc.Stages) != 1 || doc.Stages[0].Name != "analytics-primary" {
		t.Fatalf("expected flow_name and stage name in frozen document, got %+v", doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal frozen flow: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal frozen flow: %v", err)
	}
	if decoded["flow_name"] != "release-flow" {
		t.Fatalf("encoded JSON flow_name = %v, want release-flow", decoded["flow_name"])
	}
	stages, ok := decoded["stages"].([]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("encoded JSON stages = %v", decoded["stages"])
	}
	stMap := stages[0].(map[string]any)
	if stMap["name"] != "analytics-primary" {
		t.Fatalf("encoded JSON stage name = %v, want analytics-primary", stMap["name"])
	}
}
