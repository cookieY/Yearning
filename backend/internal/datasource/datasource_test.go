package datasource

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
)

func TestResolveCapabilitiesMatrix(t *testing.T) {
	cases := []struct {
		engine, mode, version string
		wantTarget            string
		wantErr               bool
	}{
		{"mysql", "mysql", "5.7.44-log", "mysql-5.7", false},
		{"mysql", "mysql", "8.0.36", "mysql-8.0", false},
		{"mysql", "mysql", "9.1.0", "", true},
		{"postgresql", "postgresql", "14.11", "postgresql-14", false},
		{"postgresql", "postgresql", "17.2", "postgresql-17", false},
		{"postgresql", "postgresql", "13.3", "", true},
		{"tidb", "mysql", "8.0.11-TiDB-v7.5.1", "tidb-mysql", false},
		{"oceanbase", "mysql", "5.7.65-OceanBase", "oceanbase-mysql", false},
		{"polardb", "mysql", "8.0.2", "polardb-mysql", false},
		{"polardb", "postgresql", "15.4", "polardb-postgresql", false},
		{"mysql", "postgresql", "8.0.1", "", true}, // Engine and compatibility mode mismatch
	}
	for _, tc := range cases {
		target, caps, err := ResolveCapabilities(tc.engine, tc.mode, tc.version)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s/%s %s: expected unsupported", tc.engine, tc.mode, tc.version)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s/%s %s: %v", tc.engine, tc.mode, tc.version, err)
		}
		if target != tc.wantTarget {
			t.Fatalf("target = %s want %s", target, tc.wantTarget)
		}
		if !caps[CapConnectionTest] || !caps[CapDmlExecution] {
			t.Fatalf("required capability missing for %s", target)
		}
		if tc.engine == "mysql" && target != "mysql-5.7" && !caps[CapGhOst] {
			t.Fatalf("mysql 8.0 must advertise gh_ost")
		}
		if tc.engine == "postgresql" && caps[CapGhOst] {
			t.Fatalf("postgresql must not advertise gh_ost")
		}
	}
}

func TestEngineModeCombinations(t *testing.T) {
	valid := map[string]bool{}
	for _, tgt := range CapabilityMatrix {
		key := tgt.Engine + "/" + tgt.Mode
		valid[key] = true
	}
	for _, combo := range []string{"mysql/mysql", "tidb/mysql", "oceanbase/mysql", "postgresql/postgresql", "polardb/mysql", "polardb/postgresql"} {
		if !valid[combo] {
			t.Fatalf("engine/mode %s missing from matrix", combo)
		}
	}
	if engineModeOK("mysql", "postgresql") {
		t.Fatal("mysql/postgresql combo accepted")
	}
	if engineModeOK("postgresql", "mysql") {
		t.Fatal("postgresql/mysql combo accepted")
	}
}

func TestConstraintMatches(t *testing.T) {
	if !ConstraintMatches("5.7.x", "5.7.44") || ConstraintMatches("5.7.x", "5.6.50") {
		t.Fatal("5.7.x matching wrong")
	}
	if !ConstraintMatches("latest_stable_at_release", "anything-1.2") {
		t.Fatal("cloud constraint must accept latest stable")
	}
}

// TestOpenTLSMaterialCertClassification verifies the PEM classification heuristic:
// with private key => the last certificate block is the client certificate and remaining blocks enter the CA chain;
// without private key => all certificate blocks enter the CA chain; client certificates are never mixed into RootCAs.
func TestOpenTLSMaterialCertClassification(t *testing.T) {
	f := &Factory{}
	makePEM := func(blocks ...string) []byte {
		var out []byte
		for _, b := range blocks {
			out = append(out, []byte(b)...)
		}
		return out
	}
	m := &DatasourceTLSMaterialModel{VerifyServer: true}
	keyPEM := "-----BEGIN EC PRIVATE KEY-----\nZm9v\n-----END EC PRIVATE KEY-----\n"
	cert1 := "-----BEGIN CERTIFICATE-----\nY2VydDE=\n-----END CERTIFICATE-----\n"
	cert2 := "-----BEGIN CERTIFICATE-----\nY2VydDI=\n-----END CERTIFICATE-----\n"

	// With private key + two certs: cert1 -> CA chain, cert2 -> client certificate.
	m.MaterialCiphertext = makePEM(cert1, cert2, keyPEM)
	mat, err := f.openTLSMaterialForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(mat.CertPEM) != cert2 {
		t.Fatalf("client certificate must be the last certificate block, got %q", mat.CertPEM)
	}
	if string(mat.CAPEM) != cert1 {
		t.Fatalf("CA chain must not contain client certificate, got %q", mat.CAPEM)
	}
	if string(mat.KeyPEM) != keyPEM {
		t.Fatal("private key parse error")
	}

	// Single certificate + private key (pure mTLS client material): cert is client certificate, CA chain is empty.
	m.MaterialCiphertext = makePEM(cert1, keyPEM)
	mat, err = f.openTLSMaterialForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(mat.CertPEM) != cert1 {
		t.Fatalf("with single certificate + private key, certificate must be client certificate, got CAPEM=%q", mat.CAPEM)
	}
	if mat.CAPEM != nil {
		t.Fatalf("pure mTLS material must not be mixed into RootCAs, got %q", mat.CAPEM)
	}

	// Without private key + two certs: all go into CA chain, no client certificate.
	m.MaterialCiphertext = makePEM(cert1, cert2)
	mat, err = f.openTLSMaterialForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(mat.CAPEM) != cert1+cert2 {
		t.Fatalf("without private key, all certificates must be in CA chain, got %q", mat.CAPEM)
	}
	if mat.CertPEM != nil {
		t.Fatalf("without private key, no client certificate must be produced, got %q", mat.CertPEM)
	}
}

func TestCapabilitiesServesFreshCachedProbeWithinRateLimitWindow(t *testing.T) {
	svc := &Service{}
	dsID := "0198d9cc-0000-7000-8000-000000000099"
	now := time.Now().UTC()
	svc.probeMu.Store(dsID, now)
	svc.lastCaps.Store(dsID, cachedCapabilities{
		purpose: "review",
		view: CapabilitiesView{
			DatasourceID:        dsID,
			DetectedVersion:     "8.0.46",
			IdentityFingerprint: "fp-1",
			Capabilities:        map[string]bool{"query": true, "gh_ost": true},
			CheckedAt:           now,
		},
		cachedAt: time.Now(),
	})

	got, err := svc.Capabilities(context.Background(), dsID, "review")
	if err != nil {
		t.Fatalf("Capabilities returned unexpected error: %v", err)
	}
	if got.DetectedVersion != "8.0.46" || !got.Capabilities["gh_ost"] {
		t.Fatalf("unexpected cached capabilities: %+v", got)
	}
	// Mutating the returned map must not corrupt the cached entry.
	got.Capabilities["gh_ost"] = false
	gotAgain, err := svc.Capabilities(context.Background(), dsID, "review")
	if err != nil {
		t.Fatalf("second Capabilities call failed: %v", err)
	}
	if !gotAgain.Capabilities["gh_ost"] {
		t.Fatal("cached capabilities map was mutated by caller")
	}

	// Different purpose misses the cache and hits the active rate limit instead of silently reusing review credentials.
	if _, err := svc.Capabilities(context.Background(), dsID, "execution"); !errors.Is(err, ErrTestRateLimited) {
		t.Fatalf("mismatched purpose error = %v, want ErrTestRateLimited", err)
	}
	if _, err := svc.Capabilities(context.Background(), dsID, "invalid"); !errors.Is(err, bizerr.ErrValidationFailed) {
		t.Fatalf("invalid purpose error = %v, want ErrValidationFailed", err)
	}
}
