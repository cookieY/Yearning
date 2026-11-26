// ast_postgres_ddl.go holds ast_postgres domain helpers and operations.
package rollback

import (
	"fmt"
	pgnode "github.com/pganalyze/pg_query_go/v6"
	pg_query "github.com/wasilibs/go-pgquery"
	"strings"
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
