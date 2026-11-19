//go:build integration

package database_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestAdapterSuite_MetadataEstimateCapability(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		exists, err := database.TableExists(ctx, env.adapter, env.review, env.schema, "items")
		if err != nil || !exists {
			t.Fatalf("observed table existence = %v, %v", exists, err)
		}
		exists, err = database.TableExists(ctx, env.adapter, env.review, env.schema, "metadata_missing_table")
		if err != nil || exists {
			t.Fatalf("observed missing table existence = %v, %v", exists, err)
		}

		rows, supported, err := database.TableRowEstimate(ctx, env.adapter, env.review, env.schema, "items")
		if err != nil || !supported {
			t.Fatalf("row estimate capability = rows:%v supported:%v err:%v", rows, supported, err)
		}
		if rows != nil && *rows < 0 {
			t.Fatalf("row estimate must not be negative: %d", *rows)
		}

		rows, supported, err = database.TableRowEstimate(ctx, env.adapter, env.review, env.schema, "metadata_missing_table")
		if err != nil || !supported || rows != nil {
			t.Fatalf("missing table estimate must be supported unknown = rows:%v supported:%v err:%v",
				rows, supported, err)
		}

		indexes, err := env.adapter.ListIndexes(ctx, env.review, env.schema, "items")
		if err != nil {
			t.Fatal(err)
		}
		foundPrimary := false
		for _, index := range indexes {
			if index.Primary {
				foundPrimary = true
				break
			}
		}
		if !foundPrimary {
			t.Fatalf("primary index flag missing: %+v", indexes)
		}
	})
}

// TestAdapterSuite_HiddenMetadataIsVisibilityUnproven proves that a restricted
// review credential cannot convert catalog filtering into a fabricated absence.
// The fixture creates an object the review account cannot see, grants only an
// explicit table-level read on "items", and requires the existence API to keep
// the hidden lookup unknown while the granted object remains observable.
func TestAdapterSuite_HiddenMetadataIsVisibilityUnproven(t *testing.T) {
	runBothEngines(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		user := "r012_" + suffix[len(suffix)-12:]
		password := "R012fixture9aZ"
		hidden := "r012_hidden_table"

		switch env.target.mode {
		case "mysql":
			if _, err := env.admin.ExecContext(ctx,
				"CREATE TABLE `"+env.schema+"`.`"+hidden+"` (id BIGINT PRIMARY KEY)"); err != nil {
				t.Fatalf("create hidden table: %v", err)
			}
			if _, err := env.admin.ExecContext(ctx,
				"CREATE USER '"+user+"'@'%' IDENTIFIED BY '"+password+"'"); err != nil {
				t.Fatalf("create restricted user: %v", err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(context.Background(),
					"DROP USER IF EXISTS '"+user+"'@'%'")
			})
			if _, err := env.admin.ExecContext(ctx,
				"GRANT SELECT ON `"+env.schema+"`.`items` TO '"+user+"'@'%'"); err != nil {
				t.Fatalf("grant visible table: %v", err)
			}
		case "postgresql":
			if _, err := env.admin.ExecContext(ctx,
				`CREATE TABLE "`+env.schema+`"."`+hidden+`" (id BIGINT PRIMARY KEY)`); err != nil {
				t.Fatalf("create hidden table: %v", err)
			}
			if _, err := env.admin.ExecContext(ctx,
				`CREATE ROLE "`+user+`" LOGIN PASSWORD '`+password+`' NOSUPERUSER NOCREATEDB NOCREATEROLE`); err != nil {
				t.Fatalf("create restricted role: %v", err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+user+`"`)
			})
		default:
			t.Fatalf("unsupported adapter fixture %q", env.target.mode)
		}

		restrictedAdapter := env.adapter
		if env.target.mode == "mysql" {
			endpoint := env.target.endpoint()
			endpoint.DatabaseName = env.schema
			restrictedAdapter = database.NewMySQLAdapter(endpoint, database.AdapterOptions{})
			t.Cleanup(func() { _ = restrictedAdapter.Close() })
		}
		pool, err := restrictedAdapter.OpenPurposePool(ctx, database.PurposeReview, database.Credential{
			Username: user,
			Password: password,
		}, nil)
		if err != nil {
			t.Fatalf("open restricted review pool: %v", err)
		}
		t.Cleanup(func() { _ = pool.Close() })

		if env.target.mode == "mysql" {
			visible, err := database.TableExists(ctx, restrictedAdapter, pool, env.schema, "items")
			if err != nil || !visible {
				t.Fatalf("explicitly granted table visibility = %v, %v", visible, err)
			}
		}
		if _, err := database.TableExists(ctx, restrictedAdapter, pool, env.schema, hidden); !errors.Is(
			err, database.ErrMetadataVisibilityUnproven,
		) {
			t.Fatalf("hidden table must stay unproven, got %v", err)
		}
	})
}

func TestAdapterSuite_NonBaseRelationIsNotReportedAbsent(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		viewName := "r012_catalog_view"
		var err error
		switch env.target.mode {
		case "mysql":
			_, err = env.admin.ExecContext(ctx,
				"CREATE VIEW `"+env.schema+"`.`"+viewName+"` AS SELECT id FROM `"+env.schema+"`.`items`")
		case "postgresql":
			_, err = env.admin.ExecContext(ctx,
				`CREATE VIEW "`+env.schema+`"."`+viewName+`" AS SELECT id FROM "`+env.schema+`"."items"`)
		}
		if err != nil {
			t.Fatalf("create same-namespace view: %v", err)
		}
		exists, err := database.TableExists(ctx, env.adapter, env.review, env.schema, viewName)
		if exists || !errors.Is(err, database.ErrUnsupportedRelationType) {
			t.Fatalf("view existence = %v, %v; want unsupported relation", exists, err)
		}
	})
}
