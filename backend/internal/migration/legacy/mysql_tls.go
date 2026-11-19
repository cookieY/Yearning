package legacy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/go-sql-driver/mysql"
)

// buildMySQLTLS constructs the TLS configuration for legacy MySQL connections with the same semantics as the target:
// verify_full verifies both certificate chain and hostname, whereas verify_ca verifies the chain only.
func buildMySQLTLS(spec rawTLS) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: spec.ServerName}
	if spec.CAFile != nil && *spec.CAFile != "" {
		pem, err := os.ReadFile(*spec.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca file contains no certificates")
		}
		cfg.RootCAs = pool
	}
	if (spec.CertificateFile != nil && *spec.CertificateFile != "") || (spec.PrivateKeyFile != nil && *spec.PrivateKeyFile != "") {
		certPath, keyPath := "", ""
		if spec.CertificateFile != nil {
			certPath = *spec.CertificateFile
		}
		if spec.PrivateKeyFile != nil {
			keyPath = *spec.PrivateKeyFile
		}
		if certPath == "" || keyPath == "" {
			return nil, fmt.Errorf("tls client certificate and private key must be provided together")
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load tls client keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	switch spec.Mode {
	case "verify_full":
		cfg.InsecureSkipVerify = false
	case "verify_ca":
		roots := cfg.RootCAs
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyLegacyChain(rawCerts, roots)
		}
	default:
		return nil, fmt.Errorf("unsupported tls mode %q", spec.Mode)
	}
	return cfg, nil
}

func verifyLegacyChain(rawCerts [][]byte, roots *x509.CertPool) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("server presented no certificate")
	}
	certs := make([]*x509.Certificate, 0, len(rawCerts))
	for _, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("parse server certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
	for _, c := range certs[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return fmt.Errorf("verify server certificate chain: %w", err)
	}
	return nil
}

// registerMySQLTLS registers a TLS config with the mysql driver and returns the tls parameter name to append to the DSN.
func registerMySQLTLS(name string, cfg *tls.Config) error {
	if err := mysql.RegisterTLSConfig(name, cfg); err != nil {
		return fmt.Errorf("register mysql tls config: %w", err)
	}
	return nil
}

// normalizeMySQLValue normalizes database/sql scan values into a controlled type set:
// nil / int64 / integral float64 -> int64 / string / []byte -> string / time.Time.
func normalizeMySQLValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case int64:
		return t
	case uint64:
		if t <= 1<<63-1 {
			return int64(t)
		}
		return t
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	default:
		return v
	}
}
