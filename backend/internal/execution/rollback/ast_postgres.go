package rollback

import (
	"fmt"
	"strings"

	pgnode "github.com/pganalyze/pg_query_go/v6"
	pg_query "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// analyzePostgresWithAST parses and validates the AST using the official PostgreSQL parser (wasilibs/go-pgquery, zero CGO).
func analyzePostgresWithAST(defaultSchema, rawSQL, trimmed string) (*ParsedStatement, error) {
	tree, err := pg_query.Parse(trimmed)
	if err != nil {
		return nil, &CapabilityError{
			Reason:  "postgres_ast_parse_error",
			Message: fmt.Sprintf("PostgreSQL AST parse failed: %v", err),
		}
	}
	if tree == nil || len(tree.Stmts) == 0 || tree.Stmts[0].GetStmt() == nil {
		return nil, &CapabilityError{
			Reason:  "empty_statement",
			Message: "empty SQL statement cannot generate rollback",
		}
	}
	if len(tree.Stmts) > 1 {
		return nil, &CapabilityError{
			Reason:  "multi_statement_in_single_unit_unsupported",
			Message: "multiple SQL statements in a single execution unit are not supported",
		}
	}

	stmtNode := tree.Stmts[0].GetStmt()
	switch {
	case stmtNode.GetMergeStmt() != nil:
		return nil, &CapabilityError{
			Reason:  "merge_unsupported",
			Message: "MERGE statements are not supported when rollback protection is enabled",
		}
	case stmtNode.GetInsertStmt() != nil:
		return analyzePostgresInsertAST(defaultSchema, rawSQL, trimmed, stmtNode.GetInsertStmt())
	case stmtNode.GetUpdateStmt() != nil:
		return analyzePostgresUpdateAST(defaultSchema, rawSQL, trimmed, stmtNode.GetUpdateStmt())
	case stmtNode.GetDeleteStmt() != nil:
		return analyzePostgresDeleteAST(defaultSchema, rawSQL, trimmed, stmtNode.GetDeleteStmt())
	case stmtNode.GetTruncateStmt() != nil:
		return nil, &CapabilityError{
			Reason:  "destructive_ddl_truncate_requires_backup",
			Message: "TRUNCATE TABLE destroys table data and cannot be safely rolled back without a full table backup",
		}
	case stmtNode.GetCreateTableAsStmt() != nil:
		return nil, &CapabilityError{
			Reason:  "create_table_as_select_or_like_unsupported",
			Message: "CREATE TABLE ... AS SELECT is not supported when rollback protection is enabled",
		}
	case stmtNode.GetCreateStmt() != nil:
		return analyzePostgresCreateTableAST(defaultSchema, rawSQL, trimmed, stmtNode.GetCreateStmt())
	case stmtNode.GetIndexStmt() != nil:
		return analyzePostgresCreateIndexAST(defaultSchema, rawSQL, trimmed, stmtNode.GetIndexStmt())
	case stmtNode.GetDropStmt() != nil:
		return analyzePostgresDropAST(defaultSchema, rawSQL, trimmed, stmtNode.GetDropStmt())
	case stmtNode.GetDropdbStmt() != nil:
		return nil, &CapabilityError{
			Reason:  "destructive_ddl_drop_schema_unsupported",
			Message: "DROP DATABASE is destructive and cannot be rolled back",
		}
	case stmtNode.GetRenameStmt() != nil:
		return analyzePostgresRenameAST(defaultSchema, rawSQL, trimmed, stmtNode.GetRenameStmt())
	case stmtNode.GetAlterTableStmt() != nil:
		return analyzePostgresAlterTableAST(defaultSchema, rawSQL, trimmed, stmtNode.GetAlterTableStmt())
	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_postgres_ast_statement",
			Message: "PostgreSQL statement type is not supported when rollback protection is enabled",
		}
	}
}

func checkPostgresExprNodeSafety(node *pgnode.Node, subqueryReason string) error {
	if node == nil {
		return nil
	}
	return walkPostgresProto(node.ProtoReflect(), subqueryReason)
}

func walkPostgresProto(msg protoreflect.Message, subqueryReason string) error {
	if !msg.IsValid() {
		return nil
	}
	if n, ok := msg.Interface().(*pgnode.Node); ok && n != nil {
		if n.GetSubLink() != nil || n.GetSelectStmt() != nil {
			return &CapabilityError{
				Reason:  subqueryReason,
				Message: "subqueries are not supported when rollback protection is enabled",
			}
		}
		if n.GetMultiAssignRef() != nil {
			return &CapabilityError{
				Reason:  "update_row_constructor_set_unsupported",
				Message: "row-constructor SET (a, b) = (...) is not supported when rollback protection is enabled",
			}
		}
		if fc := n.GetFuncCall(); fc != nil {
			if name := extractPostgresFuncName(fc); name != "" {
				if reason, bad := nondeterministicFunctions[strings.ToUpper(name)]; bad {
					return &CapabilityError{
						Reason:  reason,
						Message: fmt.Sprintf("nondeterministic function %s() is not supported when rollback protection is enabled", strings.ToUpper(name)),
					}
				}
			}
		}
	}

	var walkErr error
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if walkErr != nil {
			return false
		}
		if fd.IsList() {
			if fd.Kind() != protoreflect.MessageKind {
				return true
			}
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				if err := walkPostgresProto(list.Get(i).Message(), subqueryReason); err != nil {
					walkErr = err
					return false
				}
			}
			return true
		}
		if fd.Kind() == protoreflect.MessageKind {
			if err := walkPostgresProto(v.Message(), subqueryReason); err != nil {
				walkErr = err
				return false
			}
		}
		return true
	})
	return walkErr
}

func extractPostgresFuncName(fc *pgnode.FuncCall) string {
	names := fc.GetFuncname()
	if len(names) == 0 {
		return ""
	}
	last := names[len(names)-1]
	if s := last.GetString_(); s != nil {
		return s.GetSval()
	}
	return ""
}

func deparsePostgresExpr(exprNode *pgnode.Node) (string, error) {
	res := &pgnode.ParseResult{
		Stmts: []*pgnode.RawStmt{
			{
				Stmt: &pgnode.Node{
					Node: &pgnode.Node_SelectStmt{
						SelectStmt: &pgnode.SelectStmt{
							TargetList: []*pgnode.Node{
								{
									Node: &pgnode.Node_ResTarget{
										ResTarget: &pgnode.ResTarget{
											Val: exprNode,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	out, err := pg_query.Deparse(res)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if strings.HasPrefix(strings.ToUpper(out), "SELECT ") {
		return strings.TrimSpace(out[len("SELECT "):]), nil
	}
	return out, nil
}

func deparsePostgresWhereClause(whereNode *pgnode.Node) (string, error) {
	res := &pgnode.ParseResult{
		Stmts: []*pgnode.RawStmt{
			{
				Stmt: &pgnode.Node{
					Node: &pgnode.Node_SelectStmt{
						SelectStmt: &pgnode.SelectStmt{
							WhereClause: whereNode,
						},
					},
				},
			},
		},
	}
	out, err := pg_query.Deparse(res)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	const prefix = "SELECT WHERE "
	if len(out) >= len(prefix) && strings.EqualFold(out[:len(prefix)], prefix) {
		return strings.TrimSpace(out[len(prefix):]), nil
	}
	return "", fmt.Errorf("unexpected deparse output for WHERE clause: %s", out)
}

func extractPostgresRangeVar(defaultSchema string, rv *pgnode.RangeVar) (string, bool, string, string, error) {
	if rv == nil || rv.GetRelname() == "" {
		return "", false, "", "", &CapabilityError{Reason: "invalid_table_name", Message: "missing target table relation"}
	}
	schema := defaultSchema
	explicit := rv.GetSchemaname() != ""
	if explicit {
		schema = rv.GetSchemaname()
	}
	alias := ""
	if rv.GetAlias() != nil {
		alias = rv.GetAlias().GetAliasname()
	}
	return schema, explicit, rv.GetRelname(), alias, nil
}

func analyzePostgresInsertAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.InsertStmt) (*ParsedStatement, error) {
	if stmt.GetWithClause() != nil {
		return nil, &CapabilityError{
			Reason:  "cte_dml_unsupported",
			Message: "CTE (WITH ...) statements are not supported when rollback protection is enabled",
		}
	}
	if stmt.GetOnConflictClause() != nil {
		return nil, &CapabilityError{
			Reason:  "upsert_on_conflict_unsupported",
			Message: "INSERT ... ON CONFLICT is not supported when rollback protection is enabled",
		}
	}
	if len(stmt.GetReturningList()) > 0 {
		return nil, &CapabilityError{
			Reason:  "dml_returning_clause_unsupported",
			Message: "DML statements with user-specified RETURNING clauses are not supported",
		}
	}
	if stmt.GetOverride() != pgnode.OverridingKind_OVERRIDING_NOT_SET && stmt.GetOverride() != pgnode.OverridingKind_OVERRIDING_KIND_UNDEFINED {
		return nil, &CapabilityError{
			Reason:  "insert_overriding_unsupported",
			Message: "INSERT ... OVERRIDING is not supported when rollback protection is enabled",
		}
	}

	schemaName, schemaExplicit, tableName, _, err := extractPostgresRangeVar(defaultSchema, stmt.GetRelation())
	if err != nil {
		return nil, err
	}

	cols := make([]string, 0, len(stmt.GetCols()))
	for _, colNode := range stmt.GetCols() {
		rt := colNode.GetResTarget()
		if rt == nil || rt.GetName() == "" {
			return nil, &CapabilityError{Reason: "invalid_column_list", Message: "invalid column target in INSERT"}
		}
		if len(rt.GetIndirection()) > 0 {
			return nil, &CapabilityError{Reason: "insert_indirection_unsupported", Message: "composite/array field indirection in INSERT is not supported"}
		}
		cols = append(cols, rt.GetName())
	}

	selNode := stmt.GetSelectStmt()
	if selNode == nil || selNode.GetSelectStmt() == nil {
		return nil, &CapabilityError{
			Reason:  "insert_non_values_unsupported",
			Message: "INSERT DEFAULT VALUES is not supported; specify explicit VALUES",
		}
	}
	sel := selNode.GetSelectStmt()
	if len(sel.GetValuesLists()) == 0 || len(sel.GetTargetList()) > 0 || len(sel.GetFromClause()) > 0 || sel.GetOp() != pgnode.SetOperation_SETOP_NONE {
		return nil, &CapabilityError{
			Reason:  "insert_select_unsupported",
			Message: "INSERT ... SELECT is not supported when rollback protection is enabled",
		}
	}
	if len(sel.GetValuesLists()) > DefaultMaxCapturedRowsPerStatement {
		return nil, &CapabilityError{
			Reason:  "insert_batch_exceeds_rollback_limit",
			Message: fmt.Sprintf("batch INSERT count %d exceeds rollback capture limit %d", len(sel.GetValuesLists()), DefaultMaxCapturedRowsPerStatement),
		}
	}

	tuples := make([][]string, 0, len(sel.GetValuesLists()))
	for _, rowNode := range sel.GetValuesLists() {
		list := rowNode.GetList()
		if list == nil {
			return nil, &CapabilityError{Reason: "invalid_values_tuple", Message: "expected VALUES tuple list"}
		}
		items := list.GetItems()
		if len(cols) > 0 && len(items) != len(cols) {
			return nil, &CapabilityError{
				Reason:  "insert_column_value_count_mismatch",
				Message: "INSERT column count does not match VALUES tuple length",
			}
		}
		tuple := make([]string, 0, len(items))
		for _, expr := range items {
			if err := checkPostgresExprNodeSafety(expr, "insert_subquery_value_unsupported"); err != nil {
				return nil, err
			}
			deparsed, derr := deparsePostgresExpr(expr)
			if derr != nil {
				return nil, &CapabilityError{Reason: "insert_expr_deparse_failed", Message: derr.Error()}
			}
			tuple = append(tuple, deparsed)
		}
		tuples = append(tuples, tuple)
	}

	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDML,
		Kind:           KindInsert,
		SchemaName:     schemaName,
		SchemaExplicit: schemaExplicit,
		TableName:      tableName,
		InsertColumns:  cols,
		InsertTuples:   tuples,
	}, nil
}

func analyzePostgresUpdateAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.UpdateStmt) (*ParsedStatement, error) {
	if stmt.GetWithClause() != nil {
		return nil, &CapabilityError{
			Reason:  "cte_dml_unsupported",
			Message: "CTE (WITH ...) statements are not supported when rollback protection is enabled",
		}
	}
	if len(stmt.GetFromClause()) > 0 {
		return nil, &CapabilityError{
			Reason:  "update_from_unsupported",
			Message: "UPDATE ... FROM is not supported when rollback protection is enabled",
		}
	}
	if len(stmt.GetReturningList()) > 0 {
		return nil, &CapabilityError{
			Reason:  "dml_returning_clause_unsupported",
			Message: "UPDATE with user-specified RETURNING is not supported",
		}
	}
	if stmt.GetWhereClause() == nil {
		return nil, &CapabilityError{
			Reason:  "update_without_where_unsupported",
			Message: "UPDATE without a WHERE clause is rejected when rollback protection is enabled",
		}
	}

	schemaName, schemaExplicit, tableName, alias, err := extractPostgresRangeVar(defaultSchema, stmt.GetRelation())
	if err != nil {
		return nil, err
	}

	targets := stmt.GetTargetList()
	if len(targets) == 0 {
		return nil, &CapabilityError{Reason: "empty_update_set", Message: "UPDATE SET clause is empty"}
	}
	updatedCols := make([]string, 0, len(targets))
	for _, tNode := range targets {
		rt := tNode.GetResTarget()
		if rt == nil || rt.GetName() == "" {
			return nil, &CapabilityError{Reason: "invalid_update_set_assignment", Message: "invalid column target in UPDATE SET"}
		}
		if len(rt.GetIndirection()) > 0 {
			return nil, &CapabilityError{
				Reason:  "update_indirection_unsupported",
				Message: "JSON/array subscript or composite field assignment in UPDATE is not supported",
			}
		}
		if err := checkPostgresExprNodeSafety(rt.GetVal(), "update_subquery_unsupported"); err != nil {
			return nil, err
		}
		updatedCols = append(updatedCols, rt.GetName())
	}

	if err := checkPostgresExprNodeSafety(stmt.GetWhereClause(), "update_subquery_unsupported"); err != nil {
		return nil, err
	}

	whereClause, err := deparsePostgresWhereClause(stmt.GetWhereClause())
	if err != nil || whereClause == "" {
		return nil, &CapabilityError{Reason: "invalid_where_clause", Message: "failed to deparse PostgreSQL UPDATE WHERE clause"}
	}

	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDML,
		Kind:           KindUpdate,
		SchemaName:     schemaName,
		SchemaExplicit: schemaExplicit,
		TableName:      tableName,
		TableAlias:     alias,
		UpdatedColumns: updatedCols,
		WhereClause:    whereClause,
	}, nil
}

func analyzePostgresDeleteAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.DeleteStmt) (*ParsedStatement, error) {
	if stmt.GetWithClause() != nil {
		return nil, &CapabilityError{
			Reason:  "cte_dml_unsupported",
			Message: "CTE (WITH ...) statements are not supported when rollback protection is enabled",
		}
	}
	if len(stmt.GetUsingClause()) > 0 {
		return nil, &CapabilityError{
			Reason:  "delete_using_or_join_unsupported",
			Message: "DELETE ... USING is not supported when rollback protection is enabled",
		}
	}
	if len(stmt.GetReturningList()) > 0 {
		return nil, &CapabilityError{
			Reason:  "dml_returning_clause_unsupported",
			Message: "DELETE with user-specified RETURNING is not supported",
		}
	}
	if stmt.GetWhereClause() == nil {
		return nil, &CapabilityError{
			Reason:  "delete_without_where_unsupported",
			Message: "DELETE without a WHERE clause is rejected when rollback protection is enabled",
		}
	}

	schemaName, schemaExplicit, tableName, alias, err := extractPostgresRangeVar(defaultSchema, stmt.GetRelation())
	if err != nil {
		return nil, err
	}

	if err := checkPostgresExprNodeSafety(stmt.GetWhereClause(), "delete_subquery_unsupported"); err != nil {
		return nil, err
	}

	whereClause, err := deparsePostgresWhereClause(stmt.GetWhereClause())
	if err != nil || whereClause == "" {
		return nil, &CapabilityError{Reason: "invalid_where_clause", Message: "failed to deparse PostgreSQL DELETE WHERE clause"}
	}

	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDML,
		Kind:           KindDelete,
		SchemaName:     schemaName,
		SchemaExplicit: schemaExplicit,
		TableName:      tableName,
		TableAlias:     alias,
		WhereClause:    whereClause,
	}, nil
}

func analyzePostgresCreateTableAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.CreateStmt) (*ParsedStatement, error) {
	rv := stmt.GetRelation()
	if rv == nil || rv.GetRelname() == "" {
		return nil, &CapabilityError{Reason: "invalid_create_syntax", Message: "missing table relation in CREATE TABLE"}
	}
	// Relpersistence: "p" = permanent, "t" = temporary, "u" = unlogged
	if rv.GetRelpersistence() != "" && rv.GetRelpersistence() != "p" {
		return nil, &CapabilityError{
			Reason:  "create_temporary_or_unlogged_table_unsupported",
			Message: "CREATE TEMPORARY/UNLOGGED TABLE is not supported when rollback protection is enabled",
		}
	}
	if stmt.GetPartspec() != nil || stmt.GetPartbound() != nil || len(stmt.GetInhRelations()) > 0 {
		return nil, &CapabilityError{
			Reason:  "create_table_as_select_or_like_unsupported",
			Message: "CREATE TABLE ... PARTITION / INHERITS is not supported when rollback protection is enabled",
		}
	}
	for _, elt := range stmt.GetTableElts() {
		if elt.GetTableLikeClause() != nil {
			return nil, &CapabilityError{
				Reason:  "create_table_as_select_or_like_unsupported",
				Message: "CREATE TABLE ... (LIKE ...) is not supported when rollback protection is enabled",
			}
		}
	}

	schemaName, schemaExplicit, tableName, _, err := extractPostgresRangeVar(defaultSchema, rv)
	if err != nil {
		return nil, err
	}
	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDDL,
		Kind:           KindCreateTable,
		SchemaName:     schemaName,
		SchemaExplicit: schemaExplicit,
		TableName:      tableName,
	}, nil
}

func analyzePostgresCreateIndexAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.IndexStmt) (*ParsedStatement, error) {
	if stmt.GetIdxname() == "" {
		return nil, &CapabilityError{Reason: "invalid_create_index_syntax", Message: "explicit index name is required in CREATE INDEX"}
	}
	schemaName, schemaExplicit, tableName, _, err := extractPostgresRangeVar(defaultSchema, stmt.GetRelation())
	if err != nil {
		return nil, err
	}
	return &ParsedStatement{
		RawSQL:         rawSQL,
		TrimmedSQL:     trimmed,
		OperationType:  OperationTypeDDL,
		Kind:           KindAddIndex,
		SchemaName:     schemaName,
		SchemaExplicit: schemaExplicit,
		TableName:      tableName,
		IndexName:      stmt.GetIdxname(),
		IndexUnique:    stmt.GetUnique(),
	}, nil
}

func analyzePostgresDropAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.DropStmt) (*ParsedStatement, error) {
	switch stmt.GetRemoveType() {
	case pgnode.ObjectType_OBJECT_TABLE:
		if stmt.GetBehavior() == pgnode.DropBehavior_DROP_CASCADE {
			return nil, &CapabilityError{
				Reason:  "drop_table_cascade_unsupported",
				Message: "DROP TABLE ... CASCADE is not supported when rollback protection is enabled",
			}
		}
		objs := stmt.GetObjects()
		if len(objs) != 1 || objs[0].GetList() == nil {
			return nil, &CapabilityError{
				Reason:  "multi_table_drop_unsupported",
				Message: "DROP TABLE requires a single target table when rollback protection is enabled",
			}
		}
		items := objs[0].GetList().GetItems()
		schema := defaultSchema
		schemaExplicit := false
		tblName := ""
		if len(items) == 1 && items[0].GetString_() != nil {
			tblName = items[0].GetString_().GetSval()
		} else if len(items) == 2 && items[0].GetString_() != nil && items[1].GetString_() != nil {
			schema = items[0].GetString_().GetSval()
			schemaExplicit = true
			tblName = items[1].GetString_().GetSval()
		} else {
			return nil, &CapabilityError{Reason: "invalid_drop_table_syntax", Message: "invalid table name in DROP TABLE"}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindDropTable,
			SchemaName:     schema,
			SchemaExplicit: schemaExplicit,
			TableName:      tblName,
		}, nil
	case pgnode.ObjectType_OBJECT_SCHEMA, pgnode.ObjectType_OBJECT_DATABASE:
		return nil, &CapabilityError{
			Reason:  "destructive_ddl_drop_schema_unsupported",
			Message: "DROP SCHEMA/DATABASE is destructive and cannot be rolled back",
		}
	case pgnode.ObjectType_OBJECT_INDEX:
		if stmt.GetBehavior() == pgnode.DropBehavior_DROP_CASCADE {
			return nil, &CapabilityError{
				Reason:  "drop_index_cascade_unsupported",
				Message: "DROP INDEX ... CASCADE is not supported when rollback protection is enabled",
			}
		}
		objs := stmt.GetObjects()
		if len(objs) != 1 || objs[0].GetList() == nil {
			return nil, &CapabilityError{
				Reason:  "multi_index_drop_unsupported",
				Message: "DROP INDEX must target exactly one index when rollback protection is enabled",
			}
		}
		items := objs[0].GetList().GetItems()
		schema := defaultSchema
		schemaExplicit := false
		idxName := ""
		if len(items) == 1 && items[0].GetString_() != nil {
			idxName = items[0].GetString_().GetSval()
		} else if len(items) == 2 && items[0].GetString_() != nil && items[1].GetString_() != nil {
			schema = items[0].GetString_().GetSval()
			schemaExplicit = true
			idxName = items[1].GetString_().GetSval()
		} else {
			return nil, &CapabilityError{Reason: "invalid_drop_index_syntax", Message: "invalid index name in DROP INDEX"}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindDropIndex,
			SchemaName:     schema,
			SchemaExplicit: schemaExplicit,
			IndexName:      idxName,
		}, nil
	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_drop_object",
			Message: fmt.Sprintf("DROP %s is not supported when rollback protection is enabled", stmt.GetRemoveType().String()),
		}
	}
}

func analyzePostgresRenameAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.RenameStmt) (*ParsedStatement, error) {
	schemaName, schemaExplicit, tableName, _, err := extractPostgresRangeVar(defaultSchema, stmt.GetRelation())
	if err != nil {
		return nil, err
	}
	switch stmt.GetRenameType() {
	case pgnode.ObjectType_OBJECT_COLUMN:
		if stmt.GetSubname() == "" || stmt.GetNewname() == "" {
			return nil, &CapabilityError{Reason: "invalid_rename_column_syntax", Message: "missing old or new column name in RENAME COLUMN"}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindRenameColumn,
			SchemaName:     schemaName,
			SchemaExplicit: schemaExplicit,
			TableName:      tableName,
			ColumnName:     stmt.GetSubname(),
			PostColumnName: stmt.GetNewname(),
		}, nil
	case pgnode.ObjectType_OBJECT_TABLE:
		if stmt.GetNewname() == "" {
			return nil, &CapabilityError{Reason: "invalid_rename_table_syntax", Message: "missing new table name in ALTER TABLE RENAME"}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindRenameTable,
			SchemaName:     schemaName,
			SchemaExplicit: schemaExplicit,
			TableName:      tableName,
			PostSchemaName: schemaName,
			PostTableName:  stmt.GetNewname(),
		}, nil
	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_rename_object",
			Message: "only RENAME TABLE and RENAME COLUMN are supported when rollback protection is enabled",
		}
	}
}

func analyzePostgresAlterTableAST(defaultSchema, rawSQL, trimmed string, stmt *pgnode.AlterTableStmt) (*ParsedStatement, error) {
	if stmt.GetObjtype() != pgnode.ObjectType_OBJECT_TABLE {
		return nil, &CapabilityError{
			Reason:  "unsupported_alter_object",
			Message: "only ALTER TABLE is supported when rollback protection is enabled",
		}
	}
	schemaName, schemaExplicit, tableName, _, err := extractPostgresRangeVar(defaultSchema, stmt.GetRelation())
	if err != nil {
		return nil, err
	}
	cmds := stmt.GetCmds()
	if len(cmds) == 0 {
		return nil, &CapabilityError{Reason: "invalid_alter_table_syntax", Message: "incomplete ALTER TABLE statement"}
	}
	if len(cmds) > 1 {
		return nil, &CapabilityError{
			Reason:  "multi_action_alter_table_unsupported",
			Message: "multi-action ALTER TABLE (comma-separated actions) must be split into individual statements when rollback protection is enabled",
		}
	}

	cmd := cmds[0].GetAlterTableCmd()
	if cmd == nil {
		return nil, &CapabilityError{Reason: "invalid_alter_table_syntax", Message: "invalid ALTER TABLE command"}
	}

	switch cmd.GetSubtype() {
	case pgnode.AlterTableType_AT_DropColumn:
		if cmd.GetBehavior() == pgnode.DropBehavior_DROP_CASCADE {
			return nil, &CapabilityError{
				Reason:  "drop_column_cascade_unsupported",
				Message: "ALTER TABLE DROP COLUMN ... CASCADE is not supported when rollback protection is enabled",
			}
		}
		colName := cmd.GetName()
		if colName == "" {
			return nil, &CapabilityError{
				Reason:  "invalid_drop_column_syntax",
				Message: "missing column name in ALTER TABLE DROP COLUMN",
			}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindDropColumn,
			SchemaName:     schemaName,
			SchemaExplicit: schemaExplicit,
			TableName:      tableName,
			ColumnName:     colName,
		}, nil

	case pgnode.AlterTableType_AT_AddColumn:
		colDef := cmd.GetDef().GetColumnDef()
		if colDef == nil || colDef.GetColname() == "" {
			return nil, &CapabilityError{Reason: "invalid_add_column_syntax", Message: "missing column definition in ADD COLUMN"}
		}
		colDefSQL := deparsePostgresColumnDef(colDef, trimmed)
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindAddColumn,
			SchemaName:     schemaName,
			SchemaExplicit: schemaExplicit,
			TableName:      tableName,
			ColumnName:     colDef.GetColname(),
			ColumnDefSQL:   colDefSQL,
		}, nil

	case pgnode.AlterTableType_AT_AlterColumnType,
		pgnode.AlterTableType_AT_ColumnDefault,
		pgnode.AlterTableType_AT_DropNotNull,
		pgnode.AlterTableType_AT_SetNotNull:
		colName := cmd.GetName()
		if colName == "" {
			return nil, &CapabilityError{Reason: "invalid_alter_column_syntax", Message: "missing column name in ALTER COLUMN"}
		}
		if cmd.GetSubtype() == pgnode.AlterTableType_AT_AlterColumnType {
			colDef := cmd.GetDef().GetColumnDef()
			if colDef != nil && colDef.GetRawDefault() != nil {
				// PostgreSQL AST stores ALTER COLUMN TYPE ... USING <expr> in ColumnDef.RawDefault
				return nil, &CapabilityError{
					Reason:  "alter_column_type_using_expression_unsupported",
					Message: "ALTER COLUMN TYPE ... USING is potentially lossy and rejected when rollback protection is enabled",
				}
			}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindModifyColumn,
			SchemaName:     schemaName,
			SchemaExplicit: schemaExplicit,
			TableName:      tableName,
			ColumnName:     colName,
			PostColumnName: colName,
			ColumnDefSQL:   trimmed,
		}, nil

	case pgnode.AlterTableType_AT_AddConstraint:
		con := cmd.GetDef().GetConstraint()
		if con == nil {
			return nil, &CapabilityError{Reason: "invalid_add_constraint", Message: "missing constraint definition in ADD CONSTRAINT"}
		}
		if con.GetConname() == "" {
			return nil, &CapabilityError{
				Reason:  "unnamed_constraint_unsupported",
				Message: "ALTER TABLE ADD PRIMARY KEY / FOREIGN KEY / CHECK requires an explicit CONSTRAINT <name> clause when rollback protection is enabled",
			}
		}
		typeHint := ""
		switch con.GetContype() {
		case pgnode.ConstrType_CONSTR_FOREIGN:
			typeHint = "FOREIGN KEY"
		case pgnode.ConstrType_CONSTR_UNIQUE:
			typeHint = "UNIQUE"
		case pgnode.ConstrType_CONSTR_CHECK:
			typeHint = "CHECK"
		case pgnode.ConstrType_CONSTR_PRIMARY:
			typeHint = "PRIMARY KEY"
		}
		return &ParsedStatement{
			RawSQL:             rawSQL,
			TrimmedSQL:         trimmed,
			OperationType:      OperationTypeDDL,
			Kind:               KindAddConstraint,
			SchemaName:         schemaName,
			SchemaExplicit:     schemaExplicit,
			TableName:          tableName,
			ConstraintName:     con.GetConname(),
			ConstraintTypeHint: typeHint,
			ConstraintDefSQL:   trimmed,
		}, nil

	case pgnode.AlterTableType_AT_DropConstraint:
		if cmd.GetBehavior() == pgnode.DropBehavior_DROP_CASCADE {
			return nil, &CapabilityError{
				Reason:  "drop_constraint_cascade_unsupported",
				Message: "DROP CONSTRAINT ... CASCADE is not supported when rollback protection is enabled",
			}
		}
		if cmd.GetName() == "" {
			return nil, &CapabilityError{Reason: "invalid_drop_constraint", Message: "missing constraint name in DROP CONSTRAINT"}
		}
		return &ParsedStatement{
			RawSQL:         rawSQL,
			TrimmedSQL:     trimmed,
			OperationType:  OperationTypeDDL,
			Kind:           KindDropConstraint,
			SchemaName:     schemaName,
			SchemaExplicit: schemaExplicit,
			TableName:      tableName,
			ConstraintName: cmd.GetName(),
		}, nil

	default:
		return nil, &CapabilityError{
			Reason:  "unsupported_alter_table_action",
			Message: fmt.Sprintf("PostgreSQL ALTER TABLE %s is not supported when rollback protection is enabled", cmd.GetSubtype().String()),
		}
	}
}

func deparsePostgresColumnDef(colDef *pgnode.ColumnDef, fallback string) string {
	res := &pgnode.ParseResult{
		Stmts: []*pgnode.RawStmt{
			{
				Stmt: &pgnode.Node{
					Node: &pgnode.Node_CreateStmt{
						CreateStmt: &pgnode.CreateStmt{
							Relation: &pgnode.RangeVar{Relname: "__yr_tmp"},
							TableElts: []*pgnode.Node{
								{
									Node: &pgnode.Node_ColumnDef{
										ColumnDef: colDef,
									},
								},
							},
						},
					},
				},
			},
		},
	}
	out, err := pg_query.Deparse(res)
	if err != nil {
		return fallback
	}
	openIdx := strings.IndexByte(out, '(')
	closeIdx := strings.LastIndexByte(out, ')')
	if openIdx >= 0 && closeIdx > openIdx {
		inner := strings.TrimSpace(out[openIdx+1 : closeIdx])
		colName := colDef.GetColname()
		if strings.HasPrefix(strings.ToLower(inner), strings.ToLower(colName)+" ") {
			return strings.TrimSpace(inner[len(colName):])
		}
		quoted := QuoteIdent("postgres", colName)
		if strings.HasPrefix(inner, quoted+" ") {
			return strings.TrimSpace(inner[len(quoted):])
		}
		return inner
	}
	return fallback
}
