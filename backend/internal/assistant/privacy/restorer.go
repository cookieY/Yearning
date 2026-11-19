// restorer.go implements dialect lexical/syntactic boundary validation, authorized object checks,
// parameter binding completeness verification, and token-stream structured restoration for
// model-generated anonymized SQL (strictly prohibiting global string replacement).
package privacy

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// RestorationResult is the structured output produced when ValidateAndRestoreSQL succeeds.
type RestorationResult struct {
	RestoredSQL      string
	StatementKind    domain.StatementKind
	RiskCategory     domain.DraftRiskCategory
	ReferencedTables []string
	SemanticWarnings []string
}

var (
	anonObjectTokenRe = regexp.MustCompile(`^(?:database|schema|table|column|index|constraint|alias|cte|ident)_[0-9]+$`)
	valPlaceholderRe  = regexp.MustCompile(`^val_(?:str|int|num|bool|ts|uuid|json|hex)_[0-9]+$`)
)

// ValidateAndRestoreSQL parses the model-returned anonymized SQL according to the target dialect,
// verifies single-statement boundaries, allowed v1 statement kinds, authorized object token mappings,
// and parameter placeholder bindings, and restores it into executable SQL via the token stream.
func ValidateAndRestoreSQL(
	session *TurnPrivacySession,
	anonymizedSQL string,
	profile fingerprint.Profile,
) (*RestorationResult, error) {
	if session == nil || session.IsReleased() {
		return nil, domain.ErrSQLRestorationInvalid("privacy_session_unavailable")
	}
	if err := profile.Validate(); err != nil {
		return nil, domain.ErrSQLRestorationInvalid("unsupported_dialect_profile")
	}
	trimmed := strings.TrimSpace(anonymizedSQL)
	if trimmed == "" {
		return nil, domain.ErrSQLRestorationInvalid("empty_sql_draft")
	}

	lex, err := fingerprint.Tokenize([]byte(trimmed), profile.Flags())
	if err != nil || len(lex.Tokens) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("sql_parse_failed")
	}

	// 1. Single-statement boundary check: forbid multi-statement stacking (';' is only allowed at the end).
	toks := lex.Tokens
	for i, t := range toks {
		if t.IsPunct(";") && i != len(toks)-1 {
			return nil, domain.ErrSQLRestorationInvalid("multi_statement_forbidden")
		}
	}
	if toks[len(toks)-1].IsPunct(";") {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("empty_sql_draft")
	}

	// 2. Statement classification and allowed v1 statement scope check (PRD §5.1 A2).
	stmtKind, riskCat, err := classifyAllowedStatement(toks)
	if err != nil {
		return nil, err
	}

	// 3. Scan and structurally restore the token stream (recognizing :val_<type>_<n> combinations and object tokens).
	var (
		outTokens        []string
		referencedTables = make(map[string]struct{})
		hasWhereClause   bool
		hasSelectStar    bool
	)

	for i := 0; i < len(toks); i++ {
		t := toks[i]

		// Check for forbidden out-of-scope clauses such as INTO OUTFILE / INTO DUMPFILE.
		if t.IsWord("where") {
			hasWhereClause = true
		}
		if t.IsPunct("*") && i > 0 && toks[i-1].IsWord("select") {
			hasSelectStar = true
		}
		if t.IsWord("into") && i+1 < len(toks) && (toks[i+1].IsWord("outfile") || toks[i+1].IsWord("dumpfile")) {
			return nil, domain.ErrSQLRestorationInvalid("into_outfile_forbidden")
		}

		// Handle :val_<kind>_<n> placeholders (the lexer may split into ":" + "val_str_1" or TokParam ":val_str_1").
		if t.IsPunct(":") {
			if i+1 >= len(toks) || toks[i+1].Kind != fingerprint.TokWord {
				return nil, domain.ErrSQLRestorationInvalid("malformed_parameter_placeholder")
			}
			phName := strings.ToLower(toks[i+1].Text)
			if !valPlaceholderRe.MatchString(phName) {
				return nil, domain.ErrSQLRestorationInvalid("unknown_parameter_placeholder:" + phName)
			}
			fullPh := ":" + phName
			bound, ok := session.LookupValue(fullPh)
			if !ok {
				return nil, domain.ErrSQLRestorationInvalid("unbound_parameter_placeholder:" + fullPh)
			}
			lit, err := formatSafeLiteral(bound, profile)
			if err != nil {
				return nil, err
			}
			outTokens = append(outTokens, lit)
			i++ // Skip the placeholder name token.
			continue
		}

		switch t.Kind {
		case fingerprint.TokParam:
			ph := strings.ToLower(t.Text)
			if !strings.HasPrefix(ph, ":val_") {
				return nil, domain.ErrSQLRestorationInvalid("unbound_positional_parameter:" + ph)
			}
			bound, ok := session.LookupValue(ph)
			if !ok {
				return nil, domain.ErrSQLRestorationInvalid("unbound_parameter_placeholder:" + ph)
			}
			lit, err := formatSafeLiteral(bound, profile)
			if err != nil {
				return nil, err
			}
			outTokens = append(outTokens, lit)

		case fingerprint.TokString, fingerprint.TokDollarString:
			// The model must not emit unbound string literals unless the literal itself is a :val_<type>_<n> placeholder.
			trimmedLit := strings.TrimSpace(t.Text)
			if strings.HasPrefix(strings.ToLower(trimmedLit), ":val_") {
				bound, ok := session.LookupValue(strings.ToLower(trimmedLit))
				if !ok {
					return nil, domain.ErrSQLRestorationInvalid("unbound_parameter_placeholder:" + trimmedLit)
				}
				lit, err := formatSafeLiteral(bound, profile)
				if err != nil {
					return nil, err
				}
				outTokens = append(outTokens, lit)
				continue
			}
			return nil, domain.ErrSQLRestorationInvalid("unparameterized_string_literal_forbidden")

		case fingerprint.TokNumber:
			// Allow small structural integers (e.g., LIMIT 1, COUNT(1), VARCHAR(64), DECIMAL(10,2), 0, 1).
			if isAllowedStructuralNumber(t.Text, toks, i) {
				outTokens = append(outTokens, t.Text)
				continue
			}
			return nil, domain.ErrSQLRestorationInvalid("unparameterized_number_literal_forbidden")

		case fingerprint.TokQuotedIdent:
			low := strings.ToLower(t.Text)
			if anonObjectTokenRe.MatchString(low) {
				kind, orig, ok := session.LookupIdentifier(low)
				if !ok {
					return nil, domain.ErrSQLRestorationInvalid("unknown_or_unauthorized_object_token:" + low)
				}
				if kind == revprivacy.KindTable {
					referencedTables[orig] = struct{}{}
				}
				outTokens = append(outTokens, quoteIdentifierForProfile(orig, profile))
				continue
			}
			return nil, domain.ErrSQLRestorationInvalid("unmapped_quoted_identifier_forbidden")

		case fingerprint.TokWord:
			low := strings.ToLower(t.Text)
			if anonObjectTokenRe.MatchString(low) {
				kind, orig, ok := session.LookupIdentifier(low)
				if !ok {
					return nil, domain.ErrSQLRestorationInvalid("unknown_or_unauthorized_object_token:" + low)
				}
				if kind == revprivacy.KindTable {
					referencedTables[orig] = struct{}{}
				}
				outTokens = append(outTokens, quoteIdentifierForProfile(orig, profile))
				continue
			}
			if valPlaceholderRe.MatchString(low) {
				bound, ok := session.LookupValue(":" + low)
				if !ok {
					return nil, domain.ErrSQLRestorationInvalid("unbound_parameter_placeholder::" + low)
				}
				lit, err := formatSafeLiteral(bound, profile)
				if err != nil {
					return nil, err
				}
				outTokens = append(outTokens, lit)
				continue
			}
			if isSQLKeywordForAnon(low, toks, i) {
				outTokens = append(outTokens, strings.ToUpper(low))
				continue
			}
			return nil, domain.ErrSQLRestorationInvalid("unmapped_identifier_forbidden:" + low)

		case fingerprint.TokPunct:
			if t.Text == "?" {
				return nil, domain.ErrSQLRestorationInvalid("unbound_question_mark_parameter")
			}
			outTokens = append(outTokens, t.Text)

		default:
			return nil, domain.ErrSQLRestorationInvalid("unsupported_token_kind")
		}
	}

	restoredSQL := formatTokenStreamSQL(outTokens)

	// 4. Secondary lexical and statement boundary re-verification: ensure the restored SQL remains a single statement of the same kind without syntax escape.
	reLex, err := fingerprint.Tokenize([]byte(restoredSQL), profile.Flags())
	if err != nil || len(reLex.Tokens) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("restored_sql_reparse_failed")
	}
	for i, rt := range reLex.Tokens {
		if rt.IsPunct(";") && i != len(reLex.Tokens)-1 {
			return nil, domain.ErrSQLRestorationInvalid("restored_sql_multi_statement_escape")
		}
	}
	reKind, _, err := classifyAllowedStatement(reLex.Tokens)
	if err != nil || reKind != stmtKind {
		return nil, domain.ErrSQLRestorationInvalid("restored_sql_statement_kind_drift")
	}

	// 5. At least one authorized table must be referenced.
	if len(referencedTables) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("no_authorized_table_referenced")
	}

	tables := make([]string, 0, len(referencedTables))
	for tbl := range referencedTables {
		tables = append(tables, tbl)
	}
	sort.Strings(tables)

	warnings := collectSemanticWarnings(stmtKind, hasWhereClause, hasSelectStar)

	return &RestorationResult{
		RestoredSQL:      restoredSQL,
		StatementKind:    stmtKind,
		RiskCategory:     riskCat,
		ReferencedTables: tables,
		SemanticWarnings: warnings,
	}, nil
}

// RawSQLScope defines the server-authorized target schema, preloaded table/column whitelist,
// and optional catalog column loader enforced by ValidateRawAssistantSQL.
type RawSQLScope struct {
	AuthorizedSchema      string
	AllowedColumnsByTable map[string]map[string]map[string]struct{}
	LookupTableColumns    func(schema, table string) ([]string, bool)
}

type rawScopeResolver struct {
	authSchema string
	preloaded  map[string]map[string]struct{}
	cache      map[string]map[string]struct{}
	missing    map[string]bool
	lookup     func(schema, table string) ([]string, bool)
}

func newRawScopeResolver(scope RawSQLScope) (*rawScopeResolver, error) {
	authSchema := strings.ToLower(strings.TrimSpace(scope.AuthorizedSchema))
	if authSchema == "" {
		return nil, domain.ErrSQLRestorationInvalid("unauthorized_scope_unavailable")
	}
	preloaded := make(map[string]map[string]struct{})
	if scopeCols, ok := scope.AllowedColumnsByTable[authSchema]; ok {
		for tbl, cols := range scopeCols {
			tblKey := strings.ToLower(strings.TrimSpace(tbl))
			if tblKey == "" {
				continue
			}
			cset := make(map[string]struct{}, len(cols))
			for c := range cols {
				if cLow := strings.ToLower(strings.TrimSpace(c)); cLow != "" {
					cset[cLow] = struct{}{}
				}
			}
			preloaded[tblKey] = cset
		}
	} else {
		// Accept case-insensitive keys in AllowedColumnsByTable if caller passed mixed-case schema names.
		for sch, tblMap := range scope.AllowedColumnsByTable {
			if strings.ToLower(strings.TrimSpace(sch)) != authSchema {
				continue
			}
			for tbl, cols := range tblMap {
				tblKey := strings.ToLower(strings.TrimSpace(tbl))
				if tblKey == "" {
					continue
				}
				cset := make(map[string]struct{}, len(cols))
				for c := range cols {
					if cLow := strings.ToLower(strings.TrimSpace(c)); cLow != "" {
						cset[cLow] = struct{}{}
					}
				}
				preloaded[tblKey] = cset
			}
		}
	}
	return &rawScopeResolver{
		authSchema: authSchema,
		preloaded:  preloaded,
		cache:      make(map[string]map[string]struct{}, len(preloaded)),
		missing:    make(map[string]bool),
		lookup:     scope.LookupTableColumns,
	}, nil
}

func (r *rawScopeResolver) resolveTable(schemaPart, tablePart string) (map[string]struct{}, error) {
	if schemaPart != "" && strings.ToLower(strings.TrimSpace(schemaPart)) != r.authSchema {
		return nil, domain.ErrSQLRestorationInvalid("unauthorized_schema:" + strings.ToLower(strings.TrimSpace(schemaPart)))
	}
	tblLow := strings.ToLower(strings.TrimSpace(tablePart))
	if tblLow == "" {
		return nil, domain.ErrSQLRestorationInvalid("empty_table_identifier")
	}
	if cols, ok := r.cache[tblLow]; ok {
		return cols, nil
	}
	if cols, ok := r.preloaded[tblLow]; ok {
		r.cache[tblLow] = cols
		return cols, nil
	}
	if r.missing[tblLow] {
		return nil, domain.ErrSQLRestorationInvalid("unauthorized_table:" + formatQualifiedTable(schemaPart, tblLow))
	}
	if r.lookup != nil {
		cols, ok := r.lookup(r.authSchema, tblLow)
		if ok {
			cset := make(map[string]struct{}, len(cols))
			for _, c := range cols {
				if cLow := strings.ToLower(strings.TrimSpace(c)); cLow != "" {
					cset[cLow] = struct{}{}
				}
			}
			r.cache[tblLow] = cset
			return cset, nil
		}
	}
	r.missing[tblLow] = true
	return nil, domain.ErrSQLRestorationInvalid("unauthorized_table:" + formatQualifiedTable(schemaPart, tblLow))
}

func formatQualifiedTable(schemaPart, tablePart string) string {
	if schemaPart != "" {
		return strings.ToLower(strings.TrimSpace(schemaPart)) + "." + strings.ToLower(strings.TrimSpace(tablePart))
	}
	return strings.ToLower(strings.TrimSpace(tablePart))
}

// ValidateRawAssistantSQL enforces dialect parse, single-statement boundary, allowed statement kind,
// comment/escape, semantic warning, and fail-closed authorized schema/table/column scope validation
// for SAN ("san") model drafts that already contain raw identifiers and literals.
func ValidateRawAssistantSQL(rawSQL string, profile fingerprint.Profile, scope RawSQLScope) (*RestorationResult, error) {
	resolver, err := newRawScopeResolver(scope)
	if err != nil {
		return nil, err
	}
	toks, stmtKind, riskCat, err := parseSingleAllowedStatement(rawSQL, profile)
	if err != nil {
		return nil, err
	}

	var (
		referencedTables = make(map[string]struct{})
		hasWhereClause   bool
		hasSelectStar    bool
	)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.IsWord("where") {
			hasWhereClause = true
		}
		if t.IsPunct("*") && i > 0 && toks[i-1].IsWord("select") {
			hasSelectStar = true
		}
		if t.IsWord("into") && i+1 < len(toks) && (toks[i+1].IsWord("outfile") || toks[i+1].IsWord("dumpfile")) {
			return nil, domain.ErrSQLRestorationInvalid("into_outfile_forbidden")
		}
		if t.IsPunct(":") && i+1 < len(toks) && toks[i+1].Kind == fingerprint.TokWord {
			if valPlaceholderRe.MatchString(strings.ToLower(toks[i+1].Text)) {
				return nil, domain.ErrSQLRestorationInvalid("unbound_parameter_placeholder:" + strings.ToLower(toks[i+1].Text))
			}
		}
		switch t.Kind {
		case fingerprint.TokParam:
			return nil, domain.ErrSQLRestorationInvalid("unbound_positional_parameter:" + strings.ToLower(t.Text))
		case fingerprint.TokPunct:
			if t.Text == "?" {
				return nil, domain.ErrSQLRestorationInvalid("unbound_question_mark_parameter")
			}
		case fingerprint.TokWord:
			low := strings.ToLower(t.Text)
			if anonObjectTokenRe.MatchString(low) {
				return nil, domain.ErrSQLRestorationInvalid("unreplaced_anonymized_token:" + low)
			}
		case fingerprint.TokQuotedIdent, fingerprint.TokString, fingerprint.TokDollarString, fingerprint.TokNumber:
			// Allowed in Raw-SQL Native SAN drafts.
		default:
			return nil, domain.ErrSQLRestorationInvalid("unsupported_token_kind")
		}
	}

	if err := validateRawAuthorizedScope(toks, stmtKind, resolver, referencedTables); err != nil {
		return nil, err
	}
	if len(referencedTables) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("no_authorized_table_referenced")
	}

	// Re-verify single-statement invariance after stripping any trailing delimiter.
	trimmed := strings.TrimSpace(rawSQL)
	reLex, rerr := fingerprint.Tokenize([]byte(trimmed), profile.Flags())
	if rerr != nil || len(reLex.Tokens) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("restored_sql_reparse_failed")
	}
	reToks := reLex.Tokens
	for i, rt := range reToks {
		if rt.IsPunct(";") && i != len(reToks)-1 {
			return nil, domain.ErrSQLRestorationInvalid("restored_sql_multi_statement_escape")
		}
	}
	if reToks[len(reToks)-1].IsPunct(";") {
		reToks = reToks[:len(reToks)-1]
	}
	reKind, _, cerr := classifyAllowedStatement(reToks)
	if cerr != nil || reKind != stmtKind {
		return nil, domain.ErrSQLRestorationInvalid("restored_sql_statement_kind_drift")
	}

	tables := make([]string, 0, len(referencedTables))
	for tbl := range referencedTables {
		tables = append(tables, tbl)
	}
	sort.Strings(tables)

	return &RestorationResult{
		RestoredSQL:      trimmed,
		StatementKind:    stmtKind,
		RiskCategory:     riskCat,
		ReferencedTables: tables,
		SemanticWarnings: collectSemanticWarnings(stmtKind, hasWhereClause, hasSelectStar),
	}, nil
}

type rawScopeAnalysis struct {
	consumed         map[int]bool
	activeCols       map[string]struct{}
	tableAliases     map[string]map[string]struct{}
	cteNames         map[string]bool
	cteAliases       map[string]bool
	cteCols          map[string]bool
	selectAliases    map[string]bool
	indexNames       map[string]bool
	constraintNames  map[string]bool
	ddlNewTables     map[string]bool
	ddlNewCols       map[string]bool
	insertTargetCols map[string]struct{}
	hasOnConflictUpd bool
}

func validateRawAuthorizedScope(
	toks []fingerprint.Token,
	stmtKind domain.StatementKind,
	resolver *rawScopeResolver,
	referencedTables map[string]struct{},
) error {
	st := &rawScopeAnalysis{
		consumed:        make(map[int]bool),
		activeCols:      make(map[string]struct{}),
		tableAliases:    make(map[string]map[string]struct{}),
		cteNames:        make(map[string]bool),
		cteAliases:      make(map[string]bool),
		cteCols:         make(map[string]bool),
		selectAliases:   make(map[string]bool),
		indexNames:      make(map[string]bool),
		constraintNames: make(map[string]bool),
		ddlNewTables:    make(map[string]bool),
		ddlNewCols:      make(map[string]bool),
	}

	// Pass 1a: Collect CTE headers (WITH [RECURSIVE] cte [(c1, c2)] AS (...)) before scanning table refs.
	if stmtKind == domain.StmtSelect && len(toks) > 0 && toks[0].IsWord("with") {
		collectRawCTEDeclarations(toks, st)
	}

	// Pass 1b: Collect index/constraint/select aliases, resolve table references, and track table aliases.
	if err := collectRawTablesAndDeclarations(toks, stmtKind, resolver, referencedTables, st); err != nil {
		return err
	}

	// Pass 2: Validate every qualified and unqualified non-table identifier against resolved scope.
	return verifyRawIdentifiers(toks, resolver.authSchema, st)
}

func collectRawCTEDeclarations(toks []fingerprint.Token, st *rawScopeAnalysis) {
	depth := 0
	expectCTEName := true
	inCTEDefList := false
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if t.IsPunct("(") {
			if depth == 0 && !expectCTEName && i > 0 && toks[i-1].IsWord("as") {
				depth++
				continue
			}
			if depth == 0 && inCTEDefList {
				depth++
				continue
			}
			depth++
			continue
		}
		if t.IsPunct(")") {
			if depth > 0 {
				depth--
			}
			if depth == 0 {
				if inCTEDefList {
					inCTEDefList = false
				} else {
					// Next comma starts another CTE; SELECT starts the main query.
					expectCTEName = false
				}
			}
			continue
		}
		if depth == 1 && inCTEDefList {
			if ident, ok := rawIdentName(t); ok && !(t.Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident))) {
				st.cteCols[strings.ToLower(ident)] = true
				st.consumed[i] = true
			}
			continue
		}
		if depth != 0 {
			continue
		}
		if t.IsWord("recursive") && expectCTEName {
			continue
		}
		if t.IsWord("select") && !expectCTEName {
			return
		}
		if t.IsPunct(",") {
			expectCTEName = true
			inCTEDefList = false
			continue
		}
		if expectCTEName {
			if ident, ok := rawIdentName(t); ok && !(t.Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident))) {
				st.cteNames[strings.ToLower(ident)] = true
				st.consumed[i] = true
				expectCTEName = false
				if i+1 < len(toks) && toks[i+1].IsPunct("(") {
					inCTEDefList = true
				}
			}
		}
	}
}

func collectRawTablesAndDeclarations(
	toks []fingerprint.Token,
	stmtKind domain.StatementKind,
	resolver *rawScopeResolver,
	referencedTables map[string]struct{},
	st *rawScopeAnalysis,
) error {
	createTargetPending := stmtKind == domain.StmtCreateTable
	depth := 0
	fromClauseAtDepth := make(map[int]bool)
	funcArgDepth := make(map[int]bool)

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.IsPunct("(") {
			depth++
			if i > 0 && toks[i-1].Kind == fingerprint.TokWord && isFunctionParenFrom(strings.ToLower(toks[i-1].Text)) {
				funcArgDepth[depth] = true
			}
			continue
		}
		if t.IsPunct(")") {
			delete(fromClauseAtDepth, depth)
			delete(funcArgDepth, depth)
			if depth > 0 {
				depth--
			}
			continue
		}

		if t.Kind == fingerprint.TokWord {
			low := strings.ToLower(t.Text)
			switch low {
			case "from":
				if !funcArgDepth[depth] {
					fromClauseAtDepth[depth] = true
				}
			case "where", "group", "having", "order", "limit", "offset", "fetch", "union", "intersect", "except", "set", "values", "returning", "select", "window", "on", "using":
				fromClauseAtDepth[depth] = false
			case "conflict":
				if i > 0 && toks[i-1].IsWord("on") && stmtKind == domain.StmtInsert {
					st.hasOnConflictUpd = true
				}
			}
		}

		if st.consumed[i] {
			continue
		}

		if prevIsIndexContext(toks, i) {
			if ident, ok := rawIdentName(t); ok && !(t.Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident))) {
				st.indexNames[strings.ToLower(ident)] = true
				st.consumed[i] = true
				continue
			}
		}
		if i > 0 && toks[i-1].IsWord("constraint") {
			if ident, ok := rawIdentName(t); ok {
				st.constraintNames[strings.ToLower(ident)] = true
				st.consumed[i] = true
				continue
			}
		}

		tableCtx := isRawTablePosition(toks, i, stmtKind, fromClauseAtDepth[depth], funcArgDepth[depth])
		if tableCtx {
			if t.Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(t.Text)) {
				continue
			}
			nextIdx, handled, err := consumeAndAuthorizeTableRef(toks, i, stmtKind, &createTargetPending, resolver, referencedTables, st)
			if err != nil {
				return err
			}
			if handled {
				i = nextIdx - 1
				continue
			}
		}

		// Record ALTER TABLE new column declarations: ADD [COLUMN] [IF NOT EXISTS] <new_col> / RENAME [COLUMN] <old> TO <new>
		if stmtKind == domain.StmtAlterTable && depth == 0 {
			if colIdx, colName, ok := detectAlterNewColumnDecl(toks, i); ok {
				st.ddlNewCols[strings.ToLower(colName)] = true
				st.consumed[colIdx] = true
			}
		}

		// Record SELECT / expression aliases (<expr> AS <alias>). Table aliases are handled by consumeAndAuthorizeTableRef.
		if prevIsAliasContext(toks, i) {
			if ident, ok := rawIdentName(t); ok && !(t.Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident))) {
				if !(stmtKind == domain.StmtCreateTable && t.IsWord("select")) {
					st.selectAliases[strings.ToLower(ident)] = true
					st.consumed[i] = true
				}
			}
		}
	}

	if stmtKind == domain.StmtCreateTable {
		collectCreateTableNewColumns(toks, st)
	}
	if st.hasOnConflictUpd && st.insertTargetCols != nil {
		st.tableAliases["excluded"] = st.insertTargetCols
	}
	return nil
}

func isFunctionParenFrom(word string) bool {
	switch word {
	case "extract", "trim", "substring", "substr", "overlay", "position":
		return true
	}
	return false
}

func isRawTablePosition(toks []fingerprint.Token, idx int, stmtKind domain.StatementKind, inFromClause, inFuncArg bool) bool {
	if idx <= 0 {
		return false
	}
	if prevIsTableContext(toks, idx) {
		// Inside EXTRACT(YEAR FROM created_at) / TRIM(... FROM col), "from" is a function argument, not a table clause.
		if inFuncArg && toks[idx-1].IsWord("from") {
			return false
		}
		return true
	}
	if (stmtKind == domain.StmtCreateIndex || stmtKind == domain.StmtDropIndex) && toks[idx-1].IsWord("on") {
		return true
	}
	if toks[idx-1].IsWord("references") {
		return true
	}
	if inFromClause && toks[idx-1].IsPunct(",") {
		return true
	}
	return false
}

func consumeAndAuthorizeTableRef(
	toks []fingerprint.Token,
	idx int,
	stmtKind domain.StatementKind,
	createTargetPending *bool,
	resolver *rawScopeResolver,
	referencedTables map[string]struct{},
	st *rawScopeAnalysis,
) (int, bool, error) {
	first, ok := rawIdentName(toks[idx])
	if !ok {
		return idx, false, nil
	}

	var schemaPart, tablePart, displayName string
	endIdx := idx + 1
	if idx+2 < len(toks) && toks[idx+1].IsPunct(".") {
		second, ok2 := rawIdentName(toks[idx+2])
		if !ok2 {
			return idx, false, domain.ErrSQLRestorationInvalid("malformed_table_reference")
		}
		schemaPart = first
		tablePart = second
		displayName = first + "." + second
		st.consumed[idx] = true
		st.consumed[idx+1] = true
		st.consumed[idx+2] = true
		endIdx = idx + 3
	} else {
		tablePart = first
		displayName = first
		st.consumed[idx] = true
	}

	tableLow := strings.ToLower(strings.TrimSpace(tablePart))
	if schemaPart == "" && st.cteNames[tableLow] {
		endIdx = consumeOptionalAlias(toks, endIdx, func(aliasLow string) {
			st.cteAliases[aliasLow] = true
		}, st)
		return endIdx, true, nil
	}

	if stmtKind == domain.StmtCreateTable && *createTargetPending {
		*createTargetPending = false
		if schemaPart != "" && strings.ToLower(strings.TrimSpace(schemaPart)) != resolver.authSchema {
			return idx, false, domain.ErrSQLRestorationInvalid("unauthorized_schema:" + strings.ToLower(strings.TrimSpace(schemaPart)))
		}
		referencedTables[displayName] = struct{}{}
		st.ddlNewTables[tableLow] = true
		endIdx = consumeOptionalAlias(toks, endIdx, func(aliasLow string) {
			st.ddlNewTables[aliasLow] = true
		}, st)
		return endIdx, true, nil
	}

	cols, err := resolver.resolveTable(schemaPart, tablePart)
	if err != nil {
		return idx, false, err
	}
	referencedTables[displayName] = struct{}{}
	for c := range cols {
		st.activeCols[c] = struct{}{}
	}
	st.tableAliases[tableLow] = cols
	if stmtKind == domain.StmtInsert && idx > 0 && toks[idx-1].IsWord("into") {
		st.insertTargetCols = cols
	}
	endIdx = consumeOptionalAlias(toks, endIdx, func(aliasLow string) {
		st.tableAliases[aliasLow] = cols
	}, st)
	return endIdx, true, nil
}

func consumeOptionalAlias(toks []fingerprint.Token, pos int, onAlias func(string), st *rawScopeAnalysis) int {
	if pos >= len(toks) {
		return pos
	}
	if toks[pos].IsWord("as") && pos+1 < len(toks) {
		if alias, ok := rawIdentName(toks[pos+1]); ok && !isAliasTerminator(toks[pos+1], alias) {
			st.consumed[pos] = true
			st.consumed[pos+1] = true
			onAlias(strings.ToLower(alias))
			return pos + 2
		}
		return pos
	}
	if alias, ok := rawIdentName(toks[pos]); ok && !isAliasTerminator(toks[pos], alias) {
		st.consumed[pos] = true
		onAlias(strings.ToLower(alias))
		return pos + 1
	}
	return pos
}

func isAliasTerminator(tok fingerprint.Token, ident string) bool {
	if tok.Kind != fingerprint.TokWord {
		return false
	}
	return isRawSQLKeyword(strings.ToLower(ident))
}

func detectAlterNewColumnDecl(toks []fingerprint.Token, idx int) (int, string, bool) {
	if !toks[idx].IsWord("add") && !toks[idx].IsWord("to") {
		return 0, "", false
	}
	if toks[idx].IsWord("to") {
		// RENAME COLUMN old_col TO new_col
		if idx >= 3 && toks[idx-2].IsWord("column") && idx+1 < len(toks) {
			if ident, ok := rawIdentName(toks[idx+1]); ok && !(toks[idx+1].Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident))) {
				return idx + 1, ident, true
			}
		}
		return 0, "", false
	}
	next := idx + 1
	if next < len(toks) && toks[next].IsWord("column") {
		next++
	}
	if next+2 < len(toks) && toks[next].IsWord("if") && toks[next+1].IsWord("not") && toks[next+2].IsWord("exists") {
		next += 3
	}
	if next >= len(toks) {
		return 0, "", false
	}
	ident, ok := rawIdentName(toks[next])
	if !ok {
		return 0, "", false
	}
	if toks[next].Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident)) {
		return 0, "", false
	}
	return next, ident, true
}

func collectCreateTableNewColumns(toks []fingerprint.Token, st *rawScopeAnalysis) {
	inCols := false
	expectCol := false
	depth := 0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if !inCols {
			if t.IsWord("as") || t.IsWord("select") {
				return
			}
			if t.IsPunct("(") {
				inCols = true
				expectCol = true
				depth = 1
			}
			continue
		}
		if t.IsPunct("(") {
			depth++
			continue
		}
		if t.IsPunct(")") {
			depth--
			if depth <= 0 {
				return
			}
			continue
		}
		if depth == 1 && t.IsPunct(",") {
			expectCol = true
			continue
		}
		if depth == 1 && expectCol {
			expectCol = false
			if ident, ok := rawIdentName(t); ok {
				low := strings.ToLower(ident)
				if !(t.Kind == fingerprint.TokWord && isRawSQLKeyword(low)) {
					st.ddlNewCols[low] = true
					st.consumed[i] = true
				}
			}
		}
	}
}

func verifyRawIdentifiers(toks []fingerprint.Token, authSchema string, st *rawScopeAnalysis) error {
	for i := 0; i < len(toks); i++ {
		if st.consumed[i] {
			continue
		}
		t := toks[i]
		if t.Kind != fingerprint.TokWord && t.Kind != fingerprint.TokQuotedIdent {
			continue
		}

		// Handle 3-part dotted chain (schema.table.column) and 2-part dotted chain (qualifier.column or qualifier.*).
		if i+2 < len(toks) && toks[i+1].IsPunct(".") {
			if i+4 < len(toks) && toks[i+3].IsPunct(".") && !st.consumed[i+2] && !st.consumed[i+4] {
				sch, ok1 := rawIdentName(t)
				tbl, ok2 := rawIdentName(toks[i+2])
				if !ok1 || !ok2 {
					return domain.ErrSQLRestorationInvalid("malformed_qualified_identifier")
				}
				schLow := strings.ToLower(sch)
				if schLow != authSchema {
					return domain.ErrSQLRestorationInvalid("unauthorized_schema:" + schLow)
				}
				tblLow := strings.ToLower(tbl)
				cols, ok := st.tableAliases[tblLow]
				if !ok {
					return domain.ErrSQLRestorationInvalid("unauthorized_qualifier:" + schLow + "." + tblLow)
				}
				if !toks[i+4].IsPunct("*") {
					col, ok3 := rawIdentName(toks[i+4])
					if !ok3 {
						return domain.ErrSQLRestorationInvalid("malformed_column_identifier")
					}
					if _, hasCol := cols[strings.ToLower(col)]; !hasCol {
						return domain.ErrSQLRestorationInvalid("unauthorized_column:" + schLow + "." + tblLow + "." + strings.ToLower(col))
					}
				}
				i += 4
				continue
			}

			qual, ok1 := rawIdentName(t)
			if !ok1 {
				return domain.ErrSQLRestorationInvalid("malformed_qualified_identifier")
			}
			qualLow := strings.ToLower(qual)
			if st.cteNames[qualLow] || st.cteAliases[qualLow] {
				if !toks[i+2].IsPunct("*") {
					col, ok2 := rawIdentName(toks[i+2])
					if !ok2 {
						return domain.ErrSQLRestorationInvalid("malformed_column_identifier")
					}
					colLow := strings.ToLower(col)
					_, inActive := st.activeCols[colLow]
					if !inActive && !st.selectAliases[colLow] && !st.cteCols[colLow] {
						return domain.ErrSQLRestorationInvalid("unauthorized_column:" + qualLow + "." + colLow)
					}
				}
				i += 2
				continue
			}
			if st.ddlNewTables[qualLow] {
				if !toks[i+2].IsPunct("*") {
					col, ok2 := rawIdentName(toks[i+2])
					if !ok2 || !st.ddlNewCols[strings.ToLower(col)] {
						return domain.ErrSQLRestorationInvalid("unauthorized_column:" + qualLow + "." + strings.ToLower(col))
					}
				}
				i += 2
				continue
			}
			cols, ok := st.tableAliases[qualLow]
			if !ok {
				return domain.ErrSQLRestorationInvalid("unauthorized_qualifier:" + qualLow)
			}
			if !toks[i+2].IsPunct("*") {
				col, ok2 := rawIdentName(toks[i+2])
				if !ok2 {
					return domain.ErrSQLRestorationInvalid("malformed_column_identifier")
				}
				if _, hasCol := cols[strings.ToLower(col)]; !hasCol {
					return domain.ErrSQLRestorationInvalid("unauthorized_column:" + qualLow + "." + strings.ToLower(col))
				}
			}
			i += 2
			continue
		}

		ident, ok := rawIdentName(t)
		if !ok {
			return domain.ErrSQLRestorationInvalid("malformed_identifier")
		}
		low := strings.ToLower(ident)
		if _, hasCol := st.activeCols[low]; hasCol {
			continue
		}
		if st.selectAliases[low] || st.cteCols[low] || st.ddlNewCols[low] || st.indexNames[low] || st.constraintNames[low] {
			continue
		}
		if t.Kind == fingerprint.TokWord && isRawSQLKeyword(low) {
			continue
		}
		return domain.ErrSQLRestorationInvalid("unauthorized_or_unknown_identifier:" + low)
	}
	return nil
}

func isRawSQLKeyword(low string) bool {
	switch low {
	case "mysql", "postgresql", "mariadb", "tidb", "dialect", "template", "metadata":
		return false
	case "round", "abs", "floor", "ceil", "ceiling", "ifnull", "to_char", "to_date", "to_timestamp",
		"date_add", "date_sub", "datediff", "epoch", "timezone", "after", "before", "cascade", "restrict",
		"comment", "check", "column", "if":
		return true
	}
	return sqlReservedAndBuiltins[low]
}

func parseSingleAllowedStatement(rawSQL string, profile fingerprint.Profile) ([]fingerprint.Token, domain.StatementKind, domain.DraftRiskCategory, error) {
	if err := profile.Validate(); err != nil {
		return nil, "", "", domain.ErrSQLRestorationInvalid("unsupported_dialect_profile")
	}
	trimmed := strings.TrimSpace(rawSQL)
	if trimmed == "" {
		return nil, "", "", domain.ErrSQLRestorationInvalid("empty_sql_draft")
	}
	lex, err := fingerprint.Tokenize([]byte(trimmed), profile.Flags())
	if err != nil || len(lex.Tokens) == 0 {
		return nil, "", "", domain.ErrSQLRestorationInvalid("sql_parse_failed")
	}
	toks := lex.Tokens
	for i, t := range toks {
		if t.IsPunct(";") && i != len(toks)-1 {
			return nil, "", "", domain.ErrSQLRestorationInvalid("multi_statement_forbidden")
		}
	}
	if len(toks) > 0 && toks[len(toks)-1].IsPunct(";") {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return nil, "", "", domain.ErrSQLRestorationInvalid("empty_sql_draft")
	}
	stmtKind, riskCat, err := classifyAllowedStatement(toks)
	if err != nil {
		return nil, "", "", err
	}
	return toks, stmtKind, riskCat, nil
}

func extractRawTableReference(toks []fingerprint.Token, idx int) (string, bool) {
	first, ok := rawIdentName(toks[idx])
	if !ok {
		return "", false
	}
	if toks[idx].Kind == fingerprint.TokWord && isSQLKeywordOrGeneric(first) {
		return "", false
	}
	if idx+2 < len(toks) && toks[idx+1].IsPunct(".") {
		if second, ok2 := rawIdentName(toks[idx+2]); ok2 {
			return first + "." + second, true
		}
	}
	return first, true
}

func rawIdentName(tok fingerprint.Token) (string, bool) {
	if tok.Kind == fingerprint.TokWord {
		return tok.Text, tok.Text != ""
	}
	if tok.Kind == fingerprint.TokQuotedIdent {
		s := strings.TrimSpace(tok.Text)
		if len(s) >= 2 {
			if (s[0] == '`' && s[len(s)-1] == '`') || (s[0] == '"' && s[len(s)-1] == '"') {
				s = s[1 : len(s)-1]
			}
		}
		return s, s != ""
	}
	return "", false
}

func collectSemanticWarnings(stmtKind domain.StatementKind, hasWhereClause, hasSelectStar bool) []string {
	var warnings []string
	if (stmtKind == domain.StmtUpdate || stmtKind == domain.StmtDelete) && !hasWhereClause {
		warnings = append(warnings, "Statement has no WHERE filter and will affect the entire table; verify the target scope carefully")
	}
	if stmtKind == domain.StmtSelect && hasSelectStar {
		warnings = append(warnings, "Statement uses SELECT *; specify required columns explicitly to reduce network transfer and index lookup overhead")
	}
	return warnings
}

// classifyAllowedStatement checks whether the statement belongs to the 8 allowed v1 statement kinds and explicitly rejects high-risk statements.
func classifyAllowedStatement(toks []fingerprint.Token) (domain.StatementKind, domain.DraftRiskCategory, error) {
	if len(toks) == 0 {
		return "", "", domain.ErrSQLRestorationInvalid("empty_statement")
	}
	first := strings.ToLower(toks[0].Text)
	switch first {
	case "select":
		return domain.StmtSelect, domain.RiskReadOnly, nil
	case "with":
		// WITH CTE is only allowed when the final main statement is SELECT.
		hasSelect := false
		for _, t := range toks[1:] {
			if t.Kind != fingerprint.TokWord {
				continue
			}
			switch strings.ToLower(t.Text) {
			case "insert", "update", "delete", "drop", "truncate", "alter", "create", "grant", "revoke":
				return "", "", domain.ErrSQLRestorationInvalid("with_modifying_cte_forbidden")
			case "select":
				hasSelect = true
			}
		}
		if !hasSelect {
			return "", "", domain.ErrSQLRestorationInvalid("with_without_select")
		}
		return domain.StmtSelect, domain.RiskReadOnly, nil
	case "insert":
		return domain.StmtInsert, domain.RiskDML, nil
	case "update":
		return domain.StmtUpdate, domain.RiskDML, nil
	case "delete":
		return domain.StmtDelete, domain.RiskDML, nil
	case "create":
		if len(toks) < 2 {
			return "", "", domain.ErrSQLRestorationInvalid("incomplete_create_statement")
		}
		second := strings.ToLower(toks[1].Text)
		if second == "table" {
			return domain.StmtCreateTable, domain.RiskDDL, nil
		}
		if second == "index" || (second == "unique" && len(toks) >= 3 && strings.ToLower(toks[2].Text) == "index") ||
			(second == "concurrently" && len(toks) >= 3 && strings.ToLower(toks[2].Text) == "index") {
			return domain.StmtCreateIndex, domain.RiskDDL, nil
		}
		return "", "", domain.ErrSQLRestorationInvalid("unsupported_create_statement:" + second)
	case "alter":
		if len(toks) >= 2 && strings.ToLower(toks[1].Text) == "table" {
			// Destructive clauses such as DROP COLUMN / TRUNCATE PARTITION still classify as alter_table and are adjudicated by formal precheck.
			return domain.StmtAlterTable, domain.RiskDDL, nil
		}
		return "", "", domain.ErrSQLRestorationInvalid("unsupported_alter_statement")
	case "drop":
		if len(toks) >= 2 && strings.ToLower(toks[1].Text) == "index" {
			return domain.StmtDropIndex, domain.RiskDDL, nil
		}
		if len(toks) >= 2 {
			return "", "", domain.ErrSQLRestorationInvalid("forbidden_drop_statement:" + strings.ToLower(toks[1].Text))
		}
		return "", "", domain.ErrSQLRestorationInvalid("forbidden_drop_statement")
	case "truncate", "grant", "revoke", "call", "do", "execute", "prepare", "deallocate", "copy", "load", "set", "reset", "show", "explain", "analyze", "vacuum", "lock", "unlock":
		return "", "", domain.ErrSQLRestorationInvalid("forbidden_statement_kind:" + first)
	default:
		return "", "", domain.ErrSQLRestorationInvalid("unsupported_statement_kind:" + first)
	}
}

func quoteIdentifierForProfile(ident string, profile fingerprint.Profile) string {
	if profile.IsPostgres() {
		return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func formatSafeLiteral(bound BoundValue, profile fingerprint.Profile) (string, error) {
	if strings.ContainsRune(bound.RawValue, '\x00') {
		return "", domain.ErrSQLRestorationInvalid("null_byte_in_bound_value")
	}
	switch bound.Kind {
	case ValInt:
		if _, err := strconv.ParseInt(strings.TrimSpace(bound.RawValue), 10, 64); err != nil {
			return "", domain.ErrSQLRestorationInvalid("invalid_int_bound_value")
		}
		return strings.TrimSpace(bound.RawValue), nil
	case ValNum:
		if _, err := strconv.ParseFloat(strings.TrimSpace(bound.RawValue), 64); err != nil {
			return "", domain.ErrSQLRestorationInvalid("invalid_numeric_bound_value")
		}
		return strings.TrimSpace(bound.RawValue), nil
	case ValBool:
		low := strings.ToLower(strings.TrimSpace(bound.RawValue))
		if low != "true" && low != "false" {
			return "", domain.ErrSQLRestorationInvalid("invalid_bool_bound_value")
		}
		return strings.ToUpper(low), nil
	case ValString, ValTimestamp, ValUUID, ValJSON, ValHex:
		return quoteSQLStringLiteral(bound.RawValue, profile), nil
	default:
		return quoteSQLStringLiteral(bound.RawValue, profile), nil
	}
}

func quoteSQLStringLiteral(raw string, profile fingerprint.Profile) string {
	escaped := strings.ReplaceAll(raw, "'", "''")
	if !profile.IsPostgres() {
		// Default MySQL sessions do not enable NO_BACKSLASH_ESCAPES; escape backslashes to prevent \' single-quote escape!
		escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	}
	return "'" + escaped + "'"
}

func formatTokenStreamSQL(tokens []string) string {
	var b strings.Builder
	for i, tok := range tokens {
		if i > 0 && needsSpaceBetween(tokens[i-1], tok) {
			b.WriteByte(' ')
		}
		b.WriteString(tok)
	}
	return b.String()
}

func needsSpaceBetween(prev, curr string) bool {
	if curr == "," || curr == ";" || curr == ")" || curr == "." || prev == "." || prev == "(" {
		return false
	}
	if prev == "::" || curr == "::" {
		return false
	}
	return true
}

func isAllowedStructuralNumber(num string, toks []fingerprint.Token, idx int) bool {
	if num == "0" || num == "1" {
		return true
	}
	// Allow structural constants in data type precision declarations such as VARCHAR(64), DECIMAL(10, 2) or LIMIT / OFFSET clauses.
	if idx > 0 {
		prev := strings.ToLower(toks[idx-1].Text)
		if prev == "limit" || prev == "offset" || prev == "top" {
			return true
		}
		if toks[idx-1].IsPunct("(") && idx >= 2 {
			fnOrType := strings.ToLower(toks[idx-2].Text)
			if sqlDataTypeOrFuncAllowNumber[fnOrType] {
				return true
			}
		}
		if toks[idx-1].IsPunct(",") && idx >= 4 && toks[idx-3].IsPunct("(") {
			fnOrType := strings.ToLower(toks[idx-4].Text)
			if sqlDataTypeOrFuncAllowNumber[fnOrType] {
				return true
			}
		}
	}
	return false
}

var sqlDataTypeOrFuncAllowNumber = map[string]bool{
	"varchar": true, "char": true, "nvarchar": true, "decimal": true, "numeric": true,
	"int": true, "bigint": true, "smallint": true, "tinyint": true, "bit": true,
	"float": true, "double": true, "timestamp": true, "datetime": true, "time": true,
	"binary": true, "varbinary": true, "count": true, "round": true, "substr": true, "substring": true,
}

func prevIsTableContext(toks []fingerprint.Token, idx int) bool {
	if idx <= 0 {
		return false
	}
	prev := strings.ToLower(toks[idx-1].Text)
	switch prev {
	case "from", "join", "into", "update", "table":
		return true
	}
	if prev == "exists" && idx >= 4 {
		// CREATE TABLE IF NOT EXISTS <tbl> / ALTER TABLE IF EXISTS <tbl>
		p3 := strings.ToLower(toks[idx-3].Text)
		p4 := strings.ToLower(toks[idx-4].Text)
		if p3 == "table" || p4 == "table" {
			return true
		}
	}
	return false
}

func prevIsIndexContext(toks []fingerprint.Token, idx int) bool {
	if idx <= 0 {
		return false
	}
	prev := strings.ToLower(toks[idx-1].Text)
	if prev == "index" || prev == "concurrently" {
		return true
	}
	if prev == "exists" && idx >= 3 {
		p2 := strings.ToLower(toks[idx-2].Text)
		p3 := strings.ToLower(toks[idx-3].Text)
		if p2 == "index" || p3 == "index" {
			return true
		}
	}
	return false
}

func prevIsAliasContext(toks []fingerprint.Token, idx int) bool {
	if idx <= 0 {
		return false
	}
	return strings.ToLower(toks[idx-1].Text) == "as"
}

func isSQLKeywordForAnon(low string, toks []fingerprint.Token, idx int) bool {
	if idx > 0 && toks[idx-1].IsPunct(".") {
		return false
	}
	return sqlReservedAndBuiltins[low]
}

func isSQLKeywordOrGeneric(word string) bool {
	return sqlReservedAndBuiltins[strings.ToLower(strings.TrimSpace(word))]
}

var sqlReservedAndBuiltins = map[string]bool{
	"select": true, "from": true, "where": true, "insert": true, "into": true,
	"values": true, "update": true, "set": true, "delete": true, "create": true,
	"alter": true, "drop": true, "table": true, "index": true, "unique": true,
	"concurrently": true, "view": true, "with": true, "recursive": true, "as": true,
	"distinct": true, "all": true, "on": true, "using": true, "join": true,
	"inner": true, "left": true, "right": true, "full": true, "outer": true,
	"cross": true, "natural": true, "and": true, "or": true, "not": true,
	"in": true, "exists": true, "between": true, "like": true, "ilike": true,
	"is": true, "null": true, "true": true, "false": true, "group": true,
	"by": true, "having": true, "order": true, "asc": true, "desc": true,
	"nulls": true, "first": true, "last": true, "limit": true, "offset": true,
	"fetch": true, "next": true, "rows": true, "only": true, "union": true,
	"intersect": true, "except": true, "case": true, "when": true, "then": true,
	"else": true, "end": true, "cast": true, "coalesce": true, "nullif": true,
	"greatest": true, "least": true, "count": true, "sum": true, "avg": true,
	"min": true, "max": true, "now": true, "current_timestamp": true,
	"current_date": true, "current_time": true, "lower": true, "upper": true,
	"trim": true, "length": true, "concat": true, "substring": true, "substr": true,
	"date_trunc": true, "extract": true, "interval": true, "day": true,
	"days": true, "hour": true, "hours": true, "minute": true, "minutes": true,
	"second": true, "seconds": true, "month": true, "months": true, "year": true,
	"years": true, "primary": true, "key": true, "foreign": true, "references": true,
	"constraint": true, "default": true, "auto_increment": true, "serial": true,
	"bigserial": true, "identity": true, "generated": true, "always": true,
	"stored": true, "virtual": true, "add": true, "column": true, "modify": true,
	"change": true, "rename": true, "to": true, "if": true, "returning": true,
	"conflict": true, "do": true, "nothing": true, "duplicate": true,
	"varchar": true, "char": true, "text": true, "int": true, "integer": true,
	"bigint": true, "smallint": true, "tinyint": true, "boolean": true, "bool": true,
	"decimal": true, "numeric": true, "float": true, "double": true, "real": true,
	"date": true, "datetime": true, "timestamp": true, "timestamptz": true,
	"time": true, "json": true, "jsonb": true, "uuid": true, "bytea": true,
	"blob": true, "unsigned": true, "zerofill": true, "engine": true,
	"innodb": true, "charset": true, "utf8mb4": true, "collate": true,
	"btree": true, "hash": true, "gin": true, "gist": true, "over": true,
	"partition": true, "row_number": true, "rank": true, "dense_rank": true,
	"lag": true, "lead": true, "filter": true, "within": true, "any": true,
	"some": true, "array": true, "lateral": true,
	"show": true, "begin": true, "start": true, "transaction": true, "commit": true,
	"rollback": true, "read": true, "write": true, "extension": true, "schema": true,
	"database": true, "oid": true, "int2": true, "int4": true, "int8": true,
	"float4": true, "float8": true, "bpchar": true, "regclass": true, "regtype": true,
	"timetz": true, "varbinary": true, "mediumtext": true, "longtext": true,
	"tinytext": true, "mediumint": true, "ordinality": true, "unnest": true,
	"repeat": true, "format_type": true, "pg_get_expr": true, "col_description": true,
	"dialect": true, "template": true, "metadata": true, "mysql": true,
	"postgresql": true, "mariadb": true, "tidb": true,
}
