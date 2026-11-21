// Package auditlog provides same-transaction persistence for business audit and domain events
// (repository-commands: business_outbox_atomicity required).
//
// Audit events are written to audit_events (90-day retention expressed via expires_at);
// domain events are written to domain_events and persisted to outbox_events in the same transaction.
// All writes go through named methods; full-row Save and map-based table writes are forbidden.
package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/realtime"
)

// AuditEventModel maps to the audit_events table (gorm-model-registry: platform/audit/postgres).
type AuditEventModel struct {
	ID                    string    `gorm:"column:id;primaryKey"`
	EventType             string    `gorm:"column:event_type"`
	ActorKind             string    `gorm:"column:actor_kind"`
	ActorID               *string   `gorm:"column:actor_id"`
	ActorUsernameSnapshot *string   `gorm:"column:actor_username_snapshot"`
	ResourceType          string    `gorm:"column:resource_type"`
	ResourceID            *string   `gorm:"column:resource_id"`
	Action                string    `gorm:"column:action"`
	Outcome               string    `gorm:"column:outcome"`
	RequestID             *string   `gorm:"column:request_id"`
	SourceIP              *string   `gorm:"column:source_ip"`
	UserAgent             *string   `gorm:"column:user_agent"`
	Metadata              []byte    `gorm:"column:metadata"`
	OccurredAt            time.Time `gorm:"column:occurred_at"`
	ExpiresAt             time.Time `gorm:"column:expires_at"`
}

func (AuditEventModel) TableName() string { return "audit_events" }

// DomainEventModel maps to the domain_events table.
type DomainEventModel struct {
	ID            string    `gorm:"column:id;primaryKey"`
	EventType     string    `gorm:"column:event_type"`
	Subject       string    `gorm:"column:subject"`
	Sequence      int64     `gorm:"column:sequence"`
	CorrelationID string    `gorm:"column:correlation_id"`
	CausationID   *string   `gorm:"column:causation_id"`
	Actor         []byte    `gorm:"column:actor"`
	EventData     []byte    `gorm:"column:event_data"`
	OccurredAt    time.Time `gorm:"column:occurred_at"`
}

func (DomainEventModel) TableName() string { return "domain_events" }

// OutboxEventModel maps to the outbox_events table.
type OutboxEventModel struct {
	ID               string     `gorm:"column:id;primaryKey"`
	EventType        string     `gorm:"column:event_type"`
	Subject          string     `gorm:"column:subject"`
	Payload          []byte     `gorm:"column:payload"`
	OccurredAt       time.Time  `gorm:"column:occurred_at"`
	AvailableAt      time.Time  `gorm:"column:available_at"`
	PublishedAt      *time.Time `gorm:"column:published_at"`
	DeliveryAttempts int        `gorm:"column:delivery_attempts"`
	LastFailureName  *string    `gorm:"column:last_failure_name"`
	LeaseOwner       *string    `gorm:"column:lease_owner"`
	LeaseExpiresAt   *time.Time `gorm:"column:lease_expires_at"`
}

func (OutboxEventModel) TableName() string { return "outbox_events" }

// Actor describes the snapshot of the actor who triggered a domain event.
type Actor struct {
	Kind     string `json:"kind"`
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
}

// AuditInput is the input for a single audit event. Metadata must never contain secrets, SQL text, or query results.
type AuditInput struct {
	EventType     string
	ActorKind     string // user | system | worker | cli
	ActorID       *string
	ActorUsername *string
	ResourceType  string
	ResourceID    *string
	Action        string
	Outcome       string // succeeded | denied | failed
	RequestID     *string
	SourceIP      *string
	UserAgent     *string
	Metadata      map[string]any
}

// Recorder writes audit and domain events within an existing or standalone transaction.
type Recorder struct {
	db  *gorm.DB
	hub atomic.Pointer[realtime.Hub]
}

// NewRecorder constructs a Recorder.
func NewRecorder(db *gorm.DB) (*Recorder, error) {
	if db == nil {
		return nil, errors.New("auditlog: nil db")
	}
	return &Recorder{db: db}, nil
}

// SetRealtimeHub attaches an optional in-process realtime Hub for SSE event fan-out.
func (r *Recorder) SetRealtimeHub(h *realtime.Hub) {
	if r == nil {
		return
	}
	r.hub.Store(h)
}

// RealtimeHub returns the attached realtime Hub, or nil when none is wired.
func (r *Recorder) RealtimeHub() *realtime.Hub {
	if r == nil {
		return nil
	}
	return r.hub.Load()
}

// Audit writes a single audit event; uses a standalone transaction when tx is nil.
func (r *Recorder) Audit(ctx context.Context, tx *gorm.DB, in AuditInput) error {
	meta, err := json.Marshal(in.Metadata)
	if err != nil {
		return fmt.Errorf("auditlog: metadata encode: %w", err)
	}
	if in.Metadata == nil {
		meta = []byte("{}")
	}
	now := time.Now().UTC()
	row := AuditEventModel{
		ID:                    uuid.NewString(),
		EventType:             in.EventType,
		ActorKind:             in.ActorKind,
		ActorID:               in.ActorID,
		ActorUsernameSnapshot: in.ActorUsername,
		ResourceType:          in.ResourceType,
		ResourceID:            in.ResourceID,
		Action:                in.Action,
		Outcome:               in.Outcome,
		RequestID:             in.RequestID,
		SourceIP:              in.SourceIP,
		UserAgent:             in.UserAgent,
		Metadata:              meta,
		OccurredAt:            now,
		ExpiresAt:             now.Add(90 * 24 * time.Hour),
	}
	writer := r.db
	if tx != nil {
		writer = tx
	}
	if err := writer.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	return nil
}

// Emit writes domain_events and outbox_events within the same transaction.
// Sequence numbers are allocated per subject (MAX+1) with brief retries on unique constraint conflicts.
func (r *Recorder) Emit(ctx context.Context, tx *gorm.DB, eventType, subject string, actor Actor, data map[string]any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("auditlog: event data encode: %w", err)
	}
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return fmt.Errorf("auditlog: actor encode: %w", err)
	}
	writer := r.db
	if tx != nil {
		writer = tx
	}
	now := time.Now().UTC()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var maxSeq *int64
		if err := writer.WithContext(ctx).
			Model(&DomainEventModel{}).
			Where("subject = ?", subject).
			Select("max(sequence)").Scan(&maxSeq).Error; err != nil {
			return err
		}
		seq := int64(1)
		if maxSeq != nil {
			seq = *maxSeq + 1
		}
		evt := DomainEventModel{
			ID:            uuid.NewString(),
			EventType:     eventType,
			Subject:       subject,
			Sequence:      seq,
			CorrelationID: uuid.NewString(),
			Actor:         actorJSON,
			EventData:     payload,
			OccurredAt:    now,
		}
		if err := writer.WithContext(ctx).Create(&evt).Error; err != nil {
			lastErr = err
			if isUniqueViolation(err) {
				continue
			}
			return err
		}
		// outbox references domain_events using the same ID (logical-relations:
		// outbox_events.id -> domain_events.id, delete_source).
		outbox := OutboxEventModel{
			ID:          evt.ID,
			EventType:   eventType,
			Subject:     subject,
			Payload:     payload,
			OccurredAt:  now,
			AvailableAt: now,
		}
		if err := writer.WithContext(ctx).Create(&outbox).Error; err != nil {
			return err
		}
		r.broadcastRealtime(ctx, writer, evt.ID, eventType, subject, extractEventState(data), now, actor)
		return nil
	}
	return fmt.Errorf("auditlog: sequence allocation exhausted: %w", lastErr)
}

// NotifyRealtime broadcasts a real-time SSE event without writing outbox_events or domain_events.
// Used by mutations that only record Audit entries (e.g. query access request create/withdraw, grant relinquish).
func (r *Recorder) NotifyRealtime(ctx context.Context, tx *gorm.DB, eventType, subject, state string, actor Actor) error {
	if r == nil {
		return nil
	}
	writer := r.db
	if tx != nil {
		writer = tx
	}
	r.broadcastRealtime(ctx, writer, uuid.NewString(), eventType, subject, state, time.Now().UTC(), actor)
	return nil
}

// NotifyRealtimeTargets broadcasts a real-time SSE event with caller-supplied target user IDs
// (for example, pre-mutation union of old and new permission-group members).
func (r *Recorder) NotifyRealtimeTargets(
	ctx context.Context,
	tx *gorm.DB,
	eventType, subject, state string,
	actor Actor,
	targetUserIDs []string,
	notifyAdmins bool,
) error {
	if r == nil {
		return nil
	}
	writer := r.db
	if tx != nil {
		writer = tx
	}
	hub := r.RealtimeHub()
	isPG := writer != nil && writer.Dialector != nil && writer.Dialector.Name() == "postgres"
	if hub == nil && !isPG {
		return nil
	}
	seen := make(map[string]struct{}, len(targetUserIDs)+1)
	targets := make([]string, 0, len(targetUserIDs)+1)
	add := func(uid string) {
		uid = strings.TrimSpace(uid)
		if uid == "" || uid == systemNilUserID {
			return
		}
		if _, ok := seen[uid]; ok {
			return
		}
		seen[uid] = struct{}{}
		targets = append(targets, uid)
	}
	add(actor.UserID)
	for _, uid := range targetUserIDs {
		add(uid)
	}
	r.dispatchRealtime(ctx, writer, hub, realtime.BroadcastMessage{
		Envelope: realtime.EventEnvelope{
			ID:         uuid.NewString(),
			Type:       externalEventType(eventType),
			Subject:    subject,
			Aggregate:  aggregateFromSubject(subject),
			State:      state,
			OccurredAt: time.Now().UTC(),
		},
		TargetUserIDs: targets,
		NotifyAdmins:  notifyAdmins,
	})
	return nil
}

func (r *Recorder) broadcastRealtime(ctx context.Context, writer *gorm.DB, eventID, eventType, subject, state string, occurredAt time.Time, actor Actor) {
	hub := r.RealtimeHub()
	isPG := writer != nil && writer.Dialector != nil && writer.Dialector.Name() == "postgres"
	if hub == nil && !isPG {
		return
	}
	targets, notifyAdmins := resolveRealtimeAudience(ctx, writer, subject, actor)
	msg := realtime.BroadcastMessage{
		Envelope: realtime.EventEnvelope{
			ID:         eventID,
			Type:       externalEventType(eventType),
			Subject:    subject,
			Aggregate:  aggregateFromSubject(subject),
			State:      state,
			OccurredAt: occurredAt,
		},
		TargetUserIDs: targets,
		NotifyAdmins:  notifyAdmins,
	}
	r.dispatchRealtime(ctx, writer, hub, msg)
}

func (r *Recorder) dispatchRealtime(ctx context.Context, writer *gorm.DB, hub *realtime.Hub, msg realtime.BroadcastMessage) {
	if err := realtime.NotifyPostgresTx(ctx, writer, msg); err != nil {
		zap.L().Warn("auditlog: realtime pg_notify failed",
			zap.String("event_type", msg.Envelope.Type),
			zap.String("subject", msg.Envelope.Subject),
			zap.Error(err),
		)
	}
	if hub != nil && !hub.PGListenerActive() {
		hub.Publish(msg)
	}
}

const systemNilUserID = "00000000-0000-0000-0000-000000000000"

// resolveRealtimeAudience resolves least-privilege recipient user IDs for a domain subject.
// Respects admin_is_not_business_override: NotifyAdmins is always false for change_orders,
// change_drafts, review_runs, and query_* aggregates.
func resolveRealtimeAudience(ctx context.Context, db *gorm.DB, subject string, actor Actor) ([]string, bool) {
	seen := make(map[string]struct{})
	var targets []string
	addUser := func(uid string) {
		uid = strings.TrimSpace(uid)
		if uid == "" || uid == systemNilUserID {
			return
		}
		if _, ok := seen[uid]; ok {
			return
		}
		seen[uid] = struct{}{}
		targets = append(targets, uid)
	}
	addUsers := func(uids []string) {
		for _, uid := range uids {
			addUser(uid)
		}
	}

	addUser(actor.UserID)

	var notifyAdmins bool
	prefix, aggID := splitSubject(subject)
	if db != nil && aggID != "" {
		switch prefix {
		case "query-sessions":
			var uids []string
			if err := db.WithContext(ctx).Raw(
				`select user_id_snapshot from query_sessions where id = ?`, aggID).
				Scan(&uids).Error; err == nil {
				addUsers(uids)
			}
		case "query-grants":
			var uids []string
			if err := db.WithContext(ctx).Raw(
				`select requester_user_id_snapshot from query_grants where id = ?`, aggID).
				Scan(&uids).Error; err == nil {
				addUsers(uids)
			}
			var reviewers []string
			if err := db.WithContext(ctx).Raw(`
				select a.reviewer_user_id_snapshot
				from query_access_approval_actors a
				join query_access_approval_steps st on st.id = a.step_id
				join query_grants g on g.query_access_request_id = st.request_id
				where g.id = ?`, aggID).Scan(&reviewers).Error; err == nil {
				addUsers(reviewers)
			}
		case "query-access-requests":
			var uids []string
			if err := db.WithContext(ctx).Raw(
				`select requester_user_id_snapshot from query_access_requests where id = ?`, aggID).
				Scan(&uids).Error; err == nil {
				addUsers(uids)
			}
			var reviewers []string
			if err := db.WithContext(ctx).Raw(`
				select a.reviewer_user_id_snapshot
				from query_access_approval_actors a
				join query_access_approval_steps st on st.id = a.step_id
				where st.request_id = ?`, aggID).Scan(&reviewers).Error; err == nil {
				addUsers(reviewers)
			}
		case "query-executions":
			var uids []string
			if err := db.WithContext(ctx).Raw(
				`select user_id_snapshot from query_executions where id = ?`, aggID).
				Scan(&uids).Error; err == nil {
				addUsers(uids)
			}
		case "change-orders":
			var submitters []string
			if err := db.WithContext(ctx).Raw(
				`select submitter_user_id_snapshot from change_orders where id = ?`, aggID).
				Scan(&submitters).Error; err == nil {
				addUsers(submitters)
			}
			var reviewers []string
			if err := db.WithContext(ctx).Raw(`
				select aa.reviewer_user_id_snapshot
				from order_approval_actors aa
				join order_approval_steps st on st.id = aa.step_id
				join order_stages sg on sg.id = st.stage_id
				where sg.order_id = ?`, aggID).Scan(&reviewers).Error; err == nil {
				addUsers(reviewers)
			}
			var executors []string
			if err := db.WithContext(ctx).Raw(`
				select ea.executor_user_id_snapshot
				from order_execution_actors ea
				join order_stages sg on sg.id = ea.stage_id
				where sg.order_id = ?`, aggID).Scan(&executors).Error; err == nil {
				addUsers(executors)
			}
		case "change-drafts":
			var owners []string
			if err := db.WithContext(ctx).Raw(
				`select owner_user_id from change_drafts where id = ?`, aggID).
				Scan(&owners).Error; err == nil {
				addUsers(owners)
			}
		case "review-runs":
			var owners []string
			if err := db.WithContext(ctx).Raw(`
				select d.owner_user_id
				from change_drafts d
				join review_runs r on r.draft_id = d.id
				where r.id = ?`, aggID).Scan(&owners).Error; err == nil {
				addUsers(owners)
			}
		case "flow":
			var grantedUsers []string
			if err := db.WithContext(ctx).Raw(`
				select distinct m.user_id
				from permission_group_members m
				join permission_group_flow_grants g on g.permission_group_id = m.permission_group_id
				join permission_groups pg on pg.id = g.permission_group_id
				where g.flow_id = ? and pg.enabled = true`, aggID).Scan(&grantedUsers).Error; err == nil {
				addUsers(grantedUsers)
			}
			notifyAdmins = true
		case "user":
			notifyAdmins = true
		}
	} else if prefix == "flow" || prefix == "user" {
		notifyAdmins = true
	}

	return targets, notifyAdmins
}

func splitSubject(subject string) (prefix, id string) {
	if idx := strings.LastIndexByte(subject, '/'); idx >= 0 {
		return subject[:idx], subject[idx+1:]
	}
	if idx := strings.LastIndexByte(subject, ':'); idx >= 0 {
		return subject[:idx], subject[idx+1:]
	}
	return subject, ""
}

func aggregateFromSubject(subject string) string {
	_, id := splitSubject(subject)
	if id != "" {
		return id
	}
	return subject
}

// externalEventType normalizes internal event names to the CloudEvents-compatible
// `io.yearning.v4.*` vocabulary defined by api/contracts/repository-event-routing.json.
func externalEventType(internalType string) string {
	if strings.HasPrefix(internalType, "io.yearning.v4.") {
		return internalType
	}
	switch internalType {
	case "change_order.submitted":
		return "io.yearning.v4.change_order.submitted"
	case "change_order.approval_decided":
		return "io.yearning.v4.approval.decided"
	case "change_order.step_activated":
		return "io.yearning.v4.change_order.step_activated"
	case "change_order.stage_ready_for_execution",
		"change_order.rejected",
		"change_order.withdrawn",
		"change_order.voided",
		"workflow.node_invalidated":
		return "io.yearning.v4.change_order.state_changed"
	case "execution.unknown_resolved":
		return "io.yearning.v4.execution.verification_recorded"
	case "query_access.requested",
		"query_access.withdrawn",
		"query_access.approved",
		"query_access.rejected",
		"query_access.state_changed":
		return "io.yearning.v4.query_access.state_changed"
	case "query_grant.activated":
		return "io.yearning.v4.query_grant.activated"
	case "query_grant.revoked",
		"query_grant.relinquished":
		return "io.yearning.v4.query_grant.revoked"
	case "query_session.opened",
		"query_session.revocation_requested",
		"query_session.state_changed":
		return "io.yearning.v4.query_session.state_changed"
	case "query_session.closed":
		return "io.yearning.v4.query_session.closed"
	case "query.execution_completed",
		"query.execution_failed",
		"query.execution_result_unknown",
		"query_execution.state_changed":
		return "io.yearning.v4.query_execution.state_changed"
	case "change_draft.revised",
		"change_draft.precheck_completed",
		"change_draft.state_changed":
		return "io.yearning.v4.change_draft.state_changed"
	case "review_run.completed", "review.completed":
		return "io.yearning.v4.review.completed"
	case "review.blocked":
		return "io.yearning.v4.review.blocked"
	case "flow.updated":
		return "io.yearning.v4.flow.updated"
	case "user.deleted":
		return "io.yearning.v4.user.deleted"
	}
	if strings.HasPrefix(internalType, "execution.") {
		return "io.yearning.v4.execution.state_changed"
	}
	return "io.yearning.v4." + internalType
}

func extractEventState(data map[string]any) string {
	if data == nil {
		return ""
	}
	if v, ok := data["state"].(string); ok && v != "" {
		return v
	}
	if v, ok := data["to"].(string); ok && v != "" {
		return v
	}
	return ""
}

func isUniqueViolation(err error) bool { return txutil.IsUniqueViolation(err) }

// HMACFingerprint computes a hex HMAC over arbitrary bytes using the payload purpose (for non-sensitive integrity checks).
func HMACFingerprint(c *crypto.Crypter, purpose string, data []byte) (string, error) {
	mac, err := c.HMAC(purpose, data)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", mac), nil
}
