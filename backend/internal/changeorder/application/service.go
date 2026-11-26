// service.go ChangeOrder domain application service wiring and shared helpers.
// All three commands (submit/decide/withdraw_void) follow repository-commands declarations:
// serializable isolation, bounded retry on 40001/40P01, optimistic locking CAS, full
// RowsAffected checks, and atomic persistence of domain events, Outbox, and idempotency
// responses within the same transaction.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
)

// Actor represents the command initiator (session identity snapshot).
type Actor struct {
	UserID   string
	Username string
}

// RequestMeta holds the audit context for a single HTTP request.
type RequestMeta struct {
	RequestID string
	SourceIP  string
	UserAgent string
}

// Services aggregates all Use Case dependencies for the ChangeOrder domain.
type Services struct {
	DB       *gorm.DB
	Crypter  *crypto.Crypter
	Recorder *auditlog.Recorder
	// SearchPathProbe is a test seam. nil opens the real execution-purpose
	// connection and verifies the frozen PostgreSQL environment.
	SearchPathProbe ExecutionSearchPathProbe
}

// NewServices constructs the application services.
func NewServices(db *gorm.DB, crypter *crypto.Crypter, recorder *auditlog.Recorder) (*Services, error) {
	if db == nil || crypter == nil || recorder == nil {
		return nil, errors.New("changeorder: nil service dependency")
	}
	return &Services{DB: db, Crypter: crypter, Recorder: recorder}, nil
}

func newID() string     { return uuid.NewString() }
func nowUTC() time.Time { return time.Now().UTC() }

func (s *Services) serializable(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return txutil.Serializable(ctx, s.DB, fn)
}

// ---- Domain Events ----

// Internal event names (repository-event-routing.json rules; external types are mapped by
// the outbox consumer according to the routing table; this package does not emit external events directly).
const (
	EventOrderSubmitted    = "change_order.submitted"
	EventApprovalDecided   = "change_order.approval_decided"
	EventStageReadyForExec = "change_order.stage_ready_for_execution"
	EventOrderRejected     = "change_order.rejected"
	EventOrderWithdrawn    = "change_order.withdrawn"
	EventOrderVoided       = "change_order.voided"
	EventNodeInvalidated   = "workflow.node_invalidated"
	// S007/RCP-20261031-NOTIFICATION-RULES-AND-CHANNELS: approval step activated
	// event (emitted by decide for same-stage progression and by the execution domain for
	// cross-stage first-step activation; recipients = frozen candidate approvers of the newly activated step).
	EventStepActivated = "change_order.step_activated"
)

// StepActivatedPayload maps to domain-event.schema stepActivated def
// (additionalProperties:false; extra fields will be rejected by consumers).
type StepActivatedPayload struct {
	AggregateID      string `json:"aggregate_id"`
	StageID          string `json:"stage_id"`
	StepID           string `json:"step_id"`
	AggregateVersion int64  `json:"aggregate_version"`
}

// orderSubject returns a schema-valid subject (change-orders/{uuid}).
func orderSubject(orderID string) string { return "change-orders/" + orderID }

// emitOrderEvent writes a change_order domain event (domain_events + outbox in the same transaction).
func (s *Services) emitOrderEvent(ctx context.Context, tx *gorm.DB, internalType, orderID string,
	actor auditlog.Actor, data any) error {
	return s.Recorder.EmitPayload(ctx, tx, internalType, orderSubject(orderID), actor, data)
}

// OrderSubmittedPayload maps to domain-event.schema.json orderSubmitted
// def (additionalProperties:false; extra fields are forbidden).
type OrderSubmittedPayload struct {
	OrderID          string `json:"order_id"`
	DisplayNumber    string `json:"display_number"`
	SubmitterUserID  string `json:"submitter_user_id"`
	StageCount       int    `json:"stage_count"`
	SnapshotHash     string `json:"snapshot_hash"`
	AggregateVersion int64  `json:"aggregate_version"`
}

// StateChangedPayload maps to stateChanged def.
type StateChangedPayload struct {
	AggregateID      string  `json:"aggregate_id"`
	From             *string `json:"from"`
	To               string  `json:"to"`
	ReasonCode       string  `json:"reason_code"`
	StageID          *string `json:"stage_id,omitempty"`
	AggregateVersion int64   `json:"aggregate_version"`
}

// ApprovalDecidedPayload maps to approvalDecided def
// (additionalProperties:false; SEC-0036: comment does not enter domain events —
// approval comments are only written to approval_decisions rows and timeline projections;
// extra fields in event payloads would be rejected/dead-lettered by outbox consumers per schema).
type ApprovalDecidedPayload struct {
	OrderID          string `json:"order_id"`
	StageID          string `json:"stage_id"`
	StepID           string `json:"step_id"`
	ReviewerUserID   string `json:"reviewer_user_id"`
	Decision         string `json:"decision"`
	AggregateVersion int64  `json:"aggregate_version"`
}

// NodeInvalidatedPayload is the payload when workflow.node_invalidated outputs
// change_order.state_changed discriminated by affected_resource_type
// (routing discriminator + required stateChanged fields).
type NodeInvalidatedPayload struct {
	AffectedResourceType string  `json:"affected_resource_type"`
	AggregateID          string  `json:"aggregate_id"`
	From                 *string `json:"from"`
	To                   string  `json:"to"`
	ReasonCode           string  `json:"reason_code"`
	AggregateVersion     int64   `json:"aggregate_version"`
}

// ---- Frozen Hash Reproduction (byte-identical to review/application frozenHashes) ----

// flowSnapshotStruct matches the private flowSnapshot struct in review/application field-for-field
// (field order determines JSON bytes -> hash; ExecutionActors is always empty and unused in
// the freeze algorithm, kept isomorphic here to prevent drift).
type flowSnapshotStruct struct {
	FlowID      string                   `json:"flow_id"`
	FlowVersion int64                    `json:"flow_version"`
	Stages      []flowStageSummaryStruct `json:"stages"`
}

type flowStageSummaryStruct struct {
	Position               int      `json:"position"`
	DatasourceID           string   `json:"datasource_id"`
	DatasourceEngine       string   `json:"datasource_engine"`
	DatasourceMode         string   `json:"datasource_compatibility_mode"`
	DatasourceHost         string   `json:"datasource_host"`
	DatasourcePort         int      `json:"datasource_port"`
	DatasourceDatabaseName string   `json:"datasource_database_name"`
	DatasourceIdentityFP   string   `json:"datasource_identity_fingerprint"`
	ApprovalStepActors     []string `json:"approval_step_actors"`
	ExecutionActors        []string `json:"execution_actors"`
}

// stageFact holds flow stage + datasource facts (submission freeze read surface). Stages no longer
// carry schema mappings (RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS).
type stageFact struct {
	StageID           string
	Position          int
	DatasourceID      string
	Engine            string
	CompatibilityMode string
	Host              string
	Port              int
	DatabaseName      *string
	IdentityFP        *string
	Name              string
}

// loadFlowStageFacts returns ordered stage and datasource facts (including stage IDs for reading executors).
func (s *Services) loadFlowStageFacts(ctx context.Context, db *gorm.DB, flowID string) ([]stageFact, error) {
	var rows []stageFact
	err := db.WithContext(ctx).Raw(`
		select fs.id::text as stage_id, fs.position, fs.datasource_id::text as datasource_id,
		       d.engine, d.compatibility_mode, d.host, d.port, d.database_name,
		       d.identity_fingerprint as identity_fp,
		       d.name
		from flow_stages fs
		join datasources d on d.id = fs.datasource_id
		where fs.flow_id = ?
		order by fs.position`, flowID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// loadFlowApprovalActors returns the stage_position -> step-position-ordered set of
// (stepPosition, userID) pairs (shared by snapshot freezing and hash reproduction).
func (s *Services) loadFlowApprovalSteps(ctx context.Context, db *gorm.DB, flowID string) (map[int][]flowStepActors, error) {
	var rows []struct {
		StagePosition int
		StepPosition  int
		UserID        string
	}
	err := db.WithContext(ctx).Raw(`
		select fs.position as stage_position, st.position as step_position, a.user_id::text as user_id
		from flow_approval_steps st
		join flow_stages fs on fs.id = st.stage_id
		join flow_approval_step_actors a on a.step_id = st.id
		where st.flow_id = ? and st.stage_id is not null
		order by fs.position, st.position, a.user_id`, flowID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[int][]flowStepActors{}
	for _, r := range rows {
		// Rows arrive ordered by (fs.position, st.position, user_id):
		// rows for the same step are contiguous, and trailing aggregation preserves actor order.
		steps := out[r.StagePosition]
		if len(steps) == 0 || steps[len(steps)-1].Position != r.StepPosition {
			steps = append(steps, flowStepActors{Position: r.StepPosition})
			out[r.StagePosition] = steps
		}
		idx := len(steps) - 1
		out[r.StagePosition][idx].Actors = append(out[r.StagePosition][idx].Actors, r.UserID)
	}
	return out, nil
}

// flowStepActors holds the position and candidate approvers of a single approval step.
type flowStepActors struct {
	Position int
	Actors   []string
}

// loadFlowExecutionActors returns the stage_id -> executor set mapping (ordered by user_id).
func (s *Services) loadFlowExecutionActors(ctx context.Context, db *gorm.DB, flowID string) (map[string][]string, error) {
	var rows []struct {
		StageID string
		UserID  string
	}
	err := db.WithContext(ctx).Raw(`
		select fs.id::text as stage_id, a.user_id::text as user_id
		from flow_stages fs
		join flow_execution_actors a on a.stage_id = fs.id
		where fs.flow_id = ?
		order by a.user_id`, flowID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.StageID] = append(out[r.StageID], r.UserID)
	}
	return out, nil
}

// computeFlowSnapshotHash reproduces the flow frozen hash of review/application.frozenHashes
// (ApprovalStepActors is the ordered union of stage_position -> users;
// ExecutionActors does not participate in the freeze algorithm, kept nil for parity with the original implementation).
func computeFlowSnapshotHash(flowID string, flowVersion int64, stages []stageFact,
	approvalByPos map[int][]string) (string, error) {
	snap := flowSnapshotStruct{FlowID: flowID, FlowVersion: flowVersion}
	for i := range stages {
		st := stages[i]
		identity := ""
		if st.IdentityFP != nil {
			identity = *st.IdentityFP
		}
		databaseName := ""
		if st.DatabaseName != nil {
			databaseName = *st.DatabaseName
		}
		snap.Stages = append(snap.Stages, flowStageSummaryStruct{
			Position: st.Position, DatasourceID: st.DatasourceID,
			DatasourceEngine: st.Engine, DatasourceMode: st.CompatibilityMode,
			DatasourceHost: st.Host, DatasourcePort: st.Port,
			DatasourceDatabaseName: databaseName, DatasourceIdentityFP: identity,
			ApprovalStepActors: approvalByPos[st.Position],
		})
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// computeRuleConfigHashes recomputes the same frozen identities as review
// StartRun. tool_config_hash covers enabled Prompt Tools and every enabled
// knowledge candidate, so a policy change after the worker read still makes
// submission outdated.
func (s *Services) computeRuleConfigHashes(ctx context.Context, db *gorm.DB, ruleSetID *string) (string, string, error) {
	noneHash := hashutil.SHA256Hex([]byte("none"))
	ruleHash := noneHash
	var toolHashes []string
	if ruleSetID != nil && *ruleSetID != "" {
		var rule struct {
			ConfigHash string
			Enabled    bool
		}
		if err := db.WithContext(ctx).Raw(
			`select config_hash, enabled from rule_sets where id = ?`, *ruleSetID).Scan(&rule).Error; err != nil {
			return "", "", err
		}
		if rule.ConfigHash == "" {
			return "", "", domain.ErrNotFound()
		}
		ruleHash = rule.ConfigHash
		if rule.Enabled {
			if err := db.WithContext(ctx).Raw(`
				select t.config_hash
				from rule_set_prompt_tools rt
				join prompt_tools t on t.id = rt.prompt_tool_id
				where rt.rule_set_id = ? and t.state = 'enabled'
				order by t.id`, *ruleSetID).Scan(&toolHashes).Error; err != nil {
				return "", "", err
			}
		}
	}
	var knowledgeRows []knowledgePolicyIdentity
	if err := db.WithContext(ctx).Raw(`
		select id::text as id, config_hash
		from knowledge_entries
		where state = 'enabled'
		order by id`).Scan(&knowledgeRows).Error; err != nil {
		return "", "", err
	}
	return ruleHash, effectiveChangeReviewToolHash(toolHashes, knowledgeRows), nil
}

type knowledgePolicyIdentity struct {
	ID         string
	ConfigHash string
}

func effectiveChangeReviewToolHash(
	toolHashes []string,
	knowledgeRows []knowledgePolicyIdentity,
) string {
	identities := append([]string(nil), toolHashes...)
	for _, entry := range knowledgeRows {
		identities = append(
			identities,
			"knowledge_entry:"+entry.ID+":"+entry.ConfigHash,
		)
	}
	return reviewdomain.EffectiveToolConfigHash(identities)
}

// flowGranted checks whether the user is authorized to submit to the flow
// (same semantics as review/application.flowGranted; submit command carries its own read surface).
func (s *Services) flowGranted(ctx context.Context, db *gorm.DB, userID, flowID string) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Raw(`
		select count(*) from permission_group_flow_grants g
		join permission_group_members m on m.permission_group_id = g.permission_group_id
		join permission_groups p on p.id = g.permission_group_id
		join flows f on f.id = g.flow_id
		where m.user_id = ? and g.flow_id = ? and p.enabled and f.status = 'enabled'
		  and f.flow_type = 'change_review'`, userID, flowID).Scan(&n).Error
	return n > 0, err
}

// ---- Idempotency (idempotency_records; response envelopes stored encrypted) ----

func (s *Services) idemStore() *txutil.IdempotencyStore {
	return txutil.NewIdempotencyStore(s.DB, s.Crypter, txutil.IdempotencyErrors{
		Reused:     func() error { return domain.ErrIdemReused() },
		InProgress: func() error { return domain.ErrIdemInProgress() },
		Concurrent: func() error { return domain.ErrConcurrent() },
	})
}

// ---- Common Row Loaders ----

// loadOrderForUpdate locks and returns the work order row.
func (s *Services) loadOrderForUpdate(ctx context.Context, tx *gorm.DB, orderID string) (*persistence.ChangeOrderModel, error) {
	var m persistence.ChangeOrderModel
	if err := tx.WithContext(ctx).Clauses(txutil.ForUpdate()).
		Where("id = ?", orderID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	return &m, nil
}

// loadOrderStages returns all stages of a work order (ordered by position).
func (s *Services) loadOrderStages(ctx context.Context, db *gorm.DB, orderID string) ([]persistence.OrderStageModel, error) {
	var rows []persistence.OrderStageModel
	if err := db.WithContext(ctx).Where("order_id = ?", orderID).
		Order("position").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// loadOrderSteps returns all approval steps for the given stages (ordered by stage and position).
func (s *Services) loadOrderSteps(ctx context.Context, db *gorm.DB, stageIDs []string) ([]persistence.OrderApprovalStepModel, error) {
	var rows []persistence.OrderApprovalStepModel
	if len(stageIDs) == 0 {
		return rows, nil
	}
	if err := db.WithContext(ctx).Where("stage_id in ?", stageIDs).
		Order("stage_id").Order("position").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// updateOrderVersioned updates the work order with optimistic locking: WHERE id AND version;
// 0 rows distinguishes 1003 vs 1004 via stale check.
func (s *Services) updateOrderVersioned(ctx context.Context, tx *gorm.DB, m *persistence.ChangeOrderModel,
	expectVersion int64, sets map[string]any) error {
	res := tx.WithContext(ctx).Model(&persistence.ChangeOrderModel{}).
		Where("id = ? and version = ?", m.ID, expectVersion).
		Updates(sets)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		if expectVersion != m.Version {
			return domain.ErrConcurrent()
		}
		return domain.ErrConcurrent()
	}
	return nil
}
