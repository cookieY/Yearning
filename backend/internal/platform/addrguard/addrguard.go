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
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

// forbiddenIP binds a category sentinel to a user-safe rejection message without echoing raw IPs.
type forbiddenIP struct {
	sentinel error
	reason   string
}

// Resolver resolves a hostname into a list of IPs; injectable so unit tests avoid real DNS queries.
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

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
