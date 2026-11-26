// service.go Review-domain application service wiring and shared types.
// Transaction boundaries are declared by each Use Case; repository write paths follow repository-commands:
// version CAS, RowsAffected checks on every write, and events + Outbox in the same transaction.
package application

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
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

// Services aggregates all Use Case dependencies for the Review domain.
type Services struct {
	DB       *gorm.DB
	Crypter  *crypto.Crypter
	Recorder *auditlog.Recorder
	Engine   *fingerprint.Engine
	// Ephemeral is the in-memory store for ephemeral precheck Runs (RCP-20261101-EPHEMERAL-PRECHECK:
	// zero DB persistence during precheck; materialized upon submission). TTL is replaceable in tests (SetEphemeralTTL).
	Ephemeral *EphemeralStore
	// DatasourceIdentityProbe is a test seam for application integration
	// fixtures whose datasource rows are intentionally unreachable. Production
	// leaves it nil and uses the read-only database Adapter probe.
	DatasourceIdentityProbe func(context.Context, *gorm.DB, *crypto.Crypter, datasourceTargetRow) (database.ServerIdentity, error)
	// PostgreSQL empty-mapping stages probe the execution role's frozen path
	// and resolve every relation identity across the complete effective path.
	// Production leaves both nil; local fixtures may replace only these two
	// trusted database-boundary seams.
	PostgresPathProbe        PostgresPathProbe
	PostgresRelationResolver PostgresRelationResolver
	// Batch resolution is the production shape: one bounded catalog snapshot
	// for every unique exact relation name across the complete effective path.
	PostgresRelationBatchResolver PostgresRelationBatchResolver
	// Callable identity is resolved by the catalog on the frozen effective
	// path; the seam exists for fixtures without a reachable target.
	PostgresCallableResolver PostgresCallableResolver
}

// NewServices constructs the application services; when engine is nil, it builds one using the Crypter signer.
func NewServices(db *gorm.DB, crypter *crypto.Crypter, recorder *auditlog.Recorder, engine *fingerprint.Engine) (*Services, error) {
	if db == nil || crypter == nil || recorder == nil {
		return nil, errors.New("review: nil service dependency")
	}
	if engine == nil {
		e, err := fingerprint.NewEngine(fingerprint.CryptoSigner{C: crypter}, fingerprint.Options{})
		if err != nil {
			return nil, err
		}
		engine = e
	}
	return &Services{DB: db, Crypter: crypter, Recorder: recorder, Engine: engine,
		Ephemeral: NewEphemeralStore(30 * time.Minute)}, nil
}

// SetEphemeralStore replaces the ephemeral Run store (for injecting short TTLs / isolated stores in tests).
func (s *Services) SetEphemeralStore(st *EphemeralStore) { s.Ephemeral = st }

// newID generates a UUID v4 string.
func newID() string { return uuid.NewString() }

// ---- Common persistence helpers (repository implementation) ----

// ErrNotFound uniformly maps to 1002; ErrVersionConflict maps to 1003;
// ErrConcurrent maps to 1004; ErrReferenced maps to 1006 (GORM contract optimistic locking
// and reverse-reference semantics).
var (
	ErrNotFound        = errors.New("review: resource not found")
	ErrVersionConflict = errors.New("review: version conflict")
	ErrConcurrent      = errors.New("review: concurrent modification")
	ErrReferenced      = errors.New("review: resource referenced")
)

// loadDraftForUpdate locks and returns a draft row.
func (s *Services) loadDraftForUpdate(ctx context.Context, tx *gorm.DB, draftID string) (*persistence.ChangeDraftModel, error) {
	var m persistence.ChangeDraftModel
	if err := tx.WithContext(ctx).
		Clauses(txutil.ForUpdate()).
		Where("id = ?", draftID).
		First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// loadDraft reads a draft row (without locking).
func (s *Services) loadDraft(ctx context.Context, db *gorm.DB, draftID string) (*persistence.ChangeDraftModel, error) {
	var m persistence.ChangeDraftModel
	if err := db.WithContext(ctx).Where("id = ?", draftID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// updateDraftVersioned performs an optimistic-lock update: WHERE id AND version;
// 0 rows affected distinguishes 1003 vs 1004 via stale check.
func (s *Services) updateDraftVersioned(ctx context.Context, tx *gorm.DB, m *persistence.ChangeDraftModel,
	expectVersion int64, sets map[string]any) error {
	res := tx.WithContext(ctx).Model(&persistence.ChangeDraftModel{}).
		Where("id = ? and version = ?", m.ID, expectVersion).
		Updates(sets)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		if expectVersion != m.Version {
			return ErrVersionConflict
		}
		return ErrConcurrent
	}
	return nil
}

// updateRunVersioned performs an optimistic-lock update on a Run.
func (s *Services) updateRunVersioned(ctx context.Context, tx *gorm.DB, runID string, expectVersion int64, sets map[string]any) error {
	res := tx.WithContext(ctx).Model(&persistence.ReviewRunModel{}).
		Where("id = ? and version = ?", runID, expectVersion).
		Updates(sets)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConcurrent
	}
	return nil
}

// flowSnapshot is the structural summary of a flow frozen at run creation time (used for hashing).
type flowSnapshot struct {
	FlowID      string             `json:"flow_id"`
	FlowVersion int64              `json:"flow_version"`
	Stages      []flowStageSummary `json:"stages"`
}

// flowStageSummary is the summary of a flow stage frozen at run creation time (participating in the flow hash).
// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: flow stages no longer configure logical-to-physical Schema
// mappings, so the hash composition removes the schema_mapping_ciphertext component (synchronized across
// submission and review; legacy Run hashes failing re-verification is the expected fail-closed behavior).
type flowStageSummary struct {
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

// flowRow reads the required columns from flows.
type flowRow struct {
	ID        string
	FlowType  string
	Status    string
	RuleSetID *string
	Version   int64
}

// stageRow reads the required columns from flow_stages + datasources + actors. Stage configuration no
// longer carries Schema mappings (RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS); object scope is determined
// by the unmapped branch (PG = frozen search_path, MySQL = datasource default database).
type stageRow struct {
	ID                string
	Position          int
	DatasourceID      string
	Engine            string
	CompatibilityMode string
	Host              string
	Port              int
	DatabaseName      *string
	IdentityFP        *string
	Enabled           bool
}

// loadFlowStages returns ordered stages and datasource facts.
func (s *Services) loadFlowStages(ctx context.Context, db *gorm.DB, flowID string) ([]stageRow, error) {
	var rows []stageRow
	err := db.WithContext(ctx).Raw(`
		select fs.id::text as id, fs.position, fs.datasource_id::text as datasource_id, d.engine,
		       d.compatibility_mode, d.host, d.port, d.database_name,
		       d.identity_fingerprint as identity_fp, d.enabled
		from flow_stages fs
		join datasources d on d.id = fs.datasource_id
		where fs.flow_id = ?
		order by fs.position`, flowID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// loadApprovalActors returns the union of approvers for each stage_position
// (approval steps of a change flow belong to a stage; sorted by step position and user ID for determinism).
func (s *Services) loadApprovalActors(ctx context.Context, db *gorm.DB, flowID string) (map[int][]string, error) {
	var rows []struct {
		StagePosition int
		UserID        string
	}
	err := db.WithContext(ctx).Raw(`
		select fs.position as stage_position, a.user_id::text as user_id
		from flow_approval_steps st
		join flow_stages fs on fs.id = st.stage_id
		join flow_approval_step_actors a on a.step_id = st.id
		where st.flow_id = ? and st.stage_id is not null
		order by fs.position, st.position, a.user_id`, flowID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[int][]string{}
	for _, r := range rows {
		out[r.StagePosition] = append(out[r.StagePosition], r.UserID)
	}
	return out, nil
}

// loadExecutionActors returns the executors for each stage.
func (s *Services) loadExecutionActors(ctx context.Context, db *gorm.DB, flowID string) (map[string][]string, error) {
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

// flowGranted verifies the user's submission authorization for a flow
// (permission group membership -> flow grant -> flow enabled and of type change_review).
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

// draftProfile returns the dialect profile used to parse draft SQL:
// engine/compatibility_mode of the flow's first-stage datasource.
func (s *Services) draftProfile(ctx context.Context, db *gorm.DB, draftID string) (fingerprint.Profile, error) {
	var name string
	err := db.WithContext(ctx).Raw(`
		select d.engine || '/' || d.compatibility_mode as name
		from change_drafts cd
		join flow_stages fs on fs.flow_id = cd.flow_id
		join datasources d on d.id = fs.datasource_id
		where cd.id = ?
		order by fs.position limit 1`, draftID).Scan(&name).Error
	if err != nil {
		return fingerprint.Profile{}, err
	}
	if name == "" {
		return fingerprint.Profile{}, ErrNotFound
	}
	return fingerprint.ParseProfile(name)
}

// fingerprintFailureName extracts the B5 Failure error name; non-Failure errors default to fingerprint failure.
func fingerprintFailureName(err error) string {
	var f *fingerprint.Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return "fingerprint_failed"
}

// nowUTC returns the current time in UTC.
func nowUTC() time.Time { return time.Now().UTC() }

// errEnumUnknown indicates an unknown persisted state enum (PERSISTENCE_ENUM_UNKNOWN,
// HTTP 500 transport error; mapping to a default state is forbidden).
var errEnumUnknown = errors.New("review: persisted enum unknown")

// failureCodeOf maps a domain error to a business code; unknown errors default to fingerprint failure (fail closed).
func failureCodeOf(err error) int32 {
	var de *domain.Error
	if errors.As(err, &de) {
		return int32(de.Code)
	}
	return int32(domain.CodeSQLFingerprintFailed)
}
