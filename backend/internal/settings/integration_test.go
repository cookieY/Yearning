//go:build integration

// integration_test.go B12 settings domain acceptance tests (S006): typed validation (unknown keys,
// forbidden keys, value ranges), high-impact replacement requiring confirmation token, and audit trail -> revision history.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func itestDB(t *testing.T) *gorm.DB {
	t.Helper()
	password := osGetenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured")
	}
	db, err := gorm.Open(postgres.Open(fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envDef("YEARNING_TEST_DATABASE_HOST", "localhost"),
		envDef("YEARNING_TEST_DATABASE_PORT", "54330"),
		envDef("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		envDef("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"))),
		&gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	return db
}

func newTestService(t *testing.T) (*gorm.DB, *Service) {
	t.Helper()
	db := itestDB(t)
	db.Exec(`delete from audit_events where event_type = 'settings.replaced'`)
	db.Exec(`delete from outbox_events where event_type = 'settings.replaced'`)
	db.Exec(`delete from system_settings where namespace in ('query','general')`)
	ring, err := crypto.LoadKeyring(writeKeyring(t))
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return db, New(db, crypter)
}

func TestValidateTypedNamespaces(t *testing.T) {
	// Reject unknown key.
	if _, _, err := Validate("query", []byte(`{"nope":1}`)); err == nil {
		t.Fatal("unknown key accepted")
	}
	// Forbidden field (product quota category).
	for _, ns := range []string{"query", "ai-budget"} {
		if _, _, err := Validate(ns, []byte(`{"maximum_rows":100}`)); err == nil && ns == "query" {
			t.Fatal("forbidden field accepted")
		}
	}
	// Value range check.
	if _, _, err := Validate("query", []byte(`{"default_timeout_ms":999999}`)); err == nil {
		t.Fatal("range violation accepted")
	}
	// Forbidden key in ai-budget.
	if _, _, err := Validate("ai-budget", []byte(`{"per_order_cost_limit":5}`)); err == nil {
		t.Fatal("per-order budget accepted")
	}
	// Enum validation.
	if _, _, err := Validate("general", []byte(`{"default_locale":"fr-FR"}`)); err == nil {
		t.Fatal("bad locale accepted")
	}
	// Valid input.
	norm, impacts, err := Validate("query", []byte(`{"approval_enabled":false,"default_timeout_ms":60000}`))
	if err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if norm["default_timeout_ms"].(int64) != 60000 {
		t.Fatal("normalization lost")
	}
	if impacts["approval_enabled"] != "high" {
		t.Fatalf("impact = %v", impacts)
	}
	// Schema surface.
	schema, err := SchemaJSON("execution")
	if err != nil || !strings.Contains(schema, "restriction_enabled") {
		t.Fatalf("schema: %v", err)
	}
}

func TestReplaceHighImpactRequiresToken(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	adminID := "00000000-0000-0000-0000-000000000001"
	otherID := "00000000-0000-0000-0000-000000000002"
	db.Exec(`delete from system_settings where namespace = 'query'`)
	// Reject high-impact change without token.
	if _, err := s.Replace(ctx, ReplaceInput{
		Namespace: "query", Settings: json.RawMessage(`{"approval_enabled":false}`),
		ExpectVersion: 1, ActorID: adminID, ActorUsername: "admin",
	}); err == nil {
		t.Fatal("high-impact without token accepted")
	}
	// Assess -> token -> success (default approval_enabled=true -> false: existing grants remain unchanged).
	assess, err := s.Assess(ctx, "query", json.RawMessage(`{"approval_enabled":false}`), adminID)
	if err != nil {
		t.Fatal(err)
	}
	if assess.Impact.Level != "high" || len(assess.Impact.ChangedPaths) == 0 {
		t.Fatalf("assessment: %+v", assess.Impact)
	}
	if len(assess.Impact.Effects) != 1 ||
		assess.Impact.Effects[0].Kind != "active_query_grants" ||
		assess.Impact.Effects[0].Consequence != "unchanged" {
		t.Fatalf("effects: %+v", assess.Impact.Effects)
	}
	// Token is bound to the admin session: reject when presented by another admin.
	if _, err := s.Replace(ctx, ReplaceInput{
		Namespace: "query", Settings: json.RawMessage(`{"approval_enabled":false}`),
		ExpectVersion: assess.CurrentVersion, ActorID: otherID, ActorUsername: "other",
		ImpactToken: assess.ImpactToken,
	}); err == nil {
		t.Fatal("token bound to another session accepted")
	}
	view, err := s.Replace(ctx, ReplaceInput{
		Namespace: "query", Settings: json.RawMessage(`{"approval_enabled":false}`),
		ExpectVersion: assess.CurrentVersion, ActorID: adminID, ActorUsername: "admin",
		ImpactToken: assess.ImpactToken,
	})
	if err != nil {
		t.Fatalf("replace with token: %v", err)
	}
	if v, _ := view.Settings["approval_enabled"].(bool); v {
		t.Fatal("setting not applied")
	}
	// Reject tampered token.
	if _, err := s.Replace(ctx, ReplaceInput{
		Namespace: "query", Settings: json.RawMessage(`{"approval_enabled":true}`),
		ExpectVersion: view.Version, ActorID: adminID, ActorUsername: "admin",
		ImpactToken: assess.ImpactToken,
	}); err == nil {
		t.Fatal("token for different proposal accepted")
	}
	// false -> true: active direct-access grants are revoked immediately.
	on, err := s.Assess(ctx, "query", json.RawMessage(`{"approval_enabled":true}`), adminID)
	if err != nil {
		t.Fatal(err)
	}
	if len(on.Impact.Effects) != 1 || on.Impact.Effects[0].Consequence != "revoked" {
		t.Fatalf("enable effects: %+v", on.Impact.Effects)
	}
	// Revision history (audit trail; Page projection fields).
	revisions, next, err := s.ListRevisions(ctx, "query", 10, "")
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions: %v (next=%v)", err, next)
	}
	if revisions[0].ChangedBy != adminID {
		t.Fatalf("revision changed_by = %s", revisions[0].ChangedBy)
	}
	if len(revisions[0].ChangedPaths) == 0 ||
		revisions[0].ChangedPaths[0] != "/approval_enabled" {
		t.Fatalf("revision changed_paths = %v", revisions[0].ChangedPaths)
	}
	if revisions[0].ImpactSummary == nil || revisions[0].ImpactSummary.Level != "high" {
		t.Fatalf("revision impact_summary = %+v", revisions[0].ImpactSummary)
	}
	_ = db
}

// TestImpactTokenExpiry verifies TTL=300s (PRD 10 §4): expired tokens are rejected
// and non-expired tokens are accepted. Service.Now is injectable.
func TestImpactTokenExpiry(t *testing.T) {
	_, s := newTestService(t)
	ctx := context.Background()
	adminID := "00000000-0000-0000-0000-000000000001"
	assess, err := s.Assess(ctx, "query", json.RawMessage(`{"approval_enabled":false}`), adminID)
	if err != nil {
		t.Fatal(err)
	}
	// Expired after 301 seconds.
	s.Now = func() (n time.Time) { return assessNow(assess.ExpiresAt).Add(301 * time.Second) }
	if err := s.VerifyImpactToken(assess.ImpactToken, "query",
		json.RawMessage(`{"approval_enabled":false}`), assess.CurrentVersion, adminID); err == nil {
		t.Fatal("expired token accepted")
	}
	// Accepted before expiration (299 seconds).
	s.Now = func() (n time.Time) { return assessNow(assess.ExpiresAt).Add(-301 * time.Second) }
	if err := s.VerifyImpactToken(assess.ImpactToken, "query",
		json.RawMessage(`{"approval_enabled":false}`), assess.CurrentVersion, adminID); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func assessNow(expiresAt string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		panic(err)
	}
	return parsed
}

// TestSchemaViewFields verifies that schema responses include schema_version and ui_hints,
// sourced from the same field table as Validate.
func TestSchemaViewFields(t *testing.T) {
	_, s := newTestService(t)
	ctx := context.Background()
	view, err := s.SchemaView(ctx, "ai-budget")
	if err != nil {
		t.Fatal(err)
	}
	if view.Namespace != "ai-budget" || view.SchemaVersion < 1 {
		t.Fatalf("schema view: %+v", view)
	}
	if !strings.Contains(string(view.JSONSchema), "daily_budget_minor") {
		t.Fatal("json_schema missing field")
	}
	hint, ok := view.UIHints["enforced"].(map[string]any)
	if !ok || hint["impact"] != "immediate" {
		t.Fatalf("ui_hints: %+v", view.UIHints)
	}
}

// TestExecutionEffectsRealCount verifies that effects for high-impact execution fields
// reflect the actual count of unstarted scheduled executions.
func TestExecutionEffectsRealCount(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	db.Exec(`delete from execution_schedules where state = 'scheduled'`)
	db.Exec(`insert into execution_schedules (id, order_id, stage_id, scheduled_by_user_id_snapshot,
		scheduled_by_username_snapshot, scheduled_for, state, version, created_at, updated_at) values
		('11111111-1111-1111-1111-111111111111','31111111-1111-1111-1111-111111111111',
		 '21111111-1111-1111-1111-111111111111','00000000-0000-0000-0000-000000000001','admin',
		 now(), 'scheduled', 1, now(), now()),
		('12111111-1111-1111-1111-111111111111','32111111-1111-1111-1111-111111111111',
		 '22111111-1111-1111-1111-111111111111','00000000-0000-0000-0000-000000000001','admin',
		 now(), 'cancelled', 1, now(), now())`)
	defer db.Exec(`delete from execution_schedules where id in
		('11111111-1111-1111-1111-111111111111','12111111-1111-1111-1111-111111111111')`)
	assess, err := s.Assess(ctx, "execution", json.RawMessage(
		`{"restriction_enabled":true,"windows":[],"osc":{"tool":"none","defaults":{}}}`),
		"00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if assess.Impact.Level != "high" {
		t.Fatalf("level = %s", assess.Impact.Level)
	}
	if len(assess.Impact.Effects) != 1 ||
		assess.Impact.Effects[0].Kind != "scheduled_executions" ||
		assess.Impact.Effects[0].Count != 1 ||
		assess.Impact.Effects[0].Consequence != "applies_immediately" {
		t.Fatalf("effects: %+v", assess.Impact.Effects)
	}
}

// TestFirstWriteViaGetVersion (review L8 evidence): GET synthetic view has version=1 ->
// first write with If-Match: 1 must succeed (CAS insert path); inserted version is ExpectVersion+1
// (=2) so that any successful write advances past the client's held version, blocking replays with the same If-Match.
func TestFirstWriteViaGetVersion(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	db.Exec(`delete from system_settings where namespace = 'general'`)
	view, err := s.Get(ctx, "general")
	if err != nil {
		t.Fatal(err)
	}
	if view.Version != 1 {
		t.Fatalf("synthesized view version = %d, want 1", view.Version)
	}
	replaced, err := s.Replace(ctx, ReplaceInput{
		Namespace:     "general",
		Settings:      json.RawMessage(`{"default_locale":"en-US"}`),
		ExpectVersion: view.Version,
		ActorID:       "00000000-0000-0000-0000-000000000001",
		ActorUsername: "admin",
	})
	if err != nil {
		t.Fatalf("first write via GET version rejected: %v", err)
	}
	if replaced.Version != 2 {
		t.Fatalf("inserted version = %d, want 2 (must advance past client-held 1)", replaced.Version)
	}
	if replaced.Settings["default_locale"] != "en-US" {
		t.Fatalf("value lost: %v", replaced.Settings)
	}
	// Replay with the same If-Match must fail with a version conflict (first write advanced past the client's held version).
	if _, err := s.Replace(ctx, ReplaceInput{
		Namespace:     "general",
		Settings:      json.RawMessage(`{"default_locale":"en-US"}`),
		ExpectVersion: view.Version,
		ActorID:       "00000000-0000-0000-0000-000000000001",
		ActorUsername: "admin",
	}); err != ErrConflict {
		t.Fatalf("replay with same If-Match must conflict, got %v", err)
	}
}

// TestSEC0010AIBudgetNamespaceRoundTrip SEC-0010 regression: contract namespace ai-budget
// is persisted under storage key ai_budget (migration 000001 CHECK only permits ai_budget); the budget reader
// (review/application reading ai_budget) must observe the written value -- previously writing ai-budget
// violated CHECK and reading ai_budget always returned empty, leaving enforced permanently false.
func TestSEC0010AIBudgetNamespaceRoundTrip(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	adminID := "00000000-0000-0000-0000-000000000001"
	db.Exec(`delete from system_settings where namespace = 'ai_budget'`)
	db.Exec(`delete from audit_events where event_type = 'settings.replaced' and metadata->>'namespace' = 'ai-budget'`)
	proposed := json.RawMessage(`{"enforced":true,"daily_budget_minor":5000,"currency":"CNY"}`)
	assess, err := s.Assess(ctx, "ai-budget", proposed, adminID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Replace(ctx, ReplaceInput{
		Namespace: "ai-budget", Settings: proposed,
		ExpectVersion: assess.CurrentVersion, ActorID: adminID, ActorUsername: "admin",
		ImpactToken: assess.ImpactToken,
	})
	if err != nil {
		t.Fatalf("replace ai-budget: %v", err)
	}
	if view.Namespace != "ai-budget" {
		t.Fatalf("view namespace = %q, want contract name ai-budget", view.Namespace)
	}
	// Row is persisted under storage key ai_budget with enforced=true.
	var enforced bool
	if err := db.Raw(`select (settings->>'enforced')::boolean from system_settings where namespace = 'ai_budget'`).Scan(&enforced).Error; err != nil || !enforced {
		t.Fatalf("row under ai_budget missing or enforced=false: err=%v enforced=%v", err, enforced)
	}
	// API read surface round-trips using the contract namespace name.
	got, err := s.Get(ctx, "ai-budget")
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings["enforced"] != true {
		t.Fatalf("get round-trip lost enforced: %v", got.Settings)
	}
	// Revision history remains queryable under the contract name ai-budget.
	revs, _, err := s.ListRevisions(ctx, "ai-budget", 10, "")
	if err != nil || len(revs) == 0 {
		t.Fatalf("revisions under contract name ai-budget: err=%v count=%d", err, len(revs))
	}
	// Updating only low-impact fields (e.g. daily_budget_minor / alert_threshold_percent) while keeping enforced unchanged
	// must succeed without requiring an impact_token even when the full settings object is submitted.
	lowImpactProposal := json.RawMessage(`{"enforced":true,"daily_budget_minor":8000,"currency":"CNY","alert_threshold_percent":85,"input_price_per_million_minor":100,"output_price_per_million_minor":200}`)
	lowAssess, err := s.Assess(ctx, "ai-budget", lowImpactProposal, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if lowAssess.Impact.Level != "low" {
		t.Fatalf("expected low impact when enforced is unchanged, got %s", lowAssess.Impact.Level)
	}
	view2, err := s.Replace(ctx, ReplaceInput{
		Namespace:     "ai-budget",
		Settings:      lowImpactProposal,
		ExpectVersion: view.Version,
		ActorID:       adminID,
		ActorUsername: "admin",
		ImpactToken:   "",
	})
	if err != nil {
		t.Fatalf("low-impact full-payload replace without token must succeed, got: %v", err)
	}
	if fmt.Sprint(view2.Settings["daily_budget_minor"]) != "8000" {
		t.Fatalf("expected daily_budget_minor=8000, got %v", view2.Settings["daily_budget_minor"])
	}
}
