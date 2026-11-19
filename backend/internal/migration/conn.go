// Package migration provides shared infrastructure for v4 schema migration and legacy data migration.
package migration

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cookieY/YearningX/backend/internal/config"
)

// TargetEndpoint describes the target PostgreSQL endpoint for migration; Password is resolved at call time.
// TLS semantics are carried in the DSN: verify_ca -> sslmode=verify-ca, verify_full -> verify-full,
// and certificate file paths are passed via sslrootcert/sslcert/sslkey to the libpq-style parser.
type TargetEndpoint struct {
	Host            string
	Port            int
	Database        string
	Username        string
	Password        string
	TLSMode         string // verify_ca | verify_full
	ServerName      string
	CAFile          *string
	CertificateFile *string
	PrivateKeyFile  *string
}

// TargetDSN builds the target connection string. Every value is quoted per libpq rules via config.QuoteDSNValue
// (SEC-0045) so spaces, keywords, or special characters in credentials or paths can never alter connection semantics.
func (e TargetEndpoint) TargetDSN() (string, error) {
	sslmode := "verify-full"
	switch e.TLSMode {
	case "verify_ca":
		sslmode = "verify-ca"
	case "verify_full":
		sslmode = "verify-full"
	default:
		return "", fmt.Errorf("unsupported tls mode %q", e.TLSMode)
	}
	_ = e.ServerName // verify-ca does not verify hostname; verify-full uses the host parameter
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		config.QuoteDSNValue(e.Host), e.Port, config.QuoteDSNValue(e.Database),
		config.QuoteDSNValue(e.Username), config.QuoteDSNValue(e.Password), sslmode)
	if e.CAFile != nil && *e.CAFile != "" {
		dsn += fmt.Sprintf(" sslrootcert=%s", config.QuoteDSNValue(*e.CAFile))
	}
	if e.CertificateFile != nil && *e.CertificateFile != "" {
		dsn += fmt.Sprintf(" sslcert=%s", config.QuoteDSNValue(*e.CertificateFile))
	}
	if e.PrivateKeyFile != nil && *e.PrivateKeyFile != "" {
		dsn += fmt.Sprintf(" sslkey=%s", config.QuoteDSNValue(*e.PrivateKeyFile))
	}
	return dsn, nil
}

// OpenTarget opens a connection pool to the target PostgreSQL instance. TLS is mandatory per contract.
func OpenTarget(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse target config: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect target: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping target: %w", err)
	}
	return pool, nil
}
