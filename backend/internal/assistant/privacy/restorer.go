// restorer.go implements dialect lexical/syntactic boundary validation, authorized object checks,
// parameter binding completeness verification, and token-stream structured restoration for
// model-generated anonymized SQL (strictly prohibiting global string replacement).
package privacy

import (
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
	"sort"
	"strings"
)

// ValidateAndRestoreSQL parses the model-returned anonymized SQL according to the target dialect,
// verifies single-statement boundaries, allowed v1 statement kinds, authorized object token mappings,
// and parameter placeholder bindings, and restores it into executable SQL via the token stream.
func ValidateAndRestoreSQL(
	session *TurnPrivacySession,
	anonymizedSQL string,
	profile fingerprint.Profile,
	outputLocale ...string,
) (*RestorationResult, error) {
	if session == nil || session.IsReleased() {
		return nil, domain.ErrSQLRestorationInvalid("privacy_session_unavailable")
	}
	toks, stmtKind, riskCat, err := parseSingleAllowedStatement(anonymizedSQL, profile)
	if err != nil {
		return nil, err
	}
	anonScope := collectAnonDeclaredScope(toks, stmtKind)

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
			ident, _ := rawIdentName(t)
			low := strings.ToLower(strings.TrimSpace(ident))
			if revprivacy.AnonTokenPattern.MatchString(low) {
				kind, orig, ok := session.LookupIdentifier(low)
				if ok {
					if kind == revprivacy.KindTable {
						referencedTables[orig] = struct{}{}
					}
					outTokens = append(outTokens, quoteIdentifierForProfile(orig, profile))
					continue
				}
				if !isReservedAnonObjectToken(low) && anonScope.isAllowedLocalIdentifier(toks, i, low) {
					outTokens = append(outTokens, quoteIdentifierForProfile(ident, profile))
					continue
				}
				return nil, domain.ErrSQLRestorationInvalid("unknown_or_unauthorized_object_token:" + low)
			}
			if anonScope.isAllowedLocalIdentifier(toks, i, low) {
				outTokens = append(outTokens, quoteIdentifierForProfile(ident, profile))
				continue
			}
			return nil, domain.ErrSQLRestorationInvalid("unmapped_quoted_identifier_forbidden")

		case fingerprint.TokWord:
			low := strings.ToLower(t.Text)
			if revprivacy.AnonTokenPattern.MatchString(low) {
				kind, orig, ok := session.LookupIdentifier(low)
				if ok {
					if kind == revprivacy.KindTable {
						referencedTables[orig] = struct{}{}
					}
					outTokens = append(outTokens, quoteIdentifierForProfile(orig, profile))
					continue
				}
				if !isReservedAnonObjectToken(low) && anonScope.isAllowedLocalIdentifier(toks, i, low) {
					outTokens = append(outTokens, quoteIdentifierForProfile(t.Text, profile))
					continue
				}
				return nil, domain.ErrSQLRestorationInvalid("unknown_or_unauthorized_object_token:" + low)
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
			if anonScope.isAllowedLocalIdentifier(toks, i, low) {
				outTokens = append(outTokens, quoteIdentifierForProfile(t.Text, profile))
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

	warnings := collectSemanticWarnings(stmtKind, hasWhereClause, hasSelectStar, firstLocaleArg(outputLocale))

	return &RestorationResult{
		RestoredSQL:      restoredSQL,
		StatementKind:    stmtKind,
		RiskCategory:     riskCat,
		ReferencedTables: tables,
		SemanticWarnings: warnings,
	}, nil
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

// ValidateRawAssistantSQL enforces dialect parse, single-statement boundary, allowed statement kind,
// comment/escape, semantic warning, and fail-closed authorized schema/table/column scope validation
// for SAN ("san") model drafts that already contain raw identifiers and literals.
func ValidateRawAssistantSQL(rawSQL string, profile fingerprint.Profile, scope RawSQLScope, outputLocale ...string) (*RestorationResult, error) {
	resolver, err := newRawScopeResolver(scope)
	if err != nil {
		return nil, err
	}
	toks, stmtKind, riskCat, err := parseSingleAllowedStatement(rawSQL, profile)
	if err != nil {
		return nil, err
	}
	hasWhereClause, hasSelectStar, err := scanRawTokenConstraints(toks)
	if err != nil {
		return nil, err
	}
	referencedTables := make(map[string]struct{})
	if err := validateRawAuthorizedScope(toks, stmtKind, resolver, referencedTables); err != nil {
		return nil, err
	}
	if len(referencedTables) == 0 {
		return nil, domain.ErrSQLRestorationInvalid("no_authorized_table_referenced")
	}
	trimmed, err := reverifyRawSingleStatement(rawSQL, profile, stmtKind)
	if err != nil {
		return nil, err
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
		SemanticWarnings: collectSemanticWarnings(stmtKind, hasWhereClause, hasSelectStar, firstLocaleArg(outputLocale)),
	}, nil
}

func scanRawTokenConstraints(toks []fingerprint.Token) (hasWhereClause, hasSelectStar bool, err error) {
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.IsWord("where") {
			hasWhereClause = true
		}
		if t.IsPunct("*") && i > 0 && toks[i-1].IsWord("select") {
			hasSelectStar = true
		}
		if err := checkRawTokenHazard(toks, i); err != nil {
			return false, false, err
		}
	}
	return hasWhereClause, hasSelectStar, nil
}

func checkRawTokenHazard(toks []fingerprint.Token, idx int) error {
	t := toks[idx]
	if t.IsWord("into") && idx+1 < len(toks) && (toks[idx+1].IsWord("outfile") || toks[idx+1].IsWord("dumpfile")) {
		return domain.ErrSQLRestorationInvalid("into_outfile_forbidden")
	}
	if t.IsPunct(":") && idx+1 < len(toks) && toks[idx+1].Kind == fingerprint.TokWord {
		if valPlaceholderRe.MatchString(strings.ToLower(toks[idx+1].Text)) {
			return domain.ErrSQLRestorationInvalid("unbound_parameter_placeholder:" + strings.ToLower(toks[idx+1].Text))
		}
	}
	switch t.Kind {
	case fingerprint.TokParam:
		return domain.ErrSQLRestorationInvalid("unbound_positional_parameter:" + strings.ToLower(t.Text))
	case fingerprint.TokPunct:
		if t.Text == "?" {
			return domain.ErrSQLRestorationInvalid("unbound_question_mark_parameter")
		}
	case fingerprint.TokWord:
		low := strings.ToLower(t.Text)
		if revprivacy.AnonTokenPattern.MatchString(low) {
			return domain.ErrSQLRestorationInvalid("unreplaced_anonymized_token:" + low)
		}
	case fingerprint.TokQuotedIdent, fingerprint.TokString, fingerprint.TokDollarString, fingerprint.TokNumber:
	default:
		return domain.ErrSQLRestorationInvalid("unsupported_token_kind")
	}
	return nil
}

func reverifyRawSingleStatement(rawSQL string, profile fingerprint.Profile, stmtKind domain.StatementKind) (string, error) {
	trimmed := strings.TrimSpace(rawSQL)
	reLex, rerr := fingerprint.Tokenize([]byte(trimmed), profile.Flags())
	if rerr != nil || len(reLex.Tokens) == 0 {
		return "", domain.ErrSQLRestorationInvalid("restored_sql_reparse_failed")
	}
	reToks := reLex.Tokens
	for i, rt := range reToks {
		if rt.IsPunct(";") && i != len(reToks)-1 {
			return "", domain.ErrSQLRestorationInvalid("restored_sql_multi_statement_escape")
		}
	}
	if reToks[len(reToks)-1].IsPunct(";") {
		reToks = reToks[:len(reToks)-1]
	}
	reKind, _, cerr := classifyAllowedStatement(reToks)
	if cerr != nil || reKind != stmtKind {
		return "", domain.ErrSQLRestorationInvalid("restored_sql_statement_kind_drift")
	}
	return trimmed, nil
}

func isValidRawCTEIdent(tok fingerprint.Token) (string, bool) {
	ident, ok := rawIdentName(tok)
	if !ok || (tok.Kind == fingerprint.TokWord && isRawSQLKeyword(strings.ToLower(ident))) {
		return "", false
	}
	return strings.ToLower(ident), true
}

func collectRawCTEDeclarations(toks []fingerprint.Token, st *rawScopeAnalysis) {
	collectCTEDeclarations(toks, st.consumed, st.cteNames, st.cteCols, isValidRawCTEIdent)
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
