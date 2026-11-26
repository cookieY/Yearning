// ast_mysql_ddl.go holds ast_mysql domain helpers and operations.
package rollback

import (
	"fmt"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
	"strings"
)

// analyzeMySQLWithAST performs full AST parsing and rollback safety capability analysis using the TiDB MySQL Parser.
func analyzeMySQLWithAST(defaultSchema, rawSQL, trimmed string) (*ParsedStatement, error) {
	if hasMySQLConditionalComment(trimmed) {
		return nil, &CapabilityError{
			Reason:  "mysql_conditional_comment_unsupported",
			Message: "MySQL conditional comments (/*! ... */) are not supported when rollback protection is enabled",
		}
	}

	p := parser.New()
	stmtNodes, _, err := p.ParseSQL(trimmed)
	if err != nil {
		// For non-standard clauses unsupported by the TiDB Parser (such as DML RETURNING), run a lexical scan first to report a precise Reason
		if tokErr := diagnoseMySQLParseFailure(trimmed, err); tokErr != nil {
			return nil, tokErr
		}
		return nil, &CapabilityError{
			Reason:  "mysql_ast_parse_error",
			Message: fmt.Sprintf("MySQL AST parse failed: %v", err),
		}
	}
	if len(stmtNodes) == 0 {
		return nil, &CapabilityError{
			Reason:  "empty_statement",
			Message: "empty SQL statement cannot generate rollback",
		}
	}
	if len(stmtNodes) > 1 {
		return nil, &CapabilityError{
			Reason:  "multi_statement_in_single_unit_unsupported",
			Message: "multiple SQL statements in a single execution unit are not supported",
		}
	}

	stmt := stmtNodes[0]
	switch node := stmt.(type) {
	case *ast.InsertStmt:
		return analyzeMySQLInsertAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.UpdateStmt:
		return analyzeMySQLUpdateAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.DeleteStmt:
		return analyzeMySQLDeleteAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.TruncateTableStmt:
		return nil, &CapabilityError{
			Reason:  "destructive_ddl_truncate_requires_backup",
			Message: "TRUNCATE TABLE destroys table data and cannot be safely rolled back without a full table backup",
		}
	case *ast.CreateTableStmt:
		return analyzeMySQLCreateTableAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.CreateIndexStmt:
		return analyzeMySQLCreateIndexAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.DropTableStmt:
		return analyzeMySQLDropTableAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.DropDatabaseStmt:
		return nil, &CapabilityError{
			Reason:  "destructive_ddl_drop_schema_unsupported",
			Message: "DROP DATABASE/SCHEMA is destructive and cannot be rolled back",
		}
	case *ast.DropIndexStmt:
		return analyzeMySQLDropIndexAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.RenameTableStmt:
		return analyzeMySQLRenameTableAST(defaultSchema, rawSQL, trimmed, node)
	case *ast.AlterTableStmt:
		return analyzeMySQLAlterTableAST(defaultSchema, rawSQL, trimmed, node)
	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_mysql_ast_statement",
			Message: fmt.Sprintf("MySQL statement %T is not supported when rollback protection is enabled", stmt),
		}
	}
}

func (v *mysqlExprSafetyVisitor) Enter(n ast.Node) (ast.Node, bool) {
	if v.err != nil {
		return n, true
	}
	switch node := n.(type) {
	case *ast.SubqueryExpr, *ast.ExistsSubqueryExpr, *ast.CompareSubqueryExpr, *ast.SelectStmt, *ast.SetOprStmt:
		v.err = &CapabilityError{
			Reason:  v.subqueryReason,
			Message: "subqueries are not supported when rollback protection is enabled",
		}
		return n, true
	case *ast.PatternInExpr:
		if node.Sel != nil {
			v.err = &CapabilityError{
				Reason:  v.subqueryReason,
				Message: "IN (SELECT ...) subqueries are not supported when rollback protection is enabled",
			}
			return n, true
		}
	case *ast.FuncCallExpr:
		fnUpper := strings.ToUpper(node.FnName.L)
		if reason, bad := nondeterministicFunctions[fnUpper]; bad {
			v.err = &CapabilityError{
				Reason:  reason,
				Message: fmt.Sprintf("nondeterministic function %s() is not supported when rollback protection is enabled", fnUpper),
			}
			return n, true
		}
	}
	return n, false
}

func (v *mysqlExprSafetyVisitor) Leave(n ast.Node) (ast.Node, bool) {
	return n, v.err == nil
}

func checkMySQLNodeSafety(node ast.Node, subqueryReason string) error {
	if node == nil {
		return nil
	}
	v := &mysqlExprSafetyVisitor{subqueryReason: subqueryReason}
	node.Accept(v)
	if v.err != nil {
		return v.err
	}
	return nil
}

func restoreMySQLNode(node ast.Node) (string, error) {
	var sb strings.Builder
	flags := format.RestoreStringSingleQuotes | format.RestoreKeyWordUppercase | format.RestoreNameBackQuotes | format.RestoreStringWithoutDefaultCharset
	ctx := format.NewRestoreCtx(flags, &sb)
	if err := node.Restore(ctx); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func restoreMySQLLiteralOrExpr(node ast.ExprNode) (string, error) {
	var sb strings.Builder
	flags := format.RestoreStringSingleQuotes | format.RestoreKeyWordUppercase | format.RestoreStringWithoutDefaultCharset
	ctx := format.NewRestoreCtx(flags, &sb)
	if err := node.Restore(ctx); err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}

func extractMySQLSingleTableRef(defaultSchema string, refs *ast.TableRefsClause, joinErrReason, multiErrReason string) (string, string, string, error) {
	if refs == nil || refs.TableRefs == nil || refs.TableRefs.Left == nil {
		return "", "", "", &CapabilityError{Reason: "invalid_table_name", Message: "missing target table reference"}
	}
	join := refs.TableRefs
	if join.Right != nil {
		if join.Tp != 0 || join.On != nil || len(join.Using) > 0 || join.NaturalJoin || join.StraightJoin {
			return "", "", "", &CapabilityError{
				Reason:  joinErrReason,
				Message: "JOIN-based DML is not supported when rollback protection is enabled",
			}
		}
		return "", "", "", &CapabilityError{
			Reason:  multiErrReason,
			Message: "multi-table DML is not supported when rollback protection is enabled",
		}
	}
	ts, ok := join.Left.(*ast.TableSource)
	if !ok {
		return "", "", "", &CapabilityError{Reason: multiErrReason, Message: "complex table source is not supported"}
	}
	tn, ok := ts.Source.(*ast.TableName)
	if !ok {
		return "", "", "", &CapabilityError{Reason: multiErrReason, Message: "derived table DML is not supported"}
	}
	if len(tn.PartitionNames) > 0 {
		return "", "", "", &CapabilityError{
			Reason:  "partition_explicit_dml_unsupported",
			Message: "explicit PARTITION clause in DML is not supported when rollback protection is enabled",
		}
	}
	schema := defaultSchema
	if tn.Schema.O != "" {
		schema = tn.Schema.O
	}
	return schema, tn.Name.O, ts.AsName.O, nil
}

func analyzeMySQLInsertAST(defaultSchema, rawSQL, trimmed string, stmt *ast.InsertStmt) (*ParsedStatement, error) {
	if stmt.IsReplace {
		return nil, &CapabilityError{
			Reason:  "replace_into_unsupported",
			Message: "REPLACE INTO is not supported when rollback protection is enabled",
		}
	}
	if stmt.IgnoreErr {
		return nil, &CapabilityError{
			Reason:  "insert_ignore_unsupported",
			Message: "INSERT IGNORE is not supported when rollback protection is enabled",
		}
	}
	if len(stmt.OnDuplicate) > 0 {
		return nil, &CapabilityError{
			Reason:  "upsert_on_conflict_unsupported",
			Message: "INSERT with ON DUPLICATE KEY UPDATE is not supported when rollback protection is enabled",
		}
	}
	if stmt.Select != nil {
		return nil, &CapabilityError{
			Reason:  "insert_select_unsupported",
			Message: "INSERT ... SELECT is not supported when rollback protection is enabled",
		}
	}
	if stmt.Setlist {
		return nil, &CapabilityError{
			Reason:  "insert_non_values_unsupported",
			Message: "INSERT INTO ... SET is not supported; use INSERT INTO ... VALUES (...)",
		}
	}
	if len(stmt.PartitionNames) > 0 {
		return nil, &CapabilityError{
			Reason:  "insert_partition_unsupported",
			Message: "INSERT INTO ... PARTITION is not supported when rollback protection is enabled",
		}
	}

	schemaName, tableName, _, err := extractMySQLSingleTableRef(defaultSchema, stmt.Table, "insert_join_unsupported", "insert_multi_table_unsupported")
	if err != nil {
		return nil, err
	}

	cols := make([]string, 0, len(stmt.Columns))
	for _, col := range stmt.Columns {
		cols = append(cols, col.Name.O)
	}

	if len(stmt.Lists) == 0 {
		return nil, &CapabilityError{Reason: "insert_empty_values", Message: "INSERT VALUES contains no tuples"}
	}
	if len(stmt.Lists) > DefaultMaxCapturedRowsPerStatement {
		return nil, &CapabilityError{
			Reason:  "insert_batch_exceeds_rollback_limit",
			Message: fmt.Sprintf("batch INSERT count %d exceeds rollback capture limit %d", len(stmt.Lists), DefaultMaxCapturedRowsPerStatement),
		}
	}

	tuples := make([][]string, 0, len(stmt.Lists))
	for _, rowExprs := range stmt.Lists {
		if len(cols) > 0 && len(rowExprs) != len(cols) {
			return nil, &CapabilityError{
				Reason:  "insert_column_value_count_mismatch",
				Message: "INSERT column count does not match VALUES tuple length",
			}
		}
		tuple := make([]string, 0, len(rowExprs))
		for _, expr := range rowExprs {
			if err := checkMySQLNodeSafety(expr, "insert_subquery_value_unsupported"); err != nil {
				return nil, err
			}
			exprStr, rerr := restoreMySQLLiteralOrExpr(expr)
			if rerr != nil {
				return nil, &CapabilityError{Reason: "insert_expr_restore_failed", Message: rerr.Error()}
			}
			tuple = append(tuple, exprStr)
		}
		tuples = append(tuples, tuple)
	}

	return &ParsedStatement{
		RawSQL:        rawSQL,
		TrimmedSQL:    trimmed,
		OperationType: OperationTypeDML,
		Kind:          KindInsert,
		SchemaName:    schemaName,
		TableName:     tableName,
		InsertColumns: cols,
		InsertTuples:  tuples,
	}, nil
}

func analyzeMySQLUpdateAST(defaultSchema, rawSQL, trimmed string, stmt *ast.UpdateStmt) (*ParsedStatement, error) {
	if stmt.With != nil {
		return nil, &CapabilityError{
			Reason:  "cte_dml_unsupported",
			Message: "CTE (WITH ...) statements are not supported when rollback protection is enabled",
		}
	}
	if stmt.IgnoreErr {
		return nil, &CapabilityError{
			Reason:  "update_ignore_unsupported",
			Message: "UPDATE IGNORE is not supported when rollback protection is enabled",
		}
	}
	if stmt.MultipleTable {
		return nil, &CapabilityError{
			Reason:  "multi_table_update_unsupported",
			Message: "multi-table UPDATE is not supported when rollback protection is enabled",
		}
	}

	schemaName, tableName, alias, err := extractMySQLSingleTableRef(defaultSchema, stmt.TableRefs, "update_join_unsupported", "multi_table_update_unsupported")
	if err != nil {
		return nil, err
	}

	if stmt.Where == nil {
		return nil, &CapabilityError{
			Reason:  "update_without_where_unsupported",
			Message: "UPDATE without a WHERE clause is rejected when rollback protection is enabled",
		}
	}
	if stmt.Order != nil || stmt.Limit != nil {
		return nil, &CapabilityError{
			Reason:  "update_order_limit_unsupported",
			Message: "UPDATE with ORDER BY or LIMIT is not supported when rollback protection is enabled",
		}
	}

	if len(stmt.List) == 0 {
		return nil, &CapabilityError{Reason: "empty_update_set", Message: "UPDATE SET clause is empty"}
	}
	updatedCols := make([]string, 0, len(stmt.List))
	for _, assign := range stmt.List {
		if assign.Column == nil {
			return nil, &CapabilityError{Reason: "invalid_update_set_assignment", Message: "missing column in UPDATE SET"}
		}
		if err := checkMySQLNodeSafety(assign.Expr, "update_subquery_unsupported"); err != nil {
			return nil, err
		}
		updatedCols = append(updatedCols, assign.Column.Name.O)
	}

	if err := checkMySQLNodeSafety(stmt.Where, "update_subquery_unsupported"); err != nil {
		return nil, err
	}

	whereClause, err := extractOrRestoreMySQLWhere(trimmed, stmt.Where)
	if err != nil {
		return nil, err
	}

	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDML,
		Kind:           KindUpdate,
		SchemaName:     schemaName,
		TableName:      tableName,
		TableAlias:     alias,
		UpdatedColumns: updatedCols,
		WhereClause:    whereClause,
	}, nil
}

func analyzeMySQLDeleteAST(defaultSchema, rawSQL, trimmed string, stmt *ast.DeleteStmt) (*ParsedStatement, error) {
	if stmt.With != nil {
		return nil, &CapabilityError{
			Reason:  "cte_dml_unsupported",
			Message: "CTE (WITH ...) statements are not supported when rollback protection is enabled",
		}
	}
	if stmt.IgnoreErr || stmt.Quick || stmt.Priority != mysql.NoPriority {
		return nil, &CapabilityError{
			Reason:  "delete_modifier_unsupported",
			Message: "DELETE with LOW_PRIORITY/QUICK/IGNORE is not supported when rollback protection is enabled",
		}
	}
	if stmt.IsMultiTable || (stmt.Tables != nil && len(stmt.Tables.Tables) > 0) {
		return nil, &CapabilityError{
			Reason:  "delete_using_or_join_unsupported",
			Message: "multi-table DELETE or DELETE ... USING is not supported when rollback protection is enabled",
		}
	}

	schemaName, tableName, alias, err := extractMySQLSingleTableRef(defaultSchema, stmt.TableRefs, "delete_using_or_join_unsupported", "delete_using_or_join_unsupported")
	if err != nil {
		return nil, err
	}

	if stmt.Where == nil {
		return nil, &CapabilityError{
			Reason:  "delete_without_where_unsupported",
			Message: "DELETE without a WHERE clause is rejected when rollback protection is enabled",
		}
	}
	if stmt.Order != nil || stmt.Limit != nil {
		return nil, &CapabilityError{
			Reason:  "delete_order_limit_unsupported",
			Message: "DELETE with ORDER BY or LIMIT is not supported when rollback protection is enabled",
		}
	}

	if err := checkMySQLNodeSafety(stmt.Where, "delete_subquery_unsupported"); err != nil {
		return nil, err
	}

	whereClause, err := extractOrRestoreMySQLWhere(trimmed, stmt.Where)
	if err != nil {
		return nil, err
	}

	return &ParsedStatement{
		RawSQL:        rawSQL,
		TrimmedSQL:    trimmed,
		OperationType: OperationTypeDML,
		Kind:          KindDelete,
		SchemaName:    schemaName,
		TableName:     tableName,
		TableAlias:    alias,
		WhereClause:   whereClause,
	}, nil
}

func extractOrRestoreMySQLWhere(trimmed string, whereNode ast.ExprNode) (string, error) {
	if pos := whereNode.OriginTextPosition(); pos > 0 && pos < len(trimmed) {
		clause := strings.TrimSpace(trimmed[pos:])
		if clause != "" {
			return clause, nil
		}
	}
	restored, err := restoreMySQLNode(whereNode)
	if err != nil || strings.TrimSpace(restored) == "" {
		return "", &CapabilityError{Reason: "invalid_where_clause", Message: "failed to restore MySQL WHERE clause from AST"}
	}
	return strings.TrimSpace(restored), nil
}

func analyzeMySQLCreateTableAST(defaultSchema, rawSQL, trimmed string, stmt *ast.CreateTableStmt) (*ParsedStatement, error) {
	if stmt.TemporaryKeyword != ast.TemporaryNone {
		return nil, &CapabilityError{
			Reason:  "create_temporary_or_unlogged_table_unsupported",
			Message: "CREATE TEMPORARY TABLE is not supported when rollback protection is enabled",
		}
	}
	if stmt.Select != nil || stmt.ReferTable != nil || stmt.Partition != nil {
		return nil, &CapabilityError{
			Reason:  "create_table_as_select_or_like_unsupported",
			Message: "CREATE TABLE ... AS SELECT / LIKE / PARTITION is not supported when rollback protection is enabled",
		}
	}
	schema := defaultSchema
	if stmt.Table.Schema.O != "" {
		schema = stmt.Table.Schema.O
	}
	return &ParsedStatement{
		RawSQL:        rawSQL,
		TrimmedSQL:    trimmed,
		OperationType: OperationTypeDDL,
		Kind:          KindCreateTable,
		SchemaName:    schema,
		TableName:     stmt.Table.Name.O,
	}, nil
}

func analyzeMySQLDropTableAST(defaultSchema, rawSQL, trimmed string, stmt *ast.DropTableStmt) (*ParsedStatement, error) {
	if stmt.IsView {
		return nil, &CapabilityError{
			Reason:  "drop_view_unsupported",
			Message: "DROP VIEW is not supported when rollback protection is enabled",
		}
	}
	if stmt.TemporaryKeyword != ast.TemporaryNone {
		return nil, &CapabilityError{
			Reason:  "drop_temporary_table_unsupported",
			Message: "DROP TEMPORARY TABLE is not supported when rollback protection is enabled",
		}
	}
	if len(stmt.Tables) != 1 || stmt.Tables[0] == nil || stmt.Tables[0].Name.O == "" {
		return nil, &CapabilityError{
			Reason:  "multi_table_drop_unsupported",
			Message: "DROP TABLE requires a single target table when rollback protection is enabled",
		}
	}
	tbl := stmt.Tables[0]
	schema := defaultSchema
	if tbl.Schema.O != "" {
		schema = tbl.Schema.O
	}
	return &ParsedStatement{
		RawSQL:        rawSQL,
		TrimmedSQL:    trimmed,
		OperationType: OperationTypeDDL,
		Kind:          KindDropTable,
		SchemaName:    schema,
		TableName:     tbl.Name.O,
	}, nil
}

func analyzeMySQLCreateIndexAST(defaultSchema, rawSQL, trimmed string, stmt *ast.CreateIndexStmt) (*ParsedStatement, error) {
	if stmt.IndexName == "" || stmt.Table == nil {
		return nil, &CapabilityError{Reason: "invalid_create_index_syntax", Message: "missing index name or table in CREATE INDEX"}
	}
	schema := defaultSchema
	if stmt.Table.Schema.O != "" {
		schema = stmt.Table.Schema.O
	}
	return &ParsedStatement{
		RawSQL:        rawSQL,
		TrimmedSQL:    trimmed,
		OperationType: OperationTypeDDL,
		Kind:          KindAddIndex,
		SchemaName:    schema,
		TableName:     stmt.Table.Name.O,
		IndexName:     stmt.IndexName,
		IndexUnique:   stmt.KeyType == ast.IndexKeyTypeUnique,
	}, nil
}

func analyzeMySQLDropIndexAST(defaultSchema, rawSQL, trimmed string, stmt *ast.DropIndexStmt) (*ParsedStatement, error) {
	if stmt.Table == nil || stmt.Table.Name.O == "" {
		return nil, &CapabilityError{
			Reason:  "mysql_drop_index_requires_on_table",
			Message: "MySQL DROP INDEX requires ON <table>",
		}
	}
	schema := defaultSchema
	if stmt.Table.Schema.O != "" {
		schema = stmt.Table.Schema.O
	}
	return &ParsedStatement{
		RawSQL:        rawSQL,
		TrimmedSQL:    trimmed,
		OperationType: OperationTypeDDL,
		Kind:          KindDropIndex,
		SchemaName:    schema,
		TableName:     stmt.Table.Name.O,
		IndexName:     stmt.IndexName,
	}, nil
}

func analyzeMySQLRenameTableAST(defaultSchema, rawSQL, trimmed string, stmt *ast.RenameTableStmt) (*ParsedStatement, error) {
	if len(stmt.TableToTables) == 0 {
		return nil, &CapabilityError{Reason: "invalid_rename_table_syntax", Message: "empty RENAME TABLE statement"}
	}
	if len(stmt.TableToTables) > 1 {
		return nil, &CapabilityError{
			Reason:  "multi_table_rename_unsupported",
			Message: "RENAME TABLE with multiple table pairs in one statement is not supported",
		}
	}
	pair := stmt.TableToTables[0]
	oldSchema := defaultSchema
	if pair.OldTable.Schema.O != "" {
		oldSchema = pair.OldTable.Schema.O
	}
	newSchema := oldSchema
	if pair.NewTable.Schema.O != "" {
		newSchema = pair.NewTable.Schema.O
	}
	if !strings.EqualFold(oldSchema, newSchema) {
		return nil, &CapabilityError{
			Reason:  "cross_schema_rename_table_unsupported",
			Message: "cross-schema RENAME TABLE is not supported when rollback protection is enabled",
		}
	}
	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDDL,
		Kind:           KindRenameTable,
		SchemaName:     oldSchema,
		TableName:      pair.OldTable.Name.O,
		PostSchemaName: newSchema,
		PostTableName:  pair.NewTable.Name.O,
	}, nil
}

func analyzeMySQLAlterAddConstraintAST(schema, tableName, rawSQL, trimmed string, con *ast.Constraint) (*ParsedStatement, error) {
	if con == nil {
		return nil, &CapabilityError{Reason: "invalid_add_constraint", Message: "missing constraint in ALTER TABLE ADD"}
	}
	switch con.Tp {
	case ast.ConstraintKey, ast.ConstraintIndex:
		if con.Name == "" {
			return nil, &CapabilityError{
				Reason:  "add_index_requires_explicit_name",
				Message: "ALTER TABLE ADD INDEX requires an explicit index name when rollback protection is enabled",
			}
		}
		return &ParsedStatement{
			RawSQL:        rawSQL,
			TrimmedSQL:    trimmed,
			OperationType: OperationTypeDDL,
			Kind:          KindAddIndex,
			SchemaName:    schema,
			TableName:     tableName,
			IndexName:     con.Name,
			IndexUnique:   false,
		}, nil

	case ast.ConstraintUniq, ast.ConstraintUniqKey, ast.ConstraintUniqIndex:
		// Distinguish ADD CONSTRAINT <name> UNIQUE (...) from ADD UNIQUE INDEX <name> (...)
		if hasAddConstraintKeyword(trimmed) && con.Name != "" {
			defSQL, err := restoreMySQLConstraintBody(con)
			if err != nil {
				return nil, err
			}
			return &ParsedStatement{
				RawSQL:             rawSQL,
				TrimmedSQL:         trimmed,
				OperationType:      OperationTypeDDL,
				Kind:               KindAddConstraint,
				SchemaName:         schema,
				TableName:          tableName,
				ConstraintName:     con.Name,
				ConstraintTypeHint: "UNIQUE",
				ConstraintDefSQL:   defSQL,
			}, nil
		}
		if con.Name == "" {
			return nil, &CapabilityError{
				Reason:  "add_unique_index_requires_explicit_name",
				Message: "ALTER TABLE ADD UNIQUE INDEX requires an explicit name when rollback protection is enabled",
			}
		}
		return &ParsedStatement{
			RawSQL:        rawSQL,
			TrimmedSQL:    trimmed,
			OperationType: OperationTypeDDL,
			Kind:          KindAddIndex,
			SchemaName:    schema,
			TableName:     tableName,
			IndexName:     con.Name,
			IndexUnique:   true,
		}, nil

	case ast.ConstraintForeignKey, ast.ConstraintCheck, ast.ConstraintPrimaryKey:
		if con.Name == "" {
			return nil, &CapabilityError{
				Reason:  "unnamed_constraint_unsupported",
				Message: "ALTER TABLE ADD PRIMARY KEY / FOREIGN KEY / CHECK requires an explicit CONSTRAINT <name> clause when rollback protection is enabled",
			}
		}
		typeHint := ""
		switch con.Tp {
		case ast.ConstraintForeignKey:
			typeHint = "FOREIGN KEY"
		case ast.ConstraintCheck:
			typeHint = "CHECK"
		case ast.ConstraintPrimaryKey:
			typeHint = "PRIMARY KEY"
		}
		defSQL, err := restoreMySQLConstraintBody(con)
		if err != nil {
			return nil, err
		}
		return &ParsedStatement{
			RawSQL:             rawSQL,
			TrimmedSQL:         trimmed,
			OperationType:      OperationTypeDDL,
			Kind:               KindAddConstraint,
			SchemaName:         schema,
			TableName:          tableName,
			ConstraintName:     con.Name,
			ConstraintTypeHint: typeHint,
			ConstraintDefSQL:   defSQL,
		}, nil

	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_constraint_type",
			Message: "unsupported constraint type in ALTER TABLE ADD",
		}
	}
}

func restoreMySQLColumnDefWithoutName(col *ast.ColumnDef) (string, error) {
	var sb strings.Builder
	flags := format.RestoreStringSingleQuotes | format.RestoreKeyWordUppercase | format.RestoreStringWithoutDefaultCharset
	ctx := format.NewRestoreCtx(flags, &sb)
	if col.Tp != nil {
		if err := col.Tp.Restore(ctx); err != nil {
			return "", &CapabilityError{Reason: "column_type_restore_failed", Message: err.Error()}
		}
	}
	for _, opt := range col.Options {
		sb.WriteByte(' ')
		if err := opt.Restore(ctx); err != nil {
			return "", &CapabilityError{Reason: "column_option_restore_failed", Message: err.Error()}
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func restoreMySQLConstraintBody(con *ast.Constraint) (string, error) {
	copyCon := *con
	copyCon.Name = ""
	s, err := restoreMySQLNode(&copyCon)
	if err != nil {
		return "", &CapabilityError{Reason: "constraint_restore_failed", Message: err.Error()}
	}
	return strings.TrimSpace(s), nil
}

func hasAddConstraintKeyword(trimmed string) bool {
	upper := strings.ToUpper(trimmed)
	return strings.Contains(upper, "ADD CONSTRAINT")
}

func diagnoseMySQLParseFailure(trimmed string, parseErr error) error {
	upper := strings.ToUpper(strings.TrimSpace(trimmed))
	if strings.HasPrefix(upper, "MERGE ") || strings.HasPrefix(upper, "MERGE\t") || strings.HasPrefix(upper, "MERGE\n") {
		return &CapabilityError{
			Reason:  "merge_unsupported",
			Message: "MERGE statements are not supported when rollback protection is enabled",
		}
	}
	if strings.HasPrefix(upper, "DROP INDEX ") && !strings.Contains(upper, " ON ") {
		return &CapabilityError{
			Reason:  "mysql_drop_index_requires_on_table",
			Message: "MySQL DROP INDEX requires ON <table>",
		}
	}
	_ = parseErr
	return nil
}
