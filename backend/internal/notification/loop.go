// loop.go Worker wiring and main processing loop.
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Worker is the notification Worker (outbox consumer + delivery executor).
type Worker struct {
	mu        sync.RWMutex
	db        *gorm.DB
	crypter   *crypto.Crypter
	pollEvery time.Duration
	now       func() time.Time
	// locale notification language (S006 general.default_locale; defaults to zh-CN).
	locale string
	// DingTalkBaseURL test injection hook (defaults to https://oapi.dingtalk.com).
	DingTalkBaseURL string
	// auditor records suppression audit events (S007; nil = delete row without audit record in unit tests).
	auditor *auditlog.Recorder
	// publicBaseURL controlled origin for back-links in messages (general.public_base_url;
	// empty = omit links).
	publicBaseURL string
	// logger structured logger for explainable skips (R1 M6; nil-safe).
	logger *zap.Logger
}

// SetAuditor injects the suppression audit recorder (composition root; nil-safe).
func (w *Worker) SetAuditor(a *auditlog.Recorder) { w.auditor = a }

// NewWorker constructs a Worker.
func NewWorker(db *gorm.DB, crypter *crypto.Crypter, pollEvery time.Duration) *Worker {
	if pollEvery <= 0 {
		pollEvery = 10 * time.Second
	}
	return &Worker{
		db: db, crypter: crypter,
		pollEvery: pollEvery, now: func() time.Time { return time.Now().UTC() },
		locale: "zh-CN",
	}
}

func newID() string { return uuid.NewString() }

// Run executes the main worker loop. Exits when ctx is cancelled; individual event or delivery
// failures within a tick do not block remaining items (best-effort, never altering business outcomes).
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Tick(ctx)
		}
	}
}

// Tick executes a single processing round (callable directly in tests).
func (w *Worker) Tick(ctx context.Context) {
	var events []OutboxEventModel
	if err := w.db.WithContext(ctx).
		Where("published_at is null").Order("occurred_at, id").Limit(100).
		Find(&events).Error; err != nil {
		return
	}
	for i := range events {
		if _, err := w.processOutboxEvent(ctx, &events[i]); err != nil {
			continue
		}
	}
	var ready []NotificationDeliveryModel
	if err := w.db.WithContext(ctx).
		Where("(state = 'queued' or (state = 'failed' and delivery_attempt_count < ? and next_attempt_at <= ?) or (state = 'sending' and updated_at < ?))",
			MaxDeliveryAttempts, w.now(), w.now().Add(-sendingStaleAfter)).
		Order("created_at, id").Limit(50).Find(&ready).Error; err != nil {
		return
	}
	for i := range ready {
		ch, err := w.loadChannel(ctx, ready[i].NotificationChannel)
		if err != nil {
			continue
		}
		_ = w.processDelivery(ctx, &ready[i], ch)
	}
	// Retain dead_letter rows for 90 days (PRD08 §10): purge upon expiration; read-only visible to admin only.
	_ = w.db.WithContext(ctx).Exec(`
		delete from notification_deliveries
		where state = 'dead_letter' and updated_at < ?`,
		w.now().Add(-90*24*time.Hour)).Error
	w.processTestDeliveries(ctx)
}

func (w *Worker) loadChannel(ctx context.Context, id string) (*NotificationChannelModel, error) {
	var row NotificationChannelModel
	err := w.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	return &row, err
}

// encryptAddress seals a delivery recipient address (contract column recipient_address_ciphertext;
// AAD matches openDeliveryAddress field for field).
func (w *Worker) encryptAddress(deliveryID, address string) ([]byte, []byte, string, error) {
	return sealDeliveryAddress(w.crypter, deliveryID, address)
}

// SetLocale sets the notification language (wired from S006 settings by the composition root).
func (w *Worker) SetLocale(l string) {
	if l == "en-US" || l == "zh-CN" {
		w.mu.Lock()
		w.locale = l
		w.mu.Unlock()
	}
}

func (w *Worker) getLocale() string {
	w.mu.RLock()
	l := w.locale
	w.mu.RUnlock()
	if l == "en-US" {
		return "en-US"
	}
	return "zh-CN"
}

// SetPublicBaseURL injects the controlled public base URL (S006 general.public_base_url;
// empty string = omit links from messages — degrading gracefully without guessing request Host headers).
func (w *Worker) SetPublicBaseURL(u string) {
	w.mu.Lock()
	w.publicBaseURL = u
	w.mu.Unlock()
}

func (w *Worker) getPublicBaseURL() string {
	w.mu.RLock()
	u := w.publicBaseURL
	w.mu.RUnlock()
	return u
}

// SetLogger injects the structured logger (R1 M6 explainable skips; nil-safe).
func (w *Worker) SetLogger(l *zap.Logger) { w.logger = l }

// logSkip logs a structured skip record (bounded fields: event/channel/reason; excludes addresses and message bodies).
func (w *Worker) logSkip(ctx context.Context, eventType, channelID, reason string) {
	if w.logger == nil {
		return
	}
	w.logger.Info("notification delivery skipped",
		zap.String("event_type", eventType),
		zap.String("channel_id", channelID),
		zap.String("reason_code", reason),
	)
}

// processTestDeliveries processes notification_test_delivery async tasks
// (PRD08 §6: async task that neither reuses real domain events nor mutates business state).
func (w *Worker) processTestDeliveries(ctx context.Context) {
	var tasks []struct {
		ID      string     `gorm:"column:id"`
		Input   []byte     `gorm:"column:input"`
		Fence   int64      `gorm:"column:fence_token"`
		Owner   *string    `gorm:"column:lease_owner"`
		Expires *time.Time `gorm:"column:lease_expires_at"`
	}
	if err := w.db.WithContext(ctx).Raw(`
		select id, input, fence_token, lease_owner, lease_expires_at from async_tasks
		where kind = 'notification_test_delivery'
		  and (state = 'queued'
		       or (state = 'running' and lease_expires_at < now() and lease_owner = 'notification-worker'))
		order by created_at limit 10`).Scan(&tasks).Error; err != nil {
		return
	}
	for _, t := range tasks {
		// CAS claim (fenced concurrency protection; claims queued tasks or expired running leases
		// so tasks never stay wedged in running after a worker crash).
		res := w.db.WithContext(ctx).Exec(`
			update async_tasks set state = 'running',
			    lease_owner = ?, lease_expires_at = now() + interval '60 seconds'
			where id = ? and (state = 'queued'
			   or (state = 'running' and lease_expires_at < now() and lease_owner = ?))`,
			w.instanceName(), t.ID, w.instanceName())
		if res.Error != nil || res.RowsAffected != 1 {
			continue
		}
		var input testDeliveryInput
		_ = json.Unmarshal(t.Input, &input)
		ch, err := w.loadChannel(ctx, input.ChannelID)
		if err != nil {
			w.finishTask(ctx, t.ID, "failed", 1002, "CHANNEL_NOT_FOUND")
			continue
		}
		// Fixed test copy (carrying no domain event payload content).
		text := "Yearning test delivery"
		var sendErr error
		switch ch.Kind {
		case "email":
			var cfg EmailConfig
			_ = json.Unmarshal(ch.Configuration, &cfg)
			secret, serr := openChannelSecret(w.crypter, ch)
			if serr != nil {
				sendErr = fmt.Errorf("%s", FailAuth)
			} else {
				// Use optional encrypted test recipient when supplied; otherwise fall back to channel FromAddress.
				to := cfg.FromAddress
				if len(input.RecipientCipher) > 0 {
					decrypted, derr := openTestDeliveryRecipient(w.crypter, t.ID, &input)
					if derr != nil {
						sendErr = fmt.Errorf("%s", FailMail)
					} else {
						to = decrypted
					}
				}
				if sendErr == nil {
					sendErr = SendEmail(ctx, cfg, secret, to, "Yearning Test", text)
				}
			}
		case "dingtalk":
			secret, serr := openChannelSecret(w.crypter, ch)
			if serr != nil {
				sendErr = fmt.Errorf("%s", FailWebhook)
			} else {
				sendErr = SendDingTalk(ctx, w.DingTalkBaseURL, secret, text)
			}
		case "feishu":
			secret, serr := openChannelSecret(w.crypter, ch)
			if serr != nil {
				sendErr = fmt.Errorf("%s", FailFeishu)
			} else {
				sendErr = SendFeishu(ctx, secret, text)
			}
		case "slack":
			secret, serr := openChannelSecret(w.crypter, ch)
			if serr != nil {
				sendErr = fmt.Errorf("%s", FailSlack)
			} else {
				sendErr = SendSlack(ctx, secret, text)
			}
		default:
			sendErr = fmt.Errorf("%s", FailWebhook)
		}
		if sendErr == nil {
			w.finishTask(ctx, t.ID, "succeeded", 0, "")
		} else {
			// business_err_code uses a registered code: test delivery is essentially channel config validation,
			// so failures record VALIDATION_FAILED (1001) while the exact cause is expressed via bounded failure_name
			// (error-codes.json has no notification-specific error code; see implementation-notes).
			w.finishTask(ctx, t.ID, "failed", 1001, boundedFailure(ch.Kind, sendErr))
		}
	}
}

func (w *Worker) finishTask(ctx context.Context, id, state string, code int, failure string) {
	var err error
	if state == "succeeded" {
		// Raise progress_total to at least 1 so legacy/pre-fix rows satisfy async_task_progress_consistency.
		err = w.db.WithContext(ctx).Exec(`
			update async_tasks set state = 'succeeded', finished_at = now(), updated_at = now(),
			    progress_total = greatest(progress_total, 1), progress_completed = 1,
			    lease_owner = null, lease_expires_at = null
			where id = ? and state = 'running'`, id).Error
	} else {
		fn := failure
		err = w.db.WithContext(ctx).Exec(`
			update async_tasks set state = ?, finished_at = now(), updated_at = now(),
			    business_err_code = ?, failure_name = ?,
			    lease_owner = null, lease_expires_at = null
			where id = ? and state = 'running'`, state, code, fn, id).Error
	}
	if err != nil && w.logger != nil {
		w.logger.Warn("notification test delivery task finish failed",
			zap.String("task_id", id),
			zap.String("state", state),
			zap.Error(err),
		)
	}
}

func (w *Worker) instanceName() string { return "notification-worker" }
