// retentiondb.go NEW-QUERY-02: dedicated connection pool constructor for the yearning_retention role.
//
// Background: retention cleanup executes DELETE on managed tables such as evidence_raw_payloads and
// idempotency_records — the application role (yearning_app) lacks this privilege, causing permission
// denied on the first round of the serve app connection and silently breaking cleanup (SEC-0040 root cause).
// Production must construct this connection from a dedicated DSN (YEARNING_RETENTION_DATABASE_DSN).
//
// Connection setup matches platform/database.Connect (gorm + Discard logger +
// SkipDefaultTransaction + DisableAutomaticPing), with two differences:
//   - Input is a complete libpq DSN provided by the deployment surface, so keys like sslmode take
//     effect per DSN semantics — without SEC-0057 environment-derived surface resetting (which protects
//     user datasources against env injection and does not apply to platform outbound connections);
//   - Dialing goes through addrguard (SEC-0009/T05: Control callback validates the resolved address
//     before connect, rejecting loopback/link-local/metadata/unspecified/broadcast/multicast/Unix sockets
//     as a DNS rebinding defense). The YEARNING_ALLOW_LOOPBACK_TARGETS bypass only permits loopback
//     and is rejected at config validation time in production.
package main

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// retentionConnectTimeout bounds DNS resolution and dial time for the dedicated connection.
const retentionConnectTimeout = 10 * time.Second

// openRetentionPool opens a dedicated gorm connection pool using the yearning_retention role DSN.
// Returns the handle and close function; pool limits are intentionally small (cleanup is a low-frequency
// batch job that does not compete with application connections). Returns an error on DSN parse failure,
// addrguard endpoint rejection, or gorm open failure.
func openRetentionPool(dsn string) (*gorm.DB, func() error, error) {
	pgCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("retention: parse database dsn: %w", err)
	}
	port := int(pgCfg.Port)
	if port == 0 {
		port = 5432
	}
	// Endpoint shape validation: IP literals are checked immediately (loopback requires explicit bypass);
	// Unix socket paths are rejected outright; hostnames are guarded at dial time by DialFunc below.
	if err := addrguard.CheckEndpointFor(addrguard.PurposeRetentionDB, pgCfg.Host, port); err != nil {
		return nil, nil, fmt.Errorf("retention: %w", err)
	}
	pgCfg.DialFunc = addrguard.DialFuncFor(addrguard.PurposeRetentionDB, retentionConnectTimeout)

	sqlDB := stdlib.OpenDB(*pgCfg)
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(10 * time.Minute)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger:                 gormlogger.Discard,
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("retention: open postgres connection: %w", err)
	}
	return db, sqlDB.Close, nil
}
