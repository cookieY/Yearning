// Package database manages the PostgreSQL connection pool and explicit transactions.
//
// Constraints (ADR-0001, platform-postgresql contract):
//   - The application role performs no DDL; this package only manages connections and transaction boundaries;
//   - GORM internal SQL logging is disabled so SQL statements never enter stdout or telemetry;
//   - Transactions are managed explicitly via TxManager, propagating transaction handles through context.Context.
package database

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/config"
)

// Tx represents the commit/rollback boundary of a single database transaction.
type Tx interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Connector abstracts transaction creation to allow testing transaction semantics without a live PostgreSQL instance.
type Connector interface {
	Begin(ctx context.Context) (Tx, error)
	Ping(ctx context.Context) error
	Close() error
}

// TxManager provides nesting-safe transaction execution: reuses an existing transaction in ctx instead of starting a nested Begin.
type TxManager struct {
	conn Connector
}

func NewTxManager(conn Connector) *TxManager {
	return &TxManager{conn: conn}
}

type txKey struct{}

// WithinTx executes fn inside a transaction: commits if fn succeeds, or rolls back if fn returns an error or panics.
// Calls already inside a transaction join and reuse the existing transaction so a business operation runs in a single transaction.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(Tx); ok {
		return fn(ctx)
	}
	tx, err := m.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	ctx = context.WithValue(ctx, txKey{}, tx)
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(r)
		}
	}()
	if err := fn(ctx); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			return fmt.Errorf("rollback after error %v failed: %w", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// Pool is a thin wrapper around the GORM connection pool.
type Pool struct {
	db    *gorm.DB
	stats func() (open, inUse int)
	close func() error
	ping  func(ctx context.Context) error
}

// Connect initializes a lazy connection pool without performing a startup Ping.
// The kubernetes contract requires Readiness to fail when PostgreSQL is unavailable without crashing the process,
// so connection liveness is evaluated uniformly by Ping/Readiness.
// The password in the DSN is passed only to the driver and never enters logs or error messages.
func Connect(ctx context.Context, cfg config.DatabaseConfig) (*Pool, error) {
	_ = ctx
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger:                 gormlogger.Discard,
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("unwrap sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeMinutes) * time.Minute)
	return &Pool{
		db:    db,
		close: sqlDB.Close,
		ping:  sqlDB.PingContext,
		stats: func() (int, int) { return sqlDB.Stats().OpenConnections, sqlDB.Stats().InUse },
	}, nil
}

// Ping verifies that the pool can establish a working connection.
func (p *Pool) Ping(ctx context.Context) error { return p.ping(ctx) }

// Close closes the underlying connection pool.
func (p *Pool) Close() error { return p.close() }

// Gorm exposes the underlying GORM handle for domain repositories;
// callers must obtain transactions via TxManager and never call Begin directly.
func (p *Pool) Gorm() *gorm.DB { return p.db }

// Begin implements Connector; the returned transaction handle is bound to the request ctx.
func (p *Pool) Begin(ctx context.Context) (Tx, error) {
	gormTx := p.db.WithContext(ctx).Begin()
	if gormTx.Error != nil {
		return nil, gormTx.Error
	}
	return gormTxAdapter{tx: gormTx}, nil
}

type gormTxAdapter struct{ tx *gorm.DB }

func (a gormTxAdapter) Commit(ctx context.Context) error {
	return a.tx.WithContext(ctx).Commit().Error
}

func (a gormTxAdapter) Rollback(ctx context.Context) error {
	return a.tx.WithContext(ctx).Rollback().Error
}

// Stats returns open and in-use connection counts for Readiness diagnostics (contains no sensitive information).
func (p *Pool) Stats() (open, inUse int) { return p.stats() }
