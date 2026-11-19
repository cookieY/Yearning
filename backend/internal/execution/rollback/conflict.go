package rollback

import (
	"context"
	"fmt"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// VerifyDMLStatementPreconditionInTx acquires row locks (SELECT ... FOR UPDATE) within the
// rollback transaction for a single DML evidence item about to execute inverse SQL, and compares
// the current row state column-by-column against the post-execution evidence image. Any drift
// returns a *ConflictError, causing the caller to roll back the entire transaction.
func VerifyDMLStatementPreconditionInTx(
	ctx context.Context,
	pool *database.PurposePool,
	tx database.DMLTransaction,
	engine string,
	ev StatementEvidence,
) error {
	meta, err := database.InspectTableRollbackMetadata(ctx, pool, tx, ev.SchemaName, ev.TableName)
	if err != nil {
		return err
	}
	if !meta.Exists {
		return &ConflictError{
			Reason:  "target_table_missing",
			Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName),
		}
	}
	if meta.TriggerCount > 0 {
		return &ConflictError{
			Reason:  "target_table_has_triggers",
			Message: fmt.Sprintf("table %s.%s now has %d trigger(s); refusing rollback", ev.SchemaName, ev.TableName, meta.TriggerCount),
		}
	}
	for _, pkCol := range ev.IdentityKeyColumns {
		if _, ok := meta.ColumnByName(pkCol); !ok {
			return &ConflictError{
				Reason:  "identity_key_column_missing",
				Message: fmt.Sprintf("identity key column %s no longer exists on %s.%s", pkCol, ev.SchemaName, ev.TableName),
			}
		}
	}

	qualTable := QualifiedTable(engine, ev.SchemaName, ev.TableName)
	selectCols := buildQuotedColumnProjection(engine, meta.Columns, "")
	orderByPK := buildQuotedOrderBy(engine, ev.IdentityKeyColumns, "")

	switch ev.Kind {
	case KindInsert, KindUpdate:
		if len(ev.AfterRows) == 0 {
			return nil
		}
		pkWhere, err := BuildMultiRowPKWhereClause(engine, ev.IdentityKeyColumns, ev.AfterRows)
		if err != nil {
			return err
		}
		lockQuery := fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s ORDER BY %s FOR UPDATE",
			selectCols,
			qualTable,
			pkWhere,
			orderByPK,
		)
		_, currentRows, err := database.QueryTxRollbackRows(ctx, tx, lockQuery)
		if err != nil {
			return fmt.Errorf("rollback conflict check query failed: %w", err)
		}
		if len(currentRows) != len(ev.AfterRows) {
			return &ConflictError{
				Reason:  "row_deleted_or_missing_since_execution",
				Message: fmt.Sprintf("expected %d row(s) on %s.%s matching post-execution identity, found %d", len(ev.AfterRows), ev.SchemaName, ev.TableName, len(currentRows)),
			}
		}
		curByKey := make(map[string]database.RollbackRow, len(currentRows))
		for _, r := range currentRows {
			k, kerr := RowIdentityKey(ev.IdentityKeyColumns, r)
			if kerr != nil {
				return kerr
			}
			curByKey[k] = r
		}
		for _, expected := range ev.AfterRows {
			k, kerr := RowIdentityKey(ev.IdentityKeyColumns, expected)
			if kerr != nil {
				return kerr
			}
			actual, ok := curByKey[k]
			if !ok {
				return &ConflictError{
					Reason:  "row_identity_missing_since_execution",
					Message: fmt.Sprintf("row identity %s missing on %s.%s", k, ev.SchemaName, ev.TableName),
				}
			}
			for _, col := range meta.Columns {
				if col.IsGenerated {
					continue
				}
				expCell, okE := expected.Get(col.Name)
				actCell, okA := actual.Get(col.Name)
				if !okE || !okA {
					return &ConflictError{
						Reason:  "schema_column_drifted_since_execution",
						Message: fmt.Sprintf("column %s drifted on %s.%s since original execution", col.Name, ev.SchemaName, ev.TableName),
					}
				}
				if !expCell.Equal(actCell) {
					return &ConflictError{
						Reason:  "row_modified_since_execution",
						Message: fmt.Sprintf("row (%s) column %s on %s.%s was modified after the original ticket executed", k, col.Name, ev.SchemaName, ev.TableName),
					}
				}
			}
		}
		return nil

	case KindDelete:
		if len(ev.BeforeRows) == 0 {
			return nil
		}
		pkWhere, err := BuildMultiRowPKWhereClause(engine, ev.IdentityKeyColumns, ev.BeforeRows)
		if err != nil {
			return err
		}
		pkProj := make([]string, 0, len(ev.IdentityKeyColumns))
		for _, col := range ev.IdentityKeyColumns {
			pkProj = append(pkProj, QuoteIdent(engine, col))
		}
		lockQuery := fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s FOR UPDATE",
			strings.Join(pkProj, ", "),
			qualTable,
			pkWhere,
		)
		_, existingRows, err := database.QueryTxRollbackRows(ctx, tx, lockQuery)
		if err != nil {
			return fmt.Errorf("rollback delete conflict check query failed: %w", err)
		}
		if len(existingRows) > 0 {
			return &ConflictError{
				Reason:  "deleted_row_re_inserted_since_execution",
				Message: fmt.Sprintf("%d deleted row identity key(s) on %s.%s already exist and would collide on rollback INSERT", len(existingRows), ev.SchemaName, ev.TableName),
			}
		}
		return nil

	default:
		return nil
	}
}

// VerifyDDLStatementPrecondition validates current target database schema structure and dependency preconditions before executing a single inverse DDL statement.
func VerifyDDLStatementPrecondition(
	ctx context.Context,
	pool *database.PurposePool,
	engine string,
	ev StatementEvidence,
) error {
	lookupSchema := ev.SchemaName
	lookupTable := ev.TableName
	if ev.Kind == KindRenameTable && ev.PostTableName != "" {
		lookupTable = ev.PostTableName
	}
	curMeta, err := database.InspectTableRollbackMetadata(ctx, pool, nil, lookupSchema, lookupTable)
	if err != nil {
		return err
	}

	switch ev.Kind {
	case KindCreateTable:
		if !curMeta.Exists {
			return &ConflictError{
				Reason:  "created_table_already_dropped",
				Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName),
			}
		}
		if len(curMeta.IncomingForeignKeys) > 0 {
			return &ConflictError{
				Reason:  "created_table_has_dependent_foreign_keys",
				Message: fmt.Sprintf("table %s.%s is referenced by child foreign key %s", ev.SchemaName, ev.TableName, curMeta.IncomingForeignKeys[0].ConstraintName),
			}
		}
		if ev.SchemaAfter != nil && len(curMeta.Columns) != len(ev.SchemaAfter.Columns) {
			return &ConflictError{
				Reason:  "created_table_schema_drifted",
				Message: fmt.Sprintf("table %s.%s schema was modified after creation", ev.SchemaName, ev.TableName),
			}
		}

	case KindDropTable:
		if curMeta.Exists {
			return &ConflictError{
				Reason:  "dropped_table_already_recreated",
				Message: fmt.Sprintf("table %s.%s already exists", ev.SchemaName, ev.TableName),
			}
		}

	case KindAddColumn:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		if _, ok := curMeta.ColumnByName(ev.ColumnName); !ok {
			return &ConflictError{
				Reason:  "added_column_already_missing",
				Message: fmt.Sprintf("column %s no longer exists on %s.%s", ev.ColumnName, ev.SchemaName, ev.TableName),
			}
		}

	case KindDropColumn:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		if _, ok := curMeta.ColumnByName(ev.ColumnName); ok {
			return &ConflictError{
				Reason:  "dropped_column_already_recreated",
				Message: fmt.Sprintf("column %s already exists on %s.%s", ev.ColumnName, ev.SchemaName, ev.TableName),
			}
		}

	case KindRenameColumn:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		if _, ok := curMeta.ColumnByName(ev.PostColumnName); !ok {
			return &ConflictError{
				Reason:  "renamed_column_missing",
				Message: fmt.Sprintf("column %s no longer exists on %s.%s", ev.PostColumnName, ev.SchemaName, ev.TableName),
			}
		}
		if _, ok := curMeta.ColumnByName(ev.ColumnName); ok {
			return &ConflictError{
				Reason:  "original_column_name_occupied",
				Message: fmt.Sprintf("column %s already exists on %s.%s", ev.ColumnName, ev.SchemaName, ev.TableName),
			}
		}

	case KindRenameTable:
		if !curMeta.Exists {
			return &ConflictError{
				Reason:  "renamed_table_missing",
				Message: fmt.Sprintf("table %s.%s no longer exists", lookupSchema, lookupTable),
			}
		}
		origMeta, err := database.InspectTableRollbackMetadata(ctx, pool, nil, ev.SchemaName, ev.TableName)
		if err != nil {
			return err
		}
		if origMeta.Exists {
			return &ConflictError{
				Reason:  "original_table_name_occupied",
				Message: fmt.Sprintf("original table name %s.%s is already occupied", ev.SchemaName, ev.TableName),
			}
		}

	case KindAddIndex:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		if _, ok := curMeta.IndexByName(ev.IndexName); !ok {
			return &ConflictError{
				Reason:  "added_index_already_dropped",
				Message: fmt.Sprintf("index %s no longer exists on %s.%s", ev.IndexName, ev.SchemaName, ev.TableName),
			}
		}

	case KindDropIndex:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		if _, ok := curMeta.IndexByName(ev.IndexName); ok {
			return &ConflictError{
				Reason:  "dropped_index_already_recreated",
				Message: fmt.Sprintf("index %s already exists on %s.%s", ev.IndexName, ev.SchemaName, ev.TableName),
			}
		}

	case KindModifyColumn:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		colName := ev.PostColumnName
		if colName == "" {
			colName = ev.ColumnName
		}
		curCol, ok := curMeta.ColumnByName(colName)
		if !ok {
			return &ConflictError{
				Reason:  "modified_column_missing",
				Message: fmt.Sprintf("column %s no longer exists on %s.%s", colName, ev.SchemaName, ev.TableName),
			}
		}
		if ev.SchemaAfter != nil {
			if afterCol, okA := ev.SchemaAfter.ColumnByName(colName); okA {
				if !strings.EqualFold(curCol.ColumnType, afterCol.ColumnType) || curCol.Nullable != afterCol.Nullable {
					return &ConflictError{
						Reason:  "modified_column_drifted_since_execution",
						Message: fmt.Sprintf("column %s on %s.%s was altered again after the original ticket", colName, ev.SchemaName, ev.TableName),
					}
				}
			}
		}

	case KindAddConstraint:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		_, hasCon := curMeta.ConstraintByName(ev.ConstraintName)
		_, hasIdx := curMeta.IndexByName(ev.ConstraintName)
		if !hasCon && !hasIdx {
			return &ConflictError{
				Reason:  "added_constraint_already_dropped",
				Message: fmt.Sprintf("constraint %s no longer exists on %s.%s", ev.ConstraintName, ev.SchemaName, ev.TableName),
			}
		}

	case KindDropConstraint:
		if !curMeta.Exists {
			return &ConflictError{Reason: "target_table_missing", Message: fmt.Sprintf("table %s.%s no longer exists", ev.SchemaName, ev.TableName)}
		}
		if _, hasCon := curMeta.ConstraintByName(ev.ConstraintName); hasCon {
			return &ConflictError{
				Reason:  "dropped_constraint_already_recreated",
				Message: fmt.Sprintf("constraint %s already exists on %s.%s", ev.ConstraintName, ev.SchemaName, ev.TableName),
			}
		}
	}

	return nil
}
