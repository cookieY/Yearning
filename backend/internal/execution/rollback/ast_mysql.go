package rollback

import (
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
	"strings"
)

type mysqlExprSafetyVisitor struct {
	subqueryReason string
	err            *CapabilityError
}

func analyzeMySQLAlterTableAST(defaultSchema, rawSQL, trimmed string, stmt *ast.AlterTableStmt) (*ParsedStatement, error) {
	if stmt.Table == nil || stmt.Table.Name.O == "" {
		return nil, &CapabilityError{Reason: "invalid_alter_table_syntax", Message: "missing table name in ALTER TABLE"}
	}
	if len(stmt.Specs) == 0 {
		return nil, &CapabilityError{Reason: "invalid_alter_table_syntax", Message: "incomplete ALTER TABLE statement"}
	}
	if len(stmt.Specs) > 1 {
		return nil, &CapabilityError{
			Reason:  "multi_action_alter_table_unsupported",
			Message: "multi-action ALTER TABLE (comma-separated actions) must be split into individual statements when rollback protection is enabled",
		}
	}

	schema := defaultSchema
	if stmt.Table.Schema.O != "" {
		schema = stmt.Table.Schema.O
	}
	tableName := stmt.Table.Name.O
	spec := stmt.Specs[0]

	switch spec.Tp {
	case ast.AlterTableDropColumn:
		if spec.OldColumnName == nil || spec.OldColumnName.Name.O == "" {
			return nil, &CapabilityError{
				Reason:  "invalid_drop_column_syntax",
				Message: "missing column name in ALTER TABLE DROP COLUMN",
			}
		}
		return &ParsedStatement{
			RawSQL:        rawSQL,
			TrimmedSQL:    trimmed,
			OperationType: OperationTypeDDL,
			Kind:          KindDropColumn,
			SchemaName:    schema,
			TableName:     tableName,
			ColumnName:    spec.OldColumnName.Name.O,
		}, nil
	case ast.AlterTableAddColumns:
		if len(spec.NewColumns) != 1 || spec.NewColumns[0].Name == nil {
			return nil, &CapabilityError{
				Reason:  "invalid_add_column_syntax",
				Message: "ALTER TABLE ADD COLUMN requires a single column name and definition",
			}
		}
		colDef, err := restoreMySQLColumnDefWithoutName(spec.NewColumns[0])
		if err != nil {
			return nil, err
		}
		return &ParsedStatement{
			RawSQL:        rawSQL,
			TrimmedSQL:    trimmed,
			OperationType: OperationTypeDDL,
			Kind:          KindAddColumn,
			SchemaName:    schema,
			TableName:     tableName,
			ColumnName:    spec.NewColumns[0].Name.Name.O,
			ColumnDefSQL:  colDef,
		}, nil

	case ast.AlterTableModifyColumn, ast.AlterTableChangeColumn:
		if len(spec.NewColumns) != 1 || spec.NewColumns[0].Name == nil {
			return nil, &CapabilityError{
				Reason:  "invalid_modify_column_syntax",
				Message: "missing column definition in MODIFY/CHANGE COLUMN",
			}
		}
		newCol := spec.NewColumns[0]
		colName := newCol.Name.Name.O
		postColName := colName
		if spec.Tp == ast.AlterTableChangeColumn && spec.OldColumnName != nil {
			colName = spec.OldColumnName.Name.O
			if !strings.EqualFold(colName, postColName) {
				return nil, &CapabilityError{
					Reason:  "mysql_change_column_rename_and_type_combined_unsupported",
					Message: "use RENAME COLUMN or MODIFY COLUMN separately when rollback protection is enabled",
				}
			}
		}
		colDef, err := restoreMySQLColumnDefWithoutName(newCol)
		if err != nil {
			return nil, err
		}
		newTypeSQL := ""
		if newCol.Tp != nil {
			newTypeSQL = newCol.Tp.String()
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindModifyColumn,
			SchemaName:     schema,
			TableName:      tableName,
			ColumnName:     colName,
			PostColumnName: postColName,
			ColumnDefSQL:   colDef,
			NewDataTypeSQL: newTypeSQL,
		}, nil

	case ast.AlterTableRenameColumn:
		if spec.OldColumnName == nil || spec.NewColumnName == nil {
			return nil, &CapabilityError{Reason: "invalid_rename_column_syntax", Message: "expected RENAME COLUMN <old> TO <new>"}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindRenameColumn,
			SchemaName:     schema,
			TableName:      tableName,
			ColumnName:     spec.OldColumnName.Name.O,
			PostColumnName: spec.NewColumnName.Name.O,
		}, nil

	case ast.AlterTableRenameTable:
		if spec.NewTable == nil || spec.NewTable.Name.O == "" {
			return nil, &CapabilityError{Reason: "invalid_rename_table_syntax", Message: "missing new table name in ALTER TABLE RENAME"}
		}
		newSchema := schema
		if spec.NewTable.Schema.O != "" {
			newSchema = spec.NewTable.Schema.O
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindRenameTable,
			SchemaName:     schema,
			TableName:      tableName,
			PostSchemaName: newSchema,
			PostTableName:  spec.NewTable.Name.O,
		}, nil

	case ast.AlterTableDropIndex:
		if spec.Name == "" {
			return nil, &CapabilityError{Reason: "invalid_drop_index", Message: "missing index name in ALTER TABLE DROP INDEX"}
		}
		return &ParsedStatement{
			RawSQL:        rawSQL,
			TrimmedSQL:    trimmed,
			OperationType: OperationTypeDDL,
			Kind:          KindDropIndex,
			SchemaName:    schema,
			TableName:     tableName,
			IndexName:     spec.Name,
		}, nil

	case ast.AlterTableDropForeignKey:
		if spec.Name == "" {
			return nil, &CapabilityError{Reason: "invalid_drop_foreign_key", Message: "expected DROP FOREIGN KEY <name>"}
		}
		return &ParsedStatement{
			RawSQL:             rawSQL,
			TrimmedSQL:         trimmed,
			OperationType:      OperationTypeDDL,
			Kind:               KindDropConstraint,
			SchemaName:         schema,
			TableName:          tableName,
			ConstraintName:     spec.Name,
			ConstraintTypeHint: "FOREIGN KEY",
		}, nil

	case ast.AlterTableDropCheck:
		if spec.Constraint == nil || spec.Constraint.Name == "" {
			return nil, &CapabilityError{Reason: "invalid_drop_check", Message: "expected DROP CHECK <name>"}
		}
		return &ParsedStatement{
			RawSQL:             rawSQL,
			TrimmedSQL:         trimmed,
			OperationType:      OperationTypeDDL,
			Kind:               KindDropConstraint,
			SchemaName:         schema,
			TableName:          tableName,
			ConstraintName:     spec.Constraint.Name,
			ConstraintTypeHint: "CHECK",
		}, nil

	case ast.AlterTableAddConstraint:
		return analyzeMySQLAlterAddConstraintAST(schema, tableName, rawSQL, trimmed, spec.Constraint)

	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_alter_table_action",
			Message: "ALTER TABLE action is not supported when rollback protection is enabled",
		}
	}
}

func hasMySQLConditionalComment(s string) bool {
	inSingle := false
	inDouble := false
	inBacktick := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingle {
			if c == '\\' {
				i++
				continue
			}
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '\\' {
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		}
		if inBacktick {
			if c == '`' {
				inBacktick = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inBacktick = true
		case '-':
			if i+1 < len(s) && s[i+1] == '-' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
			}
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '/':
			if i+2 < len(s) && s[i+1] == '*' && s[i+2] == '!' {
				return true
			}
		}
	}
	return false
}
