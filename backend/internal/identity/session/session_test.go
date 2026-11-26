package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newRouter(store Store, opts Options) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Middleware(store, opts))
	r.GET("/whoami", func(c *gin.Context) {
		id := FromContext(c.Request.Context())
		if id == nil {
			c.JSON(http.StatusOK, gin.H{"anonymous": true})
			return
		}
		c.JSON(http.StatusOK, gin.H{"anonymous": false, "username": id.Username})
	})
	return r
}

func TestValidSessionInjectsIdentity(t *testing.T) {
	store := NewMemoryStore()
	store.Put(&Session{
		ID:        "sess-1",
		Identity:  &Identity{UserID: "u-1", Username: "admin"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	r := newRouter(store, Options{CookieName: "yearning_session", AnonymousAllowed: true})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "sess-1"})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !contains(w.Body.String(), `"username":"admin"`) {
		t.Fatalf("identity missing in response: %s", w.Body.String())
	}
}

func TestMissingCookieContinuesAnonymously(t *testing.T) {
	r := newRouter(NewMemoryStore(), Options{CookieName: "yearning_session", AnonymousAllowed: true})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"anonymous":true`) {
		t.Fatalf("anonymous request must pass when allowed: %d %s", w.Code, w.Body.String())
	}
}

func TestUnknownSessionIdIsAnonymous(t *testing.T) {
	r := newRouter(NewMemoryStore(), Options{CookieName: "yearning_session", AnonymousAllowed: true})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "forged"})
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"anonymous":true`) {
		t.Fatalf("forged session id must degrade to anonymous: %d %s", w.Code, w.Body.String())
	}
}

func TestExpiredSessionIsAnonymous(t *testing.T) {
	store := NewMemoryStore()
	store.Put(&Session{
		ID:        "sess-old",
		Identity:  &Identity{UserID: "u-1", Username: "admin"},
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	r := newRouter(store, Options{CookieName: "yearning_session", AnonymousAllowed: true})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "sess-old"})
	r.ServeHTTP(w, req)
	if !contains(w.Body.String(), `"anonymous":true`) {
		t.Fatalf("expired session must be anonymous: %s", w.Body.String())
	}
}

func TestProtectedModeRejectsAnonymous(t *testing.T) {
	r := newRouter(NewMemoryStore(), Options{CookieName: "yearning_session", AnonymousAllowed: false})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous under protected route must be 401, got %d", w.Code)
	}
}

func TestIssueCookieAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/login", func(c *gin.Context) {
		IssueCookie(c, "yearning_session", "sess-1", true, 0)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login", nil))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes must be HttpOnly+Secure+Lax: %+v", cookie)
	}
	if cookie.Path != "/" {
		t.Fatalf("cookie path = %s", cookie.Path)
	}
}

func TestMemoryStoreGetNotFound(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Regression test: MemoryStore is read and written concurrently under concurrent gin requests (Copilot PR#5 review).
// Verified race-free under go test -race.
func TestMemoryStoreConcurrentAccess(t *testing.T) {
	store := NewMemoryStore()
	store.Put(&Session{ID: "sess-c", Identity: &Identity{UserID: "u", Username: "admin"}})

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				store.Put(&Session{ID: fmt.Sprintf("sess-%d-%d", worker, i)})
				if _, err := store.Get(context.Background(), "sess-c"); err != nil {
					t.Errorf("get: %v", err)
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestWithIdentityAndViewAsRole(t *testing.T) {
	id := &Identity{UserID: "u-2", Username: "alice", IsBuiltinAdmin: false, Role: "user", ViewAsRole: "approver"}
	ctx := WithIdentity(context.Background(), id)
	got := FromContext(ctx)
	if got == nil || got.UserID != "u-2" || got.ViewAsRole != "approver" || got.HasAdminAccess() {
		t.Fatalf("unexpected identity from context: %+v", got)
	}
	if FromContext(WithIdentity(context.Background(), nil)) != nil {
		t.Fatal("expected nil identity for nil WithIdentity")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
