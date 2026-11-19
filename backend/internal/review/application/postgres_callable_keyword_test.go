package application

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestPostgresCallableKeywordExtraction(t *testing.T) {
	cases := []struct {
		name, sql string
		want      []string
	}{
		{"like", `SELECT x LIKE 'a'`, []string{"operator:~~"}},
		{"not like", `SELECT x NOT LIKE 'a'`, []string{"operator:!~~"}},
		{"ilike", `SELECT x ILIKE 'a'`, []string{"operator:~~*"}},
		{"not ilike", `SELECT x NOT ILIKE 'a'`, []string{"operator:!~~*"}},
		{"escape helper is fixed", `SELECT x LIKE 'a' ESCAPE '!'`, []string{"operator:~~"}},
		{"similar", `SELECT x SIMILAR TO 'a'`, []string{"operator:~"}},
		{"not similar", `SELECT x NOT SIMILAR TO 'a' ESCAPE '!'`, []string{"operator:!~"}},
		{"between", `SELECT x BETWEEN 1 AND 2`, []string{"operator:>=", "operator:<="}},
		{"symmetric", `SELECT x BETWEEN SYMMETRIC 1 AND 2`, []string{"operator:>=", "operator:<="}},
		{"not between", `SELECT x NOT BETWEEN 1 AND 2`, []string{"operator:<", "operator:>"}},
		{"not symmetric", `SELECT x NOT BETWEEN SYMMETRIC 1 AND 2`, []string{"operator:<", "operator:>"}},
		{"distinct", `SELECT x IS DISTINCT FROM y`, []string{"operator:="}},
		{"not distinct", `SELECT x IS NOT DISTINCT FROM y`, []string{"operator:="}},
		{"in", `SELECT x IN (1,2)`, []string{"operator:="}},
		{"not in list", `SELECT x NOT IN (1,2)`, []string{"operator:=", "operator:<>"}},
		{"not in subquery", `SELECT x NOT IN (SELECT y FROM app.docs)`, []string{"operator:=", "operator:<>"}},
		{"nullif", `SELECT NULLIF(x,y)`, []string{"operator:="}},
		{"simple case", `SELECT CASE x WHEN y THEN 1 ELSE 0 END`, []string{"operator:="}},
		{"searched case", `SELECT CASE WHEN true THEN 1 ELSE 0 END`, nil},
		{"neq spelling", `SELECT x != y`, []string{"operator:<>"}},
		{"quoted function", `SELECT "MixedCase"(x)`, []string{"function:MixedCase"}},
		{"quoted keyword function", `SELECT "LIKE"(x)`, []string{"function:LIKE"}},
		{"qualified function", `SELECT "app"."MixedCase"(x)`, nil},
		{"ordinary function", `SELECT LoWeR(x)`, []string{"function:lower"}},
		{"quoted column", `SELECT "LIKE" FROM app.docs`, nil},
		{"literal and comment", `SELECT 'LIKE NULLIF !=' /* "Bad"() */`, nil},
		{"timezone fixed", `SELECT now() AT TIME ZONE 'UTC'`, []string{"function:now"}},
		{"local fixed", `SELECT x AT LOCAL`, nil},
		{"timezone arguments scanned", `SELECT x AT TIME ZONE lower('UTC')`, []string{"function:lower"}},
		{"overlaps fixed", `(a,b) OVERLAPS (c,d)`, nil},
		{"any written operator", `SELECT x > ANY(ARRAY[1])`, []string{"operator:>"}},
		{"all written operator", `SELECT x < ALL(ARRAY[1])`, []string{"operator:<"}},
		{"some written operator", `SELECT x <> SOME(ARRAY[1])`, []string{"operator:<>"}},
		{"like any", `SELECT x LIKE ANY(ARRAY['a'])`, []string{"operator:~~"}},
		{"nested calls", `SELECT x NOT LIKE lower('a')`, []string{"operator:!~~", "function:lower"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs, ok := postgresCallableRefs(tc.sql)
			if !ok {
				t.Fatal("valid statement rejected")
			}
			var got []string
			for _, ref := range refs {
				got = append(got, database.PostgresCallablePayloadKey(ref.Kind, ref.Name))
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("refs = %v, want %v", got, want)
			}
		})
	}
}

func TestPostgresCallableExtractionFailsClosedAndBoundsWork(t *testing.T) {
	for _, sql := range []string{`SELECT "bad-name"(x)`, `SELECT "bad""name"(x)`, "SELECT 'unterminated"} {
		if _, ok := postgresCallableRefs(sql); ok {
			t.Fatal("unrepresentable or invalid input accepted")
		}
	}
	var sql strings.Builder
	for i := 0; i <= postgresCallableMaxRefs; i++ {
		fmt.Fprintf(&sql, "f%d(),", i)
	}
	if _, ok := postgresCallableRefs(sql.String()); ok {
		t.Fatal("unique reference limit was ignored")
	}
	repeated := strings.Repeat("lower(x),", postgresCallableMaxRefs+1)
	refs, ok := postgresCallableRefs(repeated)
	if !ok || len(refs) != 1 {
		t.Fatalf("deduplication failed: ok=%v count=%d", ok, len(refs))
	}
}
