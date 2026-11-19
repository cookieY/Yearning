package application

import (
	"context"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestPostgresCallablePathOrderFreezeAndDecode(t *testing.T) {
	for _, order := range []string{"implicit catalog first", "explicit catalog first", "user first"} {
		for _, sql := range []string{`SELECT 1`, `SELECT lower('X')`} {
			t.Run(order+sql, func(t *testing.T) {
				path := testPostgresPath()
				switch order {
				case "explicit catalog first":
					path.Configured = "pg_catalog, app, public"
					path.Explicit = []string{"pg_catalog", "app", "public"}
					path.CreationSchema = "pg_catalog"
				case "user first":
					path.Configured = "app, pg_catalog, public"
					path.Explicit = []string{"app", "pg_catalog", "public"}
					path.Effective = []string{"app", "pg_catalog", "public"}
				}
				if err := path.Validate(); err != nil {
					t.Fatal(err)
				}
				refs, ok := postgresCallableRefs(sql)
				svc := &Services{
					PostgresPathProbe: func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error) { return path, nil },
					PostgresCallableResolver: func(_ context.Context, _ PostgresSearchTarget, _ FrozenPostgresSearchPath, req []database.PostgresCallableRequest) ([]database.PostgresCallableResolution, error) {
						resolutions := make([]database.PostgresCallableResolution, len(req))
						for i, r := range req {
							resolutions[i] = database.PostgresCallableResolution{Request: r, Winners: []database.PostgresCallableWinner{{Schema: "pg_catalog", Signature: "25", ResultType: "25", OID: 870, PgCatalog: true}}}
						}
						return resolutions, nil
					},
				}
				binding, err := svc.freezePostgresStageBinding(context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"}, []postgresAnalyzedStatement{{result: fingerprint.Result{Validity: testStatement(1).Facts}, callables: refs, callablesOK: ok}}, privacy.NewRunMapper())
				if err != nil {
					t.Fatal(err)
				}
				wantSafe := order != "user first"
				if !binding.Complete || !binding.CallablesChecked || binding.CallablesSafe != wantSafe {
					t.Fatalf("complete=%v checked=%v safe=%v wantSafe=%v", binding.Complete, binding.CallablesChecked, binding.CallablesSafe, wantSafe)
				}
				if _, valid := decodeFrozenPostgresPathBinding(binding); !valid {
					t.Fatal("truthful safe/partial binding rejected")
				}
				if !wantSafe {
					forged := binding
					forged.CallablesSafe = true
					if _, valid := decodeFrozenPostgresPathBinding(forged); valid {
						t.Fatal("forged safe binding for user-first path accepted")
					}
				}
			})
		}
	}
}
