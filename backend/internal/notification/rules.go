// rules.go notification rules surface (S007 / RCP-20261031-NOTIFICATION-RULES-AND-CHANNELS):
// rule CRUD + target resolution + preview. A rule consists of name + enabled flag + flow scope
// (flow_id null = all flows) + one trigger event + one target role + one channel. The event_type
// vocabulary is pinned by the migration CHECK constraint (structurally excluding AI review events —
// vocabulary constants in this file align verbatim with migration 000021 CHECK).
// Recipients are resolved from frozen stage/step snapshots on the change order, never from live flow templates.
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// Target role kinds (aligned with migration 000021 CHECK constraint).
const (
	TargetSubmitter      = "order_submitter"
	TargetStageApprovers = "stage_approvers"
	TargetStageExecutors = "stage_executors"
	TargetGrantRequester = "grant_requester"
	TargetBuiltinAdmins  = "builtin_admins"
)

var targetKinds = map[string]bool{
	TargetSubmitter: true, TargetStageApprovers: true, TargetStageExecutors: true,
	TargetGrantRequester: true, TargetBuiltinAdmins: true,
}

// EventVocabulary aligns verbatim with the 000021 notification_rules_event_type CHECK constraint
// (validator pins consistency across both sides; AI review events are excluded).
var EventVocabulary = []string{
	"change_order.submitted",
	"change_order.step_activated",
	"change_order.approval_decided",
	"change_order.stage_ready_for_execution",
	"change_order.rejected",
	"change_order.withdrawn",
	"change_order.voided",
	"execution.scheduled",
	"execution.schedule_cancelled",
	"execution.started",
	"execution.succeeded",
	"execution.failed",
	"execution.partial_failed",
	"execution.cancelled",
	"execution.partial_cancelled",
	"execution.result_unknown",
	"execution.blocked_datasource_unavailable",
	"execution.missed_schedule",
	"query_grant.revoked",
	"ai.budget_warning",
	"ai.budget_exhausted",
	"ai.provider_failover",
}

var eventVocabularySet = func() map[string]bool {
	m := make(map[string]bool, len(EventVocabulary))
	for _, e := range EventVocabulary {
		m[e] = true
	}
	return m
}()

// todoEvents personal todo event family (subject to pre-send suppression checks when the todo has already been handled).
var todoEvents = map[string]bool{
	"change_order.submitted": true, "change_order.step_activated": true,
	"change_order.stage_ready_for_execution": true,
}

// RuleView conforms to OpenAPI NotificationRule (read surface).
type RuleView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Enabled         bool    `json:"enabled"`
	FlowID          *string `json:"flow_id"`
	EventType       string  `json:"event_type"`
	TargetKind      string  `json:"target_kind"`
	ChannelID       string  `json:"channel_id"`
	MessageTemplate *string `json:"message_template"`
	Version         int64   `json:"version"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// RuleWrite conforms to OpenAPI NotificationRuleWrite.
type RuleWrite struct {
	Name            string  `json:"name"`
	Enabled         bool    `json:"enabled"`
	FlowID          *string `json:"flow_id"`
	EventType       string  `json:"event_type"`
	TargetKind      string  `json:"target_kind"`
	ChannelID       string  `json:"channel_id"`
	MessageTemplate *string `json:"message_template"`
}

func validateRuleWrite(in *RuleWrite) error {
	if in.Name == "" || len(in.Name) > 128 {
		return ErrValidation
	}
	if !eventVocabularySet[in.EventType] || !targetKinds[in.TargetKind] {
		return ErrValidation
	}
	if in.FlowID != nil && *in.FlowID == "" {
		in.FlowID = nil
	}
	if in.ChannelID == "" {
		return ErrValidation
	}
	if in.MessageTemplate != nil {
		trimmed := strings.TrimSpace(*in.MessageTemplate)
		if trimmed == "" {
			in.MessageTemplate = nil
		} else {
			if err := ValidateMessageTemplate(trimmed); err != nil {
				return err
			}
			in.MessageTemplate = &trimmed
		}
	}
	return nil
}

func ruleView(r *NotificationRuleModel) *RuleView {
	return &RuleView{
		ID: r.ID, Name: r.Name, Enabled: r.Enabled, FlowID: r.FlowID,
		EventType: r.EventType, TargetKind: r.TargetKind, ChannelID: r.ChannelID,
		MessageTemplate: r.MessageTemplate,
		Version:         r.Version,
		CreatedAt:       r.CreatedAt.UTC().Format(timeRFC3339Nano),
		UpdatedAt:       r.UpdatedAt.UTC().Format(timeRFC3339Nano),
	}
}

// ListRules lists notification rules (admin surface).
func (s *Service) ListRules(ctx context.Context) ([]RuleView, error) {
	var rows []NotificationRuleModel
	if err := s.DB.WithContext(ctx).Order("event_type, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RuleView, 0, len(rows))
	for i := range rows {
		out = append(out, *ruleView(&rows[i]))
	}
	return out, nil
}

// GetRule returns a single notification rule.
func (s *Service) GetRule(ctx context.Context, id string) (*RuleView, error) {
	row, err := s.loadRule(ctx, id)
	if err != nil {
		return nil, err
	}
	return ruleView(row), nil
}

func (s *Service) loadRule(ctx context.Context, id string) (*NotificationRuleModel, error) {
	var row NotificationRuleModel
	if err := s.DB.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// CreateRule creates a notification rule (channel must exist; out-of-vocabulary events are rejected
// with 1001 — backed by the storage-layer CHECK constraint as a second gate).
func (s *Service) CreateRule(ctx context.Context, actorID string, in *RuleWrite) (*RuleView, error) {
	if err := validateRuleWrite(in); err != nil {
		return nil, err
	}
	if err := s.ensureChannelExists(ctx, in.ChannelID); err != nil {
		return nil, err
	}
	now := s.NowFunc()
	row := &NotificationRuleModel{
		ID: newID(), Name: in.Name, Enabled: in.Enabled, FlowID: in.FlowID,
		EventType: in.EventType, TargetKind: in.TargetKind, ChannelID: in.ChannelID,
		MessageTemplate: in.MessageTemplate,
		Version:         1, CreatedByUserID: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return ruleView(row), nil
}

// ReplaceRule replaces a notification rule (optimistic lock).
func (s *Service) ReplaceRule(ctx context.Context, id string, expectVersion int64, in *RuleWrite) (*RuleView, error) {
	if err := validateRuleWrite(in); err != nil {
		return nil, err
	}
	row, err := s.loadRule(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectVersion != row.Version {
		return nil, ErrConflict
	}
	if err := s.ensureChannelExists(ctx, in.ChannelID); err != nil {
		return nil, err
	}
	row.Name, row.Enabled, row.FlowID = in.Name, in.Enabled, in.FlowID
	row.EventType, row.TargetKind, row.ChannelID = in.EventType, in.TargetKind, in.ChannelID
	row.MessageTemplate = in.MessageTemplate
	row.Version++
	res := s.DB.WithContext(ctx).Model(&NotificationRuleModel{}).
		Where("id = ? and version = ?", id, expectVersion).Updates(map[string]any{
		"name": row.Name, "enabled": row.Enabled, "flow_id": row.FlowID,
		"event_type": row.EventType, "target_kind": row.TargetKind,
		"channel_id": row.ChannelID, "message_template": row.MessageTemplate,
		"version": row.Version, "updated_at": s.NowFunc(),
	})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrConflict
	}
	return ruleView(row), nil
}

// DeleteRule deletes a notification rule (optimistic lock; idempotent 404).
func (s *Service) DeleteRule(ctx context.Context, id string, expectVersion int64) error {
	row, err := s.loadRule(ctx, id)
	if err != nil {
		return err
	}
	if expectVersion != row.Version {
		return ErrConflict
	}
	res := s.DB.WithContext(ctx).Where("id = ? and version = ?", id, row.Version).
		Delete(&NotificationRuleModel{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) ensureChannelExists(ctx context.Context, id string) error {
	var n int64
	if err := s.DB.WithContext(ctx).Model(&NotificationChannelModel{}).
		Where("id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return ErrValidation
	}
	return nil
}

// ---- Target resolution (shared by Worker and Preview) ----

// recipientKeyForUser / recipientKeyForGroup are the sole constructors for deduplication identities
// (R1 M7: format is pinned by the migration CHECK constraint, constructed in production via these two helpers and asserted by tests).
func recipientKeyForUser(userID string) string { return "user:" + userID }

func recipientKeyForGroup(channelID string) string { return "group:" + channelID }

// ResolvedTarget represents a resolved delivery target (individual = user + email address; group = channel-level).
type ResolvedTarget struct {
	RecipientKey string
	UserID       *string
	Address      string
}

// RuleMatch represents an enabled rule matched by an event (including its channel row for Worker delivery creation).
type RuleMatch struct {
	Rule    *NotificationRuleModel
	Channel *NotificationChannelModel
}

// loadRuleMatches loads enabled rules matching the event (filtered by flow scope; read-only query surface).
// Order-family events match rule.flow_id against the order's frozen flow_id; query/ai-family events match
// only rules with a null flow_id (all flows).
func (w *Worker) loadRuleMatches(ctx context.Context, ev *OutboxEventModel) ([]RuleMatch, error) {
	var rules []NotificationRuleModel
	if err := w.db.WithContext(ctx).
		Where("enabled = true and event_type = ?", ev.EventType).
		Order("id").Find(&rules).Error; err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	flowID := w.eventFlowID(ctx, ev)
	matches := make([]RuleMatch, 0, len(rules))
	for i := range rules {
		r := &rules[i]
		if r.FlowID != nil {
			if flowID == "" || *r.FlowID != flowID {
				continue
			}
		}
		ch, err := w.loadChannel(ctx, r.ChannelID)
		if err != nil {
			w.logSkip(ctx, ev.EventType, r.ChannelID, "CHANNEL_DELETED")
			continue // Channel deleted: rule does not take effect
		}
		if !ch.Enabled {
			w.logSkip(ctx, ev.EventType, r.ChannelID, "CHANNEL_DISABLED")
			continue // Disabling a channel acts as the send safety switch
		}
		matches = append(matches, RuleMatch{Rule: r, Channel: ch})
	}
	return matches, nil
}

// eventFlowID extracts the frozen flow_id of the change order associated with the event (order family);
// returns empty for non-order events. The order's flow identity is stored inside the encrypted
// order_flow_snapshots payload (change_orders has no flow_id column); decryption failure or missing row
// returns empty (equivalent to "out of flow scope", failing open for global rules while failing closed for scoped rules).
func (w *Worker) eventFlowID(ctx context.Context, ev *OutboxEventModel) string {
	orderID := eventOrderID(ev)
	if orderID == "" || w.crypter == nil {
		return ""
	}
	var row struct {
		Ciphertext []byte `gorm:"column:flow_snapshot_ciphertext"`
		Nonce      []byte `gorm:"column:flow_snapshot_nonce"`
		KeyID      string `gorm:"column:flow_snapshot_key_id"`
		Algorithm  string `gorm:"column:flow_snapshot_algorithm"`
	}
	if err := w.db.WithContext(ctx).Raw(`
		select flow_snapshot_ciphertext, flow_snapshot_nonce,
		       flow_snapshot_key_id, flow_snapshot_algorithm
		from order_flow_snapshots where order_id = ?`, orderID).
		Scan(&row).Error; err != nil || row.Ciphertext == nil {
		return ""
	}
	plain, err := w.crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce,
		KeyID: row.KeyID, Algorithm: row.Algorithm,
	}, crypto.AAD{
		DeploymentID: w.crypter.DeploymentID(), Table: "order_flow_snapshots",
		PrimaryKey: orderID, Column: "flow_snapshot_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return ""
	}
	var doc struct {
		FlowID string `json:"flow_id"`
	}
	if json.Unmarshal(plain, &doc) != nil || doc.FlowID == "" {
		return ""
	}
	return doc.FlowID
}

// eventOrderID extracts the change order ID from the event subject or payload.
func eventOrderID(ev *OutboxEventModel) string {
	var payload struct {
		AggregateID string `json:"aggregate_id"`
		OrderID     string `json:"order_id"`
	}
	_ = json.Unmarshal(ev.Payload, &payload)
	if payload.AggregateID != "" {
		return payload.AggregateID
	}
	if payload.OrderID != "" {
		return payload.OrderID
	}
	return trimSubjectID(ev.Subject, "change-orders/")
}

func trimSubjectID(subject, prefix string) string {
	if len(subject) > len(prefix) && subject[:len(prefix)] == prefix {
		return subject[len(prefix):]
	}
	return ""
}

// eventStepID extracts the newly activated step ID for step_activated events (returns empty for other events).
func eventStepID(ev *OutboxEventModel) string {
	if ev.EventType != "change_order.step_activated" {
		return ""
	}
	var payload struct {
		StepID string `json:"step_id"`
	}
	_ = json.Unmarshal(ev.Payload, &payload)
	return payload.StepID
}

// resolveRuleTargets resolves a rule + event into a set of delivery targets (raw pre-deduplication set;
// group channels always produce a single group target rather than duplicating per reviewer). Returns empty on failure or no targets.
func (w *Worker) resolveRuleTargets(ctx context.Context, m RuleMatch, ev *OutboxEventModel) []ResolvedTarget {
	if m.Channel.Kind != "email" {
		// DingTalk/Feishu/Slack channels = fixed group/channel target: single delivery, never
		// duplicated per reviewer count (PRD §5.2 / acceptance-12).
		return []ResolvedTarget{{
			RecipientKey: recipientKeyForGroup(m.Channel.ID), Address: "robot",
		}}
	}
	var userIDs []string
	switch m.Rule.TargetKind {
	case TargetSubmitter:
		userIDs = w.resolveUsers(ctx, ev, "submitter")
	case TargetStageApprovers:
		userIDs = w.resolveApprovers(ctx, ev)
	case TargetStageExecutors:
		userIDs = w.resolveExecutors(ctx, ev)
	case TargetGrantRequester:
		userIDs = w.resolveGrantRequester(ctx, ev)
	case TargetBuiltinAdmins:
		_ = w.db.WithContext(ctx).Raw(
			`select id::text from users where is_builtin_admin = true`).
			Scan(&userIDs).Error
	}
	seen := map[string]bool{}
	targets := make([]ResolvedTarget, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		var addr string
		if err := w.db.WithContext(ctx).Raw(
			`select coalesce(email, '') from users where id = ?`, uid).Scan(&addr).Error; err != nil {
			continue
		}
		if addr == "" {
			// No valid email address: explainable skip (surfaced in preview; never treating username as an email address).
			continue
		}
		u := uid
		targets = append(targets, ResolvedTarget{
			RecipientKey: recipientKeyForUser(uid), UserID: &u, Address: addr,
		})
	}
	return targets
}

// resolveUsers resolves the set of user IDs according to the target role semantics.
func (w *Worker) resolveUsers(ctx context.Context, ev *OutboxEventModel, kind string) []string {
	var out []string
	switch kind {
	case "submitter":
		orderID := eventOrderID(ev)
		if orderID == "" {
			return nil
		}
		_ = w.db.WithContext(ctx).Raw(
			`select submitter_user_id_snapshot::text from change_orders where id = ?`,
			orderID).Scan(&out).Error
	}
	return out
}

// resolveApprovers resolves frozen approvers of the currently active approval step (or the newly
// activated step for step_activated events — recipients come from frozen snapshots, never from live flow templates).
func (w *Worker) resolveApprovers(ctx context.Context, ev *OutboxEventModel) []string {
	var out []string
	if stepID := eventStepID(ev); stepID != "" {
		_ = w.db.WithContext(ctx).Raw(`
			select a.reviewer_user_id_snapshot::text
			from order_approval_actors a where a.step_id = ?`, stepID).Scan(&out).Error
		return out
	}
	orderID := eventOrderID(ev)
	if orderID == "" {
		return nil
	}
	_ = w.db.WithContext(ctx).Raw(`
		select a.reviewer_user_id_snapshot::text
		from order_approval_actors a
		join order_approval_steps st on st.id = a.step_id
		join order_stages s on s.id = st.stage_id
		where s.order_id = ? and st.state = 'active'`, orderID).Scan(&out).Error
	return out
}

// resolveExecutors resolves all frozen executors of the current stage.
func (w *Worker) resolveExecutors(ctx context.Context, ev *OutboxEventModel) []string {
	orderID := eventOrderID(ev)
	if orderID == "" {
		return nil
	}
	var out []string
	_ = w.db.WithContext(ctx).Raw(`
		select ea.executor_user_id_snapshot::text
		from order_execution_actors ea
		join order_stages st on st.id = ea.stage_id
		join change_orders o on o.id = st.order_id
		where st.order_id = ? and st.position = o.current_stage_position`,
		orderID).Scan(&out).Error
	return out
}

// resolveGrantRequester resolves the requester of a revoked query grant.
func (w *Worker) resolveGrantRequester(ctx context.Context, ev *OutboxEventModel) []string {
	grantID := ""
	var payload struct {
		GrantID string `json:"grant_id"`
	}
	_ = json.Unmarshal(ev.Payload, &payload)
	grantID = payload.GrantID
	if grantID == "" {
		grantID = trimSubjectID(ev.Subject, "query-grants/")
	}
	if grantID == "" {
		return nil
	}
	var out []string
	_ = w.db.WithContext(ctx).Raw(
		`select requester_user_id_snapshot::text from query_grants where id = ?`,
		grantID).Scan(&out).Error
	return out
}

// ---- Preview (no sending, no state mutation) ----

// RulePreviewInput conforms to OpenAPI NotificationRulePreviewRequest.
type RulePreviewInput struct {
	Rule          RuleWrite `json:"rule"`
	SampleOrderID string    `json:"sample_order_id"`
}

// PreviewTarget conforms to OpenAPI NotificationRulePreviewTarget.
type PreviewTarget struct {
	RecipientKey string  `json:"recipient_key"`
	UserID       *string `json:"recipient_user_id"`
	Address      string  `json:"masked_address"`
}

// RulePreview conforms to OpenAPI NotificationRulePreview.
type RulePreview struct {
	Targets []PreviewTarget `json:"targets"`
	Message string          `json:"message_preview"`
}

// PreviewRule previews target resolution and rendered message copy (without sending, emitting domain
// events, or mutating state). sample_order_id is optional: when omitted, returns an empty target set +
// template preview copy; email addresses are masked (retaining domain while masking local part — full addresses never appear in preview).
func (s *Service) PreviewRule(ctx context.Context, in *RulePreviewInput) (*RulePreview, error) {
	if err := validateRuleWrite(&in.Rule); err != nil {
		return nil, err
	}
	match := RuleMatch{
		Rule: &NotificationRuleModel{
			EventType: in.Rule.EventType, TargetKind: in.Rule.TargetKind,
			ChannelID: in.Rule.ChannelID, FlowID: in.Rule.FlowID,
			MessageTemplate: in.Rule.MessageTemplate,
		},
	}
	if err := s.DB.WithContext(ctx).Where("id = ?", in.Rule.ChannelID).
		First(&match.Channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrValidation
		}
		return nil, err
	}
	fake := &Worker{db: s.DB, crypter: s.Crypter, locale: "zh-CN"}
	var ev OutboxEventModel
	if in.SampleOrderID != "" {
		ev = OutboxEventModel{
			EventType: in.Rule.EventType, Subject: "change-orders/" + in.SampleOrderID,
			Payload: []byte(`{"aggregate_id":"` + in.SampleOrderID + `"}`),
		}
		// Sample change order must exist (preview must never fabricate recipients).
		var n int64
		if err := s.DB.WithContext(ctx).Model(&orderExistence{}).
			Where("id = ?", in.SampleOrderID).Count(&n).Error; err != nil || n == 0 {
			return nil, ErrValidation
		}
	} else {
		ev = OutboxEventModel{EventType: in.Rule.EventType}
	}
	targets := fake.resolveRuleTargets(ctx, match, &ev)
	out := &RulePreview{Targets: make([]PreviewTarget, 0, len(targets))}
	for _, t := range targets {
		out.Targets = append(out.Targets, PreviewTarget{
			RecipientKey: t.RecipientKey, UserID: t.UserID,
			Address: maskAddress(t.Address),
		})
	}
	customTpl := derefTemplate(in.Rule.MessageTemplate)
	if in.SampleOrderID != "" {
		out.Message = fake.EventBodyWithTemplate(ctx, &ev, "", customTpl)
	} else {
		out.Message = previewMessageWithTemplate(in.Rule.EventType, customTpl)
	}
	return out, nil
}

func derefTemplate(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// orderExistence read-only existence probe projection (avoiding a dependency on the full changeorder model).
type orderExistence struct {
	ID string `gorm:"column:id"`
}

func (orderExistence) TableName() string { return "change_orders" }

// maskAddress masks an email address (retaining <= 2 characters of the local part + ***; full addresses never appear in preview).
func maskAddress(addr string) string {
	at := -1
	for i, r := range addr {
		if r == '@' {
			at = i
			break
		}
	}
	if at <= 0 {
		return "***"
	}
	local := addr[:at]
	if len(local) > 2 {
		local = local[:2]
	}
	return local + "***" + addr[at:]
}

// previewMessage renders the preview message copy for an event using the built-in default template.
func previewMessage(eventType string) string {
	return previewMessageWithTemplate(eventType, "")
}

// previewMessageWithTemplate renders preview message copy for an event using customTemplate when
// non-empty and falling back to the built-in default template with sample placeholder values.
func previewMessageWithTemplate(eventType, customTemplate string) string {
	if _, ok := templateKey[eventType]; !ok {
		return ""
	}
	tpl := strings.TrimSpace(customTemplate)
	if tpl != "" && ValidateMessageTemplate(tpl) != nil {
		tpl = ""
	}
	if tpl == "" {
		tpl = DefaultPlaceholderTemplate(eventType, "zh-CN")
	}
	if tpl == "" {
		return ""
	}
	return RenderTemplateString(tpl, sampleTemplateVars("zh-CN", eventType))
}
