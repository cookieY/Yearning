package legacy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
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
