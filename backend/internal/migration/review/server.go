// Package review implements `serve --migration-mode`: exposes only login/logout/current-user and
// /api/v4/admin/migrations* while returning 503 for all normal business routes. Runs under the
// yearning_migration_workbench database role.
//
// Surface hardening (SEC-0043): login failure throttling + account lockout, double-submit CSRF protection
// on mutating methods (Cookie + X-CSRF-Token header, matching the main server's yearning_csrf posture),
// login failure audit logging, and /api/v4/health/live|ready probe endpoints (matching normal serve probe semantics).
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/httpapi"
	"github.com/cookieY/YearningX/backend/internal/migration/legacy"
)

// Server is the HTTP server for migration review mode.
type Server struct {
	Pool   *pgxpool.Pool
	DB     *gorm.DB
	Logger *zap.Logger

	// loginGuard implements in-memory login throttling and lockout (single-replica workload; SEC-0043).
	loginGuard *loginGuard
}

// Login throttling parameters: lock the account for loginLockWindow after loginFailureThreshold consecutive failures.
const (
	loginFailureThreshold = 5
	loginLockWindow       = 15 * time.Minute
)

// maxLoginGuardKeys bounds the number of keys in the failure table (NEW-AUTH-02) so random username spraying
// cannot grow memory without bound. When full, expired keys are swept first, then the entry with the oldest failure timestamp is evicted.
const (
	maxLoginGuardKeys = 1000
	maxLoginGuardIPs  = 4096
	maxFailuresPerIP  = 20
)

type loginFailState struct {
	count       int
	lockedUntil time.Time
	// lastFailure records the most recent failure timestamp, used for expiration sweeps and in-window counter resets
	// (NEW-AUTH-02: previously partial counts of 1..4 failures never expired, staying in memory and accumulating across windows).
	lastFailure time.Time
}

type loginGuard struct {
	mu       sync.Mutex
	failed   map[string]loginFailState
	ipFailed map[string][]time.Time
	// saturated is set when the table is full and cannot safely evict any entry because active locks exist.
	// While saturated, all new login attempts fail closed until at least one lock expires and is swept;
	// otherwise evicting locked entries would let an attacker spray 1000 usernames to evict a target's active lock (NEW-AUTH-07).
	saturated bool
}

func newLoginGuard() *loginGuard {
	return &loginGuard{
		failed:   map[string]loginFailState{},
		ipFailed: map[string][]time.Time{},
	}
}

// normalizeLoginGuardKey normalizes a username into a canonical lockout key (NEW-AUTH-02): lowercase + trimmed
// whitespace. Because the database query matches on lower(username)=lower($1), unnormalized keys would allow
// case/whitespace variants (Admin/ADMIN/aDmIn) to each get an independent 5-attempt quota against the same account;
// normalization ensures all variants share a single bucket.
func normalizeLoginGuardKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// locked reports whether username is currently within an active lockout window.
func (g *loginGuard) locked(username string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.saturated {
		g.sweepExpiredLocked(now)
		if len(g.failed) < maxLoginGuardKeys {
			g.saturated = false
		} else {
			// Table remains full of active locks: fail closed globally so failure counts for new usernames
			// cannot bypass lockout by going unrecorded.
			return true
		}
	}
	st, ok := g.failed[normalizeLoginGuardKey(username)]
	return ok && now.Before(st.lockedUntil)
}

// sweepExpiredLocked removes entries whose lock has expired and whose last failure is outside the observation window
// (expiring stale partial counts alongside the window). Caller must hold mu.
func (g *loginGuard) sweepExpiredLocked(now time.Time) {
	for k, st := range g.failed {
		if now.After(st.lockedUntil) && now.Sub(st.lastFailure) >= loginLockWindow {
			delete(g.failed, k)
		}
	}
}

// evictOldestLocked evicts the unlocked entry with the oldest lastFailure timestamp (approximate LRU).
// Caller must hold mu; returns false when active locks prevent safe eviction so active locks are never
// dropped to make room for new entries (NEW-AUTH-07).
func (g *loginGuard) evictOldestLocked(now time.Time) bool {
	// NEW-AUTH-11: as long as any active lock exists in the table, do not evict any entry --
	// including partial counts with 1..4 failures. Otherwise an attacker could fill 999 slots with active locks
	// plus 1 target partial count, then spray a new username to evict the target's partial count and reset its
	// 5th-attempt counter back to 1. Returning false when any active lock exists enters saturation (fail closed
	// for new logins); when no active locks exist, evicting the oldest partial count keeps the table bounded (NEW-AUTH-02).
	for _, st := range g.failed {
		if now.Before(st.lockedUntil) {
			return false
		}
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, st := range g.failed {
		if first || st.lastFailure.Before(oldestAt) {
			oldestKey, oldestAt, first = k, st.lastFailure, false
		}
	}
	if !first {
		delete(g.failed, oldestKey)
		return true
	}
	return false
}

// recordFailure records a failed login attempt; enters lockout and returns the lockout deadline once the threshold is reached.
func (g *loginGuard) recordFailure(username string, now time.Time) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := normalizeLoginGuardKey(username)
	st, ok := g.failed[key]
	if ok && now.Sub(st.lastFailure) >= loginLockWindow && now.After(st.lockedUntil) {
		// Discard stale partial counts from outside the observation window (reusing the 15-minute window duration) and restart counting.
		st = loginFailState{}
	}
	if !ok && len(g.failed) >= maxLoginGuardKeys {
		g.sweepExpiredLocked(now)
		if len(g.failed) >= maxLoginGuardKeys && !g.evictOldestLocked(now) {
			// Active locks prevent eviction: refuse to insert the new key and mark the guard saturated
			// so locked() fails closed on new logins; automatically recovers once existing locks expire.
			g.saturated = true
			return time.Time{}
		}
	}
	st.count++
	st.lastFailure = now
	if st.count >= loginFailureThreshold {
		st.lockedUntil = now.Add(loginLockWindow)
		st.count = 0
	}
	g.failed[key] = st
	return st.lockedUntil
}

// recordSuccess clears failure state for username after a successful login.
func (g *loginGuard) recordSuccess(username string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failed, normalizeLoginGuardKey(username))
}

func (g *loginGuard) pruneIPLocked(ip string, now time.Time) []time.Time {
	if g.ipFailed == nil {
		return nil
	}
	kept := g.ipFailed[ip][:0]
	for _, t := range g.ipFailed[ip] {
		if now.Sub(t) < loginLockWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(g.ipFailed, ip)
		return nil
	}
	g.ipFailed[ip] = kept
	return kept
}

// allowIP reports whether clientIP is below the per-IP failure threshold within loginLockWindow.
func (g *loginGuard) allowIP(ip string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	ip = strings.TrimSpace(ip)
	kept := g.pruneIPLocked(ip, now)
	return len(kept) < maxFailuresPerIP
}

// recordIPFailure records a failed authentication attempt against clientIP, bounding ipFailed capacity.
func (g *loginGuard) recordIPFailure(ip string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ipFailed == nil {
		g.ipFailed = map[string][]time.Time{}
	}
	ip = strings.TrimSpace(ip)
	kept := g.pruneIPLocked(ip, now)
	if len(kept) == 0 && len(g.ipFailed) >= maxLoginGuardIPs {
		for k := range g.ipFailed {
			g.pruneIPLocked(k, now)
		}
		if len(g.ipFailed) >= maxLoginGuardIPs {
			var oldestIP string
			var oldestAt time.Time
			first := true
			for k, ts := range g.ipFailed {
				last := ts[len(ts)-1]
				if first || last.Before(oldestAt) {
					oldestIP, oldestAt, first = k, last, false
				}
			}
			if !first {
				delete(g.ipFailed, oldestIP)
			}
		}
	}
	g.ipFailed[ip] = append(g.ipFailed[ip], now)
}

// NewRouter constructs the Gin router for migration review mode.
func NewRouter(s *Server) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	if s.loginGuard == nil {
		s.loginGuard = newLoginGuard()
	}
	r := gin.New()
	r.Use(httpapi.RequestIDMiddleware(), httpapi.RecoveryMiddleware(s.Logger), httpapi.AccessLogMiddleware(s.Logger))
	api := r.Group("/api/v4")
	// Probe endpoints (SEC-0019/0043): same paths as normal serve mode; live touches no dependencies,
	// while ready verifies metadata PostgreSQL reachability. Business route 503 semantics remain unchanged.
	readiness := httpapi.NewReadiness(s.Logger,
		httpapi.ReadinessCheck{Name: "metadata_postgresql", Check: s.Pool.Ping})
	api.GET("/health/live", readiness.HandleLive)
	api.GET("/health/ready", readiness.HandleReady)
	api.POST("/auth/login", s.handleLogin)
	api.POST("/auth/logout", s.auth(s.handleLogout))
	api.GET("/users/me", s.auth(s.handleMe))
	admin := api.Group("/admin/migrations", s.auth(nil))
	admin.GET("", s.handleListRuns)
	admin.GET("/:migration_run_id", s.handleGetRun)
	admin.PUT("/:migration_run_id/candidate-mappings/:candidate_id/confirmation", s.handleConfirmCandidate)
	admin.POST("/:migration_run_id/approval", s.handleApprove)
	r.NoRoute(func(c *gin.Context) {
		// Contract invariant: in migration review mode, all normal business routes return 503 before reaching domain handlers.
		if strings.HasPrefix(c.Request.URL.Path, "/api/v4/") {
			httpapi.WriteProblem(c, http.StatusServiceUnavailable, "business routes unavailable during migration review")
			return
		}
		httpapi.WriteProblem(c, http.StatusNotFound, "not found")
	})
	return r
}

// sessionCookieName is the dedicated session cookie name for migration-mode.
const sessionCookieName = "yearning_migration_session"

// csrfCookieName is the readable double-submit CSRF cookie (non-HttpOnly, matching yearning_csrf
// on the main server while the session cookie itself remains HttpOnly).
const csrfCookieName = "yearning_migration_csrf"

// HeaderCSRFToken is the CSRF header required on mutating requests (matching X-CSRF-Token on the main server).
const HeaderCSRFToken = "X-CSRF-Token"

func (s *Server) handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "username and password required"})
		return
	}
	now := time.Now()
	clientIP := strings.TrimSpace(c.ClientIP())
	// SEC-0043 / POSTFIX-05: reject immediately on per-IP failure threshold or active account lockout
	// before querying DB, verifying password, or mutating username lockout state.
	if !s.loginGuard.allowIP(clientIP, now) || s.loginGuard.locked(req.Username, now) {
		s.auditLoginOutcome(req.Username, c, "locked")
		c.Header("Retry-After", "60")
		httpapi.WriteProblem(c, http.StatusTooManyRequests, "login locked; retry later")
		return
	}
	var id, hash string
	var isAdmin bool
	err := s.DB.Raw(`select id::text, coalesce(password_hash,''), is_builtin_admin from users where lower(username)=lower($1) and is_builtin_admin`, req.Username).
		Row().Scan(&id, &hash, &isAdmin)
	if err != nil || !legacy.VerifyDjangoHash(req.Password, hash) {
		failNow := time.Now()
		s.loginGuard.recordFailure(req.Username, failNow)
		s.loginGuard.recordIPFailure(clientIP, failNow)
		s.auditLoginOutcome(req.Username, c, "failed")
		httpapi.WriteProblem(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token := uuid.NewString()
	sessionID := uuid.NewString()
	// Double-submit CSRF token: send raw value in the client cookie and store only its SHA-256 digest in the database.
	csrfToken := uuid.NewString()
	if err := s.DB.Exec(`insert into auth_sessions (id, user_id, token_hash, csrf_hash, created_at, last_seen_at, expires_at)
		values ($1, $2, $3, $4, now(), now(), now() + interval '8 hours')`,
		sessionID, id, tokenHash(token), tokenHash(csrfToken)).Error; err != nil {
		httpapi.WriteProblem(c, http.StatusInternalServerError, "session create failed")
		return
	}
	s.loginGuard.recordSuccess(req.Username)
	s.auditLoginOutcome(req.Username, c, "succeeded")
	// Migration approval is a high-sensitivity surface: cookies must set Secure + SameSite=Strict + HttpOnly.
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookieName, Value: sessionID + ":" + token,
		Path: "/", MaxAge: 8 * 3600, HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode,
	})
	// CSRF cookie is non-HttpOnly (double-submit pattern: readable by same-origin client scripts, unsendable cross-site).
	http.SetCookie(c.Writer, &http.Cookie{
		Name: csrfCookieName, Value: csrfToken,
		Path: "/", MaxAge: 8 * 3600, HttpOnly: false, Secure: true,
		SameSite: http.SameSiteStrictMode,
	})
	httpapi.WriteSuccess(c, nil)
}

// auditLoginOutcome records an audit event for login outcomes (SEC-0043): login successes, failures, and lockouts
// during the migration window must be traceable, logging only username and source metadata without any password material.
func (s *Server) auditLoginOutcome(username string, c *gin.Context, outcome string) {
	if s.Logger == nil {
		return
	}
	s.Logger.Warn("migration review login attempt",
		zap.String("username", username),
		zap.String("outcome", outcome),
		zap.String("source_ip", c.ClientIP()),
		zap.String("user_agent", c.Request.UserAgent()),
		zap.String("request_id", httpapi.RequestID(c)),
	)
}

func (s *Server) auth(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(sessionCookieName)
		if err != nil || !strings.Contains(raw, ":") {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		parts := strings.SplitN(raw, ":", 2)
		var userID string
		var tokenHashValue, csrfHashValue string
		var expiresAt time.Time
		var revokedAt *time.Time
		err = s.DB.Raw(`select u.id::text, a.token_hash, a.csrf_hash, a.expires_at, a.revoked_at
			from auth_sessions a join users u on u.id = a.user_id
			where a.id = $1 and u.is_builtin_admin`, parts[0]).Row().Scan(&userID, &tokenHashValue, &csrfHashValue, &expiresAt, &revokedAt)
		if err != nil || tokenHashValue != tokenHash(parts[1]) || time.Now().After(expiresAt) || revokedAt != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		// Double-submit CSRF check (SEC-0043): mutating methods must present an X-CSRF-Token header matching
		// the session's csrf_hash in the database; GET requests do not mutate state and are exempt.
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			if got := c.GetHeader(HeaderCSRFToken); got == "" || tokenHash(got) != csrfHashValue {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		}
		c.Set("admin_user_id", userID)
		if handler != nil {
			handler(c)
			return
		}
		c.Next()
	}
}

func (s *Server) handleLogout(c *gin.Context) {
	raw, _ := c.Cookie(sessionCookieName)
	if parts := strings.SplitN(raw, ":", 2); len(parts) == 2 {
		_ = s.DB.Exec(`update auth_sessions set revoked_at = now(), revoke_reason='logout' where id = $1`, parts[0]).Error
	}
	// Clearing cookies must reuse the exact attributes used when issuing them (Secure/SameSite) so browsers honor deletion.
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name: csrfCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: false, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	httpapi.WriteSuccess(c, nil)
}

func (s *Server) handleMe(c *gin.Context) {
	// §17.6-4: align the me view with required CurrentUser fields (version/created_at/
	// updated_at/can_access_admin) while retaining the capabilities extension as the frontend's
	// migration-mode detection signal.
	userID := c.GetString("admin_user_id")
	var username, displayName string
	var email *string
	var version int64
	var createdAt, updatedAt time.Time
	err := s.DB.Raw(`select username, coalesce(display_name, ''), email, version, created_at, updated_at
		from users where id = $1`, userID).Row().Scan(&username, &displayName, &email, &version, &createdAt, &updatedAt)
	if err != nil {
		httpapi.WriteProblem(c, http.StatusInternalServerError, "user load failed")
		return
	}
	httpapi.WriteSuccess(c, gin.H{
		"id": userID, "username": username, "display_name": displayName, "email": email,
		"is_builtin_admin": true,
		"version":          version,
		"created_at":       createdAt.UTC().Format(time.RFC3339Nano),
		"updated_at":       updatedAt.UTC().Format(time.RFC3339Nano),
		"can_access_admin": true,
		"capabilities":     []string{"migration_review"},
	})
}

func (s *Server) handleListRuns(c *gin.Context) {
	// §18.82: limit and after are declared in OpenAPI (ListLegacyMigrationRunsParams) --
	// previously hardcoded to limit 100 on the server; implements keyset pagination ordered by (started_at, id) DESC
	// using the standard "RFC3339Nano|uuid" cursor format with a default page size of 20.
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= 200 {
			limit = parsed
		}
	}
	type runRow struct {
		ID           string    `json:"id"`
		State        string    `json:"state"`
		ManifestHash *string   `json:"manifest_hash"`
		Version      int64     `json:"version"`
		StartedAt    time.Time `json:"started_at"`
		UpdatedAt    time.Time `json:"updated_at"`
	}
	const selectRuns = `select id::text, state, manifest_hash, version, started_at, updated_at
		from legacy_migration_runs`
	query := s.DB.Raw(selectRuns)
	if after := c.Query("after"); after != "" {
		sep := strings.LastIndex(after, "|")
		if sep <= 0 {
			httpapi.WriteProblem(c, http.StatusBadRequest, "invalid after cursor")
			return
		}
		cursorTime, cursorID := after[:sep], after[sep+1:]
		if _, err := time.Parse(time.RFC3339Nano, cursorTime); err != nil {
			httpapi.WriteProblem(c, http.StatusBadRequest, "invalid after cursor")
			return
		}
		if _, err := uuid.Parse(cursorID); err != nil {
			httpapi.WriteProblem(c, http.StatusBadRequest, "invalid after cursor")
			return
		}
		query = s.DB.Raw(selectRuns+` where (started_at, id) < (?, ?)`, cursorTime, cursorID)
	}
	var rows []runRow
	if err := query.Order("started_at desc").Order("id desc").Limit(limit + 1).
		Scan(&rows).Error; err != nil {
		httpapi.WriteProblem(c, http.StatusInternalServerError, "query failed")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []runRow{}
	}
	var nextCursor string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = last.StartedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
	}
	httpapi.WriteSuccess(c, gin.H{"items": rows, "page": gin.H{
		"limit": limit, "has_more": hasMore, "next_cursor": nextCursor,
	}})
}

func (s *Server) handleGetRun(c *gin.Context) {
	runID := c.Param("migration_run_id")
	if _, err := uuid.Parse(runID); err != nil {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "invalid run id"})
		return
	}
	var state string
	var report string
	var manifestHash *string
	var version int64
	err := s.DB.Raw(`select state, report::text, manifest_hash, version from legacy_migration_runs where id = $1`, runID).Row().Scan(&state, &report, &manifestHash, &version)
	if err != nil {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1002, Name: "RESOURCE_NOT_FOUND", Message: "migration run not found"})
		return
	}
	// Expose version so the client can supply If-Match on confirm/approve requests (optimistic concurrency).
	httpapi.WriteSuccess(c, gin.H{"id": runID, "state": state, "manifest_hash": manifestHash, "version": version, "report": jsonRaw(report)})
}

func (s *Server) handleConfirmCandidate(c *gin.Context) {
	runID := c.Param("migration_run_id")
	candidateID := c.Param("candidate_id")
	var req struct {
		Confirmed            bool   `json:"confirmed"`
		TargetDefinitionHash string `json:"target_definition_hash"`
		Comment              string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !strings.HasPrefix(req.TargetDefinitionHash, "sha256:") {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "confirmed and target_definition_hash required"})
		return
	}
	expectVersion, ok := requireIfMatch(c)
	if !ok {
		return
	}
	// Candidates are stored inside the report jsonb; confirmation updates report.candidates and is gated by run state.
	var state, report string
	var version int64
	if err := s.DB.Raw(`select state, report::text, version from legacy_migration_runs where id=$1`, runID).Row().Scan(&state, &report, &version); err != nil {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1002, Name: "RESOURCE_NOT_FOUND", Message: "migration run not found"})
		return
	}
	if version != expectVersion {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1004, Name: "CONCURRENT_MODIFICATION", Message: "version mismatch"})
		return
	}
	if state != "awaiting_confirmation" {
		// The migration_confirm error profile does not declare 5002; state validation failures map to 5005
		// MIGRATION_INVARIANT_FAILED.
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 5005, Name: "MIGRATION_INVARIANT_FAILED", Message: "run is not awaiting confirmation"})
		return
	}
	// SEC-0049: persist candidate confirmation and its audit event in the same transaction (as required by the
	// workbench purpose contract), capturing the session's actual username in the actor snapshot
	// (replacing the hardcoded 'admin' string in report.candidates[].confirmed_by as well).
	actorID, actorUsername, ok := s.sessionActor(c)
	if !ok {
		return
	}
	updated, err := updateCandidateInReport(report, candidateID, req.Confirmed, req.TargetDefinitionHash, actorUsername)
	if err != nil {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1002, Name: "RESOURCE_NOT_FOUND", Message: err.Error()})
		return
	}
	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		// Optimistic concurrency: WHERE version = $expected and increment version; 0 rows affected means conflict -> 1004.
		res := tx.Exec(`update legacy_migration_runs set report = $2, version = version + 1, updated_at = now()
			where id = $1 and version = $3`, runID, jsonRaw(updated), expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errCandidateConcurrent
		}
		return insertAuditEvent(tx, c, auditEventInput{
			EventType: "migration.candidate_confirmed",
			ActorID:   actorID, ActorUsername: actorUsername,
			ResourceID: runID, Action: "confirm",
			Metadata: map[string]any{
				"candidate_id": candidateID,
				"confirmed":    req.Confirmed,
			},
		})
	})
	if txErr != nil {
		if errors.Is(txErr, errCandidateConcurrent) {
			httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1004, Name: "CONCURRENT_MODIFICATION", Message: "version mismatch"})
			return
		}
		httpapi.WriteProblem(c, http.StatusInternalServerError, "update failed")
		return
	}
	httpapi.WriteSuccess(c, gin.H{"candidate_id": candidateID, "confirmed": req.Confirmed})
}

func (s *Server) handleApprove(c *gin.Context) {
	runID := c.Param("migration_run_id")
	var req struct {
		ManifestHash       string `json:"manifest_hash"`
		ConfirmationPhrase string `json:"confirmation_phrase"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "body required"})
		return
	}
	if req.ConfirmationPhrase != "APPROVE "+runID {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "confirmation phrase mismatch"})
		return
	}
	expectVersion, ok := requireIfMatch(c)
	if !ok {
		return
	}
	var state, report string
	var manifestHash *string
	var version int64
	if err := s.DB.Raw(`select state, report::text, manifest_hash, version from legacy_migration_runs where id=$1`, runID).Row().Scan(&state, &report, &manifestHash, &version); err != nil {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1002, Name: "RESOURCE_NOT_FOUND", Message: "migration run not found"})
		return
	}
	if version != expectVersion {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1004, Name: "CONCURRENT_MODIFICATION", Message: "version mismatch"})
		return
	}
	if state == "planned" || state == "dry_run_running" {
		// The migration_approve error profile declares 5002 when a successful Dry Run has not yet completed.
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 5002, Name: "MIGRATION_DRY_RUN_REQUIRED", Message: "dry run required before approval"})
		return
	}
	if state != "awaiting_confirmation" {
		// Already past the confirmation stage: map state validation failure to 5005.
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 5005, Name: "MIGRATION_INVARIANT_FAILED", Message: "run is not awaiting confirmation"})
		return
	}
	if manifestHash == nil || strings.TrimPrefix(req.ManifestHash, "sha256:") != *manifestHash {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 5005, Name: "MIGRATION_INVARIANT_FAILED", Message: "manifest hash mismatch"})
		return
	}
	if !allCandidatesConfirmed(report) {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 5003, Name: "MIGRATION_MAPPING_UNCONFIRMED", Message: "every candidate must be confirmed first"})
		return
	}
	// SEC-0049: populate approved_by_username_snapshot from the session's actual username (no longer hardcoded
	// to 'admin') and insert the audit_events record in the same transaction for non-repudiation.
	actorID, actorUsername, ok := s.sessionActor(c)
	if !ok {
		return
	}
	var approvedHash string
	if manifestHash != nil {
		approvedHash = *manifestHash
	}
	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		// Optimistic concurrency: WHERE version = $expected and increment version; 0 rows affected means conflict -> 1004.
		res := tx.Exec(`update legacy_migration_runs
			set state='approved', approved_manifest_hash = manifest_hash,
			    approved_by_user_id_snapshot = $2, approved_by_username_snapshot = $3,
			    approved_at = now(), updated_at = now(), version = version + 1
			where id = $1 and state = 'awaiting_confirmation' and version = $4`,
			runID, actorID, actorUsername, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errCandidateConcurrent
		}
		return insertAuditEvent(tx, c, auditEventInput{
			EventType: "migration.approved",
			ActorID:   actorID, ActorUsername: actorUsername,
			ResourceID: runID, Action: "approve",
			Metadata: map[string]any{
				"manifest_hash": approvedHash,
				"prior_state":   state,
				"run_version":   expectVersion,
			},
		})
	})
	if txErr != nil {
		if errors.Is(txErr, errCandidateConcurrent) {
			httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1004, Name: "CONCURRENT_MODIFICATION", Message: "version mismatch"})
			return
		}
		httpapi.WriteProblem(c, http.StatusInternalServerError, "approve failed")
		return
	}
	httpapi.WriteSuccess(c, gin.H{"id": runID, "state": "approved"})
}

// sessionActor returns the current builtin admin's user ID and username for audit snapshots.
func (s *Server) sessionActor(c *gin.Context) (string, string, bool) {
	adminID := c.GetString("admin_user_id")
	var username string
	if err := s.DB.Raw(`select username from users where id = $1`, adminID).Row().Scan(&username); err != nil || username == "" {
		httpapi.WriteProblem(c, http.StatusInternalServerError, "actor resolution failed")
		return "", "", false
	}
	return adminID, username, true
}

// auditEventInput holds parameters for insertAuditEvent; Metadata must contain only non-sensitive identifiers.
type auditEventInput struct {
	EventType     string
	ActorID       string
	ActorUsername string
	ResourceID    string
	Action        string
	Metadata      map[string]any
}

// insertAuditEvent appends an audit_events row within the caller's transaction (SEC-0049).
// expires_at satisfies the audit_retention_exact constraint (occurred_at + 90 days);
// empty source_ip or request_id values are written as NULL (inet/uuid columns reject empty strings).
func insertAuditEvent(tx *gorm.DB, c *gin.Context, in auditEventInput) error {
	var sourceIP any
	if v := c.ClientIP(); v != "" {
		sourceIP = v
	}
	var requestID any
	if id := httpapi.RequestID(c); id != "" {
		if _, err := uuid.Parse(id); err == nil {
			requestID = id
		}
	}
	ua := c.Request.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	var metadata any
	if in.Metadata != nil {
		if raw, err := jsonEncode(in.Metadata); err == nil {
			metadata = json.RawMessage(raw)
		}
	}
	return tx.Exec(`insert into audit_events
		(id, event_type, actor_kind, actor_id, actor_username_snapshot,
		 resource_type, resource_id, action, outcome, request_id,
		 source_ip, user_agent, metadata, occurred_at, expires_at)
		values ($1, $2, 'user', $3::uuid, $4, 'legacy_migration_run', $5::uuid, $6, 'succeeded',
		        $7::uuid, $8::inet, $9, $10::jsonb, now(), now() + interval '90 days')`,
		uuid.NewString(), in.EventType, in.ActorID, in.ActorUsername,
		in.ResourceID, in.Action, requestID, sourceIP, ua, metadata).Error
}

// requireIfMatch parses and enforces the OpenAPI If-Match header (required, matching pattern
// ^"[1-9][0-9]*"$ -- a quoted positive integer). Missing or malformed values return 1001 VALIDATION_FAILED.
func requireIfMatch(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "If-Match must be a quoted positive integer"})
		return 0, false
	}
	v, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || v < 1 {
		httpapi.WriteBusinessError(c, &httpapi.BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "If-Match must be a quoted positive integer"})
		return 0, false
	}
	return v, true
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func jsonRaw(s string) map[string]any {
	var out map[string]any
	_ = jsonUnmarshalMap(s, &out)
	return out
}

func jsonUnmarshalMap(s string, out *map[string]any) error {
	return jsonDecode(s, out)
}

// updateCandidateInReport updates a candidate's confirmed status in the report JSON after verifying its hash,
// recording the session's actual username in confirmed_by (SEC-0049, replacing hardcoded 'admin').
func updateCandidateInReport(report, candidateID string, confirmed bool, targetHash, actor string) (string, error) {
	var obj map[string]any
	if err := jsonDecode(report, &obj); err != nil {
		return "", err
	}
	candidates, _ := obj["candidates"].([]any)
	found := false
	for i, c := range candidates {
		m, _ := c.(map[string]any)
		if m == nil || m["candidate_id"] != candidateID {
			continue
		}
		if m["target_definition_hash"] != targetHash {
			return "", errCandidateHashMismatch
		}
		m["confirmed"] = confirmed
		if confirmed {
			m["confirmed_by"] = actor
			m["confirmed_at"] = time.Now().UTC().Format(time.RFC3339)
		} else {
			m["confirmed_by"] = nil
			m["confirmed_at"] = nil
		}
		candidates[i] = m
		found = true
	}
	if !found {
		return "", errCandidateNotFound
	}
	obj["candidates"] = candidates
	return jsonEncode(obj)
}

func allCandidatesConfirmed(report string) bool {
	var obj map[string]any
	if err := jsonDecode(report, &obj); err != nil {
		return false
	}
	candidates, _ := obj["candidates"].([]any)
	if len(candidates) == 0 {
		return true
	}
	for _, c := range candidates {
		m, _ := c.(map[string]any)
		if m == nil || m["confirmed"] != true {
			return false
		}
	}
	return true
}

type sentinelError string

func (e sentinelError) Error() string { return string(e) }

const (
	errCandidateNotFound     = sentinelError("candidate not found")
	errCandidateHashMismatch = sentinelError("candidate target hash mismatch")
	errCandidateConcurrent   = sentinelError("concurrent modification")
)
