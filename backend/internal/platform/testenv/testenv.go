// Package testenv centralizes integration-test database configuration and
// fail-closed skip policy across backend test packages.
//
// When YEARNING_REQUIRE_INTEGRATION is "1" or "true", SkipOrFatal fails the
// test with t.Fatalf instead of t.Skipf so unconfigured or unreachable DBs can
// never silently turn integration suites green.
package testenv

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Reporter is the subset of testing.TB used by SkipOrFatal so tests can verify
// both skip and fail-closed branches cleanly.
type Reporter interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// DBConfig captures resolved connection parameters for an integration database.
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
	CAFile   string
}

// LibpqDSN formats DBConfig as a libpq key-value connection string.
func (c DBConfig) LibpqDSN() string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.DBName, sslMode,
	)
	if c.CAFile != "" {
		dsn += fmt.Sprintf(" sslrootcert=%s", c.CAFile)
	}
	return dsn
}

// URLDSN formats DBConfig as a postgres:// connection URL.
func (c DBConfig) URLDSN() string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   "/" + strings.TrimPrefix(c.DBName, "/"),
	}
	q := u.Query()
	q.Set("sslmode", sslMode)
	if c.CAFile != "" {
		q.Set("sslrootcert", c.CAFile)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Option customizes default fallback coordinates when env vars are unset.
type Option func(*resolveOptions)

type resolveOptions struct {
	defaultHost    string
	defaultPort    int
	defaultUser    string
	defaultDBName  string
	defaultSSLMode string
	extraDSNKeys   []string
}

// WithDefaultHost overrides the default host fallback.
func WithDefaultHost(host string) Option {
	return func(o *resolveOptions) { o.defaultHost = host }
}

// WithDefaultPort overrides the default TCP port fallback.
func WithDefaultPort(port int) Option {
	return func(o *resolveOptions) { o.defaultPort = port }
}

// WithDefaultUser overrides the default database username fallback.
func WithDefaultUser(user string) Option {
	return func(o *resolveOptions) { o.defaultUser = user }
}

// WithDefaultDBName overrides the default database name fallback.
func WithDefaultDBName(name string) Option {
	return func(o *resolveOptions) { o.defaultDBName = name }
}

// WithDefaultSSLMode overrides the default sslmode fallback.
func WithDefaultSSLMode(mode string) Option {
	return func(o *resolveOptions) { o.defaultSSLMode = mode }
}

// WithExtraDSNEnv prepends caller-specific DSN env var names to lookup order.
func WithExtraDSNEnv(keys ...string) Option {
	return func(o *resolveOptions) { o.extraDSNKeys = append(o.extraDSNKeys, keys...) }
}

// RequireIntegration reports whether missing integration infrastructure must
// fail tests instead of skipping them.
func RequireIntegration() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("YEARNING_REQUIRE_INTEGRATION")))
	return v == "1" || v == "true" || v == "yes"
}

// SkipOrFatal calls t.Fatalf when YEARNING_REQUIRE_INTEGRATION is enabled, and
// t.Skipf otherwise.
func SkipOrFatal(t Reporter, format string, args ...any) {
	t.Helper()
	skipOrFatalWith(RequireIntegration(), t, format, args...)
}

func skipOrFatalWith(require bool, t Reporter, format string, args ...any) {
	t.Helper()
	if require {
		t.Fatalf("integration required (YEARNING_REQUIRE_INTEGRATION=1): "+format, args...)
		return
	}
	t.Skipf(format, args...)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func firstPort(defaultPort int, keys ...string) int {
	if raw := firstEnv(keys...); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 {
			return p
		}
	}
	return defaultPort
}

func applyDSN(raw string, cfg *DBConfig) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if strings.Contains(raw, "://") {
		if applyURLDSN(raw, cfg) {
			return
		}
	}
	applyLibpqDSN(raw, cfg)
}

func applyURLDSN(raw string, cfg *DBConfig) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if h := u.Hostname(); h != "" {
		cfg.Host = h
	}
	if p := u.Port(); p != "" {
		applyDSNField(cfg, "port", p)
	}
	if u.User != nil {
		if usr := u.User.Username(); usr != "" {
			cfg.User = usr
		}
		if pw, ok := u.User.Password(); ok && pw != "" {
			cfg.Password = pw
		}
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		cfg.DBName = db
	}
	q := u.Query()
	applyDSNField(cfg, "sslmode", strings.TrimSpace(q.Get("sslmode")))
	applyDSNField(cfg, "sslrootcert", strings.TrimSpace(q.Get("sslrootcert")))
	return true
}

func applyLibpqDSN(raw string, cfg *DBConfig) {
	for _, field := range strings.Fields(raw) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		applyDSNField(cfg, k, strings.Trim(v, "'\""))
	}
}

func applyDSNField(cfg *DBConfig, key, val string) {
	if val == "" {
		return
	}
	switch key {
	case "host":
		cfg.Host = val
	case "port":
		if p, err := strconv.Atoi(val); err == nil && p > 0 {
			cfg.Port = p
		}
	case "user":
		cfg.User = val
	case "password":
		cfg.Password = val
	case "dbname":
		cfg.DBName = val
	case "sslmode":
		cfg.SSLMode = val
	case "sslrootcert":
		cfg.CAFile = val
	}
}

// ResolveMetaPG returns metadata PostgreSQL config and whether credentials are configured.
// Canonical env vars: YEARNING_TEST_META_{DSN,HOST,PORT,USER,PASSWORD,DBNAME,SSLMODE,CA_FILE}.
// Legacy fallbacks: YEARNING_TEST_DATABASE_*, YEARNING_B13_TEST_DB_*, YEARNING_TEST_PG_DSN,
// YEARNING_TEST_POSTGRES_DSN, YEARNING_TEST_PG_CA_FILE.
func ResolveMetaPG(opts ...Option) (DBConfig, bool) {
	o := resolveOptions{
		defaultHost:    "127.0.0.1",
		defaultPort:    54330,
		defaultUser:    "yearning",
		defaultDBName:  "yearning_v4",
		defaultSSLMode: "disable",
	}
	for _, opt := range opts {
		opt(&o)
	}
	cfg := DBConfig{
		Host:    o.defaultHost,
		Port:    o.defaultPort,
		User:    o.defaultUser,
		DBName:  o.defaultDBName,
		SSLMode: o.defaultSSLMode,
	}
	if h := firstEnv("YEARNING_TEST_META_HOST", "YEARNING_TEST_DATABASE_HOST", "YEARNING_B13_TEST_DB_HOST"); h != "" {
		cfg.Host = h
	}
	cfg.Port = firstPort(cfg.Port, "YEARNING_TEST_META_PORT", "YEARNING_TEST_DATABASE_PORT", "YEARNING_B13_TEST_DB_PORT")
	if u := firstEnv("YEARNING_TEST_META_USER", "YEARNING_TEST_DATABASE_USER", "YEARNING_B13_TEST_DB_USER"); u != "" {
		cfg.User = u
	}
	if pw := firstEnv("YEARNING_TEST_META_PASSWORD", "YEARNING_TEST_DATABASE_PASSWORD", "YEARNING_B13_TEST_DB_PASSWORD"); pw != "" {
		cfg.Password = pw
	}
	if db := firstEnv("YEARNING_TEST_META_DBNAME", "YEARNING_TEST_DATABASE_DBNAME", "YEARNING_B13_TEST_DB_NAME"); db != "" {
		cfg.DBName = db
	}
	if ssl := firstEnv("YEARNING_TEST_META_SSLMODE", "YEARNING_TEST_DATABASE_SSLMODE"); ssl != "" {
		cfg.SSLMode = ssl
	}
	if ca := firstEnv("YEARNING_TEST_META_CA_FILE", "YEARNING_TEST_PG_CA_FILE"); ca != "" {
		cfg.CAFile = ca
	}

	dsnKeys := append(append([]string{}, o.extraDSNKeys...),
		"YEARNING_TEST_META_DSN",
		"YEARNING_TEST_POSTGRES_DSN",
		"YEARNING_TEST_DATABASE_DSN",
		"YEARNING_TEST_PG_DSN",
	)
	if rawDSN := firstEnv(dsnKeys...); rawDSN != "" {
		applyDSN(rawDSN, &cfg)
	}
	if cfg.Password == "" {
		return cfg, false
	}
	return cfg, true
}

// RequireMetaPG resolves metadata PostgreSQL config or calls SkipOrFatal if unconfigured.
func RequireMetaPG(t Reporter, opts ...Option) DBConfig {
	t.Helper()
	cfg, ok := ResolveMetaPG(opts...)
	if !ok {
		SkipOrFatal(t, "integration PostgreSQL not configured (set YEARNING_TEST_META_PASSWORD or YEARNING_TEST_META_DSN)")
	}
	return cfg
}

// MetaDSN resolves metadata PostgreSQL libpq DSN or calls SkipOrFatal if unconfigured.
func MetaDSN(t Reporter, opts ...Option) string {
	t.Helper()
	return RequireMetaPG(t, opts...).LibpqDSN()
}

// MetaURL resolves metadata PostgreSQL URL DSN or calls SkipOrFatal if unconfigured.
func MetaURL(t Reporter, opts ...Option) string {
	t.Helper()
	return RequireMetaPG(t, opts...).URLDSN()
}

// ResolveTargetPG returns target PostgreSQL (PG17) config and whether credentials are configured.
// Canonical env vars: YEARNING_TEST_TARGET_PG_{HOST,PORT,USER,PASSWORD,DBNAME}.
func ResolveTargetPG(opts ...Option) (DBConfig, bool) {
	o := resolveOptions{
		defaultHost:    "127.0.0.1",
		defaultPort:    54331,
		defaultUser:    "yearning",
		defaultDBName:  "yearning",
		defaultSSLMode: "disable",
	}
	for _, opt := range opts {
		opt(&o)
	}
	pw := firstEnv("YEARNING_TEST_TARGET_PG_PASSWORD", "YEARNING_TEST_ADAPTER_PG_PASSWORD", "YEARNING_ROLLBACK_TEST_PG17_PASSWORD")
	cfg := DBConfig{
		Host:     firstEnv("YEARNING_TEST_TARGET_PG_HOST", "YEARNING_TEST_ADAPTER_PG_HOST", "YEARNING_ROLLBACK_TEST_PG_HOST"),
		Port:     firstPort(o.defaultPort, "YEARNING_TEST_TARGET_PG_PORT", "YEARNING_TEST_ADAPTER_PG_PORT", "YEARNING_ROLLBACK_TEST_PG17_PORT"),
		User:     firstEnv("YEARNING_TEST_TARGET_PG_USER", "YEARNING_TEST_ADAPTER_PG_USER", "YEARNING_ROLLBACK_TEST_PG17_USER"),
		Password: pw,
		DBName:   firstEnv("YEARNING_TEST_TARGET_PG_DBNAME", "YEARNING_TEST_ADAPTER_PG_DB", "YEARNING_ROLLBACK_TEST_PG17_DB"),
		SSLMode:  o.defaultSSLMode,
	}
	if cfg.Host == "" {
		cfg.Host = o.defaultHost
	}
	if cfg.User == "" {
		cfg.User = o.defaultUser
	}
	if cfg.DBName == "" {
		cfg.DBName = o.defaultDBName
	}
	return cfg, cfg.Password != ""
}

// RequireTargetPG resolves target PostgreSQL config or calls SkipOrFatal if unconfigured.
func RequireTargetPG(t Reporter, opts ...Option) DBConfig {
	t.Helper()
	cfg, ok := ResolveTargetPG(opts...)
	if !ok {
		SkipOrFatal(t, "target PostgreSQL not configured (set YEARNING_TEST_TARGET_PG_PASSWORD)")
	}
	return cfg
}

// ResolveTargetMySQL returns target MySQL (8.0) config and whether credentials are configured.
// Canonical env vars: YEARNING_TEST_TARGET_MYSQL_{HOST,PORT,USER,PASSWORD,DBNAME}.
func ResolveTargetMySQL(opts ...Option) (DBConfig, bool) {
	o := resolveOptions{
		defaultHost:   "127.0.0.1",
		defaultPort:   33306,
		defaultUser:   "root",
		defaultDBName: "yearning",
	}
	for _, opt := range opts {
		opt(&o)
	}
	pw := firstEnv("YEARNING_TEST_TARGET_MYSQL_PASSWORD", "YEARNING_TEST_ADAPTER_MYSQL_PASSWORD", "YEARNING_ROLLBACK_TEST_MYSQL_PASSWORD")
	cfg := DBConfig{
		Host:     firstEnv("YEARNING_TEST_TARGET_MYSQL_HOST", "YEARNING_TEST_ADAPTER_MYSQL_HOST", "YEARNING_ROLLBACK_TEST_MYSQL_HOST"),
		Port:     firstPort(o.defaultPort, "YEARNING_TEST_TARGET_MYSQL_PORT", "YEARNING_TEST_ADAPTER_MYSQL_PORT", "YEARNING_ROLLBACK_TEST_MYSQL_PORT"),
		User:     firstEnv("YEARNING_TEST_TARGET_MYSQL_USER", "YEARNING_TEST_ADAPTER_MYSQL_USER", "YEARNING_ROLLBACK_TEST_MYSQL_USER"),
		Password: pw,
		DBName:   firstEnv("YEARNING_TEST_TARGET_MYSQL_DBNAME", "YEARNING_TEST_ADAPTER_MYSQL_DB", "YEARNING_ROLLBACK_TEST_MYSQL_DB"),
	}
	if cfg.Host == "" {
		cfg.Host = o.defaultHost
	}
	if cfg.User == "" {
		cfg.User = o.defaultUser
	}
	if cfg.DBName == "" {
		cfg.DBName = o.defaultDBName
	}
	return cfg, cfg.Password != ""
}

// RequireTargetMySQL resolves target MySQL config or calls SkipOrFatal if unconfigured.
func RequireTargetMySQL(t Reporter, opts ...Option) DBConfig {
	t.Helper()
	cfg, ok := ResolveTargetMySQL(opts...)
	if !ok {
		SkipOrFatal(t, "target MySQL not configured (set YEARNING_TEST_TARGET_MYSQL_PASSWORD)")
	}
	return cfg
}

// RequireB8EvidenceDSN resolves the isolated B8 migration skill evidence PostgreSQL DSN.
// Canonical env vars: YEARNING_TEST_B8_EVIDENCE_{HOST,PORT,USER,DBNAME} (password from Meta).
func RequireB8EvidenceDSN(t Reporter) string {
	t.Helper()
	meta := RequireMetaPG(t)
	dbName := firstEnv("YEARNING_TEST_B8_EVIDENCE_DBNAME", "YEARNING_B8_EVIDENCE_DBNAME")
	if dbName == "" {
		SkipOrFatal(t, "B8 evidence database not configured (set YEARNING_TEST_B8_EVIDENCE_DBNAME)")
	}
	cfg := DBConfig{
		Host:     firstEnv("YEARNING_TEST_B8_EVIDENCE_HOST", "YEARNING_B8_EVIDENCE_HOST"),
		Port:     firstPort(meta.Port, "YEARNING_TEST_B8_EVIDENCE_PORT", "YEARNING_B8_EVIDENCE_PORT"),
		User:     firstEnv("YEARNING_TEST_B8_EVIDENCE_USER", "YEARNING_B8_EVIDENCE_USER"),
		Password: meta.Password,
		DBName:   dbName,
		SSLMode:  "disable",
	}
	if cfg.Host == "" {
		cfg.Host = meta.Host
	}
	if cfg.User == "" {
		cfg.User = meta.User
	}
	return cfg.LibpqDSN()
}

// RequireRollbackPG18 resolves PG18 target coordinates for rollback returning tests.
// Canonical env vars: YEARNING_TEST_ROLLBACK_PG18_{HOST,PORT,USER,PASSWORD,DBNAME}.
func RequireRollbackPG18(t Reporter) DBConfig {
	t.Helper()
	meta, metaOK := ResolveMetaPG()
	pw := firstEnv("YEARNING_TEST_ROLLBACK_PG18_PASSWORD", "YEARNING_ROLLBACK_TEST_PG18_PASSWORD")
	if pw == "" && metaOK {
		pw = meta.Password
	}
	if pw == "" {
		SkipOrFatal(t, "rollback PG18 database not configured (set YEARNING_TEST_ROLLBACK_PG18_PASSWORD)")
	}
	host := firstEnv("YEARNING_TEST_ROLLBACK_PG18_HOST", "YEARNING_ROLLBACK_TEST_PG_HOST", "YEARNING_TEST_META_HOST", "YEARNING_TEST_DATABASE_HOST")
	if host == "" {
		host = meta.Host
	}
	user := firstEnv("YEARNING_TEST_ROLLBACK_PG18_USER", "YEARNING_ROLLBACK_TEST_PG18_USER")
	if user == "" {
		user = meta.User
	}
	dbName := firstEnv("YEARNING_TEST_ROLLBACK_PG18_DBNAME", "YEARNING_ROLLBACK_TEST_PG18_DB")
	if dbName == "" {
		dbName = "yearning_rollback_pg18"
	}
	port := firstPort(meta.Port, "YEARNING_TEST_ROLLBACK_PG18_PORT", "YEARNING_ROLLBACK_TEST_PG18_PORT")
	return DBConfig{
		Host:     host,
		Port:     port,
		User:     user,
		Password: pw,
		DBName:   dbName,
		SSLMode:  "disable",
	}
}
