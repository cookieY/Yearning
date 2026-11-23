package privacy

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var (
	refAnonTokenPattern   = regexp.MustCompile(`^` + AnonTokenBodyPattern + `$`)
	refPlaceholderPattern = regexp.MustCompile(`^:[a-z_]+$`)
	refCollapsePattern    = regexp.MustCompile(`^\[[0-9]+\]$`)
	refTokenRunPattern    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|[0-9]+(?:\.[0-9]+)?`)
	refLeakRunPattern     = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|:[a-z_]+|\[[0-9]+\]|[0-9]+(?:\.[0-9]+)?|.`)
)

func referenceExtractComparisonTokens(anonymized string) []string {
	runs := refLeakRunPattern.FindAllString(anonymized, -1)
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		if refAnonTokenPattern.MatchString(r) || refPlaceholderPattern.MatchString(r) || refCollapsePattern.MatchString(r) {
			continue
		}
		if strings.TrimSpace(r) == "" || len(r) == 1 && !isTokenRune(r[0]) {
			continue
		}
		out = append(out, strings.ToLower(r))
	}
	return out
}

func referenceAssertNoLeakValues(values []string, forbidden []Forbidden) error {
	for _, v := range values {
		if v == "" {
			continue
		}
		tokens := referenceExtractComparisonTokens(v)
		set := make(map[string]struct{}, len(tokens))
		for _, t := range tokens {
			set[t] = struct{}{}
		}
		for _, f := range forbidden {
			fv := strings.TrimSpace(f.Value)
			if fv == "" {
				continue
			}
			runs := refTokenRunPattern.FindAllString(fv, -1)
			if len(runs) == 1 && len(runs[0]) == len(fv) {
				if _, ok := set[strings.ToLower(fv)]; ok {
					return fmt.Errorf("privacy: tokenized value leaks %s (value suppressed)", f.What)
				}
				continue
			}
			if strings.Contains(strings.ToLower(v), strings.ToLower(fv)) {
				return fmt.Errorf("privacy: tokenized value leaks %s (value suppressed)", f.What)
			}
		}
	}
	return nil
}

func referenceAssertNoLeak(anonymized string, forbidden []Forbidden) error {
	lowerAnon := strings.ToLower(anonymized)
	for _, ch := range anonymizedForbiddenChars {
		if strings.Contains(anonymized, ch) {
			return fmt.Errorf("privacy: anonymized output contains forbidden sequence %q", ch)
		}
	}
	tokens := referenceExtractComparisonTokens(anonymized)
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	leaked := map[string]struct{}{}
	for _, f := range forbidden {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		runs := refTokenRunPattern.FindAllString(v, -1)
		if len(runs) == 1 && len(runs[0]) == len(v) {
			if _, ok := set[strings.ToLower(v)]; ok {
				leaked[f.What] = struct{}{}
			}
			continue
		}
		if strings.Contains(lowerAnon, strings.ToLower(v)) {
			leaked[f.What] = struct{}{}
		}
	}
	if len(leaked) > 0 {
		kinds := make([]string, 0, len(leaked))
		for k := range leaked {
			kinds = append(kinds, k)
		}
		for i := 1; i < len(kinds); i++ {
			for j := i; j > 0 && kinds[j] < kinds[j-1]; j-- {
				kinds[j], kinds[j-1] = kinds[j-1], kinds[j]
			}
		}
		return fmt.Errorf("privacy: anonymized output leaks %s (value suppressed)", strings.Join(kinds, ","))
	}
	return nil
}

func TestExtractComparisonTokensRegexParity(t *testing.T) {
	t.Parallel()
	samples := []string{
		"",
		"   \t\n\r  ",
		"UPDATE table_1 SET column_1 = :string, column_2 = :int WHERE column_3 IN ([10])",
		"database_1 schema_2 table_3 column_4 index_5 constraint_6 alias_7 cte_8 ident_9",
		"table_1_extra table_ _table unknown_1 Table_1",
		"SELECT 42, 3.14, 12., 1.2.3, 123abc FROM schema_1.table_2",
		":string :int :string_val :int1 :1 :A :: :_ :[1] [10] [12abc] [] [a]",
		"a+b-c*d/e=f<g>h!i~j@k#l%m^n&o|p?q,r;s(t)u{v}w",
		"SELECT caf\u00e9, \u03b1\u03b2\u03b3 FROM table_1\u00a0WHERE column_1 = \u2014",
		"invalid utf8 \xff\xfe bytes mixed with Orders_99",
		"line1\nline2\n\nSELECT * FROM table_1\nWHERE column_1 = :decimal",
	}
	for i, sample := range samples {
		got := ExtractComparisonTokens(sample)
		want := referenceExtractComparisonTokens(sample)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sample %d (%q):\n got  = %#v\n want = %#v", i, sample, got, want)
		}
	}
}

func TestAssertNoLeakAndValuesRegexParity(t *testing.T) {
	t.Parallel()
	forbidden := []Forbidden{
		{Value: "", What: "empty"},
		{Value: "   ", What: "spaces"},
		{Value: "Orders", What: "identifier:table"},
		{Value: "table", What: "identifier:short"},
		{Value: "42", What: "literal:number"},
		{Value: "3.14", What: "literal:decimal"},
		{Value: "12.", What: "literal:trailing_dot"},
		{Value: "1.2.3", What: "literal:multi_dot"},
		{Value: "secret multi word", What: "literal:string"},
		{Value: "caf\u00e9", What: "literal:utf8"},
		{Value: "internal_note", What: "comment"},
	}
	inputs := []string{
		"",
		"UPDATE table_1 SET column_1 = :string WHERE column_2 = :int",
		"UPDATE table_1 SET column_1 = :string WHERE column_2 IN ([42])",
		"SELECT Orders FROM table_1",
		"SELECT 42 FROM table_1",
		"SELECT 3.14 FROM table_1",
		"SELECT 12. FROM table_1",
		"SELECT 1.2.3 FROM table_1",
		"SELECT secret multi word FROM table_1",
		"SELECT caf\u00e9 FROM table_1",
		"SELECT orders, internal_note FROM table_1",
		"SELECT 'quoted' FROM table_1",
		"SELECT `backtick` FROM table_1",
		"SELECT 1 -- comment",
		"SELECT /* block */ 1",
		"SELECT $$dollar$$",
		"SELECT table_1 #> :string",
	}

	for _, in := range inputs {
		gotErr := AssertNoLeak(in, forbidden)
		wantErr := referenceAssertNoLeak(in, forbidden)
		if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
			t.Fatalf("AssertNoLeak(%q):\n got  = %v\n want = %v", in, gotErr, wantErr)
		}
		gotEmpty := AssertNoLeak(in, nil)
		wantEmpty := referenceAssertNoLeak(in, nil)
		if (gotEmpty == nil) != (wantEmpty == nil) || (gotEmpty != nil && gotEmpty.Error() != wantEmpty.Error()) {
			t.Fatalf("AssertNoLeak(%q, nil):\n got  = %v\n want = %v", in, gotEmpty, wantEmpty)
		}
	}

	valueInputs := [][]string{
		nil,
		{"", "table_1", "schema_2", "12."},
		{"orders"},
		{"contains Orders inside"},
		{"ratio 3.14"},
		{"has secret multi word inside"},
		{"caf\u00e9"},
		{"value with 'quotes' and internal_note"},
	}
	for _, vals := range valueInputs {
		gotErr := AssertNoLeakValues(vals, forbidden)
		wantErr := referenceAssertNoLeakValues(vals, forbidden)
		if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
			t.Fatalf("AssertNoLeakValues(%v):\n got  = %v\n want = %v", vals, gotErr, wantErr)
		}
		if err := AssertNoLeakValues(vals, nil); err != nil {
			t.Fatalf("AssertNoLeakValues(%v, nil) unexpected err: %v", vals, err)
		}
	}
}

func TestComparisonTokenCapHintAndLargeInputLeakDetection(t *testing.T) {
	cases := []struct {
		n    int
		want int
	}{
		{-10, 0},
		{0, 0},
		{1, 1},
		{12, 3},
		{maxComparisonTokenHintBytes - 1, (maxComparisonTokenHintBytes-1)/6 + 1},
		{maxComparisonTokenHintBytes, maxComparisonTokenCapHint},
		{maxComparisonTokenHintBytes * 10, maxComparisonTokenCapHint},
		{math.MaxInt, maxComparisonTokenCapHint},
	}
	for _, tc := range cases {
		if got := comparisonTokenCapHint(tc.n); got != tc.want {
			t.Errorf("comparisonTokenCapHint(%d) = %d; want %d", tc.n, got, tc.want)
		}
	}

	padding := strings.Repeat("safe_tok ", maxComparisonTokenHintBytes/len("safe_tok ")+32)
	payload := padding + "secret_orders_table"
	forbidden := []Forbidden{{Value: "secret_orders_table", What: "table_name"}}

	tokens := ExtractComparisonTokens(payload)
	if len(tokens) == 0 || tokens[len(tokens)-1] != "secret_orders_table" {
		t.Fatalf("ExtractComparisonTokens did not preserve trailing token on large input (len=%d)", len(tokens))
	}
	if err := AssertNoLeakValues([]string{payload}, forbidden); err == nil {
		t.Fatal("AssertNoLeakValues missed leak beyond cap hint boundary")
	}
	if err := AssertNoLeak(payload, forbidden); err == nil {
		t.Fatal("AssertNoLeak missed leak beyond cap hint boundary")
	}
}
