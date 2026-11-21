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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
)

const (
	failureRollbackUnsupported = "ROLLBACK_UNSUPPORTED"
	failureRollbackConflict    = "ROLLBACK_CONFLICT"
)

// isDeterministicZeroSendRejection reports whether a zero-send (BoundaryNotStarted) failure
// is a deterministic business rejection (which must advance the stage and order to failed rather than keeping running for retry).
func isDeterministicZeroSendRejection(failureName string) bool {
	switch failureName {
	case failureRollbackUnsupported, failureRollbackConflict:
		return true
	default:
		return false
	}
}

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

// loadOrderRollbackContext loads the work order's rollback configuration and target DB context.
func (s *Services) loadOrderRollbackContext(
	ctx context.Context,
	in TaskInput,
	target *stageTarget,
) (*orderRollbackContext, error) {
	var row struct {
		RollbackEnabled    bool    `gorm:"column:rollback_enabled"`
		TicketType         string  `gorm:"column:ticket_type"`
		SourceOrderID      *string `gorm:"column:source_order_id"`
		RollbackEvidenceID *string `gorm:"column:rollback_evidence_id"`
		RollbackStatus     string  `gorm:"column:rollback_status"`
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select rollback_enabled, ticket_type,
		       source_order_id::text as source_order_id,
		       rollback_evidence_id::text as rollback_evidence_id,
		       rollback_status
		from change_orders where id = ?`, in.OrderID).Scan(&row).Error; err != nil {
		return nil, err
	}
	if row.TicketType == "" {
		row.TicketType = "normal"
	}
	eng := "mysql"
	if target.DS.CompatibilityMode != "mysql" {
		eng = "postgresql"
	}
	schemaName := target.Schema
	if schemaName == "" && len(target.SearchPath) > 0 {
		schemaName = target.SearchPath[0]
	}
	if schemaName == "" && eng == "postgresql" {
		schemaName = "public"
	}
	return &orderRollbackContext{
		OrderID:            in.OrderID,
		StageID:            in.StageID,
		AttemptID:          in.AttemptID,
		RollbackEnabled:    row.RollbackEnabled,
		TicketType:         row.TicketType,
		SourceOrderID:      row.SourceOrderID,
		RollbackEvidenceID: row.RollbackEvidenceID,
		RollbackStatus:     row.RollbackStatus,
		Engine:             eng,
		DatabaseVersion:    eng,
		DatasourceID:       target.DS.ID,
		SchemaName:         schemaName,
		parsedByOrdinal:    make(map[int32]*rollback.ParsedStatement),
	}, nil
}

// precheckNormalOrderRollbackCapability performs deterministic rollback capability checks on all
// statements of a normal work order before sending any SQL to the target database. If any statement
// does not support rollback, it immediately fails closed, records unsupported evidence, and aborts execution.
func (s *Services) precheckNormalOrderRollbackCapability(
	ctx context.Context,
	rbCtx *orderRollbackContext,
	plan []PlanStatement,
) (*resultFacts, error) {
	if rbCtx == nil || !rbCtx.RollbackEnabled || rbCtx.TicketType != "normal" {
		return nil, nil
	}
	for _, st := range plan {
		parsed, err := rollback.AnalyzeStatement(rbCtx.Engine, rbCtx.SchemaName, string(st.SQL))
		if err != nil {
			_, _, reason := classifyRollbackDMLError(err)
			if reason == "" {
				reason = err.Error()
			}
			_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "", reason)
			now := nowUTC()
			code := int(domain.CodeStatementRejected)
			name := failureRollbackUnsupported
			detail, _ := json.Marshal(map[string]any{
				"engine_code_class":  "rollback_capability_rejected",
				"failure_name":       failureRollbackUnsupported,
				"unsupported_reason": reason,
			})
			return &resultFacts{
				Terminal:    domain.AttemptFailed,
				Boundary:    domain.BoundaryNotStarted,
				ReasonCode:  code,
				FailureName: name,
				Statements: []stmtFact{{
					Ordinal:     st.Ordinal,
					State:       domain.StmtFailed,
					ErrCode:     &code,
					FailureName: &name,
					Detail:      detail,
					StartedAt:   &now,
					FinishedAt:  &now,
				}},
				SkipSuffix: true,
			}, nil
		}
		rbCtx.parsedByOrdinal[st.Ordinal] = parsed
	}
	return nil, nil
}

// prepareRollbackTicketExecution locks the source work order and rollback evidence before a
// rollback work order begins execution, verifies the source order status is available, sets it to
// rollback_in_progress, and builds the ordinal-indexed map for triggering conflict checks.
func (s *Services) prepareRollbackTicketExecution(
	ctx context.Context,
	rbCtx *orderRollbackContext,
) (*resultFacts, error) {
	if rbCtx == nil || rbCtx.TicketType != "rollback" {
		return nil, nil
	}
	if rbCtx.SourceOrderID == nil || *rbCtx.SourceOrderID == "" ||
		rbCtx.RollbackEvidenceID == nil || *rbCtx.RollbackEvidenceID == "" {
		return s.rollbackConflictFacts(1, domain.BoundaryNotStarted, "rollback ticket is missing source_order_id or rollback_evidence_id"), nil
	}
	srcOrderID := *rbCtx.SourceOrderID
	evID := *rbCtx.RollbackEvidenceID

	var bundle *rollback.EvidenceBundle
	var conflictReason string
	err := s.serializable(ctx, func(tx *gorm.DB) error {
		var src struct {
			ID             string
			State          string
			RollbackStatus string
		}
		if err := tx.WithContext(ctx).Raw(`
			select id::text as id, state, rollback_status
			from change_orders where id = ? for update`, srcOrderID).Scan(&src).Error; err != nil {
			return err
		}
		if src.ID == "" {
			conflictReason = "source order not found"
			return nil
		}
		if src.RollbackStatus != rollback.StatusAvailable &&
			src.RollbackStatus != rollback.StatusRollbackInProgress &&
			src.RollbackStatus != rollback.StatusRollbackFailed {
			conflictReason = fmt.Sprintf("source order rollback_status is %q (expected available)", src.RollbackStatus)
			return nil
		}
		var ev struct {
			ID                 string
			RollbackStatus     string
			EvidenceCiphertext []byte
			EvidenceNonce      []byte
			EvidenceKeyID      string
			EvidenceAlgorithm  string
			RollbackSQLHMAC    string
		}
		if err := tx.WithContext(ctx).Raw(`
			select id::text as id, rollback_status,
			       rollback_payload_ciphertext as evidence_ciphertext,
			       rollback_payload_nonce as evidence_nonce,
			       rollback_payload_key_id as evidence_key_id,
			       rollback_payload_algorithm as evidence_algorithm,
			       rollback_sql_hmac
			from order_rollback_evidences
			where id = ? and order_id = ? for update`, evID, srcOrderID).Scan(&ev).Error; err != nil {
			return err
		}
		if ev.ID == "" || len(ev.EvidenceCiphertext) == 0 {
			conflictReason = "source rollback evidence not found or empty"
			return nil
		}
		opened, oerr := rollback.OpenEvidenceBundle(
			s.Crypter,
			ev.ID,
			crypto.Envelope{
				Ciphertext: ev.EvidenceCiphertext,
				Nonce:      ev.EvidenceNonce,
				KeyID:      ev.EvidenceKeyID,
				Algorithm:  ev.EvidenceAlgorithm,
			},
			ev.RollbackSQLHMAC,
		)
		if oerr != nil {
			conflictReason = fmt.Sprintf("failed to open rollback evidence: %v", oerr)
			return nil
		}
		bundle = opened
		now := nowUTC()
		if err := tx.WithContext(ctx).Exec(`
			update change_orders set rollback_status = ?, version = version + 1
			where id = ?`, rollback.StatusRollbackInProgress, srcOrderID).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			update order_rollback_evidences set rollback_status = ?, updated_at = ?
			where id = ?`, rollback.StatusRollbackInProgress, now, evID).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if conflictReason != "" || bundle == nil {
		return s.rollbackConflictFacts(1, domain.BoundaryNotStarted, conflictReason), nil
	}
	rbCtx.SourceBundle = bundle
	rbCtx.inverseTriggerByOrdinal = make(map[int32]rollback.StatementEvidence, len(bundle.Statements))
	var nextOrdinal int32 = 1
	for i := len(bundle.Statements) - 1; i >= 0; i-- {
		stmtEv := bundle.Statements[i]
		if len(stmtEv.InverseStatements) == 0 {
			continue
		}
		rbCtx.inverseTriggerByOrdinal[nextOrdinal] = stmtEv
		nextOrdinal += int32(len(stmtEv.InverseStatements))
	}
	return nil, nil
}

func (s *Services) rollbackConflictFacts(ordinal int32, boundary domain.SendBoundary, reason string) *resultFacts {
	now := nowUTC()
	code := int(domain.CodeStatementRejected)
	name := failureRollbackConflict
	detail, _ := json.Marshal(map[string]any{
		"engine_code_class": "rollback_conflict_detected",
		"failure_name":      failureRollbackConflict,
		"conflict_reason":   reason,
	})
	return &resultFacts{
		Terminal:    domain.AttemptFailed,
		Boundary:    boundary,
		ReasonCode:  code,
		FailureName: name,
		Statements: []stmtFact{{
			Ordinal:     ordinal,
			State:       domain.StmtFailed,
			ErrCode:     &code,
			FailureName: &name,
			Detail:      detail,
			StartedAt:   &now,
			FinishedAt:  &now,
		}},
		SkipSuffix: true,
	}
}

func (s *Services) rollbackUnsupportedFacts(ordinal int32, boundary domain.SendBoundary, reason string, prior []stmtFact) resultFacts {
	now := nowUTC()
	code := int(domain.CodeStatementRejected)
	name := failureRollbackUnsupported
	detail, _ := json.Marshal(map[string]any{
		"engine_code_class":  "rollback_capability_rejected",
		"failure_name":       failureRollbackUnsupported,
		"unsupported_reason": reason,
	})
	stmts := append([]stmtFact{}, prior...)
	stmts = append(stmts, stmtFact{
		Ordinal:     ordinal,
		State:       domain.StmtFailed,
		ErrCode:     &code,
		FailureName: &name,
		Detail:      detail,
		StartedAt:   &now,
		FinishedAt:  &now,
	})
	return resultFacts{
		Terminal:    domain.AttemptFailed,
		Boundary:    boundary,
		ReasonCode:  code,
		FailureName: name,
		Statements:  stmts,
		SkipSuffix:  true,
	}
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

// persistAvailableRollbackEvidence saves the sealed available rollback evidence and updates the work order status.
func (s *Services) persistAvailableRollbackEvidence(
	ctx context.Context,
	rbCtx *orderRollbackContext,
	evID string,
	opType string,
	env crypto.Envelope,
	sqlHMAC string,
	affectedRows int64,
) error {
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
			RollbackStatus:            rollback.StatusAvailable,
			AffectedRows:              affectedRows,
			RollbackPayloadCiphertext: env.Ciphertext,
			RollbackPayloadNonce:      env.Nonce,
			RollbackPayloadKeyID:      env.KeyID,
			RollbackPayloadAlgorithm:  env.Algorithm,
			RollbackSQLHMAC:           sqlHMAC,
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
			rollback.StatusAvailable, evID, rbCtx.OrderID).Error
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

// markSourceOrderRollbackConflict marks both the source work order and its rollback evidence as conflicted.
func (s *Services) markSourceOrderRollbackConflict(
	ctx context.Context,
	rbCtx *orderRollbackContext,
	conflictReason string,
) error {
	if rbCtx == nil || rbCtx.SourceOrderID == nil || rbCtx.RollbackEvidenceID == nil {
		return nil
	}
	now := nowUTC()
	return s.serializable(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec(`
			update change_orders set rollback_status = ?, version = version + 1
			where id = ?`, rollback.StatusConflicted, *rbCtx.SourceOrderID).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec(`
			update order_rollback_evidences
			set rollback_status = ?, conflict_reason = ?, updated_at = ?
			where id = ?`,
			rollback.StatusConflicted, conflictReason, now, *rbCtx.RollbackEvidenceID).Error
	})
}

// finalizeSourceOrderRollbackStatus updates the source work order and rollback evidence status when a rollback work order finishes.
func (s *Services) finalizeSourceOrderRollbackStatus(
	ctx context.Context,
	rbCtx *orderRollbackContext,
	terminal domain.AttemptState,
) error {
	if rbCtx == nil || rbCtx.TicketType != "rollback" ||
		rbCtx.SourceOrderID == nil || rbCtx.RollbackEvidenceID == nil {
		return nil
	}
	newStatus := rollback.StatusRollbackFailed
	if terminal == domain.AttemptSucceeded {
		newStatus = rollback.StatusRolledBack
	}
	now := nowUTC()
	return s.serializable(ctx, func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Exec(`
			update change_orders set rollback_status = ?, version = version + 1
			where id = ? and rollback_status in ('available', 'rollback_in_progress', 'rollback_failed')`,
			newStatus, *rbCtx.SourceOrderID).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec(`
			update order_rollback_evidences
			set rollback_status = ?, updated_at = ?
			where id = ? and rollback_status in ('available', 'rollback_in_progress', 'rollback_failed')`,
			newStatus, now, *rbCtx.RollbackEvidenceID).Error
	})
}

// syncSourceOrderRollbackOnOrderTransitionTx atomically synchronizes the source work order and rollback evidence status within the rollback order's own state transition transaction.
func (s *Services) syncSourceOrderRollbackOnOrderTransitionTx(
	ctx context.Context,
	tx *gorm.DB,
	order *orderRow,
	toState orderdomain.OrderState,
	now time.Time,
) error {
	if order == nil || order.TicketType != "rollback" ||
		order.SourceOrderID == nil || *order.SourceOrderID == "" {
		return nil
	}
	var targetStatus string
	switch toState {
	case orderdomain.StateCompleted:
		targetStatus = rollback.StatusRolledBack
	case orderdomain.StateFailed,
		orderdomain.StatePartialFailed,
		orderdomain.StateCancelled,
		orderdomain.StatePartialCancelled,
		orderdomain.StateResultUnknown,
		orderdomain.StateMissedSchedule,
		orderdomain.StateInvalid:
		targetStatus = rollback.StatusRollbackFailed
	default:
		return nil
	}
	if err := tx.WithContext(ctx).Exec(`
		update change_orders
		set rollback_status = ?, version = version + 1
		where id = ? and rollback_status in ('available', 'rollback_in_progress', 'rollback_failed')`,
		targetStatus, *order.SourceOrderID).Error; err != nil {
		return err
	}
	if order.RollbackEvidenceID != nil && *order.RollbackEvidenceID != "" {
		if err := tx.WithContext(ctx).Exec(`
			update order_rollback_evidences
			set rollback_status = ?, updated_at = ?
			where id = ? and rollback_status in ('available', 'rollback_in_progress', 'rollback_failed')`,
			targetStatus, now, *order.RollbackEvidenceID).Error; err != nil {
			return err
		}
	} else {
		if err := tx.WithContext(ctx).Exec(`
			update order_rollback_evidences
			set rollback_status = ?, updated_at = ?
			where order_id = ? and rollback_status in ('available', 'rollback_in_progress', 'rollback_failed')`,
			targetStatus, now, *order.SourceOrderID).Error; err != nil {
			return err
		}
	}
	return nil
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

// finalizeOscRollbackAfterRun captures post-OSC DDL schema evidence (for normal rollback-enabled orders)
// and updates the source order rollback status (for rollback orders).
func (s *Services) finalizeOscRollbackAfterRun(
	ctx context.Context,
	in TaskInput,
	rbCtx *orderRollbackContext,
	ddlParsed *rollback.ParsedStatement,
	ddlPreMeta *database.TableRollbackMetadata,
	terminal domain.AttemptState,
) {
	if rbCtx == nil {
		return
	}
	if terminal == domain.AttemptSucceeded && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" && ddlParsed != nil && ddlPreMeta != nil {
		target, cleanup, err := s.openStageTarget(ctx, in)
		if err == nil {
			defer cleanup()
			ddlSnapCtx := database.WithRollbackSearchPath(ctx, target.SearchPath)
			ev, postErr := rollback.FinalizeOSCPostExecution(ddlSnapCtx, target.Pool, rbCtx.Engine, 1, ddlParsed, ddlPreMeta)
			if postErr == nil && ev != nil {
				strategy := rollback.StrategyMySQLDDLSchemaDiff
				if rbCtx.Engine == "postgresql" {
					strategy = rollback.StrategyPostgresDDLSchemaDiff
				}
				bundle := &rollback.EvidenceBundle{
					Engine:             rbCtx.Engine,
					ServerVersion:      rbCtx.DatabaseVersion,
					ServerVersionNum:   rbCtx.PGVersionNum,
					SchemaName:         rbCtx.SchemaName,
					OperationType:      rollback.OperationTypeDDL,
					Strategy:           strategy,
					OriginalStatements: []string{ev.OriginalSQL},
					RollbackStatements: ev.InverseStatements,
					RollbackSQL:        strings.Join(ev.InverseStatements, "\n"),
					AffectedRows:       0,
					Statements:         []rollback.StatementEvidence{*ev},
				}
				evID := newID()
				env, mac, sealErr := rollback.SealEvidenceBundle(s.Crypter, evID, bundle)
				if sealErr == nil {
					_ = s.persistAvailableRollbackEvidence(ctx, rbCtx, evID, "ddl", env, mac, 0)
				} else {
					_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", sealErr.Error())
				}
			} else if postErr != nil {
				_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", postErr.Error())
			}
		} else {
			_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", err.Error())
		}
	}
	_ = s.finalizeSourceOrderRollbackStatus(ctx, rbCtx, terminal)
}

// executeDMLStatementWithRollback encapsulates single-DML-statement execution under normal
// rollback capture or rollback execution conflict verification.
func (s *Services) executeDMLStatementWithRollback(
	execCtx context.Context,
	target *stageTarget,
	tx database.DMLTransaction,
	rbCtx *orderRollbackContext,
	st PlanStatement,
) (*rollback.StatementEvidence, *database.StatementResult, error) {
	if rbCtx != nil && rbCtx.TicketType == "rollback" {
		if stmtEv, ok := rbCtx.inverseTriggerByOrdinal[st.Ordinal]; ok {
			if cerr := rollback.VerifyDMLStatementPreconditionInTx(
				execCtx, target.Pool, tx, rbCtx.Engine, stmtEv,
			); cerr != nil {
				return nil, nil, cerr
			}
		}
		res, err := target.Adapter.ExecuteDMLStatement(execCtx, tx, database.DMLStatement{SQL: string(st.SQL)})
		return nil, res, err
	}
	if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
		parsed := rbCtx.parsedByOrdinal[st.Ordinal]
		if parsed == nil {
			var perr error
			parsed, perr = rollback.AnalyzeStatement(rbCtx.Engine, rbCtx.SchemaName, string(st.SQL))
			if perr != nil {
				return nil, nil, perr
			}
		}
		if rbCtx.Engine == "mysql" {
			res, ev, meta, err := rollback.ExecuteAndCaptureMySQLDML(
				execCtx, target.Adapter, target.Pool, tx, int(st.Ordinal), parsed, rollback.DefaultMaxCapturedRowsPerStatement,
			)
			if meta != nil && meta.ServerVersion != "" {
				rbCtx.DatabaseVersion = meta.ServerVersion
			}
			return ev, res, err
		}
		res, ev, meta, _, err := rollback.ExecuteAndCapturePostgresDML(
			execCtx, target.Pool, tx, int(st.Ordinal), parsed, rollback.DefaultMaxCapturedRowsPerStatement,
		)
		if meta != nil && meta.ServerVersion != "" {
			rbCtx.DatabaseVersion = meta.ServerVersion
			rbCtx.PGVersionNum = meta.ServerVersionNum
		}
		return ev, res, err
	}
	res, err := target.Adapter.ExecuteDMLStatement(execCtx, tx, database.DMLStatement{SQL: string(st.SQL)})
	return nil, res, err
}

// classifyRollbackDMLError classifies whether an error returned by DML/DDL execution is a rollback capability rejection or conflict error.
func classifyRollbackDMLError(err error) (isCapability bool, isConflict bool, reason string) {
	if err == nil {
		return false, false, ""
	}
	var capErr *rollback.CapabilityError
	if errors.As(err, &capErr) {
		if capErr.Message != "" {
			return true, false, capErr.Reason + ": " + capErr.Message
		}
		return true, false, capErr.Reason
	}
	var confErr *rollback.ConflictError
	if errors.As(err, &confErr) {
		if confErr.Message != "" {
			return false, true, confErr.Reason + ": " + confErr.Message
		}
		return false, true, confErr.Reason
	}
	msg := err.Error()
	if strings.Contains(msg, "rollback capability rejected") {
		return true, false, msg
	}
	if strings.Contains(msg, "rollback conflict detected") {
		return false, true, msg
	}
	return false, false, ""
}
