// Package review implements `serve --migration-mode`: exposes only login/logout/current-user and
// /api/v4/admin/migrations* while returning 503 for all normal business routes. Runs under the
// yearning_migration_workbench database role.
//
// Surface hardening (SEC-0043): login failure throttling + account lockout, double-submit CSRF protection
// on mutating methods (Cookie + X-CSRF-Token header, matching the main server's yearning_csrf posture),
// login failure audit logging, and /api/v4/health/live|ready probe endpoints (matching normal serve probe semantics).
package review

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/httpapi"
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
