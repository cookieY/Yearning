// Package addrguard implements the application-layer SSRF/address guard (security-threat-model T05).
//
// In Yearning's intranet-first deployment posture, RFC1918 private networks, CGNAT (100.64/10), and ULA
// (fc00::/7) are normal internal target networks while network-segment boundaries belong to firewalls
// and NetworkPolicies. Application-layer addrguard enforces the controls network policy cannot see:
//   - Hard-deny cloud metadata endpoints (169.254/16, fe80::/10, 100.100.100.200, fd00:ec2::254, fd20:ce::254),
//     loopback (with an optional deployment toggle restricted strictly to datasource/OSC purposes),
//     reserved/deprecated ranges (0.0.0.0/8, 192.0.0.0/24, 198.18.0.0/15, 240.0.0.0/4, ::/96, 100::/64,
//     fec0::/10, 2002::/16, 2001::/32, 64:ff9b:1::/48), unspecified, broadcast, multicast, Unix sockets,
//     and NAT64 (64:ff9b::/96) recursively evaluated on the embedded IPv4 address;
//   - SelfGuard: denies Yearning's own bound listener port, Kubernetes API / DNS nameservers, and the
//     metadata database (allowing same-instance PG business databases for datasource purpose when targeting
//     a database other than the metadata DB);
//   - Two-line defense: ValidateHost*/ValidateEndpoint* at configuration/probe time (deferring transient
//     DNS failure to dial time so datasources can be created before targets are reachable) and Control
//     hook validation on the actual connected IP before connect (DNS rebinding defense).
package addrguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Purpose identifies the outbound connection category for purpose-scoped SSRF policy checks.
type Purpose string

const (
	PurposeGeneric     Purpose = "generic"
	PurposeDatasource  Purpose = "datasource"
	PurposeOSC         Purpose = "osc"
	PurposeAIProvider  Purpose = "ai_provider"
	PurposeOIDC        Purpose = "oidc"
	PurposeLDAP        Purpose = "ldap"
	PurposeWebhook     Purpose = "webhook"
	PurposeSMTP        Purpose = "smtp"
	PurposeReleaseFeed Purpose = "release_feed"
	PurposeRetentionDB Purpose = "retention_db"
)

// Rejection reasons (sentinel errors; exclude target addresses since error text surfaces in diagnostics).
var (
	// ErrAddressForbidden is the unified rejection sentinel error (checked via errors.Is).
	ErrAddressForbidden = errors.New("addrguard: target address forbidden")
	// ErrUnixSocket indicates a Unix domain socket target was rejected.
	ErrUnixSocket = errors.New("addrguard: unix socket target rejected")

	// Category-specific sentinels wrapping ErrAddressForbidden so callers and UI diagnostics can explain why.
	ErrCloudMetadataForbidden       = fmt.Errorf("%w: cloud metadata or link-local address", ErrAddressForbidden)
	ErrLoopbackForbidden            = fmt.Errorf("%w: loopback address", ErrAddressForbidden)
	ErrReservedAddressForbidden     = fmt.Errorf("%w: reserved or non-unicast address", ErrAddressForbidden)
	ErrSelfTargetForbidden          = fmt.Errorf("%w: yearning self listener", ErrAddressForbidden)
	ErrMetadataDatabaseForbidden    = fmt.Errorf("%w: yearning metadata database", ErrAddressForbidden)
	ErrClusterControlPlaneForbidden = fmt.Errorf("%w: cluster infrastructure endpoint", ErrAddressForbidden)
)

// LoopbackBypassEnv is the environment variable name for the explicit development loopback bypass.
//
// For local development / CI integration tests only: production config validation rejects this env var.
const LoopbackBypassEnv = "YEARNING_ALLOW_LOOPBACK_TARGETS"

var (
	loopbackBypassOnce       sync.Once
	loopbackBypassEnv        bool
	bypassTestOverride       atomic.Pointer[bool]
	allowLoopbackDatasource  atomic.Bool
	allowLoopbackDSOverride  atomic.Pointer[bool]
	dialerResolverForTestPtr atomic.Pointer[net.Resolver]
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

// SetLoopbackBypassForTest deterministically sets or restores the dev loopback bypass state in tests.
func SetLoopbackBypassForTest(enabled bool) (restore func()) {
	prev := bypassTestOverride.Load()
	v := enabled
	bypassTestOverride.Store(&v)
	return func() { bypassTestOverride.Store(prev) }
}

// SetAllowLoopbackDatasource configures whether loopback targets are permitted strictly for
// PurposeDatasource and PurposeOSC (e.g., single-host deployments where target MySQL/PG shares the host).
// SelfGuard still rejects Yearning's own listener port and metadata database even when enabled.
func SetAllowLoopbackDatasource(enabled bool) {
	allowLoopbackDatasource.Store(enabled)
}

func allowLoopbackDatasourceEnabled() bool {
	if o := allowLoopbackDSOverride.Load(); o != nil {
		return *o
	}
	return allowLoopbackDatasource.Load()
}

// SetAllowLoopbackDatasourceForTest deterministically overrides allow_loopback_datasource in tests.
func SetAllowLoopbackDatasourceForTest(enabled bool) (restore func()) {
	prev := allowLoopbackDSOverride.Load()
	v := enabled
	allowLoopbackDSOverride.Store(&v)
	return func() { allowLoopbackDSOverride.Store(prev) }
}

// SetDialerResolverForTest injects a custom *net.Resolver used by GuardedDialer* in tests (e.g. DNS rebinding stubs).
func SetDialerResolverForTest(r *net.Resolver) (restore func()) {
	prev := dialerResolverForTestPtr.Swap(r)
	return func() { dialerResolverForTestPtr.Store(prev) }
}

// forbiddenIP binds a category sentinel to a user-safe rejection message without echoing raw IPs.
type forbiddenIP struct {
	sentinel error
	reason   string
}

func (e forbiddenIP) Error() string { return "addrguard: " + e.reason }
func (e forbiddenIP) Unwrap() error {
	if e.sentinel != nil {
		return e.sentinel
	}
	return ErrAddressForbidden
}

// Resolver resolves a hostname into a list of IPs; injectable so unit tests avoid real DNS queries.
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

// DefaultResolver is the production system resolver returning single-stack and dual-stack addresses.
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

// Exact cloud metadata host IPs that reside inside CGNAT/ULA ranges (Alibaba Cloud, AWS IMDSv6, GCP).
// Non-metadata addresses inside 100.64.0.0/10 and fc00::/7 remain permitted for intranet/Tailscale/IPv6 deployments.
var (
	aliyunMetadataIP = net.ParseIP("100.100.100.200").To4()
	awsIMDSv6IP      = net.ParseIP("fd00:ec2::254").To16()
	gcpIMDSv6IP      = net.ParseIP("fd20:ce::254").To16()

	nat64WellKnownRange = mustCIDR("64:ff9b::/96")
	v4ThisNetRange      = mustCIDR("0.0.0.0/8")
	v4IETFProtoRange    = mustCIDR("192.0.0.0/24")
	v4BenchmarkRange    = mustCIDR("198.18.0.0/15")
	v4ReservedRange     = mustCIDR("240.0.0.0/4")

	v6CompatRange     = mustCIDR("::/96")
	v6DiscardRange    = mustCIDR("100::/64")
	v6SiteLocalRange  = mustCIDR("fec0::/10")
	v6SixToFourRange  = mustCIDR("2002::/16")
	v6TeredoRange     = mustCIDR("2001::/32")
	v6LocalNAT64Range = mustCIDR("64:ff9b:1::/48")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("addrguard: bad builtin cidr " + s)
	}
	return n
}

func unwrapNAT64(ip net.IP) (net.IP, bool) {
	if ip == nil || ip.To4() != nil {
		return ip, false
	}
	ip16 := ip.To16()
	if ip16 == nil || !nat64WellKnownRange.Contains(ip16) {
		return ip, false
	}
	return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]), true
}

// ValidateIP checks whether a single IP is permitted under PurposeGeneric.
func ValidateIP(ip net.IP) error {
	return ValidateIPFor(PurposeGeneric, ip)
}

// ValidateIPFor checks whether a single IP is permitted for the given outbound purpose.
// Cloud metadata, link-local, unspecified, multicast, broadcast, and reserved/deprecated ranges are
// always rejected. CGNAT (except 100.100.100.200) and ULA (except fd00:ec2::254 and fd20:ce::254)
// are permitted. Loopback is permitted under dev bypass or (for datasource/OSC only) allow_loopback_datasource.
func ValidateIPFor(purpose Purpose, ip net.IP) error {
	if ip == nil {
		return forbiddenIP{ErrAddressForbidden, "unparsable address"}
	}
	if embedded, ok := unwrapNAT64(ip); ok {
		return ValidateIPFor(purpose, embedded)
	}
	v4 := ip.To4()
	if ip.IsLinkLocalUnicast() {
		return forbiddenIP{ErrCloudMetadataForbidden, "link-local or cloud metadata address rejected"}
	}
	if v4 != nil && v4.Equal(aliyunMetadataIP) {
		return forbiddenIP{ErrCloudMetadataForbidden, "cloud metadata address rejected"}
	}
	if v4 == nil && (ip.Equal(awsIMDSv6IP) || ip.Equal(gcpIMDSv6IP)) {
		return forbiddenIP{ErrCloudMetadataForbidden, "cloud metadata address rejected"}
	}
	if ip.IsLoopback() {
		if loopbackBypassEnabled() {
			return nil
		}
		if (purpose == PurposeDatasource || purpose == PurposeOSC) && allowLoopbackDatasourceEnabled() {
			return nil
		}
		if purpose == PurposeDatasource || purpose == PurposeOSC {
			return forbiddenIP{ErrLoopbackForbidden, "loopback address rejected (enable outbound.allow_loopback_datasource for local datasource targets)"}
		}
		return forbiddenIP{ErrLoopbackForbidden, "loopback address rejected"}
	}
	if ip.IsUnspecified() {
		return forbiddenIP{ErrReservedAddressForbidden, "unspecified address rejected"}
	}
	if ip.IsLinkLocalMulticast() {
		return forbiddenIP{ErrReservedAddressForbidden, "link-local multicast address rejected"}
	}
	if ip.IsMulticast() {
		return forbiddenIP{ErrReservedAddressForbidden, "multicast address rejected"}
	}
	if ip.Equal(net.IPv4bcast) {
		return forbiddenIP{ErrReservedAddressForbidden, "broadcast address rejected"}
	}
	if v4 != nil {
		for _, r := range []*net.IPNet{v4ThisNetRange, v4IETFProtoRange, v4BenchmarkRange, v4ReservedRange} {
			if r.Contains(v4) {
				return forbiddenIP{ErrReservedAddressForbidden, "reserved IPv4 address rejected"}
			}
		}
		return nil
	}
	for _, r := range []*net.IPNet{
		v6CompatRange, v6DiscardRange, v6SiteLocalRange,
		v6SixToFourRange, v6TeredoRange, v6LocalNAT64Range,
	} {
		if r.Contains(ip) {
			return forbiddenIP{ErrReservedAddressForbidden, "reserved IPv6 address rejected"}
		}
	}
	return nil
}

// ---- SelfGuard: dynamic process-local, metadata DB, and K8s infra protection ----

type selfGuardState struct {
	mu             sync.RWMutex
	gen            uint64
	selfPort       int
	selfIPs        []net.IP
	metaHost       string
	metaPort       int
	metaDBName     string
	metaIPs        []net.IP
	k8sIP          net.IP
	k8sPort        int
	dnsNameservers []net.IP
}

var selfGuard = &selfGuardState{}

func collectLocalInterfaceIPs() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if podIP := net.ParseIP(strings.TrimSpace(os.Getenv("POD_IP"))); podIP != nil {
		ips = append(ips, podIP)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() {
			ips = append(ips, ip)
		}
	}
	return ips
}

func dedupIPs(in []net.IP) []net.IP {
	out := make([]net.IP, 0, len(in))
	for _, candidate := range in {
		if candidate == nil {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing.Equal(candidate) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, candidate)
		}
	}
	return out
}

func ipMatchesAny(ip net.IP, list []net.IP) bool {
	for _, item := range list {
		if item.Equal(ip) {
			return true
		}
	}
	return false
}

// RegisterSelfListener records Yearning's bound HTTP listen port and all local interface/pod IPs.
// Should be called on startup with config.ListenAddress and again via net.Listener.Addr() once bound.
func RegisterSelfListener(listenAddr string) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return
	}
	var ips []net.IP
	trimmedHost := strings.Trim(host, "[]")
	if trimmedHost == "" || trimmedHost == "0.0.0.0" || trimmedHost == "::" {
		ips = collectLocalInterfaceIPs()
	} else if ip := net.ParseIP(trimmedHost); ip != nil {
		if ip.IsUnspecified() {
			ips = collectLocalInterfaceIPs()
		} else {
			ips = []net.IP{ip}
			if podIP := net.ParseIP(strings.TrimSpace(os.Getenv("POD_IP"))); podIP != nil {
				ips = append(ips, podIP)
			}
		}
	} else {
		ips = collectLocalInterfaceIPs()
	}

	selfGuard.mu.Lock()
	defer selfGuard.mu.Unlock()
	selfGuard.selfPort = port
	selfGuard.selfIPs = dedupIPs(ips)
}

// RegisterMetadataDB registers Yearning's metadata PostgreSQL endpoint and refreshes hostname resolution
// every 60 seconds until ctx is cancelled.
func RegisterMetadataDB(ctx context.Context, host string, port int, dbName string) {
	host = strings.TrimSpace(host)
	if host == "" || port < 1 || port > 65535 {
		return
	}
	var initial []net.IP
	isLiteral := false
	if ip := parseHostIPLiteral(host); ip != nil {
		initial = []net.IP{ip}
		isLiteral = true
	} else {
		resolveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ips, err := DefaultResolver(resolveCtx, host)
		cancel()
		if err == nil && len(ips) > 0 {
			initial = dedupIPs(ips)
		}
	}

	selfGuard.mu.Lock()
	selfGuard.gen++
	gen := selfGuard.gen
	selfGuard.metaHost = host
	selfGuard.metaPort = port
	selfGuard.metaDBName = strings.TrimSpace(dbName)
	selfGuard.metaIPs = initial
	selfGuard.mu.Unlock()

	if isLiteral || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				ips, err := DefaultResolver(lookupCtx, host)
				cancel()
				if err != nil || len(ips) == 0 {
					continue
				}
				selfGuard.mu.Lock()
				if selfGuard.gen == gen {
					selfGuard.metaIPs = dedupIPs(ips)
				}
				selfGuard.mu.Unlock()
			}
		}
	}()
}

// RegisterClusterInfraFromEnv detects Kubernetes API server coordinates and resolv.conf nameservers.
func RegisterClusterInfraFromEnv() {
	var k8sIP net.IP
	var k8sPort int
	if kHost := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")); kHost != "" {
		if ip := parseHostIPLiteral(kHost); ip != nil {
			k8sIP = ip
		} else {
			lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if ips, err := DefaultResolver(lookupCtx, kHost); err == nil && len(ips) > 0 {
				k8sIP = ips[0]
			}
			cancel()
		}
		k8sPort = 443
		if pStr := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT")); pStr != "" {
			if p, err := strconv.Atoi(pStr); err == nil && p >= 1 && p <= 65535 {
				k8sPort = p
			}
		}
	}
	nameservers := parseResolvNameservers("/etc/resolv.conf")
	selfGuard.mu.Lock()
	defer selfGuard.mu.Unlock()
	selfGuard.k8sIP = k8sIP
	selfGuard.k8sPort = k8sPort
	selfGuard.dnsNameservers = dedupIPs(nameservers)
}

func parseResolvNameservers(path string) []net.IP {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			if ip := net.ParseIP(fields[1]); ip != nil {
				out = append(out, ip)
			}
		}
	}
	return out
}

// SetSelfListenerForTest sets self-listener targets in unit tests and returns a restore callback.
func SetSelfListenerForTest(ips []net.IP, port int) (restore func()) {
	selfGuard.mu.Lock()
	prevIPs, prevPort := append([]net.IP(nil), selfGuard.selfIPs...), selfGuard.selfPort
	selfGuard.selfIPs = dedupIPs(ips)
	selfGuard.selfPort = port
	selfGuard.mu.Unlock()
	return func() {
		selfGuard.mu.Lock()
		selfGuard.selfIPs = prevIPs
		selfGuard.selfPort = prevPort
		selfGuard.mu.Unlock()
	}
}

// SetMetadataDBForTest sets metadata DB guard targets in unit tests and returns a restore callback.
func SetMetadataDBForTest(ips []net.IP, port int, dbName string) (restore func()) {
	selfGuard.mu.Lock()
	prevGen, prevHost, prevPort, prevDB, prevIPs := selfGuard.gen, selfGuard.metaHost, selfGuard.metaPort, selfGuard.metaDBName, append([]net.IP(nil), selfGuard.metaIPs...)
	selfGuard.gen++
	selfGuard.metaHost = ""
	selfGuard.metaPort = port
	selfGuard.metaDBName = strings.TrimSpace(dbName)
	selfGuard.metaIPs = dedupIPs(ips)
	selfGuard.mu.Unlock()
	return func() {
		selfGuard.mu.Lock()
		selfGuard.gen = prevGen + 1
		selfGuard.metaHost = prevHost
		selfGuard.metaPort = prevPort
		selfGuard.metaDBName = prevDB
		selfGuard.metaIPs = prevIPs
		selfGuard.mu.Unlock()
	}
}

// SetClusterInfraForTest sets K8s API / DNS nameserver targets in unit tests and returns a restore callback.
func SetClusterInfraForTest(k8sIP net.IP, k8sPort int, nameservers []net.IP) (restore func()) {
	selfGuard.mu.Lock()
	prevK8sIP, prevK8sPort, prevNS := selfGuard.k8sIP, selfGuard.k8sPort, append([]net.IP(nil), selfGuard.dnsNameservers...)
	selfGuard.k8sIP = k8sIP
	selfGuard.k8sPort = k8sPort
	selfGuard.dnsNameservers = dedupIPs(nameservers)
	selfGuard.mu.Unlock()
	return func() {
		selfGuard.mu.Lock()
		selfGuard.k8sIP = prevK8sIP
		selfGuard.k8sPort = prevK8sPort
		selfGuard.dnsNameservers = prevNS
		selfGuard.mu.Unlock()
	}
}

// ResetSelfGuardForTest clears all SelfGuard state and returns a restore callback.
func ResetSelfGuardForTest() (restore func()) {
	selfGuard.mu.Lock()
	prevGen := selfGuard.gen
	prevSelfPort := selfGuard.selfPort
	prevSelfIPs := append([]net.IP(nil), selfGuard.selfIPs...)
	prevMetaHost := selfGuard.metaHost
	prevMetaPort := selfGuard.metaPort
	prevMetaDBName := selfGuard.metaDBName
	prevMetaIPs := append([]net.IP(nil), selfGuard.metaIPs...)
	prevK8sIP := selfGuard.k8sIP
	prevK8sPort := selfGuard.k8sPort
	prevDNS := append([]net.IP(nil), selfGuard.dnsNameservers...)
	selfGuard.gen++
	selfGuard.selfPort = 0
	selfGuard.selfIPs = nil
	selfGuard.metaHost = ""
	selfGuard.metaPort = 0
	selfGuard.metaDBName = ""
	selfGuard.metaIPs = nil
	selfGuard.k8sIP = nil
	selfGuard.k8sPort = 0
	selfGuard.dnsNameservers = nil
	selfGuard.mu.Unlock()
	return func() {
		selfGuard.mu.Lock()
		selfGuard.gen = prevGen + 1
		selfGuard.selfPort = prevSelfPort
		selfGuard.selfIPs = prevSelfIPs
		selfGuard.metaHost = prevMetaHost
		selfGuard.metaPort = prevMetaPort
		selfGuard.metaDBName = prevMetaDBName
		selfGuard.metaIPs = prevMetaIPs
		selfGuard.k8sIP = prevK8sIP
		selfGuard.k8sPort = prevK8sPort
		selfGuard.dnsNameservers = prevDNS
		selfGuard.mu.Unlock()
	}
}

// CheckSelfTargetFor verifies that (ip, port, targetDBName) does not hit Yearning's own listener,
// cluster control-plane/DNS endpoints, or Yearning's metadata database.
// For PurposeDatasource: same-instance PostgreSQL targets are allowed only when targetDBName is non-empty
// and differs from the registered metadata database name (PurposeRetentionDB is always exempt for metadata DB).
func CheckSelfTargetFor(purpose Purpose, ip net.IP, port int, targetDBName string) error {
	return checkSelfTarget(purpose, ip, port, targetDBName, false)
}

func checkSelfTarget(purpose Purpose, ip net.IP, port int, targetDBName string, allowEmptyDatasourceDB bool) error {
	if ip == nil {
		return nil
	}
	if embedded, ok := unwrapNAT64(ip); ok {
		ip = embedded
	}
	selfGuard.mu.RLock()
	selfPort := selfGuard.selfPort
	selfIPs := selfGuard.selfIPs
	metaPort := selfGuard.metaPort
	metaDBName := selfGuard.metaDBName
	metaIPs := selfGuard.metaIPs
	k8sIP := selfGuard.k8sIP
	k8sPort := selfGuard.k8sPort
	dnsNS := selfGuard.dnsNameservers
	selfGuard.mu.RUnlock()

	if selfPort > 0 && port == selfPort && ipMatchesAny(ip, selfIPs) {
		return forbiddenIP{ErrSelfTargetForbidden, "target matches yearning listener port"}
	}
	if k8sIP != nil && k8sPort > 0 && port == k8sPort && ip.Equal(k8sIP) {
		return forbiddenIP{ErrClusterControlPlaneForbidden, "target matches cluster kubernetes api"}
	}
	if port == 53 && ipMatchesAny(ip, dnsNS) {
		return forbiddenIP{ErrClusterControlPlaneForbidden, "target matches cluster dns nameserver"}
	}
	if metaPort > 0 && port == metaPort && ipMatchesAny(ip, metaIPs) {
		if purpose == PurposeRetentionDB {
			return nil
		}
		if purpose == PurposeDatasource {
			trimmed := strings.TrimSpace(targetDBName)
			if trimmed == "" {
				if allowEmptyDatasourceDB {
					return nil
				}
				return forbiddenIP{ErrMetadataDatabaseForbidden, "target metadata database rejected"}
			}
			if metaDBName != "" && strings.EqualFold(trimmed, metaDBName) {
				return forbiddenIP{ErrMetadataDatabaseForbidden, "target matches yearning metadata database"}
			}
			return nil
		}
		return forbiddenIP{ErrMetadataDatabaseForbidden, "target matches yearning metadata database"}
	}
	return nil
}

// parseHostIPLiteral parses a host string into a net.IP if it is an IP literal, stripping optional
// brackets and IPv6 zone identifiers (e.g. "[fe80::1%eth0]" -> fe80::1) so zoned or bracketed
// literals cannot slip past syntax-time checks as "hostnames".
func parseHostIPLiteral(host string) net.IP {
	h := strings.TrimSpace(host)
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	return net.ParseIP(h)
}

// ValidateHostForm validates the literal host syntax under PurposeGeneric.
func ValidateHostForm(host string) error {
	return ValidateHostFormFor(PurposeGeneric, host)
}

// ValidateHostFormFor validates literal host syntax (non-empty, non-Unix-socket, IP literal policy).
func ValidateHostFormFor(purpose Purpose, host string) error {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return forbiddenIP{ErrAddressForbidden, "empty host"}
	}
	if trimmed[0] == '/' {
		return ErrUnixSocket
	}
	if ip := parseHostIPLiteral(trimmed); ip != nil {
		return ValidateIPFor(purpose, ip)
	}
	return nil
}

// ValidateHost resolves host and validates all resolved IPs under PurposeGeneric.
func ValidateHost(ctx context.Context, host string, resolver Resolver) error {
	return ValidateHostFor(ctx, PurposeGeneric, host, resolver)
}

// ValidateHostFor validates host syntax and (when DNS resolution succeeds) every resolved IP for purpose.
// DNS resolution failure defers enforcement to dial-time GuardedDialer (preserving create-before-connect).
func ValidateHostFor(ctx context.Context, purpose Purpose, host string, resolver Resolver) error {
	if err := ValidateHostFormFor(purpose, host); err != nil {
		return err
	}
	if parseHostIPLiteral(host) != nil {
		return nil
	}
	if resolver == nil {
		resolver = DefaultResolver
	}
	ips, err := resolver(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	for _, ip := range ips {
		if verr := ValidateIPFor(purpose, ip); verr != nil {
			return verr
		}
	}
	return nil
}

// ValidateEndpointFor validates port, host syntax, IP policy, and SelfGuard at configuration/probe time.
// When purpose is PurposeDatasource and targetDBName is empty (instance-level PG datasource creation),
// metadata-DB database matching is deferred until probe/dial time where effectiveDB is known.
func ValidateEndpointFor(ctx context.Context, purpose Purpose, host string, port int, targetDBName string, resolver Resolver) error {
	if port < 1 || port > 65535 {
		return forbiddenIP{ErrAddressForbidden, "port out of range"}
	}
	if err := ValidateHostFormFor(purpose, host); err != nil {
		return err
	}
	allowEmptyDS := purpose == PurposeDatasource && strings.TrimSpace(targetDBName) == ""
	if ip := parseHostIPLiteral(host); ip != nil {
		return checkSelfTarget(purpose, ip, port, targetDBName, allowEmptyDS)
	}
	if resolver == nil {
		resolver = DefaultResolver
	}
	ips, err := resolver(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	for _, ip := range ips {
		if verr := ValidateIPFor(purpose, ip); verr != nil {
			return verr
		}
		if serr := checkSelfTarget(purpose, ip, port, targetDBName, allowEmptyDS); serr != nil {
			return serr
		}
	}
	return nil
}

// ResolvePinned resolves host once, validates EVERY resolved IP for purpose and SelfGuard, and returns
// a single validated IP literal (preferring IPv4 when present). Unlike ValidateHost*, DNS failure
// fails closed because the caller intends to pin and dial the target immediately (e.g. in-process gh-ost).
func ResolvePinned(ctx context.Context, purpose Purpose, host string, port int, targetDBName string, resolver Resolver) (net.IP, error) {
	if port < 1 || port > 65535 {
		return nil, forbiddenIP{ErrAddressForbidden, "port out of range"}
	}
	if err := ValidateHostFormFor(purpose, host); err != nil {
		return nil, err
	}
	if ip := parseHostIPLiteral(host); ip != nil {
		if serr := CheckSelfTargetFor(purpose, ip, port, targetDBName); serr != nil {
			return nil, serr
		}
		if v4 := ip.To4(); v4 != nil {
			return v4, nil
		}
		return ip, nil
	}
	if resolver == nil {
		if tr := dialerResolverForTestPtr.Load(); tr != nil {
			resolver = func(c context.Context, h string) ([]net.IP, error) {
				addrs, err := tr.LookupIPAddr(c, h)
				if err != nil {
					return nil, err
				}
				out := make([]net.IP, 0, len(addrs))
				for _, a := range addrs {
					out = append(out, a.IP)
				}
				return out, nil
			}
		} else {
			resolver = DefaultResolver
		}
	}
	ips, err := resolver(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, forbiddenIP{ErrAddressForbidden, "hostname resolution failed"}
	}
	var firstV4, firstAny net.IP
	for _, ip := range ips {
		if verr := ValidateIPFor(purpose, ip); verr != nil {
			return nil, verr
		}
		if serr := CheckSelfTargetFor(purpose, ip, port, targetDBName); serr != nil {
			return nil, serr
		}
		if v4 := ip.To4(); v4 != nil {
			if firstV4 == nil {
				firstV4 = v4
			}
		}
		if firstAny == nil {
			firstAny = ip
		}
	}
	if firstV4 != nil {
		return firstV4, nil
	}
	return firstAny, nil
}

// controlGuard preserves the legacy signature for PurposeGeneric unit checks.
func controlGuard(network, address string, raw syscall.RawConn) error {
	return controlGuardFor(PurposeGeneric, "")(network, address, raw)
}

func controlGuardFor(purpose Purpose, targetDBName string) func(network, address string, _ syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		if network == "unix" || network == "unixgram" || network == "unixpacket" {
			return ErrUnixSocket
		}
		host, portStr, err := net.SplitHostPort(address)
		if err != nil {
			return forbiddenIP{ErrAddressForbidden, "dial address unparsable"}
		}
		port, perr := strconv.Atoi(portStr)
		if perr != nil || port < 1 || port > 65535 {
			return forbiddenIP{ErrAddressForbidden, "dial port invalid"}
		}
		if i := strings.IndexByte(host, '%'); i >= 0 {
			host = host[:i]
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return forbiddenIP{ErrAddressForbidden, "dial address is not an IP literal"}
		}
		if verr := ValidateIPFor(purpose, ip); verr != nil {
			return verr
		}
		return CheckSelfTargetFor(purpose, ip, port, targetDBName)
	}
}

// GuardedDialer returns a net.Dialer protected under PurposeGeneric.
func GuardedDialer(timeout time.Duration) *net.Dialer {
	return GuardedDialerFor(PurposeGeneric, timeout)
}

// GuardedDialerFor returns a net.Dialer whose Control hook validates the resolved IP for purpose and SelfGuard.
func GuardedDialerFor(purpose Purpose, timeout time.Duration) *net.Dialer {
	return GuardedDialerForDatabase(purpose, "", timeout)
}

// GuardedDialerForDatabase returns a guarded net.Dialer carrying the target database name for metadata-DB isolation.
func GuardedDialerForDatabase(purpose Purpose, targetDBName string, timeout time.Duration) *net.Dialer {
	d := &net.Dialer{
		Timeout: timeout,
		Control: controlGuardFor(purpose, targetDBName),
	}
	if tr := dialerResolverForTestPtr.Load(); tr != nil {
		d.Resolver = tr
	}
	return d
}

// DialFunc returns a dial function under PurposeGeneric compatible with pgx, go-sql-driver, and http.Transport.
func DialFunc(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return DialFuncFor(PurposeGeneric, timeout)
}

// DialFuncFor returns a purpose-scoped guarded dial function.
func DialFuncFor(purpose Purpose, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return DialFuncForDatabase(purpose, "", timeout)
}

// DialFuncForDatabase returns a purpose-scoped guarded dial function carrying the target database name.
func DialFuncForDatabase(purpose Purpose, targetDBName string, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network == "unix" || network == "unixgram" || network == "unixpacket" {
			return nil, ErrUnixSocket
		}
		if len(addr) > 0 && addr[0] == '/' {
			return nil, ErrUnixSocket
		}
		d := GuardedDialerForDatabase(purpose, targetDBName, timeout)
		return d.DialContext(ctx, network, addr)
	}
}

// CheckEndpoint performs fast endpoint shape validation under PurposeGeneric.
func CheckEndpoint(host string, port int) error {
	return CheckEndpointFor(PurposeGeneric, host, port)
}

// CheckEndpointFor performs fast endpoint validation (port range + host form + literal SelfGuard check).
func CheckEndpointFor(purpose Purpose, host string, port int) error {
	return CheckEndpointForDatabase(purpose, host, port, "")
}

// CheckEndpointForDatabase performs fast endpoint validation with explicit target database name.
func CheckEndpointForDatabase(purpose Purpose, host string, port int, targetDBName string) error {
	if port < 1 || port > 65535 {
		return forbiddenIP{ErrAddressForbidden, "port out of range"}
	}
	if err := ValidateHostFormFor(purpose, host); err != nil {
		return err
	}
	if ip := parseHostIPLiteral(host); ip != nil {
		allowEmptyDS := purpose == PurposeDatasource && strings.TrimSpace(targetDBName) == ""
		return checkSelfTarget(purpose, ip, port, targetDBName, allowEmptyDS)
	}
	return nil
}

// RedirectPolicy builds an HTTP CheckRedirect handler blocking scheme downgrades (https -> http),
// capping hop count (maxHops <= 0 rejects all redirects with http.ErrUseLastResponse), and optionally
// forbidding cross-host/port redirects (used for OIDC token exchanges carrying client_secret).
func RedirectPolicy(maxHops int, allowCrossHost bool) func(req *http.Request, via []*http.Request) error {
	if maxHops <= 0 {
		return func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("addrguard: stopped after %d redirects", maxHops)
		}
		if len(via) > 0 {
			prev := via[len(via)-1]
			if strings.EqualFold(prev.URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
				return forbiddenIP{ErrAddressForbidden, "redirect https downgrade forbidden"}
			}
			if !strings.EqualFold(req.URL.Scheme, "http") && !strings.EqualFold(req.URL.Scheme, "https") {
				return forbiddenIP{ErrAddressForbidden, "redirect non-http scheme forbidden"}
			}
			if !allowCrossHost && !sameHostAndPort(via[0].URL, req.URL) {
				return forbiddenIP{ErrAddressForbidden, "cross-host redirect forbidden"}
			}
		}
		return nil
	}
}

func sameHostAndPort(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
