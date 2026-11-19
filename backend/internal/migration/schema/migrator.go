package schema

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LedgerRow maps to a single row in schema_migrations.
type LedgerRow struct {
	Version     int64
	Filename    string
	SHA256      string
	StartedAt   time.Time
	FinishedAt  *time.Time
	Success     bool
	FailureName *string
}

// ErrConcurrentMigrator indicates another Migrator instance won the race to reserve this version in the ledger.
var ErrConcurrentMigrator = errors.New("concurrent migrator won this version")

// Migrator holds the target connection pool and executes schema ledger operations.
type Migrator struct {
	Pool *pgxpool.Pool
}

// LoadLedger loads all rows from schema_migrations ordered by version.
func (m *Migrator) LoadLedger(ctx context.Context) ([]LedgerRow, error) {
	rows, err := m.Pool.Query(ctx, `
		select version, filename, sha256, started_at, finished_at, success, failure_name
		from schema_migrations order by version`)
	if err != nil {
		if sentinel := pgcErrMissingTable(err); sentinel != nil {
			return nil, sentinel
		}
		return nil, err
	}
	defer rows.Close()
	var out []LedgerRow
	for rows.Next() {
		var r LedgerRow
		if err := rows.Scan(&r.Version, &r.Filename, &r.SHA256, &r.StartedAt, &r.FinishedAt, &r.Success, &r.FailureName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		if sentinel := pgcErrMissingTable(err); sentinel != nil {
			return nil, sentinel
		}
		return nil, err
	}
	return out, nil
}

// FileStatus describes the ledger status of a single migration file.
type FileStatus struct {
	Version  int64  `json:"version"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	State    string `json:"state"` // applied | pending | failed
	Problem  string `json:"problem,omitempty"`
}

// StatusReport is the output of `migrate status`.
type StatusReport struct {
	SchemaVersion int64        `json:"schema_version"`
	TargetVersion int64        `json:"target_version"`
	Files         []FileStatus `json:"files"`
	Problems      []string     `json:"problems"`
	Healthy       bool         `json:"healthy"`
	ServerVersion string       `json:"server_version"`
}

// Analyze compares the migration directory against the database ledger and returns any discrepancies.
// An empty ledger paired with an empty directory is a valid initial state.
func Analyze(files []Migration, ledger []LedgerRow) ([]FileStatus, []string) {
	byVersion := make(map[int64]LedgerRow, len(ledger))
	for _, row := range ledger {
		byVersion[row.Version] = row
	}
	fileNames := make(map[string]bool, len(files))
	for _, f := range files {
		fileNames[f.Filename] = true
	}
	var statuses []FileStatus
	var problems []string
	for _, f := range files {
		st := FileStatus{Version: f.Version, Filename: f.Filename, SHA256: f.SHA256, State: "pending"}
		if row, ok := byVersion[f.Version]; ok {
			switch {
			case !row.Success:
				st.State = "failed"
				name := ""
				if row.FailureName != nil {
					name = *row.FailureName
				}
				st.Problem = fmt.Sprintf("failed record (failure_name=%s) requires operator intervention", name)
			case row.Filename != f.Filename:
				st.State = "applied"
				st.Problem = "applied filename differs from ledger"
			case row.SHA256 != f.SHA256:
				st.State = "applied"
				st.Problem = "executed migration file hash changed"
			default:
				st.State = "applied"
			}
			if st.Problem != "" {
				problems = append(problems, fmt.Sprintf("version %d: %s", f.Version, st.Problem))
			}
			delete(byVersion, f.Version)
		}
		statuses = append(statuses, st)
	}
	for _, row := range byVersion {
		// Ledger row exists without a corresponding file in the directory: unknown future version or deleted migration file.
		problems = append(problems, fmt.Sprintf("ledger version %d (%s) has no file in migrations dir", row.Version, row.Filename))
		statuses = append(statuses, FileStatus{
			Version: row.Version, Filename: row.Filename, SHA256: row.SHA256,
			State:   map[bool]string{true: "applied", false: "failed"}[row.Success],
			Problem: "unknown future or deleted migration",
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Version < statuses[j].Version })
	if !sort.SliceIsSorted(statuses, func(i, j int) bool { return statuses[i].Version < statuses[j].Version }) {
		problems = append(problems, "status list not sorted")
	}
	return statuses, problems
}

// Status summarizes the current migration ledger state.
func (m *Migrator) Status(ctx context.Context, files []Migration) (*StatusReport, error) {
	var serverVersion string
	if err := m.Pool.QueryRow(ctx, "select current_setting('server_version')").Scan(&serverVersion); err != nil {
		return nil, err
	}
	var schemaVersion int64
	ledger, err := m.LoadLedger(ctx)
	if err != nil {
		if !IsUndefinedTable(err) {
			return nil, err
		}
		ledger = nil
	}
	for _, row := range ledger {
		if row.Version > schemaVersion {
			schemaVersion = row.Version
		}
	}
	statuses, problems := Analyze(files, ledger)
	var targetVersion int64
	for _, f := range files {
		if f.Version > targetVersion {
			targetVersion = f.Version
		}
	}
	return &StatusReport{
		SchemaVersion: schemaVersion,
		TargetVersion: targetVersion,
		Files:         statuses,
		Problems:      problems,
		Healthy:       len(problems) == 0,
		ServerVersion: serverVersion,
	}, nil
}

// pgcErrMissingTable converts PostgreSQL error 42P01 (undefined_table) into the errUndefinedTable sentinel.
func pgcErrMissingTable(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		return errUndefinedTable
	}
	return nil
}

var errUndefinedTable = errors.New("schema_migrations table does not exist")

// IsUndefinedTable allows callers to distinguish an uninitialized ledger table from other database errors.
func IsUndefinedTable(err error) bool { return errors.Is(err, errUndefinedTable) }

// PreflightReport is the output of `migrate preflight`.
type PreflightReport struct {
	ConnectivityOK      bool     `json:"connectivity_ok"`
	ServerVersion       string   `json:"server_version"`
	ServerMajorVersion  int      `json:"server_major_version"`
	ServerMajorOK       bool     `json:"server_major_ok"`
	LedgerExists        bool     `json:"ledger_exists"`
	LedgerHealthy       bool     `json:"ledger_healthy"`
	TargetEmpty         bool     `json:"target_empty"`
	PendingVersions     []int64  `json:"pending_versions"`
	TargetSchemaVersion int64    `json:"target_schema_version_after_apply"`
	Problems            []string `json:"problems"`
	Ready               bool     `json:"ready"`
}

const expectedServerMajor = 18 // Current PostgreSQL major version pinned by the release policy in api/contracts/platform-postgresql.json

// Preflight validates target connectivity, PostgreSQL major version, ledger consistency, and initial target state.
func (m *Migrator) Preflight(ctx context.Context, files []Migration) (*PreflightReport, error) {
	report := &PreflightReport{LedgerHealthy: true}
	var serverNum int
	if err := m.Pool.QueryRow(ctx, "select current_setting('server_version_num')::int, current_setting('server_version')").Scan(&serverNum, &report.ServerVersion); err != nil {
		return nil, fmt.Errorf("query server version: %w", err)
	}
	report.ConnectivityOK = true
	report.ServerMajorVersion = serverNum / 10000
	report.ServerMajorOK = report.ServerMajorVersion == expectedServerMajor
	if !report.ServerMajorOK {
		report.Problems = append(report.Problems, fmt.Sprintf("server major %d != expected %d", report.ServerMajorVersion, expectedServerMajor))
	}
	ledger, err := m.LoadLedger(ctx)
	if err != nil {
		if !IsUndefinedTable(err) {
			return nil, err
		}
	} else {
		report.LedgerExists = true
	}
	var userTableCount int
	if err := m.Pool.QueryRow(ctx, `
		select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = 'public' and c.relkind = 'r'`).Scan(&userTableCount); err != nil {
		return nil, err
	}
	if len(ledger) == 0 {
		report.TargetEmpty = userTableCount == 0
		if !report.TargetEmpty {
			report.Problems = append(report.Problems, fmt.Sprintf("target database has %d tables but ledger is empty", userTableCount))
		}
	}
	_, problems := Analyze(files, ledger)
	if len(problems) > 0 {
		report.LedgerHealthy = false
		report.Problems = append(report.Problems, problems...)
	}
	applied := make(map[int64]bool, len(ledger))
	for _, row := range ledger {
		if row.Success {
			applied[row.Version] = true
		}
	}
	for _, f := range files {
		if !applied[f.Version] {
			report.PendingVersions = append(report.PendingVersions, f.Version)
			report.TargetSchemaVersion = f.Version
		}
	}
	if len(ledger) > 0 {
		maxApplied := int64(0)
		for v := range applied {
			if v > maxApplied {
				maxApplied = v
			}
		}
		report.TargetSchemaVersion = maxApplied
	}
	report.Ready = len(report.Problems) == 0
	return report, nil
}

// StatementPlan describes the statement breakdown and lock impact of a single migration file.
type StatementPlan struct {
	Version        int64          `json:"version"`
	Filename       string         `json:"filename"`
	StatementKinds map[string]int `json:"statement_kinds"`
	StatementCount int            `json:"statement_count"`
	Irreversible   bool           `json:"irreversible"`
	LockImpact     []string       `json:"lock_impact"`
}

// PlanReport is the output of `migrate plan`; it contains no secrets or business data.
type PlanReport struct {
	CurrentVersion int64           `json:"current_version"`
	TargetVersion  int64           `json:"target_version"`
	Pending        []StatementPlan `json:"pending"`
	TableRewrites  int             `json:"estimated_table_rewrites"`
	BackupRequired bool            `json:"backup_required"`
	Notes          []string        `json:"notes"`
}

// Plan generates the execution and lock impact plan for pending migration files.
func (m *Migrator) Plan(ctx context.Context, files []Migration) (*PlanReport, error) {
	ledger, err := m.LoadLedger(ctx)
	if err != nil {
		if !IsUndefinedTable(err) {
			return nil, err
		}
	}
	applied := make(map[int64]bool, len(ledger))
	current := int64(0)
	for _, row := range ledger {
		if row.Success {
			applied[row.Version] = true
			if row.Version > current {
				current = row.Version
			}
		}
	}
	report := &PlanReport{CurrentVersion: current, BackupRequired: true}
	report.Notes = append(report.Notes,
		"Production migrations do not support automatic Down rollback; recovery relies on pre-release PostgreSQL backups (see db/migrations/README.md)",
		"Execution is serialized via a single-instance Job; advisory locks are not used, and duplicate migrators exit on ledger primary-key conflict",
	)
	for _, f := range files {
		if applied[f.Version] {
			continue
		}
		plan := StatementPlan{
			Version: f.Version, Filename: f.Filename,
			StatementKinds: map[string]int{},
			Irreversible:   true,
		}
		for _, stmt := range f.Statements {
			kind := classifyStatement(stmt)
			plan.StatementKinds[kind]++
			plan.StatementCount++
			plan.LockImpact = append(plan.LockImpact, lockImpact(kind))
		}
		report.Pending = append(report.Pending, plan)
		report.TargetVersion = f.Version
	}
	return report, nil
}

func classifyStatement(stmt string) string {
	head := stmt
	if idx := strings.IndexAny(head, "\n("); idx >= 0 {
		head = head[:idx]
	}
	fields := strings.Fields(head)
	if len(fields) == 0 {
		return "other"
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE":
		if len(fields) >= 2 {
			switch strings.ToUpper(fields[1]) {
			case "TABLE":
				return "create_table"
			case "INDEX", "UNIQUE":
				return "create_index"
			}
		}
		return "create_other"
	case "ALTER":
		return "alter"
	case "DROP":
		return "drop"
	case "BEGIN", "COMMIT", "ROLLBACK":
		return "transaction_control"
	default:
		return "other"
	}
}

func lockImpact(kind string) string {
	switch kind {
	case "create_table":
		return "AccessExclusiveLock on new table only"
	case "create_index":
		return "ShareLock on target table"
	case "alter":
		return "AccessExclusiveLock on target table; possible full rewrite"
	case "drop":
		return "AccessExclusiveLock on target table"
	case "transaction_control":
		return "none"
	default:
		return "depends on statement"
	}
}

// ApplyRecord records the apply outcome for a single migration file.
type ApplyRecord struct {
	Version    int64  `json:"version"`
	Filename   string `json:"filename"`
	State      string `json:"state"` // applied | failed
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// ApplyReport is the output of `migrate apply`.
type ApplyReport struct {
	Applied       []ApplyRecord `json:"applied"`
	Failed        []ApplyRecord `json:"failed"`
	SchemaVersion int64         `json:"schema_version"`
	Success       bool          `json:"success"`
}

// ledgerCreateStmt returns the statement that creates schema_migrations within a file (or empty string if none).
// The ledger table is defined by the version 1 migration file itself; Migrator idempotently pre-creates it
// before the first transaction and skips that statement when executing the file (keeping the file's DDL as sole source of truth).
func ledgerCreateStmt(m Migration) string {
	for _, stmt := range m.Statements {
		if isLedgerCreate(stmt) {
			return stmt
		}
	}
	return ""
}

func isLedgerCreate(stmt string) bool {
	head := strings.ToLower(strings.Join(strings.Fields(stmt), " "))
	return strings.HasPrefix(head, "create table schema_migrations")
}

// ensureLedger idempotently pre-creates the ledger table (rewriting CREATE TABLE to CREATE TABLE IF NOT EXISTS from the file DDL).
func (m *Migrator) ensureLedger(ctx context.Context, files []Migration) error {
	if len(files) == 0 {
		return nil
	}
	stmt := ledgerCreateStmt(files[0])
	if stmt == "" {
		return fmt.Errorf("version 1 migration must create schema_migrations")
	}
	idempotent := strings.Replace(stmt, "create table schema_migrations", "create table if not exists schema_migrations", 1)
	_, err := m.Pool.Exec(ctx, idempotent)
	return err
}

// Apply executes all pending migration files in ascending version order. Each file runs in its own transaction:
// first reserves the ledger row (version primary key) inside the transaction, then executes the DDL and sets success=true;
// if any DDL statement fails, rolls back the transaction and writes a success=false failure row in a separate transaction before returning the error.
func (m *Migrator) Apply(ctx context.Context, files []Migration) (*ApplyReport, error) {
	ledger, err := m.LoadLedger(ctx)
	if err != nil {
		if !IsUndefinedTable(err) {
			return nil, err
		}
	}
	applied := make(map[int64]bool, len(ledger))
	for _, row := range ledger {
		if !row.Success {
			return nil, fmt.Errorf("ledger contains failed version %d; operator intervention required", row.Version)
		}
		applied[row.Version] = true
	}
	report := &ApplyReport{}
	var pending []Migration
	for _, f := range files {
		if !applied[f.Version] {
			pending = append(pending, f)
		}
	}
	if len(pending) > 0 {
		if err := m.ensureLedger(ctx, files); err != nil {
			return nil, fmt.Errorf("ensure ledger: %w", err)
		}
	}
	for _, f := range pending {
		rec, applyErr := m.applyOne(ctx, f)
		if applyErr == nil {
			report.Applied = append(report.Applied, rec)
			report.SchemaVersion = f.Version
		} else {
			report.Failed = append(report.Failed, rec)
			return report, fmt.Errorf("migration %s failed: %w", f.Filename, applyErr)
		}
	}
	report.Success = len(report.Failed) == 0
	if report.SchemaVersion == 0 {
		report.SchemaVersion = maxAppliedVersion(ledger)
	}
	return report, nil
}

func maxAppliedVersion(ledger []LedgerRow) int64 {
	var max int64
	for _, row := range ledger {
		if row.Success && row.Version > max {
			max = row.Version
		}
	}
	return max
}

func (m *Migrator) applyOne(ctx context.Context, f Migration) (ApplyRecord, error) {
	start := time.Now()
	rec := ApplyRecord{Version: f.Version, Filename: f.Filename}
	err := m.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`insert into schema_migrations (version, filename, sha256, started_at) values ($1, $2, $3, $4)`,
			f.Version, f.Filename, f.SHA256, start.UTC()); err != nil {
			return err
		}
		for _, stmt := range f.Statements {
			// Migration files include their own BEGIN/COMMIT; because Migrator manages the transaction boundary,
			// skip file-level transaction control statements to prevent premature commits.
			if classifyStatement(stmt) == "transaction_control" {
				continue
			}
			// The ledger table was already created by ensureLedger; skip the CREATE TABLE statement inside file 1 to avoid duplicate creation.
			if isLedgerCreate(stmt) {
				continue
			}
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx,
			`update schema_migrations set finished_at = $1, success = true where version = $2`,
			time.Now().UTC(), f.Version)
		return err
	})
	if err == nil {
		rec.State = "applied"
		rec.DurationMs = time.Since(start).Milliseconds()
		return rec, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return rec, fmt.Errorf("%w: version %d", ErrConcurrentMigrator, f.Version)
	}
	rec.State = "failed"
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.Error = redactError(err)
	// The DDL transaction has been rolled back; persist a failure row in an independent transaction so status/preflight can detect the dirty state.
	failureName := classifyFailure(err)
	writeErr := m.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			insert into schema_migrations (version, filename, sha256, started_at, finished_at, success, failure_name)
			values ($1, $2, $3, $4, $5, false, $6)
			on conflict (version) do nothing`,
			f.Version, f.Filename, f.SHA256, start.UTC(), time.Now().UTC(), failureName)
		return err
	})
	if writeErr != nil {
		rec.Error = fmt.Sprintf("%s; additionally failed to persist failure row: %s", rec.Error, redactError(writeErr))
	}
	return rec, err
}

func (m *Migrator) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := m.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// classifyFailure returns a stable failure category name without exposing raw SQL text.
func classifyFailure(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres_" + pgErr.Code
	}
	return "internal_failure"
}

// redactError sanitizes errors (SEC-0048): PostgreSQL error fields (Message/Detail/Hint/Where)
// may carry row values or SQL fragments (e.g. duplicate key errors include the conflicting column value),
// so only the structured SQLSTATE category is retained; non-driver errors likewise emit only a stable category name,
// while the raw error is surfaced via stderr/exit code for operator diagnostics without entering reports or the ledger.
func redactError(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres error " + pgErr.Code
	}
	return classifyFailure(err)
}
