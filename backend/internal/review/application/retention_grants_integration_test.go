//go:build integration

// retention_grants_integration_test.go NEW-QUERY-02 role privilege wiring verification
// (-tags integration, executed during central integration by the main agent; skipped in unit tests).
//
// Two assertion layers:
//  1. Static SQL assertion: the "grant select, delete on table (...)" list for yearning_retention in
//     db/roles/grants.sql must cover every managed table in RetentionTargets — keeping service SQL
//     (select ... for update + delete in DeleteExpired) aligned table-for-table with role grants;
//  2. Live PG assertion: has_table_privilege('yearning_retention', managedTable, 'DELETE') is true
//     for each table (skips without false positives when the role or table is absent).
package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

const retentionGrantsSQLPath = "../../../../db/roles/grants.sql"

// retentionGrantsFile resolves the absolute path of grants.sql in the repository (test cwd = package directory).
func retentionGrantsFile(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve test cwd: %v", err)
	}
	return filepath.Join(wd, retentionGrantsSQLPath)
}

// openIntegrationGorm opens a live PG handle via testkit.OpenPG (process-reentrant shared flock).
func openIntegrationGorm(t *testing.T) *gorm.DB {
	t.Helper()
	return testkit.OpenPG(t)
}

// retentionGrantsFor parses the list of tables granted to role in grants.sql.
// Recognizes only grant blocks of the form "grant <privs> on table\n<comma-separated table list>\nto <role>;";
// returns nil if not found (failing the caller per test semantics).
func retentionGrantsFor(t *testing.T, role string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(retentionGrantsFile(t))
	if err != nil {
		t.Fatalf("read grants.sql: %v", err)
	}
	text := string(raw)
	needle := "to " + role + ";"
	out := map[string]bool{}
	for _, block := range strings.Split(text, ";") {
		block = strings.TrimSpace(block)
		if !strings.HasSuffix(block, needle[:len(needle)-1]) {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(block), "grant ") {
			continue
		}
		// " on table" has no trailing space: grants.sql immediately breaks the line and indents the table list
		// after "table", so a needle with a trailing space would never match (the root cause of the first live run failure).
		onIdx := strings.Index(strings.ToLower(block), " on table")
		if onIdx < 0 {
			continue
		}
		list := block[onIdx+len(" on table"):]
		list = strings.TrimSuffix(list, needle[:len(needle)-1])
		for _, name := range strings.Split(list, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				out[strings.ToLower(name)] = true
			}
		}
	}
	return out
}

func TestRetentionTargetsCoveredByGrantsSQL(t *testing.T) {
	granted := retentionGrantsFor(t, "yearning_retention")
	if len(granted) == 0 {
		t.Fatal("grants.sql must contain grant blocks ending at yearning_retention")
	}
	for table := range RetentionTargets {
		if !granted[table] {
			t.Errorf("RetentionTargets table %q missing from grants.sql yearning_retention select/delete list", table)
		}
	}
	// The grant clause must include delete (the service executes DELETE against managed tables).
	raw, err := os.ReadFile(retentionGrantsFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "grant select, delete on table") {
		t.Fatal("yearning_retention grant block must carry select, delete privileges")
	}
}

// TestRetentionRolePrivilegeOnLiveDB live PG assertion (requires YEARNING_TEST_DATABASE_PASSWORD;
// skips if the role or managed tables have not been created).
func TestRetentionRolePrivilegeOnLiveDB(t *testing.T) {
	db := openIntegrationGorm(t)
	for table := range RetentionTargets {
		var exists bool
		if err := db.Raw(
			`select to_regclass(?) is not null`, "public."+table).Scan(&exists).Error; err != nil {
			t.Fatalf("probe table %s: %v", table, err)
		}
		if !exists {
			testenv.SkipOrFatal(t, "table %s absent in integration DB; apply migrations first", table)
		}
		var roleExists bool
		if err := db.Raw(
			`select to_regrole('yearning_retention') is not null`).Scan(&roleExists).Error; err != nil {
			t.Fatalf("probe role: %v", err)
		}
		if !roleExists {
			testenv.SkipOrFatal(t, "role yearning_retention absent in integration DB; apply db/roles first")
		}
		var allowed bool
		if err := db.Raw(
			`select has_table_privilege('yearning_retention', ?, 'DELETE')`, "public."+table).
			Scan(&allowed).Error; err != nil {
			t.Fatalf("has_table_privilege %s: %v", table, err)
		}
		if !allowed {
			t.Errorf("yearning_retention lacks DELETE on %s (grants.sql not applied?)", table)
		}
	}
}
