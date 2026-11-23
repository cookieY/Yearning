// self_guard_test.go verifies SelfGuard enforcement across dynamic listener ports,
// metadata PostgreSQL instance/database isolation, NAT64-wrapped self targets, and K8s infrastructure endpoints.
package addrguard

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestSelfGuardRejectsDynamicListenerPortForAllPurposes(t *testing.T) {
	bypassOff(t)
	t.Cleanup(ResetSelfGuardForTest())
	t.Cleanup(SetAllowLoopbackDatasourceForTest(true))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepts atomic.Int32
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			accepts.Add(1)
			_ = c.Close()
		}
	}()

	RegisterSelfListener(ln.Addr().String())
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	// With allow_loopback_datasource=true and bypass off:
	// - PurposeDatasource / PurposeOSC pass ValidateIPFor on loopback and MUST be rejected specifically by SelfGuard (ErrSelfTargetForbidden).
	// - Non-datasource purposes are rejected on loopback by ErrLoopbackForbidden (wrapping ErrAddressForbidden) and
	//   rejected by ErrSelfTargetForbidden on any Pod/interface IP matching the Yearning listener port (and under dev bypass).
	t.Setenv("POD_IP", "10.20.30.40")
	RegisterSelfListener(ln.Addr().String())
	RegisterSelfListener("0.0.0.0:" + portStr)

	for _, p := range []Purpose{PurposeDatasource, PurposeOSC} {
		if err := CheckSelfTargetFor(p, mustIP(t, "127.0.0.1"), port, "biz"); !errors.Is(err, ErrSelfTargetForbidden) {
			t.Errorf("purpose=%s CheckSelfTargetFor(127.0.0.1) = %v; want ErrSelfTargetForbidden", p, err)
		}
		if err := ValidateEndpointFor(context.Background(), p, "127.0.0.1", port, "biz", nil); !errors.Is(err, ErrSelfTargetForbidden) {
			t.Errorf("purpose=%s ValidateEndpointFor(127.0.0.1) = %v; want ErrSelfTargetForbidden", p, err)
		}
		if _, err := ResolvePinned(context.Background(), p, "127.0.0.1", port, "biz", nil); !errors.Is(err, ErrSelfTargetForbidden) {
			t.Errorf("purpose=%s ResolvePinned(127.0.0.1) = %v; want ErrSelfTargetForbidden", p, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, derr := DialFuncForDatabase(p, "biz", time.Second)(ctx, "tcp", ln.Addr().String())
		cancel()
		if derr == nil {
			_ = conn.Close()
			t.Fatalf("purpose=%s dialed self listener successfully", p)
		}
		if !errors.Is(derr, ErrSelfTargetForbidden) {
			t.Errorf("purpose=%s DialFunc error = %v; want ErrSelfTargetForbidden", p, derr)
		}
	}

	allPurposes := []Purpose{PurposeGeneric, PurposeDatasource, PurposeOSC, PurposeWebhook, PurposeReleaseFeed}
	for _, p := range allPurposes {
		if err := ValidateEndpointFor(context.Background(), p, "10.20.30.40", port, "biz", nil); !errors.Is(err, ErrSelfTargetForbidden) {
			t.Errorf("purpose=%s ValidateEndpointFor(POD_IP) = %v; want ErrSelfTargetForbidden", p, err)
		}
		if _, err := ResolvePinned(context.Background(), p, "10.20.30.40", port, "biz", nil); !errors.Is(err, ErrSelfTargetForbidden) {
			t.Errorf("purpose=%s ResolvePinned(POD_IP) = %v; want ErrSelfTargetForbidden", p, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, derr := DialFuncForDatabase(p, "biz", time.Second)(ctx, "tcp", ln.Addr().String())
		cancel()
		if derr == nil {
			_ = conn.Close()
			t.Fatalf("purpose=%s dialed self listener successfully", p)
		}
		if !errors.Is(derr, ErrAddressForbidden) {
			t.Errorf("purpose=%s DialFunc(127.0.0.1) error = %v; want ErrAddressForbidden", p, derr)
		}
	}
	if got := accepts.Load(); got != 0 {
		t.Fatalf("self listener accepted %d connections; want 0", got)
	}
}

func TestSelfGuardWildcardListenAndMetadataDBIsolation(t *testing.T) {
	bypassOff(t)
	t.Cleanup(ResetSelfGuardForTest())

	// Wildcard listen protects both loopback and interface IPs on the bound port.
	RegisterSelfListener("0.0.0.0:18080")
	if err := CheckSelfTargetFor(PurposeDatasource, mustIP(t, "127.0.0.1"), 18080, "biz"); !errors.Is(err, ErrSelfTargetForbidden) {
		t.Fatalf("wildcard self listener CheckSelfTargetFor(127.0.0.1:18080) = %v; want ErrSelfTargetForbidden", err)
	}
	if err := CheckSelfTargetFor(PurposeDatasource, mustIP(t, "127.0.0.1"), 18081, "biz"); err != nil {
		t.Fatalf("unrelated port 18081 = %v; want allowed", err)
	}

	// Metadata PostgreSQL isolation: deny metadata DB across non-datasource and matching datasource DB;
	// allow distinct business database on same PG host for PurposeDatasource, and exempt PurposeRetentionDB.
	t.Cleanup(SetMetadataDBForTest([]net.IP{mustIP(t, "10.10.0.5")}, 5432, "yearning_meta"))
	for _, p := range []Purpose{PurposeGeneric, PurposeWebhook, PurposeAIProvider, PurposeOSC} {
		if err := CheckSelfTargetFor(p, mustIP(t, "10.10.0.5"), 5432, "biz_orders"); !errors.Is(err, ErrMetadataDatabaseForbidden) {
			t.Errorf("purpose=%s metadata PG host:port = %v; want ErrMetadataDatabaseForbidden", p, err)
		}
	}
	if err := CheckSelfTargetFor(PurposeDatasource, mustIP(t, "10.10.0.5"), 5432, "Yearning_Meta"); !errors.Is(err, ErrMetadataDatabaseForbidden) {
		t.Errorf("case-insensitive metadata DB = %v; want ErrMetadataDatabaseForbidden", err)
	}
	if err := CheckSelfTargetFor(PurposeDatasource, mustIP(t, "10.10.0.5"), 5432, ""); !errors.Is(err, ErrMetadataDatabaseForbidden) {
		t.Errorf("empty datasource DB at dial/check = %v; want ErrMetadataDatabaseForbidden", err)
	}
	// Instance-level datasource registration allows empty db at ValidateEndpointFor (enforced per database_name at connect).
	if err := ValidateEndpointFor(context.Background(), PurposeDatasource, "10.10.0.5", 5432, "", nil); err != nil {
		t.Errorf("instance-level ValidateEndpointFor = %v; want deferred allow", err)
	}
	if err := CheckSelfTargetFor(PurposeDatasource, mustIP(t, "10.10.0.5"), 5432, "biz_orders"); err != nil {
		t.Errorf("same-instance business DB = %v; want allowed", err)
	}
	if err := CheckSelfTargetFor(PurposeRetentionDB, mustIP(t, "10.10.0.5"), 5432, "yearning_meta"); err != nil {
		t.Errorf("PurposeRetentionDB = %v; want allowed", err)
	}
}

func TestSelfGuardClusterInfraAndNAT64Wrapping(t *testing.T) {
	bypassOff(t)
	t.Cleanup(ResetSelfGuardForTest())
	t.Cleanup(SetClusterInfraForTest(mustIP(t, "10.96.0.1"), 443, []net.IP{mustIP(t, "10.96.0.10")}))

	if err := CheckSelfTargetFor(PurposeWebhook, mustIP(t, "10.96.0.1"), 443, ""); !errors.Is(err, ErrClusterControlPlaneForbidden) {
		t.Errorf("k8s api = %v; want ErrClusterControlPlaneForbidden", err)
	}
	if err := CheckSelfTargetFor(PurposeWebhook, mustIP(t, "64:ff9b::0a60:0001"), 443, ""); !errors.Is(err, ErrClusterControlPlaneForbidden) {
		t.Errorf("nat64 k8s api = %v; want ErrClusterControlPlaneForbidden", err)
	}
	if err := CheckSelfTargetFor(PurposeWebhook, mustIP(t, "10.96.0.10"), 53, ""); !errors.Is(err, ErrClusterControlPlaneForbidden) {
		t.Errorf("dns nameserver = %v; want ErrClusterControlPlaneForbidden", err)
	}
	if err := CheckSelfTargetFor(PurposeDatasource, mustIP(t, "10.96.0.10"), 5432, "biz"); err != nil {
		t.Errorf("non-DNS port on nameserver IP = %v; want allowed", err)
	}
}
