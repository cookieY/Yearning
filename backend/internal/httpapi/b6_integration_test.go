//go:build integration

// b6_integration_test.go B6 HTTP contract tests: authentication matrix, If-Match / idempotency
// protocol, full workflow API, task read actor relationships, and Reveal no-store + auditing.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
	"github.com/cookieY/YearningX/backend/internal/workflow"
)

type b6Fixture struct {
	engine   *ginEngine
	db       *gorm.DB
	svc      *application.Services
	deps     *B6Deps
	admin    testClient
	owner    testClient
	intruder testClient
	flowID   string
	ownerID  string

	// agentMu/agentDial: swappable responder for HTTP ephemeral precheck (POST /review-runs).
	// agentOnce is claimed during fixture wiring to inject a closure Dial -- tests switch between
	// clean/high/evidence responders via setAgentDial to drive ExecuteEphemeralRun in its goroutine.
	agentMu    sync.Mutex
	agentDial  func(req provider.Request) ([]byte, int, error)
	agentFacts func(context.Context, string, application.FrozenDatasourceTarget, string) (tool.FactSource, func(), error)
}

// setAgentDial switches the Provider responder for ephemeral prechecks (nil = unconfigured, starting precheck fails).
func (fx *b6Fixture) setAgentDial(respond func(req provider.Request) ([]byte, int, error)) {
	fx.agentMu.Lock()
	defer fx.agentMu.Unlock()
	fx.agentDial = respond
}

func (fx *b6Fixture) currentAgentDial() func(req provider.Request) ([]byte, int, error) {
	fx.agentMu.Lock()
	defer fx.agentMu.Unlock()
	return fx.agentDial
}

// setAgentFacts switches the read-only fact source for ephemeral prechecks (nil = default b9FakeFacts;
// search_path suite injects real target DB facts).
func (fx *b6Fixture) setAgentFacts(
	factsFor func(context.Context, string, application.FrozenDatasourceTarget, string) (tool.FactSource, func(), error),
) {
	fx.agentMu.Lock()
	defer fx.agentMu.Unlock()
	fx.agentFacts = factsFor
}

func (fx *b6Fixture) currentAgentFacts() func(context.Context, string, application.FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
	fx.agentMu.Lock()
	defer fx.agentMu.Unlock()
	return fx.agentFacts
}

func newB6Fixture(t *testing.T) *b6Fixture {
	t.Helper()
	db := testkit.OpenPG(t)
	truncateAll(t, db)
	// B3 table list excludes review domain tables; B6 must truncate them as well.
	for _, tbl := range []string{
		"evidence_raw_payloads", "evidence_facts", "review_finding_evidence", "review_findings",
		"stage_review_snapshots", "sql_statements", "sql_fingerprint_groups", "review_runs",
		"draft_sql_payloads", "idempotency_records",
	} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	crypter := testkit.NewCrypter(t)

	logger, _ := observedLogger()
	recorder, _ := auditlog.NewRecorder(db)
	sessions, err := identity.NewSessionStore(db, identity.DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	identitySvc, err := identity.NewService(db, sessions, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	workflowSvc, err := workflow.NewService(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc, err := application.NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture uses an intentionally unreachable synthetic datasource.
	// Keep its identity deterministic; production factory coverage uses a real probe.
	installB9DatasourceIdentityProbe(t, reviewSvc)
	b6Deps := &B6Deps{Review: reviewSvc, Sessions: sessions, DB: db}
	fx := &b6Fixture{db: db, svc: reviewSvc, deps: b6Deps}
	// RCP-20261101 ephemeral precheck: claim agentOnce BEFORE NewServeRouter (which calls b6.AgentRunner()
	// when wiring assistant/diagnostics routes) and inject closure Dial -- responder is switched
	// per test via setAgentDial (b9CleanDial/b9HighDial/b6EvidenceDial), driving ExecuteEphemeralRun
	// inside the POST /review-runs goroutine.
	b6Deps.agentOnce.Do(func() {
		deps := application.AgentDeps{
			Dial: func(ep provider.Endpoint) (provider.Client, error) {
				return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
					respond := fx.currentAgentDial()
					if respond == nil {
						return nil, 500, fmt.Errorf("b6 fixture: agent responder not configured")
					}
					return respond(req)
				}), nil
			},
			FactSourceFor: func(ctx context.Context, dsID string, target application.FrozenDatasourceTarget, purpose string) (tool.FactSource, func(), error) {
				if factsFor := fx.currentAgentFacts(); factsFor != nil {
					return factsFor(ctx, dsID, target, purpose)
				}
				return &b9FakeFacts{}, func() {}, nil
			},
		}
		b6Deps.agent = application.NewAgentRunner(reviewSvc, deps)
	})
	engine := NewServeRouter(ServeDeps{
		Logger:        logger,
		Readiness:     NewReadiness(logger),
		SessionStore:  sessions,
		SessionOption: sessionOptionsB6(),
		B3:            newB3DepsForB6(db, identitySvc, sessions, workflowSvc, crypter),
		B6:            b6Deps,
	})
	fx.engine = engine
	fx.admin = testClient{t: t, engine: engine, cookies: map[string]string{}}
	fx.owner = testClient{t: t, engine: engine, cookies: map[string]string{}}
	fx.intruder = testClient{t: t, engine: engine, cookies: map[string]string{}}

	// Identity setup: admin + owner + intruder.
	if err := identitySvc.BootstrapAdmin(context.Background(), "b6-admin-pass-1"); err != nil {
		t.Fatal(err)
	}
	if _, body, _ := fx.admin.login("admin", "b6-admin-pass-1"); errCode(body) != 0 {
		t.Fatalf("admin login: %v", body)
	}
	fx.ownerID = fx.adminCreateUserB6(t, "carol", "carol-pass-123")
	intruderID := fx.adminCreateUserB6(t, "mallory", "mallory-pass-123")
	_ = intruderID
	if _, body, _ := fx.owner.login("carol", "carol-pass-123"); errCode(body) != 0 {
		t.Fatal("carol login failed")
	}
	if _, body, _ := fx.intruder.login("mallory", "mallory-pass-123"); errCode(body) != 0 {
		t.Fatal("mallory login failed")
	}

	// Flow setup: rule set + dual datasources (with identity fingerprints) + approver carol + permission group.
	ruleSetID := fx.insertRuleSet(t)
	dsID := fx.b6CreateDatasource(t, "b6-http-ds")
	fx.flowID = fx.b6CreateFlow(t, "b6-http-flow", ruleSetID, dsID, fx.ownerID)
	fx.b6CreateGrant(t, fx.ownerID, fx.flowID)
	return fx
}

// TestB7PromptToolEngineAndParameters verifies the management HTTP boundary
// forwards engine and tunable parameters instead of silently applying the
// legacy engine=all/empty-parameters defaults.
func TestB7PromptToolEngineAndParameters(t *testing.T) {
	fx := newB6Fixture(t)
	definition := map[string]any{
		"knowledge_text": "\u5355\u6b21\u53d8\u66f4\u884c\u6570\u4e0d\u5f97\u8d85\u8fc7 {{max_rows}}",
		"max_severity":   "high",
		"version":        1,
	}

	code, body, raw := fx.admin.do("POST", "/api/v4/admin/prompt-tools", map[string]any{
		"name": "http-max-rows", "state": "draft", "engine": "mysql",
		"parameters": map[string]any{"max_rows": 1000}, "definition": definition,
	}, true)
	if code != http.StatusOK || errCode(body) != 0 {
		t.Fatalf("create prompt tool: %d %s", code, raw)
	}
	created := body["data"].(map[string]any)
	if created["engine"] != "mysql" || created["parameters"].(map[string]any)["max_rows"] != float64(1000) {
		t.Fatalf("create did not preserve engine/parameters: %s", raw)
	}
	id := created["id"].(string)

	code, body, raw = fx.admin.doWithVersion("PUT", "/api/v4/admin/prompt-tools/"+id, map[string]any{
		"name": "http-max-rows", "state": "draft", "engine": "postgresql",
		"parameters": map[string]any{"max_rows": 2500}, "definition": definition,
	}, 1)
	if code != http.StatusOK || errCode(body) != 0 {
		t.Fatalf("replace prompt tool: %d %s", code, raw)
	}
	replaced := body["data"].(map[string]any)
	if replaced["engine"] != "postgresql" || replaced["parameters"].(map[string]any)["max_rows"] != float64(2500) {
		t.Fatalf("replace did not preserve engine/parameters: %s", raw)
	}
}

// TestB6FullApiFlow full workflow: draft CRUD -> SQL -> precheck -> Run/findings/evidence -> revision invalidation.
func TestB6FullApiFlow(t *testing.T) {
	fx := newB6Fixture(t)
	fx.b6EnableProvider(t)

	// Unauthorized flow -> 2014 (verified via another user path on ungranted flow).
	// Create draft.
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": fx.flowID, "title": "release-1", "description": "b6",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create draft: %d %s", code, raw)
	}
	draftID, _ := body["data"].(map[string]any)["id"].(string)
	version := int64(1)

	// Anonymous 401; unrelated user 1002 (does not leak existence).
	anon := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if c, _, _ := anon.do("GET", "/api/v4/change-drafts/"+draftID, nil, false); c != http401 {
		t.Fatalf("anon draft read want 401 got %d", c)
	}
	if c, b, r := fx.intruder.do("GET", "/api/v4/change-drafts/"+draftID, nil, false); c != 200 || errCode(b) != 1002 {
		t.Fatalf("intruder draft read want 1002 got %d %v raw=%s", c, b, r)
	}

	// PUT SQL: missing If-Match -> 1001; stale version -> 1003.
	if c, b, _ := fx.owner.do("PUT", "/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": "CREATE TABLE t (id INT);"}, true); c != 200 || errCode(b) != 1001 {
		t.Fatalf("missing if-match want 1001 got %d %v", c, b)
	}
	if c, b, _ := fx.owner.doWithVersion("PUT", "/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": "CREATE TABLE t (id INT);"}, 999); c != 200 || errCode(b) != 1003 {
		t.Fatalf("stale if-match want 1003 got %d %v", c, b)
	}
	// SQL matches fixture fake catalog (orders table exists with columns id/amount): execution precondition
	// evaluation on ephemeral path is real -- CREATE on an existing table would be blocked as critical by
	// validity check (unvalidated in legacy FakeRunner era; 3-statement shape kept to cover multi-statement grouping).
	sqlText := "UPDATE orders SET amount = amount + 1 WHERE id = 1;\nINSERT INTO orders (id) VALUES (1);\nSELECT id FROM orders WHERE id = 1;"
	code, body, raw = fx.owner.doWithVersion("PUT", "/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": sqlText}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put sql: %d %s", code, raw)
	}
	data := body["data"].(map[string]any)
	version = int64(data["version"].(float64))
	if data["state"] != "draft" || data["revision"].(float64) != 2 || data["has_sql"] != true {
		t.Fatalf("draft view after sql: %v", data)
	}

	// Reveal: visible to owner + no-store + watermark; intruder gets 1002.
	c, b, raw := fx.owner.do("POST", "/api/v4/change-drafts/"+draftID+"/sql-reveals",
		map[string]any{"purpose": "editor-load"}, true)
	if c != 200 || errCode(b) != 0 {
		t.Fatalf("reveal: %d %s", c, raw)
	}
	if !strings.Contains(raw, "watermark") || !strings.Contains(raw, "carol") {
		t.Fatalf("reveal watermark missing: %s", raw)
	}
	var audited int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'draft.sql_revealed'`).Scan(&audited).Error
	if audited == 0 {
		t.Fatal("reveal audit missing")
	}
	if c, b, _ := fx.intruder.do("POST", "/api/v4/change-drafts/"+draftID+"/sql-reveals",
		map[string]any{"purpose": "x"}, true); c != 200 || errCode(b) != 1002 {
		t.Fatalf("intruder reveal want 1002 got %d %v", c, b)
	}

	// Ephemeral precheck (RCP-20261101 two-step flow): flow_id required -> 1001; success -> run_id.
	if c, b, _ := fx.owner.doWithHeaders("POST", "/api/v4/review-runs", map[string]any{
		"draft_id": draftID, "sql": sqlText,
	}, map[string]string{
		"X-CSRF-Token": fx.owner.cookies["yearning_csrf"],
	}); c != 200 || errCode(b) != 1001 {
		t.Fatalf("missing flow_id want 1001 got %d %v", c, b)
	}
	fx.setAgentDial(fx.b6EvidenceDial())
	code, body, raw = fx.owner.doWithHeaders("POST", "/api/v4/review-runs", map[string]any{
		"flow_id": fx.flowID, "draft_id": draftID, "sql": sqlText,
	}, map[string]string{
		"Idempotency-Key": "b6-http-key-1",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("start ephemeral review: %d %s", code, raw)
	}
	runID := body["data"].(map[string]any)["run_id"].(string)
	if runID == "" {
		t.Fatalf("run_id missing: %s", raw)
	}

	// Same-key replay: returns the same run within in-memory idempotency window (prevents double-click duplicate runs).
	code, body, raw = fx.owner.doWithHeaders("POST", "/api/v4/review-runs", map[string]any{
		"flow_id": fx.flowID, "draft_id": draftID, "sql": sqlText,
	}, map[string]string{
		"Idempotency-Key": "b6-http-key-1",
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("idempotent replay: %d %s", code, raw)
	}
	if replayed := body["data"].(map[string]any)["run_id"].(string); replayed != runID {
		t.Fatalf("replay run mismatch: %s vs %s", replayed, runID)
	}

	// Run view: poll until ready + gate passed + stage results (ephemeral Run has zero task rows;
	// legacy sql_grouping task read-surface assertions retired along with DB task chain).
	var runData map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, body, raw = fx.owner.do("GET", "/api/v4/review-runs/"+runID, nil, false)
		if c != 200 || errCode(body) != 0 {
			t.Fatalf("get run: %d %s", c, raw)
		}
		runData = body["data"].(map[string]any)
		if s := runData["state"]; s == "ready" || s == "failed" || s == "partial" || s == "blocked" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run not terminal: %v", runData["state"])
		}
		time.Sleep(25 * time.Millisecond)
	}
	if runData["state"] != "ready" {
		t.Fatalf("run want ready got %v (failure=%v)", runData["state"], runData["failure_code"])
	}
	gate := runData["gate"].(map[string]any)
	if gate["passed"] != true {
		t.Fatalf("gate must pass: %v", gate)
	}
	// R003: terminal state is immutable -- repeated reads return identical version.
	v1 := int64(runData["version"].(float64))
	_, body, _ = fx.owner.do("GET", "/api/v4/review-runs/"+runID, nil, false)
	if int64(body["data"].(map[string]any)["version"].(float64)) != v1 {
		t.Fatal("terminal run must be immutable")
	}
	if c, b, _ := fx.intruder.do("GET", "/api/v4/review-runs/"+runID, nil, false); c != 200 || errCode(b) != 1002 {
		t.Fatalf("intruder run read want 1002 got %d %v", c, b)
	}

	// Findings list (evidence responder produces medium finding + evidence references).
	c, body, raw = fx.owner.do("GET", "/api/v4/review-runs/"+runID+"/findings?limit=10", nil, false)
	if c != 200 || errCode(body) != 0 {
		t.Fatalf("findings: %d %s", c, raw)
	}
	findingItems, _ := body["data"].(map[string]any)["items"].([]any)
	if len(findingItems) == 0 {
		t.Fatal("evidence dial must produce a finding")
	}
	var findingID, evidenceID string
	findingID = findingItems[0].(map[string]any)["id"].(string)

	// Evidence chain: finding -> evidence -> raw reveal.
	if findingID != "" {
		c, body, raw = fx.owner.do("GET", "/api/v4/review-findings/"+findingID+"/evidence", nil, false)
		if c != 200 || errCode(body) != 0 {
			t.Fatalf("finding evidence: %d %s", c, raw)
		}
		if items, ok := body["data"].([]any); !ok || len(items) == 0 {
			t.Fatal("evidence items missing")
		} else {
			evidenceID = items[0].(map[string]any)["id"].(string)
		}
		c, body, raw = fx.owner.do("POST", "/api/v4/review-evidence/"+evidenceID+"/raw-reveals",
			map[string]any{"purpose": "inspect"}, true)
		if c != 200 || errCode(body) != 0 {
			t.Fatalf("raw reveal: %d %s", c, raw)
		}
		// SEC-0035: copy source_reveal_id must be an ID actually revealed by this user for this evidence
		// (legacy test passed a random UUID, which conflicted with validation even before ephemeral runs).
		sourceRevealID := body["data"].(map[string]any)["reveal_id"].(string)
		c, body, raw = fx.owner.do("POST", "/api/v4/review-evidence/"+evidenceID+"/raw-copy-events",
			map[string]any{"source_reveal_id": sourceRevealID}, true)
		if c != 200 || errCode(body) != 0 {
			t.Fatalf("copy event: %d %s", c, raw)
		}
		var copies int64
		_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'review_evidence.raw_copied'`).Scan(&copies).Error
		if copies == 0 {
			t.Fatal("copy audit missing")
		}
	}

	// Revise SQL: ephemeral semantics -- Run is decoupled from draft and old conclusions no longer flip to outdated
	// (staleness is compared client-side); old Run remains readable after revision and re-precheck is user-initiated.
	code, body, raw = fx.owner.doWithVersion("PUT", "/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": "UPDATE orders SET amount = amount;"}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("revise: %d %s", code, raw)
	}
	if c, b, _ := fx.owner.do("GET", "/api/v4/review-runs/"+runID, nil, false); c != 200 || errCode(b) != 0 {
		t.Fatalf("run must stay readable after sql revise: %d %s", c, raw)
	}

	// Delete draft (cascading cleanup).
	version = int64(body["data"].(map[string]any)["version"].(float64))
	if c, b, raw := fx.owner.doWithVersion("DELETE", "/api/v4/change-drafts/"+draftID, nil, version); c != 200 || errCode(b) != 0 {
		t.Fatalf("delete draft: %d %s", c, raw)
	}
	var draftsLeft int64
	_ = fx.db.Raw(`select count(*) from change_drafts`).Scan(&draftsLeft).Error
	if draftsLeft != 0 {
		t.Fatal("draft must be deleted")
	}
}

// TestB6FlowGrantRequired creating draft on unauthorized flow -> 2014.
func TestB6FlowGrantRequired(t *testing.T) {
	fx := newB6Fixture(t)
	// mallory has no grant.
	if c, b, _ := fx.intruder.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": fx.flowID, "title": "x",
	}, true); c != 200 || errCode(b) != 2014 {
		t.Fatalf("ungranted flow want 2014 got %d %v", c, b)
	}
}

// b6EnableProvider inserts an enabled Provider row (AEAD envelope key; test Dial does not touch network).
// Ephemeral precheck Agent requires an enabled Provider under fail-closed semantics.
func (fx *b6Fixture) b6EnableProvider(t *testing.T) {
	t.Helper()
	providerID := uuid.NewString()
	env, err := fx.svc.Crypter.Seal(crypto.PurposePayload, []byte("sk-b6-test"), crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(),
		Table:        "ai_providers", PrimaryKey: providerID,
		Column: "secret_ciphertext", Purpose: "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`
		insert into ai_providers (id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at)
		values (?, 'b6-primary', 'openai_compatible', 'https://b6.example.com', 'b6-model', true,
			1, repeat('a',64), repeat('b',64), '{}'::jsonb, ?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		providerID, env.Ciphertext, env.Nonce, env.KeyID, fx.ownerID).Error; err != nil {
		t.Fatal(err)
	}
}

// b6EvidenceDial evidence-producing responder: first round returns metadata tool requests (executed via
// b9FakeFacts to produce normalized facts), second round produces a medium finding referencing the normalized
// evidence in the request (real evidence chain shape: finding.evidence_ids <- NormalizedEvidence.EvidenceID).
func (fx *b6Fixture) b6EvidenceDial() func(req provider.Request) ([]byte, int, error) {
	return func(req provider.Request) ([]byte, int, error) {
		if tools := b9MetadataToolRequests(req); len(tools) > 0 {
			raw, err := json.Marshal(map[string]any{
				"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
				"summary": "\u8bfb\u53d6\u6570\u636e\u5e93\u5143\u6570\u636e", "findings": []map[string]any{}, "tool_requests": tools,
			})
			if err != nil {
				return nil, 500, err
			}
			return raw, 200, nil
		}
		gid := ""
		if len(req.AnonymizedStatementGroups) > 0 {
			gid = req.AnonymizedStatementGroups[0].GroupID
		}
		var evidence []string
		for _, ev := range req.NormalizedEvidence {
			evidence = append(evidence, ev.EvidenceID)
		}
		raw, err := json.Marshal(map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "\u5ba1\u6838\u53d1\u73b0\u6027\u80fd\u95ee\u9898\u5e76\u9644\u5e26\u6570\u636e\u5e93\u8bc1\u636e\u3002",
			"findings": []map[string]any{{
				"finding_key": "skill.b6-evidence", "category": "performance", "severity": "medium",
				"title": "\u67e5\u8be2\u672a\u4f7f\u7528\u7d22\u5f15", "message": "\u8bed\u53e5\u65e0\u6cd5\u547d\u4e2d\u73b0\u6709\u7d22\u5f15\uff0c\u9884\u4f30\u626b\u63cf\u884c\u6570\u504f\u9ad8\u3002",
				"suggestion": "\u8865\u5145\u7d22\u5f15\u6216\u6536\u7a84\u6761\u4ef6\u3002", "fingerprint_group_ids": []string{gid},
				"evidence_ids": evidence, "fact_basis": "evidence",
			}},
			"tool_requests": []map[string]any{},
		})
		if err != nil {
			return nil, 500, err
		}
		return raw, 200, nil
	}
}

// TestB6RequestedSchedule S18.56 Option B: draft requested execution schedule --
// create validation (future RFC3339), update tri-state (set / clear / omitted unchanged),
// frozen onto order upon submission and exposed via OrderView.
func TestB6RequestedSchedule(t *testing.T) {
	fx := newB6Fixture(t)
	future := time.Now().UTC().Add(6 * time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	// create: past timestamp -> 1001.
	if c, b, _ := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": fx.flowID, "title": "sched-1",
		"requested_scheduled_for": past,
	}, true); c != 200 || errCode(b) != 1001 {
		t.Fatalf("past requested_for want 1001 got %d %v", c, b)
	}
	// create: future timestamp -> returned in view.
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": fx.flowID, "title": "sched-1",
		"requested_scheduled_for": future,
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create draft with requested_for: %d %s", code, raw)
	}
	draftID, _ := body["data"].(map[string]any)["id"].(string)
	got, _ := body["data"].(map[string]any)["requested_scheduled_for"].(string)
	if got == "" {
		t.Fatalf("draft view must expose requested_scheduled_for: %s", raw)
	}

	// update tri-state: null clears value.
	code, body, raw = fx.owner.doWithVersion("PATCH", "/api/v4/change-drafts/"+draftID,
		map[string]any{"requested_scheduled_for": nil}, 1)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("clear requested_for: %d %s", code, raw)
	}
	if v, _ := body["data"].(map[string]any)["requested_scheduled_for"]; v != nil {
		t.Fatalf("cleared requested_for must be null, got %v", v)
	}
	// update: set to a new future value.
	future2 := time.Now().UTC().Add(36 * time.Hour).Format(time.RFC3339)
	code, body, raw = fx.owner.doWithVersion("PATCH", "/api/v4/change-drafts/"+draftID,
		map[string]any{"requested_scheduled_for": future2}, 2)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("set requested_for: %d %s", code, raw)
	}
	// update: omitted field leaves value unchanged (title-only update).
	code, body, raw = fx.owner.doWithVersion("PATCH", "/api/v4/change-drafts/"+draftID,
		map[string]any{"title": "sched-1-renamed"}, 3)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("title-only update: %d %s", code, raw)
	}
	if v, _ := body["data"].(map[string]any)["requested_scheduled_for"].(string); v == "" {
		t.Fatalf("absent field must leave requested_for unchanged: %s", raw)
	}

	// Assertions for freezing onto order upon submission live in B9 test TestB9RequestedScheduleFreeze
	// (b9 fixture has ready chain and submit shortcut).
}
