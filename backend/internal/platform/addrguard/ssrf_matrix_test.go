// ssrf_matrix_test.go tests hardcoded SSRF denial categories under intranet-first posture:
// reserved IPv4/IPv6 ranges, recursive NAT64 embedded IPv4 checks, zoned/bracketed IPv6 link-local literals,
// and alternate IPv4 address encodings dialed through GuardedDialer / DialFuncFor.
package addrguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestReservedRangesHardDenied(t *testing.T) {
	bypassOff(t)
	// Enable loopback datasource toggle to prove reserved/deprecated ranges remain denied for every purpose.
	t.Cleanup(SetAllowLoopbackDatasourceForTest(true))
	purposes := []Purpose{PurposeGeneric, PurposeDatasource, PurposeOSC, PurposeWebhook, PurposeReleaseFeed}
	reserved := []string{
		"0.1.2.3",          // 0.0.0.0/8 this-host network
		"192.0.0.1",        // 192.0.0.0/24 IETF protocol assignments
		"198.18.0.1",       // 198.18.0.0/15 benchmarking
		"240.0.0.1",        // 240.0.0.0/4 future use / reserved
		"::a9fe:a9fe",      // ::/96 deprecated IPv4-compatible IPv6
		"100::1",           // 100::/64 discard-only
		"fec0::1",          // fec0::/10 deprecated site-local
		"2002:a9fe:a9fe::", // 2002::/16 6to4
		"2001::1",          // 2001::/32 Teredo
		"64:ff9b:1::1",     // 64:ff9b:1::/48 local-use NAT64
	}
	for _, p := range purposes {
		for _, ipStr := range reserved {
			err := ValidateIPFor(p, mustIP(t, ipStr))
			if !errors.Is(err, ErrReservedAddressForbidden) || !errors.Is(err, ErrAddressForbidden) {
				t.Errorf("purpose=%s ValidateIPFor(%s) = %v; want ErrReservedAddressForbidden", p, ipStr, err)
			}
		}
	}
}

func TestNAT64RecursiveEvaluation(t *testing.T) {
	bypassOff(t)
	cases := []struct {
		name string
		ip   string
		want error // nil means allowed
	}{
		{"nat64 aws/azure imds 169.254.169.254", "64:ff9b::a9fe:a9fe", ErrCloudMetadataForbidden},
		{"nat64 aliyun metadata 100.100.100.200", "64:ff9b::6464:64c8", ErrCloudMetadataForbidden},
		{"nat64 loopback 127.0.0.1", "64:ff9b::7f00:0001", ErrLoopbackForbidden},
		{"nat64 reserved 240.0.0.1", "64:ff9b::f000:0001", ErrReservedAddressForbidden},
		{"nat64 rfc1918 10.0.0.5 allowed", "64:ff9b::0a00:0005", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIPFor(PurposeGeneric, mustIP(t, tc.ip))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("ValidateIPFor(%s) = %v; want nil", tc.ip, err)
				}
				return
			}
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrAddressForbidden) {
				t.Fatalf("ValidateIPFor(%s) = %v; want %v", tc.ip, err, tc.want)
			}
		})
	}
}

func TestZonedAndBracketedHostLiteralsRejectedAtPreflightAndDial(t *testing.T) {
	bypassOff(t)
	for _, path := range []string{"/var/run/docker.sock", " /var/run/docker.sock "} {
		if err := ValidateHostFormFor(PurposeWebhook, path); !errors.Is(err, ErrUnixSocket) {
			t.Errorf("ValidateHostFormFor(%q) = %v; want ErrUnixSocket", path, err)
		}
	}
	for _, host := range []string{"fe80::1%eth0", "[fe80::1%eth0]", "[169.254.169.254]", "[::1]", "::ffff:127.0.0.1"} {
		err := ValidateHostFormFor(PurposeWebhook, host)
		if !errors.Is(err, ErrAddressForbidden) {
			t.Errorf("ValidateHostFormFor(%q) = %v; want ErrAddressForbidden", host, err)
		}
	}
	if err := ValidateHostFormFor(PurposeWebhook, "fe80::1%eth0"); !errors.Is(err, ErrCloudMetadataForbidden) {
		t.Errorf("ValidateHostFormFor(zoned link-local) = %v; want ErrCloudMetadataForbidden", err)
	}
	if err := controlGuardFor(PurposeWebhook, "")("tcp", "[fe80::1%eth0]:5432", nil); !errors.Is(err, ErrCloudMetadataForbidden) {
		t.Errorf("controlGuard([fe80::1%%eth0]:5432) = %v; want ErrCloudMetadataForbidden", err)
	}
}

func TestAlternateIPv4FormsBlockedByDialer(t *testing.T) {
	bypassOff(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepts atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = c.Close()
		}
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	dial := DialFuncFor(PurposeWebhook, 500*time.Millisecond)
	for _, host := range []string{"127.1", "0x7f.1", "2130706433", "localhost"} {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		conn, derr := dial(ctx, "tcp", net.JoinHostPort(host, port))
		cancel()
		if derr == nil {
			_ = conn.Close()
			t.Fatalf("dial to %s:%s succeeded; want failure", host, port)
		}
	}
	if got := accepts.Load(); got != 0 {
		t.Fatalf("decoy loopback listener accepted %d connections; want 0", got)
	}
	// Sanity check HTTP ErrUseLastResponse exported sentinel behavior for RedirectPolicy(0, false).
	policy := RedirectPolicy(0, false)
	if err := policy(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("RedirectPolicy(0, false) = %v; want ErrUseLastResponse", err)
	}
}
