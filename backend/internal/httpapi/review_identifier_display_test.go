//go:build integration

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type identifierDisplayMappingRow struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	Algorithm  string
}

func identifierDisplayReplaceMapping(
	t *testing.T,
	fx *b9Fixture,
	runID string,
	pairs ...privacy.MappingPair,
) {
	t.Helper()
	if err := fx.db.Exec(
		`delete from review_identifier_mappings where review_run_id = ?`, runID,
	).Error; err != nil {
		t.Fatalf("delete identifier mapping: %v", err)
	}
	env, err := privacy.SealMapping(fx.svc.Crypter, privacy.EnvelopeBinding{
		ReviewRunID: runID, StatementIdx: 1,
	}, pairs)
	if err != nil {
		t.Fatalf("seal identifier mapping: %v", err)
	}
	if err := fx.db.Exec(`
		insert into review_identifier_mappings (
			id, review_run_id, statement_idx, identifier_mapping_ciphertext,
			identifier_mapping_nonce, identifier_mapping_key_id,
			identifier_mapping_algorithm, mapping_entry_count, created_at
		) values (?, ?, 1, ?, ?, ?, 'aes-256-gcm', ?, ?)`,
		uuid.NewString(), runID, env.Ciphertext, env.Nonce, env.KeyID,
		len(pairs), time.Now().UTC(),
	).Error; err != nil {
		t.Fatalf("insert identifier mapping: %v", err)
	}
}

func identifierDisplayStageRow(t *testing.T, fx *b9Fixture, runID string) (string, string) {
	t.Helper()
	var row struct {
		ID           string
		SnapshotHash string
	}
	if err := fx.db.Raw(`
		select id::text as id, snapshot_hash
		from stage_review_snapshots
		where review_run_id = ? and stage_position = 1`, runID).Scan(&row).Error; err != nil {
		t.Fatalf("read stage snapshot: %v", err)
	}
	if row.ID == "" {
		t.Fatalf("stage snapshot missing for run %s", runID)
	}
	return row.ID, row.SnapshotHash
}

func identifierDisplaySeedSummary(t *testing.T, fx *b9Fixture, runID, summary string) {
	t.Helper()
	var row struct {
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Algorithm  string
	}
	if err := fx.db.Raw(`
		select normalized_context_ciphertext as ciphertext,
		       normalized_context_nonce as nonce,
		       normalized_context_key_id as key_id,
		       normalized_context_algorithm as algorithm
		from stage_review_snapshots
		where review_run_id = ? and stage_position = 1`, runID).Scan(&row).Error; err != nil {
		t.Fatalf("read stage context envelope: %v", err)
	}
	aad := crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(),
		Table:        "stage_review_snapshots",
		PrimaryKey:   runID + "#1",
		Column:       "normalized_context_ciphertext",
		Purpose:      crypto.PurposePayload,
	}
	plain, err := fx.svc.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce,
		KeyID: row.KeyID, Algorithm: row.Algorithm,
	}, aad)
	if err != nil {
		t.Fatalf("open stage context: %v", err)
	}
	contextValue := map[string]any{}
	if err := json.Unmarshal(plain, &contextValue); err != nil {
		t.Fatalf("decode stage context: %v", err)
	}
	contextValue["accepted_summary"] = summary
	contextValue["accepted_output_locale"] = "zh-CN"
	sealed, err := fx.svc.Crypter.Seal(crypto.PurposePayload, mustJSON(t, contextValue), aad)
	if err != nil {
		t.Fatalf("seal stage context: %v", err)
	}
	if err := fx.db.Exec(`
		update stage_review_snapshots
		set normalized_context_ciphertext = ?,
		    normalized_context_nonce = ?,
		    normalized_context_key_id = ?,
		    normalized_context_algorithm = ?
		where review_run_id = ? and stage_position = 1`,
		sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.Algorithm, runID,
	).Error; err != nil {
		t.Fatalf("write stage context: %v", err)
	}
}

func identifierDisplayInsertFinding(t *testing.T, fx *b9Fixture, stageID string) string {
	t.Helper()
	id := uuid.NewString()
	if err := fx.db.Exec(`
		insert into review_findings (
			id, stage_review_id, category, severity, title, message, suggestion,
			model_confidence, created_at
		) values (?, ?, 'correctness', 'medium', ?, ?, ?, 0.9300, ?)`,
		id, stageID,
		"table_1 risk",
		"compare table_10, keep :string, column_99",
		"check column_1",
		time.Now().UTC(),
	).Error; err != nil {
		t.Fatalf("insert review finding: %v", err)
	}
	return id
}

func identifierDisplayRawGet(t *testing.T, client testClient, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := newJSONRequest("GET", path, nil)
	cookies := make([]string, 0, len(client.cookies))
	for name, value := range client.cookies {
		cookies = append(cookies, name+"="+value)
	}
	if len(cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(cookies, "; "))
	}
	rec := newRecorder()
	client.engine.ServeHTTP(rec, req)
	return rec
}

func identifierDisplayDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return body
}

func identifierDisplayData(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("response data missing: %#v", body)
	}
	return data
}

func identifierDisplayItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	items, ok := identifierDisplayData(t, body)["items"].([]any)
	if !ok {
		t.Fatalf("response items missing: %#v", body)
	}
	return items
}

func identifierDisplayAuditCount(t *testing.T, fx *b9Fixture, runID, surface string) int64 {
	t.Helper()
	var count int64
	if err := fx.db.Raw(`
		select count(*)
		from audit_events
		where event_type = 'review.identifier_displayed'
		  and resource_id = ?
		  and metadata->>'surface' = ?`, runID, surface).Scan(&count).Error; err != nil {
		t.Fatalf("count identifier display audits: %v", err)
	}
	return count
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestIdentifierDisplayAutomaticRestore verifies R014 end to end against
// PostgreSQL. It exercises the three authorized read surfaces while proving
// that storage, authorization, Run scope, audit and Provider boundaries do not
// change.
func TestIdentifierDisplayAutomaticRestore(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID, _ := order["id"].(string)
	var run1 string
	if err := fx.db.Raw(`
		select snap.review_run_id::text
		from order_stages os
		join stage_review_snapshots snap on snap.id = os.stage_review_snapshot_id
		where os.order_id = ?`, orderID).Scan(&run1).Error; err != nil {
		t.Fatalf("read order review run: %v", err)
	}
	stage1, snapshotHashBefore := identifierDisplayStageRow(t, fx, run1)

	// Second Run follows the same materialization path (RCP-20261101: ephemeral precheck writes zero DB rows,
	// and submission persists DB rows; two submitted orders = two independent DB Runs with unchanged Run-scope isolation).
	order2 := fx.b9ReadySubmit(t)
	orderID2, _ := order2["id"].(string)
	var run2 string
	if err := fx.db.Raw(`
		select snap.review_run_id::text
		from order_stages os
		join stage_review_snapshots snap on snap.id = os.stage_review_snapshot_id
		where os.order_id = ?`, orderID2).Scan(&run2).Error; err != nil {
		t.Fatalf("read second order review run: %v", err)
	}
	identifierDisplayStageRow(t, fx, run2)

	identifierDisplayReplaceMapping(t, fx, run1,
		privacy.MappingPair{Kind: privacy.KindTable, Token: "table_1", Original: "customer_ledger_q7"},
		privacy.MappingPair{Kind: privacy.KindTable, Token: "table_10", Original: "payment_events_x9"},
		privacy.MappingPair{Kind: privacy.KindColumn, Token: "column_1", Original: "risk_status_k3"},
		privacy.MappingPair{Kind: privacy.KindSchema, Token: "schema_1", Original: "private_schema_unreferenced"},
	)
	identifierDisplayReplaceMapping(t, fx, run2,
		privacy.MappingPair{Kind: privacy.KindTable, Token: "table_1", Original: "archive_orders_z4"},
	)
	identifierDisplaySeedSummary(t, fx, run1,
		"check table_1 and table_10; unknown column_99, placeholder :string, adjacent(table_1).")
	identifierDisplaySeedSummary(t, fx, run2, "check table_1.")
	identifierDisplayInsertFinding(t, fx, stage1)

	var saved identifierDisplayMappingRow
	if err := fx.db.Raw(`
		select identifier_mapping_ciphertext as ciphertext,
		       identifier_mapping_nonce as nonce,
		       identifier_mapping_key_id as key_id,
		       identifier_mapping_algorithm as algorithm
		from review_identifier_mappings
		where review_run_id = ? and statement_idx = 1`, run1).Scan(&saved).Error; err != nil {
		t.Fatalf("read saved mapping: %v", err)
	}

	dialBeforeReads := fx.dialCount.Load()

	t.Run("run summary and findings restore", func(t *testing.T) {
		rec := identifierDisplayRawGet(t, fx.owner, "/api/v4/review-runs/"+run1)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("get run: code=%d cache=%q body=%s",
				rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
		body := identifierDisplayDecode(t, rec)
		if errCode(body) != 0 {
			t.Fatalf("get run error: %s", rec.Body.String())
		}
		data := identifierDisplayData(t, body)
		stages := data["stage_results"].([]any)
		stage := stages[0].(map[string]any)
		summary, _ := stage["summary"].(string)
		if !strings.Contains(summary, "customer_ledger_q7") ||
			!strings.Contains(summary, "payment_events_x9") ||
			!strings.Contains(summary, "unknown column_99") ||
			!strings.Contains(summary, "placeholder :string") ||
			!strings.Contains(summary, "adjacent(customer_ledger_q7).") {
			t.Fatalf("summary not restored exactly: %q", summary)
		}
		for _, forbidden := range []string{"table_1", "table_10", "private_schema_unreferenced"} {
			if strings.Contains(rec.Body.String(), forbidden) {
				t.Fatalf("run response exposed forbidden value %q: %s", forbidden, rec.Body.String())
			}
		}

		rec = identifierDisplayRawGet(t, fx.owner, "/api/v4/review-runs/"+run1+"/findings")
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("list run findings: code=%d cache=%q body=%s",
				rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
		body = identifierDisplayDecode(t, rec)
		items := identifierDisplayItems(t, body)
		if len(items) != 1 {
			t.Fatalf("run finding count = %d: %s", len(items), rec.Body.String())
		}
		finding := items[0].(map[string]any)
		if finding["title"] != "customer_ledger_q7 risk" ||
			finding["message"] != "compare payment_events_x9, keep :string, column_99" ||
			finding["suggestion"] != "check risk_status_k3" {
			t.Fatalf("run finding not restored: %#v", finding)
		}
		if strings.Contains(rec.Body.String(), "private_schema_unreferenced") {
			t.Fatalf("run finding exposed unreferenced mapping: %s", rec.Body.String())
		}
	})

	t.Run("frozen order findings honor actor relationships", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			client testClient
		}{
			{name: "submitter", client: fx.owner},
			{name: "reviewer", client: fx.reviewer1},
			{name: "executor", client: fx.executor},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec := identifierDisplayRawGet(t, tc.client,
					"/api/v4/change-orders/"+orderID+"/review-findings")
				if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("order findings: code=%d cache=%q body=%s",
						rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
				}
				body := identifierDisplayDecode(t, rec)
				items := identifierDisplayItems(t, body)
				if len(items) != 1 {
					t.Fatalf("order finding count = %d: %s", len(items), rec.Body.String())
				}
				finding := items[0].(map[string]any)
				if finding["title"] != "customer_ledger_q7 risk" ||
					finding["message"] != "compare payment_events_x9, keep :string, column_99" ||
					finding["suggestion"] != "check risk_status_k3" {
					t.Fatalf("frozen finding not restored: %#v", finding)
				}
				if strings.Contains(rec.Body.String(), "private_schema_unreferenced") {
					t.Fatalf("order finding exposed unreferenced mapping: %s", rec.Body.String())
				}
			})
		}

		for _, tc := range []struct {
			name   string
			client testClient
		}{
			{name: "intruder", client: fx.intruder},
			{name: "unrelated admin", client: fx.admin},
		} {
			t.Run(tc.name+" denied", func(t *testing.T) {
				code, body, raw := tc.client.do("GET",
					"/api/v4/change-orders/"+orderID+"/review-findings", nil, false)
				if code != 200 || errCode(body) != 1002 {
					t.Fatalf("unrelated actor must get 1002: %d %s", code, raw)
				}
			})
		}
	})

	t.Run("run scope and historical compatibility", func(t *testing.T) {
		code, body, raw := fx.owner.do("GET", "/api/v4/review-runs/"+run2, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("get second run: %d %s", code, raw)
		}
		data := body["data"].(map[string]any)
		stage := data["stage_results"].([]any)[0].(map[string]any)
		if stage["summary"] != "check archive_orders_z4." {
			t.Fatalf("cross-Run token collision: %v", stage["summary"])
		}

		if err := fx.db.Exec(
			`delete from review_identifier_mappings where review_run_id = ?`, run2,
		).Error; err != nil {
			t.Fatalf("delete historical mapping: %v", err)
		}
		auditBefore := identifierDisplayAuditCount(t, fx, run2, "review_summary")
		code, body, raw = fx.owner.do("GET", "/api/v4/review-runs/"+run2, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("get run without mapping: %d %s", code, raw)
		}
		data = body["data"].(map[string]any)
		stage = data["stage_results"].([]any)[0].(map[string]any)
		if stage["summary"] != "check table_1." {
			t.Fatalf("missing mapping must preserve token: %v", stage["summary"])
		}
		if got := identifierDisplayAuditCount(t, fx, run2, "review_summary"); got != auditBefore {
			t.Fatalf("unrestored read wrote audit: %d -> %d", auditBefore, got)
		}

		if err := fx.db.Exec(
			`delete from review_identifier_mappings where review_run_id = ?`, run1,
		).Error; err != nil {
			t.Fatalf("delete first Run mapping: %v", err)
		}
		findingAuditBefore := identifierDisplayAuditCount(t, fx, run1, "review_findings")
		orderAuditBefore := identifierDisplayAuditCount(t, fx, run1, "order_findings")
		code, body, raw = fx.owner.do("GET", "/api/v4/review-runs/"+run1+"/findings", nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("get findings without mapping: %d %s", code, raw)
		}
		finding := identifierDisplayItems(t, body)[0].(map[string]any)
		if finding["title"] != "table_1 risk" ||
			finding["message"] != "compare table_10, keep :string, column_99" ||
			finding["suggestion"] != "check column_1" {
			t.Fatalf("missing mapping must preserve finding tokens: %#v", finding)
		}
		code, body, raw = fx.owner.do("GET",
			"/api/v4/change-orders/"+orderID+"/review-findings", nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("get order findings without mapping: %d %s", code, raw)
		}
		finding = identifierDisplayItems(t, body)[0].(map[string]any)
		if finding["title"] != "table_1 risk" ||
			finding["message"] != "compare table_10, keep :string, column_99" ||
			finding["suggestion"] != "check column_1" {
			t.Fatalf("missing mapping must preserve frozen finding tokens: %#v", finding)
		}
		if got := identifierDisplayAuditCount(t, fx, run1, "review_findings"); got != findingAuditBefore {
			t.Fatalf("unrestored finding read wrote audit: %d -> %d", findingAuditBefore, got)
		}
		if got := identifierDisplayAuditCount(t, fx, run1, "order_findings"); got != orderAuditBefore {
			t.Fatalf("unrestored order read wrote audit: %d -> %d", orderAuditBefore, got)
		}
		if err := fx.db.Exec(`
			insert into review_identifier_mappings (
				id, review_run_id, statement_idx, identifier_mapping_ciphertext,
				identifier_mapping_nonce, identifier_mapping_key_id,
				identifier_mapping_algorithm, mapping_entry_count, created_at
			) values (?, ?, 1, ?, ?, ?, ?, 4, ?)`,
			uuid.NewString(), run1, saved.Ciphertext, saved.Nonce, saved.KeyID,
			saved.Algorithm, time.Now().UTC(),
		).Error; err != nil {
			t.Fatalf("restore first Run mapping: %v", err)
		}
	})

	t.Run("audit metadata and authorization", func(t *testing.T) {
		for _, surface := range []string{"review_summary", "review_findings"} {
			if got := identifierDisplayAuditCount(t, fx, run1, surface); got != 1 {
				t.Fatalf("%s audit count = %d, want 1", surface, got)
			}
		}
		if got := identifierDisplayAuditCount(t, fx, run1, "order_findings"); got != 3 {
			t.Fatalf("order_findings audit count = %d, want 3", got)
		}
		var metadataRaw string
		if err := fx.db.Raw(`
			select metadata::text
			from audit_events
			where event_type = 'review.identifier_displayed'
			  and resource_id = ?
			  and metadata->>'surface' = 'review_findings'
			limit 1`, run1).Scan(&metadataRaw).Error; err != nil {
			t.Fatalf("read audit metadata: %v", err)
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
			t.Fatalf("decode audit metadata: %v", err)
		}
		if len(metadata) != 2 || metadata["surface"] != "review_findings" ||
			metadata["restored_count"] != float64(3) {
			t.Fatalf("audit metadata changed: %#v", metadata)
		}
		for _, forbidden := range []string{
			"customer_ledger_q7", "payment_events_x9", "risk_status_k3",
			"table_1", "table_10", "column_1",
		} {
			if strings.Contains(metadataRaw, forbidden) {
				t.Fatalf("audit metadata leaks %q: %s", forbidden, metadataRaw)
			}
		}

		for _, tc := range []struct {
			name   string
			client testClient
		}{
			{name: "intruder", client: fx.intruder},
			{name: "unrelated admin", client: fx.admin},
		} {
			for _, path := range []string{
				"/api/v4/review-runs/" + run1,
				"/api/v4/review-runs/" + run1 + "/findings",
			} {
				code, body, raw := tc.client.do("GET", path, nil, false)
				if code != 200 || errCode(body) != 1002 {
					t.Fatalf("%s %s must get 1002: %d %s", tc.name, path, code, raw)
				}
			}
		}
	})

	t.Run("audit failure denies plaintext", func(t *testing.T) {
		callback := "test:deny_identifier_display_audit"
		if err := fx.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			event, ok := tx.Statement.Dest.(*auditlog.AuditEventModel)
			if ok && event.EventType == "review.identifier_displayed" {
				tx.AddError(errors.New("injected audit failure"))
			}
		}); err != nil {
			t.Fatalf("register audit failure callback: %v", err)
		}
		defer fx.db.Callback().Create().Remove(callback)

		for _, tc := range []struct {
			surface string
			path    string
		}{
			{surface: "review_summary", path: "/api/v4/review-runs/" + run1},
			{surface: "review_findings", path: "/api/v4/review-runs/" + run1 + "/findings"},
			{surface: "order_findings", path: "/api/v4/change-orders/" + orderID + "/review-findings"},
		} {
			auditBefore := identifierDisplayAuditCount(t, fx, run1, tc.surface)
			rec := identifierDisplayRawGet(t, fx.owner, tc.path)
			if rec.Code != 500 {
				t.Fatalf("%s audit failure status = %d, want 500: %s",
					tc.surface, rec.Code, rec.Body.String())
			}
			for _, forbidden := range []string{"customer_ledger_q7", "payment_events_x9", "risk_status_k3"} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Fatalf("%s audit failure leaked %q: %s", tc.surface, forbidden, rec.Body.String())
				}
			}
			if got := identifierDisplayAuditCount(t, fx, run1, tc.surface); got != auditBefore {
				t.Fatalf("%s failed audit count changed: %d -> %d", tc.surface, auditBefore, got)
			}
		}
	})

	t.Run("corrupt mapping fails closed", func(t *testing.T) {
		if err := fx.db.Exec(`
			update review_identifier_mappings
			set identifier_mapping_ciphertext = '\x00'::bytea
			where review_run_id = ? and statement_idx = 1`, run1).Error; err != nil {
			t.Fatalf("corrupt mapping: %v", err)
		}
		for _, tc := range []struct {
			surface string
			path    string
		}{
			{surface: "review_summary", path: "/api/v4/review-runs/" + run1},
			{surface: "review_findings", path: "/api/v4/review-runs/" + run1 + "/findings"},
			{surface: "order_findings", path: "/api/v4/change-orders/" + orderID + "/review-findings"},
		} {
			rec := identifierDisplayRawGet(t, fx.owner, tc.path)
			if rec.Code != 500 {
				t.Fatalf("%s corrupt mapping status = %d, want 500: %s",
					tc.surface, rec.Code, rec.Body.String())
			}
			for _, forbidden := range []string{"customer_ledger_q7", "payment_events_x9", "risk_status_k3"} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Fatalf("%s corrupt mapping leaked %q: %s", tc.surface, forbidden, rec.Body.String())
				}
			}
		}
		if err := fx.db.Exec(`
			update review_identifier_mappings
			set identifier_mapping_ciphertext = ?,
			    identifier_mapping_nonce = ?,
			    identifier_mapping_key_id = ?,
			    identifier_mapping_algorithm = ?
			where review_run_id = ? and statement_idx = 1`,
			saved.Ciphertext, saved.Nonce, saved.KeyID, saved.Algorithm, run1,
		).Error; err != nil {
			t.Fatalf("restore mapping: %v", err)
		}
	})

	t.Run("oversize restored summary stays anonymous", func(t *testing.T) {
		longName := strings.Repeat("\u754c", 4001)
		identifierDisplayReplaceMapping(t, fx, run1,
			privacy.MappingPair{Kind: privacy.KindTable, Token: "table_1", Original: longName},
		)
		identifierDisplaySeedSummary(t, fx, run1, "table_1")
		auditBefore := identifierDisplayAuditCount(t, fx, run1, "review_summary")
		rec := identifierDisplayRawGet(t, fx.owner, "/api/v4/review-runs/"+run1)
		if rec.Code != 200 {
			t.Fatalf("oversize summary status = %d: %s", rec.Code, rec.Body.String())
		}
		body := identifierDisplayDecode(t, rec)
		stage := identifierDisplayData(t, body)["stage_results"].([]any)[0].(map[string]any)
		if stage["summary"] != "table_1" {
			t.Fatalf("oversize restored summary must remain anonymous: %v", stage["summary"])
		}
		if strings.Contains(rec.Body.String(), longName) {
			t.Fatal("oversize restored summary exposed the identifier")
		}
		if got := identifierDisplayAuditCount(t, fx, run1, "review_summary"); got != auditBefore {
			t.Fatalf("anonymous oversize read wrote audit: %d -> %d", auditBefore, got)
		}
		if err := fx.db.Exec(`
			update review_identifier_mappings
			set identifier_mapping_ciphertext = ?,
			    identifier_mapping_nonce = ?,
			    identifier_mapping_key_id = ?,
			    identifier_mapping_algorithm = ?,
			    mapping_entry_count = 4
			where review_run_id = ? and statement_idx = 1`,
			saved.Ciphertext, saved.Nonce, saved.KeyID, saved.Algorithm, run1,
		).Error; err != nil {
			t.Fatalf("restore mapping after oversize case: %v", err)
		}
	})

	t.Run("persisted anonymous results and provider boundary unchanged", func(t *testing.T) {
		var stored struct {
			Title      string
			Message    string
			Suggestion string
		}
		if err := fx.db.Raw(`
			select title, message, suggestion
			from review_findings
			where stage_review_id = ?`, stage1).Scan(&stored).Error; err != nil {
			t.Fatalf("read stored finding: %v", err)
		}
		if stored.Title != "table_1 risk" ||
			stored.Message != "compare table_10, keep :string, column_99" ||
			stored.Suggestion != "check column_1" {
			t.Fatalf("stored finding mutated: %#v", stored)
		}
		if _, snapshotHashAfter := identifierDisplayStageRow(t, fx, run1); snapshotHashAfter != snapshotHashBefore {
			t.Fatalf("stage snapshot hash changed: %s -> %s", snapshotHashBefore, snapshotHashAfter)
		}
		if got := fx.dialCount.Load(); got != dialBeforeReads {
			t.Fatalf("review reads invoked Provider: before=%d after=%d", dialBeforeReads, got)
		}
	})
}
