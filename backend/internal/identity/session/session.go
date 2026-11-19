// Package session implements the server-side Session middleware and storage interface.
//
// v4 contract (api-conventions §8): browsers use server-side PostgreSQL Sessions with
// HttpOnly; Secure; SameSite=Lax cookies. B1 establishes the middleware and Store interface;
// the real PG Store, login issuance, and revocation are implemented in B3. This package never parses JWTs or writes to localStorage.
package session

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ErrNotFound indicates the Session does not exist or has expired. Callers must not distinguish 'logged out' from 'forged'.
var ErrNotFound = errors.New("session not found")

// Identity represents the authenticated identity attached to a request.
// IsBuiltinAdmin comes from users.is_builtin_admin and Role comes from users.role ('user' | 'admin');
// HasAdminAccess() grants access only to the management surface and implies no business approval, execution, or query privileges (authorization-policy).
type Identity struct {
	UserID         string
	Username       string
	IsBuiltinAdmin bool
	Role           string
}

// HasAdminAccess reports whether the identity is eligible for management-surface operations (built-in admin or role='admin').
func (id *Identity) HasAdminAccess() bool {
	if id == nil {
		return false
	}
	return id.IsBuiltinAdmin || id.Role == "admin"
}

// Session represents a single server-side Session record.
type Session struct {
	ID        string
	Identity  *Identity
	ExpiresAt time.Time
}

// Store abstracts Session persistence. B1 provides an in-memory implementation for development and tests;
// the production implementation (PostgreSQL) is provided by B3 and supports per-user revocation.
type Store interface {
	Get(ctx context.Context, id string) (*Session, error)
}

// Options configures the Session middleware behavior.
type Options struct {
	CookieName string
	Secure     bool
	// AnonymousAllowed lets requests with missing or unmatched cookies proceed as anonymous when true (B1 skeleton behavior);
	// protected business routes enforce mandatory authentication when wired in B3.
	AnonymousAllowed bool
}

type identityKey struct{}

// Middleware reads the Session from the cookie and injects the Identity into the request context.
// Missing cookies or Store misses are treated uniformly as anonymous without exposing differences to the client.
func Middleware(store Store, opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(opts.CookieName)
		if err != nil || raw == "" {
			continueAs(c, opts, nil)
			return
		}
		sess, err := store.Get(c.Request.Context(), raw)
		if err != nil || sess == nil || sess.Identity == nil {
			continueAs(c, opts, nil)
			return
		}
		continueAs(c, opts, sess.Identity)
	}
}

func continueAs(c *gin.Context, opts Options, id *Identity) {
	if id == nil && !opts.AnonymousAllowed {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if id != nil {
		ctx := context.WithValue(c.Request.Context(), identityKey{}, id)
		c.Request = c.Request.WithContext(ctx)
	}
	c.Next()
}

// FromContext returns the current Identity from ctx, or nil for anonymous requests.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	return id
}

// IssueCookie writes the session cookie: HttpOnly + Secure + SameSite=Lax + Path=/.
// MaxAge of 0 issues a browser-session cookie.
func IssueCookie(c *gin.Context, name, value string, secure bool, maxAgeSeconds int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAgeSeconds,
	})
}

// MemoryStore is an in-memory Store for development and testing; not used in production.
// Protected by an RWMutex because concurrent gin requests read and write it concurrently.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[string]*Session{}}
}

func (m *MemoryStore) Get(ctx context.Context, id string) (*Session, error) {
	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if !sess.ExpiresAt.IsZero() && sess.ExpiresAt.Before(time.Now()) {
		return nil, ErrNotFound
	}
	return sess, nil
}

// Put writes a session for tests and mock mode only.
func (m *MemoryStore) Put(sess *Session) {
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
}
