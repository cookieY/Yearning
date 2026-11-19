package review

import (
	"os"
	"strings"
	"testing"
)

// TestMigrationWorkbenchGrantsCoverLogin NEW-DEPLOY-13 regression: the contracted
// workbench database role must be able to complete migration-review login, session lookup, and logout;
// missing SELECT on users or write privileges on auth_sessions would cause login to fail.
func TestMigrationWorkbenchGrantsCoverLogin(t *testing.T) {
	raw, err := os.ReadFile("../../../../db/roles/grants.sql")
	if err != nil {
		t.Fatalf("read grants.sql: %v", err)
	}
	sql := strings.ToLower(string(raw))
	for _, want := range []string{
		"grant select on table users to yearning_migration_workbench",
		"grant select, insert, update on table auth_sessions to yearning_migration_workbench",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("grants.sql missing workbench privilege: %q", want)
		}
	}
}
