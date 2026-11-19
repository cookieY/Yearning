package schema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func strPtr(s string) *string { return &s }

func TestSchemaCoverageHelpersAndCatalogDiff(t *testing.T) {
	// lockImpact & classifyStatement
	for _, k := range []string{"create_table", "create_index", "alter", "drop", "transaction_control", "create_other"} {
		if lockImpact(k) == "" {
			t.Fatalf("empty lockImpact for %s", k)
		}
	}
	if classifyStatement("   ") != "other" {
		t.Fatal("expected other for blank statement")
	}
	if classifyStatement("CREATE TYPE foo AS ENUM ('a')") != "create_other" {
		t.Fatal("expected create_other")
	}

	// ledgerCreateStmt & maxAppliedVersion & pgcErrMissingTable
	m := Migration{
		Version:    1,
		Statements: []string{"SELECT 1", "CREATE TABLE schema_migrations (version bigint primary key)"},
	}
	if ledgerCreateStmt(m) == "" {
		t.Fatal("expected ledgerCreateStmt match")
	}
	if ledgerCreateStmt(Migration{Version: 1, Statements: []string{"SELECT 1"}}) != "" {
		t.Fatal("expected empty ledgerCreateStmt")
	}
	if maxAppliedVersion([]LedgerRow{{Version: 1, Success: true}, {Version: 3, Success: false}, {Version: 2, Success: true}}) != 2 {
		t.Fatal("expected maxAppliedVersion 2")
	}
	if !IsUndefinedTable(pgcErrMissingTable(&pgconn.PgError{Code: "42P01"})) {
		t.Fatal("expected errUndefinedTable for 42P01")
	}
	if pgcErrMissingTable(&pgconn.PgError{Code: "23505"}) != nil {
		t.Fatal("expected nil for non-42P01")
	}

	// Analyze filename mismatch branch
	_, probs := Analyze(
		[]Migration{{Version: 1, Filename: "000001_new.up.sql", SHA256: "aa"}},
		[]LedgerRow{{Version: 1, Filename: "000001_old.up.sql", SHA256: "aa", Success: true}},
	)
	if len(probs) == 0 {
		t.Fatal("expected problem for filename mismatch")
	}

	// LoadCatalogContract
	if _, err := LoadCatalogContract("/nonexistent/catalog.json"); err == nil {
		t.Fatal("expected error on missing file")
	}
	tmpDir := t.TempDir()
	badPath := filepath.Join(tmpDir, "bad.json")
	_ = os.WriteFile(badPath, []byte("{bad"), 0o600)
	if _, err := LoadCatalogContract(badPath); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
	goodPath := filepath.Join(tmpDir, "good.json")
	_ = os.WriteFile(goodPath, []byte(`{"source":"test","tables":[{"table":"users","columns":[{"ordinal_position":1,"column":"id","database_type":"uuid","nullable":false,"primary_key_position":1}]}]}`), 0o600)
	cat, err := LoadCatalogContract(goodPath)
	if err != nil || len(cat.Tables) != 1 {
		t.Fatalf("unexpected LoadCatalogContract result: %v", err)
	}

	// diffCatalog & diffTable across all column diff branches
	contract := []TableFact{
		{
			Table: "t_missing",
			Columns: []ColumnFact{
				{OrdinalPosition: 1, Column: "id", DatabaseType: "uuid"},
			},
		},
		{
			Table: "t_diff",
			Columns: []ColumnFact{
				{OrdinalPosition: 1, Column: "id", DatabaseType: "uuid", Nullable: false, DefaultExpression: strPtr("gen_random_uuid()"), PrimaryKeyPosition: 1},
				{OrdinalPosition: 2, Column: "name", DatabaseType: "text", Nullable: true, DefaultExpression: nil, PrimaryKeyPosition: 0},
				{OrdinalPosition: 3, Column: "dropped_col", DatabaseType: "int"},
			},
		},
	}
	live := []TableFact{
		{
			Table: "t_diff",
			Columns: []ColumnFact{
				{OrdinalPosition: 2, Column: "id", DatabaseType: "text", Nullable: true, DefaultExpression: strPtr("now()"), PrimaryKeyPosition: 0},
				{OrdinalPosition: 2, Column: "name", DatabaseType: "text", Nullable: true, DefaultExpression: strPtr("'anon'"), PrimaryKeyPosition: 0},
				{OrdinalPosition: 4, Column: "extra_col", DatabaseType: "int"},
			},
		},
		{
			Table: "t_extra",
		},
	}
	diff := diffCatalog(contract, live)
	if len(diff.MissingTables) != 1 || len(diff.ExtraTables) != 1 || len(diff.TableDiffs) != 1 {
		t.Fatalf("unexpected diffCatalog result: %+v", diff)
	}
	td := diff.TableDiffs[0]
	if len(td.MissingColumns) != 1 || len(td.ExtraColumns) != 1 || len(td.ColumnDiffs) < 5 {
		t.Fatalf("unexpected TableDiffItem: %+v", td)
	}
}
