// ldapclient.go LDAP client: LDAPS/STARTTLS only, certificate chain and hostname verification,
// RFC 4515 filter escaping, and tiered connect/Bind/Search timeouts (01-auth-rbac-prd §6).
package identity

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// systemAndEnvRootCAs returns a certificate pool initialized from the OS trust store
// and augmented with any PEM certificates from SSL_CERT_FILE so darwin processes
// (whose x509.SystemCertPool ignores SSL_CERT_FILE) honor explicitly configured CA bundles.
func systemAndEnvRootCAs() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if certFile := strings.TrimSpace(os.Getenv("SSL_CERT_FILE")); certFile != "" {
		if pemBytes, err := os.ReadFile(certFile); err == nil {
			_ = pool.AppendCertsFromPEM(pemBytes)
		}
	}
	return pool
}

// LDAPConfiguration is the strongly typed view of LDAP settings in identity_providers.configuration.
type LDAPConfiguration struct {
	Host                 string  `json:"host"`
	Port                 int     `json:"port"`
	Transport            string  `json:"transport"` // ldaps | starttls
	ServerName           string  `json:"server_name"`
	BindDN               string  `json:"bind_dn"`
	BaseDN               string  `json:"base_dn"`
	UserFilter           string  `json:"user_filter"`
	UsernameAttribute    string  `json:"username_attribute"`
	DisplayNameAttribute string  `json:"display_name_attribute"`
	EmailAttribute       *string `json:"email_attribute"`
	ConnectTimeoutMS     int     `json:"connect_timeout_ms"`
	BindTimeoutMS        int     `json:"bind_timeout_ms"`
	SearchTimeoutMS      int     `json:"search_timeout_ms"`
}

// ErrLDAPConfig indicates an invalid LDAP configuration; error messages never contain secrets.
var ErrLDAPConfig = errors.New("identity: invalid ldap configuration")

// Validate enforces the LDAP configuration contract: transport must be ldaps or starttls,
// filter must contain exactly one {username} placeholder, and timeouts must be within 100..30000ms.
func (c *LDAPConfiguration) Validate() error {
	if c == nil || c.Host == "" || c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%w: host or port", ErrLDAPConfig)
	}
	if c.Transport != "ldaps" && c.Transport != "starttls" {
		return fmt.Errorf("%w: transport must be ldaps or starttls", ErrLDAPConfig)
	}
	if c.ServerName == "" || c.BaseDN == "" || c.UsernameAttribute == "" || c.DisplayNameAttribute == "" {
		return fmt.Errorf("%w: missing server_name/base_dn/attributes", ErrLDAPConfig)
	}
	if strings.Count(c.UserFilter, "{username}") != 1 {
		return fmt.Errorf("%w: user_filter must contain exactly one {username}", ErrLDAPConfig)
	}
	for _, ms := range []int{c.ConnectTimeoutMS, c.BindTimeoutMS, c.SearchTimeoutMS} {
		if ms < 100 || ms > 30000 {
			return fmt.Errorf("%w: timeout out of range", ErrLDAPConfig)
		}
	}
	return nil
}

// LDAPSearchResult holds user attributes returned by a successful LDAP Search.
type LDAPSearchResult struct {
	EntryDN     string
	Username    string
	DisplayName string
	Email       string
}

// ldapDial establishes an LDAP connection with mandatory TLS. SEC-0009: outbound dialing goes
// through addrguard (LDAP host is an admin-configurable target with the same posture as datasources
// and AI providers) — the Control callback validates the resolved IP before connect, rejecting
// loopback/link-local/cloud metadata/CGNAT/ULA/unspecified/broadcast/multicast/Unix sockets and
// defending against DNS rebinding; guard rejections converge to "ldap connect failed" at the caller.
func ldapDial(c *LDAPConfiguration) (*ldap.Conn, error) {
	tlsCfg := &tls.Config{
		ServerName: c.ServerName,
		MinVersion: tls.VersionTLS12,
		RootCAs:    systemAndEnvRootCAs(),
	}
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	dialer := addrguard.GuardedDialer(time.Duration(c.ConnectTimeoutMS) * time.Millisecond)
	switch c.Transport {
	case "ldaps":
		return ldap.DialURL("ldaps://"+addr, ldap.DialWithTLSDialer(tlsCfg, dialer))
	case "starttls":
		conn, err := ldap.DialURL("ldap://"+addr, ldap.DialWithDialer(dialer))
		if err != nil {
			return nil, err
		}
		if err := conn.StartTLS(tlsCfg); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	default:
		return nil, fmt.Errorf("%w: transport must be ldaps or starttls", ErrLDAPConfig)
	}
}

// LDAPAuthenticate executes: service Bind -> user Search (with escaped filter) -> user Bind.
// Any step failure returns an error without leaking passwords or raw filter text.
func LDAPAuthenticate(c *LDAPConfiguration, bindPassword, username, password string) (*LDAPSearchResult, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if username == "" || password == "" {
		return nil, errors.New("identity: empty ldap credentials")
	}
	conn, err := ldapDial(c)
	if err != nil {
		return nil, fmt.Errorf("identity: ldap connect failed")
	}
	defer func() { _ = conn.Close() }()

	conn.SetTimeout(time.Duration(c.BindTimeoutMS) * time.Millisecond)
	if c.BindDN != "" {
		if err := conn.Bind(c.BindDN, bindPassword); err != nil {
			return nil, fmt.Errorf("identity: ldap service bind failed")
		}
	}

	conn.SetTimeout(time.Duration(c.SearchTimeoutMS) * time.Millisecond)
	filter := strings.Replace(c.UserFilter, "{username}", ldap.EscapeFilter(username), 1)
	req := ldap.NewSearchRequest(
		c.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 0, false,
		filter,
		[]string{c.UsernameAttribute, c.DisplayNameAttribute, deref(c.EmailAttribute)},
		nil,
	)
	res, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("identity: ldap search failed")
	}
	if len(res.Entries) != 1 {
		return nil, errors.New("identity: ldap user not found or ambiguous")
	}
	entry := res.Entries[0]
	found := entry.GetAttributeValue(c.UsernameAttribute)
	if found == "" {
		return nil, errors.New("identity: ldap username attribute missing")
	}

	conn.SetTimeout(time.Duration(c.BindTimeoutMS) * time.Millisecond)
	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, errors.New("identity: ldap authentication failed")
	}
	return &LDAPSearchResult{
		EntryDN:     entry.DN,
		Username:    found,
		DisplayName: entry.GetAttributeValue(c.DisplayNameAttribute),
		Email:       entry.GetAttributeValue(deref(c.EmailAttribute)),
	}, nil
}

// LDAPConnectionTest runs a connectivity check using the stored secret: connect + service Bind + base Search.
// Performs no user Bind and returns no credentials.
func LDAPConnectionTest(c *LDAPConfiguration, bindPassword string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	conn, err := ldapDial(c)
	if err != nil {
		return fmt.Errorf("identity: ldap connect failed")
	}
	defer func() { _ = conn.Close() }()
	conn.SetTimeout(time.Duration(c.BindTimeoutMS) * time.Millisecond)
	if c.BindDN != "" {
		if err := conn.Bind(c.BindDN, bindPassword); err != nil {
			return fmt.Errorf("identity: ldap service bind failed")
		}
	}
	conn.SetTimeout(time.Duration(c.SearchTimeoutMS) * time.Millisecond)
	req := ldap.NewSearchRequest(
		c.BaseDN, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, 0, false,
		"(objectClass=*)", []string{"1.1"}, nil,
	)
	if _, err := conn.Search(req); err != nil {
		return fmt.Errorf("identity: ldap base search failed")
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
