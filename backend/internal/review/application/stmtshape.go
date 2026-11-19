// stmtshape.go Fault-tolerant lexical extraction of statement structural facts (B8 ruling: lexical facts
// live in code, business interpretation lives in skills). The extractor produces only identifier-free
// structural booleans, counts, and enums — the privacy contract forbids raw literals, comments, or
// identifiers from entering model context; name-related checks (prefix conventions, primary key names,
// keywords) are presented as code-computed compliance booleans whose conventions mirror the skill knowledge text.
//
// Fault-tolerance semantics: facts that cannot be determined are omitted (absent from group statistics);
// skill knowledge explicitly states "do not emit a finding when the fact is absent" (unknown semantics, PRD 4.3).
package application

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// Statement structural facts (single-statement granularity prior to group aggregation).
type stmtFacts struct {
	m          map[string]any
	incomplete bool
}

func (f *stmtFacts) set(k string, v any) {
	if f.m == nil {
		f.m = map[string]any{}
	}
	f.m[k] = v
}

var (
	reWordLimit      = regexp.MustCompile(`(?i)\blimit\s+\d`)
	reWordOrderBy    = regexp.MustCompile(`(?i)\border\s+by\b`)
	reSubSelect      = regexp.MustCompile(`(?i)\b(from|join|where|exists|in)\s*\(?\s*select\b`)
	reInsertCols     = regexp.MustCompile(`(?i)\binsert\s+(ignore\s+)?into\s+[^\s(]+\s*\(`)
	reNullLiteral    = regexp.MustCompile(`(?i)[=<>]\s*null\b`)
	reInsertNull     = regexp.MustCompile(`(?i)values\s*\(.*\bnull\b`)
	reAutoIncrement  = regexp.MustCompile(`(?i)\bauto_increment\b`)
	reAutoIncInit    = regexp.MustCompile(`(?i)\bauto_increment\s*=\s*(\d+)`)
	reUnsigned       = regexp.MustCompile(`(?i)\bauto_increment[^,)]*\bunsigned\b|\bunsigned\b[^,)]*\bauto_increment\b`)
	reTableComment   = regexp.MustCompile(`(?i)\bcomment\s*=?\s*'`)
	rePrimaryKey     = regexp.MustCompile(`(?i)\bprimary\s+key\b`)
	reForeignKey     = regexp.MustCompile(`(?i)\bforeign\s+key\b`)
	rePartition      = regexp.MustCompile(`(?i)\bpartition\s+by\b`)
	reIndexEntry     = regexp.MustCompile(`(?i)[,(\s](unique\s+)?(?:key|index|constraint\s+[^\s(]+\s+key)\s*([^\s(]*)\s*\(`)
	reCharLen        = regexp.MustCompile(`(?i)\bchar\s*\(\s*(\d+)\s*\)`)
	reVarCharLen     = regexp.MustCompile(`(?i)\bvarchar\s*\(\s*(\d+)\s*\)`)
	reFloatDouble    = regexp.MustCompile(`(?i)\b(float|double)\s*[(\s]`)
	reSpecialType    = regexp.MustCompile(`(?i)\b(enum|set|geometry|point|blob|text|json)\s*[(,\s]`)
	reCreateView     = regexp.MustCompile(`(?i)^create\s+(or\s+replace\s+)?(algorithm\s*=\s*\w+\s+)?(sql\s+security\s+\w+\s+)?view\b`)
	reViewStar       = regexp.MustCompile(`(?i)\bselect\s+\*`)
	reCharset        = regexp.MustCompile(`(?i)\b(charset|character\s+set)\s*=?\s*([a-z0-9_]{1,64})`)
	reCollation      = regexp.MustCompile(`(?i)\bcollate\s*=?\s*([a-z0-9_]{1,64})`)
	reNotnull        = regexp.MustCompile(`(?i)\bnot\s+null\b`)
	reDefault        = regexp.MustCompile(`(?i)\bdefault\b`)
	reModifyChange   = regexp.MustCompile(`(?i)\b(modify|change)\s+(column\s+)?[^\s]+`)
	reAfterFirst     = regexp.MustCompile(`(?i)\b(after|first)\b`)
	reRenameTable    = regexp.MustCompile(`(?i)\brename\s+table\b`)
	reQualifiedIdent = regexp.MustCompile(`(?i)\bto\s+[` + "`" + `]?[a-z0-9_]+\.`)
	reDropTarget     = regexp.MustCompile(`(?is)^drop\s+(table|database|schema|view|index|trigger|procedure|function)\b`)
	reParens         = regexp.MustCompile(`\(`)
)

// sqlKeywordSet is the subset of common reserved words used for keyword checks (trimmed from the same
// source as legacy CheckIdentifier; covers high-frequency reserved words).
var sqlKeywordSet = map[string]bool{
	"order": true, "group": true, "select": true, "insert": true, "update": true,
	"delete": true, "where": true, "from": true, "table": true, "database": true,
	"index": true, "key": true, "primary": true, "foreign": true, "references": true,
	"create": true, "drop": true, "alter": true, "rename": true, "values": true,
	"set": true, "into": true, "join": true, "on": true, "by": true, "as": true,
	"and": true, "or": true, "not": true, "null": true, "default": true,
	"constraint": true, "unique": true, "check": true, "column": true,
}

// extractStmtFacts extracts structural facts from a single statement (containing no plaintext identifiers).
func extractStmtFacts(sql []byte, op, class string) stmtFacts {
	var f stmtFacts
	s := string(sql)
	if class == "dml" {
		switch op {
		case "update", "delete":
			f.set("dml_has_limit", reWordLimit.MatchString(s))
			f.set("dml_has_order_by", reWordOrderBy.MatchString(s))
			f.set("dml_has_subselect", reSubSelect.MatchString(s))
			f.set("where_literal_null", reNullLiteral.MatchString(s))
		case "insert":
			f.set("insert_has_columns", reInsertCols.MatchString(s))
			f.set("insert_has_null_literal", reInsertNull.MatchString(s))
			f.set("dml_has_subselect", reSubSelect.MatchString(s))
		case "replace":
			f.set("dml_has_subselect", reSubSelect.MatchString(s))
		}
		return f
	}
	// DDL family.
	switch {
	case reCreateView.MatchString(s):
		f.set("ddl_is_create_view", true)
		f.set("ddl_view_select_star", reViewStar.MatchString(s))
		return f
	case op == "drop":
		m := reDropTarget.FindStringSubmatch(s)
		kind := "other"
		if m != nil {
			kind = strings.ToLower(m[1])
			if kind == "schema" {
				kind = "database"
			}
		}
		f.set("ddl_drop_target", kind)
		return f
	case op == "rename" || reRenameTable.MatchString(s):
		f.set("ddl_is_rename_table", true)
		f.set("ddl_across_db_rename", reQualifiedIdent.MatchString(s))
		return f
	case op == "alter":
		f.set("ddl_alters_column_type", reModifyChange.MatchString(s))
		f.set("ddl_changes_column_position", reAfterFirst.MatchString(s))
		return f
	case op == "create", op == "truncate":
		if op == "truncate" {
			return f
		}
	}
	if op != "create" {
		return f
	}
	// CREATE TABLE structural facts.
	f.set("ddl_has_primary_key", rePrimaryKey.MatchString(s))
	f.set("ddl_has_foreign_key", reForeignKey.MatchString(s))
	f.set("ddl_has_partition", rePartition.MatchString(s))
	f.set("ddl_table_comment_present", reTableComment.MatchString(s))
	f.set("ddl_has_float_double", reFloatDouble.MatchString(s))
	f.set("ddl_has_special_type", reSpecialType.MatchString(s))
	// Auto-increment (MySQL syntax; PG identity is covered by skill knowledge under the engine tag).
	if reAutoIncrement.MatchString(s) {
		f.set("ddl_has_auto_increment", true)
		f.set("ddl_autoincrement_unsigned", reUnsigned.MatchString(s))
		if m := reAutoIncInit.FindStringSubmatch(s); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			f.set("ddl_autoincrement_init_one", n == 1)
		}
	}
	// Column comments / NOT NULL / default values / column count (approximate count).
	f.set("ddl_column_comment_count", int64(strings.Count(strings.ToLower(s), "comment '")))
	f.set("ddl_column_count", int64(len(reColumnDefs.FindAllString(s, -1))))
	f.set("ddl_not_null_count", int64(len(reNotnull.FindAllString(s, -1))))
	f.set("ddl_default_count", int64(len(reDefault.FindAllString(s, -1))))
	// Indexes: count, max column count, empty name, prefix compliance (code-computed boolean).
	type idxEntry struct {
		name string
		open int
	}
	var idxNames []string
	maxParts := 0
	for _, loc := range reIndexEntry.FindAllStringSubmatchIndex(s, -1) {
		name := strings.Trim(strings.ToLower(s[loc[4]:loc[5]]), "`\"")
		idxNames = append(idxNames, name)
		open := loc[1] - 1
		if open < len(s) && s[open] == '(' {
			depth, parts := 1, 1
			for i := open + 1; i < len(s) && depth > 0; i++ {
				switch s[i] {
				case '(':
					depth++
				case ')':
					depth--
				case ',':
					if depth == 1 {
						parts++
					}
				}
			}
			if parts > maxParts {
				maxParts = parts
			}
		}
	}
	_ = idxEntry{}
	f.set("ddl_index_count", int64(len(idxNames)))
	f.set("ddl_index_max_key_parts", int64(maxParts))
	unnamed := 0
	prefixOK := true
	for _, n := range idxNames {
		if n == "" {
			unnamed++
			continue
		}
		if !strings.HasPrefix(n, "idx_") && !strings.HasPrefix(n, "uniq_") {
			prefixOK = false
		}
	}
	f.set("ddl_index_unnamed_count", int64(unnamed))
	f.set("ddl_index_prefix_conformant", prefixOK)
	// Primary key named id / integer type: computed when primary key column names can be parsed, otherwise omitted (unknown).
	if pk := extractPrimaryKeyColumn(s); pk != "" {
		f.set("ddl_pk_named_id", strings.EqualFold(pk, "id"))
		if col := extractColumnDef(s, pk); col != "" {
			f.set("ddl_pk_is_integer", regexp.MustCompile(`(?i)\b(tinyint|smallint|mediumint|int|integer|bigint)\b`).MatchString(col))
		}
	}
	// Maximum length of char/varchar columns.
	maxChar, maxVarChar := 0, 0
	for _, m := range reCharLen.FindAllStringSubmatch(s, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > maxChar {
			maxChar = n
		}
	}
	for _, m := range reVarCharLen.FindAllStringSubmatch(s, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > maxVarChar {
			maxVarChar = n
		}
	}
	f.set("ddl_max_char_length", int64(maxChar))
	f.set("ddl_max_varchar_length", int64(maxVarChar))
	// Charset/collation values cross the Provider boundary only when the lexer
	// proves their DDL role and the value belongs to the frozen engine
	// allowlist. Regex matches inside comments, strings or unlisted values are
	// treated as unknown and make the whole statement context incomplete.
	charset, charsetComplete := controlledDDLWord(sql, false, len(reCharset.FindAllStringSubmatch(s, -1)) > 0)
	collation, collationComplete := controlledDDLWord(sql, true, len(reCollation.FindAllStringSubmatch(s, -1)) > 0)
	if charsetComplete {
		f.set("ddl_charset", charset)
	} else if len(reCharset.FindAllStringSubmatch(s, -1)) > 0 {
		f.set("ddl_charset_status", "unknown")
		f.incomplete = true
	}
	if collationComplete {
		f.set("ddl_collation", collation)
	} else if len(reCollation.FindAllStringSubmatch(s, -1)) > 0 {
		f.set("ddl_collation_status", "unknown")
		f.incomplete = true
	}
	if f.incomplete {
		// Facts override the grouping shape field after aggregation, so the
		// persisted Provider context and metadata gate agree on incompleteness.
		f.set("metadata_context_complete", false)
	}
	// Table name length and keyword compliance (code-computed booleans; prefix is unconstrained by default,
	// while deployment-level prefixes are carried by table-scoped internal knowledge entries).
	if tn := extractTableName(s); tn != "" {
		f.set("ddl_table_name_length", int64(len(tn)))
		f.set("ddl_table_identifier_not_keyword", !sqlKeywordSet[strings.ToLower(tn)])
		f.set("ddl_table_prefix_conformant", true)
	}
	return f
}

var (
	rePKColumn   = regexp.MustCompile(`(?i)primary\s+key\s*\(\s*[` + "`" + `"]?([a-z0-9_]+)`)
	reTableName  = regexp.MustCompile(`(?is)^create\s+(temporary\s+)?table\s+(if\s+not\s+exists\s+)?[` + "`" + `"]?([a-z0-9_.]+)`)
	reColumnDefs = regexp.MustCompile(`(?i)\b([` + "`" + `"]?[a-z_][a-z0-9_]*[` + "`" + `"]?)\s+(tinyint|smallint|mediumint|int|integer|bigint|char|varchar|text|blob|decimal|numeric|float|double|date|datetime|timestamp|time|year|enum|set|json|geometry|point|bit|boolean|bool)\b`)
)

func extractPrimaryKeyColumn(s string) string {
	if m := rePKColumn.FindStringSubmatch(s); m != nil {
		return strings.Trim(m[1], "`\"")
	}
	return ""
}

func extractTableName(s string) string {
	if m := reTableName.FindStringSubmatch(s); m != nil {
		name := m[3]
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		return strings.Trim(name, "`\"")
	}
	return ""
}

// extractColumnDef returns a column definition slice (from the type token to the comma/parenthesis boundary).
func extractColumnDef(s, column string) string {
	re := regexp.MustCompile(`(?i)[` + "`" + `"]?` + regexp.QuoteMeta(column) + `[` + "`" + `"]?\s+`)
	loc := re.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	end := loc[1]
	for end < len(s) && s[end] != ',' && s[end] != ')' {
		end++
	}
	return s[loc[1]:end]
}

// mergeFactsInto aggregates facts across a group: logical OR for booleans, maximum for counts/lengths,
// and first non-empty value for enums (statements within an isomorphic group share the same fingerprint,
// so values converge). Lazily initializes the target map.
func mergeFactsInto(dst *stmtFacts, add stmtFacts) {
	if add.incomplete {
		dst.incomplete = true
	}
	if len(add.m) == 0 {
		return
	}
	if dst.m == nil {
		dst.m = map[string]any{}
	}
	for k, v := range add.m {
		switch t := v.(type) {
		case bool:
			if cur, ok := dst.m[k].(bool); ok {
				dst.m[k] = cur || t
			} else {
				dst.m[k] = t
			}
		case int64:
			if cur, ok := dst.m[k].(int64); ok {
				if t > cur {
					dst.m[k] = t
				}
			} else {
				dst.m[k] = t
			}
		case string:
			if _, ok := dst.m[k]; !ok {
				dst.m[k] = t
			}
		}
	}
}

// controlledDDLWord extracts only complete lexical declarations. The flags are
// the union of supported dialect comment/string forms because token spans, not
// substring matches, decide which text is inactive. This can only reject more
// cases and never treats commented or literal content as DDL syntax.
func controlledDDLWord(sql []byte, collation bool, suspicious bool) (string, bool) {
	if !suspicious {
		return "", false
	}
	lex, err := fingerprint.Tokenize(sql, fingerprint.DialectFlags{
		BacktickIdent:            true,
		DoubleQuoteIdent:         true,
		BackslashEscape:          true,
		HashLineComment:          true,
		ExecutableVersionComment: true,
		DollarQuote:              true,
		DollarParam:              true,
		NestedBlockComment:       true,
	})
	if err != nil {
		return "", false
	}
	values := []string{}
	for i := 0; i < len(lex.Tokens); i++ {
		token := lex.Tokens[i]
		if token.Kind != fingerprint.TokWord {
			continue
		}
		next := i + 1
		matched := token.Text == "collate"
		if !collation {
			matched = token.Text == "charset"
			if token.Text == "character" {
				if next >= len(lex.Tokens) || !lex.Tokens[next].IsWord("set") {
					continue
				}
				next++
				matched = true
			}
		}
		if !matched {
			continue
		}
		if next < len(lex.Tokens) && lex.Tokens[next].IsPunct("=") {
			next++
		}
		if next >= len(lex.Tokens) || lex.Tokens[next].Kind != fingerprint.TokWord {
			return "", false
		}
		value := strings.ToLower(lex.Tokens[next].Text)
		allowed := fingerprint.IsControlledCollationWord(value)
		if !collation {
			allowed = fingerprint.IsControlledCharsetWord(value)
		}
		if !allowed {
			return "", false
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return "", false
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return "", false
		}
	}
	return values[0], true
}
