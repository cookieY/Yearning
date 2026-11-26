package fingerprint

import (
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// timeUnitWords are INTERVAL/EXTRACT time units; preserved verbatim in unit slots.
var timeUnitWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
year years month months week weeks day days hour hours minute minutes
second seconds millisecond milliseconds microsecond microseconds
quarter dayofweek dayofyear isoyear isodow epoch decade century millennium
`) {
		timeUnitWords[w] = true
	}
}

// parseExprSeq consumes an expression sequence.
// aliasEnabled: allows SELECT-list-style implicit aliases (trailing bare word).
// listMode: top-level commas are consumed as element separators; when false, a comma terminates (SET assignments, ORDER items, etc.).
func (w *walker) parseExprSeq(aliasEnabled, listMode bool, stops ...string) {
	for !w.eof() && !w.res.fail {
		if len(stops) > 0 && w.atWord(stops...) {
			return
		}
		if w.atPunct(")") {
			return
		}
		if w.atPunct(",") {
			if listMode {
				w.pos++
				continue
			}
			return
		}
		w.parseExprAtom(aliasEnabled)
		if w.res.fail {
			return
		}
		if aliasEnabled && w.isIdentLike() && !w.atWord(stops...) {
			t := *w.cur()
			if t.Kind == TokQuotedIdent || t.Kind == TokWord && !isReserved(t.Text) {
				w.markRole(privacy.KindAlias, mustName(t))
				w.pos++
			}
		}
	}
}

// parseExprAtom consumes a single expression atom and its postfixes (JSON operators / :: type / subscripts).
func (w *walker) parseExprAtom(aliasEnabled bool) {
	t := w.cur()
	if t == nil {
		w.fail()
		return
	}
	switch t.Kind {
	case TokWord:
		if isReserved(t.Text) {
			w.parseReservedAtom(t.Text)
			return
		}
		w.parseNameRef()
	case TokQuotedIdent:
		w.parseNameRef()
	case TokString, TokNumber, TokDollarString, TokParam:
		w.pos++
	case TokPunct:
		switch t.Text {
		case "(":
			w.parseParenExpr()
		case "-", "+", "~", "!":
			w.pos++
		case "[":
			w.parseSubscript()
		default:
			w.pos++ // Binary operator
		}
	default:
		w.fail()
	}
	w.parsePostfix()
}

// parseReservedAtom handles reserved-word atoms (including IN-list collapsing and AS alias/type).
func (w *walker) parseReservedAtom(word string) {
	switch word {
	case "in":
		w.pos++
		if !w.atPunct("(") {
			w.fail()
			return
		}
		w.parseInParen()
	case "interval":
		w.pos++
		w.parseExprAtom(false)
		// Word part of INTERVAL 1 DAY / INTERVAL '1 day': preserved in the unit slot.
		if w.cur() != nil && w.cur().Kind == TokWord && timeUnitWords[w.cur().Text] {
			w.keepRawAt(w.pos)
			w.pos++
		}
	case "as":
		// Explicit alias or CAST target type (distinguished by castDepth).
		w.pos++
		if w.castDepth > 0 {
			if !w.parseTypeName() {
				w.fail()
			}
			return
		}
		if !w.isIdentLike() {
			w.fail()
			return
		}
		nt := *w.cur()
		w.markRole(privacy.KindAlias, mustName(nt))
		w.pos++
	default:
		w.pos++
	}
}

// parseNameRef handles identifier atoms: function names / qualified names / plain columns.
// Quoted identifiers are never preserved as function names (their content is a user identifier and must be anonymized).
func (w *walker) parseNameRef() {
	if parts, open, ok := w.functionCallParts(); ok {
		name := w.toks[parts[len(parts)-1]].Text
		controlled := len(parts) == 1 &&
			w.toks[parts[0]].Kind == TokWord &&
			isBuiltinFunction(name)
		if controlled {
			w.keepRawAt(parts[0])
		} else {
			// Qualified or quoted function names cannot be proven to be
			// built-ins. Clear any provisional identifier roles so the
			// renderer emits generic tokens and no false object references.
			w.markMetadataIncomplete()
			for _, i := range parts {
				w.res.roles[i] = idRole{}
				w.res.keepRaw[i] = false
			}
		}
		w.pos = open
		if controlled && (name == "cast" || name == "convert") {
			w.castDepth++
			w.parseParenExpr()
			w.castDepth--
			return
		}
		w.parseParenExpr()
		return
	}
	if !w.parseColumnName() {
		w.fail()
	}
}

// functionCallParts recognizes identifier chains immediately followed by '('.
// The returned open index points at the opening parenthesis.
func (w *walker) functionCallParts() ([]int, int, bool) {
	var parts []int
	for i := w.pos; i < len(w.toks); {
		t := w.toks[i]
		if t.Kind != TokWord && t.Kind != TokQuotedIdent {
			return nil, 0, false
		}
		parts = append(parts, i)
		i++
		if i < len(w.toks) && w.toks[i].IsPunct("(") {
			return parts, i, true
		}
		if i >= len(w.toks) || !w.toks[i].IsPunct(".") {
			return nil, 0, false
		}
		i++
	}
	return nil, 0, false
}

// parseInParen handles IN (...): pure literal lists collapse cardinality; all others are handled as general parenthesized expressions.
func (w *walker) parseInParen() {
	openIdx := w.pos
	depth := 0
	literalCount := 0
	isLiteralList := true
	i := w.pos
	for i < len(w.toks) {
		tk := &w.toks[i]
		if tk.IsPunct("(") {
			depth++
			if depth > 1 {
				isLiteralList = false
			}
			i++
			continue
		}
		if tk.IsPunct(")") {
			depth--
			if depth == 0 {
				i++
				break
			}
			i++
			continue
		}
		if depth == 1 {
			switch {
			case tk.Kind == TokString || tk.Kind == TokNumber || tk.Kind == TokDollarString || tk.Kind == TokParam:
				literalCount++
			case tk.Kind == TokWord && (tk.Text == "null" || tk.Text == "true" || tk.Text == "false"):
				literalCount++
			case tk.IsPunct(","):
			case tk.Kind == TokQuotedIdent:
				isLiteralList = false // Quoted identifier element
			default:
				isLiteralList = false
			}
		}
		i++
	}
	if depth != 0 {
		w.fail()
		return
	}
	if isLiteralList && literalCount > 0 {
		w.res.specials = append(w.res.specials, specialSpan{
			kind: specialInList, tokStart: openIdx, tokEnd: i, count: literalCount,
		})
		w.res.inSizes = append(w.res.inSizes, literalCount)
		w.pos = i
		return
	}
	w.parseParenExpr()
}

// parseParenExpr handles a parenthesized group: subquery or expression group / function arguments; returns whether it succeeded.
func (w *walker) parseParenExpr() bool {
	if !w.atPunct("(") {
		w.fail()
		return false
	}
	w.pos++
	w.depth++
	defer func() { w.depth-- }()
	if w.depth > w.maxDepth {
		w.fail()
		return false
	}
	if w.atWord("select") || w.atWord("with") {
		if w.atWord("with") {
			// Embedded WITH subquery: recurse as CTE + SELECT.
			kind := w.parseWithCTEs()
			if w.res.fail || kind != "select" {
				w.fail()
				return false
			}
			w.parseSelectBody()
		} else {
			w.pos++
			w.parseSelectBody()
		}
		if w.res.fail {
			return false
		}
		if !w.consumeClose() {
			w.fail()
			return false
		}
		return true
	}
	w.parseExprSeq(false, true, ")")
	if w.res.fail {
		return false
	}
	if !w.consumeClose() {
		w.fail()
		return false
	}
	return true
}

// parseSubscript consumes an array/JSON subscript [ ... ].
func (w *walker) parseSubscript() {
	w.pos++
	w.parseExprSeq(false, true, "]")
	if w.res.fail {
		return
	}
	if !w.atPunct("]") {
		w.fail()
		return
	}
	w.pos++
}

// parsePostfix consumes atom postfixes: :: type, JSON operators, subscripts.
func (w *walker) parsePostfix() {
	for !w.eof() {
		t := w.cur()
		if t.Kind != TokPunct {
			return
		}
		switch t.Text {
		case "::":
			w.pos++
			if !w.parseTypeName() {
				w.fail()
				return
			}
		case "->", "->>", "#>", "#>>", "@>", "<@", "@?", "#-", "#?":
			w.pos++
			if w.atLiteral() {
				w.pos++
			} else if w.isIdentLike() {
				w.markRole(privacy.KindColumn, mustName(*w.cur()))
				w.pos++
			}
		case "[":
			w.parseSubscript()
			if w.res.fail {
				return
			}
		default:
			return
		}
	}
}
