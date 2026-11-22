package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	changeapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/dashboard"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	execdomain "github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/tasks"
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

func TestWriteDomainCodedError_1012RetryableWithoutMutatingCallerMeta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("1012 populates retryable and retry_after_ms without mutating caller map", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v4/query-sessions/1/execute", nil)

		callerMeta := map[string]any{"detail": "query pool slots busy"}
		ok := writeDomainCodedError(c, 1012, "SYSTEM_CAPACITY_EXHAUSTED", "Query request rejected", callerMeta)
		if !ok {
			t.Fatal("expected writeDomainCodedError to return true")
		}
		if _, leaked := callerMeta["retry_after_ms"]; leaked {
			t.Fatalf("callerMeta was mutated: %+v", callerMeta)
		}

		var body struct {
			ErrCode   int            `json:"err_code"`
			Message   string         `json:"message"`
			Retryable bool           `json:"retryable"`
			Meta      map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if body.ErrCode != 1012 || !body.Retryable {
			t.Fatalf("expected err_code=1012 retryable=true, got err_code=%d retryable=%v", body.ErrCode, body.Retryable)
		}
		if body.Meta["retry_after_ms"] != float64(1000) || body.Meta["detail"] != "query pool slots busy" {
			t.Fatalf("unexpected meta: %+v", body.Meta)
		}
	})

	t.Run("1012 preserves caller retry_after_ms and handles nil meta", func(t *testing.T) {
		w1 := httptest.NewRecorder()
		c1, _ := gin.CreateTestContext(w1)
		c1.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		writeDomainCodedError(c1, 1012, "SYSTEM_CAPACITY_EXHAUSTED", "", nil)

		var b1 struct {
			Retryable bool           `json:"retryable"`
			Meta      map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(w1.Body.Bytes(), &b1); err != nil {
			t.Fatal(err)
		}
		if !b1.Retryable || b1.Meta["retry_after_ms"] != float64(1000) {
			t.Fatalf("expected default retry_after_ms=1000, got %+v", b1)
		}

		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		writeDomainCodedError(c2, 1012, "SYSTEM_CAPACITY_EXHAUSTED", "busy", map[string]any{"retry_after_ms": 2500})

		var b2 struct {
			Retryable bool           `json:"retryable"`
			Meta      map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(w2.Body.Bytes(), &b2); err != nil {
			t.Fatal(err)
		}
		if !b2.Retryable || b2.Meta["retry_after_ms"] != float64(2500) {
			t.Fatalf("expected custom retry_after_ms=2500, got %+v", b2)
		}
	})

	t.Run("non-1012 error keeps retryable false", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		writeDomainCodedError(c, 4011, "QUERY_STATEMENT_REJECTED", "Query request rejected", nil)

		var body struct {
			ErrCode   int            `json:"err_code"`
			Retryable bool           `json:"retryable"`
			Meta      map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.ErrCode != 4011 || body.Retryable || body.Meta != nil {
			t.Fatalf("unexpected non-1012 response: %+v", body)
		}
	})
}

func TestWriteDomainError_QueueFullAndRateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"tasks.ErrQueueFull", tasks.ErrQueueFull},
		{"datasource.ErrCapacityExhausted", datasource.ErrCapacityExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v4/admin/datasources/1/test", nil)
			if !writeDomainError(c, tc.err, 0) {
				t.Fatal("expected writeDomainError to handle queue full")
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var body struct {
				ErrCode   int            `json:"err_code"`
				Retryable bool           `json:"retryable"`
				Meta      map[string]any `json:"meta"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ErrCode != 1012 || !body.Retryable || body.Meta["retry_after_ms"] != float64(1000) {
				t.Fatalf("unexpected 1012 response: %+v", body)
			}
		})
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"identity.ErrRateLimited", identity.ErrRateLimited},
		{"identity.ErrPasswordHashBusy", identity.ErrPasswordHashBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", nil)
			if !writeDomainError(c, tc.err, 0) {
				t.Fatal("expected writeDomainError to handle rate limit")
			}
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", w.Code)
			}
			if got := w.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After header = %q, want 1", got)
			}
		})
	}

	t.Run("writeExecErr maps ErrSystemCapacity to 1012 retryable", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v4/change-orders/1/execution-attempts", nil)
		if !writeExecErr(c, execdomain.ErrSystemCapacity()) {
			t.Fatal("expected writeExecErr to handle ErrSystemCapacity")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var body struct {
			ErrCode   int            `json:"err_code"`
			Retryable bool           `json:"retryable"`
			Meta      map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.ErrCode != 1012 || !body.Retryable || body.Meta["retry_after_ms"] != float64(1000) {
			t.Fatalf("unexpected 1012 response: %+v", body)
		}
	})
}

type busyUserStubConnector struct{}

func (busyUserStubConnector) Connect(context.Context) (driver.Conn, error) {
	return busyUserStubConn{}, nil
}
func (busyUserStubConnector) Driver() driver.Driver { return nil }

type busyUserStubConn struct{}

func (busyUserStubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (busyUserStubConn) Close() error                        { return nil }
func (busyUserStubConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (busyUserStubConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, identity.ErrPasswordHashBusy
}

func TestHandleChangeMyPasswordBusyReturns429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sqlDB := sql.OpenDB(busyUserStubConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := identity.NewSessionStore(db, identity.DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := identity.NewService(db, sessions, nil, recorder)
	if err != nil {
		t.Fatal(err)
	}

	mem := session.NewMemoryStore()
	mem.Put(&session.Session{
		ID:        "tok-pw-busy",
		ExpiresAt: time.Now().Add(time.Hour),
		Identity:  &session.Identity{UserID: "u1", Username: "alice", Role: "user"},
	})

	r := gin.New()
	r.Use(session.Middleware(mem, session.Options{CookieName: "yearning_session"}))
	r.POST("/api/v4/users/me/password", (&B3Deps{Identity: svc}).HandleChangeMyPassword)

	body := bytes.NewBufferString(`{"old_password":"old-password-1","new_password":"new-strong-password-123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v4/users/me/password", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "tok-pw-busy"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

type listOrdersCaptureConnector struct {
	queries *[]string
	args    *[][]driver.NamedValue
}

func (c listOrdersCaptureConnector) Connect(context.Context) (driver.Conn, error) {
	return listOrdersCaptureConn(c), nil
}
func (listOrdersCaptureConnector) Driver() driver.Driver { return nil }

type listOrdersCaptureConn struct {
	queries *[]string
	args    *[][]driver.NamedValue
}

func (listOrdersCaptureConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (listOrdersCaptureConn) Close() error                        { return nil }
func (listOrdersCaptureConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c listOrdersCaptureConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	*c.queries = append(*c.queries, query)
	*c.args = append(*c.args, append([]driver.NamedValue(nil), args...))
	return &emptyRows{}, nil
}

type emptyRows struct{}

func (*emptyRows) Columns() []string         { return []string{"id"} }
func (*emptyRows) Close() error              { return nil }
func (*emptyRows) Next([]driver.Value) error { return io.EOF }

func TestHandleListOrdersFilterValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var capturedQueries []string
	var capturedArgs [][]driver.NamedValue
	sqlDB := sql.OpenDB(listOrdersCaptureConnector{queries: &capturedQueries, args: &capturedArgs})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}

	mem := session.NewMemoryStore()
	mem.Put(&session.Session{
		ID:        "tok-list-filter",
		ExpiresAt: time.Now().Add(time.Hour),
		Identity:  &session.Identity{UserID: "u-list-1", Username: "alice", Role: "user"},
	})

	r := gin.New()
	r.Use(session.Middleware(mem, session.Options{CookieName: "yearning_session"}))
	r.GET("/api/v4/change-orders", (&B9Deps{Orders: &changeapp.Services{DB: db}}).HandleListOrders)

	tooLong := strings.Repeat("a", 129)
	for _, tc := range []struct {
		name string
		qs   string
	}{
		{"invalid_state", "state=bogus_state"},
		{"q_too_long", "q=" + url.QueryEscape(tooLong)},
		{"datasource_too_long", "datasource=" + url.QueryEscape(tooLong)},
		{"bad_submitted_from", "submitted_from=2026/11/22"},
		{"bad_submitted_to", "submitted_to=not-a-date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v4/change-orders?"+tc.qs, nil)
			req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "tok-list-filter"})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			var env map[string]any
			if jerr := json.Unmarshal(w.Body.Bytes(), &env); jerr != nil {
				t.Fatalf("decode response: %v (body=%s)", jerr, w.Body.String())
			}
			if got := int(env["err_code"].(float64)); got != 1001 {
				t.Fatalf("%s: err_code = %d, want 1001 (body=%s)", tc.name, got, w.Body.String())
			}
		})
	}
	if len(capturedQueries) != 0 {
		t.Fatalf("invalid filters must fail before querying DB, got %d queries", len(capturedQueries))
	}

	validQS := "limit=20&state=stage_approval_active&q=" + url.QueryEscape("YR-20261122") +
		"&datasource=" + url.QueryEscape("f12-pg-target") +
		"&submitted_from=2026-11-21&submitted_to=2026-11-22"
	req := httptest.NewRequest(http.MethodGet, "/api/v4/change-orders?"+validQS, nil)
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "tok-list-filter"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid filters status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var okEnv map[string]any
	if jerr := json.Unmarshal(w.Body.Bytes(), &okEnv); jerr != nil {
		t.Fatalf("decode ok response: %v", jerr)
	}
	if int(okEnv["err_code"].(float64)) != 0 {
		t.Fatalf("valid filters err_code = %v, want 0 (body=%s)", okEnv["err_code"], w.Body.String())
	}
	if len(capturedQueries) != 1 {
		t.Fatalf("expected 1 DB query for empty valid filter result, got %d", len(capturedQueries))
	}
	lq := strings.ToLower(capturedQueries[0])
	if !strings.Contains(lq, "o.state =") || !strings.Contains(lq, "ilike") || !strings.Contains(lq, "datasource_name_snapshot") || !strings.Contains(lq, "o.submitted_at >=") || !strings.Contains(lq, "o.submitted_at <") {
		t.Fatalf("valid query missing filter predicates:\n%s", capturedQueries[0])
	}
}
