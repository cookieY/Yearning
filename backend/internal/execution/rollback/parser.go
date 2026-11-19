package rollback

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// ParsedStatement describes a SQL statement analyzed by a deterministic AST parser.
type ParsedStatement struct {
	RawSQL         string
	TrimmedSQL     string // SQL with leading/trailing whitespace and trailing semicolon stripped
	OperationType  string // "dml" | "ddl"
	Kind           StatementKind
	SchemaName     string
	SchemaExplicit bool // true when the SQL AST explicitly qualified the target schema (e.g. app.orders)
	TableName      string
	TableAlias     string

	// INSERT-specific
	InsertColumns []string
	InsertTuples  [][]string // Each tuple is a slice of SQL literals or expressions reconstructed from the AST

	// UPDATE / DELETE-specific
	UpdatedColumns []string
	WhereClause    string

	// DDL-specific
	PostSchemaName     string
	PostTableName      string
	ColumnName         string
	PostColumnName     string
	ColumnDefSQL       string
	NewDataTypeSQL     string
	IndexName          string
	IndexUnique        bool
	IndexColumns       []string
	ConstraintName     string
	ConstraintTypeHint string // FOREIGN KEY | CHECK | UNIQUE | PRIMARY KEY | ""
	ConstraintDefSQL   string
}

// AnalyzeStatement analyzes the reversibility of a single DML or DDL statement when rollback protection is enabled.
// Uses the TiDB Parser (github.com/pingcap/tidb/pkg/parser) to build a full AST for MySQL,
// and the official pure-Go libpg_query wrapper (github.com/wasilibs/go-pgquery) to build a full AST for PostgreSQL;
// returns *CapabilityError (Fail-Closed) if unsupported.
func AnalyzeStatement(engine, defaultSchema, sqlText string) (*ParsedStatement, error) {
	trimmed := trimLeadingCommentsAndSemicolon(sqlText)
	if trimmed == "" {
		return nil, &CapabilityError{Reason: "empty_statement", Message: "empty SQL statement cannot generate rollback"}
	}
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "mysql":
		return analyzeMySQLWithAST(defaultSchema, sqlText, trimmed)
	case "postgres", "postgresql":
		return analyzePostgresWithAST(defaultSchema, sqlText, trimmed)
	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_database_engine",
			Message: fmt.Sprintf("database engine %s is not supported", engine),
		}
	}
}

var nondeterministicFunctions = map[string]string{
	"RAND":            "nondeterministic_function_rand",
	"RANDOM":          "nondeterministic_function_random",
	"UUID":            "nondeterministic_function_uuid",
	"UUID_SHORT":      "nondeterministic_function_uuid_short",
	"GEN_RANDOM_UUID": "nondeterministic_function_gen_random_uuid",
	"SYSDATE":         "nondeterministic_function_sysdate",
	"CLOCK_TIMESTAMP": "nondeterministic_function_clock_timestamp",
	"TIMEOFDAY":       "nondeterministic_function_timeofday",
}

func trimLeadingCommentsAndSemicolon(sqlText string) string {
	s := strings.TrimSpace(sqlText)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return s
}

// QuoteIdent safely quotes an identifier for the specified engine.
func QuoteIdent(engine, ident string) string {
	if engine == "mysql" {
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// QualifiedTable returns the schema-qualified quoted table name.
func QualifiedTable(engine, schemaName, tableName string) string {
	if schemaName == "" {
		return QuoteIdent(engine, tableName)
	}
	return QuoteIdent(engine, schemaName) + "." + QuoteIdent(engine, tableName)
}

var numericLiteralPattern = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// FormatCellLiteral formats a captured RollbackCell into a deterministic, safe SQL literal.
func FormatCellLiteral(engine string, cell database.RollbackCell) string {
	if cell.IsNull {
		return "NULL"
	}
	if cell.HexBytes != "" {
		if engine == "mysql" {
			return "X'" + cell.HexBytes + "'"
		}
		return "decode('" + cell.HexBytes + "', 'hex')"
	}
	upperType := strings.ToUpper(strings.TrimSpace(cell.DatabaseType))
	if strings.Contains(upperType, "BOOL") || upperType == "TINYINT(1)" {
		switch strings.ToLower(strings.TrimSpace(cell.Text)) {
		case "true", "t", "1":
			if engine == "mysql" {
				return "1"
			}
			return "TRUE"
		case "false", "f", "0":
			if engine == "mysql" {
				return "0"
			}
			return "FALSE"
		}
	}
	if isPlainNumericType(upperType) && numericLiteralPattern.MatchString(strings.TrimSpace(cell.Text)) {
		return strings.TrimSpace(cell.Text)
	}
	escaped := strings.ReplaceAll(cell.Text, "'", "''")
	if engine == "mysql" && strings.Contains(escaped, `\`) {
		escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	}
	return "'" + escaped + "'"
}

func isPlainNumericType(upperType string) bool {
	for _, prefix := range []string{"INT", "BIGINT", "SMALLINT", "TINYINT", "MEDIUMINT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "OID"} {
		if strings.HasPrefix(upperType, prefix) {
			return true
		}
	}
	return false
}

// BuildPKWhereClause constructs an exact single-row identity key matching clause (col1 = val1 AND col2 = val2).
func BuildPKWhereClause(engine string, pkCols []string, row database.RollbackRow) (string, error) {
	if len(pkCols) == 0 {
		return "", &CapabilityError{Reason: "missing_identity_key", Message: "table has no identity key columns"}
	}
	parts := make([]string, 0, len(pkCols))
	for _, col := range pkCols {
		cell, ok := row.Get(col)
		if !ok {
			return "", fmt.Errorf("rollback: identity key column %s missing in captured row", col)
		}
		if cell.IsNull {
			return "", &CapabilityError{
				Reason:  "null_identity_key_value",
				Message: fmt.Sprintf("identity key column %s contains NULL", col),
			}
		}
		parts = append(parts, fmt.Sprintf("%s = %s", QuoteIdent(engine, col), FormatCellLiteral(engine, cell)))
	}
	return strings.Join(parts, " AND "), nil
}

// BuildMultiRowPKWhereClause constructs an exact multi-row identity key filter condition (IN (...) for single-column keys, OR combination for composite keys).
func BuildMultiRowPKWhereClause(engine string, pkCols []string, rows []database.RollbackRow) (string, error) {
	if len(rows) == 0 {
		return "1 = 0", nil
	}
	if len(pkCols) == 1 {
		col := pkCols[0]
		qCol := QuoteIdent(engine, col)
		vals := make([]string, 0, len(rows))
		for _, r := range rows {
			cell, ok := r.Get(col)
			if !ok || cell.IsNull {
				return "", fmt.Errorf("rollback: identity column %s missing or NULL", col)
			}
			vals = append(vals, FormatCellLiteral(engine, cell))
		}
		return fmt.Sprintf("%s IN (%s)", qCol, strings.Join(vals, ", ")), nil
	}
	clauses := make([]string, 0, len(rows))
	for _, r := range rows {
		c, err := BuildPKWhereClause(engine, pkCols, r)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, "("+c+")")
	}
	return strings.Join(clauses, " OR "), nil
}

// RowIdentityKey constructs a unique string key for a row in an in-memory map (used to align BeforeRow and AfterRow).
func RowIdentityKey(pkCols []string, row database.RollbackRow) (string, error) {
	var b strings.Builder
	for i, col := range pkCols {
		cell, ok := row.Get(col)
		if !ok || cell.IsNull {
			return "", fmt.Errorf("rollback: missing identity key column %s", col)
		}
		if i > 0 {
			b.WriteByte('|')
		}
		val := cell.Text
		if isPlainNumericType(strings.ToUpper(cell.DatabaseType)) {
			val = normalizeNumericKey(val)
		}
		b.WriteString(strconv.Itoa(len(val)))
		b.WriteByte(':')
		b.WriteString(val)
	}
	return b.String(), nil
}

func normalizeNumericKey(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
