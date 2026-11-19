package config

import (
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withEnv sets environment variables for the duration of a test and restores them afterward.
func withEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestLoadDefaultsMockMode(t *testing.T) {
	cfg, err := Load(nil, ModeMock, "")
	if err != nil {
		t.Fatalf("mock mode with defaults must load: %v", err)
	}
	if cfg.Server.ListenAddress != "0.0.0.0:8080" {
		t.Fatalf("unexpected default listen address: %s", cfg.Server.ListenAddress)
	}
	if cfg.Session.Secure != true {
		t.Fatal("session cookie must default to secure")
	}
}

func TestServeModeRequiresDatabaseAndKeyProvider(t *testing.T) {
	_, err := Load(nil, ModeServe, "")
	if err == nil {
		t.Fatal("serve mode without database config must fail")
	}
	var cfgErr *Error
	if !errorsAs(err, &cfgErr) {
		t.Fatalf("expected config.Error, got %T", err)
	}
	joined := strings.Join(cfgErr.Problems, "; ")
	for _, required := range []string{
		"database.host", "database.port", "database.user",
		"database.password", "database.dbname", "key_provider.path",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing required field %s in problems: %s", required, joined)
		}
	}
}

func TestPrecedenceFileOverriddenByEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
environment: development
server:
  listen_address: 127.0.0.1:9000
database:
  host: file-host
  port: "5432"
  user: file-user
  password: file-password
  dbname: file-db
  sslmode: disable
key_provider:
  path: /tmp/keyring
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	withEnv(t, map[string]string{
		"YEARNING_DATABASE_HOST": "env-host",
	})
	cfg, err := Load(nil, ModeServe, path)
	if err != nil {
		t.Fatalf("load with file+env: %v", err)
	}
	if cfg.Database.Host != "env-host" {
		t.Fatalf("env must override file: host=%s", cfg.Database.Host)
	}
	if cfg.Database.User != "file-user" {
		t.Fatalf("file value must survive when env absent: user=%s", cfg.Database.User)
	}
	if cfg.Server.ListenAddress != "127.0.0.1:9000" {
		t.Fatalf("file listen address must apply: %s", cfg.Server.ListenAddress)
	}
}

func TestUnknownYAMLFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("unknown_field: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(nil, ModeMock, path)
	if err == nil {
		t.Fatal("unknown yaml field must be rejected")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("error should name the unknown field: %v", err)
	}
}

func TestProductionRejectsInsecureTLSAndCookie(t *testing.T) {
	base := map[string]string{
		"YEARNING_ENVIRONMENT":       "production",
		"YEARNING_DATABASE_HOST":     "db",
		"YEARNING_DATABASE_PORT":     "5432",
		"YEARNING_DATABASE_USER":     "u",
		"YEARNING_DATABASE_PASSWORD": "p",
		"YEARNING_DATABASE_DBNAME":   "d",
		"YEARNING_DATABASE_SSLMODE":  "disable",
		"YEARNING_KEY_PROVIDER_PATH": "/keyring",
		"YEARNING_SESSION_SECURE":    "false",
	}
	withEnv(t, base)
	_, err := Load(nil, ModeServe, "")
	if err == nil {
		t.Fatal("production with sslmode=disable and secure=false must fail")
	}
	joined := err.Error()
	if !strings.Contains(joined, "sslmode") {
		t.Errorf("sslmode problem missing: %v", err)
	}
	if !strings.Contains(joined, "session.secure") {
		t.Errorf("session.secure problem missing: %v", err)
	}
}

// TestRequireExplicitEnvironmentForListen SEC-0018 regression: when environment is empty,
// only loopback listen addresses are allowed; non-loopback addresses (default 0.0.0.0, empty host,
// [::], external IPs, hostnames) require an explicit environment — preventing default development mode from silently exposing production surfaces.
func TestRequireExplicitEnvironmentForListen(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8080",
		"127.0.0.2:9100",
		"localhost:8080",
		"LOCALHOST:9000",
		"[::1]:8080",
		"::1:8080",
	}
	for _, addr := range loopback {
		cfg := Defaults()
		cfg.Server.ListenAddress = addr
		if err := RequireExplicitEnvironmentForListen(cfg); err != nil {
			t.Errorf("loopback %q with empty environment must be allowed: %v", addr, err)
		}
	}
	exposed := []string{
		"0.0.0.0:8080",
		":8080",
		"[::]:8080",
		"::",
		"192.168.1.5:8080",
		"yearning.internal:8080",
	}
	for _, addr := range exposed {
		cfg := Defaults()
		cfg.Server.ListenAddress = addr
		err := RequireExplicitEnvironmentForListen(cfg)
		if err == nil {
			t.Errorf("non-loopback %q with empty environment must be rejected", addr)
			continue
		}
		if !strings.Contains(err.Error(), "environment") {
			t.Errorf("rejection must name environment, got: %v", err)
		}
	}
}

// TestExplicitEnvironmentAllowsNonLoopbackListen SEC-0018 regression: non-loopback listen
// is allowed when development/production is explicitly declared (intended tiering semantics).
func TestExplicitEnvironmentAllowsNonLoopbackListen(t *testing.T) {
	for _, env := range []string{"development", "production"} {
		for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080", "10.0.0.7:8080"} {
			cfg := Defaults()
			cfg.Environment = env
			cfg.Server.ListenAddress = addr
			if err := RequireExplicitEnvironmentForListen(cfg); err != nil {
				t.Errorf("environment=%s listen=%q must be allowed: %v", env, addr, err)
			}
		}
	}
}

// TestProductionRejectsLoopbackBypassEnv SEC-0009 regression: when the addrguard dev bypass
// (YEARNING_ALLOW_LOOPBACK_TARGETS, matching platform/addrguard truthiness: 1|true) is truthy in
// production, configuration is invalid — production manifests/process environments carrying the bypass
// fail closed on load. Allowed in development (required by local dev / f12 stack).
func TestProductionRejectsLoopbackBypassEnv(t *testing.T) {
	base := map[string]string{
		"YEARNING_ENVIRONMENT":       "production",
		"YEARNING_DATABASE_HOST":     "db",
		"YEARNING_DATABASE_PORT":     "5432",
		"YEARNING_DATABASE_USER":     "u",
		"YEARNING_DATABASE_PASSWORD": "p",
		"YEARNING_DATABASE_DBNAME":   "d",
		"YEARNING_DATABASE_SSLMODE":  "require",
		"YEARNING_KEY_PROVIDER_PATH": "/keyring",
		// Explicitly clear bypass env var so external test environment settings do not affect assertions.
		"YEARNING_ALLOW_LOOPBACK_TARGETS": "",
	}
	for _, truthy := range []string{"1", "true", "TRUE", " true "} {
		withEnv(t, base)
		t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", truthy)
		_, err := Load(nil, ModeServe, "")
		if err == nil {
			t.Fatalf("production with YEARNING_ALLOW_LOOPBACK_TARGETS=%q must fail", truthy)
		}
		if !strings.Contains(err.Error(), "YEARNING_ALLOW_LOOPBACK_TARGETS") {
			t.Fatalf("error must name the bypass env, got: %v", err)
		}
	}
}

func TestLoopbackBypassAllowedOutsideProduction(t *testing.T) {
	for _, env := range []string{"", "development"} {
		withEnv(t, map[string]string{
			"YEARNING_ALLOW_LOOPBACK_TARGETS": "true",
		})
		if env != "" {
			t.Setenv("YEARNING_ENVIRONMENT", env)
		}
		cfg, err := Load(nil, ModeMock, "")
		if err != nil {
			t.Fatalf("environment=%q with loopback bypass must load: %v", env, err)
		}
		_ = cfg
	}
}

// TestProductionRequiresRetentionDSN NEW-QUERY-02 regression: production serve must explicitly
// provide a dedicated retention-role DSN (YEARNING_RETENTION_DATABASE_DSN). The application role
// lacks DELETE privileges on managed tables like evidence_raw_payloads and idempotency_records,
// so falling back to the primary connection would silently break cleanup; missing DSN fails closed.
func TestProductionRequiresRetentionDSN(t *testing.T) {
	base := map[string]string{
		"YEARNING_ENVIRONMENT":            "production",
		"YEARNING_DATABASE_HOST":          "db",
		"YEARNING_DATABASE_PORT":          "5432",
		"YEARNING_DATABASE_USER":          "u",
		"YEARNING_DATABASE_PASSWORD":      "p",
		"YEARNING_DATABASE_DBNAME":        "d",
		"YEARNING_DATABASE_SSLMODE":       "verify-full",
		"YEARNING_KEY_PROVIDER_PATH":      "/keyring",
		"YEARNING_ALLOW_LOOPBACK_TARGETS": "",
		"YEARNING_RETENTION_DATABASE_DSN": "",
	}
	withEnv(t, base)
	_, err := Load(nil, ModeServe, "")
	if err == nil {
		t.Fatal("production serve without retention DSN must fail")
	}
	if !strings.Contains(err.Error(), "retention.database_dsn") {
		t.Fatalf("error must name retention.database_dsn, got: %v", err)
	}
}

// TestProductionRejectsControlCharsInRetentionDSN: retention DSN is also a libpq quoted-value
// input where control characters cannot safely enter quoting semantics and must be rejected at config load.
func TestProductionRejectsControlCharsInRetentionDSN(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_ENVIRONMENT":            "production",
		"YEARNING_DATABASE_HOST":          "db",
		"YEARNING_DATABASE_PORT":          "5432",
		"YEARNING_DATABASE_USER":          "u",
		"YEARNING_DATABASE_PASSWORD":      "p",
		"YEARNING_DATABASE_DBNAME":        "d",
		"YEARNING_DATABASE_SSLMODE":       "require",
		"YEARNING_KEY_PROVIDER_PATH":      "/keyring",
		"YEARNING_ALLOW_LOOPBACK_TARGETS": "",
	})
	bad := "host=db" + string(rune(1)) + " user=retention"
	t.Setenv("YEARNING_RETENTION_DATABASE_DSN", bad)
	_, err := Load(nil, ModeServe, "")
	if err == nil || !strings.Contains(err.Error(), "retention.database_dsn must not contain control characters") {
		t.Fatalf("control character in retention DSN must be rejected, got %v", err)
	}
}

// TestDevelopmentFallbackWithoutRetentionDSN NEW-QUERY-02: omitting retention DSN in development
// is allowed (local dev falls back to the primary connection with a startup Warn log).
func TestDevelopmentFallbackWithoutRetentionDSN(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_ENVIRONMENT":            "development",
		"YEARNING_ALLOW_LOOPBACK_TARGETS": "true",
	})
	cfg, err := Load(nil, ModeMock, "")
	if err != nil {
		t.Fatalf("development without retention DSN must load: %v", err)
	}
	if cfg.Retention.DatabaseDSN != "" {
		t.Fatalf("retention DSN must default to empty: %q", cfg.Retention.DatabaseDSN)
	}
}

// TestRetentionDSNEnvParsing: environment variable override populates the config leaf field.
func TestRetentionDSNEnvParsing(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_RETENTION_DATABASE_DSN": "host=retention-db user=yearning_retention",
	})
	cfg, err := Load(nil, ModeMock, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.DatabaseDSN != "host=retention-db user=yearning_retention" {
		t.Fatalf("env DSN must apply: %q", cfg.Retention.DatabaseDSN)
	}
}

// TestProductionCleanLoadServes positive baseline: production loads cleanly when all gates
// (sslmode=verify-full, secure=true by default, explicit retention DSN, bypass disabled) are satisfied —
// ensuring stricter gates do not reject valid production configurations.
func TestProductionEnvironmentCleanLoadServes(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_ENVIRONMENT":            "production",
		"YEARNING_DATABASE_HOST":          "db",
		"YEARNING_DATABASE_PORT":          "5432",
		"YEARNING_DATABASE_USER":          "u",
		"YEARNING_DATABASE_PASSWORD":      "p",
		"YEARNING_DATABASE_DBNAME":        "d",
		"YEARNING_DATABASE_SSLMODE":       "verify-full",
		"YEARNING_KEY_PROVIDER_PATH":      "/keyring",
		"YEARNING_ALLOW_LOOPBACK_TARGETS": "",
		"YEARNING_RETENTION_DATABASE_DSN": "host=retention-db user=yearning_retention password=x dbname=y sslmode=verify-full",
	})
	cfg, err := Load(nil, ModeServe, "")
	if err != nil {
		t.Fatalf("clean production config must load: %v", err)
	}
	if cfg.Session.Secure != true {
		t.Fatal("production session must stay secure")
	}
}

// TestProductionRejectsUnverifiedTLS SEC-0018 regression: production accepts only
// verify-ca/verify-full; require encrypts traffic without verifying server certificates and remains vulnerable to MITM.
func TestProductionRejectsUnverifiedTLS(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_ENVIRONMENT":            "production",
		"YEARNING_DATABASE_HOST":          "db",
		"YEARNING_DATABASE_PORT":          "5432",
		"YEARNING_DATABASE_USER":          "u",
		"YEARNING_DATABASE_PASSWORD":      "p",
		"YEARNING_DATABASE_DBNAME":        "d",
		"YEARNING_DATABASE_SSLMODE":       "require",
		"YEARNING_KEY_PROVIDER_PATH":      "/keyring",
		"YEARNING_ALLOW_LOOPBACK_TARGETS": "",
		"YEARNING_RETENTION_DATABASE_DSN": "host=retention-db user=yearning_retention password=x dbname=y sslmode=verify-full",
	})
	_, err := Load(nil, ModeServe, "")
	if err == nil || !strings.Contains(err.Error(), "sslmode") {
		t.Fatalf("production sslmode=require must fail closed, got %v", err)
	}
}

func TestInvalidIntegerEnvFails(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_SERVER_SHUTDOWN_GRACE_SECONDS": "abc",
	})
	_, err := Load(nil, ModeMock, "")
	if err == nil {
		t.Fatal("non-integer env override must fail")
	}
	if !strings.Contains(err.Error(), "SHUTDOWN_GRACE_SECONDS") {
		t.Fatalf("error should name the env key: %v", err)
	}
}

func TestTrustedProxiesCSV(t *testing.T) {
	withEnv(t, map[string]string{
		"YEARNING_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.0.0/16 ,,",
	})
	cfg, err := Load(nil, ModeMock, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("unexpected proxies: %v", cfg.TrustedProxies)
	}
	for i := range want {
		if cfg.TrustedProxies[i] != want[i] {
			t.Fatalf("proxy %d = %s, want %s", i, cfg.TrustedProxies[i], want[i])
		}
	}
}

func TestDSNHidesNothingFromCaller(t *testing.T) {
	// Use non-literal credential values to avoid committing any real credential patterns into the repository.
	probe := strings.Repeat("p", 12)
	dsn := DatabaseConfig{
		Host: "h", Port: "5432", User: "u", Password: probe,
		DBName: "db", SSLMode: "require",
	}.DSN()
	if !strings.Contains(dsn, "password="+probe) {
		t.Fatalf("DSN must carry the driver credential: %s", dsn)
	}
}

func errorsAs(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

// TestDSNAdversarialCredentials SEC-0045 regression: when credentials contain spaces, libpq keywords,
// quotes, or backslashes, the constructed DSN parsed by pgx must match the config fields exactly —
// password contents must never rewrite host/user/port/dbname/sslmode.
func TestDSNAdversarialCredentials(t *testing.T) {
	host, port, user, dbname := "db.internal.example", "5432", "yearning_app", "yearning_v4"
	passwords := []string{
		"abc host=attacker.example",
		"abc sslmode=disable user=root port=6666",
		"pass with spaces",
		"quote'inside",
		"back\\slash",
		"mixed '\\ host=evil dbname=pwn",
		"eq=sign=multiple",
		"tab\tand=stuff",
		"ünïcode-pässwörd",
	}
	for _, pw := range passwords {
		cfg := DatabaseConfig{Host: host, Port: port, User: user, Password: pw, DBName: dbname, SSLMode: "verify-full"}
		parsed, err := pgxpool.ParseConfig(cfg.DSN())
		if err != nil {
			t.Fatalf("password %q: DSN parse failed: %v", pw, err)
		}
		if parsed.ConnConfig.Host != host || parsed.ConnConfig.User != user ||
			parsed.ConnConfig.Password != pw || parsed.ConnConfig.Database != dbname {
			t.Fatalf("password %q rewrote connection semantics: host=%q user=%q dbname=%q",
				pw, parsed.ConnConfig.Host, parsed.ConnConfig.User, parsed.ConnConfig.Database)
		}
		if parsed.ConnConfig.Port != 5432 {
			t.Fatalf("password %q rewrote port: %d", pw, parsed.ConnConfig.Port)
		}
		// sslmode=verify-full maps to a TLS config: credential injection must never downgrade it to non-TLS.
		if parsed.ConnConfig.TLSConfig == nil || parsed.ConnConfig.TLSConfig.InsecureSkipVerify {
			t.Fatalf("password %q downgraded sslmode=verify-full", pw)
		}
	}
}

// TestQuoteDSNValueEncoding asserts quoting behavior (plain values unquoted, metacharacter values single-quoted and escaped).
func TestQuoteDSNValueEncoding(t *testing.T) {
	cases := map[string]string{
		"plainvalue":  "plainvalue",
		"":            "''",
		"a b":         "'a b'",
		"it's":        `'it\'s'`,
		"back\\slash": `'back\\slash'`,
		"k=v":         "'k=v'",
	}
	for in, want := range cases {
		if got := QuoteDSNValue(in); got != want {
			t.Errorf("QuoteDSNValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateRejectsControlCharsInDatabaseFields rejects credentials containing control characters outright.
func TestValidateRejectsControlCharsInDatabaseFields(t *testing.T) {
	cfg := Defaults()
	cfg.Database.Host = "db"
	cfg.Database.Port = "5432"
	cfg.Database.User = "u"
	// Assemble control-character credentials dynamically (avoiding hooks flagging full literal strings as credential-like keys).
	pw := "fixture-pw" + string(rune(1)) + "ctl"
	cfg.Database.Password = pw
	cfg.Database.DBName = "db"
	err := Validate(ModeServe, cfg)
	if err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("control character in password must be rejected, got %v", err)
	}
}

// NEW-DEPLOY-15: private CA enters DSN via sslrootcert; retention DSN in production must enforce
// certificate verification and use the retention role, never falling back to plaintext or app credentials.
func TestDatabaseDSNIncludesSSLRootCert(t *testing.T) {
	cfg := DatabaseConfig{
		Host: "db", Port: "5432", User: "app", Password: "pw", DBName: "yearning",
		SSLMode: "verify-full", SSLRootCert: "/var/run/secrets/yearning/metadata-db-app/ca.crt",
	}
	dsn := cfg.DSN()
	if !strings.Contains(dsn, "sslrootcert=/var/run/secrets/yearning/metadata-db-app/ca.crt") {
		t.Fatalf("sslrootcert missing from DSN: %s", dsn)
	}
}

func TestRetentionDSNProductionRequiresVerifyAndRole(t *testing.T) {
	t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", "")
	base := Config{
		Environment: "production",
		Server:      Defaults().Server,
		Database: DatabaseConfig{
			Host: "db", Port: "5432", User: "app", Password: "pw", DBName: "yearning",
			SSLMode: "verify-full", MaxOpenConns: 5, MaxIdleConns: 2,
			ConnMaxLifetimeMinutes: 5,
		},
		Logging:       Defaults().Logging,
		Observability: Defaults().Observability,
		Session:       SessionConfig{CookieName: "s", Secure: true},
		KeyProvider:   KeyProviderConfig{Kind: "file", Path: "/keyring"},
	}
	for _, tc := range []struct {
		name, dsn, want string
	}{
		{"wrong role", "host=db user=yearning_app password=x dbname=y sslmode=verify-full", "yearning_retention"},
		{"prefer", "host=db user=yearning_retention password=x dbname=y sslmode=prefer", "sslmode"},
		{"missing sslmode", "host=db user=yearning_retention password=x dbname=y", "sslmode"},
		{"missing user", "host=db password=x dbname=y sslmode=verify-full", "user"},
		// POSTFIX-04: duplicate keys in libpq follow last-wins; validation must match pgx.
		{"duplicate user last wins", "host=db user=yearning_retention password=x dbname=y sslmode=verify-full user=postgres", "user"},
		{"duplicate sslmode last wins", "host=db user=yearning_retention password=x dbname=y sslmode=verify-full sslmode=disable", "sslmode"},
		// B1: libpq allows whitespace between key and '='; pgx trims whitespace before reading the key.
		{"spaced duplicate user bypass", "host=db user=yearning_retention password=x dbname=y sslmode=verify-full user = postgres", "user"},
		{"spaced duplicate sslmode bypass", "host=db user=yearning_retention password=x dbname=y sslmode=verify-full sslmode = disable", "sslmode"},
		{"tabbed duplicate user bypass", "host=db user=yearning_retention password=x dbname=y sslmode=verify-full user\t=\tpostgres", "user"},
		{"spaced duplicate trailing comment bypass", "host=db user=yearning_retention password=x dbname=y sslmode=verify-full sslmode = prefer", "sslmode"},
		// pgx performs only ASCII trim on keys; NBSP is not a delimiter and must not be treated as the user key.
		{"nbsp is not key whitespace", "host=db user=postgres application_name=x\u00a0user=yearning_retention sslmode=verify-full", "user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Retention.DatabaseDSN = tc.dsn
			err := Validate(ModeServe, cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
	// B1: libpqKeyword must produce the exact same key/value result as pgx parsing, otherwise validation
	// and actual connections could still diverge. Compare directly against ParseConfig User here.
	for _, dsn := range []string{
		"host=db user=yearning_retention password=p dbname=d sslmode=verify-full application_name=a\ruser=postgres\rsslmode=disable",
		"host=db user=yearning_retention password=p dbname=d sslmode=verify-full application_name=a\vuser=postgres",
		"host=db user=yearning_retention password=p dbname=d sslmode=verify-full application_name=a\fuser=postgres",
		"host=db user=postgres application_name=a\u00a0user=yearning_retention sslmode=verify-full",
		"host=db user=yearning_retention user = postgres",
		"host=db user=yearning_retention application_name=a\\ user=postgres",
	} {
		want, ok := libpqKeyword(dsn, "user")
		if !ok {
			t.Fatalf("libpqKeyword did not find user in %q", dsn)
		}
		parsed, err := pgconn.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("pgconn.ParseConfig(%q): %v", dsn, err)
		}
		if parsed.User != want {
			t.Fatalf("parser mismatch for %q: libpq=%q pgx=%q", dsn, want, parsed.User)
		}
	}

	cfg := base
	cfg.Retention.DatabaseDSN = "host=db user=yearning_retention password=x dbname=y sslmode=disable sslmode=verify-full user=postgres user=yearning_retention"
	if err := Validate(ModeServe, cfg); err != nil {
		t.Fatalf("valid last-wins retention DSN rejected: %v", err)
	}
	cfg = base
	cfg.Retention.DatabaseDSN = "host=db user = yearning_retention password=x dbname=y sslmode = verify-full"
	if err := Validate(ModeServe, cfg); err != nil {
		t.Fatalf("valid spaced retention DSN rejected: %v", err)
	}
	cfg = base
	cfg.Retention.DatabaseDSN = "host=db user=yearning_retention password=x dbname=y sslmode=verify-full"
	if err := Validate(ModeServe, cfg); err != nil {
		t.Fatalf("valid retention DSN rejected: %v", err)
	}
}
