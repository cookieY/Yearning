// service.go notification channel service (PRD08 §5-6): channel CRUD + Secret envelope + test delivery.
// Secrets are write-only (never echoed by APIs); non-sensitive settings like host/port are stored in plaintext jsonb.
package notification

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// ErrValidation / ErrNotFound are stable domain errors (mapped to 1001/1002).
var (
	ErrValidation = errors.New("notification: validation failed")
	ErrNotFound   = errors.New("notification: not found")
	ErrConflict   = errors.New("notification: concurrent modification")
)

// EmailConfig conforms to OpenAPI EmailNotificationConfig.
type EmailConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	TLSMode     string `json:"tls_mode"` // required|starttls
	Username    string `json:"username"`
	FromAddress string `json:"from_address"`
}

// DingTalkConfig conforms to OpenAPI DingTalkNotificationConfig.
type DingTalkConfig struct {
	WebhookHost string `json:"webhook_host"` // oapi.dingtalk.com
}

// FeishuConfig conforms to OpenAPI FeishuNotificationConfig (S007): the configuration surface has
// no non-sensitive fields; credentials (token-bearing webhook URL + optional signing secret) go into the Secret JSON envelope.
type FeishuConfig struct{}

// SlackConfig conforms to OpenAPI SlackNotificationConfig (S007): the full Incoming
// Webhook URL is the Secret, leaving the configuration surface as an empty object.
type SlackConfig struct{}

// timeRFC3339Nano timestamp serialization format (aligned with ChannelView).
const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

// Service is the notification domain application service.
type Service struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter
	// NowFunc is an injectable clock for tests.
	NowFunc func() time.Time
	// DingTalkBaseURL is a test injection hook (defaults to https://oapi.dingtalk.com).
	DingTalkBaseURL string
}

// NewService constructs a Service.
func NewService(db *gorm.DB, crypter *crypto.Crypter) *Service {
	return &Service{DB: db, Crypter: crypter, NowFunc: func() time.Time { return time.Now().UTC() }}
}

// ChannelView conforms to OpenAPI NotificationChannel (Secrets are never echoed).
type ChannelView struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Configuration any    `json:"configuration"`
	SecretSet     bool   `json:"secret_configured"`
	Version       int64  `json:"version"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// WriteInput conforms to OpenAPI NotificationChannelWrite.
type WriteInput struct {
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	Configuration json.RawMessage `json:"configuration"`
	Secret        *struct {
		Value string `json:"value"`
	} `json:"secret"`
}

func validateWrite(in *WriteInput) (any, error) {
	if in.Name == "" || len(in.Name) > 128 {
		return nil, ErrValidation
	}
	switch in.Kind {
	case "email":
		var c EmailConfig
		if err := json.Unmarshal(in.Configuration, &c); err != nil {
			return nil, ErrValidation
		}
		if c.Host == "" || len(c.Host) > 253 || c.Port < 1 || c.Port > 65535 {
			return nil, ErrValidation
		}
		if c.TLSMode != "required" && c.TLSMode != "starttls" {
			return nil, ErrValidation
		}
		if len(c.Username) > 320 {
			return nil, ErrValidation
		}
		return c, nil
	case "dingtalk":
		var c DingTalkConfig
		if err := json.Unmarshal(in.Configuration, &c); err != nil {
			return nil, ErrValidation
		}
		if c.WebhookHost != "oapi.dingtalk.com" {
			return nil, ErrValidation
		}
		return c, nil
	case "feishu":
		// Configuration surface is an empty object; credentials go into Secret (webhook URL must match
		// the official v2 hook format pinned to host open.feishu.cn — validated inside validateSecretValue).
		var c FeishuConfig
		if err := json.Unmarshal(in.Configuration, &c); err != nil {
			return nil, ErrValidation
		}
		return c, nil
	case "slack":
		var c SlackConfig
		if err := json.Unmarshal(in.Configuration, &c); err != nil {
			return nil, ErrValidation
		}
		return c, nil
	default:
		return nil, ErrValidation
	}
}

// ListChannels lists notification channels (admin surface).
func (s *Service) ListChannels(ctx context.Context) ([]ChannelView, error) {
	var rows []NotificationChannelModel
	if err := s.DB.WithContext(ctx).Order("kind, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ChannelView, 0, len(rows))
	for i := range rows {
		v, err := s.channelView(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// GetChannel returns a single notification channel.
func (s *Service) GetChannel(ctx context.Context, id string) (*ChannelView, error) {
	row, err := s.loadChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.channelView(row)
}

func (s *Service) loadChannel(ctx context.Context, id string) (*NotificationChannelModel, error) {
	var row NotificationChannelModel
	if err := s.DB.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (s *Service) channelView(row *NotificationChannelModel) (*ChannelView, error) {
	var cfg any = json.RawMessage(row.Configuration)
	if row.Kind == "email" {
		var c EmailConfig
		_ = json.Unmarshal(row.Configuration, &c)
		cfg = c
	} else if row.Kind == "dingtalk" {
		var c DingTalkConfig
		_ = json.Unmarshal(row.Configuration, &c)
		cfg = c
	} else if row.Kind == "feishu" {
		var c FeishuConfig
		_ = json.Unmarshal(row.Configuration, &c)
		cfg = c
	} else if row.Kind == "slack" {
		var c SlackConfig
		_ = json.Unmarshal(row.Configuration, &c)
		cfg = c
	}
	return &ChannelView{
		ID: row.ID, Kind: row.Kind, Name: row.Name, Enabled: row.Enabled,
		Configuration: cfg, SecretSet: row.SecretCiphertext != nil,
		Version:   row.Version,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// CreateChannel creates a notification channel (Secret envelope: purpose=notification_secret;
// AAD bound to table + PK + column, following the same contract as credential envelopes).
func (s *Service) CreateChannel(ctx context.Context, actorID string, in *WriteInput) (*ChannelView, error) {
	cfg, err := validateWrite(in)
	if err != nil {
		return nil, err
	}
	now := s.NowFunc()
	row := &NotificationChannelModel{
		ID: newID(), Kind: in.Kind, Name: in.Name, Enabled: in.Enabled,
		Version: 1, CreatedByUserID: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if row.Configuration, err = json.Marshal(cfg); err != nil {
		return nil, err
	}
	if in.Secret != nil && in.Secret.Value != "" {
		if err := validateSecretValue(in.Kind, in.Secret.Value); err != nil {
			return nil, err
		}
		if err := s.sealSecret(row, in.Secret.Value); err != nil {
			return nil, err
		}
	}
	if err := s.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	// M1/R1: PRD §7.4 default rule seeding on fresh install — when the very first channel is created,
	// no notification rules exist yet, so seed the default-enabled rule family (default-disabled rules
	// are not seeded and can be added by admins on demand). Idempotent: triggers only when
	// notification_rules is empty; seeded rule names carry a recognizable prefix and can be edited or deleted.
	if err := s.seedDefaultRules(ctx, row); err != nil {
		return nil, err
	}
	return s.channelView(row)
}

// defaultRuleSeeds defines the PRD §7.4 default-enabled rule family (submitted -> current step approvers,
// step activated -> step approvers, stage ready for execution -> submitter + executors,
// rejected/withdrawn/voided -> submitter + approvers, failed/cancelled/unknown/missed schedule -> submitter + executors).
var defaultRuleSeeds = []struct {
	EventType  string
	TargetKind string
}{
	{"change_order.submitted", TargetStageApprovers},
	{"change_order.step_activated", TargetStageApprovers},
	{"change_order.stage_ready_for_execution", TargetSubmitter},
	{"change_order.stage_ready_for_execution", TargetStageExecutors},
	{"change_order.rejected", TargetSubmitter},
	{"change_order.rejected", TargetStageApprovers},
	{"change_order.withdrawn", TargetSubmitter},
	{"change_order.voided", TargetSubmitter},
	{"execution.failed", TargetSubmitter},
	{"execution.failed", TargetStageExecutors},
	{"execution.partial_failed", TargetSubmitter},
	{"execution.partial_failed", TargetStageExecutors},
	{"execution.cancelled", TargetSubmitter},
	{"execution.cancelled", TargetStageExecutors},
	{"execution.partial_cancelled", TargetSubmitter},
	{"execution.partial_cancelled", TargetStageExecutors},
	{"execution.result_unknown", TargetSubmitter},
	{"execution.result_unknown", TargetStageExecutors},
	{"execution.blocked_datasource_unavailable", TargetSubmitter},
	{"execution.blocked_datasource_unavailable", TargetStageExecutors},
	{"execution.missed_schedule", TargetSubmitter},
	{"execution.missed_schedule", TargetStageExecutors},
}

// seedDefaultRules seeds default rules for the first channel (only when the rules table is empty;
// seeding failures do not roll back channel creation — rules can be added manually later, whereas failing
// channel creation would block admin setup).
func (s *Service) seedDefaultRules(ctx context.Context, channel *NotificationChannelModel) error {
	var n int64
	if err := s.DB.WithContext(ctx).Model(&NotificationRuleModel{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := s.NowFunc()
	for i, seed := range defaultRuleSeeds {
		row := &NotificationRuleModel{
			ID: newID(), Name: fmt.Sprintf("default-%02d-%s", i+1, seed.EventType),
			Enabled: true, FlowID: nil, EventType: seed.EventType,
			TargetKind: seed.TargetKind, ChannelID: channel.ID,
			Version: 1, CreatedByUserID: channel.CreatedByUserID,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.DB.WithContext(ctx).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// ReplaceChannel replaces a notification channel (optimistic lock; Secret supports write-only replacement).
func (s *Service) ReplaceChannel(ctx context.Context, id string, expectVersion int64, in *WriteInput) (*ChannelView, error) {
	cfg, err := validateWrite(in)
	if err != nil {
		return nil, err
	}
	row, err := s.loadChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectVersion != row.Version {
		return nil, ErrConflict
	}
	row.Name, row.Enabled, row.Version = in.Name, in.Enabled, row.Version+1
	if row.Configuration, err = json.Marshal(cfg); err != nil {
		return nil, err
	}
	if in.Secret != nil && in.Secret.Value != "" {
		if err := validateSecretValue(in.Kind, in.Secret.Value); err != nil {
			return nil, err
		}
		if err := s.sealSecret(row, in.Secret.Value); err != nil {
			return nil, err
		}
	}
	res := s.DB.WithContext(ctx).Model(&NotificationChannelModel{}).
		Where("id = ? and version = ?", id, expectVersion).Updates(map[string]any{
		"name": row.Name, "enabled": row.Enabled, "configuration": row.Configuration,
		"secret_ciphertext": row.SecretCiphertext, "secret_nonce": row.SecretNonce,
		"secret_key_id": row.SecretKeyID, "secret_algorithm": row.SecretAlgorithm,
		"version": row.Version, "updated_at": s.NowFunc(),
	})
	if res.Error != nil {
		return nil, err
	}
	if res.RowsAffected != 1 {
		return nil, ErrConflict
	}
	return s.channelView(row)
}

// ErrChannelReferenced indicates the channel is still referenced by notification rules (R1 M5:
// blocks deletion and reports referencing rules; disabling the channel is not blocked and acts as the safety switch).
var ErrChannelReferenced = errors.New("notification: channel referenced by rules")

// CountRulesForChannel returns the number of rules referencing a channel (for frontend delete confirmation prompts).
func (s *Service) CountRulesForChannel(ctx context.Context, channelID string) (int64, error) {
	var n int64
	if err := s.DB.WithContext(ctx).Model(&NotificationRuleModel{}).
		Where("channel_id = ?", channelID).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteChannel deletes a notification channel (preserving referenced delivery history; blocked when
// referenced by any notification rule; idempotent 404).
func (s *Service) DeleteChannel(ctx context.Context, id string, expectVersion int64) error {
	row, err := s.loadChannel(ctx, id)
	if err != nil {
		return err
	}
	if expectVersion != row.Version {
		return ErrConflict
	}
	// R1 M5: channels referenced by notification rules cannot be deleted (admin must update or remove the rules first).
	var refs int64
	if err := s.DB.WithContext(ctx).Model(&NotificationRuleModel{}).
		Where("channel_id = ?", id).Count(&refs).Error; err != nil {
		return err
	}
	if refs > 0 {
		return ErrChannelReferenced
	}
	res := s.DB.WithContext(ctx).Where("id = ? and version = ?", id, row.Version).
		Delete(&NotificationChannelModel{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// sealSecret seals a channel Secret into an AEAD envelope.
func (s *Service) sealSecret(row *NotificationChannelModel, plain string) error {
	return sealChannelSecret(s.Crypter, row, plain)
}

func sealChannelSecret(crypter *crypto.Crypter, row *NotificationChannelModel, plain string) error {
	if crypter == nil || row == nil {
		return ErrValidation
	}
	env, err := crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "notification_channels",
		PrimaryKey: row.ID,
		Column:     "secret",
	}, []byte(plain))
	if err != nil {
		return err
	}
	row.SecretCiphertext, row.SecretNonce = env.Ciphertext, env.Nonce
	kid := env.KeyID
	alg := env.Algorithm
	row.SecretKeyID, row.SecretAlgorithm = &kid, &alg
	return nil
}

// openChannelSecret decrypts a channel Secret (used by both Service and Worker; fails closed on nil Crypter or empty ciphertext).
func openChannelSecret(crypter *crypto.Crypter, row *NotificationChannelModel) (string, error) {
	if crypter == nil || row == nil || len(row.SecretCiphertext) == 0 || row.SecretKeyID == nil || *row.SecretKeyID == "" || row.SecretAlgorithm == nil {
		return "", ErrValidation
	}
	plain, err := crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "notification_channels",
		PrimaryKey: row.ID,
		Column:     "secret",
	}, crypto.Envelope{
		Ciphertext: row.SecretCiphertext,
		Nonce:      row.SecretNonce,
		KeyID:      *row.SecretKeyID,
		Algorithm:  *row.SecretAlgorithm,
	})
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func sealDeliveryAddress(crypter *crypto.Crypter, deliveryID, address string) ([]byte, []byte, string, error) {
	if crypter == nil {
		return nil, nil, "", ErrValidation
	}
	env, err := crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "notification_deliveries",
		PrimaryKey: deliveryID,
		Column:     "recipient_address",
	}, []byte(address))
	if err != nil {
		return nil, nil, "", err
	}
	return env.Ciphertext, env.Nonce, env.KeyID, nil
}

// openDeliveryAddress decrypts a delivery recipient address for Worker sending and Service read-side masking
// (shared across both callers so nil Crypter and empty ciphertext guards never drift).
func openDeliveryAddress(crypter *crypto.Crypter, d *NotificationDeliveryModel) (string, error) {
	if crypter == nil || d == nil || len(d.AddressCiphertext) == 0 || d.AddressKeyID == "" {
		return "", ErrValidation
	}
	plain, err := crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "notification_deliveries",
		PrimaryKey: d.ID,
		Column:     "recipient_address",
	}, crypto.Envelope{
		Ciphertext: d.AddressCiphertext,
		Nonce:      d.AddressNonce,
		KeyID:      d.AddressKeyID,
		Algorithm:  d.AddressAlgorithm,
	})
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// randomToken generates an idempotency token for test deliveries.
func randomToken() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// validateSecretValue validates the shape of a channel Secret (performed at write time rather than
// trusting send-time input; pins official hosts rather than acting as an arbitrary-URL webhook client).
// email/dingtalk preserve existing semantics (password / access_token string); feishu = JSON envelope
// {webhook_url,sign_secret?} pinned to host open.feishu.cn; slack = full Incoming Webhook URL pinned
// to host hooks.slack.com. Validates shape only; values are never echoed, logged, or stored in plaintext configuration.
func validateSecretValue(kind, value string) error {
	if value == "" {
		return ErrValidation
	}
	switch kind {
	case "feishu":
		var s feishuSecret
		if err := json.Unmarshal([]byte(value), &s); err != nil || s.WebhookURL == "" {
			return ErrValidation
		}
		const hookPrefix = "https://open.feishu.cn/open-apis/bot/v2/hook/"
		if len(s.WebhookURL) <= len(hookPrefix) || s.WebhookURL[:len(hookPrefix)] != hookPrefix {
			return ErrValidation
		}
		if s.WebhookURL[len(hookPrefix):] != sanitizeToken(s.WebhookURL[len(hookPrefix):]) {
			return ErrValidation
		}
	case "slack":
		const hookPrefix = "https://hooks.slack.com/services/"
		if len(value) <= len(hookPrefix) || value[:len(hookPrefix)] != hookPrefix {
			return ErrValidation
		}
		if value[len(hookPrefix):] != sanitizeToken(value[len(hookPrefix):]) {
			return ErrValidation
		}
	case "dingtalk":
		// Bare access_token or {access_token,sign_secret} envelope; both token and signing secret
		// use the same character allowlist sanitizer (both are alphanumeric tokens in the official spec).
		s := parseDingTalkSecret(value)
		if s.AccessToken == "" || s.AccessToken != sanitizeToken(s.AccessToken) ||
			(s.SignSecret != "" && s.SignSecret != sanitizeToken(s.SignSecret)) {
			return ErrValidation
		}
	}
	return nil
}

// sanitizeToken validates path-segment token characters (alphanumeric and standard URL-safe characters;
// rejects whitespace, control characters, query strings, and path traversal — platform adapters are not generic webhooks).
func sanitizeToken(token string) string {
	if token == "" {
		return token
	}
	for _, r := range token {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '/'
		if !ok {
			return "\x00invalid"
		}
	}
	return token
}

// TestDeliveryOptions holds optional inputs from OpenAPI NotificationTestRequest.
type TestDeliveryOptions struct {
	Recipient string
	Locale    string
}

type testDeliveryInput struct {
	ChannelID          string `json:"channel_id"`
	RecipientCipher    []byte `json:"recipient_ciphertext,omitempty"`
	RecipientNonce     []byte `json:"recipient_nonce,omitempty"`
	RecipientKeyID     string `json:"recipient_key_id,omitempty"`
	RecipientAlgorithm string `json:"recipient_algorithm,omitempty"`
	Locale             string `json:"locale,omitempty"`
}

func validateTestRecipient(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 320 || strings.ContainsAny(trimmed, "\r\n") {
		return "", ErrValidation
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || addr.Address != trimmed || !strings.Contains(addr.Address, "@") {
		return "", ErrValidation
	}
	return addr.Address, nil
}

func sealTestDeliveryRecipient(crypter *crypto.Crypter, taskID, recipient string) (crypto.Envelope, error) {
	if crypter == nil || taskID == "" || recipient == "" {
		return crypto.Envelope{}, ErrValidation
	}
	return crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "async_tasks",
		PrimaryKey: taskID,
		Column:     "test_recipient",
	}, []byte(recipient))
}

func openTestDeliveryRecipient(crypter *crypto.Crypter, taskID string, in *testDeliveryInput) (string, error) {
	if crypter == nil || taskID == "" || in == nil || len(in.RecipientCipher) == 0 || in.RecipientKeyID == "" || in.RecipientAlgorithm == "" {
		return "", ErrValidation
	}
	plain, err := crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "async_tasks",
		PrimaryKey: taskID,
		Column:     "test_recipient",
	}, crypto.Envelope{
		Ciphertext: in.RecipientCipher,
		Nonce:      in.RecipientNonce,
		KeyID:      in.RecipientKeyID,
		Algorithm:  in.RecipientAlgorithm,
	})
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// CreateTestDelivery creates an async test delivery task (PRD08 §6: neither reuses real domain events
// nor mutates business state; kind=notification_test_delivery is polled and executed by the Worker).
func (s *Service) CreateTestDelivery(ctx context.Context, channelID string) (string, error) {
	return s.CreateTestDeliveryWithOptions(ctx, channelID, TestDeliveryOptions{})
}

// CreateTestDeliveryWithOptions creates an async test delivery task with optional OpenAPI NotificationTestRequest inputs.
// Email recipients are validated and sealed before persisting in async_tasks.input (never stored in plaintext).
func (s *Service) CreateTestDeliveryWithOptions(ctx context.Context, channelID string, opts TestDeliveryOptions) (string, error) {
	ch, err := s.loadChannel(ctx, channelID)
	if err != nil {
		return "", err
	}
	locale := strings.TrimSpace(opts.Locale)
	if locale != "" && locale != "zh-CN" && locale != "en-US" {
		return "", ErrValidation
	}
	if err := tasks.CheckPendingCapacity(ctx, s.DB); err != nil {
		return "", err
	}
	taskID := newID()
	payload := testDeliveryInput{
		ChannelID: channelID,
		Locale:    locale,
	}
	if ch.Kind == "email" && strings.TrimSpace(opts.Recipient) != "" {
		addr, err := validateTestRecipient(opts.Recipient)
		if err != nil {
			return "", err
		}
		env, err := sealTestDeliveryRecipient(s.Crypter, taskID, addr)
		if err != nil {
			return "", err
		}
		payload.RecipientCipher = env.Ciphertext
		payload.RecipientNonce = env.Nonce
		payload.RecipientKeyID = env.KeyID
		payload.RecipientAlgorithm = env.Algorithm
	}
	input, _ := json.Marshal(payload)
	agg := "notification_channel"
	now := s.NowFunc()
	if err := s.DB.WithContext(ctx).Exec(`
		insert into async_tasks (id, kind, state, aggregate_type, aggregate_id, input,
		    progress_completed, progress_total, progress_unit, fence_token, created_at, updated_at)
		values (?, 'notification_test_delivery', 'queued', ?, ?, ?, 0, 1, 'deliveries', 1, ?, ?)`,
		taskID, agg, channelID, input, now, now).Error; err != nil {
		return "", err
	}
	return taskID, nil
}
