package rollback

import (
	"context"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/database"
	"regexp"
	"strconv"
	"strings"
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
