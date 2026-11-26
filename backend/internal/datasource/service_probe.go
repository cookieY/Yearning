// service_probe.go holds service domain helpers and operations.
package datasource

import (
	"context"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// Create creates a datasource (initial creation does not require reachability; connectivity is verified by the test task).
func (s *Service) Create(ctx context.Context, actor ActorRef, in *WriteInput, meta auditMeta) (*View, error) {
	if err := validateWrite(in); err != nil {
		return nil, err
	}
	if err := s.validateTargetEndpoint(ctx, in.Host, in.Port, ptr.Deref(in.DatabaseName)); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := DatasourceModel{
		ID: uuid.NewString(), Name: in.Name, Engine: in.Engine,
		CompatibilityMode: in.CompatibilityMode, DeploymentKind: in.DeploymentKind,
		Host: in.Host, Port: in.Port, DatabaseName: in.DatabaseName,
		VersionConstraint: resolveCreateVersionConstraint(in), Enabled: in.Enabled,
		TestPurpose: testPurposeOrDefault(in),
		Metadata:    []byte("{}"), Version: 1,
		CreatedByUserID: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			if txutil.IsUniqueViolation(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
		if err := s.replaceCredentials(ctx, tx, row.ID, in.Credentials); err != nil {
			return err
		}
		// Create tri-state: omitted is equivalent to null (no existing material to keep) -> nil;
		// object -> seal across all purposes (validateWrite already enforced ca_pem).
		var tlsObj *TLSInput
		if in.TLS.Set && !in.TLS.Null {
			tlsObj = in.TLS.Value
		}
		return s.replaceTLSMaterials(ctx, tx, row.ID, in.Credentials, tlsObj)
	})
	if err != nil {
		return nil, err
	}
	s.auditChange(ctx, actor, "datasource.created", "create_datasource", row.ID, "succeeded", meta, map[string]any{"engine": row.Engine})
	return s.getView(ctx, &row)
}

func resolveCreateVersionConstraint(in *WriteInput) string {
	vc := strings.TrimSpace(in.VersionConstraint)
	if vc == "" {
		return DefaultVersionConstraint(in.Engine, in.CompatibilityMode)
	}
	return vc
}

func resolveReplaceVersionConstraint(in *WriteInput, existing string) string {
	vc := strings.TrimSpace(in.VersionConstraint)
	if vc != "" {
		return vc
	}
	if prev := strings.TrimSpace(existing); prev != "" {
		return prev
	}
	return DefaultVersionConstraint(in.Engine, in.CompatibilityMode)
}

func assertNoRunningTasks(ctx context.Context, tx *gorm.DB, datasourceID string) error {
	var count int64
	err := tx.WithContext(ctx).Table("execution_attempts").
		Joins("join order_stages on order_stages.id = execution_attempts.stage_id").
		Where("order_stages.datasource_id = ? and execution_attempts.state in ?", datasourceID, runningTaskStates).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return errDatasourceBusy{Count: count}
	}
	return nil
}

func (e errDatasourceBusy) Error() string {
	return fmt.Sprintf("datasource: %d running tasks block disable", e.Count)
}

// engineChangeConflictsFlow reports whether changing the datasource to (engine, compatibilityMode)
// would create a mixed-engine flow in any multi-stage change_review flow that references it.
//
// The rule matches workflow.validateChangeFlowSingleEngine: all stages of a change_review flow must
// share the same engine and compatibility_mode. This function inspects only stages other than the
// datasource being modified — they must match the new values, otherwise the update creates a mixed-engine flow.
//
// Single-stage flows are unaffected (no cross-engine issue), so only flows with >= 2 stage positions are checked.
func engineChangeConflictsFlow(ctx context.Context, tx *gorm.DB, datasourceID, engine, compatibilityMode string) (bool, error) {
	var count int64
	err := tx.WithContext(ctx).Raw(`
		select count(*) from flow_stages fs
		join flows f on f.id = fs.flow_id
		join datasources d on d.id = fs.datasource_id
		where f.flow_type = 'change_review'
		  and fs.flow_id in (
		      select flow_id from flow_stages where datasource_id = ?
		  )
		  and fs.datasource_id <> ?
		  and (d.engine <> ? or d.compatibility_mode <> ?)
		  and (select count(*) from flow_stages x where x.flow_id = fs.flow_id) >= 2`,
		datasourceID, datasourceID, engine, compatibilityMode).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Delete performs reference-protected deletion: datasources ever referenced by flow templates, snapshots,
// or query capabilities can only be disabled (returning 1107); physical deletion is restricted to zero references.
func (s *Service) Delete(ctx context.Context, actor ActorRef, id string, expectVersion int64, meta auditMeta) error {
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
			return bizerr.ErrConcurrentModification
		}
		refs, err := countReferences(ctx, tx, id)
		if err != nil {
			return err
		}
		if refs > 0 {
			return bizerr.ErrReferenced
		}
		// Credentials, TLS materials, and SQL diagnostics data cascade on delete (logical-relations: delete_source).
		if err := tx.Where("datasource_id = ?", id).Delete(&DatasourceCredentialModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("datasource_id = ?", id).Delete(&DatasourceTLSMaterialModel{}).Error; err != nil {
			return err
		}
		for _, diagStmt := range []string{
			"delete from sql_diagnostics_order_links where datasource_id = ?",
			"delete from sql_diagnostics_ai_reports where datasource_id = ?",
			"delete from sql_diagnostics_bucket_metrics where datasource_id = ?",
			"delete from sql_diagnostics_statements where datasource_id = ?",
			"delete from sql_diagnostics_snapshots where datasource_id = ?",
			"delete from diagnostics_instance_leases where datasource_id = ?",
			"delete from datasource_diagnostics_configs where datasource_id = ?",
		} {
			if err := tx.Exec(diagStmt, id).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id = ?", id).Delete(&DatasourceModel{}).Error; err != nil {
			return err
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "datasource.deleted", ActorKind: "user",
			ActorID: &actor.UserID, ActorUsername: &actor.Username,
			ResourceType: "datasource", ResourceID: &id,
			Action: "delete_datasource", Outcome: "succeeded",
			RequestID: ptr.NonZero(meta.RequestID),
		})
	})
	if err == nil {
		s.lastCaps.Delete(id)
	}
	return err
}

func countReferences(ctx context.Context, tx *gorm.DB, datasourceID string) (int64, error) {
	var total int64
	for _, table := range referenceTables {
		var count int64
		if err := tx.WithContext(ctx).Table(table).
			Where("datasource_id = ?", datasourceID).Count(&count).Error; err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// Get and List return API views.
func (s *Service) Get(ctx context.Context, id string) (*View, error) {
	var row DatasourceModel
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	return s.getView(ctx, &row)
}

// StoredTestPurpose returns the persisted connection test purpose of a datasource (§18.61; NOT NULL
// defaulting to review). The capabilities endpoint selects probe credentials using this value (§18.73).
func (s *Service) StoredTestPurpose(ctx context.Context, id string) (string, error) {
	var purpose string
	err := s.db.WithContext(ctx).Model(&DatasourceModel{}).
		Where("id = ?", id).Select("test_purpose").Scan(&purpose).Error
	if err != nil {
		return "", err
	}
	if purpose == "" {
		return "review", nil
	}
	return purpose, nil
}

// List returns a paginated list of views (search = case-insensitive substring on name/host, §18.73 server-side q;
// filters apply per-column narrowing).
func (s *Service) List(ctx context.Context, limit int, after, search string, filters ListFilters) ([]View, *string, error) {
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch filters.Engine {
	case "", "mysql", "postgresql", "tidb", "oceanbase", "polardb":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch filters.CompatibilityMode {
	case "", "mysql", "postgresql":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch filters.CredentialMode {
	case "", "read_write", "read_only":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&DatasourceModel{}).
		Order("created_at asc, id asc").Limit(limit + 1)
	if after != "" {
		var cursor DatasourceModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(created_at, id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	if filters.Engine != "" {
		q = q.Where("engine = ?", filters.Engine)
	}
	if filters.CompatibilityMode != "" {
		q = q.Where("compatibility_mode = ?", filters.CompatibilityMode)
	}
	switch filters.CredentialMode {
	case "read_write":
		q = q.Where(credentialExecutionExists)
	case "read_only":
		q = q.Where("not " + credentialExecutionExists)
	}
	if filters.Referenced != nil {
		if *filters.Referenced {
			q = q.Where(flowStageReferenceExists)
		} else {
			q = q.Where("not " + flowStageReferenceExists)
		}
	}
	if search != "" {
		needle := "%" + search + "%"
		q = q.Where("name ILIKE ? or host ILIKE ?", needle, needle)
	}
	var rows []DatasourceModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views := make([]View, 0, len(rows))
	for i := range rows {
		v, err := s.getView(ctx, &rows[i])
		if err != nil {
			return nil, nil, err
		}
		views = append(views, *v)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

func (s *Service) getView(ctx context.Context, row *DatasourceModel) (*View, error) {
	view := View{
		ID: row.ID, Name: row.Name, Engine: row.Engine,
		CompatibilityMode: row.CompatibilityMode, DeploymentKind: row.DeploymentKind,
		Host: row.Host, Port: row.Port, DatabaseName: row.DatabaseName,
		Enabled: row.Enabled, TestPurpose: row.TestPurpose, CredentialStatus: map[string]bool{},
		Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	var creds []DatasourceCredentialModel
	if err := s.db.WithContext(ctx).
		Where("datasource_id = ? and enabled = true", row.ID).Find(&creds).Error; err != nil {
		return nil, err
	}
	for _, c := range creds {
		view.CredentialStatus[c.Purpose] = true
	}
	// tls_verified expresses only material presence (present => verifying TLS enforced) without exposing material.
	var tlsCount int64
	if err := s.db.WithContext(ctx).Model(&DatasourceTLSMaterialModel{}).
		Where("datasource_id = ?", row.ID).Count(&tlsCount).Error; err != nil {
		return nil, err
	}
	view.TLSVerified = tlsCount > 0
	var refCount int64
	if err := s.db.WithContext(ctx).Table("flow_stages").
		Where("datasource_id = ?", row.ID).Count(&refCount).Error; err != nil {
		return nil, err
	}
	view.ReferencedByFlowCount = refCount
	return &view, nil
}

// Capabilities returns the capability matrix for a datasource. When a matching
// TestAndCapabilities probe succeeded within the rate-limit window, it serves
// that fresh snapshot directly so the immediate post-test GET /capabilities
// read never collides with the 5-second connection-test rate limit or creates
// a duplicate probe task.
func (s *Service) Capabilities(ctx context.Context, id, purpose string) (*CapabilitiesView, error) {
	if purpose != "review" && purpose != "query" && purpose != "execution" {
		return nil, bizerr.ErrValidationFailed
	}
	if raw, ok := s.lastCaps.Load(id); ok {
		if cached, ok := raw.(cachedCapabilities); ok && cached.purpose == purpose && time.Since(cached.cachedAt) < testInterval {
			v := cloneCapabilitiesView(&cached.view)
			return &v, nil
		}
	}
	caps, _, err := s.TestAndCapabilities(ctx, id, purpose)
	return caps, err
}

func (ErrConnectionFailed) Error() string { return "datasource: connection failed" }

func (e ErrEndpointForbidden) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "datasource: target address forbidden"
}

func (e ErrEndpointForbidden) Unwrap() []error {
	return []error{bizerr.ErrValidationFailed, e.Err}
}

func (s *Service) auditChange(ctx context.Context, actor ActorRef, eventType, action, resourceID, outcome string, meta auditMeta, extra map[string]any) {
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: eventType, ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "datasource", ResourceID: &resourceID, Action: action, Outcome: outcome,
		RequestID: ptr.NonZero(meta.RequestID), Metadata: extra,
	})
}

func newCredentialID() string { return uuid.NewString() }

func intPtr(i int) *int { return &i }
