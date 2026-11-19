// ephemeral_materialize.go Submission-time materialization: batch-inserts the in-memory models of an
// ephemeral Run into the caller's transaction under the existing table schemas and constraints
// (RCP-20261101-EPHEMERAL-PRECHECK — submit = sole persistence point for review artifacts). Thereafter,
// approval/audit/evidence read surfaces are completely isomorphic to existing orders (order_stages -> snapshot pointers -> live rows).
package application

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MaterializeEphemeralRunTx materializes an ephemeral Run inside the submission transaction. draftID/draftRevision
// are supplied by the submitter (trial Runs only learn their attached draft at submit time); row values are column-for-column isomorphic to the DB path.
func (s *Services) MaterializeEphemeralRunTx(ctx context.Context, tx *gorm.DB, er *EphemeralRun, draftID string, draftRevision int64) error {
	er.mu.RLock()
	defer er.mu.RUnlock()
	now := nowUTC()
	run := er.Run
	run.DraftID = draftID
	run.DraftRevision = draftRevision
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		insert into review_run_sql_bindings (review_run_id, sql_payload_hmac, created_at)
		values (?, ?, ?)
		on conflict (review_run_id) do nothing`, er.ID, er.SQLHMAC, now).Error; err != nil {
		return err
	}
	// Identifier mapping envelope (isomorphic to existing read surfaces / display.Load).
	if err := persistIdentifierMappingTx(ctx, tx, s.Crypter, er.ID, er.Mapper); err != nil {
		return err
	}
	if len(er.Statements) > 0 {
		if err := tx.WithContext(ctx).CreateInBatches(er.Statements, 512).Error; err != nil {
			return err
		}
	}
	for i := range er.Groups {
		if err := tx.WithContext(ctx).Create(&er.Groups[i]).Error; err != nil {
			return err
		}
	}
	for i := range er.Snapshots {
		if err := tx.WithContext(ctx).Create(&er.Snapshots[i]).Error; err != nil {
			return err
		}
	}
	for i := range er.Facts {
		f := &er.Facts[i]
		if err := tx.WithContext(ctx).Exec(`
			insert into evidence_facts (id, stage_review_id, source_kind, source_ref,
				normalized_fact_ciphertext, normalized_fact_nonce, normalized_fact_key_id,
				normalized_fact_algorithm, fact_hash, created_at)
			values (?, ?, 'database_tool', ?, ?, ?, ?, 'aes-256-gcm', ?, ?)`,
			f.ID, f.SnapID, f.SourceRef,
			f.FactEnv.Ciphertext, f.FactEnv.Nonce, f.FactEnv.KeyID, f.FactMAC, f.CreatedAt).Error; err != nil {
			return err
		}
		rawExpires := f.CreatedAt.Add(7 * 24 * time.Hour)
		if rawExpires.Before(now) {
			rawExpires = now.Add(7 * 24 * time.Hour)
		}
		if err := tx.WithContext(ctx).Exec(`
			insert into evidence_raw_payloads (id, evidence_fact_id, raw_payload_ciphertext,
				raw_payload_nonce, raw_payload_key_id, raw_payload_algorithm, payload_hash,
				created_at, expires_at)
			values (?, ?, ?, ?, ?, 'aes-256-gcm', ?, ?, ?)`,
			newID(), f.ID, f.RawEnv.Ciphertext, f.RawEnv.Nonce, f.RawEnv.KeyID, f.RawHash,
			f.CreatedAt, rawExpires).Error; err != nil {
			return err
		}
	}
	if len(er.ToolCalls) > 0 {
		if err := tx.WithContext(ctx).CreateInBatches(er.ToolCalls, 512).Error; err != nil {
			return err
		}
	}
	for _, f := range er.Findings {
		if err := tx.WithContext(ctx).Exec(`
			insert into review_findings (id, stage_review_id, fingerprint_group_id, category,
				severity, title, message, suggestion, created_at)
			values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.SnapID, f.GroupID, f.Category, f.Severity,
			f.Title, f.Message, f.Suggestion, f.CreatedAt).Error; err != nil {
			return err
		}
		for _, eid := range f.Evidence {
			if err := tx.WithContext(ctx).Exec(`
				insert into review_finding_evidence (finding_id, evidence_fact_id) values (?, ?)`,
				f.ID, eid).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// PersistDraftSQLPayloadTx writes the draft SQL payload inside the submission transaction (RCP-20261101:
// submit carries the current SQL, so persistence point = submit). Does not bump revision — revision advances
// only on "Save and Return" (ReplaceSQL); after submit, the draft is terminal and no longer editable.
// Returns the plaintext HMAC for the caller to compare against the reviewed HMAC (preventing review-A-submit-B).
func (s *Services) PersistDraftSQLPayloadTx(ctx context.Context, tx *gorm.DB, draftID string, sqlText []byte, statementCount int32) (string, error) {
	env, mac, err := sealPayload(s.Crypter, "draft_sql_payloads", draftID, "sql_ciphertext", sqlText)
	if err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`
		delete from draft_sql_payloads where draft_id = ?`, draftID).Error; err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`
		insert into draft_sql_payloads (draft_id, sql_ciphertext, sql_nonce, sql_key_id,
			sql_algorithm, plaintext_hmac, size_bytes, statement_count, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		draftID, env.Ciphertext, env.Nonce, env.KeyID, env.Algorithm, mac,
		int32(len(sqlText)), statementCount, nowUTC()).Error; err != nil {
		return "", err
	}
	return mac, nil
}
