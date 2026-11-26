package display

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func testRenderer(t *testing.T, count int, pairs ...privacy.MappingPair) *Renderer {
	t.Helper()
	r, err := newRendererFromPairs(pairs, count)
	if err != nil {
		t.Fatalf("newRendererFromPairs: %v", err)
	}
	return r
}

func TestRestoreText(t *testing.T) {
	tests := []struct {
		name  string
		pairs []privacy.MappingPair
		input string
		want  string
		count int
	}{
		{
			name: "single identifier",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
			},
			input: "Inspect table_1 before release.",
			want:  "Inspect orders before release.",
			count: 1,
		},
		{
			name: "multiple identifiers and occurrences",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindSchema, Original: "analytics", Token: "schema_1"},
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
				{Kind: privacy.KindColumn, Original: "status", Token: "column_1"},
			},
			input: "schema_1.table_1 uses column_1; table_1 lacks a status check on column_1.",
			want:  "analytics.orders uses status; orders lacks a status check on status.",
			count: 5,
		},
		{
			name: "table one does not match table ten",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
				{Kind: privacy.KindTable, Original: "line_items", Token: "table_10"},
			},
			input: "table_10 and table_1",
			want:  "line_items and orders",
			count: 2,
		},
		{
			name: "known token does not match when embedded",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
				{Kind: privacy.KindSchema, Original: "analytics", Token: "schema_1"},
			},
			input: "x_table_1 table_1x table_1_ _table_1 $table_1 table_1$ schema_1.table_1",
			want:  "x_table_1 table_1x table_1_ _table_1 $table_1 table_1$ analytics.orders",
			count: 2,
		},
		{
			name: "unicode adjacent identifiers",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
			},
			input: "\u68c0\u67e5table_1\u8868",
			want:  "\u68c0\u67e5orders\u8868",
			count: 1,
		},
		{
			name: "unknown token is preserved",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
			},
			input: "column_9 and table_1",
			want:  "column_9 and orders",
			count: 1,
		},
		{
			name: "literal placeholder is unchanged",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
			},
			input: "Use :string and 'literal' with table_1.",
			want:  "Use :string and 'literal' with orders.",
			count: 1,
		},
		{
			name: "replacement is nonrecursive",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "table_2", Token: "table_1"},
				{Kind: privacy.KindTable, Original: "users", Token: "table_2"},
			},
			input: "table_1 and table_2",
			want:  "table_2 and users",
			count: 2,
		},
		{
			name: "html like identifier remains literal",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindColumn, Original: "<b>status</b>", Token: "column_1"},
			},
			input: "Inspect column_1.",
			want:  "Inspect <b>status</b>.",
			count: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRenderer(t, len(tt.pairs), tt.pairs...)
			got, count := r.RestoreText(tt.input)
			if got != tt.want || count != tt.count {
				t.Fatalf("RestoreText() = (%q, %d), want (%q, %d)", got, count, tt.want, tt.count)
			}
		})
	}
}

func TestRestoreTextSkipsSQLLiteralsAndComments(t *testing.T) {
	r := testRenderer(t, 2,
		privacy.MappingPair{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		privacy.MappingPair{Kind: privacy.KindColumn, Original: "status", Token: "column_1"},
	)
	tests := []struct {
		name  string
		input string
		want  string
		count int
	}{
		{
			name:  "literal followed by line comment",
			input: "WHERE status = 'table_1' -- table_1",
			want:  "WHERE status = 'table_1' -- table_1",
		},
		{
			name:  "single quoted SQL string",
			input: "WHERE status = 'table_1'",
			want:  "WHERE status = 'table_1'",
		},
		{
			name:  "doubled single quote escape",
			input: "SELECT 'one'' table_1'",
			want:  "SELECT 'one'' table_1'",
		},
		{
			name:  "backslash escaped single quote",
			input: "SELECT 'table_1\\' still table_1'",
			want:  "SELECT 'table_1\\' still table_1'",
		},
		{
			name:  "double dash line comment",
			input: "SELECT table_1 -- table_1\nFROM table_1",
			want:  "SELECT orders -- table_1\nFROM orders",
			count: 2,
		},
		{
			name:  "hash line comment",
			input: "SELECT table_1 # table_1\nFROM table_1",
			want:  "SELECT orders # table_1\nFROM orders",
			count: 2,
		},
		{
			name:  "block comment",
			input: "/* table_1 */ SELECT table_1",
			want:  "/* table_1 */ SELECT orders",
			count: 1,
		},
		{
			name:  "nested block comment",
			input: "/* outer /* table_1 */ table_1 */ SELECT table_1",
			want:  "/* outer /* table_1 */ table_1 */ SELECT orders",
			count: 1,
		},
		{
			name:  "postgres dollar quote",
			input: "SELECT $$table_1$$, $tag$table_1$tag$",
			want:  "SELECT $$table_1$$, $tag$table_1$tag$",
		},
		{
			name:  "double and backtick identifiers still restore",
			input: "SELECT \"table_1\", `table_1`",
			want:  "SELECT \"orders\", `orders`",
			count: 2,
		},
		{
			name:  "escaped double quote inside identifier",
			input: "SELECT \"a\"\"table_1\"",
			want:  "SELECT \"a\"\"orders\"",
			count: 1,
		},
		{
			name:  "ambiguous single backtick content stays anonymous",
			input: "SELECT \"x--table_1\", `x#table_1`",
			want:  "SELECT \"x--orders\", `x#table_1`",
			count: 1,
		},
		{
			name:  "simple single backtick identifier still restores",
			input: "SELECT `table_1`",
			want:  "SELECT `orders`",
			count: 1,
		},
		{
			name:  "single backtick SQL literal remains anonymous",
			input: "`WHERE status = 'table_1'`",
			want:  "`WHERE status = 'table_1'`",
		},
		{
			name: "single backtick SQL scans identifiers but protects literal and comments",
			input: "`SELECT table_1, 'table_1' FROM schema.table_1 " +
				"/* table_1 */ -- table_1`",
			want: "`SELECT orders, 'table_1' FROM schema.orders " +
				"/* table_1 */ -- table_1`",
			count: 2,
		},
		{
			name:  "unclosed single backtick protects remainder",
			input: "SELECT `table_1",
			want:  "SELECT `table_1",
		},
		{
			name:  "ambiguous Unicode single backtick span stays anonymous",
			input: "`\u8868\u683c` then table_1",
			want:  "`\u8868\u683c` then orders",
			count: 1,
		},
		{
			name:  "single backtick span skips a longer backtick run",
			input: "`foo``table_1` then table_1",
			want:  "`foo``table_1` then orders",
			count: 1,
		},
		{
			name: "fenced SQL scans identifiers but skips literal and comments",
			input: "```sql\n" +
				"SELECT 'table_1' AS literal, table_1 -- table_1\n" +
				"FROM schema.table_1 /* table_1 */\n" +
				"```",
			want: "```sql\n" +
				"SELECT 'table_1' AS literal, orders -- table_1\n" +
				"FROM schema.orders /* table_1 */\n" +
				"```",
			count: 2,
		},
		{
			name:  "longer Markdown fence delimiter",
			input: "````sql\nSELECT `table_1` FROM table_1\n````",
			want:  "````sql\nSELECT `orders` FROM orders\n````",
			count: 2,
		},
		{
			name:  "double backtick inline code stays anonymous",
			input: "Use ``table_1 -- 'table_1'`` then table_1",
			want:  "Use ``table_1 -- 'table_1'`` then orders",
			count: 1,
		},
		{
			name:  "unclosed double backtick inline code protects remainder",
			input: "Use ``table_1",
			want:  "Use ``table_1",
		},
		{
			name:  "double backtick inline skips a longer delimiter run",
			input: "``foo ``` table_1`` then table_1",
			want:  "``foo ``` table_1`` then orders",
			count: 1,
		},
		{
			name:  "natural language contractions and possessives still restore",
			input: "It's fine; don't change table_1's value.",
			want:  "It's fine; don't change orders's value.",
			count: 1,
		},
		{
			name:  "digit suffix possessive still restores following token",
			input: "table_1's column_1",
			want:  "orders's status",
			count: 2,
		},
		{
			name:  "plural possessive does not swallow following token",
			input: "Users' table_1",
			want:  "Users' orders",
			count: 1,
		},
		{
			name:  "SQL string introducers remain literals",
			input: "SELECT E'table_1', N'table_1', X'table_1', U&'table_1'",
			want:  "SELECT E'table_1', N'table_1', X'table_1', U&'table_1'",
		},
		{
			name:  "unterminated dollar quote protects remainder",
			input: "SELECT $tag$table_1",
			want:  "SELECT $tag$table_1",
		},
		{
			name:  "dollar parameter is not a quote delimiter",
			input: "SELECT $1, table_1",
			want:  "SELECT $1, orders",
			count: 1,
		},
		{
			name:  "lone dollar remains ordinary text",
			input: "cost $ table_1",
			want:  "cost $ orders",
			count: 1,
		},
		{
			name:  "unterminated literal is conservatively skipped",
			input: "SELECT 'table_1",
			want:  "SELECT 'table_1",
		},
		{
			name:  "unterminated block comment is conservatively skipped",
			input: "SELECT /* table_1",
			want:  "SELECT /* table_1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, count := r.RestoreText(tt.input)
			if got != tt.want || count != tt.count {
				t.Fatalf("RestoreText() = (%q, %d), want (%q, %d)", got, count, tt.want, tt.count)
			}
		})
	}
}

func TestRestorePointerLimitWithSQLSkipRanges(t *testing.T) {
	r := testRenderer(t, 1,
		privacy.MappingPair{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
	)
	input := "\u6761\u4ef6='\u4e2d\u6587' table_1"
	want := "\u6761\u4ef6='\u4e2d\u6587' orders"
	limit := utf8.RuneCountInString(want)

	got, count := r.RestorePointerLimit(&input, limit)
	if got == nil || *got != want || count != 1 {
		t.Fatalf("RestorePointerLimit(exact) = (%v, %d), want (%q, 1)", got, count, want)
	}
	got, count = r.RestorePointerLimit(&input, limit-1)
	if got == nil || *got != input || count != 0 {
		t.Fatalf("RestorePointerLimit(over) = (%v, %d), want (%q, 0)", got, count, input)
	}

	input = "`SELECT '\u4e2d\u6587' FROM table_1`"
	want = "`SELECT '\u4e2d\u6587' FROM orders`"
	limit = utf8.RuneCountInString(want)
	got, count = r.RestorePointerLimit(&input, limit)
	if got == nil || *got != want || count != 1 {
		t.Fatalf("RestorePointerLimit(inline exact) = (%v, %d), want (%q, 1)", got, count, want)
	}
	got, count = r.RestorePointerLimit(&input, limit-1)
	if got == nil || *got != input || count != 0 {
		t.Fatalf("RestorePointerLimit(inline over) = (%v, %d), want (%q, 0)", got, count, input)
	}
}

func TestRestorePointerLimitScannerRejections(t *testing.T) {
	r := testRenderer(t, 1,
		privacy.MappingPair{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
	)
	tests := []struct {
		name     string
		renderer *Renderer
		input    string
		maxRunes int
	}{
		{
			name:     "empty renderer over limit",
			renderer: &Renderer{},
			input:    "\u4e2d\u6587",
			maxRunes: 1,
		},
		{
			name:     "quoted identifier opening over limit",
			renderer: r,
			input:    "\"table_1\"",
			maxRunes: 0,
		},
		{
			name:     "single quoted skip range over limit",
			renderer: r,
			input:    "'\u4e2d\u6587'",
			maxRunes: 3,
		},
		{
			name:     "fenced delimiter over limit",
			renderer: r,
			input:    "```sql\n```",
			maxRunes: 2,
		},
		{
			name:     "double backtick inline over limit",
			renderer: r,
			input:    "``\u4e2d\u6587``",
			maxRunes: 3,
		},
		{
			name:     "single backtick SQL fragment lacks delimiter capacity",
			renderer: r,
			input:    "`a b`",
			maxRunes: 1,
		},
		{
			name:     "simple backtick identifier over limit",
			renderer: r,
			input:    "`table_1`",
			maxRunes: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			got, count := tt.renderer.RestorePointerLimit(&input, tt.maxRunes)
			if got == nil || *got != tt.input || count != 0 {
				t.Fatalf("RestorePointerLimit() = (%v, %d), want (%q, 0)", got, count, tt.input)
			}
			if input != tt.input {
				t.Fatalf("input mutated to %q", input)
			}
		})
	}
}

func TestRestorePointerDoesNotMutateInput(t *testing.T) {
	r := testRenderer(t, 1, privacy.MappingPair{
		Kind: privacy.KindTable, Original: "orders", Token: "table_1",
	})
	input := "Inspect table_1."
	got, count := r.RestorePointer(&input)
	if count != 1 || got == nil || *got != "Inspect orders." {
		t.Fatalf("RestorePointer() = (%v, %d), want (%q, 1)", got, count, "Inspect orders.")
	}
	if got == &input {
		t.Fatal("RestorePointer returned the input pointer")
	}
	if input != "Inspect table_1." {
		t.Fatalf("input mutated to %q", input)
	}

	got, count = r.RestorePointer(nil)
	if got != nil || count != 0 {
		t.Fatalf("RestorePointer(nil) = (%v, %d), want (nil, 0)", got, count)
	}
}

func TestRestorePointerLimit(t *testing.T) {
	r := testRenderer(t, 2,
		privacy.MappingPair{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		privacy.MappingPair{Kind: privacy.KindColumn, Original: "\u8ba2\u5355\u72b6\u6001", Token: "column_1"},
	)
	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
		count    int
	}{
		{
			name:     "exact ASCII limit is accepted",
			input:    "table_1",
			maxRunes: 6,
			want:     "orders",
			count:    1,
		},
		{
			name:     "restored ASCII text over limit is kept anonymous",
			input:    "table_1",
			maxRunes: 5,
			want:     "table_1",
			count:    0,
		},
		{
			name:     "limit counts runes rather than bytes",
			input:    "column_1",
			maxRunes: 4,
			want:     "\u8ba2\u5355\u72b6\u6001",
			count:    1,
		},
		{
			name:     "multi byte text over rune limit is kept anonymous",
			input:    "column_1",
			maxRunes: 3,
			want:     "column_1",
			count:    0,
		},
		{
			name:     "anonymous input over limit still has zero count",
			input:    "unknown text",
			maxRunes: 4,
			want:     "unknown text",
			count:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			got, count := r.RestorePointerLimit(&input, tt.maxRunes)
			if got == nil || *got != tt.want || count != tt.count {
				t.Fatalf("RestorePointerLimit() = (%v, %d), want (%q, %d)", got, count, tt.want, tt.count)
			}
			if got == &input {
				t.Fatal("RestorePointerLimit returned the input pointer")
			}
			if input != tt.input {
				t.Fatalf("input mutated to %q", input)
			}
		})
	}

	got, count := r.RestorePointerLimit(nil, 10)
	if got != nil || count != 0 {
		t.Fatalf("RestorePointerLimit(nil) = (%v, %d), want (nil, 0)", got, count)
	}
}

func TestRestorePointerLimitCountsUTF8AfterReplacement(t *testing.T) {
	r := testRenderer(t, 1,
		privacy.MappingPair{Kind: privacy.KindTable, Original: "X", Token: "table_1"},
	)
	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
		count    int
	}{
		{
			name:     "Chinese immediately after replacement fits exactly",
			input:    "table_1\u4e2d\u6587",
			maxRunes: 3,
			want:     "X\u4e2d\u6587",
			count:    1,
		},
		{
			name:     "emoji immediately after replacement fits exactly",
			input:    "table_1🙂",
			maxRunes: 2,
			want:     "X🙂",
			count:    1,
		},
		{
			name:     "Chinese immediately after replacement still enforces limit",
			input:    "table_1\u4e2d\u6587",
			maxRunes: 2,
			want:     "table_1\u4e2d\u6587",
			count:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input
			got, count := r.RestorePointerLimit(&input, tt.maxRunes)
			if got == nil || *got != tt.want || count != tt.count {
				t.Fatalf("RestorePointerLimit() = (%v, %d), want (%q, %d)", got, count, tt.want, tt.count)
			}
			if input != tt.input {
				t.Fatalf("input mutated to %q", input)
			}
		})
	}
}

func TestEmptyRendererAndNilReceiver(t *testing.T) {
	for _, r := range []*Renderer{nil, {}} {
		got, count := r.RestoreText("table_9")
		if got != "table_9" || count != 0 {
			t.Fatalf("empty RestoreText() = (%q, %d), want (%q, 0)", got, count, "table_9")
		}
	}
}

func TestRendererRejectsUnsafeMappings(t *testing.T) {
	tests := []struct {
		name      string
		pairs     []privacy.MappingPair
		declared  int
		forbidden []string
	}{
		{
			name: "count mismatch",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
			},
			declared:  2,
			forbidden: []string{"orders", "table_1"},
		},
		{
			name: "malformed delimiter",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders\tsecret", Token: "table_1"},
			},
			declared:  1,
			forbidden: []string{"orders", "secret", "table_1"},
		},
		{
			name: "duplicate token ownership",
			pairs: []privacy.MappingPair{
				{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
				{Kind: privacy.KindTable, Original: "users", Token: "table_1"},
			},
			declared:  2,
			forbidden: []string{"orders", "users", "table_1"},
		},
		{
			name:      "negative count",
			pairs:     nil,
			declared:  -1,
			forbidden: []string{"orders", "table_1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := newRendererFromPairs(tt.pairs, tt.declared)
			if r != nil || !errors.Is(err, ErrMappingUnavailable) {
				t.Fatalf("newRendererFromPairs() = (%v, %v), want nil ErrMappingUnavailable", r, err)
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error %q leaks %q", err, forbidden)
				}
			}
		})
	}
}

func TestLoadRejectsMissingDependenciesWithoutLeakingRunID(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		db   interface{}
		key  interface{}
		run  string
	}{
		{name: "nil context", ctx: nil, run: "sensitive-run-id"},
		{name: "nil database", ctx: context.Background(), run: "sensitive-run-id"},
		{name: "empty run", ctx: context.Background(), run: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Load(tt.ctx, nil, nil, tt.run)
			if r != nil || !errors.Is(err, ErrMappingUnavailable) {
				t.Fatalf("Load() = (%v, %v), want nil ErrMappingUnavailable", r, err)
			}
			if strings.Contains(err.Error(), "sensitive-run-id") {
				t.Fatalf("error %q leaks run id", err)
			}
		})
	}
}

func TestRendererFromEnvelope(t *testing.T) {
	crypter := newTestCrypter(t)
	mapper := privacy.NewRunMapper()
	if _, err := mapper.Token(privacy.KindTable, "orders"); err != nil {
		t.Fatalf("tokenize orders: %v", err)
	}
	if _, err := mapper.Token(privacy.KindColumn, "status"); err != nil {
		t.Fatalf("tokenize status: %v", err)
	}
	binding := privacy.EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: mappingEnvelopeStatementIdx}
	envelope, err := privacy.SealMapping(crypter, binding, mapper.Snapshot())
	if err != nil {
		t.Fatalf("seal mapping: %v", err)
	}

	r, err := rendererFromEnvelope(crypter, binding, envelope, mapper.Len())
	if err != nil {
		t.Fatalf("rendererFromEnvelope: %v", err)
	}
	got, count := r.RestoreText("table_1.column_1")
	if got != "orders.status" || count != 2 {
		t.Fatalf("restored = (%q, %d), want (%q, 2)", got, count, "orders.status")
	}
}

func TestRendererFromEnvelopeFailsClosedWithoutLeaks(t *testing.T) {
	crypter := newTestCrypter(t)
	mapper := privacy.NewRunMapper()
	if _, err := mapper.Token(privacy.KindTable, "orders"); err != nil {
		t.Fatalf("tokenize orders: %v", err)
	}
	binding := privacy.EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: mappingEnvelopeStatementIdx}
	envelope, err := privacy.SealMapping(crypter, binding, mapper.Snapshot())
	if err != nil {
		t.Fatalf("seal mapping: %v", err)
	}

	tampered := envelope
	tampered.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	tests := []struct {
		name     string
		crypter  *crypto.Crypter
		binding  privacy.EnvelopeBinding
		envelope crypto.Envelope
		count    int
	}{
		{
			name:     "nil crypter",
			crypter:  nil,
			binding:  binding,
			envelope: envelope,
			count:    mapper.Len(),
		},
		{
			name:     "empty run",
			crypter:  crypter,
			binding:  privacy.EnvelopeBinding{StatementIdx: mappingEnvelopeStatementIdx},
			envelope: envelope,
			count:    mapper.Len(),
		},
		{
			name:     "cross run envelope",
			crypter:  crypter,
			binding:  privacy.EnvelopeBinding{ReviewRunID: "run-43", StatementIdx: mappingEnvelopeStatementIdx},
			envelope: envelope,
			count:    mapper.Len(),
		},
		{
			name:     "corrupt ciphertext",
			crypter:  crypter,
			binding:  binding,
			envelope: tampered,
			count:    mapper.Len(),
		},
		{
			name:     "count mismatch",
			crypter:  crypter,
			binding:  binding,
			envelope: envelope,
			count:    mapper.Len() + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := rendererFromEnvelope(tt.crypter, tt.binding, tt.envelope, tt.count)
			if r != nil || !errors.Is(err, ErrMappingUnavailable) {
				t.Fatalf("rendererFromEnvelope() = (%v, %v), want nil ErrMappingUnavailable", r, err)
			}
			for _, forbidden := range []string{"orders", "table_1", "run-42"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error %q leaks %q", err, forbidden)
				}
			}
		})
	}
}

func TestRestoreIsConcurrentAndImmutable(t *testing.T) {
	r := testRenderer(t, 2,
		privacy.MappingPair{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		privacy.MappingPair{Kind: privacy.KindColumn, Original: "status", Token: "column_1"},
	)
	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, count := r.RestoreText("table_1.column_1")
			if got != "orders.status" || count != 2 {
				errs <- got
			}
		}()
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("concurrent RestoreText() = %q", got)
	}
}

func newTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	return testkit.NewCrypter(t)
}
