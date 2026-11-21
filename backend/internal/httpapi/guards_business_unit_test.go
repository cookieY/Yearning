package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

var (
	guardStubOnce sync.Once
)

type guardStubDriver struct{}

func (guardStubDriver) Open(_ string) (driver.Conn, error) { return &guardStubConn{}, nil }

type guardStubConn struct{}

func (*guardStubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (*guardStubConn) Close() error                        { return nil }
func (*guardStubConn) Begin() (driver.Tx, error)           { return &guardStubTx{}, nil }

type guardStubTx struct{}

func (*guardStubTx) Commit() error   { return nil }
func (*guardStubTx) Rollback() error { return nil }

func (*guardStubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "auth_sessions") {
		return &guardStubRows{
			cols: []string{"csrf_hash"},
			rows: [][]driver.Value{{identity.TokenHash("valid-csrf")}},
		}, nil
	}
	return &guardStubRows{cols: []string{}}, nil
}

type guardStubRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *guardStubRows) Columns() []string { return r.cols }
func (r *guardStubRows) Close() error      { return nil }
func (r *guardStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func newGuardTestSessionStore(t *testing.T) *identity.SessionStore {
	t.Helper()
	guardStubOnce.Do(func() {
		sql.Register("httpapi_guard_stub", guardStubDriver{})
	})
	sqlDB, err := sql.Open("httpapi_guard_stub", "")
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
	store, err := identity.NewSessionStore(gdb, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRequireBusinessOperationDefaultDenyAndCSRF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions := newGuardTestSessionStore(t)

	mount := func(id *session.Identity, op string, store *identity.SessionStore) *gin.Engine {
		r := gin.New()
		mem := session.NewMemoryStore()
		if id != nil {
			mem.Put(&session.Session{
				ID:        "sess-tok",
				Identity:  id,
				ExpiresAt: time.Now().Add(time.Hour),
			})
		}
		r.Use(session.Middleware(mem, session.Options{
			CookieName:       sessionCookieName,
			AnonymousAllowed: true,
		}))
		r.GET("/test", RequireBusinessOperation(op, store), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		r.POST("/test", RequireBusinessOperation(op, store), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		return r
	}

	user := &session.Identity{UserID: "u1", Username: "alice"}
	admin := &session.Identity{UserID: "u2", Username: "admin", IsBuiltinAdmin: true}

	t.Run("unauthenticated returns 401", func(t *testing.T) {
		r := mount(nil, "listChangeDrafts", sessions)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("unregistered operation returns 403 even for authenticated user and admin", func(t *testing.T) {
		for _, actor := range []*session.Identity{user, admin} {
			r := mount(actor, "unregisteredBusinessOp", sessions)
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-tok"})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("actor=%s status = %d, want 403", actor.Username, w.Code)
			}
		}
	})

	t.Run("registered GET allows authenticated user", func(t *testing.T) {
		r := mount(user, "listChangeDrafts", sessions)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-tok"})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	})

	t.Run("mutating registered op enforces CSRF", func(t *testing.T) {
		r := mount(user, "createChangeDraft", sessions)

		// Missing CSRF header -> 403
		reqMissing := httptest.NewRequest(http.MethodPost, "/test", nil)
		reqMissing.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-tok"})
		wMissing := httptest.NewRecorder()
		r.ServeHTTP(wMissing, reqMissing)
		if wMissing.Code != http.StatusForbidden {
			t.Fatalf("missing csrf status = %d, want 403", wMissing.Code)
		}

		// Valid CSRF header -> 200
		reqValid := httptest.NewRequest(http.MethodPost, "/test", nil)
		reqValid.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-tok"})
		reqValid.Header.Set(csrfHeader, "valid-csrf")
		wValid := httptest.NewRecorder()
		r.ServeHTTP(wValid, reqValid)
		if wValid.Code != http.StatusOK {
			t.Fatalf("valid csrf status = %d, want 200", wValid.Code)
		}

		// Nil SessionStore on mutating method -> fail closed 403
		rNilStore := mount(user, "createChangeDraft", nil)
		reqNil := httptest.NewRequest(http.MethodPost, "/test", nil)
		reqNil.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-tok"})
		wNilStore := httptest.NewRecorder()
		rNilStore.ServeHTTP(wNilStore, reqNil)
		if wNilStore.Code != http.StatusForbidden {
			t.Fatalf("nil session store status = %d, want 403", wNilStore.Code)
		}
	})
}
