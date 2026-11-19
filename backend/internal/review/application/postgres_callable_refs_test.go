package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// postgresTestWinner builds one catalog winner fact for a synthetic resolution.
func postgresTestWinner(
	schema, signature, resultType string, oid uint32,
) database.PostgresCallableWinner {
	return database.PostgresCallableWinner{
		Schema:     schema,
		Signature:  signature,
		ResultType: resultType,
		OID:        oid,
		PgCatalog:  schema == "pg_catalog",
	}
}

// TestPostgresCallableRefsSeparatesCallsFromSyntaxKeywords pins the token-level
// collection rule: IN and quantified comparisons freeze their real operators,
// not invented functions. Other conservative lexical candidates retain absent
// catalog evidence. Explicitly schema-qualified calls do not use path order.
func TestPostgresCallableRefsSeparatesCallsFromSyntaxKeywords(t *testing.T) {
	statement := `update app.orders set note = lower(note)
		where id in (1, 2) and 1 @ 2 = 3 and amount > some (values (1))`

	refs, ok := postgresCallableRefs(statement)
	if !ok {
		t.Fatal("postgresCallableRefs rejected a plain statement")
	}
	functions := map[string]bool{}
	operators := map[string]bool{}
	for _, ref := range refs {
		switch ref.Kind {
		case database.PostgresCallableFunction:
			functions[ref.Name] = true
		case database.PostgresCallableOperator:
			operators[ref.Name] = true
		default:
			t.Fatalf("unexpected callable kind %q", ref.Kind)
		}
	}
	for _, want := range []string{"lower", "values"} {
		if !functions[want] {
			t.Fatalf("function %q missing from %+v", want, refs)
		}
	}
	for _, want := range []string{"@", "=", ">"} {
		if !operators[want] {
			t.Fatalf("operator %q missing from %+v", want, operators)
		}
	}
	if functions["app"] {
		t.Fatalf("schema qualifier collected as a function: %+v", refs)
	}

	qualified, ok := postgresCallableRefs(`select app.lower(note) from app.orders`)
	if !ok {
		t.Fatal("postgresCallableRefs rejected a qualified call")
	}
	for _, ref := range qualified {
		if ref.Kind == database.PostgresCallableFunction && ref.Name == "lower" {
			t.Fatalf("qualified call collected as unqualified: %+v", qualified)
		}
	}
}

// TestPostgresCallableBindingFreezesAbsentNamesAsEvidence proves the two halves
// of the callable contract stay separate: an unknown name keeps the statement
// exact (it cannot resolve to anything today) yet is still frozen, so a
// definition appearing after the freeze is drift, and a winner outside
// pg_catalog makes the statement partial.
func TestPostgresCallableBindingFreezesAbsentNamesAsEvidence(t *testing.T) {
	path := testPostgresPath()
	resolutions := []database.PostgresCallableResolution{
		{
			Request: database.PostgresCallableRequest{
				Kind: database.PostgresCallableFunction, Name: "lower",
			},
			Winners: []database.PostgresCallableWinner{
				postgresTestWinner("pg_catalog", "25", "text", 870),
			},
		},
		{
			Request: database.PostgresCallableRequest{
				Kind: database.PostgresCallableFunction, Name: "values",
			},
		},
		{
			Request: database.PostgresCallableRequest{
				Kind: database.PostgresCallableOperator, Name: "=",
			},
			Winners: []database.PostgresCallableWinner{
				postgresTestWinner("pg_catalog", "23,23", "boolean", 96),
			},
		},
	}
	binding, complete := preparePostgresObjectBindingsWithCallables(
		path,
		[]StatementValidityAtOrdinal{testStatement(1)},
		nil,
		resolutions,
		true,
		nil,
	)
	if !complete || !binding.Complete {
		t.Fatalf("absent callable destroyed relation exactness: %+v", binding)
	}
	if !binding.CallablesSafe || !binding.CallablesChecked {
		t.Fatalf("absent callable treated as unsafe: %+v", binding)
	}
	if len(binding.CallablesRequested) != 3 {
		t.Fatalf("frozen requests = %+v, want 3", binding.CallablesRequested)
	}
	absent, ok := binding.Callables["function:values"]
	if !ok || len(absent.Winners) != 0 {
		t.Fatalf("absent name not frozen as empty evidence: %+v", binding.Callables)
	}

	shadowed := append([]database.PostgresCallableResolution{}, resolutions...)
	shadowed[0] = database.PostgresCallableResolution{
		Request: resolutions[0].Request,
		Winners: []database.PostgresCallableWinner{
			postgresTestWinner("app", "2950", "integer", 12345),
		},
	}
	hostile, complete := preparePostgresObjectBindingsWithCallables(
		path,
		[]StatementValidityAtOrdinal{testStatement(1)},
		nil,
		shadowed,
		true,
		nil,
	)
	if !complete || !hostile.Complete {
		t.Fatalf("shadowed callable destroyed relation exactness: %+v", hostile)
	}
	if hostile.CallablesSafe {
		t.Fatalf("shadowed callable reported safe: %+v", hostile)
	}
	decoded, ok := decodeFrozenPostgresPathBinding(hostile)
	if !ok || decoded.CallablesSafe {
		t.Fatalf("unsafe binding did not decode as unsafe: ok=%v %+v", ok, decoded)
	}
	tampered := hostile
	tampered.CallablesSafe = true
	if _, ok := decodeFrozenPostgresPathBinding(tampered); ok {
		t.Fatal("decode accepted a binding whose safety claim contradicts its evidence")
	}
}

// TestPostgresCallableRefsRejectsUntokenizableStatement keeps the fail-closed
// direction: a statement the lexer cannot read must not silently produce an
// empty callable set.
func TestPostgresCallableRefsRejectsUntokenizableStatement(t *testing.T) {
	refs, ok := postgresCallableRefs("select 'unterminated")
	if ok || len(refs) != 0 {
		t.Fatalf("unterminated statement accepted: refs=%+v ok=%v", refs, ok)
	}
	if _, ok := postgresCallableRefs(`select lower('x')`); !ok {
		t.Fatal("plain statement rejected")
	}
	_ = fingerprint.Token{}
}
