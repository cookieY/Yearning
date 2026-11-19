// token_exec_comment_test.go NEW-QUERY-01 executable comment adversarial acceptance:
// the version number in MySQL-family `/*!` may be omitted (executing on all versions when omitted), and MariaDB
// additionally has `/*M!` and `/*M!<version>` forms — all three comment bodies are SQL executed by the server
// and must be expanded into tokens for dangerous-function and write-surface scanning rather than discarded as plain comments;
// `/*!<digits>` serves as the existing regression anchor. Plain `/* ... */` and optimizer Hints `/*+ ... */`
// remain stripped in both families; PG-family `/*!` (including versioned) is treated as a plain comment.
package fingerprint

import "testing"

func hasWordToken(out LexOutput, w string) bool {
	for _, t := range out.Tokens {
		if t.IsWord(w) {
			return true
		}
	}
	return false
}

func hasNumberToken(out LexOutput, n string) bool {
	for _, t := range out.Tokens {
		if t.Kind == TokNumber && t.Text == n {
			return true
		}
	}
	return false
}

func TestMySQLExecutableCommentNoVersionExpands(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"bare /*!", "select 1 /*! , sleep(10) */"},
		{"maria M!", "select 1 /*M! , sleep(10) */"},
		{"maria M! version", "select 1 /*M!100100 , sleep(10) */"},
		{"versioned /*!50000", "select 1 /*!50000 , sleep(10) */"},
	}
	for _, c := range cases {
		out, err := Tokenize([]byte(c.sql), mysqlFlags)
		if err != nil {
			t.Fatalf("%s: Tokenize: %v", c.name, err)
		}
		if !hasWordToken(out, "sleep") {
			t.Errorf("%s: executable comment body dropped (no sleep token); tokens=%v", c.name, out.Tokens)
		}
	}

	// The execution condition header of the M! form must be stripped: m/!/100100 must not enter the token stream
	// (which would pollute parameter counts and literal statistics).
	out, err := Tokenize([]byte("select 1 /*M!100100 , 2 */"), mysqlFlags)
	if err != nil {
		t.Fatalf("M! version header: Tokenize: %v", err)
	}
	if hasWordToken(out, "m") || hasNumberToken(out, "100100") {
		t.Errorf("M! header not stripped; tokens=%v", out.Tokens)
	}
	// Empty executable comment (/*!*/) expands to zero tokens without affecting the surrounding statement.
	out, err = Tokenize([]byte("select 1 /*!*/"), mysqlFlags)
	if err != nil || len(out.Tokens) != 2 {
		t.Errorf("/*!*/ must expand to zero tokens: %v %v", out.Tokens, err)
	}
}

func TestMySQLPlainAndHintCommentsStayDropped(t *testing.T) {
	// Dangerous patterns inside plain comments must not enter the token stream (server does not execute them).
	out, err := Tokenize([]byte("select 1 /* sleep(10) */"), mysqlFlags)
	if err != nil {
		t.Fatalf("Tokenize: %v", err)
	}
	if hasWordToken(out, "sleep") {
		t.Errorf("plain comment body leaked into tokens; tokens=%v", out.Tokens)
	}
	// Optimizer Hint: extracted as a typed feature; arguments do not enter the token stream.
	out, err = Tokenize([]byte("select 1 /*+ MAX_EXECUTION_TIME(1000) */"), mysqlFlags)
	if err != nil {
		t.Fatalf("Tokenize: %v", err)
	}
	if hasWordToken(out, "max_execution_time") || len(out.Tokens) != 2 {
		t.Errorf("hint body leaked into tokens; tokens=%v hints=%v", out.Tokens, out.Hints)
	}
	if len(out.Hints) != 1 || out.Hints[0] != "max_execution_time" {
		t.Errorf("hint not typed-extracted; hints=%v", out.Hints)
	}
}

func TestPostgresExecutableCommentIsPlain(t *testing.T) {
	// PG has no executable comment semantics: /*! and /*!50000 are both plain comments, and the server
	// does not execute the comment body — comment content must not enter the token stream.
	for _, sql := range []string{
		"select 1 /*! , pg_sleep(10) */",
		"select 1 /*!50000 , pg_sleep(10) */",
	} {
		out, err := Tokenize([]byte(sql), postgresFlags)
		if err != nil {
			t.Fatalf("Tokenize(%q): %v", sql, err)
		}
		if hasWordToken(out, "pg_sleep") {
			t.Errorf("pg comment body leaked into tokens; sql=%q tokens=%v", sql, out.Tokens)
		}
	}
}

// TestMySQLExecCommentQuoteAware NEW-QUERY-05 regression: `*/` inside quotes within an executable
// comment body must not close the comment early; the full comment body must enter the token stream
// (so sleep/OUTFILE is visible to upper classifiers). Quotes after a `-- ` line comment must not cause desynchronization either.
func TestMySQLExecCommentQuoteAware(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			"quoted close then sleep",
			"select 1 /*! , 'a*/', sleep(0.2), 1 -- '\n*/",
			[]string{"sleep"},
		},
		{
			"quoted close then outfile",
			"select 1 /*! , 'a*/' into outfile '/tmp/r2x' -- '\n*/",
			[]string{"into", "outfile"},
		},
		{
			"plain quote inside comment",
			"select 1 /*! , 'a*/' */",
			nil,
		},
	}
	for _, c := range cases {
		out, err := Tokenize([]byte(c.sql), mysqlFlags)
		if err != nil {
			t.Fatalf("%s: Tokenize: %v", c.name, err)
		}
		for _, w := range c.want {
			if !hasWordToken(out, w) {
				t.Errorf("%s: missing %q token; tokens=%v", c.name, w, out.Tokens)
			}
		}
	}
}

// TestMySQLExecCommentUnterminatedFailsClosed an unclosed literal inside an executable comment body
// must return ErrUnterminated so the caller fails closed rather than classifying a truncated
// comment body as a complete statement (NEW-QUERY-05).
func TestMySQLExecCommentUnterminatedFailsClosed(t *testing.T) {
	for _, sql := range []string{
		"select 1 /*! , 'a*/",
		"select 1 /*! , `a*/",
		"select 1 /*! , \"a*/",
	} {
		if _, err := Tokenize([]byte(sql), mysqlFlags); err == nil {
			t.Errorf("Tokenize(%q) accepted unterminated executable comment literal; want error", sql)
		}
	}
}

// TestMySQLExecCommentEscapedQuoteDeterministic NEW-QUERY-08: the connection contract fixes
// sql_mode (no NO_BACKSLASH_ESCAPES/ANSI_QUOTES), so backslash-escaped quotes are parsed under a
// single deterministic semantics; the escaped quote does not close the string, and sleep/benchmark
// must not become executable tokens. If the connection contract ever allows custom sql_mode in the
// future, the dual-mode consistency check must be restored in lockstep.
func TestMySQLExecCommentEscapedQuoteDeterministic(t *testing.T) {
	for _, sql := range []string{
		`select 1 /*! , 'a\', sleep(0.2), 'b*/`,
		"select 1 /*! , 'a\\', 1 -- '\n*/",
		`select 1 /*M! , 'a\',benchmark(1)' */`,
	} {
		out, err := Tokenize([]byte(sql), mysqlFlags)
		if err != nil {
			t.Fatalf("Tokenize(%q): %v", sql, err)
		}
		if hasWordToken(out, "sleep") || hasWordToken(out, "benchmark") {
			t.Errorf("escaped-quote literal leaked executable token: %q tokens=%v", sql, out.Tokens)
		}
	}
}
