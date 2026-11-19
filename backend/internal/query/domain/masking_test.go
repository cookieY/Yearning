// masking_test.go acceptance tests for static masking analysis (RCP-20260828-FLOW-MASKING ruling):
// closes alias escape (A as B), expression references, CTE rename chains, explicit column lists,
// recursion, UNION positional OR, star name rules, and false-positive exclusions.
package domain

import (
	"reflect"
	"testing"
)

// maskOf is a convenience assertion helper returning columnName -> masked boolean.
func maskOf(t *testing.T, sql string, vocab []string, cols []string) []bool {
	t.Helper()
	return maskOfDialect(t, sql, false, vocab, cols)
}

func maskOfDialect(t *testing.T, sql string, pg bool, vocab []string, cols []string) []bool {
	t.Helper()
	plan, e := AnalyzeMasking(sql, pg, vocab)
	if e != nil {
		t.Fatalf("AnalyzeMasking(%q) error %v", sql, e)
	}
	return plan.ColumnMask(cols)
}

func TestMaskingAliasEscape(t *testing.T) {
	// Original owner ruling case: when A is sensitive, `A as B` must remain masked.
	vocab := []string{"a"}
	if got := maskOf(t, "select a as b from t", vocab, []string{"b"}); !got[0] {
		t.Error("A as B escape: b not masked")
	}
	if got := maskOf(t, "select a b from t", vocab, []string{"b"}); !got[0] {
		t.Error("implicit alias escape: b not masked")
	}
	// Direct column name.
	if got := maskOf(t, "select a from t", vocab, []string{"a"}); !got[0] {
		t.Error("bare a not masked")
	}
	// Qualified reference and qualified alias.
	if got := maskOf(t, "select t.a as z from t", vocab, []string{"z"}); !got[0] {
		t.Error("qualified t.a as z not masked")
	}
	if got := maskOf(t, "select t.a from t", vocab, []string{"a"}); !got[0] {
		t.Error("qualified t.a not masked")
	}
	// Column name after table alias dot.
	if got := maskOf(t, "select x.a as z from t x", vocab, []string{"z"}); !got[0] {
		t.Error("aliased-table x.a as z not masked")
	}
	// Non-sensitive columns remain unmasked.
	if got := maskOf(t, "select b from t", vocab, []string{"b"}); got[0] {
		t.Error("b incorrectly masked")
	}
}

func TestMaskingAliasedSensitiveColumnDoesNotMaskSibling(t *testing.T) {
	got := maskOfDialect(t,
		"select secret as exposed, note from qtarget.accounts order by id",
		true, []string{"secret"}, []string{"exposed", "note"})
	if !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatalf("aliased sensitive column contaminated sibling mask: %v", got)
	}
}

func TestMaskingExpressionContainment(t *testing.T) {
	vocab := []string{"secret"}
	cases := [][]string{
		{"select concat(secret, 'x') as z from t", "z"},
		{"select secret + 1 from t", "?column?"},
		{"select upper(secret) z from t", "z"},
		{"select coalesce(secret, 0) z from t", "z"},
		{"select case when secret > 0 then secret else 0 end z from t", "z"},
		{"select (select max(secret) from s) z from t", "z"},
	}
	for _, c := range cases {
		if got := maskOf(t, c[0], vocab, []string{c[1]}); !got[0] {
			t.Errorf("expression leak: %q output %q not masked", c[0], c[1])
		}
	}
	// Expressions that do not reference any masked column are not masked.
	if got := maskOf(t, "select concat(b, 'x') as z from t", vocab, []string{"z"}); got[0] {
		t.Error("unrelated expression masked")
	}
}

func TestMaskingCTEPropagation(t *testing.T) {
	vocab := []string{"secret"}
	// Rename inside CTE -> outer reference (rename chain propagation).
	if got := maskOf(t, "with w as (select secret as c from t) select c from w", vocab, []string{"c"}); !got[0] {
		t.Error("CTE rename c not masked")
	}
	// Double rename across chained CTEs.
	if got := maskOf(t, "with w as (select secret as c from t) select c as z from w", vocab, []string{"z"}); !got[0] {
		t.Error("double rename z not masked")
	}
	// Outer expression referencing a renamed CTE column.
	if got := maskOf(t, "with w as (select secret as c from t) select c + 1 z from w", vocab, []string{"z"}); !got[0] {
		t.Error("CTE rename in expression not masked")
	}
	// Outer AS alias over a renamed CTE column.
	if got := maskOf(t, "with w as (select secret as c from t) select c as z, b from w", vocab, []string{"z", "b"}); !got[0] || got[1] {
		t.Errorf("CTE rename mixed cols = %v; want [true false]", got)
	}
	// Explicit CTE column list mapped by position.
	if got := maskOf(t, "with w(p, q) as (select secret, b from t) select p as z, q from w", vocab, []string{"z", "q"}); !got[0] || got[1] {
		t.Errorf("explicit column list = %v; want [true false]", got)
	}
	// Upstream CTE propagating into downstream CTE.
	if got := maskOf(t, "with w as (select secret as c from t), v as (select c as d from w) select d from v", vocab, []string{"d"}); !got[0] {
		t.Error("chained CTE rename not masked")
	}
	// Recursive CTE two-pass propagation.
	if got := maskOf(t, "with recursive r as (select secret as c, 1 as n from t union all select c, n+1 from r where n < 3) select c as z from r", vocab, []string{"z"}); !got[0] {
		t.Error("recursive CTE rename not masked")
	}
	// Masked column inside a CTE does not taint unreferenced CTE outputs.
	if got := maskOf(t, "with w as (select secret as c from t) select b from w join x on w.c = x.k", vocab, []string{"b"}); got[0] {
		t.Error("unrelated CTE output masked")
	}
}

func TestMaskingStar(t *testing.T) {
	vocab := []string{"secret"}
	// Star expansion preserves column names: evaluated by runtime name rule.
	if got := maskOf(t, "select * from t", vocab, []string{"id", "secret", "name"}); !reflect.DeepEqual(got, []bool{false, true, false}) {
		t.Errorf("star name rule = %v", got)
	}
	// Table-qualified star.
	if got := maskOf(t, "select t.* from t", vocab, []string{"id", "secret"}); !got[1] {
		t.Error("qualified star name rule failed")
	}
	// Star + CTE rename (marked CTE output name enters MaskedNames).
	if got := maskOf(t, "with w as (select secret as c, b from t) select * from w", vocab, []string{"c", "b"}); !got[0] || got[1] {
		t.Errorf("star over renamed CTE = %v; want [true false]", got)
	}
	// Star mixed with ordinary renamed item (positional mismatch falls back to MaskedNames).
	if got := maskOf(t, "select *, concat(secret, 'x') as z from t", vocab, []string{"id", "secret", "z"}); !reflect.DeepEqual(got, []bool{false, true, true}) {
		t.Errorf("star + renamed expr = %v", got)
	}
	// Star over a renamed FROM subquery: positional propagation into MaskedNames.
	if got := maskOf(t, "select * from (select secret as c, b from t) x", vocab, []string{"c", "b"}); !got[0] || got[1] {
		t.Errorf("star over derived subquery = %v; want [true false]", got)
	}
}

func TestMaskingUnion(t *testing.T) {
	vocab := []string{"secret"}
	sql := "select secret as z, b from t union all select c, d from u"
	if got := maskOf(t, sql, vocab, []string{"z", "b"}); !got[0] || got[1] {
		t.Errorf("union positional OR = %v; want [true false]", got)
	}
	// Masked column in the second UNION branch: result columns inherit first-branch names while positional OR masks the column.
	sql2 := "select a, b from t union all select x.secret, d from u x"
	if got := maskOf(t, sql2, vocab, []string{"a", "b"}); !got[0] {
		t.Errorf("union second branch flag lost: %v", got)
	}
}

func TestMaskingVocabCaseNormalized(t *testing.T) {
	// H2 regression: uppercase entries in the vocabulary must not disable matching and leave sensitive columns unmasked.
	if got := maskOf(t, "select SECRET from t", []string{"Secret"}, []string{"secret"}); !got[0] {
		t.Error("uppercase vocab entry missed lowercase column")
	}
	if got := maskOf(t, "select secret as z from t", []string{"SECRET"}, []string{"z"}); !got[0] {
		t.Error("uppercase vocab missed aliased column")
	}
	if got := maskOf(t, "select name from t", []string{"SECRET"}, []string{"name"}); got[0] {
		t.Error("unrelated column masked")
	}
}

func TestMaskingVocabCaseStar(t *testing.T) {
	// H2 residual regression: star + uppercase vocabulary -- runtime name matching must also case-fold.
	if got := maskOf(t, "select * from t", []string{"SECRET"}, []string{"secret", "name"}); !got[0] || got[1] {
		t.Errorf("star with uppercase vocab = %v; want [true false]", got)
	}
}

func TestMaskingUnicodeCaseFold(t *testing.T) {
	// PostgreSQL quoted identifiers preserve spelling, but the approved masking
	// vocabulary remains case-insensitive across the full Unicode fold domain.
	got := maskOfDialect(t,
		`select "ΣΤΗΛΗ" as "ΔΙΑΡΡΟΗ" from t`, true,
		[]string{"στηλη"}, []string{"ΔΙΑΡΡΟΗ"})
	if len(got) != 1 || !got[0] {
		t.Fatalf("unicode case-folded sensitive column was not masked: %v", got)
	}
	if FoldIdentifier("Σ") != FoldIdentifier("ς") {
		t.Fatal("Unicode simple-fold cycle did not canonicalize final sigma")
	}
}

func TestMaskingQuotedIdent(t *testing.T) {
	// Quoted identifiers (backtick/double-quoted column names) match after content normalization.
	if got := maskOf(t, "select `secret` from t", []string{"secret"}, []string{"secret"}); !got[0] {
		t.Error("backtick-quoted masked column missed")
	}
	// Double-quoted identifiers follow PostgreSQL dialect semantics.
	if got := maskOfDialect(t, "select \"SECRET\" as z from t", true, []string{"secret"}, []string{"z"}); !got[0] {
		t.Error("double-quoted masked column missed")
	}
}

func TestMaskingFalsePositives(t *testing.T) {
	vocab := []string{"secret", "from"}
	// Vocabulary entry sharing a name with a SQL keyword: FROM clause keyword position must not trigger masking.
	if got := maskOf(t, "select id from t", vocab, []string{"id"}); got[0] {
		t.Error("keyword-named vocab word leaked into output mask")
	}
	// Table name / qualifier position: when `secret` is a table qualifier, only the column name after the dot is evaluated.
	if got := maskOf(t, "select secret.id from secret", []string{"secret"}, []string{"id"}); got[0] {
		t.Error("qualifier position counted as column ref")
	}
	// Function name sharing a name with a vocabulary entry: function call position does not trigger masking (only column references do).
	if got := maskOf(t, "select secret(1) z from t", []string{"secret"}, []string{"z"}); got[0] {
		t.Error("function name matched vocab")
	}
	// Case-insensitive normalization.
	if got := maskOf(t, "select SECRET as Z from t", []string{"secret"}, []string{"Z"}); !got[0] {
		t.Error("case-insensitive vocab match failed")
	}
}

func TestMaskingUnnamedFailClosed(t *testing.T) {
	vocab := []string{"secret"}
	// Unnamed masked expression + star: positional alignment is lost and the expression has no name -> fail closed by masking all unmapped columns.
	sql := "select *, secret + 1 from t"
	got := maskOf(t, sql, vocab, []string{"id", "secret", "?column?"})
	if !got[2] {
		t.Errorf("unnamed masked expression lost under star: %v", got)
	}
}
