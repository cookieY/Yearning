// ldap_guard_test.go unit tests for the LDAP egress guard (SEC-0009): ldapDial dials through
// addrguard GuardedDialer so forbidden targets (loopback/link-local/cloud metadata/CGNAT/ULA)
// fail before connect without relying on target reachability; public/RFC1918 control cases are
// tested directly via addrguard (the same ValidateIP gate invoked by the Control hook).
package identity

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// guardedLDAPCfg returns a valid LDAP configuration (passing Validate) with a replaceable host.
func guardedLDAPCfg(host string, transport string) *LDAPConfiguration {
	return &LDAPConfiguration{
		Host: host, Port: 636, Transport: transport,
		ServerName: "ldap.example.com", BaseDN: "dc=example,dc=com",
		UserFilter: "(uid={username})", UsernameAttribute: "uid",
		DisplayNameAttribute: "cn",
		ConnectTimeoutMS:     200, BindTimeoutMS: 200, SearchTimeoutMS: 200,
	}
}

// TestLDAPDialGuardRejectsForbiddenTargets verifies the guard deny surface: guarded dialing
// rejects forbidden targets before connect (with no network dependency) across both ldaps and starttls transports.
func TestLDAPDialGuardRejectsForbiddenTargets(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	addrguard.SetLoopbackBypassForTest(false)

	forbiddenHosts := []string{
		"127.0.0.1",       // loopback
		"169.254.169.254", // Cloud metadata (link-local)
		"100.100.100.200", // Alibaba Cloud metadata (CGNAT range)
		"fd00:ec2::254",   // AWS IMDSv6 (ULA range)
	}
	for _, host := range forbiddenHosts {
		for _, transport := range []string{"ldaps", "starttls"} {
			start := time.Now()
			conn, err := ldapDial(guardedLDAPCfg(host, transport))
			if err == nil {
				_ = conn.Close()
				t.Errorf("ldapDial(%s/%s) must be rejected by guard", host, transport)
				continue
			}
			if time.Since(start) > 3*time.Second {
				t.Errorf("ldapDial(%s/%s) took %v; guard rejection must be immediate", host, transport, time.Since(start))
			}
		}
	}
	// External error text converges to a bounded failure message: guard rejection details never leak to auth or connectivity test surfaces.
	if err := LDAPConnectionTest(guardedLDAPCfg("169.254.169.254", "ldaps"), "bind-secret"); err == nil ||
		err.Error() != "identity: ldap connect failed" {
		t.Errorf("LDAPConnectionTest guard rejection = %v; want bounded connect-failed message", err)
	}
	if _, err := LDAPAuthenticate(guardedLDAPCfg("100.100.100.200", "ldaps"), "bind-secret", "u", "p"); err == nil ||
		err.Error() != "identity: ldap connect failed" {
		t.Errorf("LDAPAuthenticate guard rejection = %v; want bounded connect-failed message", err)
	}
}

// TestLDAPGuardAllowsRFC1918AndPublic verifies the control surface (direct addrguard test): the
// GuardedDialer Control hook in ldapDial calls addrguard.ValidateIP — RFC1918 private networks and
// public unicast addresses are allowed (enterprise domain controllers commonly reside on RFC1918 networks).
func TestLDAPGuardAllowsRFC1918AndPublic(t *testing.T) {
	for _, ip := range []string{
		"10.0.0.10", "172.16.5.5", "192.168.0.2", // RFC1918 (common for internal domain controllers)
		"8.8.8.8", "203.0.113.20", // Public unicast
	} {
		if err := addrguard.ValidateIP(net.ParseIP(ip)); err != nil {
			t.Errorf("ValidateIP(%s) = %v; want allowed", ip, err)
		}
	}
}

// TestLDAPDialConfigStillValidated verifies that guard wiring preserves config validation: invalid
// transports are still rejected at the entry of ldapDial (without triggering a dial).
func TestLDAPDialConfigStillValidated(t *testing.T) {
	cfg := guardedLDAPCfg("203.0.113.20", "plain")
	if _, err := ldapDial(cfg); err == nil || !strings.Contains(err.Error(), "transport") {
		t.Errorf("ldapDial(bad transport) = %v; want transport error", err)
	}
}
