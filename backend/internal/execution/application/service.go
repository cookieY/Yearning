// service.go Execution domain application service wiring and shared helpers.
// begin/record_result/resolve_unknown/cancel follow repository-commands declarations:
// serializable isolation, bounded retry on 40001/40P01, optimistic locking CAS, full
// RowsAffected checks, and atomic persistence of events + Outbox + idempotency responses
// in the same transaction (ADR-0003 authoritative send boundary).
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

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/osc"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

func forUpdate() clause.Locking { return clause.Locking{Strength: "UPDATE"} }

// Actor represents the command initiator (session identity snapshot).
type Actor struct {
	UserID   string
	Username string
}

// OscRunner abstracts gh-ost process execution (implemented by osc.Runner; tests inject a fake).
type OscRunner interface {
	// Run executes a gh-ost process until exit or cancellation, returning terminal observed facts.
	Run(ctx context.Context, job *osc.Job, hooks osc.Hooks) (*osc.Outcome, error)
	// Cancel creates a panic flag and cancels the running process (addressed by attempt directory).
	Cancel(dir string)
}

// Services aggregates dependencies for the Execution domain.
type Services struct {
	DB       *gorm.DB
	Crypter  *crypto.Crypter
	Recorder *auditlog.Recorder
	// When Osc is nil, OSC Attempts are rejected during preflight with GH_OST_UNSUPPORTED
	// (environments without a wired process Adapter never downgrade to direct DDL).
	Osc OscRunner
	// OscBinaries pins binary coordinates by tool (wired from release manifest; tests inject fakes).
	OscBinaries map[string]*OscBinaryIdentity
	// PoolFactory is used by tests to inject fault proxies; nil uses default target resolution.
	PoolFactory TargetPoolFactory
	// SearchPathProbe is a test seam after opening an execution-purpose
	// connection; nil performs the fixed real PostgreSQL environment probe.
	SearchPathProbe SearchPathProbe
	// OscResolver overrides DNS resolution during OSC topology preflight in tests; nil uses addrguard.DefaultResolver.
	OscResolver addrguard.Resolver
	// OscReplicaStatusProbe overrides SHOW REPLICA STATUS probing in tests; nil queries the guarded target pool.
	OscReplicaStatusProbe func(ctx context.Context, target *stageTarget) (*database.MySQLReplicaStatus, error)
	// cancelDelayOverride injects the cancel watchdog delay for tests (0 = production 30s).
	cancelDelayOverride time.Duration
	// instanceOwner identifies this process's execution worker (for lease ownership).
	instance string
}

// NewServices constructs the application services.
func NewServices(db *gorm.DB, crypter *crypto.Crypter, recorder *auditlog.Recorder) (*Services, error) {
	if db == nil || crypter == nil || recorder == nil {
		return nil, errors.New("execution: nil service dependency")
	}
	host, _ := os.Hostname()
	svc := &Services{
		DB: db, Crypter: crypter, Recorder: recorder,
		Osc: osc.NewRunner(),
		OscBinaries: map[string]*OscBinaryIdentity{
			"gh-ost": {
				Version: osc.BuiltinGhOstVersion,
				SHA256:  osc.BuiltinInProcessGhOstSHA256,
				Path:    osc.BuiltinInProcessGhOstPath,
			},
		},
		instance: fmt.Sprintf("%s/%d", host, os.Getpid()),
	}
	return svc, nil
}

// instanceOwner returns the lease owner identifier for this process.
func (s *Services) instanceOwner() string { return s.instance }

// SetCancelWatchdogDelayForTest injects the cancel watchdog delay for tests (0 = restore
// production 30s); called only in test builds.
func (s *Services) SetCancelWatchdogDelayForTest(d time.Duration) { s.cancelDelayOverride = d }

func newID() string     { return uuid.NewString() }
func nowUTC() time.Time { return time.Now().UTC() }

// LeaseDuration lease duration for an execution Attempt (renewed by heartbeat; adjudicated by sweep upon expiry).
const LeaseDuration = 60 * time.Second

func (s *Services) serializable(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return txutil.Serializable(ctx, s.DB, fn)
}

func isSerializationFailure(err error) bool { return txutil.IsSerializationFailure(err) }

func isUniqueViolation(err error) bool { return txutil.IsUniqueViolation(err) }

// ---- Domain Events (repository-event-routing: execution.* -> execution.state_changed) ----

const (
	EventExecStarted   = "execution.started"
	EventExecSucceeded = "execution.succeeded"
	EventExecFailed    = "execution.failed"
	EventExecPartial   = "execution.partial_failed"
	EventExecUnknown   = "execution.result_unknown"
	EventUnknownResolv = "execution.unknown_resolved"
	// S007/RCP-20261031-NOTIFICATION-RULES-AND-CHANNELS: emit events in the same transaction at
	// cancellation completion and schedule anomaly adjudication points (the cancellation request
	// itself still emits zero events and zero timeline entries; events are produced only at
	// adjudication points where the work order state actually transitions).
	EventExecCancelled        = "execution.cancelled"
	EventExecPartialCancelled = "execution.partial_cancelled"
	// change_order.step_activated is emitted by this package when activating the first step
	// across stages (same vocabulary as changeorder; recipients = frozen approvers of the newly activated step).
	EventStepActivated        = "change_order.step_activated"
	EventBlockedDSUnavailable = "execution.blocked_datasource_unavailable"
	EventScheduleMissed       = "execution.missed_schedule"
)

// StepActivatedPayload maps to domain-event.schema stepActivated def
// (additionalProperties:false; extra fields will be rejected by consumers).
type StepActivatedPayload struct {
	AggregateID      string `json:"aggregate_id"`
	StageID          string `json:"stage_id"`
	StepID           string `json:"step_id"`
	AggregateVersion int64  `json:"aggregate_version"`
}

// StateChangedPayload maps to stateChanged def (aggregate = change_orders work order).
type StateChangedPayload struct {
	AggregateID      string  `json:"aggregate_id"`
	From             *string `json:"from"`
	To               string  `json:"to"`
	ReasonCode       string  `json:"reason_code"`
	StageID          *string `json:"stage_id,omitempty"`
	AggregateVersion int64   `json:"aggregate_version"`
}

// VerificationRecordedPayload maps to executionVerification def
// (second payload when unknown_resolved dual-emits via emit_all).
type VerificationRecordedPayload struct {
	OrderID          string `json:"order_id"`
	AttemptID        string `json:"attempt_id"`
	VerifiedByUserID string `json:"verified_by_user_id"`
	Result           string `json:"result"`
	EvidenceCount    int    `json:"evidence_count"`
	AggregateVersion int64  `json:"aggregate_version"`
}

// emitOrderEvent writes an execution domain event (subject=change-orders/{uuid};
// external type is mapped by the outbox consumer according to the routing table).
func (s *Services) emitOrderEvent(ctx context.Context, tx *gorm.DB, internalType, orderID string,
	actor auditlog.Actor, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	m := map[string]any{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	return s.Recorder.Emit(ctx, tx, internalType, "change-orders/"+orderID, actor, m)
}

func auditActor(a Actor) auditlog.Actor {
	return auditlog.Actor{UserID: a.UserID, Username: a.Username}
}

// ---- Idempotency (same contract surface as changeorder) ----

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

// ---- Encryption Helpers (same AAD contract surface as review/datasource) ----

// sqlHMACPurpose shares the HMAC manifest purpose with review/application.seal.go.
const sqlHMACPurpose = "sql_fingerprint"

// openOrderSQL decrypts the work order's frozen SQL (AAD = order_sql_payloads row).
func (s *Services) openOrderSQL(orderID string, row *orderSQLRow) ([]byte, error) {
	return s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.SQLCiphertext, Nonce: row.SQLNonce,
		KeyID: row.SQLKeyID, Algorithm: row.SQLAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "order_sql_payloads",
		PrimaryKey: orderID, Column: "sql_ciphertext", Purpose: crypto.PurposePayload,
	})
}

// hmacSQL computes the plaintext SQL HMAC (used for four-way consistency checks across submission/approval/execution).
func (s *Services) hmacSQL(plain []byte) (string, error) {
	mac, err := s.Crypter.HMAC(sqlHMACPurpose, plain)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac), nil
}

// sealErrorDetail encrypts statement error details (execution_statements.error_* envelope).
func (s *Services) sealErrorDetail(attemptID string, ordinal int32, detail []byte) (ct, nonce []byte, keyID string, err error) {
	env, err := s.Crypter.Seal(crypto.PurposePayload, detail, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "execution_statements",
		PrimaryKey: fmt.Sprintf("%s#%d", attemptID, ordinal),
		Column:     "error_detail", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return nil, nil, "", err
	}
	return env.Ciphertext, env.Nonce, env.KeyID, nil
}

// openErrorDetail decrypts statement error details (read surface).
func (s *Services) openErrorDetail(attemptID string, ordinal int32, ct, nonce []byte, keyID string) ([]byte, error) {
	return s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: ct, Nonce: nonce, KeyID: keyID, Algorithm: crypto.EnvelopeAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "execution_statements",
		PrimaryKey: fmt.Sprintf("%s#%d", attemptID, ordinal),
		Column:     "error_detail", Purpose: crypto.PurposePayload,
	})
}

// sha256Hex computes an unkeyed SHA-256 hex digest.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---- Target Datasource Resolution (execution-purpose credentials & TLS via canonical datasource helpers) ----

// TargetPoolFactory opens an execution Purpose pool (used by tests to inject TCP proxies / faults).
type TargetPoolFactory func(ctx context.Context, ds *datasource.DatasourceModel) (*database.PurposePool, error)

// defaultPoolFactory opens a real target pool from the frozen datasource row using the strict execution credential and TLS material.
func (s *Services) defaultPoolFactory(ctx context.Context, ds *datasource.DatasourceModel) (*database.PurposePool, error) {
	cred, err := datasource.ResolveCredential(ctx, s.DB, s.Crypter, ds.ID, datasource.CredentialResolveOptions{
		Purposes: []string{"execution"},
	})
	if err != nil {
		return nil, domain.ErrDatasourceDown()
	}
	mat, err := datasource.ResolveTLSMaterial(ctx, s.DB, s.Crypter, ds.ID, "execution")
	if err != nil {
		return nil, domain.ErrDatasourceDown()
	}
	ep := database.Endpoint{
		Host: ds.Host, Port: ds.Port,
		DatabaseName: derefStr(ds.DatabaseName), CompatibilityMod: ds.CompatibilityMode,
	}
	var adapter database.Adapter
	if ds.CompatibilityMode == "mysql" {
		adapter = database.NewMySQLAdapter(ep, database.AdapterOptions{})
	} else {
		adapter = database.NewPostgreSQLAdapter(ep, database.AdapterOptions{})
	}
	return adapter.OpenPurposePool(ctx, database.PurposeExecution,
		database.Credential{Username: cred.Username, Password: cred.Password}, mat)
}

// openPool opens a target pool, preferring the injected factory if present.
func (s *Services) openPool(ctx context.Context, ds *datasource.DatasourceModel) (*database.PurposePool, error) {
	if s.PoolFactory != nil {
		return s.PoolFactory(ctx, ds)
	}
	return s.defaultPoolFactory(ctx, ds)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func splitTLSPEM(verifyServer bool, serverName string, plain []byte) (*database.TLSMaterial, error) {
	return datasource.SplitTLSMaterial(verifyServer, serverName, plain)
}

// openExecPassword decrypts the execution credential password (called only at the instant of
// connection construction and gh-ost spawn; the password never enters logs/events/plans/argv/env).
func (s *Services) openExecPassword(ctx context.Context, db *gorm.DB, dsID string) (string, error) {
	cred, err := datasource.ResolveCredential(ctx, db, s.Crypter, dsID, datasource.CredentialResolveOptions{
		Purposes: []string{"execution"},
	})
	if err != nil {
		return "", domain.ErrDatasourceDown()
	}
	return cred.Password, nil
}

// ---- Common Row Loaders ----

// orderSQLRow is a projection of order_sql_payloads.
type orderSQLRow struct {
	SQLCiphertext  []byte `gorm:"column:sql_ciphertext"`
	SQLNonce       []byte `gorm:"column:sql_nonce"`
	SQLKeyID       string `gorm:"column:sql_key_id"`
	SQLAlgorithm   string `gorm:"column:sql_algorithm"`
	PlaintextHMAC  string `gorm:"column:plaintext_hmac"`
	StatementCount int32  `gorm:"column:statement_count"`
}

func (s *Services) loadOrderSQL(ctx context.Context, db *gorm.DB, orderID string) (*orderSQLRow, error) {
	var row orderSQLRow
	if err := db.WithContext(ctx).Raw(`
		select sql_ciphertext, sql_nonce, sql_key_id, sql_algorithm, plaintext_hmac, statement_count
		from order_sql_payloads where order_id = ?`, orderID).Scan(&row).Error; err != nil {
		return nil, err
	}
	if row.PlaintextHMAC == "" {
		return nil, domain.ErrNotFound()
	}
	return &row, nil
}

// loadAttemptForUpdate locks and returns the Attempt row.
func (s *Services) loadAttemptForUpdate(ctx context.Context, tx *gorm.DB, attemptID string) (*persistence.ExecutionAttemptModel, error) {
	var m persistence.ExecutionAttemptModel
	if err := tx.WithContext(ctx).Clauses(forUpdate()).
		Where("id = ?", attemptID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	return &m, nil
}

// updateAttemptVersioned updates the Attempt with optimistic locking (WHERE id AND version).
func (s *Services) updateAttemptVersioned(ctx context.Context, tx *gorm.DB, m *persistence.ExecutionAttemptModel,
	expectVersion int64, sets map[string]any) error {
	res := tx.WithContext(ctx).Model(&persistence.ExecutionAttemptModel{}).
		Where("id = ? and version = ?", m.ID, expectVersion).Updates(sets)
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
