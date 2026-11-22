//go:build integration

package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func acquireTasksTestLock(t *testing.T) {
	t.Helper()
	path := os.TempDir() + "/yearningx-itest-shared.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

func openTasksIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	password := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured: set YEARNING_TEST_DATABASE_PASSWORD")
	}
	acquireTasksTestLock(t)
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("YEARNING_TEST_DATABASE_HOST", "localhost"),
		env("YEARNING_TEST_DATABASE_PORT", "54330"),
		env("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		env("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"),
		env("YEARNING_TEST_DATABASE_SSLMODE", "disable"))
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.Exec("truncate table async_tasks").Error; err != nil {
		t.Fatalf("truncate async_tasks: %v", err)
	}
	return db
}

func TestEnqueueCapacityLimitIntegration(t *testing.T) {
	db := openTasksIntegrationDB(t)
	ctx := context.Background()
	defer SetPendingMaximumForTest(3)()

	newTask := func() *AsyncTaskModel {
		now := time.Now().UTC()
		return &AsyncTaskModel{
			ID:           uuid.NewString(),
			Kind:         KindSQLGrouping,
			State:        "queued",
			Input:        []byte(`{}`),
			ProgressUnit: "statements",
			FenceToken:   1,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}

	var firstID string
	for i := 0; i < 3; i++ {
		task := newTask()
		if i == 0 {
			firstID = task.ID
		}
		if err := Enqueue(ctx, db, task); err != nil {
			t.Fatalf("enqueue %d failed: %v", i+1, err)
		}
	}

	// Fourth enqueue must fail closed with ErrQueueFull and insert zero rows.
	rejected := newTask()
	if err := Enqueue(ctx, db, rejected); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("4th enqueue err = %v, want ErrQueueFull", err)
	}
	var count int64
	if err := db.Raw(`select count(*) from async_tasks where state in ('queued', 'running')`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("pending count = %d, want 3", count)
	}

	// Claim and complete one task; subsequent enqueue must succeed.
	claimed, err := Claim(ctx, db, []string{KindSQLGrouping}, "worker-itest", 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != firstID {
		t.Fatalf("claimed id = %s, want %s", claimed.ID, firstID)
	}
	if err := Complete(ctx, db, claimed.ID, claimed.FenceToken, claimed.LeaseOwner, "ok"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := Enqueue(ctx, db, newTask()); err != nil {
		t.Fatalf("enqueue after complete failed: %v", err)
	}
}
