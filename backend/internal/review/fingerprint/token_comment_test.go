// token_comment_test.go SEC-0056 dialect adversarial acceptance:
// MySQL-family `--` comments require trailing whitespace / control character or end-of-line (`--x` is two minus signs),
// while PG-family `--` is always a comment; the token difference between `-- x` and `--x` is the foundation of multi-statement detection.
package fingerprint

import "testing"

func tokTexts(t *testing.T, sql string, flags DialectFlags) []string {
	t.Helper()
	out, err := Tokenize([]byte(sql), flags)
	if err != nil {
		t.Fatalf("Tokenize(%q): %v", sql, err)
	}
	texts := make([]string, 0, len(out.Tokens))
	for _, tok := range out.Tokens {
		kind := "other"
		switch tok.Kind {
		case TokWord:
			kind = "word"
		case TokPunct:
			kind = "punct"
		case TokQuotedIdent:
			kind = "quoted"
		case TokNumber:
			kind = "number"
		}
		texts = append(texts, kind+":"+tok.Text)
	}
	return texts
}

// TestMySQLDashDashRequiresWhitespace MySQL family: --x is not a comment (two minus signs),
// so the following semicolon must become a token (relied upon by multi-statement detection); -- x / --\t / --<EOL> are comments.
func TestMySQLDashDashRequiresWhitespace(t *testing.T) {
	mf := mysqlFlags

	// `select 1 --1; drop`: semicolon must be in the token stream (--1 = minus sequence).
	got := tokTexts(t, "select 1 --1; drop", mf)
	hasSemi := false
	for _, s := range got {
		if s == "punct:;" {
			hasSemi = true
		}
	}
	if !hasSemi {
		t.Errorf("mysql `--1;` swallowed semicolon; tokens=%v", got)
	}

	// `select 1 -- 1; drop`: comment consumes to end-of-line, semicolon does not appear (consistent with MySQL semantics).
	got = tokTexts(t, "select 1 -- 1; drop", mf)
	for _, s := range got {
		if s == "punct:;" {
			t.Errorf("mysql `-- ;` must stay comment; tokens=%v", got)
		}
	}

	// `select 1 --\t1; drop`: comment after tab.
	got = tokTexts(t, "select 1 --\t1; drop", mf)
	for _, s := range got {
		if s == "punct:;" {
			t.Errorf("mysql `--\\t;` must stay comment; tokens=%v", got)
		}
	}

	// End-of-line `--` (empty comment) and end-of-input `--`.
	got = tokTexts(t, "select 1 --\nselect 2", mf)
	if len(got) != 4 { // select 1 select 2
		t.Errorf("trailing bare `--` line comment mis-handled; tokens=%v", got)
	}
	got = tokTexts(t, "select 1 --", mf)
	if len(got) != 2 {
		t.Errorf("input-ending `--` must be comment; tokens=%v", got)
	}

	// `---` (third dash followed by whitespace): MySQL semantics = the first `--` is not a comment (followed by `-`,
	// not whitespace) and emits one minus token, while the remaining `-- ` is a comment — one minus token, the rest in comment.
	got = tokTexts(t, "select 1 --- 2", mf)
	dashes := 0
	for _, s := range got {
		if s == "punct:-" {
			dashes++
		}
	}
	if dashes != 1 || len(got) != 3 {
		t.Errorf("`--- ` must lex as one minus + comment; tokens=%v", got)
	}
	// `---2` (no whitespace): three minus tokens (none followed by whitespace).
	got = tokTexts(t, "select 1 ---2", mf)
	dashes = 0
	for _, s := range got {
		if s == "punct:-" {
			dashes++
		}
	}
	if dashes != 3 {
		t.Errorf("`---2` must lex as three minus; tokens=%v", got)
	}
}

// TestPostgresDashDashAlwaysComment PG family: --x is also a comment (dialect difference comparison).
func TestPostgresDashDashAlwaysComment(t *testing.T) {
	got := tokTexts(t, "select 1 --1; drop", postgresFlags)
	for _, s := range got {
		if s == "punct:;" {
			t.Errorf("pg `--1;` must stay comment; tokens=%v", got)
		}
	}
}

// TestMySQLExecutableVersionCommentStays version comment expansion is unaffected (/*!*/ and
// /*!12345...*/ behavior regression: the latter expands to inline tokens, the former expands to zero tokens).
func TestMySQLExecutableVersionCommentStays(t *testing.T) {
	out, err := Tokenize([]byte("select /*+ idx(t) */ 1"), mysqlFlags)
	if err != nil || len(out.Tokens) != 2 {
		t.Errorf("hint comment handling changed: %v %v", out.Tokens, err)
	}
	out, err = Tokenize([]byte("select 1 /*!*/"), mysqlFlags)
	if err != nil || len(out.Tokens) != 2 {
		t.Errorf("/*!*/ plain comment handling changed: %v %v", out.Tokens, err)
	}
	out, err = Tokenize([]byte("/*!40001 select */ 1"), mysqlFlags)
	if err != nil || len(out.Tokens) != 2 || !out.Tokens[0].IsWord("select") {
		t.Errorf("version comment expansion changed: %v %v", out.Tokens, err)
	}
}
