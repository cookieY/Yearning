// service.go datasource application service: CRUD, per-Purpose credential replacement, connection test tasks,
// capability resolution, identity fingerprinting, and reference-protected deletion (02-datasource-management-prd).
package datasource

import (
	"context"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/tasks"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sync"
	"time"
)

// View is the API response view; credential_status expresses only Presence and never contains secrets.
type View struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Engine                string          `json:"engine"`
	CompatibilityMode     string          `json:"compatibility_mode"`
	DeploymentKind        string          `json:"deployment_kind"`
	Host                  string          `json:"host"`
	Port                  int             `json:"port"`
	DatabaseName          *string         `json:"database_name"`
	Enabled               bool            `json:"enabled"`
	TestPurpose           string          `json:"test_purpose"`
	CredentialStatus      map[string]bool `json:"credential_status"`
	TLSVerified           bool            `json:"tls_verified"`
	ReferencedByFlowCount int64           `json:"referenced_by_flow_count"`
	Version               int64           `json:"version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

// CredentialInput maps to OpenAPI CredentialWrite (three explicit edit modes):
// password = replace; reuse_credential_purpose pointing to another purpose = reuse;
// equal to its own purpose = keep (copies the existing full credential, valid only when that row already exists).
type CredentialInput struct {
	Purpose  string `json:"purpose"`
	Username string `json:"username"`
	Password *struct {
		Value string `json:"value"`
	} `json:"password"`
	ReuseCredentialPurpose *string `json:"reuse_credential_purpose"`
}

// TLSInput maps to the object form of OpenAPI DatasourceTLSWrite (write-only SecretInput).
type TLSInput struct {
	CAPem *struct {
		Value string `json:"value"`
	} `json:"ca_pem"`
	ClientCertPEM *struct {
		Value string `json:"value"`
	} `json:"client_cert_pem"`
	ClientKeyPEM *struct {
		Value string `json:"value"`
	} `json:"client_key_pem"`
}

// TLSWriteField distinguishes the three states of the tls payload (RCP-20261016-DATASOURCE-TLS-KEEP):
// omitted key = keep existing material and tls_verified (Set=false); explicit null = remove all
// material and revert to plaintext (Set=true, Null=true); object = full replacement (Value != nil,
// replacement object must carry a non-null ca_pem). On create, omitted is equivalent to null (no existing material to keep).
type TLSWriteField struct {
	Set   bool
	Null  bool
	Value *TLSInput
}

// WriteInput maps to OpenAPI DatasourceWrite.
type WriteInput struct {
	Name              string  `json:"name"`
	Engine            string  `json:"engine"`
	CompatibilityMode string  `json:"compatibility_mode"`
	DeploymentKind    string  `json:"deployment_kind"`
	Host              string  `json:"host"`
	Port              int     `json:"port"`
	DatabaseName      *string `json:"database_name"`
	VersionConstraint string  `json:"version_constraint"`
	Enabled           bool    `json:"enabled"`
	// TestPurpose is the connection test purpose (§18.61). nil = default, falling back to
	// review under full-replacement semantics (DatasourceWrite declares default review; omitting resets it).
	TestPurpose *string           `json:"test_purpose"`
	Credentials []CredentialInput `json:"credentials"`
	TLS         TLSWriteField     `json:"tls"`
}

// ActorRef describes the actor initiating a management operation.
type ActorRef struct {
	UserID   string
	Username string
}

// Service is the datasource application service.
type Service struct {
	db       *gorm.DB
	factory  *Factory
	crypter  *crypto.Crypter
	audit    *auditlog.Recorder
	probeMu  sync.Map // datasource_id -> last probe time (connection test rate limiting)
	lastCaps sync.Map // datasource_id -> cachedCapabilities from the latest successful probe
}

type cachedCapabilities struct {
	purpose  string
	view     CapabilitiesView
	cachedAt time.Time
}

// validateWrite validates a write payload (lengths, enums, unique credential Purposes, mutually exclusive edit modes, and TLS material shape).
func validateWrite(in *WriteInput) error {
	if in.Name == "" || len(in.Name) > 128 || in.Host == "" || len(in.Host) > 253 {
		return bizerr.ErrValidationFailed
	}
	if in.Port < 1 || in.Port > 65535 || len(in.VersionConstraint) > 64 {
		return bizerr.ErrValidationFailed
	}
	if in.DeploymentKind != "native" && in.DeploymentKind != "cloud" {
		return bizerr.ErrValidationFailed
	}
	if !engineModeOK(in.Engine, in.CompatibilityMode) {
		return bizerr.ErrValidationFailed
	}
	if in.TestPurpose != nil && *in.TestPurpose != "review" &&
		*in.TestPurpose != "query" && *in.TestPurpose != "execution" {
		return bizerr.ErrValidationFailed
	}
	if len(in.Credentials) < 1 || len(in.Credentials) > 3 {
		return bizerr.ErrValidationFailed
	}
	seen := map[string]bool{}
	for _, c := range in.Credentials {
		if c.Purpose != "review" && c.Purpose != "query" && c.Purpose != "execution" {
			return bizerr.ErrValidationFailed
		}
		if seen[c.Purpose] {
			return bizerr.ErrValidationFailed
		}
		seen[c.Purpose] = true
		hasPassword := c.Password != nil && c.Password.Value != ""
		hasReuse := c.ReuseCredentialPurpose != nil && *c.ReuseCredentialPurpose != ""
		if hasPassword && hasReuse {
			// Providing both password and reuse_credential_purpose is an ambiguous mode error.
			return bizerr.ErrValidationFailed
		}
		if !hasPassword && !hasReuse {
			// Caller must explicitly choose one of replace, reuse, or keep.
			return bizerr.ErrValidationFailed
		}
		if hasReuse && *c.ReuseCredentialPurpose != "review" &&
			*c.ReuseCredentialPurpose != "query" && *c.ReuseCredentialPurpose != "execution" {
			return bizerr.ErrValidationFailed
		}
		if hasPassword && c.Username == "" {
			// replace: new SecretInput must be accompanied by a username.
			return bizerr.ErrValidationFailed
		}
		if hasReuse && *c.ReuseCredentialPurpose != c.Purpose && c.Username == "" {
			// cross-purpose reuse: the new purpose row carries its own username.
			return bizerr.ErrValidationFailed
		}
		if hasReuse && *c.ReuseCredentialPurpose == c.Purpose && c.Username != "" {
			// keep: copies the existing complete credential (including username), so the request must not supply a username.
			return bizerr.ErrValidationFailed
		}
	}
	return validateTLS(in.TLS.Value)
}

// Replace performs a full replacement of a datasource definition (optimistic lock).
// Before disabling (enabled=false), verifies that no running execution tasks exist for this datasource.
// SEC-0026: changing endpoint-family fields (engine/compatibility_mode/host/port) clears
// identity_fingerprint and probe metadata — fingerprint comparisons for frozen approvals and Query Sessions
// will fail closed (identity_changed), forcing a fresh connection test to rebuild the identity fingerprint
// so approved orders and sessions never silently follow a new target.
func (s *Service) Replace(ctx context.Context, actor ActorRef, id string, expectVersion int64, in *WriteInput, meta auditMeta) (*View, error) {
	if err := validateWrite(in); err != nil {
		return nil, err
	}
	if err := s.validateTargetEndpoint(ctx, in.Host, in.Port, ptr.Deref(in.DatabaseName)); err != nil {
		return nil, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row DatasourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrVersionConflict
		}
		if row.Enabled && !in.Enabled {
			if err := assertNoRunningTasks(ctx, tx, id); err != nil {
				return err
			}
		}
		// Reference consistency for engine-family changes (BUG-005 / review F3): multi-stage change_review
		// flows must bind datasources of the exact same engine and compatibility mode, otherwise runtime
		// preflight recomputes dialect fingerprints under each stage's engine, mismatching the single dialect
		// frozen at review time and permanently wedging stage 2 with 3006. Create/ReplaceFlow already fails
		// closed at flow configuration time, but mutating a datasource's engine afterward would silently turn
		// an existing single-engine flow into a mixed-engine flow; block it here: if any change_review flow
		// referencing this datasource has another stage that disagrees with the new engine/compatibility_mode,
		// reject the change (1001) and require adjusting or retiring the affected flow first.
		if row.Engine != in.Engine || row.CompatibilityMode != in.CompatibilityMode {
			conflict, err := engineChangeConflictsFlow(ctx, tx, id, in.Engine, in.CompatibilityMode)
			if err != nil {
				return err
			}
			if conflict {
				return bizerr.ErrValidationFailed
			}
		}
		updates := map[string]any{
			"name": in.Name, "engine": in.Engine,
			"compatibility_mode": in.CompatibilityMode, "deployment_kind": in.DeploymentKind,
			"host": in.Host, "port": in.Port, "database_name": in.DatabaseName,
			"version_constraint": resolveReplaceVersionConstraint(in, row.VersionConstraint), "enabled": in.Enabled,
			"test_purpose": testPurposeOrDefault(in),
			"version":      gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
		}
		// Endpoint-family change -> clear frozen identity (SEC-0026: fingerprint material covers
		// engine/host/port, and compatibility_mode determines the wire protocol family so it is treated
		// as an endpoint change as well). Once cleared, TestConnection is the sole path to rebuild the fingerprint.
		if row.Engine != in.Engine || row.CompatibilityMode != in.CompatibilityMode ||
			row.Host != in.Host || row.Port != in.Port {
			updates["identity_fingerprint"] = nil
			updates["metadata"] = []byte("{}")
		}
		if err := tx.Model(&DatasourceModel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			if txutil.IsUniqueViolation(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
		if err := s.replaceCredentials(ctx, tx, id, in.Credentials); err != nil {
			return err
		}
		// Replace tri-state dispatch (RCP-20261016): omitted = keep existing material and
		// tls_verified (content unchanged, re-sealed across the new purpose set, R1 High-1);
		// null = explicit removal; object = full replacement.
		switch {
		case !in.TLS.Set:
			return s.keepTLSMaterials(ctx, tx, id, in.Credentials)
		case in.TLS.Null:
			return s.replaceTLSMaterials(ctx, tx, id, in.Credentials, nil)
		default:
			return s.replaceTLSMaterials(ctx, tx, id, in.Credentials, in.TLS.Value)
		}
	})
	if err != nil {
		return nil, err
	}
	s.lastCaps.Delete(id)
	s.auditChange(ctx, actor, "datasource.updated", "replace_datasource", id, "succeeded", meta, nil)
	return s.Get(ctx, id)
}

// runningTaskStates matches execution_attempts active states.
var runningTaskStates = []string{"created", "preflight", "running", "cancelling", "result_unknown"}

type errDatasourceBusy struct{ Count int64 }

// referenceTables lists the business tables holding a datasource_id reference.
var referenceTables = []string{
	"flow_stages", "flow_query_capabilities", "stage_review_snapshots",
	"order_stages", "query_access_request_datasources", "query_grant_capabilities",
	"query_session_capabilities", "query_executions",
}

// ListFilters holds per-column filters for the admin datasource list (RCP-20261105-ADMIN-LIST-FIELD-FILTERS).
// Zero values mean the filter was omitted; all active filters narrow results via AND and compose with q.
type ListFilters struct {
	Engine            string // mysql|postgresql|tidb|oceanbase|polardb
	CompatibilityMode string // mysql|postgresql
	CredentialMode    string // read_write = has enabled execution credential; read_only = none
	Referenced        *bool  // true = referenced by >= 1 flow stage; false = unreferenced
}

// credentialExecutionExists returns the EXISTS subquery fragment for read_write filtering:
// checks only existence of an enabled execution credential without ever reading secret material.
const credentialExecutionExists = `exists (select 1 from datasource_credentials dc
 where dc.datasource_id = datasources.id and dc.enabled = true and dc.purpose = 'execution')`

// flowStageReferenceExists returns the EXISTS subquery fragment for referenced filtering.
const flowStageReferenceExists = `exists (select 1 from flow_stages fs where fs.datasource_id = datasources.id)`

// CapabilitiesView maps to OpenAPI DatasourceCapabilities.
type CapabilitiesView struct {
	DatasourceID        string          `json:"datasource_id"`
	DetectedVersion     string          `json:"detected_version"`
	IdentityFingerprint string          `json:"identity_fingerprint"`
	Capabilities        map[string]bool `json:"capabilities"`
	CheckedAt           time.Time       `json:"checked_at"`
}

// TestAndCapabilities executes a connection probe, resolves the capability matrix, and persists the identity fingerprint.
// Probe failures return a business error (3010) without updating the fingerprint.
func (s *Service) TestAndCapabilities(ctx context.Context, id, purpose string) (*CapabilitiesView, *AsyncTaskModel, error) {
	if purpose != "review" && purpose != "query" && purpose != "execution" {
		return nil, nil, bizerr.ErrValidationFailed
	}
	if last, ok := s.probeMu.Load(id); ok {
		if t, ok := last.(time.Time); ok && time.Since(t) < testInterval {
			return nil, nil, ErrTestRateLimited
		}
	}
	if err := tasks.CheckPendingCapacity(ctx, s.db); err != nil {
		return nil, nil, err
	}
	s.probeMu.Store(id, time.Now())

	task := AsyncTaskModel{
		ID: uuid.NewString(), Kind: "admin_connection_test", State: "queued",
		AggregateType: ptr.NonZero("datasource"), AggregateID: ptr.NonZero(id),
		Input:         []byte(`{"purpose":"` + purpose + `"}`),
		ProgressTotal: 1, ProgressUnit: "step", FenceToken: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&task).Error; err != nil {
		return nil, nil, err
	}
	finished := false
	finish := func(state string, code *int, failure *string, resultRef *string) {
		finished = true
		now := time.Now().UTC()
		task.State = state
		task.BusinessErrCode = code
		task.FailureName = failure
		task.ResultRef = resultRef
		task.FinishedAt = &now
		task.UpdatedAt = now
		task.ProgressCompleted = 1
		_ = s.db.WithContext(context.WithoutCancel(ctx)).Model(&AsyncTaskModel{}).
			Where("id = ?", task.ID).
			Updates(map[string]any{
				"state": state, "business_err_code": code, "failure_name": failure,
				"result_ref": resultRef, "finished_at": now, "updated_at": now,
				"progress_completed": 1,
			}).Error
	}
	defer func() {
		if !finished {
			finish("failed", intPtr(3010), ptr.NonZero("internal_error"), nil)
		}
	}()

	var ds DatasourceModel
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&ds).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			finish("failed", intPtr(1002), ptr.NonZero("resource_not_found"), nil)
			return nil, &task, bizerr.ErrNotFound
		}
		return nil, &task, err
	}
	var cred DatasourceCredentialModel
	probePurposes := []string{purpose}
	if purpose == "review" {
		probePurposes = append(probePurposes, "query")
	} else if purpose == "query" {
		probePurposes = append(probePurposes, "review")
	}
	resolvedPurpose := ""
	var credErr error
	for _, candidate := range probePurposes {
		credErr = s.db.WithContext(ctx).
			Where("datasource_id = ? and purpose = ? and enabled = true", id, candidate).
			First(&cred).Error
		if credErr == nil {
			resolvedPurpose = candidate
			break
		}
		if !errors.Is(credErr, gorm.ErrRecordNotFound) {
			break
		}
	}
	if resolvedPurpose == "" {
		finish("failed", intPtr(3010), ptr.NonZero("credential_missing"), nil)
		// §18.76 supplement (owner ruling: datasource connection test surfaces diagnostic detail along the same path as Provider rejection reasons):
		// failure sentinel carries operator-readable Detail, which errmap attaches to meta.detail for inline display.
		return nil, &task, fmt.Errorf("%w: purpose %s", ErrCredentialMissing, purpose)
	}
	var tlsMat DatasourceTLSMaterialModel
	tlsPurposes := []string{resolvedPurpose}
	if resolvedPurpose != purpose {
		tlsPurposes = append(tlsPurposes, purpose)
	}
	for _, tlsPurpose := range tlsPurposes {
		if err := s.db.WithContext(ctx).
			Where("datasource_id = ? and purpose = ?", id, tlsPurpose).
			First(&tlsMat).Error; err == nil {
			break
		}
	}

	probe, probeErr := s.factory.Probe(ctx, &ds, &cred, &tlsMat)
	if probeErr != nil {
		finish("failed", intPtr(3010), ptr.NonZero("connection_failed"), nil)
		return nil, &task, fmt.Errorf("%w: %v", ErrConnectionFailed{}, probeErr)
	}
	fingerprint, err := s.factory.IdentityFingerprint(&ds, probe.InstanceMarker)
	if err != nil {
		finish("failed", intPtr(3010), ptr.NonZero("fingerprint_failed"), nil)
		return nil, &task, err
	}
	target, caps, flavor, tier, notice, err := ResolveCapabilitiesWithProbe(
		ds.Engine, ds.CompatibilityMode, probe.ServerVersion, probe.RawVersionBanner,
	)
	if err != nil {
		finish("failed", intPtr(3010), ptr.NonZero("capability_unresolved"), nil)
		return nil, &task, err
	}
	// Persist fingerprint and detected version/flavor (remains stable when database identity is unchanged).
	metadataBytes, resultRefBytes, err := buildProbePayloads(
		target, probe.DurationMS, probe.ServerVersion, flavor, tier, notice,
	)
	if err != nil {
		return nil, &task, err
	}
	if err := s.db.WithContext(ctx).Model(&DatasourceModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"identity_fingerprint": fingerprint,
			"metadata":             metadataBytes,
			"updated_at":           time.Now().UTC(),
		}).Error; err != nil {
		return nil, &task, err
	}
	resultRef := string(resultRefBytes)
	finish("succeeded", nil, nil, ptr.NonZero(resultRef))
	view := CapabilitiesView{
		DatasourceID: id, DetectedVersion: probe.ServerVersion,
		IdentityFingerprint: fingerprint, Capabilities: caps,
		CheckedAt: time.Now().UTC(),
	}
	s.lastCaps.Store(id, cachedCapabilities{
		purpose:  purpose,
		view:     cloneCapabilitiesView(&view),
		cachedAt: time.Now(),
	})
	return &view, &task, nil
}

// TestInterval is the minimum interval between connection tests (429 rate-limit window).
const testInterval = 5 * time.Second

// ErrTestRateLimited indicates connection testing is rate-limited.
var ErrTestRateLimited = errors.New("datasource: connection test rate limited")

// ErrCapacityExhausted indicates the durable async_tasks queue has reached its pending limit.
var ErrCapacityExhausted = tasks.ErrQueueFull

// ErrConnectionFailed indicates a connection probe failure (mapped to 3010 DATASOURCE_UNAVAILABLE).
type ErrConnectionFailed struct{}

// ErrEndpointForbidden indicates a datasource endpoint failed create/replace address guard checks
// (mapped to declared 1001 VALIDATION_FAILED with safe diagnostic detail and host field metadata).
type ErrEndpointForbidden struct {
	Err error
}

// auditMeta carries request context metadata (isomorphic to identity.RequestMeta to avoid circular imports).
type auditMeta struct {
	RequestID string
}
