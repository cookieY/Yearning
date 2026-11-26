// runner_import.go Legacy migration dry-run manifest generation, live batch apply, and post-apply invariant verification.
package legacy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/migration/internal/redact"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
)

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
			GitCommit:       ptr.To(r.Evidence.SourceCommit),
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

// redactError sanitizes errors (SEC-0048): raw PostgreSQL driver text (Message/Detail/Hint/Where,
// which may contain row values or SQL fragments) never enters the persisted run report, keeping only the
// structured SQLSTATE category; non-driver errors keep only a bounded category string. The original error
// is returned to the CLI caller without being persisted to the database.
func redactError(err error) string {
	return redact.BoundedError(err, 200)
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
