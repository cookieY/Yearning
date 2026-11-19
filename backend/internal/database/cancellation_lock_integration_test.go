//go:build integration

package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// required_suite: cancellation + context_cancellation.
func TestAdapterSuite_QueryCancellation(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		statement := env.fix.Statements["select_items"]
		cursor, err := env.adapter.OpenSelectCursor(ctx, env.query, database.OpenCursorOptions{
			StatementSQL: statement, SchemaName: env.schema, PageSize: 1,
		})
		if err != nil {
			t.Fatalf("open read-only cursor: %v", err)
		}
		if err := env.adapter.CancelQuery(ctx, env.query, cursor); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if _, err := env.adapter.FetchCursorPage(ctx, env.query, cursor, 0); !errors.Is(err, database.ErrCursorExpired) {
			t.Fatalf("cancelled cursor must expire: %v", err)
		}

		// A lock in the fixture's private schema blocks an ordinary SELECT.
		// This tests real driver cancellation without permitting SLEEP/UDFs.
		release := blockFixtureTableReads(t, env)
		defer release()
		deadlineCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			blocked, err := env.adapter.OpenSelectCursor(deadlineCtx, env.query, database.OpenCursorOptions{
				StatementSQL: statement, SchemaName: env.schema, PageSize: 1,
			})
			if err == nil {
				_, err = env.adapter.FetchCursorPage(deadlineCtx, env.query, blocked, 0)
				_ = env.adapter.CloseCursor(context.Background(), env.query, blocked)
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !errors.Is(deadlineCtx.Err(), context.DeadlineExceeded) {
				t.Fatalf("blocked SELECT must reach its deadline: err=%v context=%v", err, deadlineCtx.Err())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("database driver did not cancel the blocked SELECT")
		}
	})
}

func blockFixtureTableReads(t *testing.T, env *suiteEnv) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := env.admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if env.target.mode == "mysql" {
		if _, err := conn.ExecContext(ctx, "LOCK TABLES `"+env.schema+"`.`items` WRITE"); err != nil {
			_ = conn.Close()
			t.Fatalf("fixture lock: %v", err)
		}
		return func() {
			_, err := conn.ExecContext(context.Background(), "UNLOCK TABLES")
			_ = conn.Close()
			if err != nil {
				t.Errorf("release fixture lock: %v", err)
			}
		}
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE "`+env.schema+`"."items" IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = tx.Rollback()
		_ = conn.Close()
		t.Fatalf("fixture lock: %v", err)
	}
	return func() {
		if err := tx.Rollback(); err != nil {
			t.Errorf("release fixture lock: %v", err)
		}
		_ = conn.Close()
	}
}
