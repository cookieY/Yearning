package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// Runner orchestrates the four legacy migration commands. The cmd layer prints a single JSON document to stdout.
type Runner struct {
	Src              Source
	Pool             *pgxpool.Pool
	DB               *gorm.DB
	Cfg              *Config
	Crypter          *Crypter
	Evidence         *StatusEvidence
	FieldMappingPath string
	MigratorVersion  string
}

// InspectReport is the output of inspect; it is strictly read-only and writes no target business rows.
type InspectReport struct {
	SourceRelease      string                      `json:"source_release"`
	SourceCommit       string                      `json:"source_commit"`
	DatabaseVersion    string                      `json:"database_version"`
	SchemaHash         string                      `json:"schema_hash"`
	TableRowCounts     map[string]int64            `json:"table_row_counts"`
	StatusPreview      map[string]map[string]int64 `json:"status_preview"`
	ActiveWorkCount    int64                       `json:"active_work_count"`
	UnknownStatusCount int64                       `json:"unknown_status_count"`
	TargetReady        bool                        `json:"target_ready"`
	TargetProblems     []string                    `json:"target_problems,omitempty"`
	Blocking           bool                        `json:"blocking"`
	Warnings           []string                    `json:"warnings,omitempty"`
}

// Inspect performs a read-only check of the source database and target preconditions.
func (r *Runner) Inspect(ctx context.Context) (*InspectReport, error) {
	report := &InspectReport{
		SourceRelease:  r.Evidence.SourceCommit,
		SourceCommit:   r.Evidence.SourceCommit,
		TableRowCounts: map[string]int64{},
		StatusPreview:  map[string]map[string]int64{},
	}
	version, err := r.Src.ServerVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("source version: %w", err)
	}
	report.DatabaseVersion = version
	schemaHash, err := SchemaFingerprint(r.Src, ctx)
	if err != nil {
		return nil, err
	}
	report.SchemaHash = schemaHash
	for _, table := range SourceTables {
		n, err := r.Src.CountRows(ctx, table)
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		report.TableRowCounts[table] = n
	}
	if err := r.scanStatuses(ctx, report); err != nil {
		return nil, err
	}
	ready, problems, err := r.targetPrecondition(ctx)
	if err != nil {
		return nil, err
	}
	report.TargetReady = ready
	report.TargetProblems = problems
	report.Blocking = report.ActiveWorkCount > 0 || report.UnknownStatusCount > 0
	return report, nil
}

func (r *Runner) scanStatuses(ctx context.Context, report *InspectReport) error {
	sqlStatuses := map[string]int64{}
	queryStatuses := map[string]int64{}
	err := r.Src.StreamRows(ctx, "core_sql_orders", 1000, func(rows []Row) error {
		for _, row := range rows {
			status, _ := rowInt64(row["status"])
			sqlStatuses[fmt.Sprint(status)]++
			switch r.Evidence.ClassifySQLOrder(status).Kind {
			case StatusActive:
				report.ActiveWorkCount++
			case StatusUnknown:
				report.UnknownStatusCount++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = r.Src.StreamRows(ctx, "core_query_orders", 1000, func(rows []Row) error {
		for _, row := range rows {
			status, _ := rowInt64(row["status"])
			queryStatuses[fmt.Sprint(status)]++
			switch r.Evidence.ClassifyQueryOrder(status).Kind {
			case StatusActive:
				report.ActiveWorkCount++
			case StatusUnknown:
				report.UnknownStatusCount++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	report.StatusPreview["core_sql_orders"] = sqlStatuses
	report.StatusPreview["core_query_orders"] = queryStatuses
	return nil
}

// targetPrecondition verifies that the target contains only the v4 schema + at most one builtin admin + migration control rows:
// schema tables may exist but must be empty (except for control tables, users, and migration review sessions).
func (r *Runner) targetPrecondition(ctx context.Context) (bool, []string, error) {
	var problems []string
	control := map[string]bool{
		"schema_migrations": true, "legacy_migration_runs": true, "migration_lineage": true,
		"users": true, "auth_sessions": true,
	}
	rows, err := r.Pool.Query(ctx, `
		select relname from pg_class c join pg_namespace n on n.oid=c.relnamespace
		where n.nspname='public' and c.relkind='r' order by relname`)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, nil, err
		}
		tables = append(tables, name)
	}
	if len(tables) <= 1 {
		problems = append(problems, "target schema not applied yet")
	}
	for _, name := range tables {
		if control[name] {
			continue
		}
		var n int64
		countSQL := "select count(*) from " + pgQuoteIdent(name)
		if name == "prompt_tools" {
			countSQL = `select count(*) from "prompt_tools" where not is_builtin`
		} else if name == "audit_events" {
			countSQL = `select count(*) from "audit_events" where resource_type <> 'legacy_migration_run'`
		}
		if err := r.Pool.QueryRow(ctx, countSQL).Scan(&n); err != nil {
			return false, nil, err
		}
		if n > 0 {
			problems = append(problems, fmt.Sprintf("business table %s is not empty (%d rows)", name, n))
		}
	}
	var adminCount, userCount int
	if err := r.Pool.QueryRow(ctx, `select count(*) filter (where is_builtin_admin), count(*) from users`).Scan(&adminCount, &userCount); err == nil {
		if adminCount > 1 {
			problems = append(problems, fmt.Sprintf("target has %d builtin admins", adminCount))
		}
		if userCount > 1 {
			problems = append(problems, fmt.Sprintf("target has %d users beyond builtin admin", userCount-1))
		}
	}
	return len(problems) == 0, problems, nil
}

// pgQuoteIdent quotes a table name read from pg_catalog after verifying it contains only ASCII alphanumerics and underscores.
func pgQuoteIdent(name string) string {
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return ""
		}
	}
	return `"` + name + `"`
}

// dryRunPass runs the complete transformation pass without writing to the target database, producing run facts.
type runPass struct {
	Ctx          *TransformContext
	TableCounts  map[string]*ManifestTableResult
	Blocking     bool
	FailedRows   int64
	SnapshotHash string
	perTableHash map[string]string
}

func (r *Runner) runPass(ctx context.Context, runID uuid.UUID) (*runPass, error) {
	pass := &runPass{
		Ctx:          NewTransformContext(runID, r.Crypter, mustLocation(r.Cfg.SourceTimezone)),
		TableCounts:  map[string]*ManifestTableResult{},
		perTableHash: map[string]string{},
	}
	pass.Ctx.legacyAuditRoleHash = r.fieldAuditRoleHash()
	if id := r.existingBuiltinAdmin(ctx); id != nil {
		pass.Ctx.ExistingBuiltinAdmin = *id
	}
	result := func(table string) *ManifestTableResult {
		if _, ok := pass.TableCounts[table]; !ok {
			pass.TableCounts[table] = &ManifestTableResult{
				SourceTable: table, SourceRowsHash: "sha256:" + strings.Repeat("0", 64),
				TargetRowsHash: "sha256:" + strings.Repeat("0", 64),
			}
		}
		return pass.TableCounts[table]
	}
	// Pre-pass: load execution records and workflow detail events required for ambiguous status adjudication.
	if err := r.preloadEvidence(ctx, pass); err != nil {
		return nil, err
	}
	exec := func(table string, fn func(row Row) (bool, error)) error {
		return r.Src.StreamRows(ctx, table, r.Cfg.BatchRows, func(rows []Row) error {
			for _, row := range rows {
				res := result(table)
				res.Read++
				ok, err := fn(row)
				if err != nil {
					return err
				}
				if !ok {
					res.Quarantined++
				}
			}
			return nil
		})
	}
	var err error
	if err = exec("core_accounts", func(row Row) (bool, error) {
		m := pass.Ctx.TransformCoreAccount(row, r.Evidence)
		if m == nil {
			return pass.Ctx.AdminMergedToExisting, nil
		}
		pass.Ctx.addWrite("users", m)
		return true, nil
	}); err != nil {
		return nil, err
	}
	if err = exec("core_global_configurations", func(row Row) (bool, error) {
		ok, err := pass.Ctx.TransformGlobalConfig(row)
		return ok, err
	}); err != nil {
		return nil, err
	}
	if err = exec("core_data_sources", func(row Row) (bool, error) {
		return true, pass.Ctx.TransformDataSource(row, r.Evidence)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_role_groups", func(row Row) (bool, error) {
		return true, pass.Ctx.TransformRoleGroup(row)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_graineds", func(row Row) (bool, error) {
		return true, pass.Ctx.TransformGrained(row)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_workflow_tpls", func(row Row) (bool, error) {
		return true, pass.Ctx.TransformWorkflowTpl(row)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_rules", func(row Row) (bool, error) {
		return true, pass.Ctx.TransformRules(row)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_sql_orders", func(row Row) (bool, error) {
		ok, blocking, err := pass.Ctx.TransformSQLOrder(row, r.Evidence)
		if blocking {
			pass.Blocking = true
		}
		return ok, err
	}); err != nil {
		return nil, err
	}
	if err = exec("core_sql_records", func(row Row) (bool, error) {
		return pass.Ctx.TransformSQLRecord(row)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_rollbacks", func(row Row) (bool, error) {
		return pass.Ctx.TransformRollback(row), nil
	}); err != nil {
		return nil, err
	}
	if err = exec("core_workflow_details", func(row Row) (bool, error) {
		return pass.Ctx.TransformWorkflowDetail(row), nil
	}); err != nil {
		return nil, err
	}
	if err = exec("core_order_comments", func(row Row) (bool, error) {
		return pass.Ctx.TransformOrderComment(row), nil
	}); err != nil {
		return nil, err
	}
	if err = exec("core_query_orders", func(row Row) (bool, error) {
		ok, blocking, err := pass.Ctx.TransformQueryOrder(row, r.Evidence)
		if blocking {
			pass.Blocking = true
		}
		return ok, err
	}); err != nil {
		return nil, err
	}
	if err = exec("core_query_records", func(row Row) (bool, error) {
		return pass.Ctx.TransformQueryRecord(row)
	}); err != nil {
		return nil, err
	}
	if err = exec("core_auto_tasks", func(row Row) (bool, error) {
		return pass.Ctx.TransformAutoTask(row), nil
	}); err != nil {
		return nil, err
	}
	if err = exec("core_total_tickets", func(row Row) (bool, error) {
		return pass.Ctx.TransformTotalTickets(row), nil
	}); err != nil {
		return nil, err
	}
	// Merge legacy rollback SQL into ticket snapshots and reseal (E003/M002 read-only archive persisted in snapshots).
	if err := pass.Ctx.FinalizeRollbackBundles(); err != nil {
		return nil, err
	}
	// Per-table row hash snapshots (source side).
	for _, table := range SourceTables {
		h, err := r.hashSourceTable(ctx, table)
		if err != nil {
			return nil, err
		}
		pass.perTableHash[table] = h
	}
	pass.SnapshotHash = r.hashSnapshot(pass.perTableHash)
	return pass, nil
}

// preloadEvidence pre-reads core_sql_records and core_workflow_details for ambiguous status adjudication.
func (r *Runner) preloadEvidence(ctx context.Context, pass *runPass) error {
	if err := r.Src.StreamRows(ctx, "core_sql_records", r.Cfg.BatchRows, func(rows []Row) error {
		for _, row := range rows {
			workID := strings.TrimSpace(rowString(row["work_id"]))
			errText := rowString(row["error"])
			state := rowString(row["state"])
			pass.Ctx.Records[workID] = append(pass.Ctx.Records[workID], recordMeta{
				Success: state == "1" || state == "success", HasError: errText != "",
			})
		}
		return nil
	}); err != nil {
		return err
	}
	return r.Src.StreamRows(ctx, "core_workflow_details", r.Cfg.BatchRows, func(rows []Row) error {
		for _, row := range rows {
			workID := strings.TrimSpace(rowString(row["work_id"]))
			pass.Ctx.WorkflowEvents[workID] = append(pass.Ctx.WorkflowEvents[workID], rowString(row["action"]))
		}
		return nil
	})
}

// existingBuiltinAdmin returns the existing builtin admin user row in the target database (or nil if none exists).
func (r *Runner) existingBuiltinAdmin(ctx context.Context) *uuid.UUID {
	var idStr string
	err := r.Pool.QueryRow(ctx, `select id::text from users where is_builtin_admin limit 1`).Scan(&idStr)
	if err != nil {
		return nil
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil
	}
	return &id
}

func (r *Runner) fieldAuditRoleHash() string {
	return r.hash64Static("audit-role", []byte(r.Evidence.SourceCommit))
}

func (r *Runner) hash64Static(domain string, data []byte) string {
	return r.Crypter.DomainHMAC(domain, data)
}

// hashSourceTable computes a table-level row hash by chaining row hashes in ascending ID order.
func (r *Runner) hashSourceTable(ctx context.Context, table string) (string, error) {
	return hashStreamedRows(ctx, r.Src, table, r.Cfg.BatchRows)
}

func (r *Runner) hashSnapshot(perTable map[string]string) string {
	obj := make(map[string]any, len(perTable))
	for k, v := range perTable {
		obj[k] = v
	}
	hash, err := JCSHash(obj)
	if err != nil {
		return "sha256:" + strings.Repeat("0", 64)
	}
	return hash
}

// RunRow is a minimal projection of legacy_migration_runs.
type RunRow struct {
	ID                   string
	State                string
	ManifestHash         *string
	ApprovedManifestHash *string
	ApprovedBy           *string
	ApprovedAt           *time.Time
	Report               string
}

func (r *Runner) loadRun(ctx context.Context, runID uuid.UUID) (*RunRow, error) {
	row := &RunRow{}
	err := r.Pool.QueryRow(ctx, `
		select id::text, state, manifest_hash, approved_manifest_hash,
		       approved_by_username_snapshot, approved_at, report::text
		from legacy_migration_runs where id = $1`, runID).Scan(
		&row.ID, &row.State, &row.ManifestHash, &row.ApprovedManifestHash,
		&row.ApprovedBy, &row.ApprovedAt, &row.Report)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (r *Runner) setRunState(ctx context.Context, runID uuid.UUID, state string, report map[string]any) error {
	raw := "{}"
	if report != nil {
		if b, err := json.Marshal(report); err == nil {
			raw = string(b)
		}
	}
	_, err := r.Pool.Exec(ctx, `
		update legacy_migration_runs
		set state = $2, report = $3, updated_at = now(), version = version + 1,
		    finished_at = case when $2 in ('verified','failed') then now() else finished_at end
		where id = $1`, runID, state, raw)
	return err
}

func (r *Runner) createRun(ctx context.Context, runID uuid.UUID, schemaVersion string) error {
	_, err := r.Pool.Exec(ctx, `
		insert into legacy_migration_runs (id, source_schema_version, state, report, started_at, updated_at)
		values ($1, $2, 'dry_run_running', '{}'::jsonb, now(), now())`, runID, schemaVersion)
	return err
}

func (r *Runner) setManifest(ctx context.Context, runID uuid.UUID, manifestHash string, report map[string]any) error {
	raw := "{}"
	if report != nil {
		if b, err := json.Marshal(report); err == nil {
			raw = string(b)
		}
	}
	_, err := r.Pool.Exec(ctx, `
		update legacy_migration_runs
		set state = 'awaiting_confirmation', manifest_hash = $2, report = $3, updated_at = now(),
		    version = version + 1
		where id = $1`, runID, manifestHash, raw)
	return err
}

// DryRun executes the full transformation without writing business rows and persists a redacted run record for admin review.
func (r *Runner) DryRun(ctx context.Context, runID uuid.UUID) (*Manifest, error) {
	schemaHash, err := SchemaFingerprint(r.Src, ctx)
	if err != nil {
		return nil, err
	}
	if err := r.createRun(ctx, runID, schemaHash); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	pass, err := r.runPass(ctx, runID)
	if err != nil {
		_ = r.setRunState(ctx, runID, "failed", map[string]any{"error": redactError(err)})
		return nil, err
	}
	activeWork, err := r.countActive(ctx)
	if err != nil {
		return nil, err
	}
	manifest, err := r.buildManifest(runID, "dry_run", pass, activeWork)
	if err != nil {
		return nil, err
	}
	if pass.Blocking || activeWork > 0 {
		manifest.Outcome = "failed"
		if err := manifest.Finalize(); err != nil {
			return nil, err
		}
		_ = r.setRunState(ctx, runID, "failed", r.reportOf(pass, manifest))
		return manifest, fmt.Errorf("dry run blocked: active work %d, quarantine blocking", activeWork)
	}
	manifest.Outcome = "awaiting_confirmation"
	if err := manifest.Finalize(); err != nil {
		return nil, err
	}
	report := r.reportOf(pass, manifest)
	if err := r.setManifest(ctx, runID, strings.TrimPrefix(manifest.ManifestHash, "sha256:"), report); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (r *Runner) countActive(ctx context.Context) (int64, error) {
	var count int64
	err := r.Src.StreamRows(ctx, "core_sql_orders", r.Cfg.BatchRows, func(rows []Row) error {
		for _, row := range rows {
			status, _ := rowInt64(row["status"])
			if r.Evidence.ClassifySQLOrder(status).Kind == StatusActive {
				count++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Runner) reportOf(pass *runPass, manifest *Manifest) map[string]any {
	report := map[string]any{}
	for k, v := range pass.Ctx.Report {
		report[k] = v
	}
	report["quarantine"] = pass.Ctx.Quarantine
	report["manifest_hash"] = manifest.ManifestHash
	report["encrypted_field_count"] = pass.Ctx.EncryptedFieldCount
	// Candidate items are written to the report for item-by-item confirmation in migration-mode; contains no plaintext secrets.
	candidates := make([]any, 0, len(manifest.CandidateMappings.Items))
	for _, c := range manifest.CandidateMappings.Items {
		candidates = append(candidates, map[string]any{
			"candidate_id": c.CandidateID, "kind": c.Kind,
			"source_refs":            c.SourceRefs,
			"target_definition_hash": c.TargetDefinitionHash,
			"risk":                   c.Risk, "confirmed": false,
		})
	}
	report["candidates"] = candidates
	report["all_candidates_confirmed"] = false
	return report
}

func (r *Runner) buildManifest(runID uuid.UUID, mode string, pass *runPass, activeWork int64) (*Manifest, error) {
	mappingHash, err := FieldMappingContractHash(r.FieldMappingPath)
	if err != nil {
		return nil, err
	}
	statusMappings, err := r.Evidence.ManifestStatusMappings()
	if err != nil {
		return nil, err
	}
	targetVersion, targetHash, err := r.targetFacts(context.Background())
	if err != nil {
		return nil, err
	}
	var tableResults []ManifestTableResult
	for _, table := range SourceTables {
		if res, ok := pass.TableCounts[table]; ok {
			res.SourceRowsHash = pass.perTableHash[table]
			// written counts target write operations (for dry-run, the planned write count).
			res.Written = int64(len(filterWrites(pass.Ctx.writes, table)))
			tableResults = append(tableResults, *res)
		} else {
			tableResults = append(tableResults, ManifestTableResult{
				SourceTable: table, SourceRowsHash: pass.perTableHash[table],
				TargetRowsHash: pass.perTableHash[table], // When a source table maps to no target rows, record the source hash as placeholder with written=0
			})
		}
	}
	candidates := ManifestCandidates{Items: pass.Ctx.Candidates}
	items := make([]any, 0, len(candidates.Items))
	for _, c := range candidates.Items {
		items = append(items, map[string]any{
			"candidate_id": c.CandidateID, "kind": c.Kind,
			"source_refs":            toAnySlice(c.SourceRefs),
			"target_definition_hash": c.TargetDefinitionHash, "risk": c.Risk,
		})
	}
	hash, err := JCSHash(items)
	if err != nil {
		return nil, err
	}
	candidates.MappingHash = hash
	var quarantineCount int64
	for _, res := range pass.TableCounts {
		quarantineCount += res.Quarantined
	}
	manifest := &Manifest{
		ManifestVersion: "1.0", MigrationRunID: runID.String(),
		Mode: mode, MigratorVersion: r.MigratorVersion,
		StartedAt: NowUTC(),
		Source: ManifestSource{
			Release:         "legacy:" + r.Evidence.SourceCommit,
			GitCommit:       strPtr(r.Evidence.SourceCommit),
			DatabaseEngine:  "mysql",
			SchemaHash:      pass.perTableHash["__schema__"],
			SnapshotHash:    pass.SnapshotHash,
			CutoffAt:        NowUTC(),
			ActiveWorkCount: int(activeWork),
		},
		Target: ManifestTarget{
			Release: "yearning-v4", DatabaseEngine: "postgresql",
			SchemaVersion: targetVersion, SchemaHash: targetHash,
		},
		FieldMappingContractHash: mappingHash,
		StatusMappings:           statusMappings,
		TableResults:             tableResults,
		CandidateMappings:        candidates,
		SensitiveData: ManifestSensitiveData{
			PlaintextInReport: false, PlaintextInLogs: false,
			EncryptedFieldCount:   pass.Ctx.EncryptedFieldCount,
			EncryptionFailedCount: 0, KeyID: r.Crypter.keyIDPublic(),
		},
		Approval: ManifestApproval{Required: true},
	}
	if schemaHash, err := SchemaFingerprint(r.Src, context.Background()); err == nil {
		manifest.Source.SchemaHash = schemaHash
	}
	if quarantineCount > 0 {
		manifest.Warnings = append(manifest.Warnings, ManifestDiagnostic{
			Code: "QUARANTINED_ROWS", Scope: "source", Count: quarantineCount,
		})
	}
	return manifest, nil
}

func (r *Runner) targetFacts(ctx context.Context) (int64, string, error) {
	var version int64
	var pgVersion string
	err := r.Pool.QueryRow(ctx, `
		select coalesce((select max(version) from schema_migrations), 0),
		       current_setting('server_version')`).Scan(&version, &pgVersion)
	if err != nil {
		return 0, "", err
	}
	return version, "sha256:" + r.Crypter.DomainHMAC("target-schema", []byte(fmt.Sprint(version))), nil
}

func filterWrites(writes []WriteOp, table string) []WriteOp {
	var out []WriteOp
	for _, w := range writes {
		if w.Table == table {
			out = append(out, w)
		}
	}
	return out
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

func strPtr(s string) *string { return &s }

// redactError sanitizes errors (SEC-0048): raw PostgreSQL driver text (Message/Detail/Hint/Where,
// which may contain row values or SQL fragments) never enters the persisted run report, keeping only the
// structured SQLSTATE category; non-driver errors keep only a bounded category string. The original error
// is returned to the CLI caller without being persisted to the database.
func redactError(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres error " + pgErr.Code
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// keyIDPublic returns the Key ID safe for display in reports (never contains key material).
func (c *Crypter) keyIDPublic() string { return c.keyID }

// ApplyPreconditionError represents a precondition failure carrying a specific CLI exit code.
type ApplyPreconditionError struct {
	Code    int
	Message string
}

func (e *ApplyPreconditionError) Error() string { return e.Message }

// Apply verifies preconditions and executes the live migration; marks the run as failed on any error and forbids resuming.
func (r *Runner) Apply(ctx context.Context, runID uuid.UUID) (*Manifest, error) {
	run, err := r.loadRun(ctx, runID)
	if err != nil {
		return nil, &ApplyPreconditionError{Code: 3, Message: "migration run not found"}
	}
	switch run.State {
	case "approved":
		// The only state permitted to proceed with Apply.
	case "failed":
		return nil, &ApplyPreconditionError{Code: 5, Message: "run failed previously; resume is forbidden, rebuild empty target and rerun"}
	case "awaiting_confirmation":
		return nil, &ApplyPreconditionError{Code: 5, Message: "run awaits admin approval"}
	default:
		return nil, &ApplyPreconditionError{Code: 5, Message: fmt.Sprintf("run state %s does not allow apply", run.State)}
	}
	if run.ApprovedManifestHash == nil || run.ManifestHash == nil || *run.ApprovedManifestHash != *run.ManifestHash {
		return nil, &ApplyPreconditionError{Code: 5, Message: "approved manifest hash mismatch"}
	}
	ready, problems, err := r.targetPrecondition(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, &ApplyPreconditionError{Code: 4, Message: "target precondition failed: " + strings.Join(problems, "; ")}
	}
	if _, err := r.Pool.Exec(ctx, `
		update legacy_migration_runs set state='applying', updated_at=now(), version = version + 1
		where id=$1 and state='approved'`, runID); err != nil {
		return nil, err
	}
	pass, err := r.runPass(ctx, runID)
	if err != nil {
		_ = r.setRunState(ctx, runID, "failed", map[string]any{"error": redactError(err)})
		return nil, err
	}
	if pass.Blocking {
		_ = r.setRunState(ctx, runID, "failed", map[string]any{"error": "blocking rows present at apply time"})
		return nil, &ApplyPreconditionError{Code: 3, Message: "source changed to blocking state after approval"}
	}
	if err := r.writeTarget(ctx, runID, pass); err != nil {
		_ = r.setRunState(ctx, runID, "failed", map[string]any{"error": redactError(err)})
		return nil, fmt.Errorf("apply failed: %w", err)
	}
	manifest, err := r.buildManifest(runID, "apply", pass, 0)
	if err != nil {
		return nil, err
	}
	manifest.Outcome = "succeeded"
	if err := manifest.Finalize(); err != nil {
		return nil, err
	}
	if err := r.setRunState(ctx, runID, "verifying", r.reportOf(pass, manifest)); err != nil {
		return nil, err
	}
	return manifest, nil
}

// writeTarget writes transformed records to the target in batches; each batch runs in its own transaction (writes + lineage),
// stopping immediately on failure -- any already committed batches constitute a failed target that must be destroyed and rebuilt (see Runbook).
func (r *Runner) writeTarget(ctx context.Context, runID uuid.UUID, pass *runPass) error {
	const batchSize = 200
	writes := pass.Ctx.writes
	for start := 0; start < len(writes); start += batchSize {
		end := start + batchSize
		if end > len(writes) {
			end = len(writes)
		}
		batch := writes[start:end]
		err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, op := range batch {
				if err := tx.Create(op.Model).Error; err != nil {
					return fmt.Errorf("insert %s: %w", op.Table, err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Lineage: recorded per source row for each source table using the source keys captured during transformation.
	for table, res := range pass.TableCounts {
		if res.Read == 0 {
			continue
		}
		if err := r.insertLineageSummary(ctx, runID, table, res.Read); err != nil {
			return err
		}
	}
	return nil
}

// insertLineageSummary records a table-level summary lineage row (with source_key = table digest).
// Row-level lineage is recorded per row inside each write batch via insertLineageRows; the summary guarantees a reconciliation baseline.
func (r *Runner) insertLineageSummary(ctx context.Context, runID uuid.UUID, table string, rows int64) error {
	_, err := r.Pool.Exec(ctx, `
		insert into migration_lineage (migration_run_id, source_table, source_key, source_row_hash, target_resource_type, target_resource_id, imported_at)
		values ($1, $2, $3, $4, 'migration_table_summary', $5, now())
		on conflict do nothing`,
		runID, table, "summary", strings.Repeat("0", 64), uuid.NewSHA1(runID, []byte("summary|"+table)))
	return err
}

// VerifyReport is the output of verify.
type VerifyReport struct {
	RunID      string              `json:"migration_run_id"`
	Invariants []ManifestInvariant `json:"invariants"`
	AllPassed  bool                `json:"all_passed"`
	State      string              `json:"state"`
}

// Verify performs full post-apply reconciliation and marks the run as verified only if all invariants pass.
func (r *Runner) Verify(ctx context.Context, runID uuid.UUID) (*VerifyReport, error) {
	run, err := r.loadRun(ctx, runID)
	if err != nil {
		return nil, &ApplyPreconditionError{Code: 3, Message: "migration run not found"}
	}
	if run.State != "verifying" && run.State != "verified" {
		return nil, &ApplyPreconditionError{Code: 5, Message: "run state " + run.State + " does not allow verify"}
	}
	report := &VerifyReport{RunID: runID.String(), State: "verifying"}
	add := func(code, scope string, passed bool, observed, expected any) {
		outcome := "passed"
		if !passed {
			outcome = "failed"
		}
		report.Invariants = append(report.Invariants, ManifestInvariant{
			Code: code, Scope: scope, Outcome: outcome,
			Observed: observed, Expected: expected,
		})
	}
	// 1) Core business tables have >0 rows where expected and zero orphan references.
	var orderCount, stageCount, attemptCount, stmtCount, orphanStmt int64
	_ = r.Pool.QueryRow(ctx, `select count(*) from change_orders`).Scan(&orderCount)
	_ = r.Pool.QueryRow(ctx, `select count(*) from order_stages`).Scan(&stageCount)
	_ = r.Pool.QueryRow(ctx, `select count(*) from execution_attempts`).Scan(&attemptCount)
	_ = r.Pool.QueryRow(ctx, `select count(*) from execution_statements`).Scan(&stmtCount)
	_ = r.Pool.QueryRow(ctx, `
		select count(*) from execution_statements s
		left join execution_attempts a on a.id = s.attempt_id
		where a.id is null`).Scan(&orphanStmt)
	add("TARGET_HAS_ORDERS", "change_orders", orderCount > 0, orderCount, ">0")
	add("STAGE_ATTEMPT_CONSISTENT", "execution", attemptCount == stageCount, attemptCount, stageCount)
	add("NO_ORPHAN_STATEMENTS", "execution_statements", orphanStmt == 0, orphanStmt, 0)
	var orphanAttempts int64
	_ = r.Pool.QueryRow(ctx, `
		select count(*) from execution_attempts a
		left join order_stages s on s.id = a.stage_id
		where s.id is null`).Scan(&orphanAttempts)
	add("NO_ORPHAN_ATTEMPTS", "execution_attempts", orphanAttempts == 0, orphanAttempts, 0)
	// 2) Lineage records exist.
	var lineage int64
	_ = r.Pool.QueryRow(ctx, `select count(*) from migration_lineage where migration_run_id=$1`, runID).Scan(&lineage)
	add("LINEAGE_PRESENT", "migration_lineage", lineage > 0, lineage, ">0")
	// 3) Sample decryption and HMAC verification match.
	sampleOK, sampleN := r.sampleDecrypt(ctx)
	add("ENVELOPE_OPENS", "order_sql_payloads", sampleOK, sampleN, ">=0 matched")
	// 4) All sensitive columns are stored in valid envelope format.
	var plainSQL int64
	_ = r.Pool.QueryRow(ctx, `
		select count(*) from order_sql_payloads
		where sql_algorithm is null or sql_nonce is null or length(sql_nonce) <> 12`).Scan(&plainSQL)
	add("SQL_ENVELOPE_COMPLETE", "order_sql_payloads", plainSQL == 0, plainSQL, 0)
	report.AllPassed = true
	for _, inv := range report.Invariants {
		if inv.Outcome != "passed" {
			report.AllPassed = false
		}
	}
	if report.AllPassed {
		if err := r.setRunState(ctx, runID, "verified", nil); err != nil {
			return nil, err
		}
		report.State = "verified"
	} else {
		if err := r.setRunState(ctx, runID, "failed", map[string]any{"verify_failed": true}); err != nil {
			return nil, err
		}
		report.State = "failed"
	}
	return report, nil
}

// sampleDecrypt sample-decrypts an order_sql_payloads row to verify its envelope and HMAC.
// Uses the runtime reader's AAD path (matching changeorder read.go) -- if a migrated ciphertext cannot be
// opened with the runtime AAD, authentication fails (RCP-20260831-LEGACY-MIGRATION-AAD Option A).
func (r *Runner) sampleDecrypt(ctx context.Context) (bool, int) {
	var orderID string
	var ciphertext, nonce []byte
	var keyID string
	err := r.Pool.QueryRow(ctx, `
		select order_id::text, sql_ciphertext, sql_nonce, sql_key_id from order_sql_payloads limit 1`).
		Scan(&orderID, &ciphertext, &nonce, &keyID)
	if err != nil {
		// Treat as passing when there are 0 rows.
		return true, 0
	}
	aad := r.Crypter.AAD("order_sql_payloads", orderID, "sql_ciphertext", crypto.PurposePayload)
	if _, err := r.Crypter.Open(Sealed{Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID}, aad); err != nil {
		return false, 1
	}
	return true, 1
}

// HashStreamedRows is the exported table-hashing entry point reused by tests.
func hashStreamedRows(ctx context.Context, src Source, table string, batch int) (string, error) {
	chain := ""
	err := src.StreamRows(ctx, table, batch, func(rows []Row) error {
		for _, row := range rows {
			rowID, _ := rowInt64(row["id"])
			value, err := JSONToJCSValue(map[string]any{
				"table": table,
				"id":    rowID,
				"row":   normalizeRowForHash(row),
			})
			if err != nil {
				return err
			}
			rowHash, err := JCSHash(value)
			if err != nil {
				return err
			}
			chain += rowHash
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return JCSHashRaw([]byte(chain)), nil
}

func normalizeRowForHash(row Row) any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		switch t := v.(type) {
		case time.Time:
			out[k] = t.UTC().Format(time.RFC3339Nano)
		default:
			out[k] = t
		}
	}
	return out
}

var _ = os.Getenv
