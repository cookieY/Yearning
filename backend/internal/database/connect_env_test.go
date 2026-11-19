// connect_env_test.go SEC-0057 acceptance: process PG* environment variables (PGSSLMODE/PGHOST/
// PGDATABASE/PGUSER/PGPASSWORD/PGAPPNAME/PGOPTIONS/PGTZ/PGSSLCERT, etc.)
// no longer affect connection configuration — all connection parameters are constructed explicitly.
// Also covers SEC-0009: connection construction rejects forbidden addresses and Unix socket forms.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// TestPostgresConfigIgnoresPGEnv After injecting the full set of PG* variables into the process,
// the constructed config is still determined solely by input arguments (host/port/user/password/database/application_name/TLS).
func TestPostgresConfigIgnoresPGEnv(t *testing.T) {
	t.Setenv("PGHOST", "env-evil.internal")
	t.Setenv("PGPORT", "6543")
	t.Setenv("PGDATABASE", "env_db")
	t.Setenv("PGUSER", "env_user")
	t.Setenv("PGPASSWORD", "env_pass")
	t.Setenv("PGAPPNAME", "env_app")
	t.Setenv("PGOPTIONS", "-c statement_timeout=0")
	t.Setenv("PGTZ", "Foo/Bar")
	t.Setenv("PGCONNECT_TIMEOUT", "99")
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGSSLROOTCERT", "/nonexistent/root.crt")
	t.Setenv("PGSSLCERT", "/nonexistent/client.crt")
	t.Setenv("PGSSLKEY", "/nonexistent/client.key")
	t.Setenv("PGTARGETSESSIONATTRS", "primary")

	ep := Endpoint{Host: "db-prod.internal", Port: 5432, DatabaseName: "appdb"}
	cred := Credential{Username: "svc", Password: "secret"}

	cfg, err := BuildPostgresConfig(context.Background(), ep, cred, nil, 0)
	if err != nil {
		t.Fatalf("BuildPostgresConfig: %v", err)
	}
	if cfg.Host != ep.Host {
		t.Errorf("Host = %q; want %q (PGHOST leaked)", cfg.Host, ep.Host)
	}
	if cfg.Port != 5432 {
		t.Errorf("Port = %d; want 5432 (PGPORT leaked)", cfg.Port)
	}
	if cfg.User != cred.Username {
		t.Errorf("User = %q; want %q (PGUSER leaked)", cfg.User, cred.Username)
	}
	if cfg.Password != cred.Password {
		t.Errorf("Password leaked from PGPASSWORD")
	}
	if cfg.Database != ep.DatabaseName {
		t.Errorf("Database = %q; want %q (PGDATABASE leaked)", cfg.Database, ep.DatabaseName)
	}
	if cfg.RuntimeParams["application_name"] != ApplicationName {
		t.Errorf("application_name = %q; want %q (PGAPPNAME leaked)",
			cfg.RuntimeParams["application_name"], ApplicationName)
	}
	if _, ok := cfg.RuntimeParams["options"]; ok {
		t.Errorf("options = %q; PGOPTIONS leaked into runtime params", cfg.RuntimeParams["options"])
	}
	if _, ok := cfg.RuntimeParams["timezone"]; ok {
		t.Errorf("timezone = %q; PGTZ leaked into runtime params", cfg.RuntimeParams["timezone"])
	}
	// PGSSLMODE=disable must not downgrade the material-less path to plaintext-only failure;
	// deterministic policy: system root CA verified TLS first + plaintext fallback (cannot become disable plaintext-only due to env).
	if cfg.TLSConfig == nil {
		t.Fatal("TLSConfig = nil; PGSSLMODE=disable must not downgrade")
	}
	if cfg.TLSConfig.InsecureSkipVerify {
		t.Error("TLSConfig.InsecureSkipVerify must never be true")
	}
	if cfg.TLSConfig.ServerName != ep.Host {
		t.Errorf("TLS ServerName = %q; want %q", cfg.TLSConfig.ServerName, ep.Host)
	}
	if len(cfg.Fallbacks) != 1 || cfg.Fallbacks[0].Host != ep.Host {
		t.Errorf("Fallbacks must only be a plaintext fallback for the explicit endpoint (multi-host PGHOST leaked)")
	}
	// PGTARGETSESSIONATTRS residue must be explicitly cleared (pgx v5 materializes it into
	// a ValidateConnect hook that alters multi-host session selection — must be nil after injecting primary).
	if cfg.ValidateConnect != nil {
		t.Error("ValidateConnect must be nil (PGTARGETSESSIONATTRS leaked)")
	}
	// Guarded DialFunc must be installed (T05 dial-layer defense).
	if cfg.DialFunc == nil {
		t.Error("DialFunc must be guarded")
	}
}

// TestPostgresConfigVerifyFullEnvIsolated The material-bearing path is likewise isolated from the environment:
// verify-full is enforced, SNI = explicit host, and the environment cannot inject sslrootcert or downgrade sslmode.
func TestPostgresConfigVerifyFullEnvIsolated(t *testing.T) {
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGSSLROOTCERT", "/nonexistent/root.crt")
	ep := Endpoint{Host: "pg-cert.internal", Port: 5432}
	cred := Credential{Username: "u", Password: "p"}
	mat := &TLSMaterial{VerifyServer: true, CAPEM: nil}

	cfg, err := BuildPostgresConfig(context.Background(), ep, cred, mat, 0)
	if err != nil {
		t.Fatalf("BuildPostgresConfig: %v", err)
	}
	if cfg.TLSConfig == nil {
		t.Fatal("material path must force TLS")
	}
	if cfg.TLSConfig.InsecureSkipVerify {
		t.Error("material path must verify (verify-full)")
	}
	if cfg.TLSConfig.ServerName != ep.Host {
		t.Errorf("ServerName = %q; want %q", cfg.TLSConfig.ServerName, ep.Host)
	}
	if cfg.TLSConfig.RootCAs != nil {
		t.Error("PGSSLROOTCERT must not inject CAs")
	}
}

// TestPostgresConfigEmptyDatabaseExplicitExplicit Empty database name remains an explicit empty string
// (previously PGDATABASE would leak through).
func TestPostgresConfigEmptyDatabaseExplicit(t *testing.T) {
	t.Setenv("PGDATABASE", "env_db")
	cfg, err := BuildPostgresConfig(context.Background(),
		Endpoint{Host: "h.internal", Port: 5432}, Credential{}, nil, 0)
	if err != nil {
		t.Fatalf("BuildPostgresConfig: %v", err)
	}
	if cfg.Database != "" {
		t.Errorf("Database = %q; want empty", cfg.Database)
	}
}

// isAddrRejected checks for address guard rejection (including the Unix socket sentinel — both wrap
// into the ErrAddressForbidden semantic surface, with a dedicated sentinel for Unix sockets).
func isAddrRejected(err error) bool {
	return err != nil && (errors.Is(err, addrguard.ErrAddressForbidden) || errors.Is(err, addrguard.ErrUnixSocket))
}

// TestBuildConfigsRejectForbiddenTargets SEC-0009: connection construction directly rejects
// loopback/metadata/link-local/Unix socket forms (failing fast before dialing).
// Explicitly disable the development bypass: under integration test runs, this package's test process
// may carry YEARNING_ALLOW_LOOPBACK_TARGETS=1 (itest target PG is on loopback); pin the default
// rejection semantics per test without relying on the external environment.
func TestBuildConfigsRejectForbiddenTargets(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	addrguard.SetLoopbackBypassForTest(false)
	cred := Credential{Username: "u", Password: "p"}
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "::1", "fe80::1", "0.0.0.0", "/var/run/pg.sock"} {
		ep := Endpoint{Host: host, Port: 5432}
		if _, _, err := BuildMySQLConfig(ep, cred, nil, 0); !isAddrRejected(err) {
			t.Errorf("BuildMySQLConfig(%s) = %v; want address rejection", host, err)
		}
		if _, err := BuildPostgresConfig(context.Background(), ep, cred, nil, 0); !isAddrRejected(err) {
			t.Errorf("BuildPostgresConfig(%s) = %v; want address rejection", host, err)
		}
	}
	// Private network targets are allowed (standard internal databases are not treated as SSRF).
	if _, _, err := BuildMySQLConfig(Endpoint{Host: "10.1.2.3", Port: 3306}, cred, nil, 0); err != nil {
		t.Errorf("private mysql target rejected: %v", err)
	}
	if _, err := BuildPostgresConfig(context.Background(), Endpoint{Host: "10.1.2.3", Port: 5432}, cred, nil, 0); err != nil {
		t.Errorf("private pg target rejected: %v", err)
	}
	// Port range.
	if _, _, err := BuildMySQLConfig(Endpoint{Host: "10.1.2.3", Port: 0}, cred, nil, 0); !errors.Is(err, addrguard.ErrAddressForbidden) {
		t.Errorf("port 0 accepted")
	}
}

// TestMySQLConfigPinsSessionSemantics Connection contract pins sql_mode: lexical fingerprinting
// parses under a single backslash/double-quote semantics (sql-fingerprint.json). This pins
// that the configuration layer must send sql_mode to the session and set charset via the driver's
// SET NAMES rather than an invalid SET charset=...
func TestMySQLConfigPinsSessionSemantics(t *testing.T) {
	cfg, reg, err := BuildMySQLConfig(Endpoint{Host: "10.1.2.3", Port: 3306}, Credential{Username: "u", Password: "p"}, nil, 0)
	if err != nil {
		t.Fatalf("BuildMySQLConfig: %v", err)
	}
	if reg != nil {
		t.Fatalf("unexpected TLS registration for plaintext config")
	}
	want := "'" + MySQLSessionSQLMode + "'"
	if got := cfg.Params["sql_mode"]; got != want {
		t.Fatalf("sql_mode = %q; want %q", got, want)
	}
	if strings.Contains(MySQLSessionSQLMode, "NO_BACKSLASH_ESCAPES") || strings.Contains(MySQLSessionSQLMode, "ANSI_QUOTES") {
		t.Fatalf("session sql_mode must keep deterministic escape/quote semantics: %q", MySQLSessionSQLMode)
	}
	dsn := cfg.FormatDSN()
	if !strings.Contains(dsn, "charset=utf8mb4") {
		t.Fatalf("charset option missing from driver DSN: %q", dsn)
	}
	lowerDSN := strings.ToLower(dsn)
	if strings.Contains(lowerDSN, "set+charset") || strings.Contains(lowerDSN, "set%20charset") {
		t.Fatalf("charset leaked into Params as a SET: %q", dsn)
	}
}

// TestMySQLSessionSQLModeMatchesContract Prevents drift between the Go constant and machine contracts:
// database-adapter-contract.json and sql-fingerprint.json must match the exact sql_mode
// sent by BuildMySQLConfig.
func TestMySQLSessionSQLModeMatchesContract(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", "..", ".."))
	type adapterContract struct {
		PurposePools map[string]struct {
			SessionMutationAllowlist []string `json:"session_mutation_allowlist"`
		} `json:"purpose_pools"`
		SessionInitialization struct {
			MySQL struct {
				SQLMode string `json:"sql_mode"`
			} `json:"mysql"`
		} `json:"session_initialization"`
	}
	var adapter adapterContract
	readContract := func(rel string, dst any) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if err := json.Unmarshal(body, dst); err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
	}
	readContract("api/contracts/database-adapter-contract.json", &adapter)
	if adapter.SessionInitialization.MySQL.SQLMode != MySQLSessionSQLMode {
		t.Fatalf("adapter contract sql_mode = %q; Go constant = %q", adapter.SessionInitialization.MySQL.SQLMode, MySQLSessionSQLMode)
	}
	for purpose, pool := range adapter.PurposePools {
		allowed := false
		for _, name := range pool.SessionMutationAllowlist {
			if name == "sql_mode" {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Fatalf("purpose pool %q must allow session_initialization sql_mode", purpose)
		}
	}
	type fingerprintContract struct {
		SessionSemantics struct {
			MySQL struct {
				SQLMode string `json:"sql_mode"`
			} `json:"mysql"`
		} `json:"session_semantics"`
	}
	var fp fingerprintContract
	readContract("api/contracts/sql-fingerprint.json", &fp)
	if fp.SessionSemantics.MySQL.SQLMode != MySQLSessionSQLMode {
		t.Fatalf("fingerprint contract sql_mode = %q; Go constant = %q", fp.SessionSemantics.MySQL.SQLMode, MySQLSessionSQLMode)
	}
	// B2/M3: contract allowlist must match the Go allowed set bidirectionally, preventing silent drift
	// where "contract allows but code rejects" or vice versa.
	purposes := map[string]Purpose{"review": PurposeReview, "query": PurposeQuery, "execution": PurposeExecution}
	if len(adapter.PurposePools) != len(purposes) {
		t.Fatalf("contract purpose_pools = %d, Go purposes = %d", len(adapter.PurposePools), len(purposes))
	}
	for name, purpose := range purposes {
		pool, ok := adapter.PurposePools[name]
		if !ok {
			t.Fatalf("contract missing purpose pool %q", name)
		}
		want := map[string]bool{}
		for _, item := range pool.SessionMutationAllowlist {
			want[item] = true
		}
		got := sessionMutationAllowlist[purpose]
		if len(want) != len(got) {
			t.Fatalf("purpose %q allowlist contract=%v go=%v", name, want, got)
		}
		for item := range want {
			if !got[item] {
				t.Fatalf("purpose %q allowlist: contract allows %q, Go denies", name, item)
			}
		}
		for item := range got {
			if !want[item] {
				t.Fatalf("purpose %q allowlist: Go allows %q, contract denies", name, item)
			}
		}
	}
}
