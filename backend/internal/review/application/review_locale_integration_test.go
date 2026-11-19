//go:build integration

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// englishCleanEvidenceDial is the English variant of cleanEvidenceDial (used on the en-US frozen path;
// a Chinese summary would be rejected with 2009 by the output locale gate).
func englishCleanEvidenceDial(req provider.Request) ([]byte, int, error) {
	if len(req.NormalizedEvidence) == 0 {
		requests, err := scopedMetadataToolRequests(req, uuid.NewString())
		if err != nil {
			return nil, 500, err
		}
		if len(requests) > 0 {
			raw, err := json.Marshal(map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "Reading table metadata before continuing.", "findings": []any{},
				"tool_requests": requests,
			})
			if err != nil {
				return nil, 500, err
			}
			return raw, 200, nil
		}
	}
	raw, err := json.Marshal(map[string]any{
		"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
		"summary": "Review complete.", "findings": []any{}, "tool_requests": []any{},
	})
	if err != nil {
		return nil, 500, err
	}
	return raw, 200, nil
}

// TestB7OutputLocaleFrozenAcrossTaskChain RCP-20261101: the legacy task chain (copying output_locale
// across sql_grouping / ai_review task inputs) is retired; on the ephemeral path, the freeze boundary
// is StartEphemeralRun input -> er.OutputLocale (normalized once and never mutated thereafter).
func TestB7OutputLocaleFrozenAcrossTaskChain(t *testing.T) {
	b := newB7Fixture(t)
	sqlText := "update orders set amount = amount + 1 where id = 1;\n"
	draft := b.createDraftWithSQL(t, sqlText)
	er, _ := b.startEphemeralTerminal(t, b.owner, b.fakeDial(englishCleanEvidenceDial), EphemeralStartInput{
		FlowID:         b.flowID,
		DraftID:        draft.ID,
		SQL:            []byte(sqlText),
		OutputLocale:   "en-GB",
		IdempotencyKey: "locale-chain-" + runKeySuffix(),
	})
	// en-GB is normalized to en-US and frozen on the ephemeral Run (remaining constant throughout the review).
	if er.OutputLocale != "en-US" {
		t.Fatalf("ephemeral run locale = %q, want en-US", er.OutputLocale)
	}
	if er.State() != domain.RunStateReady {
		t.Fatalf("run want ready, got %s (code=%s)", er.State(), ephemeralRunCodeString(er))
	}
}

func (b *b7fx) addReviewLocaleSecondStage(t *testing.T) {
	t.Helper()
	dsID := uuid.NewString()
	if err := b.db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version,
		created_by_user_id, created_at, updated_at)
		values (?, 'locale-ds2', 'mysql', 'mysql', 'native', 'locale2.example.com', 3306, 'shop',
		'8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("cd", 32), b.userID).Error; err != nil {
		t.Fatalf("seed locale datasource: %v", err)
	}
	stageID := uuid.NewString()
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: scope = default database 'shop'.
	if err := b.db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 2, ?, ?, ?, 'k1', 'aes-256-gcm')`,
		stageID, b.flowID, dsID, []byte{1}, []byte{2}).Error; err != nil {
		t.Fatalf("seed locale stage: %v", err)
	}
	stepID := uuid.NewString()
	if err := b.db.Exec(`insert into flow_approval_steps (id, flow_id, stage_id, position)
		values (?, ?, ?, 1)`, stepID, b.flowID, stageID).Error; err != nil {
		t.Fatalf("seed locale step: %v", err)
	}
	if err := b.db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`,
		stepID, b.userID).Error; err != nil {
		t.Fatalf("seed locale actor: %v", err)
	}
	if err := b.db.Exec(`insert into flow_execution_actors (stage_id, user_id) values (?, ?)`,
		stageID, b.userID).Error; err != nil {
		t.Fatalf("seed locale execution actor: %v", err)
	}
}

func TestB7OutputLocaleReachesEveryModelCall(t *testing.T) {
	tests := []struct {
		name        string
		locale      string
		wantMarker  string
		avoidMarker string
	}{
		{
			name:        "Simplified Chinese",
			locale:      "zh-CN",
			wantMarker:  "Simplified Chinese (zh-CN)",
			avoidMarker: "English (en-US)",
		},
		{
			name:        "English",
			locale:      "en-US",
			wantMarker:  "English (en-US)",
			avoidMarker: "Simplified Chinese (zh-CN)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newB7Fixture(t)
			b.addReviewLocaleSecondStage(t)
			var prompts []string
			deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
				prompts = append(prompts, req.SystemPrompt)
				withTool := len(req.NormalizedEvidence) == 0
				return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
			})
			sqlText := "update orders set amount = amount + 1 where id = 1;\n"
			draft := b.createDraftWithSQL(t, sqlText)
			er, _ := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
				FlowID:         b.flowID,
				DraftID:        draft.ID,
				SQL:            []byte(sqlText),
				OutputLocale:   tt.locale,
				IdempotencyKey: "locale-model-" + runKeySuffix(),
			})
			_ = er
			if len(prompts) < 4 {
				t.Fatalf("model calls = %d, want two stages with tool and final turns", len(prompts))
			}
			for index, prompt := range prompts {
				if !strings.Contains(prompt, tt.wantMarker) {
					t.Fatalf("model call %d did not carry frozen locale %q:\n%s",
						index+1, tt.wantMarker, prompt)
				}
				if strings.Contains(prompt, tt.avoidMarker) {
					t.Fatalf("model call %d carried conflicting locale %q:\n%s",
						index+1, tt.avoidMarker, prompt)
				}
			}
		})
	}
}

func TestB7AcceptedSummaryIsFrozenEncryptedAndReadable(t *testing.T) {
	tests := []struct {
		name          string
		locale        string
		interim       string
		wantSummaries []string
	}{
		{
			name:    "English",
			locale:  "en-US",
			interim: "Interim tool-call summary must not be accepted.",
			wantSummaries: []string{
				"Review complete for stage 1.",
				"Review complete for stage 2.",
			},
		},
		{
			name:    "Simplified Chinese",
			locale:  "zh-CN",
			interim: "Interim tool-turn summary must not be accepted.",
			wantSummaries: []string{
				"Stage 1 review complete.",
				"Stage 2 review complete.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newB7Fixture(t)
			b.addReviewLocaleSecondStage(t)
			calls := 0
			deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
				calls++
				withTool := len(req.NormalizedEvidence) == 0
				body, status, err := b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
				if err != nil {
					return nil, status, err
				}
				var value map[string]any
				if err := json.Unmarshal(body, &value); err != nil {
					return nil, 500, err
				}
				if withTool {
					value["summary"] = tt.interim
				} else if calls <= 2 {
					value["summary"] = tt.wantSummaries[0]
				} else {
					value["summary"] = tt.wantSummaries[1]
				}
				body, err = json.Marshal(value)
				return body, status, err
			})

			draft := b.createDraftWithSQL(t, "update orders set amount = amount + 1 where id = 1;\n")
			er, started := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
				FlowID:         b.flowID,
				DraftID:        draft.ID,
				SQL:            []byte("update orders set amount = amount + 1 where id = 1;\n"),
				OutputLocale:   tt.locale,
				IdempotencyKey: "accepted-summary-" + tt.locale + "-" + runKeySuffix(),
			})
			b.materializeEphemeral(t, er, draft.ID, draft.Revision)

			run, err := b.svc.GetRunOwned(context.Background(), b.owner, started)
			if err != nil {
				t.Fatal(err)
			}
			if len(run.StageResults) != 2 {
				t.Fatalf("stage results = %d, want 2", len(run.StageResults))
			}
			for i, want := range tt.wantSummaries {
				if run.StageResults[i].Summary == nil {
					t.Fatalf("stage %d summary is nil", i+1)
				}
				if got := *run.StageResults[i].Summary; got != want {
					t.Fatalf("stage %d summary = %q, want %q", i+1, got, want)
				}
				if run.StageResults[i].SummaryLocale == nil || *run.StageResults[i].SummaryLocale != tt.locale {
					t.Fatalf("stage %d summary locale = %#v, want %s", i+1, run.StageResults[i].SummaryLocale, tt.locale)
				}
			}

			var rows []struct {
				StagePosition int32
				Ciphertext    []byte
				Nonce         []byte
				KeyID         string
				Algorithm     string
			}
			if err := b.db.Raw(`
				select stage_position, normalized_context_ciphertext as ciphertext,
				       normalized_context_nonce as nonce, normalized_context_key_id as key_id,
				       normalized_context_algorithm as algorithm
				from stage_review_snapshots
				where review_run_id = ? order by stage_position`, started).Scan(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for i := range rows {
				if strings.Contains(string(rows[i].Ciphertext), tt.wantSummaries[i]) {
					t.Fatalf("stage %d ciphertext contains summary plaintext", rows[i].StagePosition)
				}
				plain, err := openPayload(b.svc.Crypter, "stage_review_snapshots",
					started+"#"+fmt.Sprint(rows[i].StagePosition), "normalized_context_ciphertext",
					crypto.Envelope{
						Ciphertext: rows[i].Ciphertext, Nonce: rows[i].Nonce,
						KeyID: rows[i].KeyID, Algorithm: rows[i].Algorithm,
					})
				if err != nil {
					t.Fatalf("open stage %d context: %v", rows[i].StagePosition, err)
				}
				var contextValue struct {
					AcceptedSummary      string `json:"accepted_summary"`
					AcceptedOutputLocale string `json:"accepted_output_locale"`
				}
				if err := json.Unmarshal(plain, &contextValue); err != nil {
					t.Fatalf("decode stage %d context: %v", rows[i].StagePosition, err)
				}
				if contextValue.AcceptedSummary != tt.wantSummaries[i] {
					t.Fatalf("stage %d encrypted summary = %q, want %q",
						rows[i].StagePosition, contextValue.AcceptedSummary, tt.wantSummaries[i])
				}
				if contextValue.AcceptedOutputLocale != tt.locale {
					t.Fatalf("stage %d accepted locale = %q, want %s",
						rows[i].StagePosition, contextValue.AcceptedOutputLocale, tt.locale)
				}
			}

			beforeReplayCalls := calls
			// Terminal replay: the equivalent entry point on the ephemeral path is the idempotency of
			// ExecuteEphemeralRun (returns immediately when already terminal without touching the Provider).
			// RCP-20261101: the legacy task replay entry (AgentRunner.Execute + TaskInput) was retired with the task chain.
			NewAgentRunner(b.svc, deps).ExecuteEphemeralRun(context.Background(), er)
			if calls != beforeReplayCalls {
				t.Fatalf("terminal replay called provider %d times, want none", calls-beforeReplayCalls)
			}
			afterReplay, err := b.svc.GetRunOwned(context.Background(), b.owner, started)
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range tt.wantSummaries {
				if afterReplay.StageResults[i].Summary == nil || *afterReplay.StageResults[i].Summary != want {
					t.Fatalf("terminal replay changed stage %d summary: %#v", i+1, afterReplay.StageResults[i].Summary)
				}
				if afterReplay.StageResults[i].SummaryLocale == nil || *afterReplay.StageResults[i].SummaryLocale != tt.locale {
					t.Fatalf("terminal replay changed stage %d summary locale: %#v", i+1, afterReplay.StageResults[i].SummaryLocale)
				}
			}
		})
	}
}

// TestB7LegacyTaskWithoutOutputLocaleDefaultsToChinese removed under RCP-20261101.
// Legacy premise: the DB task chain sealed output_locale into the ai_review task input JSON, where
// legacy task payloads written by older versions might omit the field and fall back to zh-CN via
// ResolveOutputLocale(""). On the ephemeral path there is no task payload — the locale is resolved and
// frozen directly onto er.OutputLocale from StartEphemeralRun input, and the default-to-Chinese
// semantics are covered by ResolveOutputLocale unit tests and the zh-CN branch of
// TestB7OutputLocaleReachesEveryModelCall (plus "missing defaults to Chinese" in httpapi TestRunDraftReviewFreezesOutputLocale).

// TestB7OutputLocaleIsIdempotencyIdentity RCP-20261101: ephemeral idempotency is an in-memory window
// (preventing double-click double-runs) — replaying the same key reuses the first Run with the locale
// frozen from the initial request; request-hash comparison on legacy DB idempotency records (1007)
// retired along with StartRun (consistent with post-migration httpapi semantics in TestRunDraftReviewFreezesOutputLocale replay assertions).
func TestB7OutputLocaleIsIdempotencyIdentity(t *testing.T) {
	b := newB7Fixture(t)
	sqlText := "update orders set amount = amount + 1 where id = 1;\n"
	draft := b.createDraftWithSQL(t, sqlText)
	key := "locale-idempotency-" + runKeySuffix()
	first, err := b.svc.StartEphemeralRun(context.Background(), b.owner,
		NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial)), EphemeralStartInput{
			FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText),
			OutputLocale: "en-US", IdempotencyKey: key,
		})
	if err != nil {
		t.Fatal(err)
	}
	// Same key with a different locale: replay returns the same Run (first locale remains frozen without re-execution).
	second, err := b.svc.StartEphemeralRun(context.Background(), b.owner,
		NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial)), EphemeralStartInput{
			FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText),
			OutputLocale: "zh-CN", IdempotencyKey: key,
		})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("same key must replay the first run: %s vs %s", second, first)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		er := b.svc.EphemeralGetForTest(first, b.owner.UserID)
		if er != nil && er.TerminalState() {
			if er.OutputLocale != "en-US" {
				t.Fatalf("replayed run locale = %q, want frozen en-US", er.OutputLocale)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("ephemeral run not terminal")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestB7OutputLocaleRejectsWrongLanguageBeforeToolsOrPersistence(t *testing.T) {
	for _, corrected := range []bool{false, true} {
		t.Run(map[bool]string{false: "still_wrong_fails_closed", true: "corrected_retry"}[corrected], func(t *testing.T) {
			b := newB7Fixture(t)
			calls := 0
			deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
				calls++
				if calls == 1 || !corrected {
					body, status, err := b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
					table, tableErr := firstScopedTable(req)
					if tableErr != nil {
						return nil, 500, tableErr
					}
					var value map[string]any
					if err := json.Unmarshal(body, &value); err != nil {
						t.Fatal(err)
					}
					value["summary"] = "\u5ba1\u6838\u5b8c\u6210\uff0c\u4f46\u8bed\u8a00\u4e0d\u7b26\u5408\u8981\u6c42\u3002"
					// This response is otherwise schema-valid and requests a tool.
					value["tool_requests"] = []map[string]any{{
						"tool_call_id": uuid.NewString(),
						"tool_name":    "safe_explain",
						"arguments": map[string]any{
							"schema": table.Schema, "group_id": req.AnonymizedStatementGroups[0].GroupID,
						},
					}}
					body, _ = json.Marshal(value)
					return body, status, err
				}
				if calls == 2 && !strings.Contains(req.SystemPrompt, "previous response did not meet") {
					t.Fatal("corrective turn lacks fixed language instruction")
				}
				withTool := len(req.NormalizedEvidence) == 0
				return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, withTool)(req)
			})
			draft := b.createDraftWithSQL(t, "update orders set amount = amount + 1 where id = 1;\n")
			er, started := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
				FlowID:         b.flowID,
				DraftID:        draft.ID,
				SQL:            []byte("update orders set amount = amount + 1 where id = 1;\n"),
				OutputLocale:   "en-US",
				IdempotencyKey: "wrong-locale-" + runKeySuffix(),
			})
			b.materializeEphemeral(t, er, draft.ID, draft.Revision)
			var state string
			var code *int32
			if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, started).Row().Scan(&state, &code); err != nil {
				t.Fatal(err)
			}
			// RCP-20261101 (owner ruling 3: model resilience): a wrong-locale response first gets 1 corrective
			// repair turn, followed by up to 2 schema retries within budget if still non-compliant, before
			// converging to 2009 — resulting in 4 total calls on the uncorrected path (compared to 2 calls under the legacy single-shot DB chain).
			wantCalls := 4
			if corrected {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("calls = %d, want %d", calls, wantCalls)
			}
			var findings, failedLedger, succeededLedger int64
			if err := b.db.Raw(`select count(*) from review_findings f join stage_review_snapshots s on s.id = f.stage_review_id where s.review_run_id = ?`, started).Scan(&findings).Error; err != nil {
				t.Fatal(err)
			}
			if err := b.db.Raw(`select count(*) from ai_usage_ledger where review_run_id = ? and outcome = 'schema_failed'`, started).Scan(&failedLedger).Error; err != nil {
				t.Fatal(err)
			}
			if err := b.db.Raw(`select count(*) from ai_usage_ledger where review_run_id = ? and outcome = 'succeeded'`, started).Scan(&succeededLedger).Error; err != nil {
				t.Fatal(err)
			}
			if corrected {
				if state != "blocked" || findings != 1 || failedLedger != 1 || succeededLedger != 2 ||
					b.facts.scopedMetadataCalls != 1 {
					t.Fatalf("corrected retry: state=%s findings=%d ledgers=%d/%d metadata=%d",
						state, findings, failedLedger, succeededLedger, b.facts.scopedMetadataCalls)
				}
			} else {
				if state != "failed" || code == nil || *code != 2009 || findings != 0 || failedLedger != 4 {
					t.Fatalf("wrong output must fail closed: state=%s code=%v findings=%d ledger=%d", state, code, findings, failedLedger)
				}
				if b.facts.calls != 0 || b.facts.metadataCalls != 0 ||
					b.facts.tableStatsCalls != 0 || b.facts.indexCalls != 0 ||
					b.facts.explainCalls != 0 {
					t.Fatalf("rejected locale response executed a tool: %+v", b.facts)
				}
			}
		})
	}
}
