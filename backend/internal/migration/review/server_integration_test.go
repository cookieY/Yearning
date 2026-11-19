//go:build integration

// server_integration_test.go acceptance tests for migration review server alignment (§17.6-4/8):
// me-view populates required CurrentUser fields while retaining the capabilities detection signal;
// confirm/approve enforce If-Match (quoted positive integer) and advance version;
// error codes strictly follow operation profiles (5002/5003/5005 + 1004, never 1003).
package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func testDSN(t *testing.T) string {
	t.Helper()
	password := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured: set YEARNING_TEST_DATABASE_PASSWORD")
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		envOr("YEARNING_TEST_DATABASE_HOST", "localhost"),
		envOr("YEARNING_TEST_DATABASE_PORT", "54330"),
		envOr("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		envOr("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"),
		envOr("YEARNING_TEST_DATABASE_SSLMODE", "disable"))
}

// acquireLock shares the same flock file as httpapi so cross-package integration tests serialize access to the shared DB.
func acquireLock(t *testing.T) {
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

type reviewHarness struct {
	server *Server
	router *gin.Engine
	db     *gorm.DB
	cookie string
	csrf   string
	userID string
}

func newReviewHarness(t *testing.T) *reviewHarness {
	t.Helper()
	acquireLock(t)
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(postgres.Open(testDSN(t)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })

	// users_single_builtin_admin unique index: temporarily take over the existing builtin admin row
	// and clean up on test completion.
	marker := "migrate-review-" + uuid.NewString()[:8]
	if err := db.Exec(`delete from auth_sessions`).Error; err != nil {
		t.Fatalf("clear sessions: %v", err)
	}
	if err := db.Exec(`delete from users where is_builtin_admin`).Error; err != nil {
		t.Fatalf("clear builtin admin: %v", err)
	}
	userID := uuid.NewString()
	if err := db.Exec(`insert into users (id, username, password_hash, display_name, is_builtin_admin, role, version, created_at, updated_at)
		values (?, 'admin', null, ?, true, 'admin', 3, now() - interval '1 day', now())`, userID, marker).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from audit_events where actor_id = ?`, userID).Error
		_ = db.Exec(`delete from legacy_migration_runs where initiated_by_user_id = ?`, userID).Error
		_ = db.Exec(`delete from auth_sessions`).Error
		_ = db.Exec(`delete from users where id = ?`, userID).Error
	})

	token := uuid.NewString()
	sessionID := uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	csrfSum := sha256.Sum256([]byte("csrf-" + token))
	if err := db.Exec(`insert into auth_sessions (id, user_id, token_hash, csrf_hash, created_at, last_seen_at, expires_at)
		values (?, ?, ?, ?, now(), now(), now() + interval '1 hour')`,
		sessionID, userID, hex.EncodeToString(sum[:]), hex.EncodeToString(csrfSum[:])).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	s := &Server{DB: db, Logger: zap.NewNop()}
	return &reviewHarness{
		server: s, router: NewRouter(s), db: db, userID: userID,
		cookie: sessionCookieName + "=" + sessionID + ":" + token,
		csrf:   "csrf-" + token,
	}
}

func (h *reviewHarness) do(t *testing.T, method, path string, body any, ifMatch string) (int, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", h.cookie)
	// SEC-0043: mutating requests must carry an X-CSRF-Token header matching csrf_hash.
	req.Header.Set("X-CSRF-Token", h.csrf)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func (h *reviewHarness) seedRun(t *testing.T, state, manifestHash, report string, version int64) string {
	t.Helper()
	id := uuid.NewString()
	var mh any
	if manifestHash != "" {
		mh = manifestHash
	}
	// Rows in approved state must populate all approval fields (per migration_run_approval_fields
	// CHECK constraint) with a manifest hash matching the seeded value.
	approvedFields := "null, null, null, null"
	if state == "approved" {
		approvedFields = fmt.Sprintf(`'%s', '11111111-1111-7111-8111-111111111111', 'admin', now()`, goodManifest)
	}
	err := h.db.Exec(`insert into legacy_migration_runs
		(id, source_schema_version, manifest_hash, state, initiated_by_user_id, report, version, started_at, updated_at,
		 approved_manifest_hash, approved_by_user_id_snapshot, approved_by_username_snapshot, approved_at)
		values (?, 'v3-test', ?, ?, ?, ?::jsonb, ?, now(), now(), `+approvedFields+`)`,
		id, mh, state, h.userID, report, version).Error
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return id
}

func runVersion(t *testing.T, h *reviewHarness, runID string) int64 {
	t.Helper()
	var v int64
	if err := h.db.Raw(`select version from legacy_migration_runs where id = ?`, runID).Row().Scan(&v); err != nil {
		t.Fatalf("load version: %v", err)
	}
	return v
}

func errCode(t *testing.T, body map[string]any) float64 {
	t.Helper()
	if body == nil {
		t.Fatalf("no body")
	}
	v, ok := body["err_code"].(float64)
	if !ok {
		t.Fatalf("no err_code: %v", body)
	}
	return v
}

const candidateReport = `{"candidates":[{"candidate_id":"cand-1","kind":"permission_group",
	"target_definition_hash":"sha256:1111111111111111111111111111111111111111111111111111111111111111","confirmed":false}],
	"all_candidates_confirmed":false}`

const confirmedReport = `{"candidates":[{"candidate_id":"cand-1","kind":"permission_group",
	"target_definition_hash":"sha256:1111111111111111111111111111111111111111111111111111111111111111","confirmed":true,"confirmed_by":"admin"}],
	"all_candidates_confirmed":true}`

const goodManifest = "1111111111111111111111111111111111111111111111111111111111111111"

func confirmBody(confirmed bool) map[string]any {
	return map[string]any{
		"confirmed":              confirmed,
		"target_definition_hash": "sha256:" + goodManifest,
	}
}

func TestMeViewCurrentUserFields(t *testing.T) {
	h := newReviewHarness(t)
	code, body := h.do(t, "GET", "/api/v4/users/me", nil, "")
	if code != 200 || errCode(t, body) != 0 {
		t.Fatalf("me: code=%d body=%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("me: no data %v", body)
	}
	// Full set of required CurrentUser fields (§17.6-4).
	for _, f := range []string{"id", "username", "is_builtin_admin", "version",
		"created_at", "updated_at", "can_access_admin"} {
		if _, ok := data[f]; !ok {
			t.Fatalf("me missing %s: %v", f, data)
		}
	}
	if data["version"].(float64) != 3 {
		t.Fatalf("me version: want 3 got %v", data["version"])
	}
	if data["can_access_admin"] != true {
		t.Fatalf("me can_access_admin: %v", data["can_access_admin"])
	}
	// Retain capabilities extension (frontend migration-mode detection signal).
	caps, _ := data["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "migration_review" {
		t.Fatalf("me capabilities: %v", data["capabilities"])
	}
	// Timestamps must be valid RFC3339.
	for _, f := range []string{"created_at", "updated_at"} {
		if _, err := time.Parse(time.RFC3339, data[f].(string)); err != nil {
			t.Fatalf("me %s not RFC3339: %v", f, data[f])
		}
	}
}

func TestConfirmIfMatchAndVersionAdvance(t *testing.T) {
	h := newReviewHarness(t)
	runID := h.seedRun(t, "awaiting_confirmation", goodManifest, candidateReport, 1)

	// Missing or malformed If-Match -> 1001 (OpenAPI requires a quoted positive integer).
	code, body := h.do(t, "PUT", "/api/v4/admin/migrations/"+runID+
		"/candidate-mappings/cand-1/confirmation", confirmBody(true), "")
	if code != 200 || errCode(t, body) != 1001 {
		t.Fatalf("confirm no If-Match: want 1001 got code=%d body=%v", code, body)
	}
	code, body = h.do(t, "PUT", "/api/v4/admin/migrations/"+runID+
		"/candidate-mappings/cand-1/confirmation", confirmBody(true), `1`)
	if code != 200 || errCode(t, body) != 1001 {
		t.Fatalf("confirm unquoted If-Match: want 1001 got code=%d body=%v", code, body)
	}

	// Stale version -> 1004 without advancing version.
	code, body = h.do(t, "PUT", "/api/v4/admin/migrations/"+runID+
		"/candidate-mappings/cand-1/confirmation", confirmBody(true), `"9"`)
	if code != 200 || errCode(t, body) != 1004 {
		t.Fatalf("confirm stale If-Match: want 1004 got code=%d body=%v", code, body)
	}
	if runVersion(t, h, runID) != 1 {
		t.Fatalf("confirm stale: version advanced")
	}

	// Matching version -> succeeds and increments version to version+1.
	code, body = h.do(t, "PUT", "/api/v4/admin/migrations/"+runID+
		"/candidate-mappings/cand-1/confirmation", confirmBody(true), `"1"`)
	if code != 200 || errCode(t, body) != 0 {
		t.Fatalf("confirm: code=%d body=%v", code, body)
	}
	if runVersion(t, h, runID) != 2 {
		t.Fatalf("confirm: want version 2 got %d", runVersion(t, h, runID))
	}

	// After version advances, the previous version becomes stale -> 1004 (optimistic concurrency).
	code, body = h.do(t, "PUT", "/api/v4/admin/migrations/"+runID+
		"/candidate-mappings/cand-1/confirmation", confirmBody(false), `"1"`)
	if code != 200 || errCode(t, body) != 1004 {
		t.Fatalf("confirm after advance: want 1004 got code=%d body=%v", code, body)
	}
}

func TestConfirmStateGuardProfileCode(t *testing.T) {
	h := newReviewHarness(t)
	// Already approved run: state check failure returns 5005 (confirm profile does not declare 5002/1010).
	runID := h.seedRun(t, "approved", goodManifest, confirmedReport, 4)
	code, body := h.do(t, "PUT", "/api/v4/admin/migrations/"+runID+
		"/candidate-mappings/cand-1/confirmation", confirmBody(true), `"4"`)
	if code != 200 || errCode(t, body) != 5005 {
		t.Fatalf("confirm on approved run: want 5005 got code=%d body=%v", code, body)
	}
}

func TestApproveProfileCodesAndVersion(t *testing.T) {
	h := newReviewHarness(t)

	// Dry Run not yet completed -> 5002.
	dry := h.seedRun(t, "dry_run_running", "", `{}`, 1)
	code, body := h.do(t, "POST", "/api/v4/admin/migrations/"+dry+"/approval",
		map[string]any{"manifest_hash": "sha256:" + goodManifest, "confirmation_phrase": "APPROVE " + dry}, `"1"`)
	if code != 200 || errCode(t, body) != 5002 {
		t.Fatalf("approve dry-run: want 5002 got code=%d body=%v", code, body)
	}

	// Unconfirmed candidate remains -> 5003.
	unconf := h.seedRun(t, "awaiting_confirmation", goodManifest, candidateReport, 1)
	code, body = h.do(t, "POST", "/api/v4/admin/migrations/"+unconf+"/approval",
		map[string]any{"manifest_hash": "sha256:" + goodManifest, "confirmation_phrase": "APPROVE " + unconf}, `"1"`)
	if code != 200 || errCode(t, body) != 5003 {
		t.Fatalf("approve unconfirmed: want 5003 got code=%d body=%v", code, body)
	}

	// Manifest hash mismatch -> 5005.
	conf := h.seedRun(t, "awaiting_confirmation", goodManifest, confirmedReport, 1)
	code, body = h.do(t, "POST", "/api/v4/admin/migrations/"+conf+"/approval",
		map[string]any{"manifest_hash": "sha256:" + strings.Repeat("2", 64), "confirmation_phrase": "APPROVE " + conf}, `"1"`)
	if code != 200 || errCode(t, body) != 5005 {
		t.Fatalf("approve manifest mismatch: want 5005 got code=%d body=%v", code, body)
	}

	// Confirmation phrase mismatch -> 1001 VALIDATION_FAILED (as declared in profile).
	code, body = h.do(t, "POST", "/api/v4/admin/migrations/"+conf+"/approval",
		map[string]any{"manifest_hash": "sha256:" + goodManifest, "confirmation_phrase": "APPROVE nope"}, `"1"`)
	if code != 200 || errCode(t, body) != 1001 {
		t.Fatalf("approve phrase mismatch: want 1001 got code=%d body=%v", code, body)
	}

	// Stale version -> 1004.
	code, body = h.do(t, "POST", "/api/v4/admin/migrations/"+conf+"/approval",
		map[string]any{"manifest_hash": "sha256:" + goodManifest, "confirmation_phrase": "APPROVE " + conf}, `"7"`)
	if code != 200 || errCode(t, body) != 1004 {
		t.Fatalf("approve stale If-Match: want 1004 got code=%d body=%v", code, body)
	}
	if runVersion(t, h, conf) != 1 {
		t.Fatalf("approve stale: version advanced")
	}

	// Happy path: matching phrase + hash + version -> transitions to approved and increments version.
	code, body = h.do(t, "POST", "/api/v4/admin/migrations/"+conf+"/approval",
		map[string]any{"manifest_hash": "sha256:" + goodManifest, "confirmation_phrase": "APPROVE " + conf}, `"1"`)
	if code != 200 || errCode(t, body) != 0 {
		t.Fatalf("approve: code=%d body=%v", code, body)
	}
	if v := runVersion(t, h, conf); v != 2 {
		t.Fatalf("approve: want version 2 got %d", v)
	}
	var state string
	if err := h.db.Raw(`select state from legacy_migration_runs where id = ?`, conf).Row().Scan(&state); err != nil || state != "approved" {
		t.Fatalf("approve: state=%s err=%v", state, err)
	}

	// Approving an already approved run (even with current version) -> 5005 state check failure.
	code, body = h.do(t, "POST", "/api/v4/admin/migrations/"+conf+"/approval",
		map[string]any{"manifest_hash": "sha256:" + goodManifest, "confirmation_phrase": "APPROVE " + conf}, `"2"`)
	if code != 200 || errCode(t, body) != 5005 {
		t.Fatalf("re-approve: want 5005 got code=%d body=%v", code, body)
	}
}

func TestRunsExposeVersion(t *testing.T) {
	h := newReviewHarness(t)
	runID := h.seedRun(t, "awaiting_confirmation", goodManifest, candidateReport, 6)

	code, body := h.do(t, "GET", "/api/v4/admin/migrations", nil, "")
	if code != 200 || errCode(t, body) != 0 {
		t.Fatalf("list runs: code=%d body=%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	items, _ := data["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("list runs: no items %v", data)
	}
	first, _ := items[0].(map[string]any)
	if _, ok := first["version"]; !ok {
		t.Fatalf("list runs: version missing %v", first)
	}

	code, body = h.do(t, "GET", "/api/v4/admin/migrations/"+runID, nil, "")
	if code != 200 || errCode(t, body) != 0 {
		t.Fatalf("get run: code=%d body=%v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	if data["version"].(float64) != 6 {
		t.Fatalf("get run: want version 6 got %v", data["version"])
	}
}
