//go:build integration

package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestAdapterSuite_PostgresSearchPathProbeResolveAndApply(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only search_path behavior")
		}
		ctx := context.Background()
		snapshot, err := database.ProbePostgresSearchPath(ctx, env.exec)
		if err != nil {
			t.Fatalf("ProbePostgresSearchPath: %v", err)
		}
		if err := snapshot.Validate(); err != nil {
			t.Fatalf("probed snapshot: %v", err)
		}
		if snapshot.CurrentUser == "" || snapshot.CreationSchema == "" ||
			len(snapshot.Explicit) == 0 || len(snapshot.Effective) == 0 {
			t.Fatalf("incomplete probe: %+v", snapshot)
		}
		if _, err := database.ProbePostgresSearchPath(ctx, env.review); !errors.Is(err, database.ErrPostgresSearchPathUnavailable) {
			t.Fatalf("review pool probe error = %v", err)
		}

		suffix := time.Now().UnixNano()
		first := fmt.Sprintf("path_first_%d", suffix)
		second := fmt.Sprintf("path_second_%d", suffix)
		for _, schema := range []string{first, second} {
			quoted, err := env.adapter.QuoteIdentifier(schema)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
			})
		}
		firstQ, _ := env.adapter.QuoteIdentifier(first)
		secondQ, _ := env.adapter.QuoteIdentifier(second)
		if _, err := env.admin.ExecContext(ctx,
			"CREATE VIEW "+firstQ+`.shadow AS SELECT 1 AS id`); err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE TABLE "+secondQ+`.shadow (id BIGINT)`); err != nil {
			t.Fatal(err)
		}

		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    snapshot.CurrentUser,
			Configured:     first + ", " + second,
			Explicit:       []string{first, second},
			Effective:      []string{"pg_catalog", first, second},
			CreationSchema: first,
		}
		relation, found, err := database.ResolvePostgresRelation(ctx, env.review, path, "shadow")
		if err != nil || !found {
			t.Fatalf("resolve shadow: relation=%+v found=%v err=%v", relation, found, err)
		}
		if relation.Schema != first || relation.Kind != database.PostgresRelationView {
			t.Fatalf("earlier view must shadow later table: %+v", relation)
		}

		if _, err := env.admin.ExecContext(ctx,
			"DROP VIEW "+firstQ+`.shadow`); err != nil {
			t.Fatal(err)
		}
		relation, found, err = database.ResolvePostgresRelation(ctx, env.review, path, "shadow")
		if err != nil || !found || relation.Schema != second ||
			relation.Kind != database.PostgresRelationTable {
			t.Fatalf("resolve later table: relation=%+v found=%v err=%v", relation, found, err)
		}
		if _, found, err = database.ResolvePostgresRelation(ctx, env.review, path, "absent_relation"); err != nil || found {
			t.Fatalf("complete absence: found=%v err=%v", found, err)
		}

		ddlSQL := "CREATE TABLE path_ddl (id BIGINT)"
		if _, err := database.ExecuteDDLWithSearchPath(
			ctx, env.adapter, env.exec, []string{second}, ddlSQL,
		); err != nil {
			t.Fatalf("DDL with search path: %v", err)
		}
		var exists bool
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NOT NULL`, second+".path_ddl",
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("DDL target missing: exists=%v err=%v", exists, err)
		}

		tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.ApplyTxSearchPath(ctx, env.exec, tx, []string{second}); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatalf("ApplyTxSearchPath: %v", err)
		}
		if _, err := env.adapter.ExecuteDMLStatement(
			ctx, tx, database.DMLStatement{SQL: "INSERT INTO path_ddl (id) VALUES (1)"},
		); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := env.adapter.CommitDMLTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := env.admin.QueryRowContext(ctx,
			"SELECT count(*) FROM "+secondQ+".path_ddl",
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("DML target count=%d err=%v", count, err)
		}

		beforeSendAborted := fmt.Sprintf("path_abort_%d", suffix)
		beforeSendSQL := "CREATE TABLE " + beforeSendAborted + " (id BIGINT)"
		callbackErr := errors.New("before-send callback rejected")
		if _, err := database.ExecuteDDLWithSearchPathBeforeSend(
			ctx, env.adapter, env.exec, []string{second}, beforeSendSQL,
			func(context.Context) error { return callbackErr },
		); !errors.Is(err, callbackErr) {
			t.Fatalf("before-send callback error = %v, want %v", err, callbackErr)
		}
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NULL`, second+"."+beforeSendAborted,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("DDL was sent after callback rejection: absent=%v err=%v", exists, err)
		}
	})
}

func TestAdapterSuite_PostgresExactTransactionInspectionRejectsTemporaryShadow(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only exact transaction inspection")
		}
		ctx := context.Background()
		snapshot, err := database.ProbePostgresSearchPath(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		suffix := time.Now().UnixNano()
		first := fmt.Sprintf("inspect_first_%d", suffix)
		second := fmt.Sprintf("inspect_second_%d", suffix)
		firstQ, _ := env.adapter.QuoteIdentifier(first)
		secondQ, _ := env.adapter.QuoteIdentifier(second)
		for _, schema := range []string{first, second} {
			quoted, _ := env.adapter.QuoteIdentifier(schema)
			if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(
					context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE",
				)
			})
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE TABLE "+firstQ+`.orders (id BIGINT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE TABLE "+secondQ+`.orders (id BIGINT)`); err != nil {
			t.Fatal(err)
		}
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    snapshot.CurrentUser,
			Configured:     first + ", " + second,
			Explicit:       []string{first, second},
			Effective:      []string{"pg_catalog", first, second},
			CreationSchema: first,
		}

		tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.ApplyTxSearchPath(
			ctx, env.exec, tx, path.Explicit,
		); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{
			SQL: "CREATE TEMP TABLE orders (id BIGINT)",
		}); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, _, _, err := database.InspectPostgresDMLTransaction(
			ctx, env.exec, tx, path, []string{"orders"}, nil,
		); !errors.Is(err, database.ErrPostgresSearchPathUnavailable) {
			_, _ = tx.Rollback(ctx)
			t.Fatalf("temporary shadow inspection error = %v, want unavailable", err)
		}
		if _, err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}

		tx, err = env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.ApplyTxSearchPath(
			ctx, env.exec, tx, path.Explicit,
		); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		observedPath, lookups, _, err := database.InspectPostgresDMLTransaction(
			ctx, env.exec, tx, path, []string{"orders"}, nil,
		)
		if err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if !path.Equal(observedPath) || len(lookups) != 1 ||
			lookups[0].PresentBySchema[first] != database.PostgresRelationTable ||
			lookups[0].OIDBySchema[first] == 0 {
			_, _ = tx.Rollback(ctx)
			t.Fatalf("exact transaction inspection = path:%+v lookups:%+v",
				observedPath, lookups)
		}
		if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{
			SQL: "INSERT INTO orders (id) VALUES (1)",
		}); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := env.adapter.CommitDMLTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
		var inserted int
		if err := env.admin.QueryRowContext(ctx,
			"SELECT count(*) FROM "+firstQ+".orders",
		).Scan(&inserted); err != nil || inserted != 1 {
			t.Fatalf("insert after inspection count=%d err=%v", inserted, err)
		}

		// A submitted batch can remove the first matching relation and reveal
		// the reviewed later relation for the next statement. Both statements
		// run on this exact transaction and connection.
		tx, err = env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.ApplyTxSearchPath(
			ctx, env.exec, tx, path.Explicit,
		); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{
			SQL: "DROP TABLE " + firstQ + ".orders",
		}); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		_, revealed, _, err := database.InspectPostgresDMLTransaction(
			ctx, env.exec, tx, path, []string{"orders"}, nil,
		)
		if err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if len(revealed) != 1 ||
			revealed[0].PresentBySchema[first] != "" ||
			revealed[0].PresentBySchema[second] != database.PostgresRelationTable {
			_, _ = tx.Rollback(ctx)
			t.Fatalf("ordered DROP did not reveal later relation: %+v", revealed)
		}
		if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{
			SQL: "INSERT INTO orders (id) VALUES (2)",
		}); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := env.adapter.CommitDMLTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if err := env.admin.QueryRowContext(ctx,
			"SELECT count(*) FROM "+secondQ+".orders",
		).Scan(&inserted); err != nil || inserted != 1 {
			t.Fatalf("ordered DROP reveal insert count=%d err=%v", inserted, err)
		}
	})
}

func TestAdapterSuite_PostgresSearchPathTimeoutFailsClosed(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only search_path behavior")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		if _, err := database.ProbePostgresSearchPath(ctx, env.exec); err == nil {
			t.Fatal("probe deadline must fail closed")
		}
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    env.target.user,
			Configured:     "public",
			Explicit:       []string{"public"},
			Effective:      []string{"pg_catalog", "public"},
			CreationSchema: "public",
		}
		if _, err := database.LookupPostgresRelations(
			ctx, env.review, path, []string{"deadline_probe"},
		); err == nil {
			t.Fatal("catalog deadline must fail closed")
		}
	})
}

func TestAdapterSuite_PostgresDDLConnectionInspectionAndOrderedCreate(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only DDL connection inspection")
		}
		ctx := context.Background()
		snapshot, err := database.ProbePostgresSearchPath(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		suffix := time.Now().UnixNano()
		schemaName := fmt.Sprintf("ddl_inspect_%d", suffix)
		schemaQ, _ := env.adapter.QuoteIdentifier(schemaName)
		if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+schemaQ); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP SCHEMA IF EXISTS "+schemaQ+" CASCADE",
			)
		})
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    snapshot.CurrentUser,
			Configured:     schemaName,
			Explicit:       []string{schemaName},
			Effective:      []string{"pg_catalog", schemaName},
			CreationSchema: schemaName,
		}
		for _, statement := range []string{
			"CREATE TABLE ordered_one (id BIGINT)",
			"CREATE TABLE ordered_two (id BIGINT)",
		} {
			var inspectedPath database.PostgresSearchPath
			var inspected []database.PostgresRelationLookup
			result, err := database.ExecuteDDLWithSearchPathInspectedBeforeSend(
				ctx, env.adapter, env.exec, path.Explicit, statement,
				func(callbackCtx context.Context, conn *sql.Conn) error {
					var inspectErr error
					inspectedPath, inspected, _, inspectErr =
						database.InspectPostgresDDLConnection(
							callbackCtx, conn, path,
							[]string{"ordered_one", "ordered_two"}, nil,
						)
					return inspectErr
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != database.OutcomeSuccess ||
				!path.Equal(inspectedPath) || len(inspected) != 2 {
				t.Fatalf("DDL inspection result=%+v path=%+v lookups=%+v",
					result, inspectedPath, inspected)
			}
		}
	})
}

func TestAdapterSuite_PostgresSearchPathRejectsTempAndTrailingUser(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only search_path behavior")
		}
		base := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    env.target.user,
			Configured:     "public",
			Explicit:       []string{"public"},
			Effective:      []string{"public", "pg_catalog"},
			CreationSchema: "public",
		}
		base.Effective = []string{"pg_temp_4", "public", "pg_catalog"}
		if err := base.Validate(); !errors.Is(err, database.ErrPostgresSearchPathInvalid) {
			t.Fatalf("temporary effective path error = %v", err)
		}
		base.Effective = []string{"pg_catalog", "public"}
		base.CreationSchema = "not_in_path"
		if err := base.Validate(); !errors.Is(err, database.ErrPostgresSearchPathInvalid) {
			t.Fatalf("creation schema outside path error = %v", err)
		}
	})
}

func TestAdapterSuite_PostgresSearchPathBatchLookupAndExplain(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only search_path behavior")
		}
		ctx := context.Background()
		suffix := time.Now().UnixNano()
		first := fmt.Sprintf("batch_first_%d", suffix)
		second := fmt.Sprintf("batch_second_%d", suffix)
		for _, schema := range []string{first, second} {
			quoted, err := env.adapter.QuoteIdentifier(schema)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
			})
		}
		firstQ, _ := env.adapter.QuoteIdentifier(first)
		secondQ, _ := env.adapter.QuoteIdentifier(second)
		for _, statement := range []string{
			"CREATE VIEW " + firstQ + `.shadowed AS SELECT 1 AS id`,
			"CREATE TABLE " + secondQ + `.shadowed (id BIGINT)`,
			"CREATE TABLE " + secondQ + `.target (id BIGINT, amount BIGINT)`,
		} {
			if _, err := env.admin.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    env.target.user,
			Configured:     first + ", " + second,
			Explicit:       []string{first, second},
			Effective:      []string{"pg_catalog", first, second},
			CreationSchema: first,
		}
		lookups, err := database.LookupPostgresRelations(
			ctx, env.review, path, []string{"shadowed", "target", "absent"},
		)
		if err != nil {
			t.Fatalf("LookupPostgresRelations: %v", err)
		}
		if len(lookups) != 3 {
			t.Fatalf("lookup count = %d, want 3", len(lookups))
		}
		byName := map[string]database.PostgresRelationLookup{}
		for _, lookup := range lookups {
			byName[lookup.Relation] = lookup
		}
		if got := byName["shadowed"].PresentBySchema; got[first] != database.PostgresRelationView ||
			got[second] != database.PostgresRelationTable {
			t.Fatalf("shadowed presence = %v", got)
		}
		if got := byName["target"].PresentBySchema; got[second] != database.PostgresRelationTable {
			t.Fatalf("target presence = %v", got)
		}
		if got := byName["absent"].PresentBySchema; len(got) != 0 {
			t.Fatalf("absent presence = %v, want empty", got)
		}
		if _, err := database.LookupPostgresRelations(
			ctx, env.review,
			database.PostgresSearchPath{
				SchemaVersion:  database.PostgresSearchPathVersion,
				CurrentUser:    path.CurrentUser,
				Configured:     path.Configured,
				Explicit:       append([]string(nil), path.Explicit...),
				Effective:      append([]string(nil), path.Effective...),
				CreationSchema: path.CreationSchema,
			},
			[]string{"missing_relation"},
		); err != nil {
			t.Fatalf("complete absence lookup: %v", err)
		}

		fact, err := database.ExplainPostgresWithSearchPath(
			ctx, env.adapter, env.review, path.Explicit,
			"UPDATE target SET amount = amount + 1 WHERE id = 1",
		)
		if err != nil {
			t.Fatalf("ExplainPostgresWithSearchPath: %v", err)
		}
		if fact == nil {
			t.Fatal("explain returned no fact")
		}
	})
}

func TestAdapterSuite_PostgresSearchPathCatalogUsesExecutionRolePrivileges(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only search_path behavior")
		}
		ctx := context.Background()
		suffix := time.Now().UnixNano()
		role := fmt.Sprintf("path_reader_%d", suffix)
		reviewRole := fmt.Sprintf("path_reviewer_%d", suffix)
		reviewPassword := fmt.Sprintf("r%x", uint64(suffix))
		quotedRole, err := env.adapter.QuoteIdentifier(role)
		if err != nil {
			t.Fatal(err)
		}
		quotedReviewRole, err := env.adapter.QuoteIdentifier(reviewRole)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE ROLE "+quotedRole+" NOLOGIN NOINHERIT"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE ROLE "+quotedReviewRole+" LOGIN PASSWORD '"+
				reviewPassword+"' NOINHERIT"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP ROLE IF EXISTS "+quotedRole,
			)
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP ROLE IF EXISTS "+quotedReviewRole,
			)
		})
		quotedDB, err := env.adapter.QuoteIdentifier(env.target.dbname)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"GRANT CONNECT ON DATABASE "+quotedDB+" TO "+quotedReviewRole); err != nil {
			t.Fatal(err)
		}
		reviewPool, err := env.adapter.OpenPurposePool(
			ctx, database.PurposeReview,
			database.Credential{Username: reviewRole, Password: reviewPassword}, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reviewPool.Close() })

		first := fmt.Sprintf("permission_first_%d", suffix)
		second := fmt.Sprintf("permission_second_%d", suffix)
		for _, schema := range []string{first, second} {
			quoted, err := env.adapter.QuoteIdentifier(schema)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.admin.ExecContext(ctx,
				"CREATE SCHEMA "+quoted); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = env.admin.ExecContext(
					context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE",
				)
			})
		}

		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    role,
			Configured:     first + ", " + second,
			Explicit:       []string{first, second},
			Effective:      []string{"pg_catalog", first, second},
			CreationSchema: first,
		}
		if _, err := database.LookupPostgresRelations(
			ctx, reviewPool, path, []string{"permission_probe"},
		); !errors.Is(err, database.ErrPostgresSearchPathUnavailable) {
			t.Fatalf("lookup without execution-role USAGE error = %v, want unavailable", err)
		}
		for _, schema := range []string{first, second} {
			quotedSchema, _ := env.adapter.QuoteIdentifier(schema)
			if _, err := env.admin.ExecContext(ctx,
				"GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.LookupPostgresRelations(
			ctx, reviewPool, path, []string{"permission_probe"},
		); err != nil {
			t.Fatalf("lookup with execution-role USAGE: %v", err)
		}
		missingSchema := path
		missingSchema.Explicit = append([]string(nil), path.Explicit...)
		missingSchema.Explicit = append(missingSchema.Explicit, "path_missing")
		missingSchema.Effective = append([]string(nil), path.Effective...)
		missingSchema.Effective = append(missingSchema.Effective, "path_missing")
		if _, err := database.LookupPostgresRelations(
			ctx, reviewPool, missingSchema, []string{"permission_probe"},
		); !errors.Is(err, database.ErrPostgresSearchPathUnavailable) {
			t.Fatalf("lookup with nonexistent path schema error = %v, want unavailable", err)
		}
	})
}

func TestAdapterSuite_PostgresSearchPathProbesQuotedUserAndExplicitCatalogOrder(t *testing.T) {
	runBothEnginesWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		if env.target.mode != "postgresql" {
			t.Skip("PostgreSQL-only search_path behavior")
		}
		ctx := context.Background()
		suffix := time.Now().UnixNano()
		role := fmt.Sprintf("path_user_%d", suffix)
		quotedRole, err := env.adapter.QuoteIdentifier(role)
		if err != nil {
			t.Fatal(err)
		}
		password := fmt.Sprintf("p%x", uint64(suffix))
		if _, err := env.admin.ExecContext(ctx,
			"CREATE ROLE "+quotedRole+" LOGIN PASSWORD '"+password+"'"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP ROLE IF EXISTS "+quotedRole,
			)
		})
		quotedDB, err := env.adapter.QuoteIdentifier(env.target.dbname)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"GRANT CONNECT ON DATABASE "+quotedDB+" TO "+quotedRole); err != nil {
			t.Fatal(err)
		}

		mixedSchema := fmt.Sprintf("PathCase_%d", suffix)
		quotedSchema, err := env.adapter.QuoteIdentifier(mixedSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range []string{
			"CREATE SCHEMA " + quotedSchema,
			"CREATE SCHEMA " + quotedRole,
			"GRANT USAGE ON SCHEMA " + quotedSchema + " TO " + quotedRole,
			"GRANT USAGE ON SCHEMA " + quotedRole + " TO " + quotedRole,
		} {
			if _, err := env.admin.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE",
			)
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP SCHEMA IF EXISTS "+quotedRole+" CASCADE",
			)
		})
		cases := []struct {
			configured string
			explicit   []string
			effective  []string
		}{
			{
				configured: `"$user", "` + mixedSchema + `"`,
				explicit:   []string{role, mixedSchema},
				effective:  []string{"pg_catalog", role, mixedSchema},
			},
			{
				configured: `"` + mixedSchema + `", pg_catalog, "$user"`,
				explicit:   []string{mixedSchema, "pg_catalog", role},
				effective:  []string{mixedSchema, "pg_catalog", role},
			},
		}
		for _, testCase := range cases {
			if _, err := env.admin.ExecContext(ctx,
				"ALTER ROLE "+quotedRole+" IN DATABASE "+quotedDB+
					" SET search_path = "+testCase.configured); err != nil {
				t.Fatal(err)
			}
			pool, err := env.adapter.OpenPurposePool(
				ctx, database.PurposeExecution,
				database.Credential{Username: role, Password: password}, nil,
			)
			if err != nil {
				t.Fatal("open execution-pool probe fixture failed")
			}
			snapshot, probeErr := database.ProbePostgresSearchPath(ctx, pool)
			_ = pool.Close()
			if probeErr != nil {
				t.Fatalf("probe quoted path: %v", probeErr)
			}
			if snapshot.CurrentUser != role ||
				snapshot.CreationSchema != testCase.explicit[0] {
				t.Fatalf("probe identity = %q/%q, want %q/%q",
					snapshot.CurrentUser, snapshot.CreationSchema,
					role, testCase.explicit[0])
			}
			if !equalIntegrationStrings(snapshot.Explicit, testCase.explicit) {
				t.Fatalf("explicit resolved path = %v, want %v",
					snapshot.Explicit, testCase.explicit)
			}
			if !equalIntegrationStrings(snapshot.Effective, testCase.effective) {
				t.Fatalf("effective resolved path = %v, want %v",
					snapshot.Effective, testCase.effective)
			}
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE TABLE "+quotedSchema+`."CaseSensitiveRelation" (id bigint)`); err != nil {
			t.Fatal(err)
		}
		pool, err := env.adapter.OpenPurposePool(
			ctx, database.PurposeExecution,
			database.Credential{Username: role, Password: password}, nil,
		)
		if err != nil {
			t.Fatal("open quoted-relation probe fixture failed")
		}
		snapshot, probeErr := database.ProbePostgresSearchPath(ctx, pool)
		_ = pool.Close()
		if probeErr != nil {
			t.Fatal("probe quoted-relation fixture failed")
		}
		lookups, lookupErr := database.LookupPostgresRelations(
			ctx, env.review,
			database.PostgresSearchPath{
				SchemaVersion:  snapshot.SchemaVersion,
				CurrentUser:    snapshot.CurrentUser,
				Configured:     snapshot.Configured,
				Explicit:       snapshot.Explicit,
				Effective:      snapshot.Effective,
				CreationSchema: snapshot.CreationSchema,
			},
			[]string{"CaseSensitiveRelation", "casesensitiverelation"},
		)
		if lookupErr != nil {
			t.Fatal("lookup quoted relation failed")
		}
		if lookedUp := lookups[0].PresentBySchema[mixedSchema]; lookedUp != database.PostgresRelationTable {
			t.Fatalf("quoted relation presence = %q, want table", lookedUp)
		}
		if present := lookups[1].PresentBySchema[mixedSchema]; present != "" {
			t.Fatalf("unquoted lowercase relation unexpectedly resolved to %q", present)
		}

		if _, err := env.admin.ExecContext(ctx,
			"ALTER ROLE "+quotedRole+" IN DATABASE "+quotedDB+
				" SET search_path = ''"); err != nil {
			t.Fatal(err)
		}
		pool, err = env.adapter.OpenPurposePool(
			ctx, database.PurposeExecution,
			database.Credential{Username: role, Password: password}, nil,
		)
		if err != nil {
			t.Fatal("open empty-path probe fixture failed")
		}
		_, probeErr = database.ProbePostgresSearchPath(ctx, pool)
		_ = pool.Close()
		if !errors.Is(probeErr, database.ErrPostgresSearchPathUnavailable) {
			t.Fatalf("empty execution path error = %v, want unavailable", probeErr)
		}
	})
}

func equalIntegrationStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
