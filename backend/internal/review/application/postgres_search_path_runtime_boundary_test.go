package application

import (
	"context"
	"errors"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestPostgresRuntimeCallableFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		resolverErr               error
		extractionOK, unsafe      bool
		wantErr                   error
		wantChecked, wantComplete bool
	}{
		{name: "catalog unavailable keeps relation proof", resolverErr: errors.New("catalog unavailable"), extractionOK: true, wantComplete: true},
		{name: "identity drift aborts freeze", resolverErr: ErrFactSourceIdentityChanged, extractionOK: true, wantErr: ErrFactSourceIdentityChanged},
		{name: "unprovable extraction never queries catalog", extractionOK: false, wantComplete: true},
		{name: "unsafe statement stays partial", extractionOK: true, unsafe: true, wantChecked: true, wantComplete: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := 0
			svc := &Services{
				PostgresPathProbe: func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error) {
					return testPostgresPath(), nil
				},
				PostgresCallableResolver: func(_ context.Context, _ PostgresSearchTarget, _ FrozenPostgresSearchPath, requests []database.PostgresCallableRequest) ([]database.PostgresCallableResolution, error) {
					called++
					if tc.resolverErr != nil {
						return nil, tc.resolverErr
					}
					return []database.PostgresCallableResolution{{Request: requests[0]}}, nil
				},
			}
			binding, err := svc.freezePostgresStageBinding(context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"}, []postgresAnalyzedStatement{{result: fingerprint.Result{Validity: testStatement(1).Facts}, callables: []postgresCallableRef{{Kind: database.PostgresCallableFunction, Name: "absent_fn"}}, callablesOK: tc.extractionOK, pathUnsafe: tc.unsafe}}, privacy.NewRunMapper())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error=%v want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if binding.Complete != tc.wantComplete || binding.CallablesChecked != tc.wantChecked {
				t.Fatalf("complete=%v checked=%v", binding.Complete, binding.CallablesChecked)
			}
			if !tc.wantChecked && binding.CallablesSafe {
				t.Fatal("unproven callables became safe")
			}
			if !tc.extractionOK && called != 0 {
				t.Fatal("incomplete extraction invoked catalog")
			}
		})
	}
}

func TestPostgresRuntimeRejectsUnboundRelationBeforeCatalog(t *testing.T) {
	svc := &Services{PostgresPathProbe: func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error) {
		return testPostgresPath(), nil
	}}
	_, err := svc.freezePostgresStageBinding(context.Background(), frozenDatasourceTarget{CompatibilityMode: "postgresql"}, []postgresAnalyzedStatement{{objects: []fingerprint.ObjectReference{{Kind: string(privacy.KindTable), Token: "unknown_table"}}}}, privacy.NewRunMapper())
	if !errors.Is(err, ErrFactSourceUnavailable) {
		t.Fatalf("unbound relation error=%v", err)
	}
}

func TestPostgresRuntimeCallableBatchProofRequiresEveryStatement(t *testing.T) {
	ref := postgresCallableRef{Kind: database.PostgresCallableFunction, Name: "lower"}
	stmts := []postgresAnalyzedStatement{{callables: []postgresCallableRef{ref, ref}, callablesOK: true}, {callables: []postgresCallableRef{ref}, callablesOK: true}}
	got, ok := postgresCallableRequests(stmts)
	if !ok || len(got) != 1 || got[0].Name != "lower" {
		t.Fatalf("deduplicated requests=%v complete=%v", got, ok)
	}
	incomplete := append(append([]postgresAnalyzedStatement(nil), stmts...), postgresAnalyzedStatement{callablesOK: false})
	if got, ok := postgresCallableRequests(incomplete); ok || got != nil {
		t.Fatalf("partial batch returned proof: %v %v", got, ok)
	}
}

func TestPostgresRuntimeBoundsRelationsAndRejectsDuplicateBaseline(t *testing.T) {
	mapper := privacy.NewRunMapper()
	token, err := mapper.Token(privacy.KindTable, "orders")
	if err != nil {
		t.Fatal(err)
	}
	// 100k statements referencing one distinct table must succeed.
	statements := make([]postgresAnalyzedStatement, 100000)
	for i := range statements {
		statements[i].objects = []fingerprint.ObjectReference{{Kind: string(privacy.KindTable), Token: token}}
	}
	if got, err := postgresRelationRequests(statements, mapper); err != nil || len(got) != 100000 {
		t.Fatalf("100k single-table statements rejected: count=%d err=%v", len(got), err)
	}
	// >100k statements must fail closed.
	tooManyStmts := make([]postgresAnalyzedStatement, 100001)
	tooManyStmts[0].objects = []fingerprint.ObjectReference{{Kind: string(privacy.KindTable), Token: token}}
	if got, err := postgresRelationRequests(tooManyStmts, mapper); !errors.Is(err, ErrFactSourceUnavailable) || got != nil {
		t.Fatalf("oversized statement batch accepted: count=%d err=%v", len(got), err)
	}
	// >50k distinct table names must fail closed.
	manyRefs := make([]fingerprint.ObjectReference, 0, postgresSearchPathMaxResolvedTables+1)
	for i := 0; i <= postgresSearchPathMaxResolvedTables; i++ {
		tok, terr := mapper.Token(privacy.KindTable, "tbl_"+string(rune('a'+i%26))+"_"+string(rune('0'+(i/26)%10))+"_"+string(rune('a'+(i/260)%26))+"_"+string(rune('a'+(i/6760)%26)))
		if terr != nil {
			t.Fatal(terr)
		}
		manyRefs = append(manyRefs, fingerprint.ObjectReference{Kind: string(privacy.KindTable), Token: tok})
	}
	if got, err := postgresRelationRequests([]postgresAnalyzedStatement{{objects: manyRefs}}, mapper); !errors.Is(err, ErrFactSourceUnavailable) || got != nil {
		t.Fatalf("oversized distinct tables accepted: count=%d err=%v", len(got), err)
	}
	values := []PostgresRelationBaseline{{TableName: "one", PresentBySchema: map[string]string{}}, {TableName: "one", PresentBySchema: map[string]string{}}}
	if got, err := validatePostgresRelationBaselines(testPostgresPath(), []string{"one", "two"}, values); !errors.Is(err, ErrFactSourceUnavailable) || got != nil {
		t.Fatalf("duplicate baseline accepted: %v %v", got, err)
	}
}
