//go:build integration

package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// The model intentionally omits all findings. Technical blocking must derive
// from observed catalog facts and the ordered program, independently of prose.
func TestCriticalValidityBlocksProvenInvalidExecution(t *testing.T) {
	cases := []struct{ name, sql, existence, want string }{
		{"missing drop", "DROP TABLE orders;", "absent", "blocked"},
		{"conditional missing drop", "DROP TABLE IF EXISTS orders;", "absent", "ready"},
		{"missing update", "UPDATE orders SET amount=1 WHERE id=1;", "absent", "blocked"},
		{"missing alter", "ALTER TABLE orders ADD COLUMN extra INT;", "absent", "blocked"},
		{"duplicate create", "CREATE TABLE orders (id INT);", "present", "blocked"},
		{"create then drop", "CREATE TABLE orders (id INT); DROP TABLE orders;", "absent", "ready"},
		{"create with local primary key", "CREATE TABLE orders (id INT, PRIMARY KEY (id));", "absent", "ready"},
		{"create with local unique constraint", "CREATE TABLE orders (id INT, CONSTRAINT uq UNIQUE (id));", "absent", "ready"},
		{"create with self foreign key", "CREATE TABLE orders (id INT PRIMARY KEY, parent_id INT, FOREIGN KEY (parent_id) REFERENCES orders(id));", "absent", "ready"},
		{"drop then create", "DROP TABLE orders; CREATE TABLE orders (id INT);", "absent", "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newB7Fixture(t)
			source := &metadataContextFactSource{fakeFactSource: b.facts, fact: map[string]any{
				"existence": tc.existence, "metadata_complete": true, "truncated": false,
				"columns": []any{},
			}}
			deps := b.metadataContextDeps(source, func(req provider.Request) ([]byte, int, error) {
				if !strings.Contains(req.SystemPrompt, "identity_match=matched_at_freeze") {
					t.Fatal("controlled frozen target identity missing from actual Provider request")
				}
				if len(req.NormalizedEvidence) == 0 {
					requests, err := scopedMetadataToolRequests(req, repeatedProviderToolID)
					if err != nil {
						t.Fatal(err)
					}
					return completeWithTools(req, requests)
				}
				return completeWithTools(req, nil)
			})
			runID := b.runMetadataContextReview(t, deps, tc.sql)
			run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
			if err != nil || run.State != tc.want {
				t.Fatalf("state=%+v err=%v want=%s", run, err, tc.want)
			}
			if tc.want == "blocked" {
				if run.Gate.Passed || len(run.StageResults) != 1 || run.StageResults[0].HighestSeverity != "critical" || run.StageResults[0].FindingCount == 0 {
					t.Fatalf("proven invalid execution was not Critical: %+v", run)
				}
			}
		})
	}
}

func TestCriticalValidityCreateThenQualifiedDropIsReady(t *testing.T) {
	b := newB7Fixture(t)
	if err := b.db.Exec(`update datasources set database_name = 'shop' where id in
		(select datasource_id from flow_stages where flow_id = ?)`, b.flowID).Error; err != nil {
		t.Fatal(err)
	}
	source := &metadataContextFactSource{
		fakeFactSource: b.facts,
		fact: map[string]any{
			"existence": "absent", "metadata_complete": true, "truncated": false,
			"columns": []any{},
		},
	}
	deps := b.metadataContextDeps(source, func(req provider.Request) ([]byte, int, error) {
		if len(req.NormalizedEvidence) == 0 {
			requests, err := scopedMetadataToolRequests(req, repeatedProviderToolID)
			if err != nil {
				t.Fatal(err)
			}
			return completeWithTools(req, requests)
		}
		return completeWithTools(req, nil)
	})
	runID := b.runMetadataContextReview(
		t, deps, "CREATE TABLE orders (id INT); DROP TABLE shop.orders;",
	)
	run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "ready" || !run.Gate.Passed {
		t.Fatalf("create then physical-qualified drop = %+v, want ready and passed", run)
	}
	if len(run.StageResults) != 1 ||
		run.StageResults[0].HighestSeverity == "critical" ||
		run.StageResults[0].FindingCount != 0 {
		t.Fatalf("create then physical-qualified drop produced a Critical finding: %+v",
			run.StageResults)
	}
}

func TestCriticalValiditySystemFindingUsesFrozenLocale(t *testing.T) {
	for _, locale := range []string{"zh-CN", "en-US"} {
		t.Run(locale, func(t *testing.T) {
			b := newB7Fixture(t)
			source := &metadataContextFactSource{fakeFactSource: b.facts, fact: absentTableMetadataFact("shop", "orders")}
			deps := b.metadataContextDeps(source, func(req provider.Request) ([]byte, int, error) {
				requests := []map[string]any(nil)
				if len(req.NormalizedEvidence) == 0 {
					var err error
					requests, err = scopedMetadataToolRequests(req, repeatedProviderToolID)
					if err != nil {
						t.Fatal(err)
					}
				}
				body, status, err := completeWithTools(req, requests)
				if locale == "en-US" {
					var v map[string]any
					if err := json.Unmarshal(body, &v); err != nil {
						t.Fatal(err)
					}
					v["summary"] = "Review completed"
					body, err = json.Marshal(v)
				}
				return body, status, err
			})
			// RCP-20261101: ephemeral driver + materialization; finding/snapshot assertion surfaces remain unchanged.
			draft := b.createDraftWithSQL(t, "DROP TABLE orders;")
			er, started := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
				FlowID:         b.flowID,
				DraftID:        draft.ID,
				SQL:            []byte("DROP TABLE orders;"),
				OutputLocale:   locale,
				IdempotencyKey: "critical-locale-" + runKeySuffix(),
			})
			b.materializeEphemeral(t, er, draft.ID, draft.Revision)
			findings, _, err := b.svc.ListRunFindings(context.Background(), b.owner, started, 100, "")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(findings)
			if err != nil {
				t.Fatal(err)
			}
			want := "\u65e0\u6cd5\u6267\u884c"
			if locale == "en-US" {
				want = "cannot execute"
			}
			if !strings.Contains(string(encoded), want) || !strings.Contains(string(encoded), "critical") {
				t.Fatalf("localized Critical absent: %s", encoded)
			}
			snapshots, err := b.svc.loadStageSnapshots(context.Background(), b.db, started)
			if err != nil || len(snapshots) != 1 {
				t.Fatalf("stage snapshot unavailable: %v", err)
			}
			snap := snapshots[0]
			contextValue, err := openStageReviewContext(b.svc.Crypter, started, snap.StagePosition, crypto.Envelope{
				Ciphertext: snap.NormalizedContextCiphertext, Nonce: snap.NormalizedContextNonce,
				KeyID: snap.NormalizedContextKeyID, Algorithm: snap.NormalizedContextAlgorithm,
			})
			if err != nil {
				t.Fatal(err)
			}
			provenance, ok := contextValue["finding_provenance"].([]any)
			if !ok || len(provenance) == 0 {
				t.Fatal("durable Critical provenance missing")
			}
			for _, value := range provenance {
				item := value.(map[string]any)
				if item["cause"] != "proven_invalid_execution" || item["finding_id"] == "" || item["source_key"] != "" {
					t.Fatalf("incorrect system provenance: %+v", item)
				}
			}
		})
	}
}
