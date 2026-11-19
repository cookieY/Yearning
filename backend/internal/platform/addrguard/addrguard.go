// Package addrguard implements the application-layer SSRF/address guard (security-threat-model T05).
// It uniformly validates configurable outbound target addresses (datasources, AI Providers, OIDC, LDAP, notifications):
// resolves hostnames and validates every IP, rejecting loopback, link-local (including 169.254.169.254 cloud
// metadata), CGNAT shared space 100.64/10 and ULA fc00::/7 (which host Alibaba Cloud/AWS/GCP
// instance metadata), unspecified, broadcast, multicast, and Unix domain sockets; private network
// access is narrowed to RFC1918 unicast and public unicast is allowed -- normal access to internal
// databases is not considered SSRF (see T05 control semantics).
//
// Two lines of defense:
//   - At configuration/probe time: ValidateHost resolves and checks all addresses (DNS failures are not
//     rejected, falling back to the dialer layer to preserve "create datasource before testing connectivity" semantics);
//   - At dial time: GuardedDialer validates the **actual connected IP** via net.Dialer.Control
//     (DNS rebinding defense: validation happens after resolution and before connect, so a valid address
//     at configuration time cannot be rebound to a forbidden address later).
//
// Explicit development bypass (for local dev / CI integration tests only, where target DBs and Provider stubs
// run on loopback; production manifests must never set this): when process environment variable
// YEARNING_ALLOW_LOOPBACK_TARGETS=1|true is set, only loopback targets (127/8 and ::1) are allowed;
// link-local, cloud metadata (169.254.169.254), unspecified, broadcast, multicast, and Unix domain sockets
// remain rejected in all states -- the bypass never permits metadata endpoints.
//
// Unit tests in this package avoid real DNS queries by injecting a fake Resolver.
package addrguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Rejection reasons (sentinel errors; exclude target addresses since error text surfaces in diagnostics).
var (
	// ErrAddressForbidden is the unified rejection sentinel error (checked via errors.Is).
	ErrAddressForbidden = errors.New("addrguard: target address forbidden")
	// ErrUnixSocket indicates a Unix domain socket target was rejected.
	ErrUnixSocket = errors.New("addrguard: unix socket target rejected")
)

// LoopbackBypassEnv is the environment variable name for the explicit development loopback bypass.
//
// For local development / CI integration tests only: the f12 stack target PG (127.0.0.1), AI Provider
// stub (https://127.0.0.1), adapter integration suite, and itest PG (localhost) all run on loopback;
// when set to "1" or "true" (case-insensitive, whitespace-tolerant), loopback targets are permitted
// within the process. Production deployment manifests must never set this -- the bypass only allows loopback,
// while link-local, cloud metadata, CGNAT (100.64/10), ULA (fc00::/7), unspecified, broadcast, multicast,
// and Unix domain sockets remain rejected.
const LoopbackBypassEnv = "YEARNING_ALLOW_LOOPBACK_TARGETS"

// Bypass state: the environment variable is parsed once on first use (sync.Once) with zero-overhead
// reads afterward; no runtime toggle is exposed (changing the environment requires a process restart).
// The test override pointer is written only by the test hook (always nil in production) and takes precedence over the env-derived value.
var (
	loopbackBypassOnce sync.Once
	loopbackBypassEnv  bool
	bypassTestOverride atomic.Pointer[bool]
)

// loopbackBypassEnabled reports whether the explicit development loopback bypass is active.
func loopbackBypassEnabled() bool {
	if o := bypassTestOverride.Load(); o != nil {
		return *o
	}
	loopbackBypassOnce.Do(func() {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(LoopbackBypassEnv)))
		loopbackBypassEnv = v == "1" || v == "true"
	})
	return loopbackBypassEnv
}

// SetLoopbackBypassForTest is a test hook that deterministically sets or restores the loopback bypass state
// (cross-package tests cannot use t.Setenv to alter a sync.Once result already frozen on first read;
// the override takes precedence over the env-derived value and is not overwritten by once initialization).
// For testing only and must never be called in production code; returns a restore function (restoring the previous override, or nil to clear).
func SetLoopbackBypassForTest(enabled bool) (restore func()) {
	prev := bypassTestOverride.Load()
	v := enabled
	bypassTestOverride.Store(&v)
	return func() { bypassTestOverride.Store(prev) }
}

// forbiddenIP describes the category of a forbidden IP address (wrapped into ErrAddressForbidden).
type forbiddenIP struct{ reason string }

func (e forbiddenIP) Error() string { return "addrguard: " + e.reason }
func (e forbiddenIP) Unwrap() error { return ErrAddressForbidden }

// Resolver resolves a hostname into a list of IPs; injectable so unit tests can use a fake resolver
// without issuing real DNS queries. Returning an error indicates resolution is unavailable (callers decide whether to fall back).
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

// DefaultResolver is the production resolver (system resolver returning both single-stack and dual-stack addresses).
func DefaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// Cloud metadata hosting ranges (SEC-0009): CGNAT shared space and IPv6 ULA are not merely "internal networks";
// cloud providers host instance metadata services inside them -- Alibaba Cloud 100.100.100.200,
// AWS IMDSv6 fd00:ec2::254, GCP fd20:ce::254. Allowing them would give admin-configurable targets
// direct read access to cloud credentials, so they are rejected on par with link-local metadata.
var (
	cgnatRange = mustCIDR("100.64.0.0/10") // Includes 100.100.100.200 (Alibaba Cloud metadata)
	ulaRange   = mustCIDR("fc00::/7")      // Includes fd00:ec2::254 and fd20:ce::254
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("addrguard: bad builtin cidr " + s)
	}
	return n
}

// ValidateIP checks whether a single IP is permitted as an application-configured connection target
// (table-driven rejection: loopback, link-local, metadata, unspecified, broadcast, multicast,
// CGNAT shared space, and ULA). Loopback is permitted when the explicit development bypass (LoopbackBypassEnv)
// is active, while all other categories are rejected unconditionally -- the bypass never extends to
// metadata/CGNAT/ULA ranges; private network access is restricted to RFC1918 unicast.
func ValidateIP(ip net.IP) error {
	if ip == nil {
		return forbiddenIP{"unparsable address"}
	}
	if ip.IsLoopback() && !loopbackBypassEnabled() {
		return forbiddenIP{"loopback address rejected"}
	}
	if ip.IsLinkLocalUnicast() {
		// IPv4 169.254.0.0/16 (including 169.254.169.254 cloud metadata) and IPv6 fe80::/10.
		return forbiddenIP{"link-local address rejected"}
	}
	// Contains also works for IPv4-mapped IPv6 forms (::ffff:100.100.100.200):
	// Contains normalizes via To4 internally, while pure IPv6 prefixes (fc00::/7) do not match IPv4 byte lengths.
	if cgnatRange.Contains(ip) {
		return forbiddenIP{"cgnat shared address rejected"}
	}
	if ulaRange.Contains(ip) {
		return forbiddenIP{"unique local address rejected"}
	}
	if ip.IsLinkLocalMulticast() {
		return forbiddenIP{"link-local multicast address rejected"}
	}
	if ip.IsUnspecified() {
		return forbiddenIP{"unspecified address rejected"}
	}
	if ip.IsMulticast() {
		return forbiddenIP{"multicast address rejected"}
	}
	if ip.Equal(net.IPv4bcast) {
		return forbiddenIP{"broadcast address rejected"}
	}
	return nil
}

// ValidateHostForm validates the literal form of a host (without DNS resolution): Unix socket paths
// (absolute paths per pgx/mysql driver conventions) and empty hosts are rejected immediately; IP literals are validated inline.
func ValidateHostForm(host string) error {
	if host == "" {
		return forbiddenIP{"empty host"}
	}
	if host[0] == '/' {
		return ErrUnixSocket
	}
	if ip := net.ParseIP(host); ip != nil {
		return ValidateIP(ip)
	}
	return nil
}

// ValidateHost validates a host at configuration/probe time: resolves the host and checks **all** resolved
// addresses (rejecting if any address is forbidden, fail closed). Uses DefaultResolver when resolver is nil.
// DNS resolution failures are not treated as invalid configuration (GuardedDialer at dial time is the authoritative
// defense, preserving the ability to create a datasource before it becomes reachable).
func ValidateHost(ctx context.Context, host string, resolver Resolver) error {
	if err := ValidateHostForm(host); err != nil {
		return err
	}
	if net.ParseIP(host) != nil {
		return nil // IP literals were already validated in ValidateHostForm
	}
	if resolver == nil {
		resolver = DefaultResolver
	}
	ips, err := resolver(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil // Resolution unavailable: defer to the dial-time guard
	}
	for _, ip := range ips {
		if verr := ValidateIP(ip); verr != nil {
			return verr
		}
	}
	return nil
}

// controlGuard is the net.Dialer.Control callback: validates the resolved address before connect.
// Unix family networks are rejected immediately; host in host:port must be a parseable IP literal
// (resolution is already complete at the Control stage), aborting the dial on any invalid address (fail closed).
func controlGuard(network, address string, _ syscall.RawConn) error {
	if network == "unix" || network == "unixgram" || network == "unixpacket" {
		return ErrUnixSocket
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return forbiddenIP{"dial address unparsable"}
	}
	// Strip IPv6 zone identifier (e.g. fe80::1%eth0 -- addresses with a zone are link-local and will be rejected).
	for i := 0; i < len(host); i++ {
		if host[i] == '%' {
			host = host[:i]
			break
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return forbiddenIP{"dial address is not an IP literal"}
	}
	return ValidateIP(ip)
}

// GuardedDialer returns a net.Dialer protected by the address guard (Timeout bounds both DNS and connect).
// Control validates the actual resolved IP after resolution and before connect, defending against DNS rebinding.
func GuardedDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: controlGuard}
}

// DialFunc returns a dial function compatible with pgx and go-sql-driver
// (func(ctx, network, addr) (net.Conn, error)).
func DialFunc(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := GuardedDialer(timeout)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network == "unix" || network == "unixgram" || network == "unixpacket" {
			return nil, ErrUnixSocket
		}
		// Reject host values that directly specify a Unix socket path (defense-in-depth: drivers may invoke
		// the dialer with network="tcp" and addr="/var/run/x.sock").
		if len(addr) > 0 && addr[0] == '/' {
			return nil, ErrUnixSocket
		}
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// CheckEndpoint performs fast endpoint validation on configuration write paths: host form + port range +
// immediate IP literal check; DNS resolution is handled separately by ValidateHost. Returns an error on invalid port.
func CheckEndpoint(host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: port out of range", ErrAddressForbidden)
	}
	return ValidateHostForm(host)
}
