// addrguard_test.go T05 address guard unit verification: table-driven rejection of forbidden IPs, Unix sockets,
// and mixed resolution results (using a fake Resolver without real DNS queries).
// Bypass matrix: when YEARNING_ALLOW_LOOPBACK_TARGETS is active, only loopback is allowed,
// while metadata, link-local, CGNAT (100.64/10), ULA (fc00::/7), unspecified, broadcast,
// multicast, and Unix sockets remain rejected.
package addrguard

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test IP %q", s)
	}
	return ip
}

// bypassOff forces the loopback bypass off within a test (the test process environment may carry
// YEARNING_ALLOW_LOOPBACK_TARGETS=1 during integration runs, so each test pins the default rejection
// semantics independent of the external environment).
func bypassOff(t *testing.T) {
	t.Helper()
	t.Cleanup(SetLoopbackBypassForTest(false))
	SetLoopbackBypassForTest(false)
}

// TestValidateIPRejects verifies the T05 rejection surface: loopback, link-local (including metadata),
// unspecified, broadcast, multicast, CGNAT shared space, and ULA across IPv4, IPv6, and IPv4-mapped IPv6 forms
// (with loopback bypass disabled).
func TestValidateIPRejects(t *testing.T) {
	bypassOff(t)
	cases := []string{
		"127.0.0.1", "127.1.2.3", "127.255.255.255", // v4 loopback
		"::1",                                               // v6 loopback
		"169.254.169.254", "169.254.0.1", "169.254.255.254", // IPv4 link-local (cloud metadata)
		"fe80::1", "fe80::", "febf::1", // v6 link-local
		"0.0.0.0", "::", // unspecified
		"255.255.255.255",        // IPv4 broadcast
		"224.0.0.1", "239.1.1.1", // IPv4 multicast
		"ff00::1", "ff02::1", "ff0e::1", // IPv6 multicast
		"224.0.0.251",                   // IPv4 link-local multicast
		"::ffff:127.0.0.1",              // 4-in-6 loopback
		"::ffff:169.254.169.254",        // 4-in-6 link-local
		"::ffff:0.0.0.0",                // 4-in-6 unspecified
		"100.100.100.200",               // SEC-0009: Alibaba Cloud metadata (inside CGNAT range)
		"100.64.1.1", "100.127.255.254", // Inside CGNAT range boundaries (full 100.64/10 block)
		"fc00::1", "fd00:ec2::254", // ULA: AWS IMDSv6, etc.
		"fd20:ce::254",           // GCP metadata (inside ULA range)
		"fdff::1",                // Inside ULA /7 upper boundary
		"::ffff:100.100.100.200", // IPv4-mapped IPv6 CGNAT
	}
	for _, c := range cases {
		if err := ValidateIP(mustIP(t, c)); !errors.Is(err, ErrAddressForbidden) {
			t.Errorf("ValidateIP(%s) = %v; want forbidden", c, err)
		}
	}
	if err := ValidateIP(nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("ValidateIP(nil) = %v; want forbidden", err)
	}
}

// TestValidateIPAllows verifies the allowed surface: RFC1918 private unicast (normal internal target DBs
// are not SSRF) and public unicast; 100.x addresses outside CGNAT/ULA ranges remain allowed (public unicast outside /10).
func TestValidateIPAllows(t *testing.T) {
	cases := []string{
		"10.0.0.1", "172.16.0.1", "172.31.255.254", "192.168.1.10", // RFC1918 private network
		"8.8.8.8",         // Public IPv4
		"1.1.1.1",         // Public IPv4
		"100.63.255.254",  // Outside CGNAT range (public unicast)
		"100.128.0.1",     // Outside CGNAT range (public unicast)
		"2001:db8::1",     // Public IPv6 unicast
		"fe00::1",         // Outside ULA /7 range (not in fc00..fdff)
		"::ffff:10.1.2.3", // IPv4-mapped IPv6 private network
		"::ffff:8.8.8.8",  // IPv4-mapped IPv6 public address
	}
	for _, c := range cases {
		if err := ValidateIP(mustIP(t, c)); err != nil {
			t.Errorf("ValidateIP(%s) = %v; want allowed", c, err)
		}
	}
}

// TestValidateHostForm tests literal host forms: Unix socket paths and empty hosts are rejected,
// IP literals are validated immediately, and hostnames are allowed pending resolution.
func TestValidateHostForm(t *testing.T) {
	bypassOff(t)
	if err := ValidateHostForm("/var/run/postgresql"); !errors.Is(err, ErrUnixSocket) {
		t.Errorf("unix path = %v; want ErrUnixSocket", err)
	}
	if err := ValidateHostForm(""); err == nil {
		t.Error("empty host accepted")
	}
	if err := ValidateHostForm("127.0.0.1"); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("127.0.0.1 = %v; want forbidden", err)
	}
	if err := ValidateHostForm("169.254.169.254"); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("metadata IP = %v; want forbidden", err)
	}
	if err := ValidateHostForm("db-internal.prod.svc"); err != nil {
		t.Errorf("hostname = %v; want pass-through", err)
	}
}

// fakeResolver is a fake resolution function (no real DNS).
func fakeResolver(ips ...net.IP) Resolver {
	return func(context.Context, string) ([]net.IP, error) { return ips, nil }
}

func errResolver(context.Context, string) ([]net.IP, error) {
	return nil, errors.New("no such host")
}

// TestValidateHostResolved verifies per-IP validation after resolution: any forbidden address rejects the whole host
// (mixed A records containing metadata must be rejected); resolution failure is not rejected (dialer layer guards connect).
func TestValidateHostResolved(t *testing.T) {
	bypassOff(t)
	ctx := context.Background()
	if err := ValidateHost(ctx, "cluster.local", fakeResolver(mustIP(t, "10.0.0.5"), mustIP(t, "10.0.0.6"))); err != nil {
		t.Errorf("private resolution = %v; want allow", err)
	}
	if err := ValidateHost(ctx, "rebind.example", fakeResolver(mustIP(t, "10.0.0.5"), mustIP(t, "169.254.169.254"))); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("mixed resolution = %v; want forbidden", err)
	}
	if err := ValidateHost(ctx, "rebind.example", fakeResolver(mustIP(t, "127.0.0.53"))); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("loopback resolution = %v; want forbidden", err)
	}
	if err := ValidateHost(ctx, "ipv6.example", fakeResolver(mustIP(t, "2001:db8::5"))); err != nil {
		t.Errorf("public v6 resolution = %v; want allow", err)
	}
	if err := ValidateHost(ctx, "ipv6.example", fakeResolver(mustIP(t, "::ffff:169.254.169.254"))); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("mapped metadata = %v; want forbidden", err)
	}
	// SEC-0009: resolved addresses falling inside CGNAT/ULA (cloud metadata ranges) are also rejected.
	if err := ValidateHost(ctx, "aliyun.example", fakeResolver(mustIP(t, "100.100.100.200"))); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("aliyun metadata resolution = %v; want forbidden", err)
	}
	if err := ValidateHost(ctx, "aws.example", fakeResolver(mustIP(t, "fd00:ec2::254"))); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("aws imdsv6 resolution = %v; want forbidden", err)
	}
	// DNS resolution failure: not rejected (preserves create-before-connect semantics; dialer guard is authoritative).
	if err := ValidateHost(ctx, "notyet.deployed.example", errResolver); err != nil {
		t.Errorf("unresolvable = %v; want defer to dial guard", err)
	}
}

// TestDialFuncUnixAndPath verifies dual-path Unix socket rejection (both network family and path-style addr;
// Unix socket rejection is unaffected by the loopback bypass).
func TestDialFuncUnixAndPath(t *testing.T) {
	bypassOff(t)
	f := DialFunc(time.Second)
	ctx := context.Background()
	if _, err := f(ctx, "unix", "/tmp/db.sock"); !errors.Is(err, ErrUnixSocket) {
		t.Errorf("unix network = %v; want ErrUnixSocket", err)
	}
	if _, err := f(ctx, "tcp", "/tmp/db.sock"); !errors.Is(err, ErrUnixSocket) {
		t.Errorf("path addr = %v; want ErrUnixSocket", err)
	}
}

// TestControlGuard verifies the dial-time guard: resolved forbidden IPs (DNS rebinding path) are rejected
// before connect; valid private addresses are allowed; non-IP literals fail closed (with bypass disabled).
func TestControlGuard(t *testing.T) {
	bypassOff(t)
	if err := controlGuard("tcp", "169.254.169.254:5432", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("metadata dial = %v; want forbidden", err)
	}
	if err := controlGuard("tcp", "[::ffff:127.0.0.1]:3306", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("mapped loopback dial = %v; want forbidden", err)
	}
	if err := controlGuard("tcp", "[fe80::1%eth0]:5432", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("zoned link-local dial = %v; want forbidden", err)
	}
	if err := controlGuard("tcp4", "10.2.3.4:3306", nil); err != nil {
		t.Errorf("private dial = %v; want allow", err)
	}
	// SEC-0009: dial-time rejection of CGNAT/ULA ranges (cloud metadata ranges, including IPv4-mapped IPv6 forms).
	if err := controlGuard("tcp", "100.100.100.200:80", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("aliyun metadata dial = %v; want forbidden", err)
	}
	if err := controlGuard("tcp", "[fd00:ec2::254]:80", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("aws imdsv6 dial = %v; want forbidden", err)
	}
	if err := controlGuard("tcp", "[::ffff:100.100.100.200]:80", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("mapped cgnat dial = %v; want forbidden", err)
	}
	if err := controlGuard("tcp", "unix:/var/run/x:5432", nil); err == nil {
		t.Error("non-IP dial accepted")
	}
	// GuardedDialer against localhost (resolving to 127.0.0.1) must fail to dial (no network dependency:
	// Control rejects before connect, independent of target reachability).
	conn, err := GuardedDialer(500*time.Millisecond).DialContext(context.Background(), "tcp", "127.0.0.1:15432")
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial to loopback must be rejected by guard")
	} else if !errors.Is(err, ErrAddressForbidden) {
		// Some environments may wrap the Control error; at minimum the connection must not succeed.
		t.Logf("loopback dial rejected with: %v", err)
	}
}

// TestLoopbackBypassMatrix verifies the development loopback bypass matrix: when enabled, only loopback is allowed
// across the full chain (ValidateIP/ValidateHostForm/ValidateHost/CheckEndpoint/controlGuard),
// while metadata, link-local, unspecified, broadcast, multicast, and Unix sockets remain rejected;
// when disabled, loopback rejection is restored.
func TestLoopbackBypassMatrix(t *testing.T) {
	restore := SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	// Allowed surface under bypass: loopback only (full IPv4 127/8 block, ::1, and IPv4-mapped IPv6 loopback).
	allowed := []string{"127.0.0.1", "127.1.2.3", "127.255.255.255", "::1", "::ffff:127.0.0.1"}
	for _, c := range allowed {
		if err := ValidateIP(mustIP(t, c)); err != nil {
			t.Errorf("bypass: ValidateIP(%s) = %v; want allowed", c, err)
		}
	}
	// Both ValidateHostForm inline checks and ValidateHost resolution paths allow loopback under bypass.
	if err := ValidateHostForm("127.0.0.1"); err != nil {
		t.Errorf("bypass: ValidateHostForm(127.0.0.1) = %v; want allowed", err)
	}
	if err := ValidateHost(context.Background(), "localhost", fakeResolver(mustIP(t, "127.0.0.53"))); err != nil {
		t.Errorf("bypass: ValidateHost(localhost→127.0.0.53) = %v; want allowed", err)
	}
	if err := CheckEndpoint("127.0.0.1", 5432); err != nil {
		t.Errorf("bypass: CheckEndpoint(127.0.0.1) = %v; want allowed", err)
	}
	if err := controlGuard("tcp", "127.0.0.1:5432", nil); err != nil {
		t.Errorf("bypass: controlGuard(127.0.0.1) = %v; want allowed", err)
	}
	if err := controlGuard("tcp", "[::1]:5432", nil); err != nil {
		t.Errorf("bypass: controlGuard(::1) = %v; want allowed", err)
	}

	// Never allowed even under bypass: metadata, link-local, CGNAT, ULA, unspecified, broadcast,
	// and multicast (SEC-0009: 100.64/10 and fc00::/7 host cloud metadata and must never be exempted by the bypass).
	stillForbidden := []string{
		"169.254.169.254", "169.254.0.1", "fe80::1", // metadata/link-local
		"100.100.100.200", "100.64.1.1", // CGNAT range (Alibaba Cloud metadata, etc.)
		"fd00:ec2::254", "fd20:ce::254", "fd00::1", // ULA range (AWS/GCP metadata, etc.)
		"0.0.0.0", "::", // unspecified
		"255.255.255.255",      // Broadcast
		"224.0.0.1", "ff02::1", // Multicast
		"::ffff:169.254.169.254", // 4-in-6 metadata
	}
	for _, c := range stillForbidden {
		if err := ValidateIP(mustIP(t, c)); !errors.Is(err, ErrAddressForbidden) {
			t.Errorf("bypass: ValidateIP(%s) = %v; want still forbidden", c, err)
		}
	}
	if err := ValidateHostForm("/var/run/pg.sock"); !errors.Is(err, ErrUnixSocket) {
		t.Errorf("bypass: unix socket = %v; want still ErrUnixSocket", err)
	}
	if err := controlGuard("unix", "/tmp/db.sock", nil); !errors.Is(err, ErrUnixSocket) {
		t.Errorf("bypass: controlGuard(unix) = %v; want still ErrUnixSocket", err)
	}
	if _, err := DialFunc(time.Second)(context.Background(), "tcp", "/tmp/db.sock"); !errors.Is(err, ErrUnixSocket) {
		t.Errorf("bypass: DialFunc(path) = %v; want still ErrUnixSocket", err)
	}
	if err := controlGuard("tcp", "169.254.169.254:80", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("bypass: controlGuard(metadata) = %v; want still forbidden", err)
	}
	// Mixed resolution: valid private IP + metadata is still rejected as a whole (bypass only exempts loopback).
	if err := ValidateHost(context.Background(), "rebind.example", fakeResolver(mustIP(t, "10.0.0.5"), mustIP(t, "169.254.169.254"))); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("bypass: mixed resolution = %v; want forbidden", err)
	}

	// Bypass disabled: loopback rejection is restored within the same process.
	restore()
	bypassOff(t)
	for _, c := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1"} {
		if err := ValidateIP(mustIP(t, c)); !errors.Is(err, ErrAddressForbidden) {
			t.Errorf("no-bypass: ValidateIP(%s) = %v; want forbidden", c, err)
		}
	}
	if err := ValidateHostForm("127.0.0.1"); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("no-bypass: ValidateHostForm(127.0.0.1) = %v; want forbidden", err)
	}
	if err := controlGuard("tcp", "127.0.0.1:5432", nil); !errors.Is(err, ErrAddressForbidden) {
		t.Errorf("no-bypass: controlGuard(127.0.0.1) = %v; want forbidden", err)
	}
}

// TestLoopbackBypassEnvParsing verifies environment variable parsing: only "1" or "true" (case- and
// whitespace-tolerant) enables the bypass, while all other values (empty, 0, false, arbitrary strings) do not;
// the parsed result is frozen in-process via sync.Once on first read.
func TestLoopbackBypassEnvParsing(t *testing.T) {
	// This test directly exercises the env-derived surface: clear the test override and reset the sync.Once cache.
	prevOverride := bypassTestOverride.Load()
	bypassTestOverride.Store(nil)
	t.Cleanup(func() {
		bypassTestOverride.Store(prevOverride)
		loopbackBypassOnce, loopbackBypassEnv = sync.Once{}, false
	})
	resetEnvParse := func() { loopbackBypassOnce, loopbackBypassEnv = sync.Once{}, false }

	enabled := []string{"1", "true", "TRUE", " True ", "1 "}
	disabled := []string{"", "0", "false", "yes", "on", "2", "garbage"}
	for _, v := range enabled {
		t.Setenv(LoopbackBypassEnv, v)
		resetEnvParse()
		if !loopbackBypassEnabled() {
			t.Errorf("%q must enable bypass", v)
		}
	}
	for _, v := range disabled {
		t.Setenv(LoopbackBypassEnv, v)
		resetEnvParse()
		if loopbackBypassEnabled() {
			t.Errorf("%q must not enable bypass", v)
		}
	}
	// Frozen semantics: subsequent environment changes after the first read do not alter the cached result.
	t.Setenv(LoopbackBypassEnv, "1")
	resetEnvParse()
	if !loopbackBypassEnabled() {
		t.Fatal("env=1 must enable bypass on first read")
	}
	t.Setenv(LoopbackBypassEnv, "")
	if !loopbackBypassEnabled() {
		t.Error("bypass must be latched after first read")
	}
}
