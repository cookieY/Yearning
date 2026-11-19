// server_unit_test.go DB-free exposure surface regression tests (SEC-0043/0019):
// health probe registration, 503 on normal business routes, and login lockout state machine.
package review

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func unitRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	s := &Server{Logger: zap.NewNop()}
	return NewRouter(s)
}

// TestHealthEndpointsRegistered SEC-0019 regression: migration mode registers
// /api/v4/health/live|ready probe endpoints, and live returns 200 without touching dependencies.
func TestHealthEndpointsRegistered(t *testing.T) {
	r := unitRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v4/health/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("live probe = %d, want 200", rec.Code)
	}
	// ready requires a DB Pool: a nil Pool panics and recovers as 500 -- here we only assert that the route
	// is registered rather than falling through to 404 or 503 NoRoute.
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v4/health/ready", nil))
	if rec2.Code == http.StatusServiceUnavailable && rec2.Body.String() == "" {
		t.Fatal("ready must be a registered route, not NoRoute 503")
	}
}

// TestBusinessRoutesStillUnavailable contract invariant: normal business routes return 503 in migration mode.
func TestBusinessRoutesStillUnavailable(t *testing.T) {
	r := unitRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v4/change-orders", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("business route = %d, want 503", rec.Code)
	}
}

// TestUnauthenticatedRequestsRejected admin migration endpoints reject requests without a session cookie with 401.
func TestUnauthenticatedRequestsRejected(t *testing.T) {
	r := unitRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v4/admin/migrations", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin access = %d, want 401", rec.Code)
	}
}

// TestLoginGuardLockoutState SEC-0043 regression: locks out after consecutive failures reach the threshold,
// rejects immediately during the lockout window, and resets on successful login.
func TestLoginGuardLockoutState(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	for i := 0; i < loginFailureThreshold-1; i++ {
		g.recordFailure("admin", now)
		if g.locked("admin", now) {
			t.Fatalf("locked before threshold at attempt %d", i+1)
		}
	}
	g.recordFailure("admin", now)
	if !g.locked("admin", now.Add(time.Second)) {
		t.Fatal("must be locked after threshold failures")
	}
	if g.locked("admin", now.Add(loginLockWindow+time.Second)) {
		t.Fatal("lock must expire after window")
	}
	// Successful login resets the failure counter (once the lockout window expires).
	g.recordSuccess("admin")
	for i := 0; i < loginFailureThreshold-1; i++ {
		g.recordFailure("admin", now)
	}
	if g.locked("admin", now.Add(time.Second)) {
		t.Fatal("counter must reset after successful login")
	}
}

// TestLoginGuardPerAccountIsolation verifies that lockout state is isolated per account.
func TestLoginGuardPerAccountIsolation(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	for i := 0; i < loginFailureThreshold; i++ {
		g.recordFailure("alice", now)
	}
	if !g.locked("alice", now) {
		t.Fatal("alice must be locked")
	}
	if g.locked("bob", now) {
		t.Fatal("bob must not be locked by alice failures")
	}
}

// TestLoginGuardCaseVariantSharesBucket NEW-AUTH-02 regression: lockout keys normalize via lowercase +
// trim whitespace so case/whitespace variants of the same account share a single failure bucket rather than
// each receiving an independent 5-attempt quota.
func TestLoginGuardCaseVariantSharesBucket(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	// Spread 4 failures across case/whitespace variants.
	variants := []string{"Admin", "ADMIN", "aDmIn", " admin "}
	for i, v := range variants {
		if g.locked(v, now) {
			t.Fatalf("variant %q locked before threshold", v)
		}
		g.recordFailure(v, now.Add(time.Duration(i)*time.Millisecond))
	}
	// The 5th failure (also a variant) triggers lockout: all variants and the canonical key are locked uniformly.
	g.recordFailure("AdMiN", now.Add(5*time.Millisecond))
	for _, v := range []string{"admin", "Admin", "ADMIN", "aDmIn", "  ADMIN  ", "admin "} {
		if !g.locked(v, now.Add(time.Second)) {
			t.Fatalf("variant %q must be locked (shared bucket after normalization)", v)
		}
	}
	// A successful login under any variant clears the bucket and unlocks all variants.
	g.recordSuccess("ADMIN")
	for _, v := range variants {
		if g.locked(v, now.Add(2*time.Second)) {
			t.Fatalf("variant %q must be unlocked after success on another variant", v)
		}
	}
}

// TestLoginGuardFailureTableBounded NEW-AUTH-02 regression: the failure table key count is bounded;
// when full (and no active locks exist), new keys evict the oldest key by lastFailure, and expired entries outside the window are swept.
func TestLoginGuardFailureTableBounded(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	// Advance failure timestamps monotonically so the "oldest" key is deterministic.
	for i := 0; i < maxLoginGuardKeys; i++ {
		g.recordFailure(fmt.Sprintf("user-%05d", i), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(g.failed) != maxLoginGuardKeys {
		t.Fatalf("table size = %d, want %d", len(g.failed), maxLoginGuardKeys)
	}
	g.recordFailure("overflow-user", now.Add(maxLoginGuardKeys*time.Millisecond))
	if len(g.failed) > maxLoginGuardKeys {
		t.Fatalf("table size = %d after overflow insert, want <= %d", len(g.failed), maxLoginGuardKeys)
	}
	if _, ok := g.failed[normalizeLoginGuardKey("user-00000")]; ok {
		t.Fatal("oldest entry must be evicted when table is full")
	}
	if _, ok := g.failed["overflow-user"]; !ok {
		t.Fatal("newest entry must be kept after eviction")
	}

	// Expiration sweep: unlocked keys whose last failure is outside the window are reclaimed, while in-window keys are kept.
	g.recordFailure("stale-user", now.Add(-2*loginLockWindow))
	g.recordFailure("fresh-user", now)
	g.sweepExpiredLocked(now)
	if _, ok := g.failed[normalizeLoginGuardKey("stale-user")]; ok {
		t.Fatal("stale entry must be swept")
	}
	if _, ok := g.failed[normalizeLoginGuardKey("fresh-user")]; !ok {
		t.Fatal("fresh entry must survive sweep")
	}

	// Partial counts outside the window expire across windows: a single failure after window expiration restarts at 1 without locking.
	g.recordFailure("legacy-user", now.Add(-2*loginLockWindow))
	g.recordFailure("legacy-user", now.Add(-2*loginLockWindow))
	g.recordFailure("legacy-user", now.Add(-2*loginLockWindow))
	g.recordFailure("legacy-user", now.Add(-2*loginLockWindow))
	g.recordFailure("legacy-user", now)
	if g.locked("legacy-user", now.Add(time.Second)) {
		t.Fatal("stale partial count must reset across window (5th fresh failure only starts a new count)")
	}
}

// TestLoginGuardDoesNotEvictActiveLocks NEW-AUTH-07 regression: when the table is full of entries
// within their lockout window, active locks must never be evicted by age; once saturated, new logins fail closed
// and automatically recover after locks expire.
func TestLoginGuardDoesNotEvictActiveLocks(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	for i := 0; i < maxLoginGuardKeys; i++ {
		name := fmt.Sprintf("locked-%05d", i)
		for j := 0; j < loginFailureThreshold; j++ {
			g.recordFailure(name, now.Add(time.Duration(i)*time.Microsecond))
		}
	}
	if len(g.failed) != maxLoginGuardKeys {
		t.Fatalf("table size = %d, want %d", len(g.failed), maxLoginGuardKeys)
	}
	// Target lock cannot be evicted by spraying new usernames: no new keys are inserted while saturated.
	for i := 0; i < 10; i++ {
		g.recordFailure(fmt.Sprintf("spray-%02d", i), now.Add(2*time.Second))
	}
	if _, ok := g.failed[normalizeLoginGuardKey("locked-00000")]; !ok {
		t.Fatal("active lock evicted by username spray")
	}
	if len(g.failed) != maxLoginGuardKeys {
		t.Fatalf("table grew under saturation: %d", len(g.failed))
	}
	if !g.locked("victim", now.Add(3*time.Second)) {
		t.Fatal("saturated guard must fail closed for uncached usernames")
	}
	// Once all locks expire: sweeping reclaims capacity and normal evaluation resumes.
	later := now.Add(loginLockWindow + time.Minute)
	if g.locked("victim", later) {
		t.Fatal("saturation must clear after all locks expire")
	}
	if len(g.failed) >= maxLoginGuardKeys {
		t.Fatalf("expired locks not swept: %d", len(g.failed))
	}
}

// TestLoginGuardPartialCountNotEvictedWhenActiveLocksExist NEW-AUTH-11:
// if an attacker fills 999 slots with active locks while leaving 1 target account with a partial failure count,
// spraying a new username must not evict that partial count (which would reset the target's 5th attempt back to 1);
// instead the guard enters saturation and fails closed.
func TestLoginGuardPartialCountNotEvictedWhenActiveLocksExist(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	for i := 0; i < maxLoginGuardKeys-1; i++ {
		name := fmt.Sprintf("locked-%05d", i)
		for j := 0; j < loginFailureThreshold; j++ {
			g.recordFailure(name, now.Add(time.Duration(i)*time.Microsecond))
		}
	}
	target := "target-admin"
	for i := 0; i < loginFailureThreshold-1; i++ {
		g.recordFailure(target, now.Add(time.Second))
	}
	if len(g.failed) != maxLoginGuardKeys {
		t.Fatalf("table size = %d, want %d", len(g.failed), maxLoginGuardKeys)
	}
	before, ok := g.failed[normalizeLoginGuardKey(target)]
	if !ok {
		t.Fatal("target partial count missing")
	}
	for i := 0; i < 10; i++ {
		g.recordFailure(fmt.Sprintf("spray-%02d", i), now.Add(2*time.Second))
	}
	after, ok := g.failed[normalizeLoginGuardKey(target)]
	if !ok {
		t.Fatal("target partial count evicted by spray while active locks exist (NEW-AUTH-11)")
	}
	if after.count != before.count {
		t.Fatalf("target partial count changed %d -> %d under spray", before.count, after.count)
	}
	if !g.locked("unseen-user", now.Add(3*time.Second)) {
		t.Fatal("guard must fail closed when saturated by active locks")
	}
}
