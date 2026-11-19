// Package config implements configuration loading for the Yearning v4 backend.
//
// Precedence is fixed: built-in defaults < YAML config file < YEARNING_* environment variables.
// Unknown fields in the config file cause an immediate error; environment variables override individual leaf fields.
// Serve mode enforces database and Key Provider validation; mock mode requires no external dependencies.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// Mode represents the process runtime mode, determined by the subcommand rather than the config file.
type Mode string

const (
	ModeServe Mode = "serve"
	ModeMock  Mode = "mock"
)

const EnvPrefix = "YEARNING"

type Config struct {
	Environment    string              `yaml:"environment"`
	Server         ServerConfig        `yaml:"server"`
	Database       DatabaseConfig      `yaml:"database"`
	Retention      RetentionConfig     `yaml:"retention"`
	Logging        LoggingConfig       `yaml:"logging"`
	Observability  ObservabilityConfig `yaml:"observability"`
	Session        SessionConfig       `yaml:"session"`
	KeyProvider    KeyProviderConfig   `yaml:"key_provider"`
	ReleaseFeed    ReleaseFeedConfig   `yaml:"release_feed"`
	TrustedProxies []string            `yaml:"trusted_proxies"`
}

// RetentionConfig holds connection settings for retention cleanup (SEC-0040/NEW-QUERY-02).
// The cleanup execution role is yearning_retention — the application role lacks DELETE privileges on managed tables.
type RetentionConfig struct {
	// DatabaseDSN is the dedicated libpq connection string for the yearning_retention role.
	// Must be explicitly provided in production serve mode; falls back to the primary app connection
	// when empty in development (single-role local dev metadata DB) with a startup Warn log.
	DatabaseDSN string `yaml:"database_dsn"`
}

// ReleaseFeedConfig configures the upstream release snapshot source (RCP-20261020): fetched once at
// serve startup and daily on a timer; the frontend reads only this instance endpoint and never contacts GitHub directly.
type ReleaseFeedConfig struct {
	UpstreamURL string `yaml:"upstream_url"`
}

type ServerConfig struct {
	ListenAddress        string `yaml:"listen_address"`
	ShutdownGraceSeconds int    `yaml:"shutdown_grace_seconds"`
	ReadHeaderTimeoutSec int    `yaml:"read_header_timeout_seconds"`
	ReadTimeoutSeconds   int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds  int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds   int    `yaml:"idle_timeout_seconds"`
}

type DatabaseConfig struct {
	Host                   string `yaml:"host"`
	Port                   string `yaml:"port"`
	User                   string `yaml:"user"`
	Password               string `yaml:"password"`
	DBName                 string `yaml:"dbname"`
	SSLMode                string `yaml:"sslmode"`
	SSLRootCert            string `yaml:"sslrootcert"`
	MaxOpenConns           int    `yaml:"max_open_conns"`
	MaxIdleConns           int    `yaml:"max_idle_conns"`
	ConnMaxLifetimeMinutes int    `yaml:"conn_max_lifetime_minutes"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type ObservabilityConfig struct {
	ServiceName  string `yaml:"service_name"`
	DeploymentID string `yaml:"deployment_id"`
	InstanceID   string `yaml:"instance_id"`
}

type SessionConfig struct {
	CookieName string `yaml:"cookie_name"`
	Secure     bool   `yaml:"secure"`
}

type KeyProviderConfig struct {
	Kind string `yaml:"kind"`
	Path string `yaml:"path"`
}

// Error aggregates field names and reasons for configuration validation failures so startup can report them all at once.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "invalid configuration: " + strings.Join(e.Problems, "; ")
}

// Defaults returns the built-in default configuration; both validation and overrides start from this value.
// SEC-0018: environment defaults to empty (= undeclared; equivalent to development in loopback dev setups).
// When listening on a non-loopback address, RequireExplicitEnvironmentForListen requires an explicit setting —
// preventing default development mode from silently exposing production surfaces.
func Defaults() Config {
	return Config{
		Environment: "",
		Server: ServerConfig{
			ListenAddress:        "0.0.0.0:8080",
			ShutdownGraceSeconds: 20,
			ReadHeaderTimeoutSec: 10,
			ReadTimeoutSeconds:   60,
			WriteTimeoutSeconds:  120,
			IdleTimeoutSeconds:   120,
		},
		Database: DatabaseConfig{
			SSLMode:                "prefer",
			MaxOpenConns:           50,
			MaxIdleConns:           15,
			ConnMaxLifetimeMinutes: 10,
		},
		Logging:       LoggingConfig{Level: "info", Format: "json"},
		Observability: ObservabilityConfig{ServiceName: "yearning"},
		Session:       SessionConfig{CookieName: "yearning_session", Secure: true},
		KeyProvider:   KeyProviderConfig{Kind: "file"},
		ReleaseFeed:   ReleaseFeedConfig{UpstreamURL: "https://api.github.com/repos/cookieY/Yearning/releases/latest"},
	}
}

// Load assembles configuration in defaults -> file -> environment order and validates it for the given mode.
// Skips the file layer when path is empty.
func Load(ctx context.Context, mode Mode, path string) (Config, error) {
	_ = ctx
	cfg := Defaults()
	if path != "" {
		if err := applyFile(&cfg, path); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := Validate(mode, cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("decode config file %s: %w", path, err)
	}
	return nil
}

// Validate checks enums, ranges, and mode-required fields, returning a configuration error containing all issues.
func Validate(mode Mode, cfg Config) error {
	e := &Error{}
	add := func(format string, args ...any) {
		e.Problems = append(e.Problems, fmt.Sprintf(format, args...))
	}

	switch cfg.Environment {
	case "", "development", "production":
	default:
		add("environment must be development or production")
	}
	if cfg.Server.ListenAddress == "" {
		add("server.listen_address must not be empty")
	}
	if cfg.Server.ShutdownGraceSeconds <= 0 || cfg.Server.ShutdownGraceSeconds > 300 {
		add("server.shutdown_grace_seconds must be in [1,300]")
	}
	for _, timeout := range []struct {
		name  string
		value int
	}{
		{"read_header_timeout_seconds", cfg.Server.ReadHeaderTimeoutSec},
		{"read_timeout_seconds", cfg.Server.ReadTimeoutSeconds},
		{"write_timeout_seconds", cfg.Server.WriteTimeoutSeconds},
		{"idle_timeout_seconds", cfg.Server.IdleTimeoutSeconds},
	} {
		if timeout.value <= 0 {
			add("server.%s must be positive", timeout.name)
		}
	}

	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level must be one of debug/info/warn/error")
	}
	if cfg.Logging.Format != "json" {
		add("logging.format must be json")
	}
	if cfg.Observability.ServiceName == "" {
		add("observability.service_name must not be empty")
	}

	switch cfg.Database.SSLMode {
	case "require", "verify-ca", "verify-full", "prefer", "disable":
	default:
		add("database.sslmode must be one of require/verify-ca/verify-full/prefer/disable")
	}
	if cfg.Environment == "production" && cfg.Database.SSLMode != "verify-ca" && cfg.Database.SSLMode != "verify-full" {
		// SEC-0018: production accepts only certificate-verifying TLS modes. Although require encrypts traffic,
		// it does not verify the server certificate and remains vulnerable to MITM; prefer/disable are also forbidden.
		// CA certificates are supplied via the system trust store or sslrootcert in the DSN.
		add("database.sslmode must be verify-ca/verify-full when environment is production")
	}
	if mode == ModeServe && cfg.Environment == "production" && strings.TrimSpace(cfg.Retention.DatabaseDSN) != "" {
		// NEW-DEPLOY-15: retention DSN is not generated via DatabaseConfig.DSN and must be validated
		// separately, otherwise production could silently fall back to plaintext sslmode=prefer.
		if user, ok := libpqKeyword(cfg.Retention.DatabaseDSN, "user"); !ok || user != "yearning_retention" {
			add("retention.database_dsn user must be yearning_retention, got %q", user)
		}
		ssl, ok := libpqKeyword(cfg.Retention.DatabaseDSN, "sslmode")
		if !ok || (ssl != "verify-ca" && ssl != "verify-full") {
			add("retention.database_dsn sslmode must be verify-ca/verify-full when environment is production")
		}
	}
	if cfg.Database.MaxOpenConns <= 0 || cfg.Database.MaxIdleConns <= 0 {
		add("database.max_open_conns and max_idle_conns must be positive")
	}
	if cfg.Database.MaxIdleConns > cfg.Database.MaxOpenConns {
		add("database.max_idle_conns must not exceed max_open_conns")
	}
	if cfg.Database.ConnMaxLifetimeMinutes <= 0 {
		add("database.conn_max_lifetime_minutes must be positive")
	}
	// DSN security precondition (SEC-0045): control characters cannot safely enter libpq quoted values and are rejected outright.
	for field, value := range map[string]string{
		"database.host":     cfg.Database.Host,
		"database.port":     cfg.Database.Port,
		"database.user":     cfg.Database.User,
		"database.password": cfg.Database.Password,
		"database.dbname":   cfg.Database.DBName,
	} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				add("%s must not contain control characters", field)
				break
			}
		}
	}
	// NEW-QUERY-02: dedicated retention DSN shares the same security preconditions as primary DB credentials;
	// missing in production serve mode is invalid — falling back to the app connection silently breaks production cleanup.
	for _, r := range cfg.Retention.DatabaseDSN {
		if r < 0x20 || r == 0x7f {
			add("retention.database_dsn must not contain control characters")
			break
		}
	}
	if mode == ModeServe && cfg.Environment == "production" && strings.TrimSpace(cfg.Retention.DatabaseDSN) == "" {
		add("serve mode in production requires retention.database_dsn (YEARNING_RETENTION_DATABASE_DSN; yearning_retention role connection)")
	}

	if cfg.Session.CookieName == "" {
		add("session.cookie_name must not be empty")
	}
	if cfg.Environment == "production" && !cfg.Session.Secure {
		add("session.secure must be true when environment is production")
	}
	// SEC-0009: addrguard development bypass (YEARNING_ALLOW_LOOPBACK_TARGETS) must be disabled in
	// production — a truthy value in the process environment makes the configuration invalid (fail-closed).
	// Truthiness matches platform/addrguard semantics (1|true, case- and whitespace-insensitive);
	// development/undeclared environments are unrestricted (local dev and f12 stack target DBs run on loopback).
	if cfg.Environment == "production" {
		if bypass := strings.ToLower(strings.TrimSpace(os.Getenv(addrguard.LoopbackBypassEnv))); bypass == "1" || bypass == "true" {
			add("environment production must not enable %s (addrguard loopback bypass is development-only)", addrguard.LoopbackBypassEnv)
		}
	}

	switch cfg.KeyProvider.Kind {
	case "file":
	default:
		add("key_provider.kind must be file")
	}

	if mode == ModeServe {
		missing := []string{}
		if cfg.Database.Host == "" {
			missing = append(missing, "database.host")
		}
		if cfg.Database.Port == "" {
			missing = append(missing, "database.port")
		}
		if cfg.Database.User == "" {
			missing = append(missing, "database.user")
		}
		if cfg.Database.Password == "" {
			missing = append(missing, "database.password")
		}
		if cfg.Database.DBName == "" {
			missing = append(missing, "database.dbname")
		}
		if len(missing) > 0 {
			add("serve mode requires %s (file or YEARNING_DATABASE_* env)", strings.Join(missing, ", "))
		}
		if cfg.KeyProvider.Kind == "file" && cfg.KeyProvider.Path == "" {
			add("serve mode requires key_provider.path")
		}
	}

	if len(e.Problems) == 0 {
		return nil
	}
	return e
}

// QuoteDSNValue safely quotes a DSN value per libpq keyword/value syntax (SEC-0045):
// values containing whitespace, single quotes, backslashes, equals signs, or other metacharacters
// (typically database passwords) are wrapped in single quotes with internal single quotes and backslashes
// escaped, ensuring credential contents can never rewrite other DSN keywords (host/user/port/dbname/sslmode).
// The pgx DSN parser implements the same libpq rules.
func QuoteDSNValue(v string) string {
	if v == "" {
		return "''"
	}
	needsQuote := false
	for _, r := range v {
		if r <= ' ' || r == '\'' || r == '\\' || r == '=' || r == '"' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return v
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range v {
		if r == '\'' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// DSN formats the PostgreSQL connection string. All values are quoted via QuoteDSNValue so spaces,
// keywords, or special characters in credentials cannot corrupt or rewrite connection semantics; the password enters only the DSN, never logs.
func (c DatabaseConfig) DSN() string {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		QuoteDSNValue(c.Host), QuoteDSNValue(c.Port), QuoteDSNValue(c.User),
		QuoteDSNValue(c.Password), QuoteDSNValue(c.DBName), QuoteDSNValue(c.SSLMode),
	)
	if c.SSLRootCert != "" {
		dsn += " sslrootcert=" + QuoteDSNValue(c.SSLRootCert)
	}
	return dsn
}

// libpqKeyword reads a single key value from a libpq keyword/value connection string (for RETENTION DSN
// security validation); supports single-quote escaping without relying on a pgx connection (avoiding network calls during validation).
//
// Duplicate keys return the last value per libpq last-wins semantics. If first-wins were used,
// `... user=yearning_retention ... user=postgres` would pass validation while pgx actually connects
// as the latter, creating a TLS/role bypass (POSTFIX-04).
func isDSNSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

func libpqKeyword(dsn, key string) (string, bool) {
	i := 0
	var matched string
	found := false
	for i < len(dsn) {
		for i < len(dsn) && isDSNSpace(dsn[i]) {
			i++
		}
		start := i
		// POSTFIX-04/B1: libpq allows whitespace between key and '='; pgx trims whitespace from keys
		// when parsing. Ending a key strictly on the first space would miss `user = postgres`,
		// causing validated values to diverge from runtime values.
		for i < len(dsn) && dsn[i] != '=' {
			i++
		}
		// Consistent with pgx parseKeywordValueSettings: trim only ASCII whitespace
		// rather than Unicode TrimSpace (otherwise NBSP would be collapsed into a valid key).
		name := strings.Trim(dsn[start:i], " \t\n\r\v\f")
		if i >= len(dsn) || dsn[i] != '=' {
			continue
		}
		i++
		for i < len(dsn) && isDSNSpace(dsn[i]) {
			i++
		}
		var raw string
		if i < len(dsn) && dsn[i] == '\'' {
			i++
			start = i
			for i < len(dsn) {
				if dsn[i] == '\\' {
					i += 2
					continue
				}
				if dsn[i] == '\'' {
					break
				}
				i++
			}
			if i > len(dsn) {
				return "", false
			}
			raw = dsn[start:i]
			if i < len(dsn) && dsn[i] == '\'' {
				i++
			}
		} else {
			start = i
			for i < len(dsn) {
				if isDSNSpace(dsn[i]) {
					break
				}
				if dsn[i] == '\\' {
					i++
					if i >= len(dsn) {
						return "", false
					}
				}
				i++
			}
			raw = dsn[start:i]
		}
		// pgx unescape only processes the \\ and \' escape sequences.
		value := strings.ReplaceAll(strings.ReplaceAll(raw, "\\\\", "\\"), "\\'", "'")

		if name == key {
			matched = value
			found = true
		}
	}
	return matched, found
}

// ShutdownGrace returns the graceful shutdown wait window.
func (c ServerConfig) ShutdownGrace() time.Duration {
	return time.Duration(c.ShutdownGraceSeconds) * time.Second
}

// RequireExplicitEnvironmentForListen SEC-0018: non-loopback listen addresses require
// environment to be explicitly set (development|production). Empty host (":8080"),
// 0.0.0.0, and [::] are all treated as externally exposed surfaces; only 127.0.0.1/::1/localhost
// loopback addresses are exempt (no explicit declaration needed for local dev). Invoked again
// by the caller (serve wiring) after --listen overrides — the final listen address is not yet visible during config file validation.
func RequireExplicitEnvironmentForListen(cfg Config) error {
	if cfg.Environment != "" {
		return nil
	}
	host := cfg.Server.ListenAddress
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = strings.Trim(host[:i], "[]")
	}
	switch strings.ToLower(host) {
	case "", "0.0.0.0", "[::]", "::":
		return fmt.Errorf("config: environment must be set explicitly (development|production) when listening on non-loopback %q", cfg.Server.ListenAddress)
	case "127.0.0.1", "localhost", "::1":
		return nil
	}
	if strings.HasPrefix(strings.ToLower(host), "127.") {
		return nil
	}
	return fmt.Errorf("config: environment must be set explicitly (development|production) when listening on non-loopback %q", cfg.Server.ListenAddress)
}
