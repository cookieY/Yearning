// oidc.go OIDC login: Issuer Discovery, PKCE, one-time state/nonce,
// signature verification, and Audience verification (ADR-0004; tokens never appear in URLs).
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// OIDCConfiguration is the strongly typed view of OIDC settings in identity_providers.configuration.
type OIDCConfiguration struct {
	IssuerURL        string   `json:"issuer_url"`
	ClientID         string   `json:"client_id"`
	Scopes           []string `json:"scopes"`
	UsernameClaim    string   `json:"username_claim"`
	DisplayNameClaim *string  `json:"display_name_claim"`
	EmailClaim       *string  `json:"email_claim"`
	ConnectTimeoutMS int      `json:"connect_timeout_ms"`
	RequestTimeoutMS int      `json:"request_timeout_ms"`
}

// ErrOIDCConfig indicates an invalid OIDC configuration; error messages never contain the Client Secret.
var ErrOIDCConfig = errors.New("identity: invalid oidc configuration")

// Validate enforces OIDC configuration rules: HTTPS Issuer, openid scope, required claims, and timeout bounds.
func (c *OIDCConfiguration) Validate() error {
	if c == nil {
		return ErrOIDCConfig
	}
	u, err := url.Parse(c.IssuerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: issuer must be https", ErrOIDCConfig)
	}
	if c.ClientID == "" || c.UsernameClaim == "" {
		return fmt.Errorf("%w: client_id and username_claim required", ErrOIDCConfig)
	}
	hasOpenID := false
	for _, s := range c.Scopes {
		if s == "openid" {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID || len(c.Scopes) > 20 {
		return fmt.Errorf("%w: scopes must contain openid (max 20)", ErrOIDCConfig)
	}
	for _, ms := range []int{c.ConnectTimeoutMS, c.RequestTimeoutMS} {
		if ms < 100 || ms > 30000 {
			return fmt.Errorf("%w: timeout out of range", ErrOIDCConfig)
		}
	}
	return nil
}

// oidcHTTPClient constructs an HTTP client with connect and request timeouts:
// DialContext enforces the connect timeout and dials through addrguard (SEC-0009/T05: rejecting
// loopback/link-local/cloud metadata outbound targets and defending against DNS rebinding);
// Client.Timeout bounds the total request duration.
func (c *OIDCConfiguration) oidcHTTPClient() *http.Client {
	return c.oidcClientWithRedirect(addrguard.RedirectPolicy(3, true))
}

func (c *OIDCConfiguration) oidcTokenHTTPClient() *http.Client {
	return c.oidcClientWithRedirect(addrguard.RedirectPolicy(3, false))
}

func (c *OIDCConfiguration) oidcClientWithRedirect(checkRedirect func(*http.Request, []*http.Request) error) *http.Client {
	return &http.Client{
		Timeout:       time.Duration(c.RequestTimeoutMS) * time.Millisecond,
		CheckRedirect: checkRedirect,
		Transport: &http.Transport{
			DialContext: addrguard.DialFuncFor(addrguard.PurposeOIDC, time.Duration(c.ConnectTimeoutMS)*time.Millisecond),
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    systemAndEnvRootCAs(),
			},
		},
	}
}

// fetchOIDCProvider performs OIDC discovery (test injection point: unit tests count invocations
// with a fake discovery function without making real HTTP calls). The production path matches
// existing behavior: the HTTP client dials through addrguard.
var fetchOIDCProvider = func(ctx context.Context, client *http.Client, issuer string) (*oidc.Provider, error) {
	return oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
}

// ---- In-process TTL cache for OIDC Discovery (SEC-0006) ----
//
// Previously, start/callback/connectivity tests performed live IdP discovery on every call —
// allowing unauthenticated callers hitting start at high frequency to amplify outbound traffic to the IdP.
// The cache stores provider metadata (authorization/token/JWKS endpoints) keyed by issuer and reuses it within TTL:
//   - Issuers come from admin configuration rather than request input, so the keyspace is naturally bounded;
//     the 64-entry cap is a defensive backstop;
//   - Negative results (failures) are not cached: transient IdP failures retry on the next request;
//   - Admin config changes (client_id/scopes) reuse cached endpoint metadata for up to 5 minutes while
//     the verifier and oauth.Config are always constructed from the latest cfg — bounded staleness within an accepted window.
const (
	oidcDiscoveryCacheTTL      = 5 * time.Minute
	oidcDiscoveryCacheMaxEntry = 64
)

type oidcDiscoveryEntry struct {
	provider  *oidc.Provider
	fetchedAt time.Time
}

var oidcDiscoveryCache = struct {
	mu      sync.Mutex
	entries map[string]oidcDiscoveryEntry
}{entries: map[string]oidcDiscoveryEntry{}}

// nowFunc is an injectable clock (used to test TTL expiration paths).
var nowFunc = time.Now

// cachedOIDCProvider returns the cached provider on hit and opportunistically evicts expired entries.
func cachedOIDCProvider(issuer string) (*oidc.Provider, bool) {
	oidcDiscoveryCache.mu.Lock()
	defer oidcDiscoveryCache.mu.Unlock()
	e, ok := oidcDiscoveryCache.entries[issuer]
	if !ok {
		return nil, false
	}
	if nowFunc().Sub(e.fetchedAt) >= oidcDiscoveryCacheTTL {
		delete(oidcDiscoveryCache.entries, issuer)
		return nil, false
	}
	return e.provider, true
}

// storeOIDCProvider writes to the cache; when at capacity, it purges expired entries first and
// evicts the oldest entry if still full (rare in normal deployments where issuer count is well below the cap).
func storeOIDCProvider(issuer string, p *oidc.Provider) {
	oidcDiscoveryCache.mu.Lock()
	defer oidcDiscoveryCache.mu.Unlock()
	if _, exists := oidcDiscoveryCache.entries[issuer]; !exists &&
		len(oidcDiscoveryCache.entries) >= oidcDiscoveryCacheMaxEntry {
		now := nowFunc()
		for k, e := range oidcDiscoveryCache.entries {
			if now.Sub(e.fetchedAt) >= oidcDiscoveryCacheTTL {
				delete(oidcDiscoveryCache.entries, k)
			}
		}
		if len(oidcDiscoveryCache.entries) >= oidcDiscoveryCacheMaxEntry {
			var oldestKey string
			var oldestAt time.Time
			first := true
			for k, e := range oidcDiscoveryCache.entries {
				if first || e.fetchedAt.Before(oldestAt) {
					oldestKey, oldestAt, first = k, e.fetchedAt, false
				}
			}
			delete(oidcDiscoveryCache.entries, oldestKey)
		}
	}
	oidcDiscoveryCache.entries[issuer] = oidcDiscoveryEntry{provider: p, fetchedAt: nowFunc()}
}

// OIDCProviderClient wraps OIDC Discovery and the login flow.
type OIDCProviderClient struct {
	provider *oidc.Provider
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	cfg      *OIDCConfiguration
}

// NewOIDCProviderClient runs Discovery and prepares the token verifier (reusing cached issuer
// metadata within TTL; see oidcDiscoveryCache). clientSecret is kept only in memory for token
// exchange and never enters logs or error messages.
func NewOIDCProviderClient(ctx context.Context, cfg *OIDCConfiguration, clientSecret string) (*OIDCProviderClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("%w: client secret required", ErrOIDCConfig)
	}
	provider, cached := cachedOIDCProvider(cfg.IssuerURL)
	if !cached {
		p, err := fetchOIDCProvider(ctx, cfg.oidcHTTPClient(), cfg.IssuerURL)
		if err != nil {
			// Do not cache negative results: transient IdP failures retry on the next request.
			return nil, fmt.Errorf("identity: oidc discovery failed")
		}
		provider = p
		storeOIDCProvider(cfg.IssuerURL, p)
	}
	return &OIDCProviderClient{
		provider: provider,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			Scopes:       cfg.Scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		cfg:      cfg,
	}, nil
}

// stateTTL bounds the lifetime of one-time state tokens; replay protection is backed by single-use IdP authorization codes.
const stateTTL = 5 * time.Minute

type oidcStatePayload struct {
	ProviderKey string `json:"pk"`
	Verifier    string `json:"v"`
	Nonce       string `json:"n"`
	ExpireAt    int64  `json:"exp"`
	RedirectURI string `json:"r"`
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// SignState generates a self-contained signed state token: base64url(payload).base64url(hmac).
func SignState(c *crypto.Crypter, payload oidcStatePayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac, err := c.HMAC(crypto.HMACPurposeOIDCState, raw)
	if err != nil {
		return "", err
	}
	return b64url(raw) + "." + b64url(mac), nil
}

// VerifyState verifies the signature and expiration of a state token and returns its payload.
func VerifyState(c *crypto.Crypter, state string) (*oidcStatePayload, error) {
	parts := strings.Split(state, ".")
	if len(parts) != 2 {
		return nil, errors.New("identity: oidc state malformed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("identity: oidc state malformed")
	}
	ok, err := c.VerifyHMAC(crypto.HMACPurposeOIDCState, raw, mustB64Decode(parts[1]))
	if err != nil || !ok {
		return nil, errors.New("identity: oidc state signature invalid")
	}
	var payload oidcStatePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("identity: oidc state malformed")
	}
	if time.Now().UTC().Unix() > payload.ExpireAt {
		return nil, errors.New("identity: oidc state expired")
	}
	return &payload, nil
}

func mustB64Decode(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(s)
	return b
}

// OIDCIdentity represents the external identity obtained after a successful callback authentication.
type OIDCIdentity struct {
	Subject     string
	Username    string
	DisplayName string
	Email       string
}

// StartLogin builds the authorization redirect URL (carrying state, nonce, and PKCE challenge).
// Also returns the raw state string so the caller can set the browser-binding cookie (SEC-0004:
// callback compares cookie state against query state to block login CSRF / callback URL forwarding).
func (p *OIDCProviderClient) StartLogin(c *crypto.Crypter, providerKey, redirectURI string) (authURL, state string, err error) {
	verifier, err := RandomToken()
	if err != nil {
		return "", "", err
	}
	nonce, err := RandomToken()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	state, err = SignState(c, oidcStatePayload{
		ProviderKey: providerKey,
		Verifier:    verifier,
		Nonce:       nonce,
		ExpireAt:    time.Now().UTC().Add(stateTTL).Unix(),
		RedirectURI: redirectURI,
	})
	if err != nil {
		return "", "", err
	}
	authURL = p.oauth.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", b64url(sum[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("redirect_uri", redirectURI),
	)
	return authURL, state, nil
}

// FinishLogin verifies state, exchanges the authorization code via PKCE, verifies the ID Token
// (signature/Issuer/Audience/Nonce), and extracts claims. The returned identity contains no tokens;
// tokens remain in memory and are never logged.
func (p *OIDCProviderClient) FinishLogin(ctx context.Context, c *crypto.Crypter, providerKey, code, state string) (*OIDCIdentity, error) {
	payload, err := VerifyState(c, state)
	if err != nil {
		return nil, err
	}
	if payload.ProviderKey != providerKey {
		return nil, errors.New("identity: oidc state provider mismatch")
	}
	baseCtx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.RequestTimeoutMS)*time.Millisecond)
	defer cancel()
	tokenCtx := oidc.ClientContext(baseCtx, p.cfg.oidcTokenHTTPClient())
	token, err := p.oauth.Exchange(tokenCtx, code,
		oauth2.SetAuthURLParam("code_verifier", payload.Verifier),
		oauth2.SetAuthURLParam("redirect_uri", payload.RedirectURI),
	)
	if err != nil {
		return nil, errors.New("identity: oidc code exchange failed")
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("identity: oidc id_token missing")
	}
	verifyCtx := oidc.ClientContext(baseCtx, p.cfg.oidcHTTPClient())
	idToken, err := p.verifier.Verify(verifyCtx, rawIDToken)
	if err != nil {
		return nil, errors.New("identity: oidc id_token verification failed")
	}
	if idToken.Nonce != payload.Nonce {
		return nil, errors.New("identity: oidc nonce mismatch")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, errors.New("identity: oidc claims malformed")
	}
	username := claimString(claims, p.cfg.UsernameClaim)
	if username == "" {
		return nil, errors.New("identity: oidc username claim missing")
	}
	return &OIDCIdentity{
		Subject:     idToken.Subject,
		Username:    username,
		DisplayName: claimString(claims, deref(p.cfg.DisplayNameClaim)),
		Email:       claimString(claims, deref(p.cfg.EmailClaim)),
	}, nil
}

func claimString(claims map[string]any, key string) string {
	if key == "" {
		return ""
	}
	v, ok := claims[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

// RandomStateKey exposes a random key generator for tests (contains no production secrets).
func RandomStateKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw), nil
}
