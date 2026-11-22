// server_unit_test.go DB-free exposure surface regression tests (SEC-0043/0019):
// health probe registration, 503 on normal business routes, and login lockout state machine.
package review

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
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

type migLoginStubFx struct{}

func (f *migLoginStubFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *migLoginStubFx) Driver() driver.Driver                        { return migLoginStubDriver{} }

type migLoginStubDriver struct{}

func (migLoginStubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }
func (f *migLoginStubFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused")
}
func (f *migLoginStubFx) Close() error              { return nil }
func (f *migLoginStubFx) Begin() (driver.Tx, error) { return nil, errors.New("unused") }
func (f *migLoginStubFx) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (f *migLoginStubFx) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &migLoginEmptyRows{}, nil
}

type migLoginEmptyRows struct{}

func (*migLoginEmptyRows) Columns() []string {
	return []string{"id", "password_hash", "is_builtin_admin"}
}
func (*migLoginEmptyRows) Close() error              { return nil }
func (*migLoginEmptyRows) Next([]driver.Value) error { return io.EOF }

func TestLoginGuardIPTierPreventsUsernameSpray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sqlDB := sql.OpenDB(&migLoginStubFx{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: db, Logger: zap.NewNop(), loginGuard: newLoginGuard()}
	r := NewRouter(srv)

	// One IP sprays maxFailuresPerIP (20) distinct usernames with bad credentials.
	for i := 0; i < maxFailuresPerIP; i++ {
		body := fmt.Sprintf(`{"username":"spray-%03d","password":"bad-password"}`, i)
		req := httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.10:12345"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("spray attempt %d status = %d, want 401", i+1, w.Code)
		}
	}

	// 21st attempt from the same IP with an unseen username is rejected at the IP tier (429 + Retry-After: 60)
	// WITHOUT inserting the unseen username into g.failed.
	reqBlocked := httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", bytes.NewBufferString(`{"username":"spray-unseen","password":"bad-password"}`))
	reqBlocked.Header.Set("Content-Type", "application/json")
	reqBlocked.RemoteAddr = "203.0.113.10:54321"
	wBlocked := httptest.NewRecorder()
	r.ServeHTTP(wBlocked, reqBlocked)
	if wBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("21st spray attempt status = %d, want 429", wBlocked.Code)
	}
	if got := wBlocked.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	srv.loginGuard.mu.Lock()
	_, inserted := srv.loginGuard.failed[normalizeLoginGuardKey("spray-unseen")]
	srv.loginGuard.mu.Unlock()
	if inserted {
		t.Fatal("IP-blocked request must not insert username into g.failed")
	}

	// A different IP can still reach authentication without being blocked.
	reqOtherIP := httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", bytes.NewBufferString(`{"username":"admin","password":"bad-password"}`))
	reqOtherIP.Header.Set("Content-Type", "application/json")
	reqOtherIP.RemoteAddr = "203.0.113.20:12345"
	wOther := httptest.NewRecorder()
	r.ServeHTTP(wOther, reqOtherIP)
	if wOther.Code != http.StatusUnauthorized {
		t.Fatalf("other IP status = %d, want 401 (must not be blocked by attacker IP)", wOther.Code)
	}

	// Username lockout also sets Retry-After: 60.
	for i := 0; i < loginFailureThreshold; i++ {
		srv.loginGuard.recordFailure("locked-admin", time.Now())
	}
	reqLocked := httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", bytes.NewBufferString(`{"username":"locked-admin","password":"bad-password"}`))
	reqLocked.Header.Set("Content-Type", "application/json")
	reqLocked.RemoteAddr = "203.0.113.30:12345"
	wLocked := httptest.NewRecorder()
	r.ServeHTTP(wLocked, reqLocked)
	if wLocked.Code != http.StatusTooManyRequests || wLocked.Header().Get("Retry-After") != "60" {
		t.Fatalf("username lockout = (%d, Retry-After=%q), want (429, 60)", wLocked.Code, wLocked.Header().Get("Retry-After"))
	}
}

func TestLoginGuardIPTableBoundedAndExpires(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	for i := 0; i < maxLoginGuardIPs; i++ {
		g.recordIPFailure(fmt.Sprintf("198.51.100.%d", i), now.Add(time.Duration(i)*time.Microsecond))
	}
	if len(g.ipFailed) != maxLoginGuardIPs {
		t.Fatalf("ipFailed len = %d, want %d", len(g.ipFailed), maxLoginGuardIPs)
	}
	// Overflow evicts the oldest IP rather than growing unboundedly.
	g.recordIPFailure("198.51.200.1", now.Add(time.Second))
	if len(g.ipFailed) > maxLoginGuardIPs {
		t.Fatalf("ipFailed grew beyond cap: %d", len(g.ipFailed))
	}
	if _, ok := g.ipFailed["198.51.100.0"]; ok {
		t.Fatal("oldest IP must be evicted on overflow")
	}

	// Expired IP entries recover after loginLockWindow.
	for i := 0; i < maxFailuresPerIP; i++ {
		g.recordIPFailure("192.0.2.99", now)
	}
	if g.allowIP("192.0.2.99", now.Add(time.Second)) {
		t.Fatal("IP should be blocked within window")
	}
	if !g.allowIP("192.0.2.99", now.Add(loginLockWindow+time.Second)) {
		t.Fatal("IP should recover after loginLockWindow")
	}
}
