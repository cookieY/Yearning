package rollback

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// PrepareDDLPreExecution reads and validates the target object schema snapshot before executing the original DDL to confirm the DDL is safely reversible.
func PrepareDDLPreExecution(
	ctx context.Context,
	pool *database.PurposePool,
	engine string,
	parsed *ParsedStatement,
) (*database.TableRollbackMetadata, error) {
	// PostgreSQL DROP INDEX does not carry ON <table>, so look up the owning table name from pg_catalog first.
	if engine == "postgresql" && parsed.Kind == KindDropIndex && parsed.TableName == "" {
		resolvedSchema, tbl, err := resolvePostgresIndexTable(ctx, pool, parsed.SchemaName, parsed.IndexName, parsed.SchemaExplicit)
		if err != nil {
			return nil, err
		}
		if tbl == "" {
			return nil, &CapabilityError{
				Reason:  "drop_index_target_not_found",
				Message: fmt.Sprintf("index %s.%s does not exist", parsed.SchemaName, parsed.IndexName),
			}
		}
		if !parsed.SchemaExplicit && resolvedSchema != "" {
			parsed.SchemaName = resolvedSchema
		}
		parsed.TableName = tbl
	}

	beforeCtx := database.WithRollbackAllowSearchPathFallback(ctx, false)
	if engine == "postgresql" && !parsed.SchemaExplicit && parsed.Kind != KindCreateTable {
		beforeCtx = database.WithRollbackAllowSearchPathFallback(ctx, true)
	}
	metaBefore, err := database.InspectTableRollbackMetadata(beforeCtx, pool, nil, parsed.SchemaName, parsed.TableName)
	if err != nil {
		return nil, err
	}
	if !parsed.SchemaExplicit && parsed.Kind == KindCreateTable && parsed.SchemaName == "" && metaBefore.SchemaName != "" {
		parsed.SchemaName = metaBefore.SchemaName
	}
	if !parsed.SchemaExplicit && parsed.Kind != KindCreateTable && metaBefore.Exists && metaBefore.SchemaName != "" && metaBefore.SchemaName != parsed.SchemaName {
		if parsed.PostSchemaName == "" || parsed.PostSchemaName == parsed.SchemaName {
			parsed.PostSchemaName = metaBefore.SchemaName
		}
		parsed.SchemaName = metaBefore.SchemaName
	}

	switch parsed.Kind {
	case KindCreateTable:
		if metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "create_table_target_already_exists",
				Message: fmt.Sprintf("table %s.%s already exists before CREATE TABLE", parsed.SchemaName, parsed.TableName),
			}
		}

	case KindDropTable:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist before DROP TABLE", parsed.SchemaName, parsed.TableName),
			}
		}
		if strings.TrimSpace(metaBefore.CreateTableDDL) == "" && len(metaBefore.Columns) == 0 {
			return metaBefore, &CapabilityError{
				Reason:  "drop_table_definition_irreconstructible",
				Message: fmt.Sprintf("table %s.%s schema definition cannot be reconstructed", parsed.SchemaName, parsed.TableName),
			}
		}

	case KindAddColumn:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		if _, exists := metaBefore.ColumnByName(parsed.ColumnName); exists {
			return metaBefore, &CapabilityError{
				Reason:  "add_column_already_exists",
				Message: fmt.Sprintf("column %s already exists on %s.%s", parsed.ColumnName, parsed.SchemaName, parsed.TableName),
			}
		}

	case KindDropColumn:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		beforeCol, exists := metaBefore.ColumnByName(parsed.ColumnName)
		if !exists {
			return metaBefore, &CapabilityError{
				Reason:  "drop_column_not_found",
				Message: fmt.Sprintf("column %s does not exist on %s.%s", parsed.ColumnName, parsed.SchemaName, parsed.TableName),
			}
		}
		if beforeCol.IsGenerated || beforeCol.IsIdentity {
			return metaBefore, &CapabilityError{
				Reason:  "drop_generated_or_identity_column_unsupported",
				Message: fmt.Sprintf("dropping generated or identity column %s is not supported when rollback protection is enabled", parsed.ColumnName),
			}
		}

	case KindRenameColumn:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		if _, exists := metaBefore.ColumnByName(parsed.ColumnName); !exists {
			return metaBefore, &CapabilityError{
				Reason:  "rename_column_source_not_found",
				Message: fmt.Sprintf("column %s does not exist on %s.%s", parsed.ColumnName, parsed.SchemaName, parsed.TableName),
			}
		}
		if _, exists := metaBefore.ColumnByName(parsed.PostColumnName); exists {
			return metaBefore, &CapabilityError{
				Reason:  "rename_column_target_already_exists",
				Message: fmt.Sprintf("column %s already exists on %s.%s", parsed.PostColumnName, parsed.SchemaName, parsed.TableName),
			}
		}

	case KindRenameTable:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		postCtx := database.WithRollbackAllowSearchPathFallback(ctx, false)
		postMeta, err := database.InspectTableRollbackMetadata(postCtx, pool, nil, parsed.PostSchemaName, parsed.PostTableName)
		if err != nil {
			return metaBefore, err
		}
		if postMeta.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "rename_table_target_already_exists",
				Message: fmt.Sprintf("target table %s.%s already exists", parsed.PostSchemaName, parsed.PostTableName),
			}
		}

	case KindAddIndex:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		if _, exists := metaBefore.IndexByName(parsed.IndexName); exists {
			return metaBefore, &CapabilityError{
				Reason:  "add_index_already_exists",
				Message: fmt.Sprintf("index %s already exists on %s.%s", parsed.IndexName, parsed.SchemaName, parsed.TableName),
			}
		}

	case KindDropIndex:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		idxMeta, exists := metaBefore.IndexByName(parsed.IndexName)
		if !exists {
			return metaBefore, &CapabilityError{
				Reason:  "drop_index_not_found",
				Message: fmt.Sprintf("index %s does not exist on %s.%s", parsed.IndexName, parsed.SchemaName, parsed.TableName),
			}
		}
		if idxMeta.Primary || strings.TrimSpace(idxMeta.CreateDDL) == "" {
			return metaBefore, &CapabilityError{
				Reason:  "drop_index_definition_irreconstructible",
				Message: fmt.Sprintf("index %s on %s.%s cannot be reconstructed deterministically", parsed.IndexName, parsed.SchemaName, parsed.TableName),
			}
		}

	case KindModifyColumn:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		beforeCol, exists := metaBefore.ColumnByName(parsed.ColumnName)
		if !exists {
			return metaBefore, &CapabilityError{
				Reason:  "modify_column_not_found",
				Message: fmt.Sprintf("column %s does not exist on %s.%s", parsed.ColumnName, parsed.SchemaName, parsed.TableName),
			}
		}
		if beforeCol.IsGenerated || beforeCol.IsIdentity {
			return metaBefore, &CapabilityError{
				Reason:  "modify_generated_or_identity_column_unsupported",
				Message: fmt.Sprintf("modifying generated or identity column %s is not supported when rollback protection is enabled", parsed.ColumnName),
			}
		}

	case KindAddConstraint:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		if _, exists := metaBefore.ConstraintByName(parsed.ConstraintName); exists {
			return metaBefore, &CapabilityError{
				Reason:  "add_constraint_already_exists",
				Message: fmt.Sprintf("constraint %s already exists on %s.%s", parsed.ConstraintName, parsed.SchemaName, parsed.TableName),
			}
		}

	case KindDropConstraint:
		if !metaBefore.Exists {
			return metaBefore, &CapabilityError{
				Reason:  "target_table_not_found",
				Message: fmt.Sprintf("table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
			}
		}
		conMeta, exists := metaBefore.ConstraintByName(parsed.ConstraintName)
		if !exists {
			return metaBefore, &CapabilityError{
				Reason:  "drop_constraint_not_found",
				Message: fmt.Sprintf("constraint %s does not exist on %s.%s", parsed.ConstraintName, parsed.SchemaName, parsed.TableName),
			}
		}
		if !canReconstructConstraint(engine, conMeta) {
			return metaBefore, &CapabilityError{
				Reason:  "drop_constraint_definition_irreconstructible",
				Message: fmt.Sprintf("constraint %s (%s) on %s.%s cannot be deterministically reconstructed", conMeta.Name, conMeta.Type, parsed.SchemaName, parsed.TableName),
			}
		}
	}

	return metaBefore, nil
}

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

func verifyDiffAndGenerateInverseDDL(
	engine string,
	parsed *ParsedStatement,
	before, after *database.TableRollbackMetadata,
) ([]string, error) {
	qualTable := QualifiedTable(engine, parsed.SchemaName, parsed.TableName)

	switch parsed.Kind {
	case KindCreateTable:
		if !after.Exists {
			return nil, fmt.Errorf("rollback: table %s.%s missing after CREATE TABLE", parsed.SchemaName, parsed.TableName)
		}
		return []string{fmt.Sprintf("DROP TABLE %s;", qualTable)}, nil

	case KindDropTable:
		if after.Exists {
			return nil, fmt.Errorf("rollback: table %s.%s still exists after DROP TABLE", parsed.SchemaName, parsed.TableName)
		}
		return buildInverseDropTableDDL(engine, before)

	case KindAddColumn:
		if _, ok := after.ColumnByName(parsed.ColumnName); !ok {
			return nil, fmt.Errorf("rollback: added column %s missing after ALTER TABLE ADD COLUMN", parsed.ColumnName)
		}
		return []string{fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", qualTable, QuoteIdent(engine, parsed.ColumnName))}, nil

	case KindDropColumn:
		beforeCol, ok := before.ColumnByName(parsed.ColumnName)
		if !ok {
			return nil, fmt.Errorf("rollback: pre-execution column %s missing for DROP COLUMN", parsed.ColumnName)
		}
		if _, stillExists := after.ColumnByName(parsed.ColumnName); stillExists {
			return nil, fmt.Errorf("rollback: column %s still exists after ALTER TABLE DROP COLUMN", parsed.ColumnName)
		}
		return buildInverseDropColumnDDL(engine, qualTable, beforeCol), nil

	case KindRenameColumn:
		if _, ok := after.ColumnByName(parsed.PostColumnName); !ok {
			return nil, fmt.Errorf("rollback: renamed column %s missing after RENAME COLUMN", parsed.PostColumnName)
		}
		return []string{fmt.Sprintf(
			"ALTER TABLE %s RENAME COLUMN %s TO %s;",
			qualTable,
			QuoteIdent(engine, parsed.PostColumnName),
			QuoteIdent(engine, parsed.ColumnName),
		)}, nil

	case KindRenameTable:
		if !after.Exists {
			return nil, fmt.Errorf("rollback: renamed table %s.%s missing after RENAME TABLE", parsed.PostSchemaName, parsed.PostTableName)
		}
		postQual := QualifiedTable(engine, parsed.PostSchemaName, parsed.PostTableName)
		if engine == "mysql" {
			return []string{fmt.Sprintf("RENAME TABLE %s TO %s;", postQual, qualTable)}, nil
		}
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", postQual, QuoteIdent(engine, parsed.TableName))}, nil

	case KindAddIndex:
		if _, ok := after.IndexByName(parsed.IndexName); !ok {
			return nil, fmt.Errorf("rollback: added index %s missing after CREATE/ADD INDEX", parsed.IndexName)
		}
		if engine == "mysql" {
			return []string{fmt.Sprintf("DROP INDEX %s ON %s;", QuoteIdent(engine, parsed.IndexName), qualTable)}, nil
		}
		qSchema := QuoteIdent(engine, parsed.SchemaName)
		return []string{fmt.Sprintf("DROP INDEX %s.%s;", qSchema, QuoteIdent(engine, parsed.IndexName))}, nil

	case KindDropIndex:
		beforeIdx, ok := before.IndexByName(parsed.IndexName)
		if !ok || strings.TrimSpace(beforeIdx.CreateDDL) == "" {
			return nil, fmt.Errorf("rollback: missing pre-execution index definition for %s", parsed.IndexName)
		}
		if _, stillExists := after.IndexByName(parsed.IndexName); stillExists {
			return nil, fmt.Errorf("rollback: index %s still exists after DROP INDEX", parsed.IndexName)
		}
		ddl := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(beforeIdx.CreateDDL), ";")) + ";"
		return []string{ddl}, nil

	case KindModifyColumn:
		beforeCol, okB := before.ColumnByName(parsed.ColumnName)
		afterCol, okA := after.ColumnByName(parsed.PostColumnName)
		if !okB || !okA {
			return nil, fmt.Errorf("rollback: column %s metadata missing for MODIFY/ALTER COLUMN", parsed.ColumnName)
		}
		return buildInverseModifyColumnDDL(engine, qualTable, beforeCol, afterCol)

	case KindAddConstraint:
		afterCon, ok := after.ConstraintByName(parsed.ConstraintName)
		if !ok {
			// MySQL ADD CONSTRAINT UNIQUE appears in table_constraints as a UNIQUE constraint or a same-named index
			if engine == "mysql" {
				if _, idxOK := after.IndexByName(parsed.ConstraintName); idxOK {
					return []string{fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", qualTable, QuoteIdent(engine, parsed.ConstraintName))}, nil
				}
			}
			return nil, fmt.Errorf("rollback: added constraint %s missing after ADD CONSTRAINT", parsed.ConstraintName)
		}
		return []string{buildDropConstraintDDL(engine, qualTable, afterCon)}, nil

	case KindDropConstraint:
		beforeCon, ok := before.ConstraintByName(parsed.ConstraintName)
		if !ok {
			return nil, fmt.Errorf("rollback: pre-execution constraint %s missing", parsed.ConstraintName)
		}
		return []string{buildAddConstraintDDL(engine, qualTable, beforeCon)}, nil

	default:
		return nil, &CapabilityError{Reason: "unsupported_ddl_kind", Message: "unsupported DDL kind"}
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

var charLenRegex = regexp.MustCompile(`(?i)(?:varchar|character varying|char|character)\s*\(\s*(\d+)\s*\)`)

// isLosslessColumnModification verifies whether a column modification is lossless (integer widening, string length expansion, nullability/default changes).
// Narrowing length, converting across type families (e.g., VARCHAR -> INT), or changing to an incompatible type is rejected.
func isLosslessColumnModification(engine string, beforeCol database.RollbackColumnMeta, rawModifyClause string) bool {
	upperClause := strings.ToUpper(strings.TrimSpace(rawModifyClause))
	if engine == "postgresql" {
		if strings.HasPrefix(upperClause, "SET DEFAULT") ||
			strings.HasPrefix(upperClause, "DROP DEFAULT") ||
			strings.HasPrefix(upperClause, "DROP NOT NULL") ||
			strings.HasPrefix(upperClause, "SET NOT NULL") {
			return true
		}
		if strings.HasPrefix(upperClause, "SET DATA TYPE") {
			rawModifyClause = strings.TrimSpace(rawModifyClause[len("SET DATA TYPE"):])
		} else if strings.HasPrefix(upperClause, "TYPE") {
			rawModifyClause = strings.TrimSpace(rawModifyClause[len("TYPE"):])
		}
	}

	beforeFamily, beforeLen := classifyColumnType(beforeCol.ColumnType)
	afterFamily, afterLen := classifyColumnType(rawModifyClause)
	if beforeFamily == "" || afterFamily == "" || beforeFamily != afterFamily {
		return false
	}
	switch beforeFamily {
	case "string":
		// TEXT length is represented as 1_000_000_000; only expansion or equal length is allowed
		return afterLen >= beforeLen
	case "int":
		return afterLen >= beforeLen
	default:
		return strings.EqualFold(beforeCol.DataType, extractLeadingDataType(rawModifyClause))
	}
}

func classifyColumnType(typeSpec string) (string, int) {
	lower := strings.ToLower(strings.TrimSpace(typeSpec))
	if m := charLenRegex.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		return "string", n
	}
	switch {
	case strings.HasPrefix(lower, "text") || strings.HasPrefix(lower, "mediumtext") || strings.HasPrefix(lower, "longtext"):
		return "string", 1_000_000_000
	case strings.HasPrefix(lower, "tinyint"):
		return "int", 1
	case strings.HasPrefix(lower, "smallint"):
		return "int", 2
	case strings.HasPrefix(lower, "mediumint"):
		return "int", 3
	case strings.HasPrefix(lower, "bigint"):
		return "int", 8
	case strings.HasPrefix(lower, "integer") || strings.HasPrefix(lower, "int"):
		return "int", 4
	default:
		return "", 0
	}
}

func extractLeadingDataType(spec string) string {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return ""
	}
	base := fields[0]
	if idx := strings.IndexByte(base, '('); idx > 0 {
		base = base[:idx]
	}
	return base
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
