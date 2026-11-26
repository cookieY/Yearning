package rollback

import (
	"fmt"
	pgnode "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
	"strings"
)

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
