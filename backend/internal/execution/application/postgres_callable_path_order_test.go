package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/database"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"testing"
)

func TestPostgresCallablePathOrderExecutionRejectsUnsafeBinding(t *testing.T) {
	path := reviewapp.FrozenPostgresSearchPath{Version: 1, CurrentUser: "writer", Configured: "app, pg_catalog, public", Explicit: []string{"app", "pg_catalog", "public"}, Effective: []string{"app", "pg_catalog", "public"}, CreationSchema: "app"}
	binding := reviewapp.FrozenPostgresPathBinding{Version: 3, Path: path, Complete: true, CallablesChecked: true, CallablesSafe: false}
	// A nonnil pool establishes that rejection is due to the unsafe binding,
	// before attempting any connection or sending any statement.
	if err := (&Services{}).verifyPostgresReviewContext(context.Background(), &database.PurposePool{}, path, &binding, 0); err == nil {
		t.Fatal("unsafe path binding accepted at execution")
	}
}
