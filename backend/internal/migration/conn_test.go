package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dsnTestCA is a one-off self-signed placeholder certificate (used solely so the pgx parser completes
// sslrootcert loading; its private key was discarded and it has no security value).
const dsnTestCA = `-----BEGIN CERTIFICATE-----
MIIDDTCCAfWgAwIBAgIUL/AFftlOU/hAena3R5x1Mvln3Y4wDQYJKoZIhvcNAQEL
BQAwFjEUMBIGA1UEAwwLZHNuLXRlc3QtY2EwHhcNMjYxMDIxMTIwNDE0WhcNMjYx
MDIyMTIwNDE0WjAWMRQwEgYDVQQDDAtkc24tdGVzdC1jYTCCASIwDQYJKoZIhvcN
AQEBBQADggEPADCCAQoCggEBAOSE+6YZnTphTu1DaqNbs01LPN0GJVBeRiHom/D3
4Pwt/6S3RaAk1sCBc+Vn3Lm426Q8rUkpBcpzwkhULbtULR0o5nVaplPVArf4UwtX
N60qjMkeoAwGvIHObvtoMuyEAE56uAaNvlnsSRYnrn4dVf0dusjRl0tSHuwDjF75
sMEBfcfpitrWZS+KnxN9wundS1r5xAjk752wnJ9e3bHm+xo9XGW1sH9tIKHgaGGq
8RytVCG3PptI5BC6grvyz6JRgcuhkho5zhkJhaASX+1xptjKujG0ODD3+R8/gaXn
sBbiQn5q/S1VpmC5adSABh1mZjaIfhT9VJDw/LqewxHm0FMCAwEAAaNTMFEwHQYD
VR0OBBYEFGTUrZ+xxFt79KOX8FLCeFTxgERqMB8GA1UdIwQYMBaAFGTUrZ+xxFt7
9KOX8FLCeFTxgERqMA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZIhvcNAQELBQADggEB
ANwRz7M4ssNxVxhb8wKg2uld4Cd1wTLEvwP9wRXcZ/XpzfwruwAm4aiv/FgMJ5+t
dHulLJMrbbQovco6vWk9fBL1xiURUTBv42NHQj3UdZkn1htJvVDlQT9tCW3TXmQI
AsFNf3Ru+s1vRxyhedYWSFzepjQKqWfZFoWKNDyGae6CzXcGwebnFxjVgPQxaOIt
yJAuUlvRLZGihifCpqBIKmlIOoiBmZfeubgI8RTElTEfm8fXh6Ly75pb11v4KXtD
okT0n1McMG2rECZOIvh0VDTfB5JgsP3dsoPvtH/dw0Ndk3axzpwul+XONXODm1d9
aonN4mNwbg7FgN/6pqYLHU8=
-----END CERTIFICATE-----`

// TestTargetDSNAdversarialCredentials SEC-0045 regression: when target DB credentials contain
// spaces, keywords, quotes, or backslashes, the DSN parsed by pgx must match the endpoint fields exactly.
func TestTargetDSNAdversarialCredentials(t *testing.T) {
	// pgx reads and loads the CA file during DSN parsing: write the placeholder certificate to a temp file.
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, []byte(dsnTestCA), 0o600); err != nil {
		t.Fatal(err)
	}
	passwords := []string{
		"abc host=attacker.example",
		"p ' or host=evil",
		"back\\slash dbname=pwned",
		"space password",
	}
	for _, pw := range passwords {
		ep := TargetEndpoint{
			Host: "pg.target.internal", Port: 5432, Database: "yearning_v4",
			Username: "yearning_migrator", Password: pw, TLSMode: "verify_full",
			CAFile: &caPath,
		}
		dsn, err := ep.TargetDSN()
		if err != nil {
			t.Fatalf("password %q: TargetDSN failed: %v", pw, err)
		}
		parsed, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("password %q: DSN parse failed: %v", pw, err)
		}
		if parsed.ConnConfig.Host != "pg.target.internal" ||
			parsed.ConnConfig.User != "yearning_migrator" ||
			parsed.ConnConfig.Password != pw ||
			parsed.ConnConfig.Database != "yearning_v4" {
			t.Fatalf("password %q rewrote connection semantics: host=%q user=%q db=%q",
				pw, parsed.ConnConfig.Host, parsed.ConnConfig.User, parsed.ConnConfig.Database)
		}
		if parsed.ConnConfig.Port != 5432 {
			t.Fatalf("password %q rewrote port: %d", pw, parsed.ConnConfig.Port)
		}
		if parsed.ConnConfig.TLSConfig == nil || parsed.ConnConfig.TLSConfig.InsecureSkipVerify {
			t.Fatalf("password %q downgraded TLS", pw)
		}
	}
}

// TestTargetDSNTLSModeMapping verifies that TLS mode accepts only the contracted enum values.
func TestTargetDSNTLSModeMapping(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"verify_ca", "verify-ca"},
		{"verify_full", "verify-full"},
	} {
		ep := TargetEndpoint{Host: "h", Port: 5432, Database: "d", Username: "u", Password: "p", TLSMode: tc.mode}
		dsn, err := ep.TargetDSN()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dsn, "sslmode="+tc.want) {
			t.Fatalf("mode %s: dsn %q missing sslmode=%s", tc.mode, dsn, tc.want)
		}
	}
	if _, err := (TargetEndpoint{TLSMode: "disable"}).TargetDSN(); err == nil {
		t.Fatal("tls mode disable must be rejected")
	}
}
