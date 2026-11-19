// Package notification implements the S003 notification domain (PRD08; frozen tables 000001/000003):
// domain events drive deliveries via Outbox so business transactions never roll back due to send failures;
// idempotent per event + channel + recipient; up to 5 attempts with exponential backoff; dead_letter rows
// retained for 90 days and visible only to admin; notification bodies exclude raw SQL, credentials, and literals.
package notification

import "time"

// NotificationChannelModel maps to notification_channels.
type NotificationChannelModel struct {
	ID               string    `gorm:"column:id;primaryKey"`
	Kind             string    `gorm:"column:kind"` // email|dingtalk|feishu|slack (S007)
	Name             string    `gorm:"column:name"`
	Enabled          bool      `gorm:"column:enabled"`
	Configuration    []byte    `gorm:"column:configuration"`
	SecretCiphertext []byte    `gorm:"column:secret_ciphertext"`
	SecretNonce      []byte    `gorm:"column:secret_nonce"`
	SecretKeyID      *string   `gorm:"column:secret_key_id"`
	SecretAlgorithm  *string   `gorm:"column:secret_algorithm"`
	Version          int64     `gorm:"column:version"`
	CreatedByUserID  string    `gorm:"column:created_by_user_id"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (NotificationChannelModel) TableName() string { return "notification_channels" }

// NotificationDeliveryModel maps to notification_deliveries.
type NotificationDeliveryModel struct {
	ID                  string     `gorm:"column:id;primaryKey"`
	DomainEventID       string     `gorm:"column:domain_event_id"`
	NotificationChannel string     `gorm:"column:notification_channel_id"`
	RecipientUserID     *string    `gorm:"column:recipient_user_id"`
	RecipientKey        string     `gorm:"column:recipient_key"`  // user:{uuid}|group:{channel_id} (S007 deduplication identity)
	SourceRuleID        *string    `gorm:"column:source_rule_id"` // rule matched when the delivery plan was created (for explainability)
	AddressCiphertext   []byte     `gorm:"column:recipient_address_ciphertext"`
	AddressNonce        []byte     `gorm:"column:recipient_address_nonce"`
	AddressKeyID        string     `gorm:"column:recipient_address_key_id"`
	AddressAlgorithm    string     `gorm:"column:recipient_address_algorithm"`
	State               string     `gorm:"column:state"`
	AttemptCount        int        `gorm:"column:delivery_attempt_count"`
	NextAttemptAt       *time.Time `gorm:"column:next_attempt_at"`
	LastFailureName     *string    `gorm:"column:last_failure_name"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

func (NotificationDeliveryModel) TableName() string { return "notification_deliveries" }

// OutboxEventModel maps to outbox_events (the notification worker is the Outbox
// consumer; published_at is advanced by this domain).
type OutboxEventModel struct {
	ID             string     `gorm:"column:id;primaryKey"`
	EventType      string     `gorm:"column:event_type"`
	Subject        string     `gorm:"column:subject"`
	Payload        []byte     `gorm:"column:payload"`
	OccurredAt     time.Time  `gorm:"column:occurred_at"`
	AvailableAt    time.Time  `gorm:"column:available_at"`
	PublishedAt    *time.Time `gorm:"column:published_at"`
	DeliveryCount  int        `gorm:"column:delivery_attempts"`
	LastFailure    *string    `gorm:"column:last_failure_name"`
	LeaseOwner     *string    `gorm:"column:lease_owner"`
	LeaseExpiresAt *time.Time `gorm:"column:lease_expires_at"`
}

func (OutboxEventModel) TableName() string { return "outbox_events" }

// NotificationRuleModel maps to notification_rules (S007/RCP-20261031).
// event_type vocabulary is pinned by the migration CHECK constraint (structurally excluding AI review events);
// target_kind determines recipient resolution; flow_id null means all flows.
type NotificationRuleModel struct {
	ID              string    `gorm:"column:id;primaryKey"`
	Name            string    `gorm:"column:name"`
	Enabled         bool      `gorm:"column:enabled"`
	FlowID          *string   `gorm:"column:flow_id"`
	EventType       string    `gorm:"column:event_type"`
	TargetKind      string    `gorm:"column:target_kind"`
	ChannelID       string    `gorm:"column:channel_id"`
	MessageTemplate *string   `gorm:"column:message_template"`
	Version         int64     `gorm:"column:version"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (NotificationRuleModel) TableName() string { return "notification_rules" }

// Delivery states (frozen vocabulary in migration 000003 CHECK constraint).
const (
	StateQueued     = "queued"
	StateSending    = "sending"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateDeadLetter = "dead_letter"
)

// MaxDeliveryAttempts is the contract attempt ceiling (delivery_attempt_count CHECK 0..5).
const MaxDeliveryAttempts = 5
