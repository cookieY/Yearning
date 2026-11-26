//go:build integration

package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func openTasksIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testkit.OpenPG(t)
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
