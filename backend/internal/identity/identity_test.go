package identity

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func TestArgon2HashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash format: %q", hash[:20])
	}
	ok, upgrade, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok || upgrade {
		t.Fatalf("verify=%v upgrade=%v err=%v", ok, upgrade, err)
	}
	ok, _, _ = VerifyPassword("wrong password", hash)
	if ok {
		t.Fatal("wrong password accepted")
	}
	// Malformed hash must be rejected without panicking.
	ok, _, err = VerifyPassword("x", "not-a-hash")
	if ok || err == nil {
		t.Fatal("malformed hash must fail")
	}
	// Malformed Argon2id parameter/version/digest edge cases must be rejected without panicking.
	for _, bad := range []string{
		"$argon2id$v=19$m=65536,t=3,p=257$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2,x=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2$$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$",
	} {
		ok, _, err = VerifyPassword("x", bad)
		if ok || err == nil {
			t.Fatalf("malformed argon2id hash %q must be rejected", bad)
		}
	}
}

func TestDjangoPBKDF2VerifyAndUpgrade(t *testing.T) {
	hash, err := MakeDjangoPBKDF2("legacy-secret-pass", 600000)
	if err != nil {
		t.Fatal(err)
	}
	ok, upgrade, err := VerifyPassword("legacy-secret-pass", hash)
	if err != nil || !ok || !upgrade {
		t.Fatalf("verify=%v upgrade=%v err=%v", ok, upgrade, err)
	}
	ok, _, _ = VerifyPassword("wrong", hash)
	if ok {
		t.Fatal("wrong legacy password accepted")
	}
	// Legacy hashes below the allowlisted iteration count must be rejected.
	weak := "pbkdf2_sha256$1000$c2FsdA$aaaa"
	if _, _, err := VerifyPassword("x", weak); err == nil {
		t.Fatal("weak legacy hash accepted")
	}
}

func TestLDAPConfigurationValidation(t *testing.T) {
	valid := LDAPConfiguration{
		Host: "ldap.example.com", Port: 636, Transport: "ldaps",
		ServerName: "ldap.example.com", BaseDN: "dc=example,dc=com",
		UserFilter:        "(&(objectClass=person)(uid={username}))",
		UsernameAttribute: "uid", DisplayNameAttribute: "cn",
		ConnectTimeoutMS: 3000, BindTimeoutMS: 3000, SearchTimeoutMS: 5000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Plaintext LDAP transport is forbidden.
	plain := valid
	plain.Transport = "plain"
	if err := plain.Validate(); err == nil {
		t.Fatal("plaintext ldap accepted")
	}
	// Invalid placeholder count.
	noPlaceholder := valid
	noPlaceholder.UserFilter = "(uid=user)"
	if err := noPlaceholder.Validate(); err == nil {
		t.Fatal("filter without {username} accepted")
	}
	dup := valid
	dup.UserFilter = "(|(uid={username})(cn={username}))"
	if err := dup.Validate(); err == nil {
		t.Fatal("filter with two placeholders accepted")
	}
	// Timeout bounds.
	short := valid
	short.ConnectTimeoutMS = 10
	if err := short.Validate(); err == nil {
		t.Fatal("short timeout accepted")
	}
}

func TestOIDCStateSignVerifyAndTamper(t *testing.T) {
	c := newTestCrypter(t)
	payload := oidcStatePayload{
		ProviderKey: "sso", Verifier: "v-v-v", Nonce: "n-n-n",
		ExpireAt:    time.Now().UTC().Add(stateTTL).Unix(),
		RedirectURI: "https://app.example.com/api/v4/auth/oidc/sso/callback",
	}
	state, err := SignState(c, payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyState(c, state)
	if err != nil || got.ProviderKey != "sso" || got.Nonce != "n-n-n" {
		t.Fatalf("verify failed: %+v err=%v", got, err)
	}
	// Expired state must be rejected.
	payload.ExpireAt = time.Now().UTC().Add(-time.Minute).Unix()
	expired, _ := SignState(c, payload)
	if _, err := VerifyState(c, expired); err == nil {
		t.Fatal("expired state accepted")
	}
	// Tampered state must be rejected.
	if _, err := VerifyState(c, state+"x"); err == nil {
		t.Fatal("tampered state accepted")
	}
	if _, err := VerifyState(c, "not-a-state"); err == nil {
		t.Fatal("garbage state accepted")
	}
}

func TestOIDCConfigurationValidation(t *testing.T) {
	valid := OIDCConfiguration{
		IssuerURL: "https://sso.example.com", ClientID: "yearning",
		Scopes: []string{"openid", "profile"}, UsernameClaim: "preferred_username",
		ConnectTimeoutMS: 3000, RequestTimeoutMS: 5000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	for name, mutate := range map[string]func(*OIDCConfiguration){
		"http issuer": func(c *OIDCConfiguration) { c.IssuerURL = "http://insecure" },
		"no openid":   func(c *OIDCConfiguration) { c.Scopes = []string{"profile"} },
		"no username": func(c *OIDCConfiguration) { c.UsernameClaim = "" },
		"bad timeout": func(c *OIDCConfiguration) { c.RequestTimeoutMS = 10 },
	} {
		mutated := valid
		mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func newTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	return testkit.NewCrypter(t)
}

func TestValidOptionalEmail(t *testing.T) {
	valid := []string{"", "   ", "alice@example.com", "qa.user+tag@sub.yearning.io"}
	for _, v := range valid {
		if !validOptionalEmail(v) {
			t.Fatalf("expected %q to be valid optional email", v)
		}
	}
	invalid := []string{"invalid_email_no_at_symbol", "@example.com", "alice@", "alice@localhost", "alice@domain.", "alice@.domain.com", "alice @example.com"}
	for _, v := range invalid {
		if validOptionalEmail(v) {
			t.Fatalf("expected %q to be rejected as invalid email", v)
		}
	}
}

func TestArgon2LimiterCapacityAndTimeout(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer SetArgon2LimiterForTest(1, 25*time.Millisecond)()

	// Hold the sole slot so subsequent calls must time out with ErrPasswordHashBusy.
	release, err := acquireArgon2Slot(context.Background())
	if err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	if _, _, verr := VerifyPasswordCtx(context.Background(), "correct horse battery staple", hash); !errors.Is(verr, ErrPasswordHashBusy) {
		t.Fatalf("VerifyPasswordCtx while busy = %v, want ErrPasswordHashBusy", verr)
	}
	if _, herr := HashPasswordCtx(context.Background(), "another-password-value"); !errors.Is(herr, ErrPasswordHashBusy) {
		t.Fatalf("HashPasswordCtx while busy = %v, want ErrPasswordHashBusy", herr)
	}

	// Cancelled context rejects immediately without waiting for timeout.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, cerr := VerifyPasswordCtx(cctx, "correct horse battery staple", hash); !errors.Is(cerr, ErrPasswordHashBusy) {
		t.Fatalf("VerifyPasswordCtx with cancelled ctx = %v, want ErrPasswordHashBusy", cerr)
	}

	// Releasing the slot allows verification to succeed again.
	release()
	released = true
	ok, _, err := VerifyPasswordCtx(context.Background(), "correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("VerifyPasswordCtx after release = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestArgon2LimiterBoundedConcurrencyRace(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer SetArgon2LimiterForTest(2, 2*time.Second)()

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, verr := VerifyPasswordCtx(context.Background(), "correct horse battery staple", hash)
			if verr != nil || !ok {
				errs <- fmt.Errorf("ok=%v err=%v", ok, verr)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

type identityStubFx struct {
	adminHash       string
	failedAttempts  int16
	adminSecUpdates int
	txBegins        int
}

func (f *identityStubFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *identityStubFx) Driver() driver.Driver                        { return identityStubDriver{} }

type identityStubDriver struct{}

func (identityStubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }
func (f *identityStubFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused")
}
func (f *identityStubFx) Close() error { return nil }
func (f *identityStubFx) Begin() (driver.Tx, error) {
	f.txBegins++
	return &identityStubTx{}, nil
}
func (f *identityStubFx) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(strings.ToLower(query), "admin_login_security") {
		f.adminSecUpdates++
	}
	return driver.RowsAffected(1), nil
}
func (f *identityStubFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	now := time.Now().UTC()
	if strings.Contains(q, `from "users"`) {
		return &identitySingleRow{
			cols: []string{"id", "username", "password_hash", "display_name", "email", "is_builtin_admin", "role", "version", "created_at", "updated_at"},
			vals: []driver.Value{"0198da00-0000-7000-8000-000000000001", "admin", f.adminHash, "Admin", nil, true, "admin", int64(1), now, now},
		}, nil
	}
	if strings.Contains(q, `from "admin_login_security"`) {
		return &identitySingleRow{
			cols: []string{"user_id", "failed_attempts", "locked_at", "updated_at"},
			vals: []driver.Value{"0198da00-0000-7000-8000-000000000001", int64(f.failedAttempts), nil, now},
		}, nil
	}
	if strings.Contains(q, `from "external_identities"`) {
		return &identitySingleRow{
			cols: []string{"count"},
			vals: []driver.Value{int64(0)},
		}, nil
	}
	return &identitySingleRow{}, nil
}

type identityStubTx struct{}

func (*identityStubTx) Commit() error   { return nil }
func (*identityStubTx) Rollback() error { return nil }

type identitySingleRow struct {
	cols []string
	vals []driver.Value
	done bool
}

func (r *identitySingleRow) Columns() []string { return r.cols }
func (r *identitySingleRow) Close() error      { return nil }
func (r *identitySingleRow) Next(dest []driver.Value) error {
	if r.done || len(r.cols) == 0 {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}

func TestLoginLocalMapsSemaphoreBusyToRateLimitedWithoutAdminLockout(t *testing.T) {
	hash, err := HashPassword("admin-correct-password")
	if err != nil {
		t.Fatal(err)
	}
	fx := &identityStubFx{adminHash: hash, failedAttempts: 4}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionStore(db, DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(db, sessions, nil, recorder)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-populate per-(user,IP) failure state to verify saturation does not delete it.
	svc.limit.allowLogin("admin|10.1.1.1", "login-ip:10.1.1.1")

	defer SetArgon2LimiterForTest(1, 20*time.Millisecond)()
	release, err := acquireArgon2Slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, lerr := svc.LoginLocal(context.Background(), "admin", "wrong-password", RequestMeta{SourceIP: "10.1.1.1"})
	if !errors.Is(lerr, ErrRateLimited) {
		t.Fatalf("LoginLocal when Argon2id busy = %v, want ErrRateLimited", lerr)
	}
	if fx.adminSecUpdates != 0 {
		t.Fatalf("admin_login_security must not be updated when Argon2id busy, got %d updates", fx.adminSecUpdates)
	}
	svc.limit.mu.Lock()
	remaining := len(svc.limit.buckets["admin|10.1.1.1"])
	ipRemaining := len(svc.limit.buckets["login-ip:10.1.1.1"])
	svc.limit.mu.Unlock()
	if remaining != 1 || ipRemaining != 1 {
		t.Fatalf("expected prior failure preserved and busy attempt undone (user=%d ip=%d), want (1, 1)", remaining, ipRemaining)
	}
}

func TestLoginRateLimiterTieredPerIPAndSuccessAllowance(t *testing.T) {
	l := newLoginRateLimiter()

	// 1. Spraying 30 distinct usernames from one IP fills the per-IP tier (rateMaxFailsPerIP = 30),
	// blocking the 31st unseen username while leaving other IPs unaffected.
	for i := 0; i < rateMaxFailsPerIP; i++ {
		userKey := fmt.Sprintf("spray-%02d|10.0.0.1", i)
		if !l.allowLogin(userKey, "login-ip:10.0.0.1") {
			t.Fatalf("attempt %d from 10.0.0.1 unexpectedly blocked", i+1)
		}
	}
	if l.allowLogin("brand-new-user|10.0.0.1", "login-ip:10.0.0.1") {
		t.Fatal("31st attempt across distinct usernames from same IP must be blocked by per-IP tier")
	}
	if !l.allowLogin("brand-new-user|10.0.0.2", "login-ip:10.0.0.2") {
		t.Fatal("distinct source IP must not be blocked by 10.0.0.1 spray")
	}

	// 2. Repeated successful logins from a shared NAT IP pop their IP slot and are never throttled.
	for i := 0; i < rateMaxFailsPerIP+20; i++ {
		userKey := fmt.Sprintf("legit-%02d|10.0.0.3", i)
		ipKey := "login-ip:10.0.0.3"
		if !l.allowLogin(userKey, ipKey) {
			t.Fatalf("legitimate login %d from shared NAT IP blocked", i+1)
		}
		l.loginSuccess(userKey, ipKey)
	}
}

func TestChangeMyPasswordBusyAvoidsTxLock(t *testing.T) {
	hash, err := HashPassword("old-correct-password")
	if err != nil {
		t.Fatal(err)
	}
	fx := &identityStubFx{adminHash: hash}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionStore(db, DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(db, sessions, nil, recorder)
	if err != nil {
		t.Fatal(err)
	}

	defer SetArgon2LimiterForTest(1, 20*time.Millisecond)()
	release, err := acquireArgon2Slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	errBusy := svc.ChangeMyPassword(context.Background(), "0198da00-0000-7000-8000-000000000001",
		"old-correct-password", "new-strong-password-123", RequestMeta{})
	release()

	if !errors.Is(errBusy, ErrPasswordHashBusy) {
		t.Fatalf("ChangeMyPassword when Argon2id busy = %v, want ErrPasswordHashBusy", errBusy)
	}
	if fx.txBegins != 0 {
		t.Fatalf("expected 0 DB transactions (no FOR UPDATE lock) across saturated Argon2id wait, got %d", fx.txBegins)
	}

	// Once semaphore capacity is available, invalid old password fails without starting a FOR UPDATE tx,
	// whereas a valid password change commits cleanly inside a transaction.
	errBadOld := svc.ChangeMyPassword(context.Background(), "0198da00-0000-7000-8000-000000000001",
		"wrong-old-password", "new-strong-password-123", RequestMeta{})
	if !errors.Is(errBadOld, bizerr.ErrInvalidCredentials) {
		t.Fatalf("ChangeMyPassword invalid old password = %v, want ErrInvalidCredentials", errBadOld)
	}
	if fx.txBegins != 0 {
		t.Fatalf("expected 0 DB transactions on wrong old password, got %d", fx.txBegins)
	}
	if err := svc.ChangeMyPassword(context.Background(), "0198da00-0000-7000-8000-000000000001",
		"old-correct-password", "new-strong-password-123", RequestMeta{}); err != nil {
		t.Fatalf("ChangeMyPassword valid update = %v, want nil", err)
	}
	if fx.txBegins != 1 {
		t.Fatalf("expected 1 DB transaction only after Argon2id verify & hash finished, got %d", fx.txBegins)
	}
}

func TestUnusedBizErrReferenceCheck(t *testing.T) {
	// Sanity-check ErrInvalidCredentials remains unchanged for normal failures.
	if bizerr.ErrInvalidCredentials == nil {
		t.Fatal("unexpected nil sentinel")
	}
}
