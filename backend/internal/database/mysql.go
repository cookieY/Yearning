// mysql.go MySQL dialect: fixed metadata SQL (fully parameterized), instance identity, and EXPLAIN parsing.
// Metadata queries are defined exclusively here; assembling target database metadata SQL outside this package is forbidden.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

type mysqlAdapter struct {
	baseAdapter
}

const mysqlListSchemasSQL = `SELECT schema_name FROM information_schema.schemata
 WHERE schema_name NOT IN ('mysql','sys','performance_schema','information_schema')
 ORDER BY schema_name`

const mysqlListTablesSQL = `SELECT table_name FROM information_schema.tables
 WHERE table_schema = ? AND table_type = 'BASE TABLE'
 ORDER BY table_name`

const mysqlListColumnsSQL = `SELECT column_name, ordinal_position, data_type, is_nullable = 'YES',
 ifnull(column_key, ''), ifnull(column_default, ''), ifnull(extra, ''), ifnull(column_comment, '')
 FROM information_schema.columns
 WHERE table_schema = ? AND table_name = ?
 ORDER BY ordinal_position`

const mysqlListIndexesSQL = `SELECT index_name, non_unique, index_name = 'PRIMARY',
 ifnull(column_name, ''), seq_in_index, 'key', column_name IS NULL, sub_part
 FROM information_schema.statistics
 WHERE table_schema = ? AND table_name = ?
 ORDER BY index_name, seq_in_index`

const mysqlTableExistenceFactsSQL = `SELECT
 (SELECT COUNT(*) FROM information_schema.tables
  WHERE table_schema = ? AND table_name = ? AND table_type = 'BASE TABLE') AS table_count,
 (SELECT COUNT(*) FROM information_schema.tables
  WHERE table_schema = ? AND table_name = ? AND table_type <> 'BASE TABLE') AS unsupported_relation_count,
 CASE WHEN
  EXISTS (SELECT 1 FROM information_schema.user_privileges
   WHERE replace(grantee, '''', '') = current_user()
     AND privilege_type = 'SELECT')
  OR EXISTS (SELECT 1 FROM information_schema.schema_privileges
   WHERE replace(grantee, '''', '') = current_user()
     AND table_schema = ?
     AND privilege_type = 'SELECT')
  THEN 1 ELSE 0 END AS metadata_privilege`

const mysqlTableRowEstimateSQL = `SELECT table_rows FROM information_schema.tables
 WHERE table_schema = ? AND table_name = ? AND table_type = 'BASE TABLE'`

const mysqlIdentitySQL = `SELECT version(), ifnull(@@server_uuid, ''), @@lower_case_table_names`

const mysqlIdentityFallbackSQL = `SELECT version(), @@lower_case_table_names`

func listSchemasSQL(engine string) string {
	if engine == "mysql" {
		return mysqlListSchemasSQL
	}
	return pgListSchemasSQL
}

func listTablesSQL(engine string) string {
	if engine == "mysql" {
		return mysqlListTablesSQL
	}
	return pgListTablesSQL
}

func listColumnsSQL(engine string) string {
	if engine == "mysql" {
		return mysqlListColumnsSQL
	}
	return pgListColumnsSQL
}

func listIndexesSQL(engine string) string {
	if engine == "mysql" {
		return mysqlListIndexesSQL
	}
	return pgListIndexesSQL
}

// readMySQLIdentity reads the MySQL instance identity.
func readMySQLIdentity(ctx context.Context, a *baseAdapter, cred Credential, mat *TLSMaterial) (*ServerIdentity, error) {
	cfg, reg, err := BuildMySQLConfig(a.ep, cred, mat, ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer reg.close()
	// SEC-0009: connect directly from Config via Connector (FormatDSN + re-parse would drop the guarded DialFunc).
	conn, cerr := mysql.NewConnector(cfg)
	if cerr != nil {
		// §18.76 supplement: go-sql-driver Open errors do not echo the DSN, so surfacing parse reasons is safe.
		return nil, fmt.Errorf("database: mysql dial failed: %w", cerr)
	}
	db := sql.OpenDB(conn)
	defer func() { _ = db.Close() }()
	var version, serverUUID, lowerCase string
	if err := db.QueryRowContext(ctx, mysqlIdentitySQL).Scan(&version, &serverUUID, &lowerCase); err != nil {
		// OceanBase MySQL mode does not expose @@server_uuid (MySQL error 1193 ER_UNKNOWN_SYSTEM_VARIABLE).
		// Fall back to reading version() and @@lower_case_table_names so identity probing succeeds with uuid-unavailable.
		if !isMySQLUnknownSystemVariable(err) {
			// §18.76 supplement: driver errors (connection refused / auth failure / handshake incompatibility) do not contain passwords.
			return nil, fmt.Errorf("database: mysql probe failed: %w", err)
		}
		if ferr := db.QueryRowContext(ctx, mysqlIdentityFallbackSQL).Scan(&version, &lowerCase); ferr != nil {
			return nil, fmt.Errorf("database: mysql probe failed: %w", ferr)
		}
	}
	if serverUUID == "" {
		serverUUID = "uuid-unavailable"
	}
	dbName := a.ep.DatabaseName
	return &ServerIdentity{
		EngineFamily:         "mysql",
		ServerVersion:        normalizeVersionString(version),
		RawVersionBanner:     version,
		InstanceMarker:       "mysql:" + serverUUID,
		LowerCaseTableNames:  lowerCase,
		DatabaseName:         dbName,
		ConfiguredEndpointID: fmt.Sprintf("%s:%d", a.ep.Host, a.ep.Port),
	}, nil
}

func normalizeVersionString(v string) string {
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		return v[:i]
	}
	return v
}

// mysqlExplainPrefix MySQL: traditional tabular EXPLAIN (works for both SELECT and DML;
// carries no execution options like ANALYZE; JSON format has uneven support for DML targets, so v1 standardizes on tabular format).
func mysqlExplainPrefix() string { return "EXPLAIN " }

// isMySQLUnknownSystemVariable reports whether err is MySQL error 1193 (ER_UNKNOWN_SYSTEM_VARIABLE),
// which occurs on MySQL-compatible engines (such as OceanBase) when querying or setting unsupported
// MySQL session/system variables like @@server_uuid, max_execution_time, or innodb_lock_wait_timeout.
func isMySQLUnknownSystemVariable(err error) bool {
	if err == nil {
		return false
	}
	var merr *mysql.MySQLError
	if errors.As(err, &merr) && merr.Number == 1193 {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "unknown system variable")
}

// parseMySQLExplain parses EXPLAIN output across MySQL-compatible targets:
//   - Native MySQL / PolarDB MySQL tabular output (type/key/rows/Extra columns)
//   - OceanBase MySQL mode Query Plan text table (|ID|OPERATOR|NAME|EST.ROWS|EST.TIME(us)| or EST. ROWS|COST)
//   - TiDB MySQL mode tabular output (id/estRows/task/access object/operator info)
func parseMySQLExplain(columns []string, rows [][]string) (*ExplainFact, error) {
	if len(rows) == 0 {
		return nil, errors.New("database: explain output empty")
	}
	var canonical strings.Builder
	for _, row := range rows {
		canonical.WriteString(strings.Join(row, "\x1f"))
		canonical.WriteByte('\n')
	}
	raw := []byte(canonical.String())
	fact := &ExplainFact{
		RawEvidenceReference: fmt.Sprintf("sha256:%s:%d", hexSHA256(raw), len(raw)),
	}
	idxOf := func(name string) int {
		for i, c := range columns {
			if strings.EqualFold(strings.TrimSpace(c), name) {
				return i
			}
		}
		return -1
	}
	cellAt := func(row []string, idx int) string {
		if idx >= 0 && idx < len(row) {
			return strings.TrimSpace(row[idx])
		}
		return ""
	}
	seenIndex := map[string]struct{}{}
	appendIndex := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || strings.EqualFold(name, "NULL") {
			return
		}
		if _, exists := seenIndex[name]; exists {
			return
		}
		seenIndex[name] = struct{}{}
		fact.IndexNames = append(fact.IndexNames, name)
	}

	// 1. Native MySQL / PolarDB MySQL tabular EXPLAIN (type / key / rows).
	typeIdx := idxOf("type")
	keyIdx := idxOf("key")
	rowsIdx := idxOf("rows")
	if rowsIdx >= 0 || typeIdx >= 0 {
		for _, row := range rows {
			if t := cellAt(row, typeIdx); t != "" && !strings.EqualFold(t, "NULL") && fact.AccessMethod == "" {
				fact.AccessMethod = t
			}
			if k := cellAt(row, keyIdx); k != "" && !strings.EqualFold(k, "NULL") {
				for _, part := range strings.Split(k, ",") {
					appendIndex(part)
				}
			}
			if n, ok := parseEstimatedRowCount(cellAt(row, rowsIdx)); ok && n > fact.EstimatedRows {
				fact.EstimatedRows = n
			}
		}
		return fact, nil
	}

	// 2. TiDB MySQL mode tabular EXPLAIN (id / estRows / task / access object / operator info).
	if estRowsIdx := idxOf("estRows"); estRowsIdx >= 0 {
		idIdx := idxOf("id")
		accessObjIdx := idxOf("access object")
		var firstOp, scanOp string
		for _, row := range rows {
			if n, ok := parseEstimatedRowCount(cellAt(row, estRowsIdx)); ok && n > fact.EstimatedRows {
				fact.EstimatedRows = n
			}
			if op := normalizeTiDBOperatorName(cellAt(row, idIdx)); op != "" {
				if firstOp == "" {
					firstOp = op
				}
				upperOp := strings.ToUpper(op)
				if scanOp == "" && (strings.Contains(upperOp, "SCAN") || strings.Contains(upperOp, "GET") || strings.Contains(upperOp, "LOOKUP")) {
					scanOp = op
				}
			}
			for _, idxName := range extractTiDBAccessObjectIndexes(cellAt(row, accessObjIdx)) {
				appendIndex(idxName)
			}
		}
		if scanOp != "" {
			fact.AccessMethod = scanOp
		} else {
			fact.AccessMethod = firstOp
		}
		return fact, nil
	}

	// 3. OceanBase MySQL mode EXPLAIN (single "Query Plan" column or multi-line ASCII operator table).
	parseOceanBaseExplain(rows, fact, appendIndex)
	return fact, nil
}

func parseEstimatedRowCount(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "NULL") {
		return 0, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return n, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 {
		return int64(math.Round(f)), true
	}
	return 0, false
}

func stripPlanTreePrefix(s string) string {
	return strings.TrimSpace(strings.TrimLeftFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '|', '│', '├', '└', '─', '-':
			return true
		default:
			return false
		}
	}))
}

func normalizeTiDBOperatorName(raw string) string {
	cleaned := stripPlanTreePrefix(raw)
	if cleaned == "" {
		return ""
	}
	if paren := strings.IndexByte(cleaned, '('); paren > 0 {
		cleaned = strings.TrimSpace(cleaned[:paren])
	}
	if idx := strings.LastIndexByte(cleaned, '_'); idx > 0 {
		suffix := cleaned[idx+1:]
		allDigits := len(suffix) > 0
		for _, r := range suffix {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			cleaned = cleaned[:idx]
		}
	}
	return cleaned
}

func extractTiDBAccessObjectIndexes(accessObj string) []string {
	if accessObj == "" {
		return nil
	}
	var out []string
	lower := strings.ToLower(accessObj)
	searchFrom := 0
	for searchFrom < len(lower) {
		pos := strings.Index(lower[searchFrom:], "index:")
		if pos < 0 {
			break
		}
		start := searchFrom + pos + len("index:")
		end := start
		for end < len(accessObj) {
			ch := accessObj[end]
			if ch == '(' || ch == ',' || ch == ';' || ch == ' ' || ch == '\t' {
				break
			}
			end++
		}
		if name := strings.TrimSpace(accessObj[start:end]); name != "" {
			out = append(out, name)
		}
		searchFrom = end + 1
	}
	return out
}

func parseOceanBaseExplain(rows [][]string, fact *ExplainFact, appendIndex func(string)) {
	var lines []string
	for _, row := range rows {
		for _, cell := range row {
			for _, line := range strings.Split(cell, "\n") {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
	}
	headerIdx := -1
	opCol, nameCol, rowsCol, costCol := -1, -1, -1, -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		parts := splitPipeTableLine(line)
		foundOp := false
		for cIdx, p := range parts {
			upper := strings.ToUpper(strings.TrimSpace(p))
			switch upper {
			case "OPERATOR":
				opCol = cIdx
				foundOp = true
			case "NAME":
				nameCol = cIdx
			case "EST.ROWS", "EST. ROWS", "ROWS":
				rowsCol = cIdx
			case "COST", "EST. COST":
				costCol = cIdx
			}
		}
		if foundOp {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return
	}
	var firstOp, scanOp string
	seenDataRow := false
	for _, line := range lines[headerIdx+1:] {
		if line == "" {
			continue
		}
		if isOceanBaseSeparatorLine(line) {
			if seenDataRow && strings.HasPrefix(line, "=") {
				break
			}
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		parts := splitPipeTableLine(line)
		seenDataRow = true
		if opCol >= 0 && opCol < len(parts) {
			op := stripPlanTreePrefix(parts[opCol])
			if op != "" {
				if firstOp == "" {
					firstOp = op
				}
				upperOp := strings.ToUpper(op)
				if scanOp == "" && (strings.Contains(upperOp, "SCAN") || strings.Contains(upperOp, "GET") || strings.Contains(upperOp, "LOOKUP")) {
					scanOp = op
				}
			}
		}
		if rowsCol >= 0 && rowsCol < len(parts) {
			if n, ok := parseEstimatedRowCount(parts[rowsCol]); ok && n > fact.EstimatedRows {
				fact.EstimatedRows = n
			}
		}
		if costCol >= 0 && costCol < len(parts) && fact.EstimatedCost == 0 {
			if c, err := strconv.ParseFloat(strings.TrimSpace(parts[costCol]), 64); err == nil && c > 0 {
				fact.EstimatedCost = c
			}
		}
		if nameCol >= 0 && nameCol < len(parts) {
			if idxName := extractOceanBaseIndexName(parts[nameCol]); idxName != "" {
				appendIndex(idxName)
			}
		}
	}
	if scanOp != "" {
		fact.AccessMethod = scanOp
	} else {
		fact.AccessMethod = firstOp
	}
}

func splitPipeTableLine(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	return strings.Split(trimmed, "|")
}

func isOceanBaseSeparatorLine(line string) bool {
	for _, r := range line {
		if r != '=' && r != '-' && r != '+' && r != ' ' {
			return false
		}
	}
	return len(line) > 0
}

// extractOceanBaseIndexName extracts the index name from an OceanBase NAME column cell
// formatted as "table_name(index_name)" or "table_name(index_name,Reverse)".
// Bare table names without parentheses return "" so table identifiers are never added to IndexNames.
func extractOceanBaseIndexName(nameCell string) string {
	nameCell = strings.TrimSpace(nameCell)
	open := strings.IndexByte(nameCell, '(')
	close := strings.LastIndexByte(nameCell, ')')
	if open <= 0 || close <= open+1 {
		return ""
	}
	inside := strings.TrimSpace(nameCell[open+1 : close])
	if comma := strings.IndexByte(inside, ','); comma >= 0 {
		inside = strings.TrimSpace(inside[:comma])
	}
	if inside == "" || strings.ContainsAny(inside, " \t()") {
		return ""
	}
	return inside
}
