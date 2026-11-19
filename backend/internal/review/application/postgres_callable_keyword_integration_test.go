//go:build integration

package application

import (
	"context"
	"fmt"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// Exercise the complete production extraction -> freeze -> catalog -> stored
// proof path. The real SQL result first proves each fixture is callable.
func TestPostgresCallableKeywordFreezeCatalog(t *testing.T) {
	conn, schema := callableIdentityFixture(t)
	pool := callableIdentityPool(t)
	path := callableIdentityPath(t, conn, schema)
	q := quoteCallableIdentityIdent(schema)
	callableIdentityExec(t, conn, fmt.Sprintf(`create function %s.truth(uuid,text) returns boolean language sql immutable as $$ select true $$`, q))
	for _, operator := range []string{"~~", "<>", "="} {
		callableIdentityExec(t, conn, fmt.Sprintf(`create operator %s.%s (function = %s.truth, leftarg = uuid, rightarg = text)`, q, operator, q))
	}
	callableIdentityExec(t, conn, fmt.Sprintf(`create function %s."MixedCase"(uuid) returns integer language sql immutable as $$ select 42 $$`, q))
	const uuid = `'00000000-0000-0000-0000-000000000001'::uuid`
	cases := []struct {
		name, sql, proof, callable string
		kind                       database.PostgresCallableKind
		safe                       bool
	}{
		{"like", `SELECT uid LIKE 'x'`, `SELECT ` + uuid + ` LIKE 'x'::text`, "~~", database.PostgresCallableOperator, false},
		{"neq canonical", `SELECT uid != txt`, `SELECT ` + uuid + ` != 'x'::text`, "<>", database.PostgresCallableOperator, false},
		{"nullif equality", `SELECT NULLIF(uid,txt)`, `SELECT NULLIF(` + uuid + `,'x'::text) IS NULL`, "=", database.PostgresCallableOperator, false},
		{"case equality", `SELECT CASE uid WHEN txt THEN true ELSE false END`, `SELECT CASE ` + uuid + ` WHEN 'x'::text THEN true ELSE false END`, "=", database.PostgresCallableOperator, false},
		{"quoted routine", `SELECT "MixedCase"(uid)`, `SELECT "MixedCase"(` + uuid + `) = 42`, "MixedCase", database.PostgresCallableFunction, false},
		{"catalog control", `SELECT lower('X')`, `SELECT lower('X') = 'x'`, "lower", database.PostgresCallableFunction, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var proof bool
			if err := conn.QueryRowContext(context.Background(), tc.proof).Scan(&proof); err != nil || !proof {
				t.Fatalf("fixture proof = %v, error = %v", proof, err)
			}
			refs, ok := postgresCallableRefs(tc.sql)
			svc := &Services{
				PostgresPathProbe: func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error) { return path, nil },
				PostgresCallableResolver: func(ctx context.Context, _ PostgresSearchTarget, frozen FrozenPostgresSearchPath, req []database.PostgresCallableRequest) ([]database.PostgresCallableResolution, error) {
					return database.ResolvePostgresCallables(ctx, pool, databasePostgresPath(frozen), req)
				},
			}
			binding, err := svc.freezePostgresStageBinding(context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"}, []postgresAnalyzedStatement{{result: fingerprint.Result{Validity: testStatement(1).Facts}, callables: refs, callablesOK: ok}}, privacy.NewRunMapper())
			if err != nil {
				t.Fatal(err)
			}
			if !binding.Complete || !binding.CallablesChecked || binding.CallablesSafe != tc.safe {
				t.Fatalf("complete=%v checked=%v safe=%v, want safe=%v", binding.Complete, binding.CallablesChecked, binding.CallablesSafe, tc.safe)
			}
			key := database.PostgresCallablePayloadKey(tc.kind, tc.callable)
			frozen, exists := binding.Callables[key]
			if !exists || len(frozen.Winners) == 0 {
				t.Fatalf("missing stored callable evidence for %s", key)
			}
			if !tc.safe {
				found := false
				for _, winner := range frozen.Winners {
					if winner.Schema == schema {
						found = true
					}
				}
				if !found {
					t.Fatalf("user schema identity absent from stored winners: %+v", frozen.Winners)
				}
			}
		})
	}
}
