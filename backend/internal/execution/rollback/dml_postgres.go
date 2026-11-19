package rollback

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// ExecuteAndCapturePostgresDML performs version-aware PostgreSQL DML forensics and inverse SQL generation:
// - PostgreSQL 18+ (server_version_num >= 180000): native single-statement RETURNING WITH (OLD AS ..., NEW AS ...) captures before and after images simultaneously;
// - PostgreSQL < 18: in-transaction SELECT ... FOR UPDATE (pre-update lock and before-image) + RETURNING * (INSERT/DELETE/UPDATE after-image).
func ExecuteAndCapturePostgresDML(
	ctx context.Context,
	adapter database.Adapter,
	pool *database.PurposePool,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, *database.TableRollbackMetadata, string, error) {
	if maxRows <= 0 {
		maxRows = DefaultMaxCapturedRowsPerStatement
	}
	started := time.Now().UTC()
	metaCtx := ctx
	if !parsed.SchemaExplicit {
		metaCtx = database.WithRollbackAllowSearchPathFallback(ctx, true)
	}
	meta, err := database.InspectTableRollbackMetadata(metaCtx, pool, tx, parsed.SchemaName, parsed.TableName)
	if err != nil {
		if database.ClassifyError("postgresql", err).Class == database.ClassCancelled {
			return classifyPGStatementFailure(started, err), nil, nil, "", nil
		}
		return nil, nil, nil, "", err
	}
	if !parsed.SchemaExplicit && meta.Exists && meta.SchemaName != "" && meta.SchemaName != parsed.SchemaName {
		parsed.SchemaName = meta.SchemaName
	}
	if err := validateDMLTableCapabilities(meta, parsed); err != nil {
		return nil, nil, meta, "", err
	}

	if meta.ServerVersionNum >= 180000 {
		res, ev, err := executePG18DMLWithReturning(ctx, tx, ordinal, parsed, meta, maxRows)
		return res, ev, meta, StrategyPG18DMLReturning, err
	}
	res, ev, err := executePrePG18DMLWithCapture(ctx, tx, ordinal, parsed, meta, maxRows)
	return res, ev, meta, StrategyPGDMLTxReturning, err
}

func executePG18DMLWithReturning(
	ctx context.Context,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	meta *database.TableRollbackMetadata,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, error) {
	colCount := len(meta.Columns)
	switch parsed.Kind {
	case KindUpdate:
		proj := buildPG18OldNewProjection(meta.Columns)
		query := fmt.Sprintf("%s RETURNING WITH (OLD AS __yr_old, NEW AS __yr_new) %s", parsed.TrimmedSQL, proj)
		started := time.Now().UTC()
		_, combinedRows, err := database.QueryTxRollbackRows(ctx, tx, query)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(combinedRows) > maxRows {
			return nil, nil, &CapabilityError{
				Reason:  "affected_rows_exceed_rollback_limit",
				Message: fmt.Sprintf("statement UPDATE modified %d rows, exceeding rollback capture limit %d", len(combinedRows), maxRows),
			}
		}
		beforeRows := make([]database.RollbackRow, 0, len(combinedRows))
		afterRows := make([]database.RollbackRow, 0, len(combinedRows))
		for _, comb := range combinedRows {
			if len(comb.Cells) != colCount*2 {
				return nil, nil, fmt.Errorf("rollback: pg18 returning expected %d columns, got %d", colCount*2, len(comb.Cells))
			}
			bCells := make([]database.RollbackCell, colCount)
			aCells := make([]database.RollbackCell, colCount)
			for i := 0; i < colCount; i++ {
				bCells[i] = comb.Cells[i]
				bCells[i].Column = meta.Columns[i].Name
				aCells[i] = comb.Cells[colCount+i]
				aCells[i].Column = meta.Columns[i].Name
			}
			beforeRows = append(beforeRows, database.RollbackRow{Cells: bCells})
			afterRows = append(afterRows, database.RollbackRow{Cells: aCells})
		}
		inverseStmts, err := buildInverseUpdateStatements("postgresql", meta, beforeRows, afterRows)
		if err != nil {
			return nil, nil, err
		}
		res := &database.StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    int64(len(combinedRows)),
			EngineCodeClass: database.ClassSuccess,
			Outcome:         database.OutcomeSuccess,
			SendBoundary:    database.SendBoundarySent,
		}
		return res, &StatementEvidence{
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

	case KindInsert:
		proj := buildQuotedColumnProjection("postgresql", meta.Columns, `"__yr_new"`)
		query := fmt.Sprintf("%s RETURNING WITH (NEW AS __yr_new) %s", parsed.TrimmedSQL, proj)
		started := time.Now().UTC()
		_, afterRows, err := database.QueryTxRollbackRows(ctx, tx, query)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(afterRows) > maxRows {
			return nil, nil, &CapabilityError{
				Reason:  "affected_rows_exceed_rollback_limit",
				Message: fmt.Sprintf("statement INSERT inserted %d rows, exceeding rollback capture limit %d", len(afterRows), maxRows),
			}
		}
		inverseStmts, err := buildInverseInsertStatements("postgresql", meta, afterRows)
		if err != nil {
			return nil, nil, err
		}
		res := &database.StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    int64(len(afterRows)),
			EngineCodeClass: database.ClassSuccess,
			Outcome:         database.OutcomeSuccess,
			SendBoundary:    database.SendBoundarySent,
		}
		return res, &StatementEvidence{
			Ordinal:            ordinal,
			OriginalSQL:        parsed.RawSQL,
			Kind:               KindInsert,
			SchemaName:         meta.SchemaName,
			TableName:          meta.TableName,
			IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
			AfterRows:          afterRows,
			InverseStatements:  inverseStmts,
		}, nil

	case KindDelete:
		proj := buildQuotedColumnProjection("postgresql", meta.Columns, `"__yr_old"`)
		query := fmt.Sprintf("%s RETURNING WITH (OLD AS __yr_old) %s", parsed.TrimmedSQL, proj)
		started := time.Now().UTC()
		_, beforeRows, err := database.QueryTxRollbackRows(ctx, tx, query)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(beforeRows) > maxRows {
			return nil, nil, &CapabilityError{
				Reason:  "affected_rows_exceed_rollback_limit",
				Message: fmt.Sprintf("statement DELETE deleted %d rows, exceeding rollback capture limit %d", len(beforeRows), maxRows),
			}
		}
		inverseStmts, err := buildInverseDeleteStatements("postgresql", meta, beforeRows)
		if err != nil {
			return nil, nil, err
		}
		res := &database.StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    int64(len(beforeRows)),
			EngineCodeClass: database.ClassSuccess,
			Outcome:         database.OutcomeSuccess,
			SendBoundary:    database.SendBoundarySent,
		}
		return res, &StatementEvidence{
			Ordinal:            ordinal,
			OriginalSQL:        parsed.RawSQL,
			Kind:               KindDelete,
			SchemaName:         meta.SchemaName,
			TableName:          meta.TableName,
			IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
			BeforeRows:         beforeRows,
			InverseStatements:  inverseStmts,
		}, nil

	default:
		return nil, nil, &CapabilityError{Reason: "unsupported_pg_dml_kind", Message: "unsupported PostgreSQL DML kind"}
	}
}

func executePrePG18DMLWithCapture(
	ctx context.Context,
	tx database.DMLTransaction,
	ordinal int,
	parsed *ParsedStatement,
	meta *database.TableRollbackMetadata,
	maxRows int,
) (*database.StatementResult, *StatementEvidence, error) {
	selectCols := buildQuotedColumnProjection("postgresql", meta.Columns, "")
	orderByPK := buildQuotedOrderBy("postgresql", meta.IdentityKeyColumns, "")

	switch parsed.Kind {
	case KindUpdate:
		aliasClause := ""
		tablePrefix := ""
		if parsed.TableAlias != "" {
			aliasClause = " AS " + QuoteIdent("postgresql", parsed.TableAlias)
			tablePrefix = QuoteIdent("postgresql", parsed.TableAlias)
		}
		lockSQL := fmt.Sprintf(
			"SELECT %s FROM %s%s WHERE %s ORDER BY %s FOR UPDATE",
			buildQuotedColumnProjection("postgresql", meta.Columns, tablePrefix),
			QualifiedTable("postgresql", meta.SchemaName, meta.TableName),
			aliasClause,
			parsed.WhereClause,
			buildQuotedOrderBy("postgresql", meta.IdentityKeyColumns, tablePrefix),
		)
		started := time.Now().UTC()
		_, beforeRows, err := database.QueryTxRollbackRows(ctx, tx, lockSQL)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(beforeRows) > maxRows {
			return nil, nil, &CapabilityError{
				Reason:  "affected_rows_exceed_rollback_limit",
				Message: fmt.Sprintf("statement UPDATE matched %d rows, exceeding rollback capture limit %d", len(beforeRows), maxRows),
			}
		}

		returningCols := buildQuotedColumnProjection("postgresql", meta.Columns, tablePrefix)
		updSQL := fmt.Sprintf("%s RETURNING %s", parsed.TrimmedSQL, returningCols)
		_, afterRows, err := database.QueryTxRollbackRows(ctx, tx, updSQL)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(afterRows) != len(beforeRows) {
			return nil, nil, &CapabilityError{
				Reason:  "update_affected_rows_mismatch",
				Message: fmt.Sprintf("statement UPDATE modified %d rows, but before-image locked %d rows", len(afterRows), len(beforeRows)),
			}
		}
		inverseStmts, err := buildInverseUpdateStatements("postgresql", meta, beforeRows, afterRows)
		if err != nil {
			return nil, nil, err
		}
		res := &database.StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    int64(len(afterRows)),
			EngineCodeClass: database.ClassSuccess,
			Outcome:         database.OutcomeSuccess,
			SendBoundary:    database.SendBoundarySent,
		}
		return res, &StatementEvidence{
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

	case KindInsert:
		insSQL := fmt.Sprintf("%s RETURNING %s", parsed.TrimmedSQL, selectCols)
		started := time.Now().UTC()
		_, afterRows, err := database.QueryTxRollbackRows(ctx, tx, insSQL)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(afterRows) > maxRows {
			return nil, nil, &CapabilityError{
				Reason:  "affected_rows_exceed_rollback_limit",
				Message: fmt.Sprintf("statement INSERT inserted %d rows, exceeding rollback capture limit %d", len(afterRows), maxRows),
			}
		}
		inverseStmts, err := buildInverseInsertStatements("postgresql", meta, afterRows)
		if err != nil {
			return nil, nil, err
		}
		res := &database.StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    int64(len(afterRows)),
			EngineCodeClass: database.ClassSuccess,
			Outcome:         database.OutcomeSuccess,
			SendBoundary:    database.SendBoundarySent,
		}
		return res, &StatementEvidence{
			Ordinal:            ordinal,
			OriginalSQL:        parsed.RawSQL,
			Kind:               KindInsert,
			SchemaName:         meta.SchemaName,
			TableName:          meta.TableName,
			IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
			AfterRows:          afterRows,
			InverseStatements:  inverseStmts,
		}, nil

	case KindDelete:
		tablePrefix := ""
		if parsed.TableAlias != "" {
			tablePrefix = QuoteIdent("postgresql", parsed.TableAlias)
		}
		delSQL := fmt.Sprintf("%s RETURNING %s", parsed.TrimmedSQL, buildQuotedColumnProjection("postgresql", meta.Columns, tablePrefix))
		started := time.Now().UTC()
		_, beforeRows, err := database.QueryTxRollbackRows(ctx, tx, delSQL)
		if err != nil {
			return classifyPGStatementFailure(started, err), nil, nil
		}
		if len(beforeRows) > maxRows {
			return nil, nil, &CapabilityError{
				Reason:  "affected_rows_exceed_rollback_limit",
				Message: fmt.Sprintf("statement DELETE deleted %d rows, exceeding rollback capture limit %d", len(beforeRows), maxRows),
			}
		}
		_ = orderByPK
		inverseStmts, err := buildInverseDeleteStatements("postgresql", meta, beforeRows)
		if err != nil {
			return nil, nil, err
		}
		res := &database.StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    int64(len(beforeRows)),
			EngineCodeClass: database.ClassSuccess,
			Outcome:         database.OutcomeSuccess,
			SendBoundary:    database.SendBoundarySent,
		}
		return res, &StatementEvidence{
			Ordinal:            ordinal,
			OriginalSQL:        parsed.RawSQL,
			Kind:               KindDelete,
			SchemaName:         meta.SchemaName,
			TableName:          meta.TableName,
			IdentityKeyColumns: append([]string(nil), meta.IdentityKeyColumns...),
			BeforeRows:         beforeRows,
			InverseStatements:  inverseStmts,
		}, nil

	default:
		return nil, nil, &CapabilityError{Reason: "unsupported_pg_dml_kind", Message: "unsupported PostgreSQL DML kind"}
	}
}

func buildPG18OldNewProjection(cols []database.RollbackColumnMeta) string {
	parts := make([]string, 0, len(cols)*2)
	for i, c := range cols {
		parts = append(parts, fmt.Sprintf(`"__yr_old".%s AS "__old_%d"`, QuoteIdent("postgresql", c.Name), i))
	}
	for i, c := range cols {
		parts = append(parts, fmt.Sprintf(`"__yr_new".%s AS "__new_%d"`, QuoteIdent("postgresql", c.Name), i))
	}
	return strings.Join(parts, ", ")
}

func classifyPGStatementFailure(started time.Time, err error) *database.StatementResult {
	cls := database.ClassifyError("postgresql", err)
	result := &database.StatementResult{
		StartedAt:            started,
		FinishedAt:           time.Now().UTC(),
		EngineCodeClass:      cls.Class,
		SanitizedFailureName: cls.FailureName,
		SendBoundary:         database.SendBoundarySent,
	}
	switch cls.Class {
	case database.ClassSyntaxError, database.ClassNotFound, database.ClassConstraintViolation, database.ClassInsufficientPriv, database.ClassTimeout:
		result.Outcome = database.OutcomeFailed
	case database.ClassCancelled:
		result.Outcome = database.OutcomeCancelled
	default:
		result.Outcome = database.OutcomeResultUnknown
	}
	return result
}
