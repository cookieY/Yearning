//go:build integration

// H1 callable identity red-line regression (BE-B7 independent review High-1).
//
// Defect: the review side previously judged callable safety solely from the search_path string —
// assuming that if the first non-pg_temp schema in Effective was pg_catalog, any unqualified function
// or operator would necessarily resolve to pg_catalog. Real PostgreSQL resolves callables by
// pg_proc/pg_operator catalog identity (name + argument types) across each schema in the effective
// search_path in order, so all four scenarios below bypassed that string check:
//
//	S1 pg_catalog is first, but a user defines a same-named function in a later schema with a signature
//	   absent from pg_catalog (pg_catalog has lower(text) but not lower(uuid), so unqualified lower(uuid) resolves to the user schema);
//	S2 schema-qualified single-character custom operator (the adjacent two-punctuation heuristic cannot see a single `@`);
//	S3 same-named overloads with different signatures, where final identity depends on argument types
//	   that token-level analysis cannot determine;
//	S4 a function created after freeze — the frozen binding carries no callable identity evidence,
//	   so pre-send re-verification must reject it rather than silently reusing it.
//
// Each scenario first proves with real catalog/execution evidence that the path is genuinely dangerous,
// then asserts that the review contract fails closed (binding.CallablesSafe == false) while keeping
// relation completeness (Complete) unaffected: H1 may not be closed by marking everything partial.
package application

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
)

// callableIdentityFixture creates an isolated schema on a real PostgreSQL instance and sets the session
// search_path to "<schema>", public.
//
// Why not `"<schema>", pg_catalog, public`: in PostgreSQL search_path, pg_catalog is implicitly placed
// first unless explicitly listed, which is the exact deployment shape covered by S1/S3.
// The fixture target comes from T0 yearning-pg-verify (54331); skips rather than fails when the
// password is unconfigured to preserve the same skip semantics as other integration tests in this package.
func callableIdentityFixture(t *testing.T) (*sql.Conn, string) {
	t.Helper()

	cfg := testenv.RequireTargetPG(t, testenv.WithDefaultHost("127.0.0.1"), testenv.WithDefaultPort(54331))
	db, err := sql.Open("pgx", cfg.LibpqDSN())
	if err != nil {
		t.Fatalf("open target PostgreSQL: %v", err)
	}
	// LIFO cleanup: register db.Close first so it runs last; schema cleanup and conn.Close are registered next so they run first.
	t.Cleanup(func() { _ = db.Close() })

	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Fatalf("target PostgreSQL unreachable: %v", err)
	}

	schema := fmt.Sprintf("h1_callable_%d", time.Now().UnixNano())
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire fixture connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	callableIdentityExec(t, conn, fmt.Sprintf(`create schema %s`, quoteCallableIdentityIdent(schema)))
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), fmt.Sprintf(`drop schema if exists %s cascade`, quoteCallableIdentityIdent(schema)))
	})
	callableIdentityExec(t, conn, fmt.Sprintf(`set search_path = %s, public`, quoteCallableIdentityIdent(schema)))
	return conn, schema
}

func quoteCallableIdentityIdent(name string) string {
	return `"` + name + `"`
}

func callableIdentityExec(t *testing.T, conn *sql.Conn, statement string) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("fixture statement failed: %v\nSQL: %s", err, statement)
	}
}

// callableIdentityQuery executes a single-row query and requires a non-empty row.
func callableIdentityQuery(t *testing.T, conn *sql.Conn, statement string) *sql.Row {
	t.Helper()
	return conn.QueryRowContext(context.Background(), statement)
}

// callableIdentityPool opens a review-purpose pool on the same fixture target
// so the test can resolve callables through the production catalog reader
// instead of asserting on a hard-coded failure.
//
// The fixture target is loopback (127.0.0.1), which production addrguard rejects; use the repository's
// existing test hook to allow loopback deterministically (same approach as runBothEnginesWithLoopbackForTest
// in package database) without requiring callers to set extra environment variables.
func callableIdentityPool(t *testing.T) *database.PurposePool {
	t.Helper()

	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	cfg := testenv.RequireTargetPG(t, testenv.WithDefaultHost("127.0.0.1"), testenv.WithDefaultPort(54331))
	adapter := database.NewPostgreSQLAdapter(database.Endpoint{
		Host:             cfg.Host,
		Port:             cfg.Port,
		DatabaseName:     cfg.DBName,
		CompatibilityMod: "postgresql",
	}, database.AdapterOptions{})
	pool, err := adapter.OpenPurposePool(context.Background(), database.PurposeReview,
		database.Credential{
			Username: cfg.User,
			Password: cfg.Password,
		}, nil)
	if err != nil {
		t.Fatalf("open review pool on the fixture target: %v", err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		_ = adapter.Close()
	})
	return pool
}

// assertCallableIdentityResolvesUnsafe is the shared review contract assertion for S1-S3, in three steps:
//
//  1. Resolve the statement's callables on the frozen effective search_path using the production
//     catalog reader database.ResolvePostgresCallables, and require a real winner outside pg_catalog
//     visible to the target role — otherwise the fixture failed to reproduce H1 and the test fails
//     immediately (blocking false greens where empty evidence could still pass).
//  2. Pass those real catalog facts to preparePostgresObjectBindingsWithCallables and assert that the
//     frozen binding has CallablesSafe == false and CallablesChecked == true.
//  3. Relation completeness (Complete) must remain unaffected; H1 may not be closed by forcing everything to partial.
//
// Also asserts that the unsafe evidence is actually recorded in the binding (binding.Callables matches
// the resolution result 1-to-1), so S1-S3 can pass only when pg_proc/pg_operator resolution is active.
func assertCallableIdentityResolvesUnsafe(
	t *testing.T,
	pool *database.PurposePool,
	path FrozenPostgresSearchPath,
	requests []database.PostgresCallableRequest,
	scenario, evidence string,
) {
	t.Helper()

	if err := path.Validate(); err != nil {
		t.Fatalf("%s: fixture path is invalid: %v", scenario, err)
	}
	// The path string alone reveals no issue: this is the exact root cause of H1, pinned here as control evidence.
	if !path.CallableLookupSafe() {
		t.Fatalf("%s: path.CallableLookupSafe() = false; the regression scenario requires a path that the string-level check currently accepts (otherwise the test proves nothing)", scenario)
	}

	resolutions, err := database.ResolvePostgresCallables(
		context.Background(), pool, databasePostgresPath(path), requests)
	if err != nil {
		t.Fatalf("%s: resolve callables on the frozen effective path: %v", scenario, err)
	}
	if len(resolutions) != len(requests) {
		t.Fatalf("%s: resolved %d callables for %d requests", scenario, len(resolutions), len(requests))
	}
	unsafe := 0
	for _, resolution := range resolutions {
		if len(resolution.Winners) == 0 {
			t.Fatalf("%s: %s %q has no visible winner on the frozen path; the fixture must expose a catalog identity",
				scenario, resolution.Request.Kind, resolution.Request.Name)
		}
		if resolution.Unsafe() {
			unsafe++
		}
	}
	if unsafe == 0 {
		t.Fatalf("%s: catalog resolution found no non-pg_catalog winner for %v: %s; fixture did not reproduce H1",
			scenario, requests, evidence)
	}

	binding, complete := preparePostgresObjectBindingsWithCallables(
		path, []StatementValidityAtOrdinal{testStatement(1)}, nil, resolutions, true, nil)
	if !binding.CallablesChecked {
		t.Fatalf("%s: binding.CallablesChecked = false, want true: catalog evidence was supplied but not recorded", scenario)
	}
	if binding.CallablesSafe {
		t.Fatalf("%s: binding.CallablesSafe = true, want false: %s; callable identity must be resolved from pg_proc/pg_operator on the frozen effective search_path (unprovable ⇒ partial + fail closed)", scenario, evidence)
	}
	if !complete || !binding.Complete {
		t.Fatalf("%s: relation binding completeness regressed (complete=%v, binding.Complete=%v); failing closed on callables must not discard relation identity", scenario, complete, binding.Complete)
	}
	if len(binding.Callables) != len(resolutions) {
		t.Fatalf("%s: binding froze %d callables for %d resolutions; resolved catalog facts must be persisted",
			scenario, len(binding.Callables), len(resolutions))
	}
	for _, resolution := range resolutions {
		key := database.PostgresCallablePayloadKey(resolution.Request.Kind, resolution.Request.Name)
		frozen, ok := binding.Callables[key]
		if !ok || len(frozen.Winners) != len(resolution.Winners) {
			t.Fatalf("%s: binding evidence for %s does not mirror the resolved winners: %+v", scenario, key, frozen)
		}
	}
}

func callableIdentityCurrentUser(t *testing.T, conn *sql.Conn) string {
	t.Helper()
	var user string
	if err := conn.QueryRowContext(context.Background(), `select current_user`).Scan(&user); err != nil {
		t.Fatalf("read current_user: %v", err)
	}
	return user
}

func callableIdentityPath(t *testing.T, conn *sql.Conn, schema string) FrozenPostgresSearchPath {
	return FrozenPostgresSearchPath{
		Version:        postgresSearchPathSnapshotVersion,
		CurrentUser:    callableIdentityCurrentUser(t, conn),
		Configured:     `"$user", public`,
		Explicit:       []string{schema, "public"},
		Effective:      []string{"pg_catalog", schema, "public"},
		CreationSchema: schema,
	}
}

// TestPostgresCallableIdentityShadowsLeadingPgCatalogFunction S1:
// pg_catalog is first in the effective search_path, and the user schema defines a same-named signature
// absent from pg_catalog (lower(uuid)), so the unqualified call still resolves to the user schema.
func TestPostgresCallableIdentityShadowsLeadingPgCatalogFunction(t *testing.T) {
	conn, schema := callableIdentityFixture(t)

	callableIdentityExec(t, conn, fmt.Sprintf(
		`create function %s.lower(uuid) returns integer language sql immutable as $$ select 42 $$`,
		quoteCallableIdentityIdent(schema)))

	var visible bool
	if err := callableIdentityQuery(t, conn,
		fmt.Sprintf(`select pg_catalog.pg_function_is_visible(%s::regprocedure)`,
			quoteCallableIdentityLiteral(schema+".lower(uuid)"))).Scan(&visible); err != nil {
		t.Fatalf("read pg_function_is_visible: %v", err)
	}
	if !visible {
		t.Fatalf("%s.lower(uuid) is not visible on the frozen path; fixture did not reproduce H1", schema)
	}

	// Unqualified call: pg_catalog has lower(text) but not lower(uuid), so resolution continues down the path to the user schema.
	var got int
	if err := callableIdentityQuery(t, conn,
		`select lower('00000000-0000-0000-0000-000000000000'::uuid)`).Scan(&got); err != nil {
		t.Fatalf("execute unqualified lower(uuid): %v", err)
	}
	if got != 42 {
		t.Fatalf("unqualified lower(uuid) = %d, want 42: fixture did not make the user schema win over leading pg_catalog", got)
	}

	// Control: the text signature exists in pg_catalog, so under the exact same path it still resolves to pg_catalog.
	var text string
	if err := callableIdentityQuery(t, conn, `select lower('ABC')`).Scan(&text); err != nil {
		t.Fatalf("execute lower(text): %v", err)
	}
	if text != "abc" {
		t.Fatalf("lower('ABC') = %q, want \"abc\"", text)
	}

	assertCallableIdentityResolvesUnsafe(t, callableIdentityPool(t), callableIdentityPath(t, conn, schema),
		[]database.PostgresCallableRequest{
			{Kind: database.PostgresCallableFunction, Name: "lower"},
		},
		"pg_catalog-first path with shadowing lower(uuid)",
		fmt.Sprintf("%s.lower(uuid) is pg_function_is_visible on the frozen path and unqualified lower(uuid) executes it", schema))
}

// TestPostgresCallableIdentitySingleCharCustomOperator S2:
// single-character custom operator (binary `@` with (integer, integer) signature). The existing adjacent
// two-punctuation heuristic only rejects two consecutive unrecognized operator characters, leaving a single `@` outside its view.
func TestPostgresCallableIdentitySingleCharCustomOperator(t *testing.T) {
	conn, schema := callableIdentityFixture(t)

	callableIdentityExec(t, conn, fmt.Sprintf(
		`create function %s.int_add(integer, integer) returns integer language sql immutable as $$ select $1 + $2 $$`,
		quoteCallableIdentityIdent(schema)))
	callableIdentityExec(t, conn, fmt.Sprintf(
		`create operator %s.@ (leftarg = integer, rightarg = integer, function = %s.int_add)`,
		quoteCallableIdentityIdent(schema), quoteCallableIdentityIdent(schema)))

	var visible bool
	if err := callableIdentityQuery(t, conn,
		fmt.Sprintf(`select pg_catalog.pg_operator_is_visible(%s::regoperator)`,
			quoteCallableIdentityLiteral(schema+".@(integer,integer)"))).Scan(&visible); err != nil {
		t.Fatalf("read pg_operator_is_visible: %v", err)
	}
	if !visible {
		t.Fatalf("%s.@(integer,integer) is not visible on the frozen path; fixture did not reproduce H1", schema)
	}

	var sum int
	if err := callableIdentityQuery(t, conn, `select 1 @ 2`).Scan(&sum); err != nil {
		t.Fatalf("execute unqualified operator @: %v", err)
	}
	if sum != 3 {
		t.Fatalf("1 @ 2 = %d, want 3: user-schema operator did not take effect", sum)
	}

	assertCallableIdentityResolvesUnsafe(t, callableIdentityPool(t), callableIdentityPath(t, conn, schema),
		[]database.PostgresCallableRequest{
			{Kind: database.PostgresCallableOperator, Name: "@"},
		},
		"pg_catalog-first path with single-character custom operator",
		fmt.Sprintf("%s.@(integer,integer) is pg_operator_is_visible and `1 @ 2` executes it while the adjacent-operator heuristic sees only one operator character", schema))
}

// TestPostgresCallableIdentityOverloadNotProvableByName S3:
// same-named overloads with different signatures whose final identity depends on argument types;
// token-level analysis neither knows argument types nor can prove resolution to pg_catalog from the
// name alone, so it must classify as partial. Also includes an anti-blunt control: a pure pg_catalog path must continue to be judged safe.
func TestPostgresCallableIdentityOverloadNotProvableByName(t *testing.T) {
	conn, schema := callableIdentityFixture(t)

	callableIdentityExec(t, conn, fmt.Sprintf(
		`create function %s.pick(integer) returns text language sql immutable as $$ select 'int'::text $$`,
		quoteCallableIdentityIdent(schema)))
	callableIdentityExec(t, conn, fmt.Sprintf(
		`create function %s.pick(text) returns text language sql immutable as $$ select 'text'::text $$`,
		quoteCallableIdentityIdent(schema)))

	for _, signature := range []string{".pick(integer)", ".pick(text)"} {
		var visible bool
		if err := callableIdentityQuery(t, conn,
			fmt.Sprintf(`select pg_catalog.pg_function_is_visible(%s::regprocedure)`,
				quoteCallableIdentityLiteral(schema+signature))).Scan(&visible); err != nil {
			t.Fatalf("read pg_function_is_visible for %s: %v", signature, err)
		}
		if !visible {
			t.Fatalf("%s%s is not visible on the frozen path; fixture did not reproduce H1", schema, signature)
		}
	}

	var viaInt, viaText string
	if err := callableIdentityQuery(t, conn, `select pick(1)`).Scan(&viaInt); err != nil {
		t.Fatalf("execute pick(integer): %v", err)
	}
	if err := callableIdentityQuery(t, conn, `select pick('x')`).Scan(&viaText); err != nil {
		t.Fatalf("execute pick(text): %v", err)
	}
	if viaInt != "int" || viaText != "text" {
		t.Fatalf("overload resolution = (%q, %q), want (\"int\", \"text\"): the same unqualified name resolves to different catalog identities by argument type", viaInt, viaText)
	}

	// Anti-blunt control: when the path contains only pg_catalog, unqualified callables necessarily resolve
	// to pg_catalog, which must remain safe after the H1 fix (blanket fail-closed is not allowed).
	catalogOnly := FrozenPostgresSearchPath{
		Version:        postgresSearchPathSnapshotVersion,
		CurrentUser:    callableIdentityCurrentUser(t, conn),
		Configured:     `"$user", public`,
		Explicit:       []string{"pg_catalog"},
		Effective:      []string{"pg_catalog"},
		CreationSchema: "pg_catalog",
	}
	if err := catalogOnly.Validate(); err != nil {
		t.Fatalf("pg_catalog-only control path is invalid: %v", err)
	}
	control, ok := preparePostgresObjectBindings(catalogOnly, []StatementValidityAtOrdinal{testStatement(1)}, nil, nil)
	if !ok || !control.Complete || !control.CallablesSafe {
		t.Fatalf("pg_catalog-only path must stay trusted (ok=%v, complete=%v, callables_safe=%v); H1 must fail closed on unprovable identity, not on every user schema", ok, control.Complete, control.CallablesSafe)
	}

	// Resolution control: the same catalog reader resolving lower on a pg_catalog-only path must place
	// the winner in pg_catalog and judge it safe — proving the false result above comes from real catalog
	// facts rather than a resolver that unconditionally reports unsafe.
	controlResolutions, err := database.ResolvePostgresCallables(
		context.Background(), callableIdentityPool(t), databasePostgresPath(catalogOnly),
		[]database.PostgresCallableRequest{
			{Kind: database.PostgresCallableFunction, Name: "lower"},
		})
	if err != nil {
		t.Fatalf("resolve callables on the pg_catalog-only control path: %v", err)
	}
	if len(controlResolutions) != 1 || controlResolutions[0].Unsafe() ||
		len(controlResolutions[0].Winners) == 0 {
		t.Fatalf("pg_catalog-only control resolution = %+v, want a pg_catalog winner", controlResolutions)
	}
	controlBinding, controlComplete := preparePostgresObjectBindingsWithCallables(
		catalogOnly, []StatementValidityAtOrdinal{testStatement(1)}, nil, controlResolutions, true, nil)
	if !controlComplete || !controlBinding.Complete || !controlBinding.CallablesSafe {
		t.Fatalf("resolved pg_catalog-only control must stay trusted (complete=%v, binding.Complete=%v, callables_safe=%v); H1 must fail closed on unprovable identity, not on every user schema",
			controlComplete, controlBinding.Complete, controlBinding.CallablesSafe)
	}

	assertCallableIdentityResolvesUnsafe(t, callableIdentityPool(t), callableIdentityPath(t, conn, schema),
		[]database.PostgresCallableRequest{
			{Kind: database.PostgresCallableFunction, Name: "pick"},
		},
		"pg_catalog-first path with resolvable-by-argument-type overload",
		fmt.Sprintf("%s.pick has integer and text overloads, both pg_function_is_visible; the resolved identity depends on argument types the token-level review does not have", schema))
}

// TestPostgresCallableIdentityPostFreezeDefinitionCannotReuseFrozenBinding S4:
// at freeze time no lower exists in the user schema; after freeze the user defines lower(uuid). Because
// the v2 frozen binding carries no callable identity evidence, pre-send re-verification must reject reuse.
func TestPostgresCallableIdentityPostFreezeDefinitionCannotReuseFrozenBinding(t *testing.T) {
	conn, schema := callableIdentityFixture(t)

	var frozenCount int
	if err := callableIdentityQuery(t, conn, fmt.Sprintf(
		`select count(*) from pg_catalog.pg_proc p join pg_catalog.pg_namespace n on n.oid = p.pronamespace where n.nspname = %s and p.proname = 'lower'`,
		quoteCallableIdentityLiteral(schema))).Scan(&frozenCount); err != nil {
		t.Fatalf("count pg_proc at freeze time: %v", err)
	}
	if frozenCount != 0 {
		t.Fatalf("pg_proc already holds %d 'lower' rows in the fresh schema; fixture cannot represent the freeze snapshot", frozenCount)
	}

	callableIdentityExec(t, conn, fmt.Sprintf(
		`create function %s.lower(uuid) returns integer language sql immutable as $$ select 42 $$`,
		quoteCallableIdentityIdent(schema)))

	var afterFreeze int
	if err := callableIdentityQuery(t, conn, fmt.Sprintf(
		`select count(*) from pg_catalog.pg_proc p join pg_catalog.pg_namespace n on n.oid = p.pronamespace where n.nspname = %s and p.proname = 'lower'`,
		quoteCallableIdentityLiteral(schema))).Scan(&afterFreeze); err != nil {
		t.Fatalf("count pg_proc after freeze: %v", err)
	}
	if afterFreeze != 1 {
		t.Fatalf("pg_proc holds %d 'lower' rows after the post-freeze definition, want 1", afterFreeze)
	}

	// Version 2 is the frozen binding format without callable identity evidence; hard-coded here so this
	// assertion continues to hold after the fix bumps postgresObjectResolutionVersion to 3.
	legacy := map[string]any{
		"version": 2,
		"path": map[string]any{
			"version":         postgresSearchPathSnapshotVersion,
			"current_user":    "verify",
			"configured":      `"$user", public`,
			"explicit":        []string{schema, "public"},
			"effective":       []string{"pg_catalog", schema, "public"},
			"creation_schema": schema,
		},
		"complete":       true,
		"callables_safe": true,
		"baseline":       map[string]any{},
		"relations":      map[string]any{},
	}
	if _, ok := decodeFrozenPostgresPathBinding(legacy); ok {
		t.Fatalf("decodeFrozenPostgresPathBinding accepted a v2 binding with no callable identity proof after %s.lower(uuid) appeared post-freeze; send-time re-verification must reject it (want ok=false)", schema)
	}
}

func quoteCallableIdentityLiteral(value string) string {
	return `'` + value + `'`
}
