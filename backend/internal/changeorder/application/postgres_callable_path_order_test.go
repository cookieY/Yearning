package application

import (
	"context"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"testing"
)

func TestPostgresCallablePathOrderSubmitRejectsUnsafeBinding(t *testing.T) {
	path := reviewapp.FrozenPostgresSearchPath{Version: 1, CurrentUser: "writer", Configured: "app, pg_catalog, public", Explicit: []string{"app", "pg_catalog", "public"}, Effective: []string{"app", "pg_catalog", "public"}, CreationSchema: "app"}
	expected := reviewapp.FrozenPostgresReviewContext{Path: path, Binding: reviewapp.FrozenPostgresPathBinding{Version: 3, Path: path, Complete: true, CallablesChecked: true, CallablesSafe: false}}
	if err := (&Services{}).verifyDecodedSubmitPostgresSearchPath(context.Background(), stageFact{}, expected); err == nil {
		t.Fatal("unsafe path binding accepted at submit")
	}
}
