package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/dashboard"
	"github.com/cookieY/YearningX/backend/internal/identity"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
)

var (
	wiringStubOnce     sync.Once
	wiringStubMu       sync.Mutex
	wiringStubRegistry = map[string]*wiringStubState{}
)

type wiringStubState struct {
	mu           sync.Mutex
	cancelledHit chan struct{}
	once         sync.Once
}

type wiringStubDriver struct{}

func (wiringStubDriver) Open(dsn string) (driver.Conn, error) {
	wiringStubMu.Lock()
	st := wiringStubRegistry[dsn]
	wiringStubMu.Unlock()
	if st == nil {
		return nil, errors.New("unknown wiring stub dsn")
	}
	return &wiringStubConn{st: st}, nil
}

type wiringStubConn struct{ st *wiringStubState }

func (*wiringStubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (*wiringStubConn) Close() error                        { return nil }
func (*wiringStubConn) Begin() (driver.Tx, error)           { return &wiringStubTx{}, nil }

type wiringStubTx struct{}

func (*wiringStubTx) Commit() error   { return nil }
func (*wiringStubTx) Rollback() error { return nil }

func (c *wiringStubConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		c.st.once.Do(func() { close(c.st.cancelledHit) })
		return nil, ctx.Err()
	}
	return &wiringStubRows{}, nil
}

func (c *wiringStubConn) ExecContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		c.st.once.Do(func() { close(c.st.cancelledHit) })
		return nil, ctx.Err()
	}
	return driver.RowsAffected(0), nil
}

type wiringStubRows struct{}

func (*wiringStubRows) Columns() []string           { return []string{} }
func (*wiringStubRows) Close() error                { return nil }
func (*wiringStubRows) Next(_ []driver.Value) error { return io.EOF }

func newWiringStubDBAndCrypter(t *testing.T, st *wiringStubState) (*gorm.DB, *crypto.Crypter) {
	t.Helper()
	wiringStubOnce.Do(func() {
		sql.Register("httpapi_wiring_stub", wiringStubDriver{})
	})
	dsn := fmt.Sprintf("wiring-%d", time.Now().UnixNano())
	wiringStubMu.Lock()
	wiringStubRegistry[dsn] = st
	wiringStubMu.Unlock()
	t.Cleanup(func() {
		wiringStubMu.Lock()
		delete(wiringStubRegistry, dsn)
		wiringStubMu.Unlock()
	})

	sqlDB, err := sql.Open("httpapi_wiring_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	gdb, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}

	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"wiring-unit","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"id-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"cur-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(1), makeKey(2), makeKey(3), makeKey(4))
	if err := os.WriteFile(keyPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return gdb, crypter
}

func TestStrictWiringSkipsLazyDomainDerivation(t *testing.T) {
	st := &wiringStubState{cancelledHit: make(chan struct{})}
	gdb, crypter := newWiringStubDBAndCrypter(t, st)
	sessions, err := identity.NewSessionStore(gdb, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	router := NewServeRouter(ServeDeps{
		Logger:       zap.NewNop(),
		StrictWiring: true,
		B3: &B3Deps{
			DB:       gdb,
			Crypter:  crypter,
			Sessions: sessions,
		},
	})

	for _, path := range []string{
		"/api/v4/change-drafts",
		"/api/v4/admin/prompt-tools",
		"/api/v4/change-orders",
		"/api/v4/query-sessions",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("StrictWiring GET %s status = %d, want 404 (must not lazily derive domain routes)", path, w.Code)
		}
	}
}

func TestBuildServeDepsFailsFastOnMissingB3AndPropagatesLifecycleCtx(t *testing.T) {
	if _, err := BuildServeDeps(context.Background(), ServeDeps{}); err == nil {
		t.Fatal("expected BuildServeDeps to fail when B3 platform deps are missing")
	}

	st := &wiringStubState{cancelledHit: make(chan struct{})}
	gdb, crypter := newWiringStubDBAndCrypter(t, st)
	recorder, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := identity.NewSessionStore(gdb, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wired, err := BuildServeDeps(context.Background(), ServeDeps{
		Logger: zap.NewNop(),
		B3: &B3Deps{
			DB:       gdb,
			Crypter:  crypter,
			Recorder: recorder,
			Sessions: sessions,
		},
	})
	if err != nil {
		t.Fatalf("BuildServeDeps failed: %v", err)
	}
	if !wired.StrictWiring || wired.B6 == nil || wired.B7 == nil || wired.B9 == nil || wired.B10 == nil || wired.B11 == nil || wired.Assistant == nil || wired.Diagnostics == nil {
		t.Fatalf("BuildServeDeps left domain deps unwired: %+v", wired)
	}

	reqHit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reqHit <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		st.once.Do(func() { close(st.cancelledHit) })
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dashSvc := dashboard.New(gdb, crypter)
	dashSvc.ReleaseHTTPClient = srv.Client()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = NewServeRouter(ServeDeps{
		Logger:               zap.NewNop(),
		StrictWiring:         true,
		StartBackgroundLoops: true,
		LifecycleCtx:         ctx,
		Dashboard: &dashboard.Deps{
			Service:        dashSvc,
			ReleaseFeedURL: srv.URL,
		},
	})

	select {
	case <-reqHit:
	case <-time.After(2 * time.Second):
		t.Fatal("expected release refresh loop to issue initial request")
	}

	cancel()

	select {
	case <-st.cancelledHit:
	case <-time.After(2 * time.Second):
		t.Fatal("expected background loop to observe cancelled LifecycleCtx")
	}
}

func TestMapReviewError_SystemCapacityRetryableAndMeta(t *testing.T) {
	be := MapReviewError(&reviewdomain.Error{
		Code:   1012,
		Name:   "SYSTEM_CAPACITY_EXHAUSTED",
		Detail: "active sql memory budget exhausted",
	})
	if be == nil {
		t.Fatal("expected non-nil BusinessError")
	}
	if be.Code != 1012 || be.Name != "SYSTEM_CAPACITY_EXHAUSTED" {
		t.Fatalf("unexpected code/name: code=%d name=%q", be.Code, be.Name)
	}
	if !be.Retryable {
		t.Fatal("expected code 1012 BusinessError to set Retryable=true")
	}
	if be.Meta == nil || be.Meta["retry_after_ms"] != 1000 {
		t.Fatalf("expected meta.retry_after_ms=1000, got %+v", be.Meta)
	}
	if be.Meta["detail"] != "active sql memory budget exhausted" {
		t.Fatalf("expected detail to be preserved, got %+v", be.Meta)
	}
}
