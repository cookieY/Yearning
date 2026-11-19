// worker.go Outbox-driven notification Worker (PRD08 §4 + S007 rule engine):
// unpublished outbox event -> rule matching (event + flow scope) -> target resolution (frozen snapshot) ->
// delivery rows (idempotent deduplication identity = event + channel + recipient_key) -> send loop
// (<= 5 attempts with exponential backoff -> dead_letter). Business transactions write only to Outbox;
// Worker failures never roll back business state. Out-of-vocabulary events (including AI review events) produce zero deliveries.
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
)

// Recipient represents a single delivery target (UserID is nil for channel-level targets such as DingTalk group robots).
type Recipient struct {
	UserID  *string
	Address string // email address or channel-level logical address
}

// processOutboxEvent processes a single outbox event: rule matching -> target resolution -> delivery row creation
// (backed by unique(domain_event_id, channel_id, recipient_key) for concurrency safety) -> mark outbox published.
// Returns whether progress was made.
func (w *Worker) processOutboxEvent(ctx context.Context, ev *OutboxEventModel) (bool, error) {
	matches, err := w.loadRuleMatches(ctx, ev)
	if err != nil {
		return false, err
	}
	now := w.now()
	progressed := false
	for _, m := range matches {
		if m.Channel.SecretCiphertext == nil {
			// Channels without a configured Secret cannot send: skip with a structured log entry (R1 M6).
			w.logSkip(ctx, ev.EventType, m.Channel.ID, "CHANNEL_SECRET_MISSING")
			continue
		}
		targets := w.resolveRuleTargets(ctx, m, ev)
		if len(targets) == 0 {
			w.logSkip(ctx, ev.EventType, m.Channel.ID, "NO_RESOLVED_TARGET")
		}
		for _, t := range targets {
			deliveryID := newID()
			ct, nonce, keyID, aerr := w.encryptAddress(deliveryID, t.Address)
			if aerr != nil {
				return false, aerr
			}
			row := &NotificationDeliveryModel{
				ID: deliveryID, DomainEventID: ev.ID,
				NotificationChannel: m.Channel.ID, RecipientUserID: t.UserID,
				RecipientKey: t.RecipientKey, SourceRuleID: &m.Rule.ID,
				AddressCiphertext: ct, AddressNonce: nonce,
				AddressKeyID: keyID, AddressAlgorithm: "aes-256-gcm",
				State: StateQueued, CreatedAt: now, UpdatedAt: now,
			}
			// Idempotency: unique(domain_event_id, channel_id, recipient_key) guards against
			// concurrency and multiple rules hitting the same target (at most one delivery per group or individual, acceptance-12/13).
			if err := w.db.WithContext(ctx).Create(row).Error; err != nil {
				if isUniqueViolation(err) {
					continue
				}
				return false, err
			}
			progressed = true
		}
	}
	// Advance published_at (zero matched rules or zero targets = empty delivery completed, still advancing;
	// explainability is provided by the rule preview API and structured logs rather than fabricating delivery rows).
	w.markPublished(ctx, ev.ID)
	return progressed, nil
}

func (w *Worker) markPublished(ctx context.Context, id string) bool {
	res := w.db.WithContext(ctx).Model(&OutboxEventModel{}).
		Where("id = ? and published_at is null", id).
		Update("published_at", w.now())
	return res.Error == nil && res.RowsAffected == 1
}

// resolveRecipients has been superseded by the rule engine (rules.go resolveRuleTargets).
// Recipient is retained for test construction.

// suppressStaleTodo suppresses stale todo deliveries prior to sending (acceptance-16 / PRD §6.2):
// todo deliveries re-verify live change order state right before sending — if the approval step is no
// longer active or the pending-execution order has left stage_execution_pending, the todo has already
// been handled: delete the delivery row and record a bounded audit event (never recording an unsent message as delivered).
func (w *Worker) suppressStaleTodo(ctx context.Context, d *NotificationDeliveryModel, ev *OutboxEventModel) (bool, error) {
	if !todoEvents[ev.EventType] || d.RecipientUserID == nil {
		return false, nil
	}
	orderID := eventOrderID(ev)
	if orderID == "" {
		return false, nil
	}
	stale := false
	switch {
	case ev.EventType == "change_order.stage_ready_for_execution":
		var state string
		_ = w.db.WithContext(ctx).Raw(
			`select state from change_orders where id = ?`, orderID).Scan(&state).Error
		stale = state != "stage_execution_pending"
	default:
		// Approval todo: verify per recipient — the todo remains valid only if the user is still in
		// the frozen approver set of an active step and the order is still in approval state (R1 M2:
		// once step1 is approved and step2 activates, stale todos for step1 approvers must not be sent).
		var inActive int64
		_ = w.db.WithContext(ctx).Raw(`
			select count(*) from order_approval_actors a
			join order_approval_steps st on st.id = a.step_id
			join order_stages s on s.id = st.stage_id
			where s.order_id = ? and st.state = 'active'
			  and a.reviewer_user_id_snapshot = ?`,
			orderID, *d.RecipientUserID).Scan(&inActive).Error
		var state string
		_ = w.db.WithContext(ctx).Raw(
			`select state from change_orders where id = ?`, orderID).Scan(&state).Error
		stale = inActive == 0 || (state != "stage_approval_active" && state != "stage_execution_pending")
	}
	if !stale {
		return false, nil
	}
	res := w.db.WithContext(ctx).Where("id = ?", d.ID).Delete(&NotificationDeliveryModel{})
	if res.Error != nil {
		return false, res.Error
	}
	w.auditSuppression(ctx, ev, d)
	return true, nil
}

// auditSuppression records a bounded suppression audit event (metadata limited to event/channel/target
// identity, excluding addresses and free-text order fields; receiving notifications grants no access, and neither do suppression logs).
func (w *Worker) auditSuppression(ctx context.Context, ev *OutboxEventModel, d *NotificationDeliveryModel) {
	if w.auditor == nil {
		return
	}
	metadata := map[string]any{
		"event_type":     ev.EventType,
		"channel_id":     d.NotificationChannel,
		"recipient_kind": recipientKindOf(d.RecipientKey),
		"reason_code":    "SUPPRESSED_TODO_RESOLVED",
	}
	if d.RecipientUserID != nil {
		metadata["recipient_user_id"] = *d.RecipientUserID
	}
	_ = w.auditor.Audit(ctx, nil, auditlog.AuditInput{
		EventType:    "notification.suppressed",
		ActorKind:    "worker",
		ResourceType: "notification_deliveries",
		ResourceID:   &d.ID,
		Action:       "suppress",
		Outcome:      "succeeded",
		Metadata:     metadata,
	})
}

// recipientKindOf maps a recipient_key to its bounded kind (user|group).
func recipientKindOf(key string) string {
	switch {
	case strings.HasPrefix(key, "user:"):
		return "user"
	case strings.HasPrefix(key, "group:"):
		return "group"
	default:
		return "unknown"
	}
}

// sendingStaleAfter is the reclamation threshold for stuck sending rows (reclaiming leases after a pod kill;
// SMTP/HTTP send timeouts are <= 15s, so 10 minutes is well above any in-flight send).
const sendingStaleAfter = 10 * time.Minute

// processDelivery executes a single delivery attempt (claim -> sending -> send -> terminal state / backoff).
// Claim predicates include backoff expiration and sending lease expiration: multiple workers never bypass
// next_attempt_at, and sending rows stranded by a pod kill are reclaimed.
func (w *Worker) processDelivery(ctx context.Context, d *NotificationDeliveryModel, ch *NotificationChannelModel) error {
	now := w.now()
	res := w.db.WithContext(ctx).Model(&NotificationDeliveryModel{}).
		Where(`id = ? and (
			state = 'queued'
			or (state = 'failed' and delivery_attempt_count < ? and next_attempt_at <= ?)
			or (state = 'sending' and updated_at < ?))`,
			d.ID, MaxDeliveryAttempts, now, now.Add(-sendingStaleAfter)).
		Updates(map[string]any{"state": StateSending, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return nil // Already claimed by another worker or backoff has not expired yet
	}
	addr, aerr := w.openAddress(ctx, d)
	if aerr != nil {
		return w.failDelivery(ctx, d, "ADDRESS_DECRYPT_FAILED")
	}
	// Render message body (event -> template; reason = bounded delivery failure name or event reason_code).
	var ev OutboxEventModel
	if err := w.db.WithContext(ctx).Where("id = ?", d.DomainEventID).First(&ev).Error; err != nil {
		return w.failDelivery(ctx, d, "EVENT_MISSING")
	}
	// S007: pre-send suppression for todo deliveries (acceptance-16) — when the todo has already been
	// handled or the order has left the todo state, delete the row and audit instead of faking a successful delivery.
	if suppressed, serr := w.suppressStaleTodo(ctx, d, &ev); serr != nil {
		return serr
	} else if suppressed {
		return nil
	}
	reason := ev.reasonCode()
	customTemplate := w.loadRuleTemplate(ctx, d.SourceRuleID)
	body := w.EventBodyWithTemplate(ctx, &ev, reason, customTemplate)
	if body == "" {
		// In-vocabulary event missing a template (unreachable as pinned by unit tests): transition to
		// terminal dead_letter with a bounded failure name. Never record an unsent message as delivered (acceptance-16).
		res := w.db.WithContext(ctx).Model(&NotificationDeliveryModel{}).
			Where("id = ? and state = ?", d.ID, StateSending).
			Updates(map[string]any{
				"state": StateDeadLetter, "last_failure_name": "TEMPLATE_MISSING",
				"updated_at": now,
			})
		return res.Error
	}
	var sendErr error
	switch ch.Kind {
	case "email":
		var cfg EmailConfig
		_ = json.Unmarshal(ch.Configuration, &cfg)
		secret, serr := w.openSecret(ctx, ch)
		if serr != nil {
			sendErr = fmt.Errorf("%s", FailAuth)
		} else {
			sendErr = SendEmail(ctx, cfg, secret, addr, "Yearning", body)
		}
	case "dingtalk":
		secret, serr := w.openSecret(ctx, ch)
		if serr != nil {
			sendErr = fmt.Errorf("%s", FailWebhook)
		} else {
			sendErr = SendDingTalk(ctx, w.DingTalkBaseURL, secret, body)
		}
	case "feishu":
		secret, serr := w.openSecret(ctx, ch)
		if serr != nil {
			sendErr = fmt.Errorf("%s", FailFeishu)
		} else {
			sendErr = SendFeishu(ctx, secret, body)
		}
	case "slack":
		secret, serr := w.openSecret(ctx, ch)
		if serr != nil {
			sendErr = fmt.Errorf("%s", FailSlack)
		} else {
			sendErr = SendSlack(ctx, secret, body)
		}
	default:
		sendErr = fmt.Errorf("%s", FailWebhook)
	}
	if sendErr == nil {
		res := w.db.WithContext(ctx).Model(&NotificationDeliveryModel{}).
			Where("id = ?", d.ID).
			Updates(map[string]any{"state": StateSucceeded, "updated_at": now})
		return res.Error
	}
	return w.failDelivery(ctx, d, boundedFailure(ch.Kind, sendErr))
}

// boundedFailure fallback guard: last_failure_name allows only structured failure names,
// collapsing any unknown error string into a channel-level constant (PRD08 §4 bounded content).
func boundedFailure(kind string, err error) string {
	if err != nil {
		switch err.Error() {
		case FailDial, FailTLS, FailAuth, FailMail, FailWebhook, FailWebhookNOK,
			FailFeishu, FailFeishuNOK, FailSlack, FailSlackNOK:
			return err.Error()
		}
	}
	switch kind {
	case "dingtalk":
		return FailWebhook
	case "feishu":
		return FailFeishu
	case "slack":
		return FailSlack
	default:
		return FailMail
	}
}

// failDelivery handles a failed attempt: attempt < 5 -> failed + exponential backoff; 5th attempt -> dead_letter.
func (w *Worker) failDelivery(ctx context.Context, d *NotificationDeliveryModel, failureName string) error {
	now := w.now()
	attempts := d.AttemptCount + 1
	state := StateFailed
	if attempts >= MaxDeliveryAttempts {
		state = StateDeadLetter
	}
	backoff := time.Duration(1<<uint(attempts-1)) * time.Minute
	next := now.Add(backoff)
	fn := failureName
	res := w.db.WithContext(ctx).Model(&NotificationDeliveryModel{}).
		Where("id = ? and state = 'sending'", d.ID).
		Updates(map[string]any{
			"state": state, "delivery_attempt_count": attempts,
			"next_attempt_at": next, "last_failure_name": fn, "updated_at": now,
		})
	return res.Error
}

func (w *Worker) localeOf(_ context.Context) string { return "zh-CN" }

// loadRuleTemplate loads the optional custom message_template configured on the source rule of a delivery.
func (w *Worker) loadRuleTemplate(ctx context.Context, ruleID *string) string {
	if ruleID == nil || *ruleID == "" || w.db == nil {
		return ""
	}
	var tpl *string
	if err := w.db.WithContext(ctx).Raw(
		`select message_template from notification_rules where id = ?`, *ruleID).Scan(&tpl).Error; err != nil || tpl == nil {
		return ""
	}
	return *tpl
}

// reasonCode extracts a bounded reason code from the event payload (validated against
// reject_reason_code / reason_code / failure_name format; returns empty when absent or invalid).
func (ev *OutboxEventModel) reasonCode() string {
	var p struct {
		ReasonCode  string `json:"reason_code"`
		FailureName string `json:"failure_name"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	for _, c := range []string{p.ReasonCode, p.FailureName} {
		if validReason(c) {
			return c
		}
	}
	return ""
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
