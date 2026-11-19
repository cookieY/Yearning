// safety.go implements the Query Safety Check: deterministic single-SELECT validation (PRD 06 §5).
// Accepts only a single pure SELECT (including pure-SELECT CTEs); EXPLAIN, standalone metadata commands, DDL,
// DML, stored procedures, multi-statement batches, and side-effecting functions are all rejected. Together with
// the database adapter's shallow check (validateCursorStatement), this forms a dual guarantee where this layer acts as the strict classifier.
// Classification relies exclusively on the token stream: comments are stripped and strings/numbers are distinct tokens,
// providing inherent immunity against comment bypasses and literal spoofing (including MySQL /*!*/ executable version comments expanded by the tokenizer).
package domain

import (
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// dangerousFunctions lists rejected side-effecting or blocking functions (matched when followed by "(";
// extensible without altering classifier semantics).
var dangerousFunctions = map[string]bool{
	// PostgreSQL: file/directory access, large objects, process signaling, advisory locks, sequences, session config, and replication.
	"dblink": true, "pg_read_file": true, "pg_read_binary_file": true,
	"pg_ls_dir": true, "pg_stat_file": true, "pg_ls_logdir": true,
	"pg_ls_waldir": true, "pg_ls_archive_statusdir": true,
	"lo_import": true, "lo_export": true,
	"pg_terminate_backend": true, "pg_cancel_backend": true,
	"pg_reload_conf": true, "pg_switch_wal": true, "pg_rotate_logfile": true,
	"pg_advisory_lock": true, "pg_advisory_xact_lock": true,
	"pg_advisory_unlock": true, "pg_try_advisory_lock": true,
	"pg_try_advisory_xact_lock": true,
	"setval":                    true, "nextval": true, "set_config": true,
	"pg_notify": true, "pg_logical_emit_message": true,
	"pg_replication_origin_advance": true,
	"pg_sleep":                      true, "pg_sleep_for": true, "pg_sleep_until": true,
	// MySQL: file read, locks, sleep, benchmark, and counters.
	"load_file": true, "get_lock": true, "release_lock": true,
	"is_free_lock": true, "is_used_lock": true,
	"benchmark": true, "sleep": true, "uuid_short": true,
	"master_pos_wait": true, "source_pos_wait": true,
	"wait_for_executed_gtid_set": true,
	"sys_exec":                   true, "sys_eval": true,
	// Shared across both dialects.
	"currval": true,
}

// cteForbiddenHeads lists statement heads forbidden inside CTE bodies (rejecting data-modifying CTEs as a class).
var statementHeads = map[string]bool{
	"select": true, "with": true,
}

// CheckSingleSelect verifies that sql is a single pure SELECT statement (including pure-SELECT CTE chains).
// The postgres flag selects dialect-family lexical rules (PG double-quoted identifiers / dollar-quoting vs MySQL backticks /
// # comments / executable version comments). Returns nil if accepted, or *Error (4007/1001) if rejected.
func CheckSingleSelect(sql string, postgres bool) *Error {
	if sql == "" {
		return ErrValidation()
	}
	if strings.ContainsRune(sql, 0) {
		return ErrValidation()
	}
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	if postgres {
		profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	}
	lex, err := fingerprint.Tokenize([]byte(sql), profile.Flags())
	if err != nil {
		// Cannot prove safety (unterminated literal/comment or unclassifiable byte): fail closed.
		return ErrOnlySingleSelect()
	}
	toks := lex.Tokens
	if len(toks) == 0 {
		return ErrOnlySingleSelect()
	}
	// Multi-statement / statement separator: reject any top-level semicolon in the token stream (semicolons inside
	// string literals are unaffected as the tokenizer keeps them inside literal tokens).
	for _, t := range toks {
		if t.Kind == fingerprint.TokPunct && t.Text == ";" {
			return ErrOnlySingleSelect()
		}
	}
	head := tokenWord(toks, 0)
	if head == "" || !statementHeads[head] {
		return ErrOnlySingleSelect()
	}
	if verr := scanWriteSurfaces(toks); verr != nil {
		return verr
	}
	if verr := scanMidStreamHeads(toks, postgres); verr != nil {
		return verr
	}
	if verr := scanDangerousCalls(toks); verr != nil {
		return verr
	}
	if verr := walkCTEPurity(toks); verr != nil {
		return verr
	}
	return nil
}

// maxBatchStatements bounds the number of statements in a single batch request (Review R1 M2): the legacy
// single-statement rule implicitly bounded this axis, and enabling multi-statement batches replaces it with an explicit cap --
// each statement is still governed by the server-side statement timeout, while exceeding the statement count is rejected
// as a batch shape violation (4007) so a single request cannot tie up a query pool connection indefinitely and amplify SEC-0027 1012 backpressure for other users.
const maxBatchStatements = 16

// CheckSelectBatch validates a semicolon-separated batch of SELECT statements and returns the ordered list
// of individual statement strings (owner 2026-10-30 acceptance ruling F-Q-001: the query workbench permits sequential
// execution of multiple SELECT statements -- the earlier single-statement restriction intentionally narrowed legacy
// MultiSQLRunner, and multi-statement support is restored here without relaxing any safety invariants: every statement
// independently passes CheckSingleSelect, and the result set of the final statement is returned).
// Splitting rules: split only on top-level semicolons (parenthesis depth 0) in the token stream -- semicolons inside
// strings, comments, or quoted identifiers never become top-level semicolon tokens; empty segments (trailing, consecutive,
// comment-only, or leading semicolons) are discarded; all-empty segments or blank input return 1001. Any impure statement
// rejects the entire batch (propagating its 4007/1001 error), as does exceeding maxBatchStatements.
func CheckSelectBatch(sql string, postgres bool) ([]string, *Error) {
	if sql == "" {
		return nil, ErrValidation()
	}
	if strings.ContainsRune(sql, 0) {
		return nil, ErrValidation()
	}
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	if postgres {
		profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	}
	lex, err := fingerprint.Tokenize([]byte(sql), profile.Flags())
	if err != nil {
		// Cannot prove safety: fail closed, consistent with single-statement validation.
		return nil, ErrOnlySingleSelect()
	}
	toks := lex.Tokens
	var stmts []string
	depth := 0
	start := 0
	flush := func(endExclusive int) {
		part := toks[start:endExclusive]
		if len(part) == 0 {
			return
		}
		stmts = append(stmts, strings.TrimSpace(sql[part[0].Start:part[len(part)-1].End]))
	}
	for i, t := range toks {
		switch {
		case t.IsPunct("("):
			depth++
		case t.IsPunct(")"):
			if depth > 0 {
				depth--
			}
		case t.Kind == fingerprint.TokPunct && t.Text == ";" && depth == 0:
			flush(i)
			start = i + 1
		}
	}
	flush(len(toks))
	if len(stmts) == 0 {
		return nil, ErrValidation()
	}
	if len(stmts) > maxBatchStatements {
		return nil, ErrOnlySingleSelect()
	}
	for _, s := range stmts {
		if verr := CheckSingleSelect(s, postgres); verr != nil {
			return nil, verr
		}
	}
	return stmts, nil
}

// midStreamHeads lists strongly reserved statement-head keywords across both dialects: bare-word occurrences
// (outside qualifier or function-call positions) cannot appear in any valid SELECT -- their presence indicates
// a syntax error or smuggled DML via comments/version comments. Quoted forms (TokQuotedIdent) are unaffected.
// Non-reserved PG words like replace/truncate are excluded from the PG set (they can be bare identifiers,
// and version-comment expansion applies only to the MySQL dialect).
var midStreamHeadsMySQL = map[string]bool{
	"insert": true, "update": true, "delete": true, "replace": true,
	"merge": true, "truncate": true, "create": true, "alter": true,
	"drop": true, "grant": true, "revoke": true, "call": true,
}

var midStreamHeadsPG = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true,
	"create": true, "alter": true, "drop": true, "grant": true,
	"revoke": true, "call": true,
}

// scanMidStreamHeads rejects bare statement-head keywords appearing mid-stream in the token sequence
// (defense-in-depth against DML smuggled via expanded version comments; qualifier and function positions
// are excluded to avoid false positives on valid identifiers).
func scanMidStreamHeads(toks []fingerprint.Token, postgres bool) *Error {
	ban := midStreamHeadsMySQL
	if postgres {
		ban = midStreamHeadsPG
	}
	for i, t := range toks {
		if t.Kind != fingerprint.TokWord || !ban[t.Text] {
			continue
		}
		if i == 0 {
			continue // Statement head is validated separately by the caller
		}
		if toks[i-1].IsPunct(".") {
			continue // Qualifier position (non-reserved schema/table name)
		}
		if i+1 < len(toks) && toks[i+1].IsPunct("(") {
			continue // Function position (e.g. PG replace())
		}
		return ErrOnlySingleSelect()
	}
	return nil
}

// scanWriteSurfaces rejects write or locking clauses attached to a SELECT: INTO (new table/file/
// session variable), FOR UPDATE|SHARE, and LOCK IN SHARE MODE.
func scanWriteSurfaces(toks []fingerprint.Token) *Error {
	for i, t := range toks {
		if t.Kind != fingerprint.TokWord {
			continue
		}
		switch t.Text {
		case "into":
			// Any SELECT-level INTO is a write target (PG new table / MySQL OUTFILE or variable).
			return ErrOnlySingleSelect()
		case "for":
			if w := tokenWord(toks, i+1); w == "update" || w == "share" {
				return ErrOnlySingleSelect()
			}
		case "lock":
			if w := tokenWord(toks, i+1); w == "in" {
				return ErrOnlySingleSelect()
			}
		}
	}
	return nil
}

// scanDangerousCalls rejects side-effecting or blocking function calls (word or quoted identifier followed by "(").
// SEC-0008: PG double-quoted and MySQL backtick-quoted identifiers ("pg_read_file"(...),
// pg_catalog."pg_read_file"(...), `sleep`(1)) are semantically equivalent to bare-word function calls --
// unquote the identifier (Text holds the raw inner content preserving case), normalize to lowercase, and check
// the denylist; schema-qualified forms match on the final segmentautomatically since qualifiers are separate tokens.
// TokWord.Text is already lowercase; TokQuotedIdent.Text preserves original case.
func scanDangerousCalls(toks []fingerprint.Token) *Error {
	for i, t := range toks {
		var name string
		switch t.Kind {
		case fingerprint.TokWord:
			name = t.Text
		case fingerprint.TokQuotedIdent:
			name = strings.ToLower(t.Text)
		default:
			continue
		}
		if !dangerousFunctions[name] {
			continue
		}
		if i+1 < len(toks) && toks[i+1].IsPunct("(") {
			return ErrOnlySingleSelect()
		}
	}
	return nil
}

// walkCTEPurity verifies that every CTE body in every WITH chain begins with SELECT or WITH
// (data-modifying CTEs such as `WITH x AS (DELETE...)` are rejected as a class in both dialects,
// across arbitrarily nested WITH chains).
func walkCTEPurity(toks []fingerprint.Token) *Error {
	for i, t := range toks {
		if t.Kind != fingerprint.TokWord || t.Text != "with" {
			continue
		}
		pos := i + 1
		if w := tokenWord(toks, pos); w == "recursive" {
			pos++
		}
		for {
			// CTE name (identifier)
			if tokenWord(toks, pos) == "" {
				return ErrOnlySingleSelect()
			}
			pos++
			// Optional column list
			if pos < len(toks) && toks[pos].IsPunct("(") {
				end, ok := matchParen(toks, pos)
				if !ok {
					return ErrOnlySingleSelect()
				}
				pos = end + 1
			}
			// AS [NOT] [MATERIALIZED] ( body )
			if w := tokenWord(toks, pos); w != "as" {
				return ErrOnlySingleSelect()
			}
			pos++
			if w := tokenWord(toks, pos); w == "not" {
				pos++
			}
			if w := tokenWord(toks, pos); w == "materialized" {
				pos++
			}
			if pos >= len(toks) || !toks[pos].IsPunct("(") {
				return ErrOnlySingleSelect()
			}
			bodyStart := pos + 1
			end, ok := matchParen(toks, pos)
			if !ok {
				return ErrOnlySingleSelect()
			}
			head := tokenWord(toks, bodyStart)
			if head == "" || !statementHeads[head] {
				return ErrOnlySingleSelect()
			}
			pos = end + 1
			// Next CTE or end of WITH chain (comma-separated)
			if pos < len(toks) && toks[pos].IsPunct(",") {
				pos++
				continue
			}
			break
		}
	}
	return nil
}

// tokenWord returns the lowercase word at idx, or an empty string for non-word tokens.
func tokenWord(toks []fingerprint.Token, idx int) string {
	if idx < 0 || idx >= len(toks) || toks[idx].Kind != fingerprint.TokWord {
		return ""
	}
	return toks[idx].Text
}

// matchParen returns the index of the closing parenthesis matching toks[start] ("(").
func matchParen(toks []fingerprint.Token, start int) (int, bool) {
	depth := 0
	for i := start; i < len(toks); i++ {
		if toks[i].IsPunct("(") {
			depth++
		} else if toks[i].IsPunct(")") {
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}
