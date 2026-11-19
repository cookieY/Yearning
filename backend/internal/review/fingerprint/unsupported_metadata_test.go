package fingerprint

import (
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"testing"
)

func TestNonTableDDLDoesNotClaimSupportedMetadata(t *testing.T) {
	for _, sql := range []string{
		"DROP VIEW private_view", "ALTER VIEW private_view RENAME TO another_view",
		"CREATE VIEW private_view AS SELECT 1",
		"DROP MATERIALIZED VIEW private_view", "DROP SEQUENCE private_sequence",
	} {
		t.Run(sql, func(t *testing.T) {
			res, err := testEngine(t).Analyze(Profile{Engine: "postgresql", Mode: "postgresql"}, []byte(sql), privacy.NewRunMapper())
			if err != nil {
				if failure, ok := err.(*Failure); !ok || failure.Code != ErrNameAnonFailed {
					t.Fatal(err)
				}
				return // Anonymization refusal is also a closed result, never complete.
			}
			if res.MetadataContextComplete {
				t.Fatal("unsupported object metadata was marked complete")
			}
		})
	}
}

func TestDropIndexRetainsIndexTargetForCoverage(t *testing.T) {
	res := analyzeObjects(t, "DROP INDEX private_index", privacy.NewRunMapper())
	if _, ok := findObject(res, "index", RelationTarget); !ok {
		t.Fatalf("missing target index: %#v", res)
	}
}
