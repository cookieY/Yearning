// restorer_anon_scope.go performs lightweight scope analysis for anonymized SQL drafts
// in ValidateAndRestoreSQL so legitimate CTE names, table/subquery/SELECT aliases,
// new index names, and constraint names are accepted without relaxing unauthorized object boundaries.
package privacy

import (
	"strings"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type anonScopeAnalysis struct {
	consumed           map[int]bool
	cteNames           map[string]bool
	cteCols            map[string]bool
	referencedCTEs     map[string]bool
	tableAliases       map[string]bool
	subqueryAliases    map[string]bool
	selectAliases      map[string]bool
	indexNames         map[string]bool
	constraintNames    map[string]bool
	selectAliasRefSite []bool
}

func isReservedAnonObjectToken(low string) bool {
	if !revprivacy.AnonTokenPattern.MatchString(low) {
		return false
	}
	return strings.HasPrefix(low, "database_") ||
		strings.HasPrefix(low, "schema_") ||
		strings.HasPrefix(low, "table_") ||
		strings.HasPrefix(low, "column_") ||
		strings.HasPrefix(low, "ident_")
}

func isValidAnonLocalIdent(tok fingerprint.Token) (string, bool) {
	ident, ok := rawIdentName(tok)
	if !ok {
		return "", false
	}
	low := strings.ToLower(strings.TrimSpace(ident))
	if low == "" {
		return "", false
	}
	if tok.Kind == fingerprint.TokWord && isRawSQLKeyword(low) {
		return "", false
	}
	if isReservedAnonObjectToken(low) || valPlaceholderRe.MatchString(low) {
		return "", false
	}
	return low, true
}

type anonScopeWalk struct {
	toks               []fingerprint.Token
	stmtKind           domain.StatementKind
	st                 *anonScopeAnalysis
	depth              int
	fromClauseAtDepth  map[int]bool
	funcArgDepth       map[int]bool
	subqueryParenDepth map[int]bool
	clauseAtDepth      map[int]string
}

func (w *anonScopeWalk) handleOpenParen(i int) {
	isTableSubquery := isRawTablePosition(w.toks, i, w.stmtKind, w.fromClauseAtDepth[w.depth], w.funcArgDepth[w.depth])
	w.depth++
	if isTableSubquery {
		w.subqueryParenDepth[w.depth] = true
	}
	if i > 0 && w.toks[i-1].Kind == fingerprint.TokWord && isFunctionParenFrom(strings.ToLower(w.toks[i-1].Text)) {
		w.funcArgDepth[w.depth] = true
	}
}

func (w *anonScopeWalk) handleCloseParen(i int) int {
	wasSubquery := w.subqueryParenDepth[w.depth]
	delete(w.fromClauseAtDepth, w.depth)
	delete(w.funcArgDepth, w.depth)
	delete(w.subqueryParenDepth, w.depth)
	delete(w.clauseAtDepth, w.depth)
	if w.depth > 0 {
		w.depth--
	}
	if !wasSubquery {
		return i
	}
	nextIdx := consumeAnonOptionalAlias(w.toks, i+1, func(aliasLow string) {
		w.st.subqueryAliases[aliasLow] = true
	}, w.st)
	if nextIdx > i+1 {
		return nextIdx - 1
	}
	return i
}

func (w *anonScopeWalk) updateClauseState(i int, t fingerprint.Token) {
	if t.Kind != fingerprint.TokWord {
		return
	}
	switch strings.ToLower(t.Text) {
	case "from":
		if !w.funcArgDepth[w.depth] {
			w.fromClauseAtDepth[w.depth] = true
			w.clauseAtDepth[w.depth] = "from"
		}
	case "group", "having", "order":
		w.fromClauseAtDepth[w.depth] = false
		w.clauseAtDepth[w.depth] = strings.ToLower(t.Text)
	case "where", "limit", "offset", "fetch", "union", "intersect", "except", "set", "values", "returning", "select", "window", "on", "using", "check":
		w.fromClauseAtDepth[w.depth] = false
		w.clauseAtDepth[w.depth] = strings.ToLower(t.Text)
	}
	switch w.clauseAtDepth[w.depth] {
	case "group", "having", "order":
		w.st.selectAliasRefSite[i] = true
	}
}

func (w *anonScopeWalk) tryConsumeDeclaration(i int) int {
	t := w.toks[i]
	if prevIsIndexContext(w.toks, i) {
		if low, ok := isValidAnonLocalIdent(t); ok {
			w.st.indexNames[low] = true
			w.st.consumed[i] = true
			return i
		}
	}
	if i > 0 && w.toks[i-1].IsWord("constraint") {
		if low, ok := isValidAnonLocalIdent(t); ok {
			w.st.constraintNames[low] = true
			w.st.consumed[i] = true
			return i
		}
	}
	if isRawTablePosition(w.toks, i, w.stmtKind, w.fromClauseAtDepth[w.depth], w.funcArgDepth[w.depth]) {
		if nextIdx, ok := consumeAnonTableRefAlias(w.toks, i, w.st); ok {
			return nextIdx - 1
		}
	}
	if prevIsAliasContext(w.toks, i) {
		if low, ok := isValidAnonLocalIdent(t); ok && !(w.stmtKind == domain.StmtCreateTable && t.IsWord("select")) {
			w.st.selectAliases[low] = true
			w.st.consumed[i] = true
		}
	}
	return i
}

func collectAnonDeclaredScope(toks []fingerprint.Token, stmtKind domain.StatementKind) *anonScopeAnalysis {
	st := &anonScopeAnalysis{
		consumed:           make(map[int]bool),
		cteNames:           make(map[string]bool),
		cteCols:            make(map[string]bool),
		referencedCTEs:     make(map[string]bool),
		tableAliases:       make(map[string]bool),
		subqueryAliases:    make(map[string]bool),
		selectAliases:      make(map[string]bool),
		indexNames:         make(map[string]bool),
		constraintNames:    make(map[string]bool),
		selectAliasRefSite: make([]bool, len(toks)),
	}
	if stmtKind == domain.StmtSelect && len(toks) > 0 && toks[0].IsWord("with") {
		collectAnonCTEDeclarations(toks, st)
	}
	w := &anonScopeWalk{
		toks:               toks,
		stmtKind:           stmtKind,
		st:                 st,
		fromClauseAtDepth:  make(map[int]bool),
		funcArgDepth:       make(map[int]bool),
		subqueryParenDepth: make(map[int]bool),
		clauseAtDepth:      make(map[int]string),
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.IsPunct("(") {
			w.handleOpenParen(i)
			continue
		}
		if t.IsPunct(")") {
			i = w.handleCloseParen(i)
			continue
		}
		w.updateClauseState(i, t)
		if !st.consumed[i] {
			i = w.tryConsumeDeclaration(i)
		}
	}
	return st
}

type cteDeclScanner struct {
	toks          []fingerprint.Token
	consumed      map[int]bool
	cteNames      map[string]bool
	cteCols       map[string]bool
	extractIdent  func(fingerprint.Token) (string, bool)
	depth         int
	expectCTEName bool
	inCTEDefList  bool
}

func (s *cteDeclScanner) step(i int) (stop bool) {
	t := s.toks[i]
	if t.IsPunct("(") {
		s.depth++
		return false
	}
	if t.IsPunct(")") {
		if s.depth > 0 {
			s.depth--
		}
		if s.depth == 0 {
			if s.inCTEDefList {
				s.inCTEDefList = false
			} else {
				s.expectCTEName = false
			}
		}
		return false
	}
	if s.depth == 1 && s.inCTEDefList {
		if low, ok := s.extractIdent(t); ok {
			s.cteCols[low] = true
			s.consumed[i] = true
		}
		return false
	}
	if s.depth != 0 || (t.IsWord("recursive") && s.expectCTEName) {
		return false
	}
	if t.IsWord("select") && !s.expectCTEName {
		return true
	}
	if t.IsPunct(",") {
		s.expectCTEName = true
		s.inCTEDefList = false
		return false
	}
	if s.expectCTEName {
		if low, ok := s.extractIdent(t); ok {
			s.cteNames[low] = true
			s.consumed[i] = true
			s.expectCTEName = false
			s.inCTEDefList = i+1 < len(s.toks) && s.toks[i+1].IsPunct("(")
		}
	}
	return false
}

func collectCTEDeclarations(
	toks []fingerprint.Token,
	consumed map[int]bool,
	cteNames, cteCols map[string]bool,
	extractIdent func(fingerprint.Token) (string, bool),
) {
	scanner := &cteDeclScanner{
		toks:          toks,
		consumed:      consumed,
		cteNames:      cteNames,
		cteCols:       cteCols,
		extractIdent:  extractIdent,
		expectCTEName: true,
	}
	for i := 1; i < len(toks); i++ {
		if scanner.step(i) {
			return
		}
	}
}

func collectAnonCTEDeclarations(toks []fingerprint.Token, st *anonScopeAnalysis) {
	collectCTEDeclarations(toks, st.consumed, st.cteNames, st.cteCols, isValidAnonLocalIdent)
}

func consumeAnonTableRefAlias(toks []fingerprint.Token, idx int, st *anonScopeAnalysis) (int, bool) {
	first, ok := rawIdentName(toks[idx])
	if !ok {
		return idx, false
	}
	firstLow := strings.ToLower(strings.TrimSpace(first))

	// Case 1: schema_N . table_N [AS] <alias>
	if idx+2 < len(toks) && toks[idx+1].IsPunct(".") {
		second, ok2 := rawIdentName(toks[idx+2])
		if !ok2 {
			return idx, false
		}
		secondLow := strings.ToLower(strings.TrimSpace(second))
		if strings.HasPrefix(firstLow, "schema_") && revprivacy.AnonTokenPattern.MatchString(firstLow) &&
			strings.HasPrefix(secondLow, "table_") && revprivacy.AnonTokenPattern.MatchString(secondLow) {
			endIdx := consumeAnonOptionalAlias(toks, idx+3, func(aliasLow string) {
				st.tableAliases[aliasLow] = true
			}, st)
			return endIdx, true
		}
		return idx, false
	}

	// Case 2: table_N [AS] <alias>
	if strings.HasPrefix(firstLow, "table_") && revprivacy.AnonTokenPattern.MatchString(firstLow) {
		endIdx := consumeAnonOptionalAlias(toks, idx+1, func(aliasLow string) {
			st.tableAliases[aliasLow] = true
		}, st)
		return endIdx, true
	}

	// Case 3: declared CTE name [AS] <alias>
	if st.cteNames[firstLow] {
		st.consumed[idx] = true
		st.referencedCTEs[firstLow] = true
		endIdx := consumeAnonOptionalAlias(toks, idx+1, func(aliasLow string) {
			st.subqueryAliases[aliasLow] = true
		}, st)
		return endIdx, true
	}

	return idx, false
}

func consumeAnonOptionalAlias(toks []fingerprint.Token, pos int, onAlias func(string), st *anonScopeAnalysis) int {
	if pos >= len(toks) {
		return pos
	}
	if toks[pos].IsWord("as") && pos+1 < len(toks) {
		if aliasLow, ok := isValidAnonLocalIdent(toks[pos+1]); ok {
			st.consumed[pos] = true
			st.consumed[pos+1] = true
			onAlias(aliasLow)
			return pos + 2
		}
		return pos
	}
	if aliasLow, ok := isValidAnonLocalIdent(toks[pos]); ok {
		st.consumed[pos] = true
		onAlias(aliasLow)
		return pos + 1
	}
	return pos
}

func (st *anonScopeAnalysis) isAllowedLocalIdentifier(toks []fingerprint.Token, idx int, low string) bool {
	if st == nil || low == "" {
		return false
	}
	// Column/attribute after qualifier: real table aliases never fall back to local aliases or CTE columns.
	if idx > 0 && toks[idx-1].IsPunct(".") {
		if idx < 2 {
			return false
		}
		qual, ok := rawIdentName(toks[idx-2])
		if !ok {
			return false
		}
		qualLow := strings.ToLower(strings.TrimSpace(qual))
		if st.tableAliases[qualLow] {
			return false
		}
		if st.cteNames[qualLow] || st.subqueryAliases[qualLow] {
			return st.selectAliases[low] || st.cteCols[low]
		}
		return false
	}
	// Qualifier before "." (e.g., a.column_1 or cte.column_1).
	if idx+1 < len(toks) && toks[idx+1].IsPunct(".") {
		return st.tableAliases[low] || st.subqueryAliases[low] || st.cteNames[low]
	}
	// Unqualified local identifiers are strictly scoped: exact declaration/FROM-CTE sites,
	// ORDER BY / GROUP BY / HAVING select-alias references, or explicit CTE column names when a CTE is referenced.
	if idx >= 0 && idx < len(st.consumed) && st.consumed[idx] {
		return true
	}
	if st.consumed[idx] {
		return true
	}
	if idx >= 0 && idx < len(st.selectAliasRefSite) && st.selectAliasRefSite[idx] && st.selectAliases[low] {
		return true
	}
	return len(st.referencedCTEs) > 0 && st.cteCols[low]
}
