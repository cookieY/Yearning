//go:build integration

package database_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func runBothEnginesWithLoopbackForTest(t *testing.T, fn func(t *testing.T, env *suiteEnv)) {
	t.Helper()
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)
	runBothEngines(t, fn)
}

// TestAdapterSuite_QualifiedIndexAndArrayCatalogFacts covers catalog pieces
// that cannot be represented by a plain list of physical column names.
func TestAdapterSuite_QualifiedIndexAndArrayCatalogFacts(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		table := "r012_catalog_qualified"
		switch env.target.mode {
		case "mysql":
			if _, err := env.admin.ExecContext(ctx,
				"CREATE TABLE `"+env.schema+"`.`"+table+"` ("+
					"id BIGINT, email VARCHAR(100) NOT NULL, labels JSON)"); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx,
				"CREATE INDEX `r012_prefix` ON `"+env.schema+"`.`"+table+"` (email(12))"); err != nil {
				t.Fatal(err)
			}
			var serverVersion string
			if err := env.admin.QueryRowContext(ctx, "SELECT VERSION()").Scan(&serverVersion); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(serverVersion, "5.7") {
				if _, err := env.admin.ExecContext(ctx,
					"CREATE INDEX `r012_functional` ON `"+env.schema+"`.`"+table+"` ((LOWER(email)))"); err != nil {
					t.Fatal(err)
				}
			}
		case "postgresql":
			if _, err := env.admin.ExecContext(ctx,
				`CREATE TABLE "`+env.schema+`"."`+table+`" (`+
					`id BIGINT, email TEXT NOT NULL, labels TEXT[][])`); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx,
				`CREATE INDEX "r012_expr_include" ON "`+env.schema+`"."`+table+`" `+
					`((LOWER(email))) INCLUDE (id)`); err != nil {
				t.Fatal(err)
			}
		}

		columns, err := env.adapter.ListColumns(ctx, env.review, env.schema, table)
		if err != nil {
			t.Fatal(err)
		}
		if env.target.mode == "postgresql" {
			var labels *database.MetadataColumn
			for i := range columns {
				if columns[i].ColumnName == "labels" {
					labels = &columns[i]
				}
			}
			if labels == nil || labels.DatabaseType != "text[][]" {
				t.Fatalf("postgres multidimensional array column = %+v", labels)
			}
		}

		indexes, err := env.adapter.ListIndexes(ctx, env.review, env.schema, table)
		if err != nil {
			t.Fatal(err)
		}
		switch env.target.mode {
		case "mysql":
			prefix := indexNamed(indexes, "r012_prefix")
			if prefix == nil || len(prefix.PrefixLengths) != 1 || prefix.PrefixLengths[0] != 12 {
				t.Fatalf("mysql prefix index = %+v", prefix)
			}
			var version string
			if err := env.admin.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(version, "5.7") {
				functional := indexNamed(indexes, "r012_functional")
				if functional == nil || functional.ExpressionCount != 1 {
					t.Fatalf("mysql functional index = %+v", functional)
				}
			}
		case "postgresql":
			expressionIndex := indexNamed(indexes, "r012_expr_include")
			if expressionIndex == nil || expressionIndex.ExpressionCount != 1 ||
				len(expressionIndex.IncludedColumns) != 1 ||
				expressionIndex.IncludedColumns[0] != "id" {
				t.Fatalf("postgres expression/include index = %+v", expressionIndex)
			}
		}
	})
}

func indexNamed(indexes []database.MetadataIndex, name string) *database.MetadataIndex {
	for i := range indexes {
		if indexes[i].IndexName == name {
			return &indexes[i]
		}
	}
	return nil
}
