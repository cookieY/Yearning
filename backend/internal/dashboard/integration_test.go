//go:build integration

// integration_test.go B12 dashboard domain acceptance (S005/S004): workbench counters,
// admin Dashboard bounded aggregations, announcement sanitization and singleton publication, and audit read clipping.
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/gin-gonic/gin"
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
	acquireSharedITestLock(t)
	db, err := gorm.Open(postgres.Open(dsnOf(password)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	return db
}

// acquireSharedITestLock shares the same flock file with httpapi/review packages: once all four domains
// are mounted, httpapi truncation includes announcement/notification/audit tables, requiring serialized access to the shared itest DB.
func acquireSharedITestLock(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(os.TempDir()+"/yearningx-itest-shared.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

func newTestService(t *testing.T) (*gorm.DB, *Service) {
	t.Helper()
	db := itestDB(t)
	db.Exec(`delete from announcement_publication`)
	db.Exec(`delete from announcement_revisions`)
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

const testAdminID = "00000000-0000-0000-0000-000000000001"

func TestMyDashboardAndAdminViews(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	// Personal workbench (empty state = zero counts; bounded queries never time out).
	view, err := s.MyDashboard(ctx, testAdminID)
	if err != nil {
		t.Fatal(err)
	}
	if view.RefreshedAt == "" {
		t.Fatal("refreshed_at missing")
	}
	// Admin Dashboards.
	ops, err := s.OperationsDashboard(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if ops.Meta.Completeness != "complete" || ops.Meta.SystemTimezone == "" || len(ops.Meta.WindowEnd) != 10 {
		t.Fatalf("operations meta: %+v", ops.Meta)
	}
	// State distribution (RCP-20261023-HOME-METRICS-ALIGNMENT): key set matches the
	// ChangeOrderState enum verbatim, and the sum of all keys equals change_order_total
	// in the same window — an executable assertion that distribution and success rate share the same denominator.
	// OperationsDashboard runs all window queries inside a single REPEATABLE READ read-only transaction,
	// so this invariant holds even under concurrent submissions rather than by single-threaded coincidence.
	if len(ops.StateDistribution) != len(ChangeOrderStates) {
		t.Fatalf("state_distribution keys: got %d want %d", len(ops.StateDistribution), len(ChangeOrderStates))
	}
	var distributed int64
	for _, state := range ChangeOrderStates {
		count, ok := ops.StateDistribution[state]
		if !ok {
			t.Fatalf("state_distribution missing key %q", state)
		}
		if count < 0 {
			t.Fatalf("state_distribution[%q] negative: %d", state, count)
		}
		distributed += count
	}
	if distributed != ops.ChangeOrderTotal {
		t.Fatalf("state_distribution sum %d != change_order_total %d", distributed, ops.ChangeOrderTotal)
	}
	sh, err := s.SystemHealth(ctx)
	if err != nil || len(sh.Components) == 0 {
		t.Fatalf("system health: %v", err)
	}
	// Health components must not leak dependency identities (no host/dsn patterns),
	// and an empty backlog/queue must report healthy (regression guard for NULL min(occurred_at) scan).
	for _, c := range sh.Components {
		if strings.Contains(strings.ToLower(c.Component), "host") {
			t.Fatalf("component identity leak: %s", c.Component)
		}
		if c.FailureName != nil && (strings.Contains(*c.FailureName, "@") || strings.Contains(*c.FailureName, "://")) {
			t.Fatalf("failure name leak: %s", *c.FailureName)
		}
		if c.Status != "healthy" || c.FailureName != nil {
			t.Fatalf("expected healthy component %s on clean state, got status=%s failure=%v", c.Component, c.Status, c.FailureName)
		}
	}
	// Out-of-bounds window clamping.
	if _, err := s.OperationsDashboard(ctx, 999); err != nil {
		t.Fatal(err)
	}
	_ = db
}

func TestAnnouncementSanitizeAndPublish(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	// Sanitize: strip <script>/<iframe>/on* attributes while preserving markdown structure.
	rev, err := s.CreateRevision(ctx, CreateRevisionInput{
		Title: "b12 announcement", Markdown: "# Heading\n<script>alert(1)</script>\n- Point **one**\n> Quote",
		ActorID: testAdminID, ActorUsername: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(rev.SanitizedHTML)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "onerror") {
		t.Fatalf("script survived sanitizer: %s", rev.SanitizedHTML)
	}
	if !strings.Contains(rev.SanitizedHTML, "<h1>") || !strings.Contains(rev.SanitizedHTML, "<strong>") {
		t.Fatalf("markdown structure lost: %s", rev.SanitizedHTML)
	}
	if rev.ContentSHA256 == "" || rev.PolicyVersion != SanitizerPolicyVersion {
		t.Fatal("hash/policy missing")
	}
	// Publish (singleton optimistic lock); returned version matches persisted DB truth (insert branch = 1).
	pub, pubVersion, err := s.Publish(ctx, rev.ID, testAdminID, "admin", 1)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.ID != rev.ID {
		t.Fatal("published wrong revision")
	}
	// Second publish with stale expectation = conflict (pointer is already at version = 1).
	if _, _, err := s.Publish(ctx, rev.ID, testAdminID, "admin", 0); err != ErrConflict {
		t.Fatalf("stale publish: %v", err)
	}
	// Current announcement (anonymously readable); version matches the value returned by Publish (R2-F12).
	cur, err := s.Current(ctx)
	if err != nil || cur == nil || cur.Revision.ID != rev.ID {
		t.Fatalf("current: %v", err)
	}
	if cur.Version != pubVersion {
		t.Fatalf("publication version drift: publish=%d current=%d", pubVersion, cur.Version)
	}
	// Revision history in descending order.
	items, err := s.ListRevisions(ctx, 10)
	if err != nil || len(items) == 0 {
		t.Fatalf("revisions: %v", err)
	}
	// Reject oversized content.
	if _, err := s.CreateRevision(ctx, CreateRevisionInput{
		Title: "x", Markdown: strings.Repeat("a", 20001),
		ActorID: testAdminID, ActorUsername: "admin",
	}); err != ErrAnnouncementValidation {
		t.Fatalf("oversized accepted: %v", err)
	}
	_ = db
}

// fakeSessionStore is an in-memory Session store for handler-level tests.
type fakeSessionStore map[string]*session.Session

func (m fakeSessionStore) Get(_ context.Context, id string) (*session.Session, error) {
	if s, ok := m[id]; ok {
		return s, nil
	}
	return nil, nil
}

// TestAuditEventsEndpoint review L4 supplement: handler-level regression for audit read projection —
// pagination cursor, SQLi defense (injected after cursor returns 1001 with no side effects), and metadata
// allowlist clipping.
func TestAuditEventsEndpoint(t *testing.T) {
	db, s := newTestService(t)
	ctx := context.Background()
	db.Exec(`delete from audit_events where event_type = 'itest.probe'`)
	// Seed: 3 chronological rows + 1 metadata row carrying projected foreign keys and nested structures.
	seed := []struct {
		id     string
		action string
		minAgo int
		meta   string
	}{
		{"11111111-1111-4111-8111-111111111111", "seed_a", 30, `{"namespace":"query","version":2}`},
		{"22222222-2222-4222-8222-222222222222", "seed_b", 20, `{}`},
		{"33333333-3333-4333-8333-333333333333", "seed_c", 10, `{}`},
		{"44444444-4444-4444-8444-444444444444", "seed_d", 5,
			`{"namespace":"general","internal_trace":"drop-me","nested":{"secret":"x"},"reason_code":"R1"}`},
	}
	for _, r := range seed {
		if err := db.Exec(`insert into audit_events (id, event_type, actor_kind, actor_id,
			actor_username_snapshot, resource_type, resource_id, action, outcome, metadata,
			occurred_at, expires_at)
			values (?, 'itest.probe', 'user', ?, 'admin', 'system_settings', NULL, ?,
			'succeeded', ?::jsonb, now() - (?::int * interval '1 minute'),
			now() - (?::int * interval '1 minute') + interval '90 days')`,
			r.id, testAdminID, r.action, r.meta, r.minAgo, r.minAgo).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	store := fakeSessionStore{"itest-sid": {ID: "itest-sid", Identity: &session.Identity{
		UserID: testAdminID, Username: "admin", IsBuiltinAdmin: true}}}
	router.Use(session.Middleware(store, session.Options{CookieName: "itest-sid"}))
	RegisterRoutes(router, &Deps{Service: s})
	client := func(url string) (int, gin.H) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.AddCookie(&http.Cookie{Name: "itest-sid", Value: "itest-sid"})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var body gin.H
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	// 1) Pagination: limit=2 -> 2 items + has_more.
	code, body := client("/admin/audit-events?limit=2")
	if code != http.StatusOK || body["err_code"] != float64(0) {
		t.Fatalf("list: code=%d body=%v", code, body)
	}
	data := body["data"].(map[string]any)
	items := data["items"].([]any)
	page := data["page"].(map[string]any)
	if len(items) != 2 || page["has_more"] != true {
		t.Fatalf("page1: %d items page=%v", len(items), page)
	}
	// 2) Cursor: after = first row -> first row is excluded.
	first := items[0].(map[string]any)["id"].(string)
	code, body = client("/admin/audit-events?limit=50&after=" + first)
	if code != http.StatusOK || body["err_code"] != float64(0) {
		t.Fatalf("cursor: code=%d body=%v", code, body)
	}
	items = body["data"].(map[string]any)["items"].([]any)
	// Shared DB may contain other event types: assert only cursor exclusivity and presence of seeded rows.
	for _, it := range items {
		if it.(map[string]any)["id"].(string) == first {
			t.Fatal("cursor did not exclude after-id")
		}
	}
	if len(items) < 3 {
		t.Fatalf("cursor page: %d items, want >= 3", len(items))
	}
	// 3) SQLi defense: injected after-cursor payload returns 1001 with zero side effects on audit_events.
	before := int64(0)
	db.Raw(`select count(*) from audit_events where event_type = 'itest.probe'`).Scan(&before)
	sqli := `x'::uuid), ('1970-01-01'::uuid); delete from audit_events; --`
	req := httptest.NewRequest(http.MethodGet,
		"/admin/audit-events?after="+url.QueryEscape(sqli), nil)
	req.AddCookie(&http.Cookie{Name: "itest-sid", Value: "itest-sid"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var errBody gin.H
	_ = json.Unmarshal(w.Body.Bytes(), &errBody)
	if errBody["err_code"] != float64(1001) {
		t.Fatalf("sqli not rejected as 1001: %v", errBody)
	}
	after := int64(0)
	db.Raw(`select count(*) from audit_events where event_type = 'itest.probe'`).Scan(&after)
	if before != after || after != 4 {
		t.Fatalf("sqli side effect: before=%d after=%d", before, after)
	}
	// 4) Metadata clipping: allowlisted keys are retained while unknown keys and nested structures are stripped.
	// (Fetch the full page directly and locate the rich-metadata seed row 4444 in items.)
	req = httptest.NewRequest(http.MethodGet, "/admin/audit-events?limit=200", nil)
	req.AddCookie(&http.Cookie{Name: "itest-sid", Value: "itest-sid"})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var curBody gin.H
	_ = json.Unmarshal(w.Body.Bytes(), &curBody)
	_ = ctx
	items = curBody["data"].(map[string]any)["items"].([]any)
	foundRich := false
	for _, it := range items {
		m := it.(map[string]any)
		if m["id"] != "44444444-4444-4444-8444-444444444444" {
			continue
		}
		foundRich = true
		meta := m["metadata"].(map[string]any)
		if meta["namespace"] != "general" || meta["reason_code"] != "R1" {
			t.Fatalf("allowlist keys missing: %v", meta)
		}
		if _, ok := meta["internal_trace"]; ok {
			t.Fatalf("unknown key leaked: %v", meta)
		}
		if _, ok := meta["nested"]; ok {
			t.Fatalf("nested struct leaked: %v", meta)
		}
	}
	if !foundRich {
		t.Fatal("rich metadata seed row missing from page")
	}
	// 5) Anonymous requests rejected (OpenAPI global sessionCookie).
	req = httptest.NewRequest(http.MethodGet, "/admin/audit-events", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous should be 401, got %d", w.Code)
	}
	db.Exec(`delete from audit_events where event_type = 'itest.probe'`)
}
