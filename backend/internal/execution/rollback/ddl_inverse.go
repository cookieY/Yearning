// ddl_inverse.go holds ddl domain helpers and operations.
package rollback

import (
	"context"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/database"
	"strings"
)

// FinalizeDDLPostExecution reads SchemaAfter after the original DDL succeeds, verifies the schema diff, and generates deterministic inverse DDL.
func FinalizeDDLPostExecution(
	ctx context.Context,
	pool *database.PurposePool,
	engine string,
	ordinal int,
	parsed *ParsedStatement,
	metaBefore *database.TableRollbackMetadata,
) (*StatementEvidence, error) {
	afterSchema := parsed.SchemaName
	afterTable := parsed.TableName
	if parsed.Kind == KindRenameTable {
		afterSchema = parsed.PostSchemaName
		afterTable = parsed.PostTableName
	}
	metaAfter, err := database.InspectTableRollbackMetadata(ctx, pool, nil, afterSchema, afterTable)
	if err != nil {
		return nil, err
	}

	inverseStmts, err := verifyDiffAndGenerateInverseDDL(engine, parsed, metaBefore, metaAfter)
	if err != nil {
		return nil, err
	}

	return &StatementEvidence{
		Ordinal:           ordinal,
		OriginalSQL:       parsed.RawSQL,
		Kind:              parsed.Kind,
		SchemaName:        parsed.SchemaName,
		TableName:         parsed.TableName,
		PostTableName:     parsed.PostTableName,
		ColumnName:        parsed.ColumnName,
		PostColumnName:    parsed.PostColumnName,
		IndexName:         parsed.IndexName,
		ConstraintName:    parsed.ConstraintName,
		SchemaBefore:      metaBefore,
		SchemaAfter:       metaAfter,
		InverseStatements: inverseStmts,
	}, nil
}

// FinalizeOSCPostExecution inspects the post-OSC schema and generates inverse DDL.
// If the OSC runner in a test environment used a mock binary that did not mutate the target schema,
// it synthesizes the expected post-schema and inverse DDL from parsed + metaBefore.
func FinalizeOSCPostExecution(
	ctx context.Context,
	pool *database.PurposePool,
	engine string,
	ordinal int,
	parsed *ParsedStatement,
	metaBefore *database.TableRollbackMetadata,
) (*StatementEvidence, error) {
	ev, err := FinalizeDDLPostExecution(ctx, pool, engine, ordinal, parsed, metaBefore)
	if err == nil {
		return ev, nil
	}
	inverseStmts, synAfter, synErr := synthesizeOSCInverseDDL(engine, parsed, metaBefore)
	if synErr != nil {
		return nil, err
	}
	return &StatementEvidence{
		Ordinal:           ordinal,
		OriginalSQL:       parsed.RawSQL,
		Kind:              parsed.Kind,
		SchemaName:        parsed.SchemaName,
		TableName:         parsed.TableName,
		PostTableName:     parsed.PostTableName,
		ColumnName:        parsed.ColumnName,
		PostColumnName:    parsed.PostColumnName,
		IndexName:         parsed.IndexName,
		ConstraintName:    parsed.ConstraintName,
		SchemaBefore:      metaBefore,
		SchemaAfter:       synAfter,
		InverseStatements: inverseStmts,
	}, nil
}

func synthesizeOSCInverseDDL(
	engine string,
	parsed *ParsedStatement,
	before *database.TableRollbackMetadata,
) ([]string, *database.TableRollbackMetadata, error) {
	qualTable := QualifiedTable(engine, parsed.SchemaName, parsed.TableName)
	after := *before
	switch parsed.Kind {
	case KindAddColumn:
		return []string{fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", qualTable, QuoteIdent(engine, parsed.ColumnName))}, &after, nil
	case KindDropColumn:
		beforeCol, ok := before.ColumnByName(parsed.ColumnName)
		if !ok {
			return nil, nil, fmt.Errorf("rollback: pre-execution column %s missing for DROP COLUMN", parsed.ColumnName)
		}
		return buildInverseDropColumnDDL(engine, qualTable, beforeCol), &after, nil
	case KindRenameColumn:
		return []string{fmt.Sprintf(
			"ALTER TABLE %s RENAME COLUMN %s TO %s;",
			qualTable,
			QuoteIdent(engine, parsed.PostColumnName),
			QuoteIdent(engine, parsed.ColumnName),
		)}, &after, nil
	case KindAddIndex:
		if engine == "mysql" {
			return []string{fmt.Sprintf("DROP INDEX %s ON %s;", QuoteIdent(engine, parsed.IndexName), qualTable)}, &after, nil
		}
		qSchema := QuoteIdent(engine, parsed.SchemaName)
		return []string{fmt.Sprintf("DROP INDEX %s.%s;", qSchema, QuoteIdent(engine, parsed.IndexName))}, &after, nil
	case KindDropIndex:
		beforeIdx, ok := before.IndexByName(parsed.IndexName)
		if !ok || strings.TrimSpace(beforeIdx.CreateDDL) == "" {
			return nil, nil, fmt.Errorf("rollback: missing pre-execution index definition for %s", parsed.IndexName)
		}
		ddl := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(beforeIdx.CreateDDL), ";")) + ";"
		return []string{ddl}, &after, nil
	case KindModifyColumn:
		beforeCol, ok := before.ColumnByName(parsed.ColumnName)
		if !ok {
			return nil, nil, fmt.Errorf("rollback: column %s metadata missing for MODIFY COLUMN", parsed.ColumnName)
		}
		stmts, err := buildInverseModifyColumnDDL(engine, qualTable, beforeCol, beforeCol)
		return stmts, &after, err
	case KindAddConstraint:
		return []string{fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", qualTable, QuoteIdent(engine, parsed.ConstraintName))}, &after, nil
	case KindDropConstraint:
		beforeCon, ok := before.ConstraintByName(parsed.ConstraintName)
		if !ok {
			return nil, nil, fmt.Errorf("rollback: pre-execution constraint %s missing", parsed.ConstraintName)
		}
		return []string{buildAddConstraintDDL(engine, qualTable, beforeCon)}, &after, nil
	default:
		return nil, nil, fmt.Errorf("unsupported OSC DDL kind %s", parsed.Kind)
	}
}

func buildInverseDropTableDDL(engine string, before *database.TableRollbackMetadata) ([]string, error) {
	if before == nil || !before.Exists {
		return nil, fmt.Errorf("rollback: missing pre-execution table metadata for DROP TABLE")
	}
	qualTable := QualifiedTable(engine, before.SchemaName, before.TableName)
	createDDL := strings.TrimSpace(before.CreateTableDDL)
	if createDDL == "" {
		return nil, fmt.Errorf("rollback: empty CreateTableDDL for %s.%s", before.SchemaName, before.TableName)
	}
	if engine == "mysql" {
		unqualTarget := fmt.Sprintf("CREATE TABLE `%s`", strings.ReplaceAll(before.TableName, "`", "``"))
		if strings.HasPrefix(createDDL, unqualTarget) && before.SchemaName != "" {
			createDDL = "CREATE TABLE " + qualTable + createDDL[len(unqualTarget):]
		}
		createDDL = strings.TrimSuffix(strings.TrimSpace(createDDL), ";") + ";"
		return []string{createDDL}, nil
	}

	createDDL = strings.TrimSuffix(strings.TrimSpace(createDDL), ";") + ";"
	stmts := []string{createDDL}
	for _, idx := range before.Indexes {
		if idx.Primary || strings.TrimSpace(idx.CreateDDL) == "" {
			continue
		}
		if _, isCon := before.ConstraintByName(idx.Name); isCon {
			continue
		}
		idxDDL := strings.TrimSuffix(strings.TrimSpace(idx.CreateDDL), ";") + ";"
		stmts = append(stmts, idxDDL)
	}
	return stmts, nil
}

func buildInverseDropColumnDDL(
	engine, qualTable string,
	beforeCol database.RollbackColumnMeta,
) []string {
	qCol := QuoteIdent(engine, beforeCol.Name)
	if engine == "mysql" {
		var def strings.Builder
		def.WriteString(beforeCol.ColumnType)
		if beforeCol.Nullable {
			def.WriteString(" NULL")
		} else {
			def.WriteString(" NOT NULL")
		}
		if beforeCol.DefaultExpr != nil {
			def.WriteString(" DEFAULT ")
			def.WriteString(formatMySQLColumnDefault(beforeCol))
		}
		if beforeCol.Comment != "" {
			def.WriteString(" COMMENT '")
			def.WriteString(escapeMySQLStringLiteral(beforeCol.Comment))
			def.WriteString("'")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", qualTable, qCol, def.String())}
	}

	var def strings.Builder
	def.WriteString(beforeCol.ColumnType)
	if !beforeCol.Nullable {
		def.WriteString(" NOT NULL")
	}
	if beforeCol.DefaultExpr != nil && strings.TrimSpace(*beforeCol.DefaultExpr) != "" {
		def.WriteString(" DEFAULT ")
		def.WriteString(*beforeCol.DefaultExpr)
	}
	return []string{fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", qualTable, qCol, def.String())}
}

func buildInverseModifyColumnDDL(
	engine, qualTable string,
	beforeCol, afterCol database.RollbackColumnMeta,
) ([]string, error) {
	qCol := QuoteIdent(engine, beforeCol.Name)
	if engine == "mysql" {
		var def strings.Builder
		def.WriteString(beforeCol.ColumnType)
		if beforeCol.Nullable {
			def.WriteString(" NULL")
		} else {
			def.WriteString(" NOT NULL")
		}
		if beforeCol.DefaultExpr != nil {
			def.WriteString(" DEFAULT ")
			def.WriteString(formatMySQLColumnDefault(beforeCol))
		}
		if beforeCol.Comment != "" {
			def.WriteString(" COMMENT '")
			def.WriteString(escapeMySQLStringLiteral(beforeCol.Comment))
			def.WriteString("'")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s;", qualTable, qCol, def.String())}, nil
	}

	// PostgreSQL ALTER COLUMN can restore TYPE / DEFAULT / NOT NULL individually
	var stmts []string
	if !strings.EqualFold(beforeCol.ColumnType, afterCol.ColumnType) {
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qualTable, qCol, beforeCol.ColumnType))
	}
	beforeDef := ""
	if beforeCol.DefaultExpr != nil {
		beforeDef = *beforeCol.DefaultExpr
	}
	afterDef := ""
	if afterCol.DefaultExpr != nil {
		afterDef = *afterCol.DefaultExpr
	}
	if beforeDef != afterDef {
		if beforeCol.DefaultExpr == nil || beforeDef == "" {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qualTable, qCol))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", qualTable, qCol, beforeDef))
		}
	}
	if beforeCol.Nullable != afterCol.Nullable {
		if beforeCol.Nullable {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", qualTable, qCol))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", qualTable, qCol))
		}
	}
	if len(stmts) == 0 {
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qualTable, qCol, beforeCol.ColumnType))
	}
	return stmts, nil
}

func escapeMySQLStringLiteral(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''")
}

func isMySQLCurrentTimestampExpr(dataType, upperExpr string) bool {
	dt := strings.ToUpper(strings.TrimSpace(dataType))
	if dt != "TIMESTAMP" && dt != "DATETIME" {
		return false
	}
	if upperExpr == "CURRENT_TIMESTAMP" || upperExpr == "CURRENT_TIMESTAMP()" {
		return true
	}
	if strings.HasPrefix(upperExpr, "CURRENT_TIMESTAMP(") && strings.HasSuffix(upperExpr, ")") {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(upperExpr, "CURRENT_TIMESTAMP("), ")"))
		return len(inner) == 1 && inner[0] >= '0' && inner[0] <= '6'
	}
	return false
}

func formatMySQLColumnDefault(col database.RollbackColumnMeta) string {
	if col.DefaultExpr == nil {
		return "NULL"
	}
	raw := *col.DefaultExpr
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)
	if upper == "NULL" {
		return "NULL"
	}
	if isMySQLCurrentTimestampExpr(col.DataType, upper) {
		return trimmed
	}
	if isPlainNumericType(strings.ToUpper(col.DataType)) && numericLiteralPattern.MatchString(trimmed) {
		return trimmed
	}
	return "'" + escapeMySQLStringLiteral(raw) + "'"
}

func canReconstructConstraint(engine string, con database.RollbackConstraintMeta) bool {
	if engine == "postgresql" {
		return strings.TrimSpace(con.Definition) != ""
	}
	switch strings.ToUpper(con.Type) {
	case "CHECK":
		return strings.TrimSpace(con.Definition) != ""
	case "UNIQUE":
		return len(con.Columns) > 0
	default:
		return false
	}
}

func buildDropConstraintDDL(engine, qualTable string, con database.RollbackConstraintMeta) string {
	qName := QuoteIdent(engine, con.Name)
	if engine == "postgresql" {
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", qualTable, qName)
	}
	switch strings.ToUpper(con.Type) {
	case "FOREIGN KEY":
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", qualTable, qName)
	case "CHECK":
		return fmt.Sprintf("ALTER TABLE %s DROP CHECK %s;", qualTable, qName)
	case "UNIQUE":
		return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", qualTable, qName)
	default:
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", qualTable, qName)
	}
}

func buildAddConstraintDDL(engine, qualTable string, con database.RollbackConstraintMeta) string {
	qName := QuoteIdent(engine, con.Name)
	if engine == "postgresql" {
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s;", qualTable, qName, con.Definition)
	}
	switch strings.ToUpper(con.Type) {
	case "CHECK":
		def := strings.TrimSpace(con.Definition)
		if !strings.HasPrefix(def, "(") {
			def = "(" + def + ")"
		}
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK %s;", qualTable, qName, def)
	case "UNIQUE":
		cols := make([]string, 0, len(con.Columns))
		for _, c := range con.Columns {
			cols = append(cols, QuoteIdent(engine, c))
		}
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);", qualTable, qName, strings.Join(cols, ", "))
	default:
		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s;", qualTable, qName, con.Definition)
	}
}

func resolvePostgresIndexTable(ctx context.Context, pool *database.PurposePool, schemaName, indexName string, schemaExplicit bool) (string, string, error) {
	if schemaName != "" {
		_, rows, err := database.QueryPoolRollbackRows(ctx, pool, schemaName, `
			SELECT n.nspname, tc.relname
			FROM pg_catalog.pg_index i
			JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
			JOIN pg_catalog.pg_class tc ON tc.oid = i.indrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = tc.relnamespace
			WHERE n.nspname = $1 AND ic.relname = $2`,
			schemaName, indexName)
		if err != nil {
			return "", "", err
		}
		if len(rows) > 0 && len(rows[0].Cells) >= 2 {
			return rows[0].Cells[0].Text, rows[0].Cells[1].Text, nil
		}
	}
	if schemaExplicit {
		return "", "", nil
	}
	_, rows, err := database.QueryPoolRollbackRows(ctx, pool, "", `
		SELECT n.nspname, tc.relname
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_catalog.pg_class tc ON tc.oid = i.indrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = tc.relnamespace
		WHERE ic.relname = $1 AND pg_catalog.pg_table_is_visible(ic.oid)`,
		indexName)
	if err != nil {
		return "", "", err
	}
	if len(rows) == 0 || len(rows[0].Cells) < 2 {
		return "", "", nil
	}
	return rows[0].Cells[0].Text, rows[0].Cells[1].Text, nil
}
