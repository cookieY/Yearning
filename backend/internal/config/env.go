package config

import (
	"os"
	"strconv"
	"strings"
)

// applyEnv overrides individual leaf fields from YEARNING_* environment variables.
// Invalid integer override values are recorded as issues and reported together by Validate.
func applyEnv(cfg *Config) error {
	e := &Error{}
	set := func(key string, target *string) {
		if raw, ok := os.LookupEnv(key); ok {
			*target = raw
		}
	}
	setInt := func(key string, target *int) {
		raw, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(raw) == "" {
			return
		}
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			e.Problems = append(e.Problems, "env "+key+" must be an integer")
			return
		}
		*target = value
	}
	setBool := func(key string, target *bool) {
		raw, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(raw) == "" {
			return
		}
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			e.Problems = append(e.Problems, "env "+key+" must be a boolean")
			return
		}
		*target = value
	}

	set(EnvPrefix+"_ENVIRONMENT", &cfg.Environment)
	set(EnvPrefix+"_SERVER_LISTEN_ADDRESS", &cfg.Server.ListenAddress)
	setInt(EnvPrefix+"_SERVER_SHUTDOWN_GRACE_SECONDS", &cfg.Server.ShutdownGraceSeconds)
	setInt(EnvPrefix+"_SERVER_READ_HEADER_TIMEOUT_SECONDS", &cfg.Server.ReadHeaderTimeoutSec)
	setInt(EnvPrefix+"_SERVER_READ_TIMEOUT_SECONDS", &cfg.Server.ReadTimeoutSeconds)
	setInt(EnvPrefix+"_SERVER_WRITE_TIMEOUT_SECONDS", &cfg.Server.WriteTimeoutSeconds)
	setInt(EnvPrefix+"_SERVER_IDLE_TIMEOUT_SECONDS", &cfg.Server.IdleTimeoutSeconds)

	set(EnvPrefix+"_DATABASE_HOST", &cfg.Database.Host)
	set(EnvPrefix+"_DATABASE_PORT", &cfg.Database.Port)
	set(EnvPrefix+"_DATABASE_USER", &cfg.Database.User)
	set(EnvPrefix+"_DATABASE_PASSWORD", &cfg.Database.Password)
	set(EnvPrefix+"_DATABASE_DBNAME", &cfg.Database.DBName)
	set(EnvPrefix+"_DATABASE_SSLMODE", &cfg.Database.SSLMode)
	set(EnvPrefix+"_DATABASE_SSLROOTCERT", &cfg.Database.SSLRootCert)
	setInt(EnvPrefix+"_DATABASE_MAX_OPEN_CONNS", &cfg.Database.MaxOpenConns)
	setInt(EnvPrefix+"_DATABASE_MAX_IDLE_CONNS", &cfg.Database.MaxIdleConns)
	setInt(EnvPrefix+"_DATABASE_CONN_MAX_LIFETIME_MINUTES", &cfg.Database.ConnMaxLifetimeMinutes)

	set(EnvPrefix+"_RETENTION_DATABASE_DSN", &cfg.Retention.DatabaseDSN)

	set(EnvPrefix+"_LOGGING_LEVEL", &cfg.Logging.Level)
	set(EnvPrefix+"_LOGGING_FORMAT", &cfg.Logging.Format)

	set(EnvPrefix+"_OBSERVABILITY_SERVICE_NAME", &cfg.Observability.ServiceName)
	set(EnvPrefix+"_OBSERVABILITY_DEPLOYMENT_ID", &cfg.Observability.DeploymentID)
	set(EnvPrefix+"_OBSERVABILITY_INSTANCE_ID", &cfg.Observability.InstanceID)

	set(EnvPrefix+"_SESSION_COOKIE_NAME", &cfg.Session.CookieName)
	setBool(EnvPrefix+"_SESSION_SECURE", &cfg.Session.Secure)

	set(EnvPrefix+"_KEY_PROVIDER_KIND", &cfg.KeyProvider.Kind)
	set(EnvPrefix+"_KEY_PROVIDER_PATH", &cfg.KeyProvider.Path)

	set(EnvPrefix+"_RELEASE_FEED_UPSTREAM_URL", &cfg.ReleaseFeed.UpstreamURL)

	if raw, ok := os.LookupEnv(EnvPrefix + "_TRUSTED_PROXIES"); ok {
		cfg.TrustedProxies = splitCSV(raw)
	}

	if len(e.Problems) == 0 {
		return nil
	}
	return e
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
