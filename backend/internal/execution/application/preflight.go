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

	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/osc"
	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// PlanStatement is a single statement in the frozen statement plan (SQL lives only in memory; only fact columns are persisted to the ledger).
type PlanStatement struct {
	Ordinal             int32
	Kind                string // ddl|dml
	Operation           string
	SQL                 []byte
	CanonicalHMAC       string
	DialectHMAC         string
	ExplicitlyQualified bool
}

// planResult holds the outcome of a plan freeze.
type planResult struct {
	Statements []PlanStatement
	Kind       domain.ExecutionKind
	SQLHMAC    string
}

type frozenStatementRow struct {
	Ordinal                  int32  `gorm:"column:ordinal"`
	StatementKind            string `gorm:"column:statement_kind"`
	FingerprintQuality       string `gorm:"column:fingerprint_quality"`
	CanonicalFingerprintHMAC string `gorm:"column:canonical_fingerprint_hmac"`
	DialectFingerprintHMAC   string `gorm:"column:dialect_fingerprint_hmac"`
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
	var frozen []frozenStatementRow
	if err := db.WithContext(ctx).Raw(`
		select ordinal, statement_kind, fingerprint_quality, canonical_fingerprint_hmac, dialect_fingerprint_hmac
		from sql_statements where review_run_id = ? order by ordinal`, run.ReviewRunID).
		Scan(&frozen).Error; err != nil {
		return nil, err
	}
	if int32(len(frozen)) != row.StatementCount {
		return nil, domain.ErrPreflightFailed("statement_count_mismatch")
	}
	// Split statements + fingerprint each using the same engine and profile as review.
	// MySQL stages also inspect anonymized object references from this pass so execution
	// enforces the exact same explicit db.table qualification rules without double-parsing.
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
		var stmtMapper *privacy.RunMapper
		if ds.CompatibilityMode != "postgresql" {
			stmtMapper = privacy.NewRunMapper()
		}
		res, aerr := engine.Analyze(profile, st.SQL, stmtMapper)
		if aerr != nil {
			return nil, domain.ErrPreflightFailed("sql_fingerprint_failed")
		}
		ordinal := int32(len(plan.Statements) + 1)
		if int(ordinal) > len(frozen) {
			return nil, domain.ErrPreflightFailed("statement_count_mismatch")
		}
		f := frozen[ordinal-1]
		// Both review and execution preflight derive statement_kind (ddl|dml) from the leading
		// SQL verb (operation) using the shared fingerprint operation classifier.
		op := strings.ToLower(string(fingerprint.FirstOperationSlice(st.SQL)))
		class := fingerprint.StatementClassOf(op)
		if f.Ordinal != ordinal || f.StatementKind != class ||
			f.CanonicalFingerprintHMAC != res.CanonicalFingerprintHMAC ||
			f.DialectFingerprintHMAC != res.DialectFingerprintHMAC {
			// SQL was modified after review (Review/Submission/Execution hashes do not match),
			// or a frozen san-wire-v1 RENAME/CTAS order needs re-review under san-wire-v2.
			return nil, domain.ErrPreflightFailed(preflightStatementMismatchReason(f, ordinal, op, class, res))
		}
		explicitlyQualified := false
		if stmtMapper != nil {
			explicitlyQualified = reviewapp.InspectMySQLFingerprintObjects(
				res.Objects, res.MetadataContextComplete, stmtMapper,
			).AllExplicitlyQualified()
		}
		sqlCopy := append([]byte(nil), st.SQL...)
		plan.Statements = append(plan.Statements, PlanStatement{
			Ordinal: ordinal, Kind: class,
			Operation: op,
			SQL:       sqlCopy, CanonicalHMAC: res.CanonicalFingerprintHMAC,
			DialectHMAC:         res.DialectFingerprintHMAC,
			ExplicitlyQualified: explicitlyQualified,
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

func preflightStatementMismatchReason(f frozenStatementRow, ordinal int32, op, class string, res fingerprint.Result) string {
	if f.Ordinal == ordinal {
		if op == "rename" && class == "ddl" && (f.StatementKind == "dml" || f.FingerprintQuality == fingerprint.QualityLexer) {
			return "sql_changed_after_review: RENAME TABLE classification and fingerprint upgraded to DDL; please re-review this change order before execution"
		}
		if op == "create" && class == "ddl" && f.StatementKind == "ddl" && res.Validity.Unsupported == "create_table_as_select" {
			return "sql_changed_after_review: fingerprint upgraded for CREATE TABLE ... AS SELECT; please re-review"
		}
	}
	return "sql_changed_after_review"
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
// MySQL (>= 5.7) -> that tool; mixed DDL/DML -> fail closed (single-transaction and non-transactional
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

// oscCapable reports whether the datasource is an OSC-serviceable target
// (osc-execution-contract scope: native MySQL >= 5.7 only; non-native probe flavors fail closed).
func oscCapable(ds *datasource.DatasourceModel) bool {
	if ds == nil || ds.Engine != "mysql" || ds.CompatibilityMode != "mysql" || ds.DeploymentKind != "native" {
		return false
	}
	if len(ds.Metadata) > 0 {
		var meta struct {
			DetectedFlavor string `json:"detected_flavor"`
		}
		if err := json.Unmarshal(ds.Metadata, &meta); err == nil {
			flavor := strings.TrimSpace(strings.ToLower(meta.DetectedFlavor))
			if flavor != "" && flavor != "mysql_native" {
				return false
			}
		}
	}
	return true
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

// systemTimezone reads general.system_timezone via canonical platform/systz.
func (s *Services) systemTimezone(ctx context.Context, db *gorm.DB) *time.Location {
	return systz.Location(ctx, db)
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
type frozenTargetSnapshot = orderdomain.TargetSnapshot

type stageSchemaResolution struct {
	Schema       string
	SearchPath   []string
	PostgresPath *reviewapp.FrozenPostgresSearchPath
	Binding      *reviewapp.FrozenPostgresPathBinding
}

func resolveEffectiveMySQLExecutionDB(ds *datasource.DatasourceModel, targetDB string) string {
	effectiveDB := strings.TrimSpace(targetDB)
	if effectiveDB == "" && ds != nil && ds.DatabaseName != nil {
		effectiveDB = strings.TrimSpace(*ds.DatabaseName)
	}
	return effectiveDB
}

func allPlanStatementsExplicitlyQualified(stmts []PlanStatement) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, st := range stmts {
		if !st.ExplicitlyQualified {
			return false
		}
	}
	return true
}

// stageSchemaResolutionForDatasource is pure once the frozen review path and (for empty-default MySQL
// stages) statement qualification facts have been loaded. PostgreSQL replays only Explicit; Effective
// keeps implicit namespaces for lookup and must never be installed as schema context.
func stageSchemaResolutionForDatasource(
	ds *datasource.DatasourceModel,
	frozen *reviewapp.FrozenPostgresSearchPath,
	targetDB string,
	planStmts ...PlanStatement,
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
	effectiveDB := resolveEffectiveMySQLExecutionDB(ds, targetDB)
	if effectiveDB != "" {
		if database.ValidateIdentifier(effectiveDB) != nil {
			return stageSchemaResolution{}, domain.ErrPreflightFailed("schema_unresolvable")
		}
		return stageSchemaResolution{
			Schema: effectiveDB, SearchPath: []string{effectiveDB},
		}, nil
	}
	if allPlanStatementsExplicitlyQualified(planStmts) {
		return stageSchemaResolution{}, nil
	}
	return stageSchemaResolution{}, domain.ErrPreflightFailed("schema_unresolvable")
}

// openTargetSnapshot decrypts the target snapshot.
func (s *Services) openTargetSnapshot(stage *targetSnapshotRow) (*frozenTargetSnapshot, error) {
	snap, err := orderdomain.OpenTargetSnapshot(s.Crypter, stage.StageID, crypto.Envelope{
		Ciphertext: stage.TargetSnapshotCiphertext, Nonce: stage.TargetSnapshotNonce,
		KeyID: stage.TargetSnapshotKeyID, Algorithm: stage.TargetSnapshotAlgorithm,
	})
	if err != nil {
		return nil, domain.ErrPreflightFailed("target_snapshot_unreadable")
	}
	return &snap, nil
}

// resolveStageSchema resolves the stage execution schema (RCP-20261031-REMOVE-FLOW-
// SCHEMA-MAPPINGS: stages no longer configure logical-to-physical schema mappings).
//   - PostgreSQL: execution environment comes from the search_path context frozen by the
//     accepted review (the immutable stage_review_snapshot envelope associated with order_stages);
//     the path must pass validation and match object bindings, otherwise fail closed.
//   - MySQL / other engines: effective default database (explicit target_database override or
//     datasources.database_name), or empty default schema context when every frozen statement
//     explicitly qualifies valid non-system db.table references.
func (s *Services) resolveStageSchema(ctx context.Context, db *gorm.DB, orderID string,
	stage *targetSnapshotRow, snap *frozenTargetSnapshot,
	ds *datasource.DatasourceModel, planStmts ...PlanStatement) (stageSchemaResolution, error) {
	var frozen *reviewapp.FrozenPostgresSearchPath
	var binding *reviewapp.FrozenPostgresPathBinding
	if ds != nil && ds.CompatibilityMode == "postgresql" {
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
	var targetDB string
	if snap != nil {
		targetDB = snap.TargetDatabase
	}
	stmts := planStmts
	if ds != nil && ds.CompatibilityMode != "postgresql" &&
		resolveEffectiveMySQLExecutionDB(ds, targetDB) == "" &&
		len(stmts) == 0 && s != nil && db != nil && stage != nil {
		plan, err := s.freezeStatementPlan(ctx, db, orderID, stage.StageID, ds)
		if err != nil {
			return stageSchemaResolution{}, err
		}
		stmts = plan.Statements
	}
	resolution, err := stageSchemaResolutionForDatasource(ds, frozen, targetDB, stmts...)
	if err != nil {
		return stageSchemaResolution{}, err
	}
	resolution.Binding = binding
	return resolution, nil
}
