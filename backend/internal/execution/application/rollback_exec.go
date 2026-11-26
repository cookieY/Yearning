// rollback_exec.go Orchestrates the unified SQL rollback engine within the Execution domain:
// 1. Normal work orders (ticket_type='normal' & rollback_enabled=true):
//   - DML: captures before-images within the same target DB transaction (MySQL SELECT ... FOR UPDATE +
//     PK/LAST_INSERT_ID after-images; PG 18+ RETURNING OLD/NEW; PG <18 SELECT ... FOR UPDATE +
//     RETURNING *), immediately rolls back the transaction (fail-closed) if row count or capability
//     constraints are violated, seals evidence prior to commit, and persists it to order_rollback_evidences after commit.
//   - DDL: captures a before-schema snapshot and validates reversibility prior to execution (failing
//     closed before send on destructive DDL), captures an after-schema snapshot post-execution,
//     verifies the schema diff, and generates/persists inverse DDL.
//
// 2. Rollback work orders (ticket_type='rollback' & source_order_id!=nil):
//   - Locks the source order and rollback evidence prior to execution, marking them rollback_in_progress;
//   - Runs conflict detection before executing each inverse SQL statement group in reverse order
//     (row-locked column-by-column comparison for DML; structural consistency and zero-data-loss verification for DDL);
//   - Immediately aborts and marks the source order conflicted on conflict; marks the source order rolled_back on success.
package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
	"gorm.io/gorm"
)

const (
	failureRollbackUnsupported = "ROLLBACK_UNSUPPORTED"
	failureRollbackConflict    = "ROLLBACK_CONFLICT"
)

// orderRollbackContext holds the rollback context during a single Attempt execution.
type orderRollbackContext struct {
	OrderID            string
	StageID            string
	AttemptID          string
	RollbackEnabled    bool
	TicketType         string
	SourceOrderID      *string
	RollbackEvidenceID *string
	RollbackStatus     string

	Engine          string // "mysql" | "postgresql"
	DatabaseVersion string
	PGVersionNum    int
	DatasourceID    string
	SchemaName      string

	// parsedByOrdinal caches pre-parsed single-statement structures (for normal & rollback_enabled=true).
	parsedByOrdinal map[int32]*rollback.ParsedStatement

	SourceBundle *rollback.EvidenceBundle
	// inverseTriggerByOrdinal: key = plan Statement Ordinal (1-based) of the Rollback work order;
	// value = source StatementEvidence requiring conflict verification before executing that Ordinal.
	inverseTriggerByOrdinal map[int32]rollback.StatementEvidence
}

// persistUnsupportedRollbackEvidence records an unsupported rollback evidence row and updates the work order status.
func (s *Services) persistUnsupportedRollbackEvidence(
	ctx context.Context,
	rbCtx *orderRollbackContext,
	opType string,
	reason string,
) error {
	if opType == "" {
		opType = "dml"
	}
	evID := newID()
	env, mac, err := rollback.SealEvidenceBundle(s.Crypter, evID, &rollback.EvidenceBundle{
		Engine:        rbCtx.Engine,
		ServerVersion: rbCtx.DatabaseVersion,
		SchemaName:    rbCtx.SchemaName,
		OperationType: opType,
	})
	if err != nil {
		return err
	}
	now := nowUTC()
	return s.serializable(ctx, func(tx *gorm.DB) error {
		m := &persistence.OrderRollbackEvidenceModel{
			ID:                        evID,
			OrderID:                   rbCtx.OrderID,
			StageID:                   rbCtx.StageID,
			AttemptID:                 rbCtx.AttemptID,
			DatabaseEngine:            rbCtx.Engine,
			DatabaseVersion:           rbCtx.DatabaseVersion,
			DatasourceID:              rbCtx.DatasourceID,
			SchemaName:                rbCtx.SchemaName,
			OperationType:             opType,
			RollbackStatus:            rollback.StatusUnsupported,
			UnsupportedReason:         &reason,
			AffectedRows:              0,
			RollbackPayloadCiphertext: env.Ciphertext,
			RollbackPayloadNonce:      env.Nonce,
			RollbackPayloadKeyID:      env.KeyID,
			RollbackPayloadAlgorithm:  env.Algorithm,
			RollbackSQLHMAC:           mac,
			CreatedAt:                 now,
			UpdatedAt:                 now,
		}
		if err := tx.WithContext(ctx).Create(m).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec(`
			update change_orders
			set rollback_status = ?, rollback_evidence_id = ?, version = version + 1
			where id = ?`,
			rollback.StatusUnsupported, evID, rbCtx.OrderID).Error
	})
}

// persistIncompleteRollbackEvidence marks the status as evidence_incomplete when mutations have
// occurred on the target DB but evidence is incomplete, ensuring safe rollback is never fabricated or falsely reported.
func (s *Services) persistIncompleteRollbackEvidence(
	ctx context.Context,
	rbCtx *orderRollbackContext,
	opType string,
	reason string,
) error {
	if opType == "" {
		opType = "dml"
	}
	evID := newID()
	env, mac, err := rollback.SealEvidenceBundle(s.Crypter, evID, &rollback.EvidenceBundle{
		Engine:        rbCtx.Engine,
		ServerVersion: rbCtx.DatabaseVersion,
		SchemaName:    rbCtx.SchemaName,
		OperationType: opType,
	})
	if err != nil {
		return err
	}
	now := nowUTC()
	return s.serializable(ctx, func(tx *gorm.DB) error {
		m := &persistence.OrderRollbackEvidenceModel{
			ID:                        evID,
			OrderID:                   rbCtx.OrderID,
			StageID:                   rbCtx.StageID,
			AttemptID:                 rbCtx.AttemptID,
			DatabaseEngine:            rbCtx.Engine,
			DatabaseVersion:           rbCtx.DatabaseVersion,
			DatasourceID:              rbCtx.DatasourceID,
			SchemaName:                rbCtx.SchemaName,
			OperationType:             opType,
			RollbackStatus:            rollback.StatusEvidenceIncomplete,
			UnsupportedReason:         &reason,
			AffectedRows:              0,
			RollbackPayloadCiphertext: env.Ciphertext,
			RollbackPayloadNonce:      env.Nonce,
			RollbackPayloadKeyID:      env.KeyID,
			RollbackPayloadAlgorithm:  env.Algorithm,
			RollbackSQLHMAC:           mac,
			CreatedAt:                 now,
			UpdatedAt:                 now,
		}
		if err := tx.WithContext(ctx).Create(m).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec(`
			update change_orders
			set rollback_status = ?, rollback_evidence_id = ?, version = version + 1
			where id = ?`,
			rollback.StatusEvidenceIncomplete, evID, rbCtx.OrderID).Error
	})
}

// prepareOscRollbackBeforeSpawn validates rollback capability or rollback preconditions before spawning an OSC process (gh-ost / pt-osc).
func (s *Services) prepareOscRollbackBeforeSpawn(
	ctx context.Context,
	in TaskInput,
	schemaName string,
) (*orderRollbackContext, *rollback.ParsedStatement, *database.TableRollbackMetadata, *resultFacts, error) {
	var row struct {
		RollbackEnabled bool   `gorm:"column:rollback_enabled"`
		TicketType      string `gorm:"column:ticket_type"`
		RollbackStatus  string `gorm:"column:rollback_status"`
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select rollback_enabled, ticket_type, rollback_status
		from change_orders where id = ?`, in.OrderID).Scan(&row).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	if !row.RollbackEnabled && row.TicketType != "rollback" {
		return nil, nil, nil, nil, nil
	}

	target, cleanup, err := s.openStageTarget(ctx, in)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer cleanup()

	rbCtx, err := s.loadOrderRollbackContext(ctx, in, target)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if rbCtx == nil {
		return nil, nil, nil, nil, nil
	}
	if schemaName != "" {
		rbCtx.SchemaName = schemaName
	}
	plan, err := s.loadPlan(ctx, in, target.DS)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	if rbCtx.TicketType == "rollback" {
		rbFailFacts, rbErr := s.prepareRollbackTicketExecution(ctx, rbCtx)
		if rbErr != nil {
			return nil, nil, nil, nil, rbErr
		}
		if rbFailFacts != nil {
			return rbCtx, nil, nil, rbFailFacts, nil
		}
		if len(plan) > 0 {
			st := plan[0]
			if stmtEv, ok := rbCtx.inverseTriggerByOrdinal[st.Ordinal]; ok {
				rbVerifyCtx := database.WithRollbackSearchPath(ctx, target.SearchPath)
				if cerr := rollback.VerifyDDLStatementPrecondition(rbVerifyCtx, target.Pool, rbCtx.Engine, stmtEv); cerr != nil {
					if _, isConf, reason := classifyRollbackDMLError(cerr); isConf {
						_ = s.markSourceOrderRollbackConflict(ctx, rbCtx, reason)
						return rbCtx, nil, nil, s.rollbackConflictFacts(st.Ordinal, domain.BoundaryNotStarted, reason), nil
					}
					return nil, nil, nil, nil, cerr
				}
			}
		}
		return rbCtx, nil, nil, nil, nil
	}

	if rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
		rbFailFacts, rbErr := s.precheckNormalOrderRollbackCapability(ctx, rbCtx, plan)
		if rbErr != nil {
			return nil, nil, nil, nil, rbErr
		}
		if rbFailFacts != nil {
			return rbCtx, nil, nil, rbFailFacts, nil
		}
		if len(plan) == 0 {
			return rbCtx, nil, nil, nil, nil
		}
		st := plan[0]
		ddlParsed := rbCtx.parsedByOrdinal[st.Ordinal]
		if ddlParsed == nil {
			var perr error
			ddlParsed, perr = rollback.AnalyzeStatement(rbCtx.Engine, rbCtx.SchemaName, string(st.SQL))
			if perr != nil {
				if isCap, _, reason := classifyRollbackDMLError(perr); isCap {
					_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "ddl", reason)
					ff := s.rollbackUnsupportedFacts(st.Ordinal, domain.BoundaryNotStarted, reason, nil)
					return rbCtx, nil, nil, &ff, nil
				}
				return nil, nil, nil, nil, perr
			}
		}
		ddlSnapCtx := database.WithRollbackSearchPath(ctx, target.SearchPath)
		ddlPreMeta, preErr := rollback.PrepareDDLPreExecution(ddlSnapCtx, target.Pool, rbCtx.Engine, ddlParsed)
		if preErr != nil {
			if isCap, _, reason := classifyRollbackDMLError(preErr); isCap {
				_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "ddl", reason)
				ff := s.rollbackUnsupportedFacts(st.Ordinal, domain.BoundaryNotStarted, reason, nil)
				return rbCtx, nil, nil, &ff, nil
			}
			return nil, nil, nil, nil, preErr
		}
		return rbCtx, ddlParsed, ddlPreMeta, nil, nil
	}

	return rbCtx, nil, nil, nil, nil
}
