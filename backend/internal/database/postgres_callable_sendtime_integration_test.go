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

// postgresCallableResolutionIndex keys resolutions by the frozen binding key.
func postgresCallableResolutionIndex(
	resolutions []database.PostgresCallableResolution,
) map[string]database.PostgresCallableResolution {
	out := make(map[string]database.PostgresCallableResolution, len(resolutions))
	for _, resolution := range resolutions {
		out[database.PostgresCallablePayloadKey(
			resolution.Request.Kind, resolution.Request.Name,
		)] = resolution
	}
	return out
}

// postgresCallablesMatch is the send-time comparison the execution layer
// performs: the frozen evidence must equal the freshly resolved evidence.
func postgresCallablesMatch(
	frozen, observed []database.PostgresCallableResolution,
) bool {
	if len(frozen) != len(observed) {
		return false
	}
	index := postgresCallableResolutionIndex(observed)
	for _, expected := range frozen {
		key := database.PostgresCallablePayloadKey(
			expected.Request.Kind, expected.Request.Name,
		)
		actual, ok := index[key]
		if !ok || len(actual.Winners) != len(expected.Winners) {
			return false
		}
		for i := range expected.Winners {
			if actual.Winners[i] != expected.Winners[i] {
				return false
			}
		}
	}
	return true
}

// TestPostgresCallableIdentityDriftFailsClosedBeforeSend proves the send-time
// boundary: with catalog evidence accepted into the snapshot the transaction
// checker sees a callable defined after the freeze, and an inspected DDL whose
// callable identity no longer matches the frozen evidence is never sent.
func TestPostgresCallableIdentityDriftFailsClosedBeforeSend(t *testing.T) {
	runPostgresWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		snapshot, err := database.ProbePostgresSearchPath(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		suffix := time.Now().UnixNano()
		app := fmt.Sprintf("callable_app_%d", suffix)
		quoted, quoteErr := env.adapter.QuoteIdentifier(app)
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE",
			)
		})
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    snapshot.CurrentUser,
			Configured:     app,
			Explicit:       []string{app},
			Effective:      []string{"pg_catalog", app},
			CreationSchema: app,
		}
		requests := []database.PostgresCallableRequest{
			{Kind: database.PostgresCallableFunction, Name: "lower"},
			{Kind: database.PostgresCallableFunction, Name: "count"},
			{Kind: database.PostgresCallableOperator, Name: "@"},
			{Kind: database.PostgresCallableOperator, Name: "="},
		}
		frozen, err := database.ResolvePostgresCallables(ctx, env.exec, path, requests)
		if err != nil {
			t.Fatal(err)
		}
		for _, resolution := range frozen {
			if resolution.Unsafe() || !resolution.Comparable() {
				t.Fatalf("freeze-time callable %v is not provable: %+v",
					resolution.Request, resolution)
			}
		}

		// Control: the same before-send comparison on an unchanged catalog
		// accepts the statement, so the rejection below is caused by drift.
		clean := fmt.Sprintf("callable_clean_%d", suffix)
		cleanSQL := "CREATE TABLE " + clean + " (id BIGINT)"
		var cleanRejected error
		result, err := database.ExecuteDDLWithSearchPathInspectedBeforeSend(
			ctx, env.adapter, env.exec, path.Explicit, cleanSQL,
			func(callbackCtx context.Context, conn *sql.Conn) error {
				_, _, observed, inspectErr := database.InspectPostgresDDLConnection(
					callbackCtx, conn, path, nil, requests,
				)
				if inspectErr != nil {
					return inspectErr
				}
				if !postgresCallablesMatch(frozen, observed) {
					cleanRejected = errors.New("callable identity drift")
					return cleanRejected
				}
				return nil
			},
		)
		if err != nil || result == nil {
			t.Fatalf("clean inspected DDL err=%v result=%+v rejected=%v",
				err, result, cleanRejected)
		}

		// A definition created inside the executing transaction is visible to
		// the transaction inspection, which is the DML send boundary.
		tx, err := env.adapter.BeginDMLTransaction(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.ApplyTxSearchPath(ctx, env.exec, tx, path.Explicit); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		shadowSQL := "CREATE FUNCTION " + quoted + ".lower(uuid) RETURNS integer " +
			"LANGUAGE sql IMMUTABLE AS 'SELECT 42'"
		if _, err := env.adapter.ExecuteDMLStatement(ctx, tx, database.DMLStatement{
			SQL: shadowSQL,
		}); err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		observedPath, _, resolutions, err := database.InspectPostgresDMLTransaction(
			ctx, env.exec, tx, path, nil, requests,
		)
		if err != nil {
			_, _ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if !path.Equal(observedPath) {
			t.Fatalf("transaction path = %+v, want %+v", observedPath, path)
		}
		observedIndex := postgresCallableResolutionIndex(resolutions)
		lower, ok := observedIndex[database.PostgresCallablePayloadKey(
			database.PostgresCallableFunction, "lower",
		)]
		competing := 0
		for _, winner := range lower.Winners {
			if winner.Schema == app && !winner.PgCatalog && winner.OID != 0 {
				competing++
			}
		}
		if !ok || !lower.Unsafe() || competing == 0 {
			t.Fatalf("post-freeze transaction callable not detected: %+v", lower)
		}
		if postgresCallablesMatch(frozen, resolutions) {
			t.Fatal("drifted transaction evidence matched the frozen baseline")
		}

		// The same definition committed after the freeze must abort an
		// inspected DDL before the statement is handed to the driver.
		if _, err := env.admin.ExecContext(ctx, shadowSQL); err != nil {
			t.Fatal(err)
		}
		drifted := fmt.Sprintf("callable_drift_%d", suffix)
		driftedSQL := "CREATE TABLE " + drifted + " (id BIGINT)"
		driftErr := errors.New("postgres callable identity drift")
		result, err = database.ExecuteDDLWithSearchPathInspectedBeforeSend(
			ctx, env.adapter, env.exec, path.Explicit, driftedSQL,
			func(callbackCtx context.Context, conn *sql.Conn) error {
				_, _, observed, inspectErr := database.InspectPostgresDDLConnection(
					callbackCtx, conn, path, nil, requests,
				)
				if inspectErr != nil {
					return inspectErr
				}
				if !postgresCallablesMatch(frozen, observed) {
					return driftErr
				}
				return nil
			},
		)
		if !errors.Is(err, driftErr) {
			t.Fatalf("sent-time callable drift error = %v, want %v", err, driftErr)
		}
		if result != nil && result.Outcome == database.OutcomeSuccess {
			t.Fatalf("drifted DDL was reported successful: %+v", result)
		}
		var absent bool
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NULL`, app+"."+drifted,
		).Scan(&absent); err != nil || !absent {
			t.Fatalf("drifted DDL was sent: absent=%v err=%v", absent, err)
		}
	})
}

// TestPostgresCallableIdentityAbsentNamePostFreezeDriftFailsClosed covers the
// half of the callable contract that the drift test above cannot reach: a name
// that does not exist anywhere on the frozen path at freeze time.
//
// The approved semantics freeze such a name as evidence with zero winners, so
// the statement stays exact (nothing can resolve to it) and the send-time
// re-resolution catches a definition that appears afterwards. This test proves
// both directions with a real catalog:
//
//	control: nothing is ever created            -> send accepted, SQL executed
//	drift:   the name appears after the freeze  -> send refused, SQL never sent
func TestPostgresCallableIdentityAbsentNamePostFreezeDriftFailsClosed(t *testing.T) {
	runPostgresWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		snapshot, err := database.ProbePostgresSearchPath(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		suffix := time.Now().UnixNano()
		app := fmt.Sprintf("callable_absent_%d", suffix)
		quoted, quoteErr := env.adapter.QuoteIdentifier(app)
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE",
			)
		})
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    snapshot.CurrentUser,
			Configured:     app,
			Explicit:       []string{app},
			Effective:      []string{"pg_catalog", app},
			CreationSchema: app,
		}
		const absentName = "h1_absent_callable"
		requests := []database.PostgresCallableRequest{
			{Kind: database.PostgresCallableFunction, Name: absentName},
			{Kind: database.PostgresCallableFunction, Name: "lower"},
		}
		frozen, err := database.ResolvePostgresCallables(ctx, env.exec, path, requests)
		if err != nil {
			t.Fatal(err)
		}
		frozenIndex := postgresCallableResolutionIndex(frozen)
		absent, ok := frozenIndex[database.PostgresCallablePayloadKey(
			database.PostgresCallableFunction, absentName,
		)]
		if !ok || len(absent.Winners) != 0 {
			t.Fatalf("freeze-time evidence for %q must be the empty form: %+v", absentName, absent)
		}
		// The empty form is frozen evidence, not a proof failure: a name with no
		// visible winner cannot be reached by the statement at freeze time.
		if absent.Unsafe() {
			t.Fatalf("empty freeze-time evidence for %q reported unsafe: %+v", absentName, absent)
		}
		known := frozenIndex[database.PostgresCallablePayloadKey(
			database.PostgresCallableFunction, "lower",
		)]
		if len(known.Winners) == 0 || known.Unsafe() {
			t.Fatalf("control callable lower is not provable at freeze time: %+v", known)
		}

		driftErr := errors.New("postgres callable identity drift after freeze")
		send := func(statementSQL string) error {
			_, sendErr := database.ExecuteDDLWithSearchPathInspectedBeforeSend(
				ctx, env.adapter, env.exec, path.Explicit, statementSQL,
				func(callbackCtx context.Context, conn *sql.Conn) error {
					_, _, observed, inspectErr := database.InspectPostgresDDLConnection(
						callbackCtx, conn, path, nil, requests,
					)
					if inspectErr != nil {
						return inspectErr
					}
					if !postgresCallablesMatch(frozen, observed) {
						return driftErr
					}
					return nil
				},
			)
			return sendErr
		}

		// Control: the absent name never appears, so the frozen empty evidence
		// still matches and the statement is sent.
		control := fmt.Sprintf("callable_absent_clean_%d", suffix)
		if err := send("CREATE TABLE " + control + " (id BIGINT)"); err != nil {
			t.Fatalf("control send with an absent name rejected: %v", err)
		}
		var controlExists bool
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NOT NULL`, app+"."+control,
		).Scan(&controlExists); err != nil || !controlExists {
			t.Fatalf("control statement was not executed: exists=%v err=%v", controlExists, err)
		}

		// Drift: the very same name gets defined after the freeze.
		definedSQL := "CREATE FUNCTION " + quoted + "." + absentName +
			"(uuid) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT 7'"
		if _, err := env.admin.ExecContext(ctx, definedSQL); err != nil {
			t.Fatal(err)
		}
		observed, err := database.ResolvePostgresCallables(ctx, env.exec, path, requests)
		if err != nil {
			t.Fatal(err)
		}
		observedAbsent := postgresCallableResolutionIndex(observed)[database.PostgresCallablePayloadKey(
			database.PostgresCallableFunction, absentName,
		)]
		if !observedAbsent.Unsafe() {
			t.Fatalf("post-freeze definition of %q is not visible to the catalog reader: %+v", absentName, observedAbsent)
		}
		if postgresCallablesMatch(frozen, observed) {
			t.Fatalf("post-freeze definition of %q matched the frozen empty evidence", absentName)
		}

		drifted := fmt.Sprintf("callable_absent_drift_%d", suffix)
		if err := send("CREATE TABLE " + drifted + " (id BIGINT)"); !errors.Is(err, driftErr) {
			t.Fatalf("send-time error = %v, want %v: a name absent at freeze but defined afterwards must fail closed", err, driftErr)
		}
		var driftedExists bool
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NOT NULL`, app+"."+drifted,
		).Scan(&driftedExists); err != nil || driftedExists {
			t.Fatalf("drifted statement reached the database: exists=%v err=%v", driftedExists, err)
		}
	})
}

// TestPostgresCallableIdentityKeywordLikePostFreezeFailsClosed covers HI-1 at
// the send-time boundary: LIKE resolves through pg_operator as "~~", and the
// request set derived from a statement whose only callable is LIKE must be
// frozen like any other evidence. When the operator keeps matching only
// pg_catalog the send is allowed; defining the user-side overload after the
// freeze must make the inspected send fail closed and not execute the DDL.
func TestPostgresCallableIdentityKeywordLikePostFreezeFailsClosed(t *testing.T) {
	runPostgresWithLoopbackForTest(t, func(t *testing.T, env *suiteEnv) {
		ctx := context.Background()
		snapshot, err := database.ProbePostgresSearchPath(ctx, env.exec)
		if err != nil {
			t.Fatal(err)
		}
		suffix := time.Now().UnixNano()
		app := fmt.Sprintf("callable_keyword_%d", suffix)
		quoted, quoteErr := env.adapter.QuoteIdentifier(app)
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		if _, err := env.admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.admin.ExecContext(
				context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE",
			)
		})
		path := database.PostgresSearchPath{
			SchemaVersion:  database.PostgresSearchPathVersion,
			CurrentUser:    snapshot.CurrentUser,
			Configured:     app,
			Explicit:       []string{app},
			Effective:      []string{"pg_catalog", app},
			CreationSchema: app,
		}
		// The request set a "uid LIKE 'x%'" statement produces: LIKE is not a
		// token-level operator, PostgreSQL resolves it as pg_operator "~~".
		requests := []database.PostgresCallableRequest{
			{Kind: database.PostgresCallableOperator, Name: "~~"},
		}
		frozen, err := database.ResolvePostgresCallables(ctx, env.exec, path, requests)
		if err != nil {
			t.Fatal(err)
		}
		frozenIndex := postgresCallableResolutionIndex(frozen)
		catalogOnly, ok := frozenIndex[database.PostgresCallablePayloadKey(
			database.PostgresCallableOperator, "~~",
		)]
		if !ok || len(catalogOnly.Winners) == 0 {
			t.Fatalf("freeze-time evidence for operator ~~ is empty: %+v", catalogOnly)
		}
		// Fixture precondition: at freeze time every visible ~~ overload is
		// pg_catalog, so the frozen evidence is the catalog-only form.
		if catalogOnly.Unsafe() {
			t.Fatalf("fixture already exposes a user ~~ overload before the freeze: %+v", catalogOnly)
		}

		driftErr := errors.New("postgres callable identity drift after freeze")
		send := func(statementSQL string) error {
			_, sendErr := database.ExecuteDDLWithSearchPathInspectedBeforeSend(
				ctx, env.adapter, env.exec, path.Explicit, statementSQL,
				func(callbackCtx context.Context, conn *sql.Conn) error {
					_, _, observed, inspectErr := database.InspectPostgresDDLConnection(
						callbackCtx, conn, path, nil, requests,
					)
					if inspectErr != nil {
						return inspectErr
					}
					if !postgresCallablesMatch(frozen, observed) {
						return driftErr
					}
					return nil
				},
			)
			return sendErr
		}

		control := fmt.Sprintf("callable_keyword_clean_%d", suffix)
		if err := send("CREATE TABLE " + control + " (id BIGINT)"); err != nil {
			t.Fatalf("control send with catalog-only ~~ rejected: %v", err)
		}
		var controlExists bool
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NOT NULL`, app+"."+control,
		).Scan(&controlExists); err != nil || !controlExists {
			t.Fatalf("control statement was not executed: exists=%v err=%v", controlExists, err)
		}

		quotedFunction, quoteFnErr := env.adapter.QuoteIdentifier("uuid_pattern")
		if quoteFnErr != nil {
			t.Fatal(quoteFnErr)
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE FUNCTION "+quoted+"."+quotedFunction+
				"(uuid, text) RETURNS boolean LANGUAGE sql IMMUTABLE AS 'SELECT true'",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := env.admin.ExecContext(ctx,
			"CREATE OPERATOR "+quoted+".~~ (leftarg = uuid, rightarg = text, function = "+
				quoted+"."+quotedFunction+")",
		); err != nil {
			t.Fatal(err)
		}

		observed, err := database.ResolvePostgresCallables(ctx, env.exec, path, requests)
		if err != nil {
			t.Fatal(err)
		}
		observedLike := postgresCallableResolutionIndex(observed)[database.PostgresCallablePayloadKey(
			database.PostgresCallableOperator, "~~",
		)]
		if !observedLike.Unsafe() {
			t.Fatalf("post-freeze user ~~ overload is not visible to the catalog reader: %+v", observedLike)
		}
		if postgresCallablesMatch(frozen, observed) {
			t.Fatal("send-time comparison accepted a drift in the keyword-resolved operator")
		}

		drift := fmt.Sprintf("callable_keyword_drift_%d", suffix)
		if err := send("CREATE TABLE " + drift + " (id BIGINT)"); !errors.Is(err, driftErr) {
			t.Fatalf("send-time error = %v, want the drift sentinel after the user ~~ overload appeared", err)
		}
		var driftExists bool
		if err := env.admin.QueryRowContext(ctx,
			`SELECT to_regclass($1) IS NOT NULL`, app+"."+drift,
		).Scan(&driftExists); err != nil {
			t.Fatal(err)
		}
		if driftExists {
			t.Fatalf("statement affected rows despite the drift: %s was created", app+"."+drift)
		}
	})
}
