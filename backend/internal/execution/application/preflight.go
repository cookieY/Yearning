// preflight.go Pre-execution Preflight (PRD 04 §4): four-way SQL hash consistency,
// datasource identity consistency, execution window validity, statement plan freezing, and Kind
// derivation (including gh-ost rules).
// Plaintext SQL exists only in memory within this package and never enters logs or events.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/osc"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// PlanStatement is a single statement in the frozen statement plan (SQL lives only in memory; only fact columns are persisted to the ledger).
type PlanStatement struct {
	Ordinal       int32
	Kind          string // ddl|dml
	Operation     string
	SQL           []byte
	CanonicalHMAC string
	DialectHMAC   string
}

// planResult holds the outcome of a plan freeze.
type planResult struct {
	Statements []PlanStatement
	Kind       domain.ExecutionKind
	SQLHMAC    string
}

// freezeStatementPlan decrypts work order SQL -> verifies HMAC consistency -> splits statements
// by dialect and fingerprints each -> verifies ordinal-by-ordinal against sql_statements of the
// accepted review Run -> freezes the plan and Kind.
func (s *Services) freezeStatementPlan(ctx context.Context, db *gorm.DB,
	orderID, stageID string, ds *datasource.DatasourceModel) (*planResult, error) {
	row, err := s.loadOrderSQL(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	plain, err := s.openOrderSQL(orderID, row)
	if err != nil {
		return nil, err
	}
	mac, err := s.hmacSQL(plain)
	if err != nil {
		return nil, err
	}
	if mac != row.PlaintextHMAC {
		return nil, domain.ErrPreflightFailed("sql_hmac_mismatch")
	}
	// Frozen statement facts from the accepted review Run (bound via stage_review_snapshot_id).
	var run struct {
		ReviewRunID string `gorm:"column:review_run_id"`
	}
	if err := db.WithContext(ctx).Raw(`
		select s.review_run_id from order_stages st
		join stage_review_snapshots s on s.id = st.stage_review_snapshot_id
		where st.id = ?`, stageID).Scan(&run).Error; err != nil {
		return nil, err
	}
	if run.ReviewRunID == "" {
		return nil, domain.ErrPreflightFailed("accepted_review_missing")
	}
	var frozen []struct {
		Ordinal                  int32  `gorm:"column:ordinal"`
		StatementKind            string `gorm:"column:statement_kind"`
		CanonicalFingerprintHMAC string `gorm:"column:canonical_fingerprint_hmac"`
		DialectFingerprintHMAC   string `gorm:"column:dialect_fingerprint_hmac"`
	}
	if err := db.WithContext(ctx).Raw(`
		select ordinal, statement_kind, canonical_fingerprint_hmac, dialect_fingerprint_hmac
		from sql_statements where review_run_id = ? order by ordinal`, run.ReviewRunID).
		Scan(&frozen).Error; err != nil {
		return nil, err
	}
	if int32(len(frozen)) != row.StatementCount {
		return nil, domain.ErrPreflightFailed("statement_count_mismatch")
	}
	// Split statements + fingerprint each (same engine and profile as review; RunMapper is unused —
	// fingerprint recomputation only needs to match frozen rows and does not require stable identifier mapping).
	profile := fingerprint.Profile{Engine: ds.Engine, Mode: ds.CompatibilityMode}
	if err := profile.Validate(); err != nil {
		return nil, domain.ErrPreflightFailed("dialect_profile_invalid")
	}
	engine, err := fingerprint.NewEngine(fingerprint.CryptoSigner{C: s.Crypter}, fingerprint.Options{})
	if err != nil {
		return nil, err
	}
	split := changeset.NewSplitter(strings.NewReader(string(plain)), profile, 0)
	plan := &planResult{SQLHMAC: mac}
	for {
		st, done, serr := split.Next()
		if serr != nil {
			return nil, domain.ErrPreflightFailed("sql_split_failed")
		}
		if done {
			break
		}
		res, aerr := engine.Analyze(profile, st.SQL, nil)
		if aerr != nil {
			return nil, domain.ErrPreflightFailed("sql_fingerprint_failed")
		}
		ordinal := int32(len(plan.Statements) + 1)
		if int(ordinal) > len(frozen) {
			return nil, domain.ErrPreflightFailed("statement_count_mismatch")
		}
		f := frozen[ordinal-1]
		// The statement_kind frozen during review is a ddl/dml classification (review-side
		// statementClassOf criterion); the engine outputs an operation name, which is bucketed here using the same criterion.
		class := statementClassOf(res.StatementKind)
		if f.Ordinal != ordinal || f.StatementKind != class ||
			f.CanonicalFingerprintHMAC != res.CanonicalFingerprintHMAC ||
			f.DialectFingerprintHMAC != res.DialectFingerprintHMAC {
			// SQL was modified after review (Review/Submission/Execution hashes do not match).
			return nil, domain.ErrPreflightFailed("sql_changed_after_review")
		}
		sqlCopy := append([]byte(nil), st.SQL...)
		plan.Statements = append(plan.Statements, PlanStatement{
			Ordinal: ordinal, Kind: class,
			Operation: strings.ToLower(string(firstWord(sqlCopy))),
			SQL:       sqlCopy, CanonicalHMAC: res.CanonicalFingerprintHMAC,
			DialectHMAC: res.DialectFingerprintHMAC,
		})
	}
	if int32(len(plan.Statements)) != row.StatementCount {
		return nil, domain.ErrPreflightFailed("statement_count_mismatch")
	}
	kind, err := deriveExecutionKind(plan.Statements, ds, s.loadOscPolicy(ctx, db))
	if err != nil {
		return nil, err
	}
	plan.Kind = kind
	return plan, nil
}

// statementClassOf uses the same criterion as review-side statementClassOf (operation name -> ddl/dml).
func statementClassOf(kind string) string {
	switch strings.ToLower(kind) {
	case "create", "alter", "drop", "truncate":
		return "ddl"
	default:
		return "dml"
	}
}

// oscPolicy represents the OSC policy in the execution settings namespace (RCP-20260827-OSC-PARAMS:
// admin-managed tool switch and default tuning; defaults to none = direct DDL when unconfigured).
type oscPolicy struct {
	Tool     osc.Tool
	Defaults *osc.Overrides
}

// loadOscPolicy reads the execution settings osc policy (missing/corrupted = fail-safe none for direct DDL).
func (s *Services) loadOscPolicy(ctx context.Context, db *gorm.DB) oscPolicy {
	var raw *string
	_ = db.WithContext(ctx).Raw(
		`select settings::text from system_settings where namespace = 'execution'`).Scan(&raw).Error
	policy := oscPolicy{Tool: "none", Defaults: &osc.Overrides{}}
	if raw == nil {
		return policy
	}
	var cfg struct {
		Osc *struct {
			Tool     string         `json:"tool"`
			Defaults map[string]any `json:"defaults"`
		} `json:"osc"`
	}
	if err := json.Unmarshal([]byte(*raw), &cfg); err != nil || cfg.Osc == nil {
		return policy
	}
	switch osc.Tool(cfg.Osc.Tool) {
	case osc.ToolGhOst, osc.ToolPtOsc:
		policy.Tool = osc.Tool(cfg.Osc.Tool)
	default:
		return oscPolicy{Tool: "none", Defaults: &osc.Overrides{}}
	}
	if defaults, err := osc.ParseOverrides(cfg.Osc.Defaults); err == nil {
		policy.Defaults = defaults
	}
	return policy
}

// deriveExecutionKind derives the Attempt execution Kind:
// all DML -> dml; all DDL -> ddl; policy enables OSC tool + single ALTER TABLE + native
// MySQL 5.7/8.0 -> that tool; mixed DDL/DML -> fail closed (single-transaction and non-transactional
// semantics cannot coexist in a single Attempt).
func deriveExecutionKind(stmts []PlanStatement, ds *datasource.DatasourceModel,
	policy oscPolicy) (domain.ExecutionKind, error) {
	if len(stmts) == 0 {
		return "", domain.ErrPreflightFailed("empty_statement_plan")
	}
	first := stmts[0].Kind
	for _, st := range stmts[1:] {
		if st.Kind != first {
			return "", domain.ErrPreflightFailed("mixed_statement_kinds")
		}
	}
	if first == "dml" {
		return domain.KindDML, nil
	}
	if len(stmts) == 1 && isAlterTable(stmts[0]) && oscCapable(ds) {
		switch policy.Tool {
		case osc.ToolGhOst:
			return domain.KindGhOst, nil
		case osc.ToolPtOsc:
			return domain.KindPtOsc, nil
		}
	}
	return domain.KindDDL, nil
}

// isAlterTable determines whether a statement is an ALTER TABLE (deterministic first + second token check).
func isAlterTable(st PlanStatement) bool {
	s := strings.TrimLeft(strings.TrimSpace(string(st.SQL)), "( \t\r\n")
	w := strings.Fields(strings.ToUpper(s))
	return len(w) >= 2 && w[0] == "ALTER" && w[1] == "TABLE"
}

// oscCapable reports whether the datasource is a gh-ost-serviceable target
// (gh-ost-execution-contract scope: native mysql-5.7/8.0 only).
func oscCapable(ds *datasource.DatasourceModel) bool {
	if ds.Engine != "mysql" || ds.CompatibilityMode != "mysql" || ds.DeploymentKind != "native" {
		return false
	}
	v := strings.TrimSpace(ds.VersionConstraint)
	return strings.HasPrefix(v, "5.7") || strings.HasPrefix(v, "8.0")
}

// firstWord extracts the leading word of a statement.
func firstWord(sql []byte) []byte {
	s := strings.TrimLeft(strings.TrimSpace(string(sql)), "( \t\r\n")
	if i := strings.IndexAny(s, " \t\r\n("); i > 0 {
		return []byte(s[:i])
	}
	return []byte(s)
}

// validateOscOverrides validates user-submitted override keys (unknown key / out-of-range value -> 1001).
func validateOscOverrides(raw map[string]any) (*osc.Overrides, error) {
	ov, err := osc.ParseOverrides(raw)
	if err != nil {
		return nil, domain.ErrValidation()
	}
	return ov, nil
}

// checkTarget verifies datasource availability and identity consistency
// (allowed only when Credential/TLS rotated with unchanged identity; instance/cluster identity change fails immediately).
func (s *Services) checkTarget(ctx context.Context, db *gorm.DB, ds *datasource.DatasourceModel,
	stageIdentity string) error {
	if !ds.Enabled {
		return domain.ErrDatasourceDown()
	}
	if ds.IdentityFingerprint == nil || *ds.IdentityFingerprint != stageIdentity {
		return domain.ErrPreflightFailed("datasource_identity_changed")
	}
	return nil
}

// loadDatasource reads the datasource row.
func (s *Services) loadDatasource(ctx context.Context, db *gorm.DB, id string) (*datasource.DatasourceModel, error) {
	var ds datasource.DatasourceModel
	if err := db.WithContext(ctx).Where("id = ?", id).First(&ds).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	return &ds, nil
}

// execWindow represents an execution window (OpenAPI ExecutionWindow).
type execWindow struct {
	DaysOfWeek     []string `json:"days_of_week"`
	StartLocalTime string   `json:"start_local_time"`
	EndLocalTime   string   `json:"end_local_time"`
}

// checkExecutionWindow verifies that the current time falls within an allowed execution window
// (always passes when restriction_enabled=false or unconfigured; timezone is read from general.system_timezone).
func (s *Services) checkExecutionWindow(ctx context.Context, db *gorm.DB) error {
	var raw *string
	if err := db.WithContext(ctx).Raw(
		`select settings::text from system_settings where namespace = 'execution'`).Scan(&raw).Error; err != nil {
		return err
	}
	if raw == nil || *raw == "" {
		return nil
	}
	var cfg struct {
		RestrictionEnabled bool         `json:"restriction_enabled"`
		Windows            []execWindow `json:"windows"`
	}
	if err := json.Unmarshal([]byte(*raw), &cfg); err != nil {
		// Treat corrupted configuration as disabled (execution windows are a rate limiter, not a security boundary).
		return nil
	}
	if !cfg.RestrictionEnabled {
		return nil
	}
	now := time.Now().In(s.systemTimezone(ctx, db))
	for _, w := range cfg.Windows {
		if windowContains(w, now) {
			return nil
		}
	}
	return domain.ErrOutsideWindow()
}

// checkExecutionWindowAt verifies that the specified timestamp falls within the global execution window
// (scheduled time must fall within a window; B11/E007).
func (s *Services) checkExecutionWindowAt(ctx context.Context, db *gorm.DB, at time.Time) error {
	var raw *string
	if err := db.WithContext(ctx).Raw(
		`select settings::text from system_settings where namespace = 'execution'`).Scan(&raw).Error; err != nil {
		return err
	}
	if raw == nil || *raw == "" {
		return nil
	}
	var cfg struct {
		RestrictionEnabled bool         `json:"restriction_enabled"`
		Windows            []execWindow `json:"windows"`
	}
	if err := json.Unmarshal([]byte(*raw), &cfg); err != nil {
		return nil
	}
	if !cfg.RestrictionEnabled {
		return nil
	}
	local := at.In(s.systemTimezone(ctx, db))
	for _, w := range cfg.Windows {
		if windowContains(w, local) {
			return nil
		}
	}
	return domain.ErrOutsideWindow()
}

// systemTimezone reads general.system_timezone (defaults to UTC).
func (s *Services) systemTimezone(ctx context.Context, db *gorm.DB) *time.Location {
	var tzName string
	_ = db.WithContext(ctx).Raw(
		`select settings->>'system_timezone' from system_settings where namespace = 'general'`).
		Scan(&tzName).Error
	if tzName == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(tzName); err == nil {
		return loc
	}
	return time.UTC
}

// windowContains reports whether now falls within the window (start > end is treated as a
// cross-midnight window: [start, 24:00) on the start day or [00:00, end] on the following day).
func windowContains(w execWindow, now time.Time) bool {
	start, ok1 := parseHHMM(w.StartLocalTime)
	end, ok2 := parseHHMM(w.EndLocalTime)
	if !ok1 || !ok2 {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	day := strings.ToLower(now.Weekday().String())
	if start <= end {
		return dayAllowed(w.DaysOfWeek, day) && cur >= start && cur <= end
	}
	if cur >= start {
		return dayAllowed(w.DaysOfWeek, day)
	}
	if cur <= end {
		prev := strings.ToLower(now.AddDate(0, 0, -1).Weekday().String())
		return dayAllowed(w.DaysOfWeek, prev)
	}
	return false
}

// dayAllowed reports whether day is in the window's days-of-week set.
func dayAllowed(days []string, day string) bool {
	for _, d := range days {
		if strings.EqualFold(strings.TrimSpace(d), day) {
			return true
		}
	}
	return false
}

// parseHHMM parses HH:MM into minutes since midnight.
func parseHHMM(s string) (int, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, false
	}
	h := int(parts[0][0]-'0')*10 + int(parts[0][1]-'0')
	m := int(parts[1][0]-'0')*10 + int(parts[1][1]-'0')
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// targetSnapshotRow is a projection of order_stages (columns read by the execution surface).
type targetSnapshotRow struct {
	StageID                  string `gorm:"column:stage_id"`
	OrderID                  string `gorm:"column:order_id"`
	Position                 int32  `gorm:"column:position"`
	DatasourceID             string `gorm:"column:datasource_id"`
	StageReviewSnapshotID    string `gorm:"column:stage_review_snapshot_id"`
	ReviewSnapshotHash       string `gorm:"column:review_snapshot_hash"`
	IdentityFingerprint      string `gorm:"column:datasource_identity_fingerprint"`
	State                    string `gorm:"column:state"`
	Version                  int64  `gorm:"column:version"`
	TargetSnapshotCiphertext []byte `gorm:"column:target_snapshot_ciphertext"`
	TargetSnapshotNonce      []byte `gorm:"column:target_snapshot_nonce"`
	TargetSnapshotKeyID      string `gorm:"column:target_snapshot_key_id"`
	TargetSnapshotAlgorithm  string `gorm:"column:target_snapshot_algorithm"`
}

// loadOrderStage reads the work order's currently active stage row (smallest position in a live
// state — completed prior stages of a multi-stage order no longer participate in execution checks).
// Falls back to the final stage row when no live stage exists (all terminal: failed/partial_failed/cancelled, etc.):
// E004 fact checking in begin (sent -> 3004) still requires stage addressing.
func (s *Services) loadOrderStage(ctx context.Context, db *gorm.DB, orderID string) (*targetSnapshotRow, error) {
	var row targetSnapshotRow
	err := db.WithContext(ctx).Raw(`
		select id::text as stage_id, order_id::text, position, datasource_id::text as datasource_id,
		       stage_review_snapshot_id::text as stage_review_snapshot_id,
		       review_snapshot_hash, datasource_identity_fingerprint, state, version,
		       target_snapshot_ciphertext, target_snapshot_nonce,
		       target_snapshot_key_id, target_snapshot_algorithm
		from order_stages
		where order_id = ? and state in
		      ('approval_active','execution_pending','scheduled','running','result_unknown')
		order by position limit 1`, orderID).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.StageID == "" {
		err = db.WithContext(ctx).Raw(`
			select id::text as stage_id, order_id::text, position, datasource_id::text as datasource_id,
			       stage_review_snapshot_id::text as stage_review_snapshot_id,
			       review_snapshot_hash, datasource_identity_fingerprint, state, version,
			       target_snapshot_ciphertext, target_snapshot_nonce,
			       target_snapshot_key_id, target_snapshot_algorithm
			from order_stages where order_id = ? order by position desc limit 1`, orderID).Scan(&row).Error
		if err != nil {
			return nil, err
		}
	}
	if row.StageID == "" {
		return nil, domain.ErrNotFound()
	}
	return &row, nil
}

// loadStageByID reads a stage row by stage ID (the execution Runner locates the stage carried
// by the task without relying on the live window — the stage row remains readable before terminal state persistence).
func (s *Services) loadStageByID(ctx context.Context, db *gorm.DB, stageID string) (*targetSnapshotRow, error) {
	var row targetSnapshotRow
	err := db.WithContext(ctx).Raw(`
		select id::text as stage_id, order_id::text, position, datasource_id::text as datasource_id,
		       stage_review_snapshot_id::text as stage_review_snapshot_id,
		       review_snapshot_hash, datasource_identity_fingerprint, state, version,
		       target_snapshot_ciphertext, target_snapshot_nonce,
		       target_snapshot_key_id, target_snapshot_algorithm
		from order_stages where id = ?`, stageID).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.StageID == "" {
		return nil, domain.ErrNotFound()
	}
	return &row, nil
}

// frozenTargetSnapshot is the decrypted JSON structure of the target snapshot
// (byte-identical to what changeorder/application.submit freezes).
// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: target snapshots no longer carry
// schema mapping ciphertexts; object scope is determined by the datasource default database
// or the PostgreSQL search_path context frozen during review.
type frozenTargetSnapshot struct {
	DatasourceID        string `json:"datasource_id"`
	Engine              string `json:"engine"`
	IdentityFingerprint string `json:"identity_fingerprint"`
}

type stageSchemaResolution struct {
	Schema       string
	SearchPath   []string
	PostgresPath *reviewapp.FrozenPostgresSearchPath
	Binding      *reviewapp.FrozenPostgresPathBinding
}

// stageSchemaResolutionForDatasource is pure once the frozen review path has
// been loaded. PostgreSQL replays only Explicit; Effective keeps implicit
// namespaces for lookup and must never be installed as schema context.
func stageSchemaResolutionForDatasource(
	ds *datasource.DatasourceModel,
	frozen *reviewapp.FrozenPostgresSearchPath,
) (stageSchemaResolution, error) {
	if ds == nil {
		return stageSchemaResolution{}, domain.ErrPreflightFailed("schema_unresolvable")
	}
	if ds.CompatibilityMode == "postgresql" {
		if frozen == nil {
			return stageSchemaResolution{},
				domain.ErrPreflightFailed("postgres_search_path_snapshot_missing")
		}
		if frozen.Validate() != nil || frozen.HasTemporarySchema() {
			return stageSchemaResolution{},
				domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
		}
		return stageSchemaResolution{
			Schema:       frozen.CreationSchema,
			SearchPath:   append([]string(nil), frozen.Explicit...),
			PostgresPath: frozen,
		}, nil
	}
	// MySQL / other engines: the datasource connection's default database is the sole allowed schema.
	if ds.DatabaseName == nil || *ds.DatabaseName == "" {
		return stageSchemaResolution{}, domain.ErrPreflightFailed("schema_unresolvable")
	}
	return stageSchemaResolution{
		Schema: *ds.DatabaseName, SearchPath: []string{*ds.DatabaseName},
	}, nil
}

// openTargetSnapshot decrypts the target snapshot.
func (s *Services) openTargetSnapshot(stage *targetSnapshotRow) (*frozenTargetSnapshot, error) {
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: stage.TargetSnapshotCiphertext, Nonce: stage.TargetSnapshotNonce,
		KeyID: stage.TargetSnapshotKeyID, Algorithm: stage.TargetSnapshotAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "order_stages",
		PrimaryKey: stage.StageID, Column: "target_snapshot_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return nil, domain.ErrPreflightFailed("target_snapshot_unreadable")
	}
	var snap frozenTargetSnapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		return nil, domain.ErrPreflightFailed("target_snapshot_unreadable")
	}
	return &snap, nil
}

// resolveStageSchema resolves the stage execution schema (RCP-20261031-REMOVE-FLOW-
// SCHEMA-MAPPINGS: stages no longer configure logical-to-physical schema mappings).
//   - PostgreSQL: execution environment comes from the search_path context frozen by the
//     accepted review (the immutable stage_review_snapshot envelope associated with order_stages);
//     the path must pass validation and match object bindings, otherwise fail closed.
//   - MySQL / other engines: the datasource connection's default database is the sole allowed
//     schema (datasource drift is pinned by target snapshot identity_fingerprint).
//
// The legacy implementation located the template flow_stages row via frozen FlowID+position and
// compared mapping ciphertexts byte-for-byte to prevent "silently using new template mappings after
// template replacement"; with mapping consumption removed, the execution side no longer reads
// any template stage rows, and that bypass naturally disappears.
func (s *Services) resolveStageSchema(ctx context.Context, db *gorm.DB, orderID string,
	stage *targetSnapshotRow, snap *frozenTargetSnapshot,
	ds *datasource.DatasourceModel) (stageSchemaResolution, error) {
	var frozen *reviewapp.FrozenPostgresSearchPath
	var binding *reviewapp.FrozenPostgresPathBinding
	if ds.CompatibilityMode == "postgresql" {
		environment, environmentErr := s.loadFrozenPostgresReviewContext(ctx, db, stage)
		if environmentErr != nil {
			return stageSchemaResolution{}, environmentErr
		}
		path := environment.Path
		if path.Validate() != nil || !path.Equal(environment.Binding.Path) {
			return stageSchemaResolution{},
				domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
		}
		frozen = &path
		binding = &environment.Binding
	}
	resolution, err := stageSchemaResolutionForDatasource(ds, frozen)
	if err != nil {
		return stageSchemaResolution{}, err
	}
	resolution.Binding = binding
	return resolution, nil
}
