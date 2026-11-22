// service.go wires the Query domain application services and shared helpers (B11).
// Commands follow repository-commands declarations: serializable isolation, bounded retries on 40001/40P01,
// optimistic locking CAS with RowsAffected checks, and atomic event + Outbox + idempotency response writes in a single transaction.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sync"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
)

// Actor represents the command initiator (session identity snapshot).
type Actor struct {
	UserID         string
	Username       string
	DisplayName    string
	IsBuiltinAdmin bool
}

// Services aggregates dependencies for the Query domain.
type Services struct {
	DB       *gorm.DB
	Crypter  *crypto.Crypter
	Recorder *auditlog.Recorder
	// PoolFactory allows tests to inject target pools or faults; nil uses default query credential resolution.
	PoolFactory QueryPoolFactory
	// IdentityProbe allows tests to inject server-identity probe results during on-demand identity_fingerprint
	// bootstrap; nil uses the production database.Adapter + addrguard + TLS probe path.
	IdentityProbe IdentityProbeFunc
	// MaskingSource supplies the sensitive column vocabulary; defaults to the database in production and can be overridden in tests.
	MaskingSource MaskingSource
	// instance identifies this process instance (for cursor Owner affinity).
	instance string
	// cursorIdle is the cursor idle timeout (defaults to 10 minutes; injectable for tests).
	cursorIdle time.Duration
	// cursorEvictMinIdle is the minimum idle duration before an open cursor is eligible for LRU eviction
	// when the query pool budget is exhausted (defaults to 30 seconds; injectable for tests).
	cursorEvictMinIdle    time.Duration
	cursorEvictMinIdleSet bool
	// nowFunc is an injectable clock for tests.
	nowFunc func() time.Time
	// In-process live cursor registry (Pod-affine; lost cursors expire without replay).
	regMu    sync.Mutex
	registry map[string]*liveCursor
	// In-process short-TTL metadata cache + singleflight (lazy-initialized via metadataCache()).
	metaCacheMu  sync.Mutex
	metaCache    *metadataCache
	metaCacheTTL time.Duration
}

const defaultCursorEvictMinIdle = 30 * time.Second

// NewServices constructs the Query domain application services.
func NewServices(db *gorm.DB, crypter *crypto.Crypter, recorder *auditlog.Recorder) (*Services, error) {
	if db == nil || crypter == nil || recorder == nil {
		return nil, errors.New("query: nil service dependency")
	}
	host, _ := os.Hostname()
	return &Services{
		DB: db, Crypter: crypter, Recorder: recorder,
		MaskingSource:         databaseMaskingSource{},
		instance:              fmt.Sprintf("%s/%d", host, os.Getpid()),
		cursorIdle:            10 * time.Minute,
		cursorEvictMinIdle:    defaultCursorEvictMinIdle,
		cursorEvictMinIdleSet: true,
		metaCacheTTL:          defaultMetadataCacheTTL,
		nowFunc:               func() time.Time { return time.Now().UTC() },
	}, nil
}

// SetCursorIdleForTest injects the cursor idle timeout for testing; called only in test builds.
func (s *Services) SetCursorIdleForTest(d time.Duration) { s.cursorIdle = d }

// SetCursorEvictMinIdleForTest injects the minimum idle duration before LRU cursor eviction for testing.
func (s *Services) SetCursorEvictMinIdleForTest(d time.Duration) {
	s.cursorEvictMinIdle = d
	s.cursorEvictMinIdleSet = true
}

func (s *Services) effectiveCursorEvictMinIdle() time.Duration {
	if !s.cursorEvictMinIdleSet && s.cursorEvictMinIdle == 0 {
		return defaultCursorEvictMinIdle
	}
	return s.cursorEvictMinIdle
}

// SetMetadataCacheTTLForTest overrides the metadata cache TTL for testing; 0 disables caching.
func (s *Services) SetMetadataCacheTTLForTest(d time.Duration) {
	s.metaCacheMu.Lock()
	s.metaCacheTTL = d
	if s.metaCache != nil {
		s.metaCache.clear()
	}
	s.metaCacheMu.Unlock()
}

// SetClockForTest injects the clock function for testing; called only in test builds.
func (s *Services) SetClockForTest(fn func() time.Time) { s.nowFunc = fn }

func newID() string { return uuid.NewString() }

func (s *Services) now() time.Time {
	if s.nowFunc == nil {
		return time.Now().UTC()
	}
	return s.nowFunc().UTC()
}

func (s *Services) serializable(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return txutil.Serializable(ctx, s.DB, fn)
}

func isSerializationFailure(err error) bool { return txutil.IsSerializationFailure(err) }

func isUniqueViolation(err error) bool { return txutil.IsUniqueViolation(err) }

// ---- Domain events (frozen surface in repository-event-routing) ----

const (
	EventAccessApproved = "query_access.approved"
	EventAccessRejected = "query_access.rejected"
	EventGrantActivated = "query_grant.activated"
	EventGrantRevoked   = "query_grant.revoked"
	EventSessionOpened  = "query_session.opened"
	EventSessionClosed  = "query_session.closed"
	EventSessionRevReq  = "query_session.revocation_requested"
	EventExecCompleted  = "query.execution_completed"
	EventExecFailed     = "query.execution_failed"
	EventExecUnknown    = "query.execution_result_unknown"
)

func auditActor(a Actor) auditlog.Actor {
	return auditlog.Actor{UserID: a.UserID, Username: a.Username}
}

// emitQueryEvent writes a query domain event (with subject = aggregate/{uuid}).
func (s *Services) emitQueryEvent(ctx context.Context, tx *gorm.DB, internalType, subject string,
	actor Actor, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	m := map[string]any{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	return s.Recorder.Emit(ctx, tx, internalType, subject, auditActor(actor), m)
}

// ---- Idempotency (matching execution/changeorder contract surface) ----

func (s *Services) idemStore() *txutil.IdempotencyStore {
	return &txutil.IdempotencyStore{
		DB:      s.DB,
		Crypter: s.Crypter,
		Errs: txutil.IdempotencyErrors{
			Reused:     func() error { return domain.ErrIdemReused() },
			InProgress: func() error { return domain.ErrIdemInProgress() },
			Concurrent: func() error { return domain.ErrConcurrent() },
		},
	}
}

func (s *Services) idemClaim(ctx context.Context, userID, route, aggregateID, key, reqHash string) (claimed bool, replay []byte, err error) {
	return s.idemStore().Claim(ctx, userID, route, aggregateID, key, reqHash)
}

func (s *Services) idemRelease(ctx context.Context, userID, route, aggregateID, key string) error {
	return s.idemStore().Release(ctx, userID, route, aggregateID, key)
}

func (s *Services) idemComplete(ctx context.Context, userID, route, aggregateID, key string, response []byte) error {
	return s.idemStore().Complete(ctx, userID, route, aggregateID, key, response)
}

func idemRequestHash(parts ...string) string { return txutil.RequestHash(parts...) }

// ---- Cryptographic helpers (AAD contract surface) ----

// sealPayload encrypts a query domain payload using AAD bound to table name + primary key.
func (s *Services) sealPayload(table, primaryKey, column string, plain []byte) (crypto.Envelope, error) {
	return s.Crypter.Seal(crypto.PurposePayload, plain, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: table,
		PrimaryKey: primaryKey, Column: column, Purpose: crypto.PurposePayload,
	})
}

// openPayload decrypts a query domain payload.
func (s *Services) openPayload(table, primaryKey, column string, env crypto.Envelope) ([]byte, error) {
	return s.Crypter.Open(crypto.PurposePayload, env, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: table,
		PrimaryKey: primaryKey, Column: column, Purpose: crypto.PurposePayload,
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// AffinityHMAC computes the cursor affinity cookie HMAC signature (using the cursor_affinity purpose;
// frozen value in cryptographic-key-contract hmac.purposes).
func (s *Services) AffinityHMAC(data []byte) ([]byte, error) {
	return s.Crypter.HMAC("cursor_affinity", data)
}

// hmacSQL computes the plaintext SQL HMAC (using the sql_fingerprint purpose, matching execution and review).
func (s *Services) hmacSQL(plain []byte) (string, error) {
	mac, err := s.Crypter.HMAC("sql_fingerprint", plain)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac), nil
}

// ---- Query settings (frozen surface in settings-namespaces query namespace) ----

type querySettings struct {
	ApprovalEnabled                bool
	DefaultTimeoutMS               int
	MaximumApprovedDurationMinutes int
	MaxResultRows                  int
}

// loadQuerySettings loads query namespace settings (returning defaults when missing or failing safe to defaults on corruption).
func (s *Services) loadQuerySettings(ctx context.Context, db *gorm.DB) querySettings {
	q := querySettings{
		ApprovalEnabled:                true,
		DefaultTimeoutMS:               30000,
		MaximumApprovedDurationMinutes: 43200,
		MaxResultRows:                  1000,
	}
	var raw string
	if err := db.WithContext(ctx).Raw(
		`select settings::text from system_settings where namespace = 'query'`).Scan(&raw).Error; err != nil || raw == "" {
		return q
	}
	var parsed struct {
		ApprovalEnabled                *bool `json:"approval_enabled"`
		DefaultTimeoutMS               *int  `json:"default_timeout_ms"`
		MaximumApprovedDurationMinutes *int  `json:"maximum_approved_access_duration_minutes"`
		MaxResultRows                  *int  `json:"max_result_rows"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return q
	}
	if parsed.ApprovalEnabled != nil {
		q.ApprovalEnabled = *parsed.ApprovalEnabled
	}
	if parsed.DefaultTimeoutMS != nil && *parsed.DefaultTimeoutMS >= 1 && *parsed.DefaultTimeoutMS <= 300000 {
		q.DefaultTimeoutMS = *parsed.DefaultTimeoutMS
	}
	if parsed.MaximumApprovedDurationMinutes != nil && *parsed.MaximumApprovedDurationMinutes >= 1 {
		q.MaximumApprovedDurationMinutes = *parsed.MaximumApprovedDurationMinutes
	}
	if parsed.MaxResultRows != nil && *parsed.MaxResultRows >= 1 && *parsed.MaxResultRows <= 100000 {
		q.MaxResultRows = *parsed.MaxResultRows
	}
	return q
}

// lockUpdate returns a FOR UPDATE locking clause.
func lockUpdate() clause.Locking { return clause.Locking{Strength: "UPDATE"} }

// auditInput constructs a standard audit event input.
func auditInput(a Actor, eventType, resourceType, resourceID, action, outcome string) auditlog.AuditInput {
	rid := resourceID
	uname := a.Username
	return auditlog.AuditInput{
		EventType:     eventType,
		ActorKind:     "user",
		ActorID:       &a.UserID,
		ActorUsername: &uname,
		ResourceType:  resourceType,
		ResourceID:    &rid,
		Action:        action,
		Outcome:       outcome,
	}
}
