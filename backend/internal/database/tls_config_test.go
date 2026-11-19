package database

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// Self-signed CA + server certificate fixture: proves BuildTLSConfig enforces full certificate chain + hostname verification by default.
// Valid CA + matching SNI -> handshake succeeds; wrong CA -> handshake fails; verify_server=false -> construction rejected.
// Closes legacy gap target_tls_contract_test (blocking_package=BE-B4-ADAPTER).

type tlsFixture struct {
	caPEM      []byte
	serverCert tls.Certificate
	listener   net.Listener
	addr       string
}

func startTLSFixture(t *testing.T, hostname string) *tlsFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "b4-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverCert := tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				// Close after completing server handshake; this fixture only verifies client handshake verification behavior.
				if tlsConn, ok := c.(*tls.Conn); ok {
					_ = tlsConn.SetDeadline(time.Now().Add(3 * time.Second))
					_ = tlsConn.Handshake()
				}
				_ = c.Close()
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })

	return &tlsFixture{
		caPEM:      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		serverCert: serverCert,
		listener:   ln,
		addr:       ln.Addr().String(),
	}
}

func TestTLSVerificationHandshake(t *testing.T) {
	f := startTLSFixture(t, "localhost")
	host, portStr, err := net.SplitHostPort(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = portStr

	// Positive case: trusted fixture CA + SNI=localhost -> handshake succeeds.
	goodCfg, err := BuildTLSConfig("localhost", &TLSMaterial{
		VerifyServer: true,
		CAPEM:        f.caPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	dialAndCheck(t, goodCfg, f.addr, host, true)

	// Counterexample 1: system root trust chain (does not trust fixture CA) -> must fail (proving verification is not skipped).
	systemCfg, err := BuildTLSConfig("localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	dialAndCheck(t, systemCfg, f.addr, host, false)

	// Counterexample 2: trusted CA but hostname mismatch -> must fail.
	wrongNameCfg, err := BuildTLSConfig("other.internal", &TLSMaterial{
		VerifyServer: true,
		CAPEM:        f.caPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	dialAndCheck(t, wrongNameCfg, f.addr, host, false)
}

func TestTLSVerifyServerCannotBeDisabled(t *testing.T) {
	if _, err := BuildTLSConfig("localhost", &TLSMaterial{VerifyServer: false}); err == nil {
		t.Fatal("verify_server=false must be rejected")
	}
}

func dialAndCheck(t *testing.T, cfg *tls.Config, addr, host string, wantOK bool) {
	t.Helper()
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, cfg)
	if err != nil {
		if wantOK {
			t.Fatalf("expected handshake to succeed but failed: %v", err)
		}
		return
	}
	defer func() { _ = conn.Close() }()
	if !wantOK {
		_ = conn.HandshakeContext(context.Background())
		t.Fatal("expected handshake to fail but succeeded (verification was skipped)")
	}
}
