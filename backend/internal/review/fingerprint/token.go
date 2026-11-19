package fingerprint

import (
	"errors"
	"fmt"

	"strings"
	"unicode/utf8"
)

// TokenKind is a lexical token category. Comments are not tokens: line and block comments are stripped in the lexer,
// optimizer Hints are extracted as typed features, and MySQL executable version comments are expanded into inline tokens.
type TokenKind uint8

const (
	TokWord TokenKind = iota
	TokString
	TokQuotedIdent
	TokDollarString
	TokNumber
	TokParam
	TokPunct
)

// Token is a single lexical token.
//   - Word.Text is normalized to lowercase; Literal preserves original casing (used by mapper and leak set);
//   - QuotedIdent.Text is the raw content inside quotes;
//   - String/Binary Text is the raw content inside quotes, and Prefix records the E/X/B/N introducer;
//   - DollarString.Text is the raw content inside delimiters;
//   - Number/Param/Punct Text is the normalized text.
type Token struct {
	Kind    TokenKind
	Text    string
	Literal string
	Prefix  byte // String introducer: 'E','X','B','N', or 0
	Start   int  // Byte offset within input (inclusive); recursively expanded version-comment tokens are rebased to outer positions
	End     int  // End offset (exclusive)
}

// IsPunct reports whether the token is the given punctuation.
func (t Token) IsPunct(s string) bool { return t.Kind == TokPunct && t.Text == s }

// IsWord reports whether the token is the given keyword (lowercase comparison).
func (t Token) IsWord(w string) bool { return t.Kind == TokWord && t.Text == w }

// LexOutput is the complete output of a single lexical scan.
type LexOutput struct {
	Tokens   []Token
	Hints    []string // Typed feature names of optimizer Hints (excluding arguments to prevent identifier leakage)
	Comments []string // Raw comment text (enters only the leak forbidden set, never the output)
	Features []string // Dialect markers (e.g. version_comment)
}

// ErrUnterminated indicates the input was truncated inside a literal or comment;
// streaming statement splitting uses it to request more data, and at EOF it escalates to SQL_PARSE_FAILED.
var ErrUnterminated = errors.New("fingerprint: unterminated literal or comment")

// multiCharOps are multi-character operators matched longest-first.
var multiCharOps = []string{
	"<=>", "!<=", "!>=", "->>", "#>>", "@?", "~~*", "!~~", "!~~*",
	"<<=", ">>=", "::", "#-", "#?",
	"<=", ">=", "<>", "!=", "==", ":=", "=>", "->", "||", "&&", ">>", "<<",
	"@>", "<@", "~~", "#>", "%%",
}

// singleCharOps are single-character punctuation.
var singleCharOps = "()[]{}<>+-*/%=,;.:!&|^~?#@"

// Tokenize performs dialect-aware lexical scanning. Input must be part or all of a single statement;
// semicolon tokens are treated as top-level (semicolons inside strings, comments, or dollar quotes never become tokens).
// On ErrUnterminated, returns the completed token prefix (streaming statement splitting continues using the prefix,
// while statement-level callers treat it as a failure); other lexical errors return no usable tokens.
//
// Backslash-escape and double-quoted string semantics for the MySQL family are fixed by the connection contract
// (database.MySQLSessionSQLMode: no NO_BACKSLASH_ESCAPES, no ANSI_QUOTES),
// so this layer performs a single deterministic parse and no longer guesses dual modes on inputs containing `\'`.
func Tokenize(sql []byte, flags DialectFlags) (LexOutput, error) {
	return tokenizeOnce(sql, flags)
}

// tokenizeOnce performs a single lexical scan; recursive expansion of executable comments still goes through Tokenize
// to ensure the comment body is equally inspected.
func tokenizeOnce(sql []byte, flags DialectFlags) (LexOutput, error) {
	if !utf8.Valid(sql) {
		return LexOutput{}, fmt.Errorf("fingerprint: encoding invalid: input is not valid UTF-8")
	}
	out := LexOutput{}
	i := 0
	n := len(sql)
	for i < n {
		b := sql[i]
		switch {
		case b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\v' || b == '\f':
			i++
		case b == '-' && i+1 < n && sql[i+1] == '-' && dashDashStartsComment(sql, i, flags):
			// Line comments terminate at newline or end of input: within a statement slice both produce equivalent tokens.
			// SEC-0056: MySQL-family -- comments require whitespace / control character
			// or end-of-line after the second dash — `--x` is two minus operators rather than a comment (otherwise
			// the semicolon in `SELECT 1 --1; DROP` would be swallowed into a comment, bypassing token-level multi-statement checks).
			// PG family: -- always starts a line comment.
			end := indexByteFrom(sql, i, '\n')
			if end < 0 {
				out.Comments = append(out.Comments, strings.TrimRight(string(sql[i+2:]), "\r"))
				i = n
				continue
			}
			out.Comments = append(out.Comments, strings.TrimRight(string(sql[i+2:end]), "\r"))
			i = end + 1
		case b == '#' && flags.HashLineComment:
			end := indexByteFrom(sql, i, '\n')
			if end < 0 {
				out.Comments = append(out.Comments, strings.TrimRight(string(sql[i+1:]), "\r"))
				i = n
				continue
			}
			out.Comments = append(out.Comments, strings.TrimRight(string(sql[i+1:end]), "\r"))
			i = end + 1
		case b == '/' && i+1 < n && sql[i+1] == '*':
			body, bodyOffset, next, kind, err := scanBlockComment(sql, i, flags)
			if err != nil {
				if errors.Is(err, ErrUnterminated) {
					return out, ErrUnterminated
				}
				return LexOutput{}, err
			}
			switch kind {
			case blockCommentHint:
				out.Hints = append(out.Hints, hintKind(body))
			case blockCommentVersion:
				// The leading "!" + version digits is an execution condition, not SQL content: strip it before lexing,
				// otherwise version numbers would pollute parameter counts and literal type statistics.
				sqlBody, headerLen := stripVersionHeader(body)
				inner, err := Tokenize([]byte(sqlBody), flags)
				if err != nil {
					// Executable comment bodies are SQL that the server will parse: inner lexical failures
					// (unterminated literals or comments) must fail closed rather than treating a truncated
					// token prefix as a complete statement for classification (NEW-QUERY-05).
					return out, ErrUnterminated
				}
				for _, t := range inner.Tokens {
					t.Start += bodyOffset + headerLen
					t.End += bodyOffset + headerLen
					out.Tokens = append(out.Tokens, t)
				}
				out.Comments = append(out.Comments, inner.Comments...)
				out.Hints = append(out.Hints, inner.Hints...)
				out.Features = append(out.Features, "version_comment")
			default:
				out.Comments = append(out.Comments, body)
			}
			i = next
		case b == '\'':
			tok, next, err := scanQuoted(sql, i, '\'', flags.BackslashEscape)
			if err != nil {
				if errors.Is(err, ErrUnterminated) {
					return out, ErrUnterminated
				}
				return LexOutput{}, err
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		case b == '"' && flags.DoubleQuoteIdent:
			tok, next, err := scanQuotedIdent(sql, i, '"')
			if err != nil {
				if errors.Is(err, ErrUnterminated) {
					return out, ErrUnterminated
				}
				return LexOutput{}, err
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		case b == '"' && !flags.DoubleQuoteIdent:
			tok, next, err := scanQuoted(sql, i, '"', flags.BackslashEscape)
			if err != nil {
				if errors.Is(err, ErrUnterminated) {
					return out, ErrUnterminated
				}
				return LexOutput{}, err
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		case b == '`' && flags.BacktickIdent:
			tok, next, err := scanQuotedIdent(sql, i, '`')
			if err != nil {
				if errors.Is(err, ErrUnterminated) {
					return out, ErrUnterminated
				}
				return LexOutput{}, err
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		case b == '$' && (flags.DollarQuote || flags.DollarParam):
			tok, next, err := scanDollar(sql, i, flags)
			if err != nil {
				if errors.Is(err, ErrUnterminated) {
					return out, ErrUnterminated
				}
				return LexOutput{}, err
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		case isDigit(b) || b == '.' && i+1 < n && isDigit(sql[i+1]):
			tok, next, err := scanNumber(sql, i)
			if err != nil {
				return LexOutput{}, err
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		case isWordStart(b):
			tok, next, err := scanWord(sql, i)
			if err != nil {
				return LexOutput{}, err
			}
			// String introducers: X'1F'/B'01'/E'...'/N'...'.
			// E'' literals always enable backslash escaping (PG semantics); others follow the dialect family.
			if len(tok.Text) == 1 && i+1 < n && sql[i+1] == '\'' {
				c := tok.Text[0]
				if c == 'x' || c == 'b' || c == 'e' || c == 'n' {
					escape := c == 'e' || flags.BackslashEscape
					str, next2, err := scanQuoted(sql, i+1, '\'', escape)
					if err != nil {
						if errors.Is(err, ErrUnterminated) {
							return out, ErrUnterminated
						}
						return LexOutput{}, err
					}
					str.Prefix = upper(c)
					str.Start, str.End = i, next2
					out.Tokens = append(out.Tokens, str)
					i = next2
					continue
				}
			}
			tok.Start, tok.End = i, next
			out.Tokens = append(out.Tokens, tok)
			i = next
		default:
			op, size := scanPunct(sql, i)
			if size == 0 {
				return LexOutput{}, fmt.Errorf("fingerprint: unclassifiable byte %#x at offset %d", b, i)
			}
			out.Tokens = append(out.Tokens, Token{Kind: TokPunct, Text: op, Start: i, End: i + size})
			i += size
		}
	}
	return out, nil
}

// dashDashStartsComment determines whether -- enters a line comment.
// MySQL family (marked by HashLineComment): `--` is a comment only when followed by whitespace / control character
// (including space, tab, newline per MySQL documentation semantics) or end of input; `--x` parses as two minus
// operators (handed off to the punctuation path to emit two '-' tokens).
// PG family: -- followed by any character is a line comment.
func dashDashStartsComment(sql []byte, i int, flags DialectFlags) bool {
	if !flags.HashLineComment {
		return true // PG family
	}
	return i+2 >= len(sql) || sql[i+2] <= ' '
}

type blockKind uint8

const (
	blockCommentPlain blockKind = iota
	blockCommentHint
	blockCommentVersion
)

// scanBlockComment scans /* ... */ and classifies it (plain / Hint / MySQL executable version comment).
// PG family nests; MySQL family does not nest. Executable comment bodies use quote-, backtick-, and
// line-comment-aware MySQL lexical scanning: the comment body is SQL executed by the server, so `*/` inside
// a string or quoted identifier does not close the comment (NEW-QUERY-05); fails closed when boundaries cannot be proven.
func scanBlockComment(sql []byte, start int, flags DialectFlags) (body string, bodyOffset int, next int, kind blockKind, err error) {
	i := start + 2
	depth := 1
	bodyStart := i
	kind = blockKindAt(sql, bodyStart, flags)
	if kind == blockCommentVersion {
		return scanExecutableBlockComment(sql, start, bodyStart, flags)
	}
	for i < len(sql) {
		if flags.NestedBlockComment && sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			depth++
			i += 2
			continue
		}
		if sql[i] == '*' && i+1 < len(sql) && sql[i+1] == '/' {
			depth--
			if depth == 0 {
				return string(sql[bodyStart:i]), bodyStart, i + 2, classifyBlock(string(sql[bodyStart:i]), flags), nil
			}
			i += 2
			continue
		}
		i++
	}
	return "", 0, 0, blockCommentPlain, ErrUnterminated
}

// scanExecutableBlockComment scans MySQL/MariaDB executable comments (/*!, /*M!).
// The executable comment body is processed with SQL lexing: single/double-quoted strings, backtick identifiers,
// backslash escapes, and MySQL `-- ` and `#` line comments may all contain `*/`; only `*/` outside quotes and
// comments closes the outer comment. Any unclosed literal returns ErrUnterminated, which the caller escalates
// to a parse failure (fail closed).
func scanExecutableBlockComment(sql []byte, start, bodyStart int, flags DialectFlags) (body string, bodyOffset int, next int, kind blockKind, err error) {
	i := bodyStart
	for i < len(sql) {
		c := sql[i]
		switch {
		case c == '\'':
			nextI, ok := skipQuotedCommentLiteral(sql, i, '\'', flags.BackslashEscape)
			if !ok {
				return "", 0, 0, blockCommentPlain, ErrUnterminated
			}
			i = nextI
		case c == '"':
			// In MySQL, double quotes denote strings by default and identifiers under ANSI_QUOTES; both close
			// with a double quote, so the skipping rule is identical.
			nextI, ok := skipQuotedCommentLiteral(sql, i, '"', flags.BackslashEscape)
			if !ok {
				return "", 0, 0, blockCommentPlain, ErrUnterminated
			}
			i = nextI
		case c == '`' && flags.BacktickIdent:
			nextI, ok := skipQuotedCommentLiteral(sql, i, '`', false)
			if !ok {
				return "", 0, 0, blockCommentPlain, ErrUnterminated
			}
			i = nextI
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-' &&
			(i+2 >= len(sql) || sql[i+2] <= ' '):
			// MySQL line comment: -- holds only when followed by whitespace / control character, consuming until newline.
			end := indexByteFrom(sql, i, '\n')
			if end < 0 {
				return "", 0, 0, blockCommentPlain, ErrUnterminated
			}
			i = end + 1
		case c == '#' && flags.HashLineComment:
			end := indexByteFrom(sql, i, '\n')
			if end < 0 {
				return "", 0, 0, blockCommentPlain, ErrUnterminated
			}
			i = end + 1
		case c == '*' && i+1 < len(sql) && sql[i+1] == '/':
			return string(sql[bodyStart:i]), bodyStart, i + 2, blockCommentVersion, nil
		default:
			i++
		}
	}
	return "", 0, 0, blockCommentPlain, ErrUnterminated
}

// skipQuotedCommentLiteral skips a quoted literal or identifier inside an executable comment body:
// doubled quotes escape; when backslash is true, a backslash escapes the next byte. Returns the position
// after the closing quote, or ok=false if input ends before closing.
func skipQuotedCommentLiteral(sql []byte, start int, quote byte, backslash bool) (int, bool) {
	i := start + 1
	for i < len(sql) {
		c := sql[i]
		if backslash && c == '\\' && i+1 < len(sql) {
			i += 2
			continue
		}
		if c == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return 0, false
}

// blockKindAt classifies by inspecting only the comment body prefix so executable comment scanning
// can select the quote-aware path beforehand; full classification is still verified by classifyBlock once body is extracted.
func blockKindAt(sql []byte, bodyStart int, flags DialectFlags) blockKind {
	if bodyStart >= len(sql) {
		return blockCommentPlain
	}
	end := bodyStart + 3
	if end > len(sql) {
		end = len(sql)
	}
	return classifyBlock(string(sql[bodyStart:end]), flags)
}

// classifyBlock classifies a block comment (plain / Hint / MySQL-family executable comment).
// NEW-QUERY-01: the version number in MySQL-family `/*!` may be omitted (when omitted, it executes on all
// versions per official Comments documentation), and MariaDB additionally supports `/*M!` and `/*M!<version>`
// executable forms — all three comment bodies must be expanded as SQL for scanning (fail closed). Dialect flags
// do not distinguish MariaDB from MySQL, so `M!` is handled on the stricter side: MySQL servers do not execute
// that form, so expanding it only includes a non-executing comment body in the scan, tightening rather than relaxing.
// The PG family has no executable comment semantics: `/*!` (with or without version digits) is always a plain comment
// and is not expanded (historically `/*!<digits>` was mistakenly expanded in PG; expanding a comment body that PG
// never executes was an overly strict defect and is corrected here).
func classifyBlock(body string, flags DialectFlags) blockKind {
	if strings.HasPrefix(body, "+") {
		return blockCommentHint
	}
	if !flags.ExecutableVersionComment {
		return blockCommentPlain
	}
	if strings.HasPrefix(body, "!") ||
		strings.HasPrefix(body, "M!") || strings.HasPrefix(body, "m!") {
		return blockCommentVersion
	}
	return blockCommentPlain
}

// stripVersionHeader strips the execution condition header of an executable comment — `!` + digits or `M!` + digits
// (version digits optional; when omitted, strips the prefix itself) — returning the SQL body and header length.
func stripVersionHeader(body string) (string, int) {
	off := 0
	switch {
	case len(body) >= 2 && (body[0] == 'M' || body[0] == 'm') && body[1] == '!':
		off = 2
	case len(body) >= 1 && body[0] == '!':
		off = 1
	default:
		return body, 0
	}
	i := off
	for i < len(body) && isDigit(body[i]) {
		i++
	}
	return body[i:], i
}

// hintKind extracts the typed feature name (first word) from a Hint body and discards all arguments.
func hintKind(body string) string {
	body = strings.TrimPrefix(body, "+")
	fields := strings.FieldsFunc(strings.ToLower(body), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '('
	})
	if len(fields) == 0 || fields[0] == "" {
		return "opaque"
	}
	return fields[0]
}

// scanQuoted scans single/double-quoted literals; doubled quotes escape; MySQL family supports backslash escaping.
func scanQuoted(sql []byte, start int, quote byte, backslash bool) (Token, int, error) {
	i := start + 1
	var b strings.Builder
	for i < len(sql) {
		c := sql[i]
		if backslash && c == '\\' && i+1 < len(sql) {
			b.WriteByte(sql[i+1])
			i += 2
			continue
		}
		if c == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				b.WriteByte(quote)
				i += 2
				continue
			}
			return Token{Kind: TokString, Text: b.String()}, i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	return Token{}, 0, ErrUnterminated
}

// scanQuotedIdent scans quoted identifiers (` or "), with doubled-quote escaping.
func scanQuotedIdent(sql []byte, start int, quote byte) (Token, int, error) {
	i := start + 1
	var b strings.Builder
	for i < len(sql) {
		c := sql[i]
		if c == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				b.WriteByte(quote)
				i += 2
				continue
			}
			content := b.String()
			if content == "" {
				return Token{}, 0, fmt.Errorf("fingerprint: empty quoted identifier at offset %d", start)
			}
			return Token{Kind: TokQuotedIdent, Text: content, Literal: content}, i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	return Token{}, 0, ErrUnterminated
}

// scanDollar handles $-prefixed tokens: PG dollar-quoted strings, $n parameters, or failure.
func scanDollar(sql []byte, start int, flags DialectFlags) (Token, int, error) {
	// Try dollar-quote delimiter $tag$ or $$; tag allows only letters, digits, and underscores,
	// and must not swallow the closing '$' (isWordByte includes '$', so a dedicated tag byte set is used).
	if flags.DollarQuote {
		i := start + 1
		for i < len(sql) && isDollarTagByte(sql[i]) {
			i++
		}
		if i < len(sql) && sql[i] == '$' {
			delim := string(sql[start : i+1])
			idx := strings.Index(string(sql[i+1:]), delim)
			if idx < 0 {
				return Token{}, 0, ErrUnterminated
			}
			content := string(sql[i+1 : i+1+idx])
			return Token{Kind: TokDollarString, Text: content}, i + 1 + idx + len(delim), nil
		}
	}
	// $n positional parameter.
	if flags.DollarParam {
		i := start + 1
		for i < len(sql) && isDigit(sql[i]) {
			i++
		}
		if i > start+1 {
			return Token{Kind: TokParam, Text: string(sql[start:i])}, i, nil
		}
	}
	return Token{}, 0, fmt.Errorf("fingerprint: unclassifiable '$' at offset %d", start)
}

// scanNumber scans numeric literals (integer / decimal / scientific notation / hexadecimal).
func scanNumber(sql []byte, start int) (Token, int, error) {
	i := start
	n := len(sql)
	if sql[start] == '0' && start+1 < n && (sql[start+1] == 'x' || sql[start+1] == 'X') {
		i = start + 2
		for i < n && isHexByte(sql[i]) {
			i++
		}
		if i == start+2 {
			return Token{}, 0, fmt.Errorf("fingerprint: malformed hex literal at offset %d", start)
		}
		if i < n && isWordStart(sql[i]) {
			return Token{}, 0, fmt.Errorf("fingerprint: invalid token after hex literal at offset %d", i)
		}
		return Token{Kind: TokNumber, Text: string(sql[start:i])}, i, nil
	}
	for i < n && isDigit(sql[i]) {
		i++
	}
	if i < n && sql[i] == '.' {
		i++
		for i < n && isDigit(sql[i]) {
			i++
		}
	}
	if i < n && (sql[i] == 'e' || sql[i] == 'E') {
		j := i + 1
		if j < n && (sql[j] == '+' || sql[j] == '-') {
			j++
		}
		if j < n && isDigit(sql[j]) {
			for j < n && isDigit(sql[j]) {
				j++
			}
			i = j
		}
	}
	if i < n && isWordStart(sql[i]) {
		return Token{}, 0, fmt.Errorf("fingerprint: invalid token after number literal at offset %d", i)
	}
	return Token{Kind: TokNumber, Text: strings.ToLower(string(sql[start:i]))}, i, nil
}

// scanWord scans bare words (keywords or identifiers).
func scanWord(sql []byte, start int) (Token, int, error) {
	i := start
	for i < len(sql) && isWordByte(sql[i]) {
		i++
	}
	if i == start {
		return Token{}, 0, fmt.Errorf("fingerprint: empty word at offset %d", start)
	}
	raw := string(sql[start:i])
	return Token{Kind: TokWord, Text: strings.ToLower(raw), Literal: raw}, i, nil
}

// scanPunct matches operators/punctuation; returns (text, byteCount), or 0 length on no match.
// Zero-allocation path: byte-by-byte comparison.
func scanPunct(sql []byte, start int) (string, int) {
	rest := sql[start:]
	for _, op := range multiCharOps {
		if matchOp(rest, op) {
			return op, len(op)
		}
	}
	for i := 0; i < len(singleCharOps); i++ {
		if rest[0] == singleCharOps[i] {
			return string(singleCharOps[i : i+1]), 1
		}
	}
	return "", 0
}

func matchOp(rest []byte, op string) bool {
	if len(rest) < len(op) {
		return false
	}
	for i := 0; i < len(op); i++ {
		if rest[i] != op[i] {
			return false
		}
	}
	return true
}

// indexByteFrom returns the absolute index of b in sql[from:], or -1 if not found.
func indexByteFrom(sql []byte, from int, b byte) int {
	for i := from; i < len(sql); i++ {
		if sql[i] == b {
			return i
		}
	}
	return -1
}

// isDollarTagByte checks dollar-quote tag bytes (excluding '$').
func isDollarTagByte(b byte) bool {
	return b == '_' || isDigit(b) || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isHexByte(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
func isWordStart(b byte) bool { return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isWordByte(b byte) bool {
	return isWordStart(b) || isDigit(b) || b == '$'
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}
