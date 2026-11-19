package rollback

import (
	"context"
	"fmt"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// ExecuteAndCaptureMySQLDML performs pre-execution forensics (SELECT ... FOR UPDATE),
// original DML execution, post-execution image verification, and deterministic inverse SQL
// generation within the same MySQL transaction.
func ExecuteAndCaptureMySQLDML(
	ctx context.Context,
	adapter database.Adapter,
	pool *database.PurposePool,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, *database.TableRollbackMetadata, error) {
	if maxRows <= 0 {
		maxRows = DefaultMaxCapturedRowsPerStatement
	}
	meta, err := database.InspectTableRollbackMetadata(ctx, pool, tx, parsed.SchemaName, parsed.TableName)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := validateDMLTableCapabilities(meta, parsed); err != nil {
		return nil, nil, meta, err
	}

	switch parsed.Kind {
	case KindUpdate:
		res, ev, err := executeMySQLUpdateWithCapture(ctx, adapter, tx, ordinal, parsed, meta, maxRows)
		return res, ev, meta, err
	case KindDelete:
		res, ev, err := executeMySQLDeleteWithCapture(ctx, adapter, tx, ordinal, parsed, meta, maxRows)
		return res, ev, meta, err
	case KindInsert:
		res, ev, err := executeMySQLInsertWithCapture(ctx, adapter, tx, ordinal, parsed, meta, maxRows)
		return res, ev, meta, err
	default:
		return nil, nil, meta, &CapabilityError{
			Reason:  "unsupported_mysql_dml_kind",
			Message: fmt.Sprintf("unsupported MySQL DML kind %s", parsed.Kind),
		}
	}
}

func validateDMLTableCapabilities(meta *database.TableRollbackMetadata, parsed *ParsedStatement) error {
	if meta == nil || !meta.Exists {
		return &CapabilityError{
			Reason:  "target_table_not_found",
			Message: fmt.Sprintf("target table %s.%s does not exist", parsed.SchemaName, parsed.TableName),
		}
	}
	if len(meta.IdentityKeyColumns) == 0 {
		return &CapabilityError{
			Reason:  "table_missing_primary_or_not_null_unique_key",
			Message: fmt.Sprintf("table %s.%s has no PRIMARY KEY or NOT NULL UNIQUE key required for deterministic row rollback", meta.SchemaName, meta.TableName),
		}
	}
	if meta.TriggerCount > 0 {
		return &CapabilityError{
			Reason:  "table_has_triggers",
			Message: fmt.Sprintf("table %s.%s has %d trigger(s); trigger side effects cannot be deterministically rolled back", meta.SchemaName, meta.TableName, meta.TriggerCount),
		}
	}
	if meta.IsPartitioned && meta.Engine == "postgresql" {
		return &CapabilityError{
			Reason:  "partitioned_table_dml_unsupported",
			Message: fmt.Sprintf("partitioned table %s.%s DML rollback is not supported", meta.SchemaName, meta.TableName),
		}
	}
	if unsafe, detail := meta.HasUnsafeIncomingCascade(parsed.Kind == KindUpdate, parsed.Kind == KindDelete); unsafe {
		return &CapabilityError{
			Reason:  "foreign_key_cascade_unsupported",
			Message: fmt.Sprintf("table %s.%s has cascading child foreign key (%s)", meta.SchemaName, meta.TableName, detail),
		}
	}
	if parsed.Kind == KindUpdate {
		for _, updCol := range parsed.UpdatedColumns {
			for _, pkCol := range meta.IdentityKeyColumns {
				if strings.EqualFold(updCol, pkCol) {
					return &CapabilityError{
						Reason:  "primary_key_modification_unsupported",
						Message: fmt.Sprintf("statement UPDATE modifying identity key column %s on %s.%s is not supported when rollback protection is enabled", pkCol, meta.SchemaName, meta.TableName),
					}
				}
			}
		}
	}
	return nil
}

func executeMySQLUpdateWithCapture(
	ctx context.Context,
	adapter database.Adapter,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	meta *database.TableRollbackMetadata,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, error) {
	selectCols := buildQuotedColumnProjection("mysql", meta.Columns, "")
	orderByPK := buildQuotedOrderBy("mysql", meta.IdentityKeyColumns, "")
	aliasClause := ""
	if parsed.TableAlias != "" {
		aliasClause = " AS " + QuoteIdent("mysql", parsed.TableAlias)
	}
	lockSQL := fmt.Sprintf(
		"SELECT %s FROM %s%s WHERE %s ORDER BY %s FOR UPDATE",
		selectCols,
		QualifiedTable("mysql", meta.SchemaName, meta.TableName),
		aliasClause,
		parsed.WhereClause,
		orderByPK,
	)
	_, beforeRows, err := database.QueryTxRollbackRows(ctx, tx, lockSQL)
	if err != nil {
		return nil, nil, fmt.Errorf("rollback: mysql before-image SELECT FOR UPDATE failed: %w", err)
	}
	if len(beforeRows) > maxRows {
		return nil, nil, &CapabilityError{
			Reason:  "affected_rows_exceed_rollback_limit",
			Message: fmt.Sprintf("statement UPDATE matched %d rows, exceeding rollback capture limit %d", len(beforeRows), maxRows),
		}
	}

	stmtRes, _, err := database.ExecuteDMLWithLastInsertID(ctx, adapter, tx, parsed.RawSQL)
	if err != nil {
		return stmtRes, nil, err
	}
	if stmtRes.Outcome != database.OutcomeSuccess {
		return stmtRes, nil, nil
	}

	if len(beforeRows) == 0 {
		if stmtRes.AffectedRows > 0 {
			return stmtRes, nil, &CapabilityError{
				Reason:  "update_affected_rows_mismatch",
				Message: fmt.Sprintf("pre-execution SELECT FOR UPDATE captured 0 rows but UPDATE modified %d rows", stmtRes.AffectedRows),
			}
		}
		return stmtRes, &StatementEvidence{
			Ordinal:            ordinal,
			OriginalSQL:        parsed.RawSQL,
			Kind:               KindUpdate,
			SchemaName:         meta.SchemaName,
			TableName:          meta.TableName,
			IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
		}, nil
	}

	if stmtRes.AffectedRows > int64(len(beforeRows)) {
		return stmtRes, nil, &CapabilityError{
			Reason:  "update_affected_rows_exceeded_before_image",
			Message: fmt.Sprintf("statement UPDATE modified %d rows, exceeding %d locked before-image rows", stmtRes.AffectedRows, len(beforeRows)),
		}
	}

	pkWhere, err := BuildMultiRowPKWhereClause("mysql", meta.IdentityKeyColumns, beforeRows)
	if err != nil {
		return stmtRes, nil, err
	}
	afterSQL := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY %s FOR UPDATE",
		selectCols,
		QualifiedTable("mysql", meta.SchemaName, meta.TableName),
		pkWhere,
		orderByPK,
	)
	_, afterRows, err := database.QueryTxRollbackRows(ctx, tx, afterSQL)
	if err != nil {
		return stmtRes, nil, fmt.Errorf("rollback: mysql after-image SELECT failed: %w", err)
	}
	if len(afterRows) != len(beforeRows) {
		return stmtRes, nil, &CapabilityError{
			Reason:  "update_after_image_row_count_mismatch",
			Message: fmt.Sprintf("expected %d rows in after-image, got %d", len(beforeRows), len(afterRows)),
		}
	}

	inverseStmts, err := buildInverseUpdateStatements("mysql", meta, beforeRows, afterRows)
	if err != nil {
		return stmtRes, nil, err
	}
	return stmtRes, &StatementEvidence{
		Ordinal:            ordinal,
		OriginalSQL:        parsed.RawSQL,
		Kind:               KindUpdate,
		SchemaName:         meta.SchemaName,
		TableName:          meta.TableName,
		IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
		BeforeRows:         beforeRows,
		AfterRows:          afterRows,
		InverseStatements:  inverseStmts,
	}, nil
}

func executeMySQLDeleteWithCapture(
	ctx context.Context,
	adapter database.Adapter,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	meta *database.TableRollbackMetadata,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, error) {
	selectCols := buildQuotedColumnProjection("mysql", meta.Columns, "")
	orderByPK := buildQuotedOrderBy("mysql", meta.IdentityKeyColumns, "")
	aliasClause := ""
	if parsed.TableAlias != "" {
		aliasClause = " AS " + QuoteIdent("mysql", parsed.TableAlias)
	}
	lockSQL := fmt.Sprintf(
		"SELECT %s FROM %s%s WHERE %s ORDER BY %s FOR UPDATE",
		selectCols,
		QualifiedTable("mysql", meta.SchemaName, meta.TableName),
		aliasClause,
		parsed.WhereClause,
		orderByPK,
	)
	_, beforeRows, err := database.QueryTxRollbackRows(ctx, tx, lockSQL)
	if err != nil {
		return nil, nil, fmt.Errorf("rollback: mysql delete before-image SELECT FOR UPDATE failed: %w", err)
	}
	if len(beforeRows) > maxRows {
		return nil, nil, &CapabilityError{
			Reason:  "affected_rows_exceed_rollback_limit",
			Message: fmt.Sprintf("statement DELETE matched %d rows, exceeding rollback capture limit %d", len(beforeRows), maxRows),
		}
	}

	stmtRes, _, err := database.ExecuteDMLWithLastInsertID(ctx, adapter, tx, parsed.RawSQL)
	if err != nil {
		return stmtRes, nil, err
	}
	if stmtRes.Outcome != database.OutcomeSuccess {
		return stmtRes, nil, nil
	}
	if stmtRes.AffectedRows != int64(len(beforeRows)) {
		return stmtRes, nil, &CapabilityError{
			Reason:  "delete_affected_rows_mismatch",
			Message: fmt.Sprintf("statement DELETE modified %d rows, but before-image captured %d rows", stmtRes.AffectedRows, len(beforeRows)),
		}
	}

	inverseStmts, err := buildInverseDeleteStatements("mysql", meta, beforeRows)
	if err != nil {
		return stmtRes, nil, err
	}
	return stmtRes, &StatementEvidence{
		Ordinal:            ordinal,
		OriginalSQL:        parsed.RawSQL,
		Kind:               KindDelete,
		SchemaName:         meta.SchemaName,
		TableName:          meta.TableName,
		IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
		BeforeRows:         beforeRows,
		InverseStatements:  inverseStmts,
	}, nil
}

func executeMySQLInsertWithCapture(
	ctx context.Context,
	adapter database.Adapter,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	meta *database.TableRollbackMetadata,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, error) {
	tupleCount := len(parsed.InsertTuples)
	if tupleCount > maxRows {
		return nil, nil, &CapabilityError{
			Reason:  "affected_rows_exceed_rollback_limit",
			Message: fmt.Sprintf("statement INSERT tuple count %d exceeds rollback capture limit %d", tupleCount, maxRows),
		}
	}

	// Row identity determination strategy:
	// 1. VALUES explicitly provides non-NULL/non-DEFAULT literals for all IdentityKeyColumns;
	// 2. A single-column AUTO_INCREMENT primary key assigned by MySQL is captured accurately via LastInsertId combined with in-transaction verification.
	explicitPKPredicates, hasExplicitPK := extractExplicitInsertPKPredicates("mysql", parsed, meta)
	autoIncCol := ""
	if !hasExplicitPK {
		if len(meta.IdentityKeyColumns) == 1 {
			if colMeta, ok := meta.ColumnByName(meta.IdentityKeyColumns[0]); ok && colMeta.IsAutoIncrement {
				autoIncCol = colMeta.Name
			}
		}
		if autoIncCol == "" {
			return nil, nil, &CapabilityError{
				Reason:  "mysql_insert_missing_row_identity",
				Message: fmt.Sprintf("MySQL INSERT into %s.%s must either supply explicit identity key values or use an AUTO_INCREMENT key", meta.SchemaName, meta.TableName),
			}
		}
	}

	stmtRes, lastInsertID, err := database.ExecuteDMLWithLastInsertID(ctx, adapter, tx, parsed.RawSQL)
	if err != nil {
		return stmtRes, nil, err
	}
	if stmtRes.Outcome != database.OutcomeSuccess {
		return stmtRes, nil, nil
	}
	if stmtRes.AffectedRows != int64(tupleCount) {
		return stmtRes, nil, &CapabilityError{
			Reason:  "insert_affected_rows_mismatch",
			Message: fmt.Sprintf("statement INSERT expected %d affected rows, got %d", tupleCount, stmtRes.AffectedRows),
		}
	}

	selectCols := buildQuotedColumnProjection("mysql", meta.Columns, "")
	orderByPK := buildQuotedOrderBy("mysql", meta.IdentityKeyColumns, "")
	var afterQuery string
	if hasExplicitPK {
		afterQuery = fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s ORDER BY %s FOR UPDATE",
			selectCols,
			QualifiedTable("mysql", meta.SchemaName, meta.TableName),
			strings.Join(explicitPKPredicates, " OR "),
			orderByPK,
		)
	} else {
		if lastInsertID <= 0 {
			return stmtRes, nil, &CapabilityError{
				Reason:  "mysql_insert_last_insert_id_unavailable",
				Message: "MySQL LastInsertId returned 0 for AUTO_INCREMENT INSERT",
			}
		}
		qPK := QuoteIdent("mysql", autoIncCol)
		afterQuery = fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s >= %d ORDER BY %s ASC LIMIT %d FOR UPDATE",
			selectCols,
			QualifiedTable("mysql", meta.SchemaName, meta.TableName),
			qPK,
			lastInsertID,
			qPK,
			tupleCount,
		)
	}

	_, afterRows, err := database.QueryTxRollbackRows(ctx, tx, afterQuery)
	if err != nil {
		return stmtRes, nil, fmt.Errorf("rollback: mysql insert after-image SELECT failed: %w", err)
	}
	if len(afterRows) != tupleCount {
		return stmtRes, nil, &CapabilityError{
			Reason:  "insert_after_image_count_mismatch",
			Message: fmt.Sprintf("statement INSERT captured %d after-image rows, expected %d", len(afterRows), tupleCount),
		}
	}

	inverseStmts, err := buildInverseInsertStatements("mysql", meta, afterRows)
	if err != nil {
		return stmtRes, nil, err
	}
	return stmtRes, &StatementEvidence{
		Ordinal:            ordinal,
		OriginalSQL:        parsed.RawSQL,
		Kind:               KindInsert,
		SchemaName:         meta.SchemaName,
		TableName:          meta.TableName,
		IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
		AfterRows:          afterRows,
		InverseStatements:  inverseStmts,
	}, nil
}

func extractExplicitInsertPKPredicates(engine string, parsed *ParsedStatement, meta *database.TableRollbackMetadata) ([]string, bool) {
	colNames := parsed.InsertColumns
	if len(colNames) == 0 {
		for _, c := range meta.Columns {
			colNames = append(colNames, c.Name)
		}
	}
	pkIdx := make([]int, len(meta.IdentityKeyColumns))
	for i, pk := range meta.IdentityKeyColumns {
		found := -1
		for j, col := range colNames {
			if strings.EqualFold(pk, col) {
				found = j
				break
			}
		}
		if found < 0 {
			return nil, false
		}
		pkIdx[i] = found
	}

	predicates := make([]string, 0, len(parsed.InsertTuples))
	for _, tuple := range parsed.InsertTuples {
		parts := make([]string, 0, len(meta.IdentityKeyColumns))
		for i, pk := range meta.IdentityKeyColumns {
			pos := pkIdx[i]
			if pos >= len(tuple) {
				return nil, false
			}
			expr := strings.TrimSpace(tuple[pos])
			upper := strings.ToUpper(expr)
			if expr == "" || upper == "NULL" || upper == "DEFAULT" {
				return nil, false
			}
			if !isSafeSimpleLiteral(expr) {
				return nil, false
			}
			parts = append(parts, fmt.Sprintf("%s = %s", QuoteIdent(engine, pk), expr))
		}
		predicates = append(predicates, "("+strings.Join(parts, " AND ")+")")
	}
	return predicates, true
}

func isSafeSimpleLiteral(expr string) bool {
	if numericLiteralPattern.MatchString(expr) {
		return true
	}
	if len(expr) >= 2 && expr[0] == '\'' && expr[len(expr)-1] == '\'' {
		return true
	}
	return false
}

func buildQuotedColumnProjection(engine string, cols []database.RollbackColumnMeta, tablePrefix string) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		q := QuoteIdent(engine, c.Name)
		if tablePrefix != "" {
			q = tablePrefix + "." + q
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, ", ")
}

func buildQuotedOrderBy(engine string, pkCols []string, tablePrefix string) string {
	parts := make([]string, 0, len(pkCols))
	for _, col := range pkCols {
		q := QuoteIdent(engine, col)
		if tablePrefix != "" {
			q = tablePrefix + "." + q
		}
		parts = append(parts, q+" ASC")
	}
	return strings.Join(parts, ", ")
}

func buildInverseUpdateStatements(
	engine string,
	meta *database.TableRollbackMetadata,
	beforeRows []database.RollbackRow,
	afterRows []database.RollbackRow,
) ([]string, error) {
	afterByKey := make(map[string]database.RollbackRow, len(afterRows))
	for _, r := range afterRows {
		k, err := RowIdentityKey(meta.IdentityKeyColumns, r)
		if err != nil {
			return nil, err
		}
		afterByKey[k] = r
	}

	qualTable := QualifiedTable(engine, meta.SchemaName, meta.TableName)
	stmts := make([]string, 0, len(beforeRows))
	for i := len(beforeRows) - 1; i >= 0; i-- {
		before := beforeRows[i]
		k, err := RowIdentityKey(meta.IdentityKeyColumns, before)
		if err != nil {
			return nil, err
		}
		after, ok := afterByKey[k]
		if !ok {
			return nil, &CapabilityError{
				Reason:  "update_row_identity_changed",
				Message: "row identity key changed during UPDATE; cannot match before-image to after-image",
			}
		}

		var assignments []string
		for _, col := range meta.Columns {
			if col.IsGenerated || col.IsPrimaryKey {
				continue
			}
			bCell, okB := before.Get(col.Name)
			aCell, okA := after.Get(col.Name)
			if !okB || !okA {
				continue
			}
			if !bCell.Equal(aCell) {
				assignments = append(assignments, fmt.Sprintf("%s = %s", QuoteIdent(engine, col.Name), FormatCellLiteral(engine, bCell)))
			}
		}
		// Even if all values of a row matched by UPDATE remain identical, generate an idempotent restore statement on the first non-generated, non-PK column to maintain observable rollback SQL.
		if len(assignments) == 0 {
			for _, col := range meta.Columns {
				if col.IsGenerated {
					continue
				}
				if bCell, okB := before.Get(col.Name); okB {
					assignments = append(assignments, fmt.Sprintf("%s = %s", QuoteIdent(engine, col.Name), FormatCellLiteral(engine, bCell)))
					break
				}
			}
		}
		wherePK, err := BuildPKWhereClause(engine, meta.IdentityKeyColumns, after)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, fmt.Sprintf(
			"UPDATE %s SET %s WHERE %s;",
			qualTable,
			strings.Join(assignments, ", "),
			wherePK,
		))
	}
	return stmts, nil
}

func buildInverseDeleteStatements(
	engine string,
	meta *database.TableRollbackMetadata,
	beforeRows []database.RollbackRow,
) ([]string, error) {
	qualTable := QualifiedTable(engine, meta.SchemaName, meta.TableName)
	var insertCols []string
	var quotedCols []string
	hasIdentityAlways := false
	for _, col := range meta.Columns {
		if col.IsGenerated {
			continue
		}
		if engine == "postgresql" && col.IsIdentity && col.IdentityGeneration == "ALWAYS" {
			hasIdentityAlways = true
		}
		insertCols = append(insertCols, col.Name)
		quotedCols = append(quotedCols, QuoteIdent(engine, col.Name))
	}
	overrideClause := ""
	if hasIdentityAlways {
		overrideClause = " OVERRIDING SYSTEM VALUE"
	}

	stmts := make([]string, 0, len(beforeRows))
	for _, row := range beforeRows {
		vals := make([]string, 0, len(insertCols))
		for _, colName := range insertCols {
			cell, ok := row.Get(colName)
			if !ok {
				return nil, fmt.Errorf("rollback: column %s missing in before-image row", colName)
			}
			vals = append(vals, FormatCellLiteral(engine, cell))
		}
		stmts = append(stmts, fmt.Sprintf(
			"INSERT INTO %s (%s)%s VALUES (%s);",
			qualTable,
			strings.Join(quotedCols, ", "),
			overrideClause,
			strings.Join(vals, ", "),
		))
	}
	return stmts, nil
}

func buildInverseInsertStatements(
	engine string,
	meta *database.TableRollbackMetadata,
	afterRows []database.RollbackRow,
) ([]string, error) {
	qualTable := QualifiedTable(engine, meta.SchemaName, meta.TableName)
	stmts := make([]string, 0, len(afterRows))
	for i := len(afterRows) - 1; i >= 0; i-- {
		wherePK, err := BuildPKWhereClause(engine, meta.IdentityKeyColumns, afterRows[i])
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, fmt.Sprintf(
			"DELETE FROM %s WHERE %s;",
			qualTable,
			wherePK,
		))
	}
	return stmts, nil
}
