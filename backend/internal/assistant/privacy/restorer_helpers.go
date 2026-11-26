// restorer_helpers.go holds restorer domain helpers and operations.
package privacy

import (
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"regexp"
	"strconv"
	"strings"
)

// RestorationResult is the structured output produced when ValidateAndRestoreSQL succeeds.
type RestorationResult struct {
	RestoredSQL      string
	StatementKind    domain.StatementKind
	RiskCategory     domain.DraftRiskCategory
	ReferencedTables []string
	SemanticWarnings []string
}

var valPlaceholderRe = regexp.MustCompile(`^val_(?:str|int|num|bool|ts|uuid|json|hex)_[0-9]+$`)

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

const (
	semanticWarningNoWhereEN    = "Statement has no WHERE filter and will affect the entire table; verify the target scope carefully"
	semanticWarningSelectStarEN = "Statement uses `SELECT *`; specify required columns explicitly to reduce network transfer and index lookup overhead"

	// Localized zh-CN equivalents kept Unicode-escaped per LANG-001 pure-English Go source rule.
	semanticWarningNoWhereZH    = "\u8bed\u53e5\u7f3a\u5c11 WHERE \u6761\u4ef6\uff0c\u5c06\u5f71\u54cd\u6574\u5f20\u8868\uff0c\u8bf7\u4ed4\u7ec6\u6838\u5bf9\u53d8\u66f4\u8303\u56f4"
	semanticWarningSelectStarZH = "\u8bed\u53e5\u4f7f\u7528\u4e86 `SELECT *`\uff0c\u5efa\u8bae\u660e\u786e\u6307\u5b9a\u6240\u9700\u5217\u4ee5\u51cf\u5c11\u7f51\u7edc\u4f20\u8f93\u4e0e\u56de\u8868\u5f00\u9500"
)

func firstLocaleArg(locales []string) string {
	if len(locales) == 0 {
		return ""
	}
	return locales[0]
}

func isEnglishWarningLocale(locale string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(locale))
	return trimmed == "en-us" || strings.HasPrefix(trimmed, "en")
}

func collectSemanticWarnings(stmtKind domain.StatementKind, hasWhereClause, hasSelectStar bool, outputLocale string) []string {
	isEN := isEnglishWarningLocale(outputLocale)
	var warnings []string
	if (stmtKind == domain.StmtUpdate || stmtKind == domain.StmtDelete) && !hasWhereClause {
		if isEN {
			warnings = append(warnings, semanticWarningNoWhereEN)
		} else {
			warnings = append(warnings, semanticWarningNoWhereZH)
		}
	}
	if stmtKind == domain.StmtSelect && hasSelectStar {
		if isEN {
			warnings = append(warnings, semanticWarningSelectStarEN)
		} else {
			warnings = append(warnings, semanticWarningSelectStarZH)
		}
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
	// Allow structural constants in data type precision declarations such as VARCHAR(64), DECIMAL(10, 2) or LIMIT / OFFSET / FETCH clauses.
	if idx > 0 {
		prev := strings.ToLower(toks[idx-1].Text)
		if prev == "limit" || prev == "offset" || prev == "top" {
			return true
		}
		if (prev == "next" || prev == "first") && idx >= 2 && toks[idx-2].IsWord("fetch") {
			return true
		}
		if toks[idx-1].IsPunct(",") && isLimitOffsetComma(toks, idx-1) {
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

// isLimitOffsetComma matches MySQL's LIMIT <offset>, <count> syntax where <offset> is a numeric
// literal or a bound value placeholder (:val_int_n, ":" + val_int_n, or val_int_n).
func isLimitOffsetComma(toks []fingerprint.Token, commaIdx int) bool {
	if commaIdx < 2 {
		return false
	}
	prev := toks[commaIdx-1]
	if prev.Kind == fingerprint.TokNumber || prev.Kind == fingerprint.TokParam {
		return toks[commaIdx-2].IsWord("limit")
	}
	if prev.Kind == fingerprint.TokWord && valPlaceholderRe.MatchString(strings.ToLower(prev.Text)) {
		if toks[commaIdx-2].IsWord("limit") {
			return true
		}
		if commaIdx >= 3 && toks[commaIdx-2].IsPunct(":") && toks[commaIdx-3].IsWord("limit") {
			return true
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
	if prev == "key" && idx >= 2 {
		p2 := strings.ToLower(toks[idx-2].Text)
		if p2 == "add" || p2 == "drop" || p2 == "unique" || p2 == "fulltext" || p2 == "spatial" {
			return true
		}
	}
	if prev == "exists" && idx >= 3 {
		p2 := strings.ToLower(toks[idx-2].Text)
		p3 := strings.ToLower(toks[idx-3].Text)
		if p2 == "index" || p3 == "index" {
			return true
		}
		if idx >= 4 && strings.ToLower(toks[idx-4].Text) == "index" {
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
