//go:build integration

package database_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestAdapterSuite_PolicyCatalogCompleteSnapshot(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		table := "policy_catalog_orders"
		mixedColumn := "PolicyCaseColumn"
		index := "policy_catalog_owner_idx"
		view := "policy_catalog_orders_view"

		var createTable, createIndex, createView, actualTable, constraint string
		switch env.target.mode {
		case "mysql":
			constraint = "PRIMARY"
			createTable = "CREATE TABLE `" + env.schema + "`.`" + table + "` (" +
				"id BIGINT PRIMARY KEY, owner_name VARCHAR(100), `" + mixedColumn + "` INT)"
			createIndex = "CREATE INDEX `" + index + "` ON `" + env.schema + "`.`" + table + "` (owner_name)"
			createView = "CREATE VIEW `" + env.schema + "`.`" + view + "` AS SELECT id, owner_name FROM `" +
				env.schema + "`.`" + table + "`"
			if _, err := env.admin.ExecContext(ctx, createTable); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx, createIndex); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx, createView); err != nil {
				t.Fatal(err)
			}
			if err := env.admin.QueryRowContext(ctx,
				`SELECT table_name FROM information_schema.tables
				 WHERE table_schema = ? AND lower(table_name) = lower(?)`,
				env.schema, table,
			).Scan(&actualTable); err != nil {
				t.Fatal(err)
			}
		case "postgresql":
			createTable = `CREATE TABLE "` + env.schema + `"."` + table + `" (` +
				`id BIGINT PRIMARY KEY, owner_name TEXT, "` + mixedColumn + `" INTEGER,
				 CONSTRAINT policy_catalog_owner_positive CHECK (id > 0))`
			createIndex = `CREATE INDEX "` + index + `" ON "` + env.schema + `"."` + table + `" (owner_name)`
			createView = `CREATE VIEW "` + env.schema + `"."` + view +
				`" AS SELECT id, owner_name FROM "` + env.schema + `"."` + table + `"`
			if _, err := env.admin.ExecContext(ctx, createTable); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx, createIndex); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx, createView); err != nil {
				t.Fatal(err)
			}
			actualTable = table
			constraint = "policy_catalog_owner_positive"
		}

		catalog, err := database.ReadPolicyCatalog(ctx, env.adapter, env.review, []string{env.schema})
		if err != nil {
			t.Fatalf("ReadPolicyCatalog: %v", err)
		}
		if catalog.SchemaCount != 1 {
			t.Fatalf("schema count = %d", catalog.SchemaCount)
		}
		want := map[database.PolicyCatalogIdentifier]bool{
			{Kind: database.PolicyCatalogSchema, Schema: env.schema, Name: env.schema}:                            false,
			{Kind: database.PolicyCatalogTable, Schema: env.schema, Name: actualTable}:                            false,
			{Kind: database.PolicyCatalogView, Schema: env.schema, Name: view}:                                    false,
			{Kind: database.PolicyCatalogColumn, Schema: env.schema, Relation: actualTable, Name: "id"}:           false,
			{Kind: database.PolicyCatalogColumn, Schema: env.schema, Relation: actualTable, Name: "owner_name"}:   false,
			{Kind: database.PolicyCatalogColumn, Schema: env.schema, Relation: actualTable, Name: mixedColumn}:    false,
			{Kind: database.PolicyCatalogIndex, Schema: env.schema, Relation: actualTable, Name: index}:           false,
			{Kind: database.PolicyCatalogConstraint, Schema: env.schema, Relation: actualTable, Name: constraint}: false,
		}
		for _, identifier := range catalog.Identifiers {
			if identifier.Schema != env.schema {
				t.Fatalf("catalog escaped requested schema: %+v", identifier)
			}
			if identifier.Kind != database.PolicyCatalogKind(strings.ToLower(string(identifier.Kind))) {
				t.Fatalf("kind is not canonical lower-case: %q", identifier.Kind)
			}
			if _, ok := want[identifier]; ok {
				want[identifier] = true
			}
		}
		for identifier, found := range want {
			if !found {
				t.Fatalf("catalog missing identifier %+v", identifier)
			}
		}
		if !catalogHasKind(catalog, database.PolicyCatalogConstraint) {
			t.Fatal("catalog did not include a real constraint")
		}
		if !catalogHasKind(catalog, database.PolicyCatalogIndex) {
			t.Fatal("catalog did not include a real index")
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := database.ReadPolicyCatalog(
			canceled, env.adapter, env.review, []string{env.schema},
		); !errors.Is(err, database.ErrPolicyCatalogUnavailable) {
			t.Fatalf("canceled catalog error = %v", err)
		}
	})
}

func TestAdapterSuite_PolicyCatalogRejectsSchemaCaseAndInjection(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		if err := database.ValidateIdentifier("safe"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ReadPolicyCatalog(
			ctx, env.adapter, env.review, []string{env.schema, env.schema},
		); !errors.Is(err, database.ErrPolicyCatalogMalformed) {
			t.Fatalf("duplicate schema error = %v", err)
		}
		if _, err := database.ReadPolicyCatalog(
			ctx, env.adapter, env.review, []string{env.schema + ";drop"},
		); !errors.Is(err, database.ErrPolicyCatalogMalformed) {
			t.Fatalf("injection schema error = %v", err)
		}
		if _, err := database.ReadPolicyCatalog(
			ctx, env.adapter, env.query, []string{env.schema},
		); !errors.Is(err, database.ErrPolicyCatalogUnsupported) {
			t.Fatalf("non-review pool error = %v", err)
		}

		wrongCase := strings.ToUpper(env.schema)
		if wrongCase == env.schema {
			return
		}
		if _, err := database.ReadPolicyCatalog(
			ctx, env.adapter, env.review, []string{wrongCase},
		); !errors.Is(err, database.ErrPolicyCatalogSchemaUnavailable) {
			t.Fatalf("case-folded schema lookup error = %v, want schema unavailable", err)
		}
	})
}

func TestAdapterSuite_PolicyCatalogVisibilityProof(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		suffix := time.Now().UnixNano()
		user := fmt.Sprintf("pc_%d", suffix)
		password := fmt.Sprintf("Pc_%d_Aa1!", suffix)
		restricted := *env.target
		restricted.user = user
		restricted.password = password
		if env.target.mode == "mysql" {
			restricted.dbname = ""
		}

		switch env.target.mode {
		case "mysql":
			if _, err := env.admin.ExecContext(ctx,
				"CREATE USER '"+user+"'@'%' IDENTIFIED BY '"+password+"'"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+user+"'@'%'")
			})
			if _, err := env.admin.ExecContext(ctx,
				"GRANT USAGE ON `"+env.schema+"`.* TO '"+user+"'@'%'"); err != nil {
				t.Fatal(err)
			}
		case "postgresql":
			if _, err := env.admin.ExecContext(ctx,
				`CREATE ROLE "`+user+`" LOGIN PASSWORD '`+password+`'`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(context.Background(),
					`REVOKE ALL ON SCHEMA "`+env.schema+`" FROM "`+user+`"`)
				_, _ = env.admin.ExecContext(context.Background(),
					`REVOKE CONNECT ON DATABASE "`+env.target.dbname+`" FROM "`+user+`"`)
				_, _ = env.admin.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+user+`"`)
			})
			if _, err := env.admin.ExecContext(ctx,
				`GRANT CONNECT ON DATABASE "`+env.target.dbname+`" TO "`+user+`"`); err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx,
				`REVOKE ALL ON SCHEMA "`+env.schema+`" FROM "`+user+`"`); err != nil {
				t.Fatal(err)
			}
		}

		var adapter database.Adapter
		switch env.target.mode {
		case "mysql":
			adapter = database.NewMySQLAdapter(restricted.endpoint(), database.AdapterOptions{})
		case "postgresql":
			adapter = database.NewPostgreSQLAdapter(restricted.endpoint(), database.AdapterOptions{})
		}
		pool, err := adapter.OpenPurposePool(ctx, database.PurposeReview, restricted.credential(), nil)
		if err != nil {
			_ = adapter.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = pool.Close()
			_ = adapter.Close()
		})

		if _, err := database.ReadPolicyCatalog(
			ctx, adapter, pool, []string{env.schema},
		); !errors.Is(err, database.ErrMetadataVisibilityUnproven) &&
			!errors.Is(err, database.ErrPolicyCatalogSchemaUnavailable) {
			t.Fatalf("unproven metadata visibility error = %v", err)
		}

		switch env.target.mode {
		case "mysql":
			if _, err := env.admin.ExecContext(ctx,
				"GRANT SELECT ON `"+env.schema+"`.* TO '"+user+"'@'%'"); err != nil {
				t.Fatal(err)
			}
		case "postgresql":
			if _, err := env.admin.ExecContext(ctx,
				`GRANT USAGE ON SCHEMA "`+env.schema+`" TO "`+user+`"`); err != nil {
				t.Fatal(err)
			}
		}
		catalog, err := database.ReadPolicyCatalog(ctx, adapter, pool, []string{env.schema})
		if err != nil {
			t.Fatalf("catalog after visibility grant: %v", err)
		}
		if catalog.SchemaCount != 1 || len(catalog.Identifiers) == 0 {
			t.Fatalf("catalog after visibility grant = %+v", catalog)
		}
	})
}

func catalogHasKind(catalog database.PolicyCatalog, kind database.PolicyCatalogKind) bool {
	for _, identifier := range catalog.Identifiers {
		if identifier.Kind == kind {
			return true
		}
	}
	return false
}
