// identifiermapping.go NEW-AI-01 fix: local AEAD envelope persistence wiring for Review Run
// anonymous identifier mappings.
//
// Contract (api/contracts/ai-review-runtime-contract.json input_privacy
// .identifier_mapping_storage=local_AEAD_envelope_never_provider;
// sql-fingerprint.json privacy.identifier_mapping_storage=
// encrypted_and_bound_to_review_run, read-only): the Run-scoped tokenization delivered by SEC-0002
// previously existed only in the in-process RunMapper — lost as soon as the Run ended, breaking cross-Run
// token semantic continuity and leaving the contract-declared local mapping storage unwired (dead code).
//
// This file wires privacy.SealMapping/OpenMapping into the review Runner lifecycle:
//   - Run startup (including a new attempt on crash replay) first rehydrates persisted mappings —
//     token continuity is a prerequisite for explainable evidence;
//   - After each stage's facts are persisted, the current RunMapper snapshot is idempotently persisted
//     to review_identifier_mappings (envelope AAD binds "{review_run_id}#{statement_idx}", see
//     mappingEnvelopeStatementIdx for the sentinel statement index);
//   - Repeated sealing is idempotent: zero writes on identical snapshot; a superset snapshot (new tokens
//     allocated in the current attempt) is decrypted, merged, and resealed; any conflict with stored
//     mappings (same name with different token, or a stored entry missing from the current snapshot)
//     always fails closed without overwriting.
//
// R014 has wired automatic restoration for summaries and Findings into the display component and
// authorized read surfaces. Evidence/export and full mapping reveal remain out of scope; see runtime contract result_identifier_display.
package application

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// mappingEnvelopeStatementIdx is the statement index sentinel for Run-scoped mapping envelopes:
// privacy.OpenMapping requires statement_idx >= 1, so Run-scoped mappings (one token space per Run)
// use the fixed value 1; the AAD primary-key shape "{run_id}#1" matches the migration comment.
const mappingEnvelopeStatementIdx = 1

// rehydrateIdentifierMapping attempts to load persisted identifier mappings for this Run
// (restoring token continuity on crash replay). A no-op when no record exists (first attempt);
// returns an error on envelope decryption failure or malformed payload — failing closed because new
// tokens without continuity could collide with existing tokens and must not proceed to overwrite storage.
func (r *AgentRunner) rehydrateIdentifierMapping(ctx context.Context, db *gorm.DB, runID string, mapper *privacy.RunMapper) error {
	var row struct {
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Algorithm  string
	}
	err := db.WithContext(ctx).Raw(`
		select identifier_mapping_ciphertext as ciphertext, identifier_mapping_nonce as nonce,
		       identifier_mapping_key_id as key_id, identifier_mapping_algorithm as algorithm
		from review_identifier_mappings
		where review_run_id = ? and statement_idx = ?`, runID, mappingEnvelopeStatementIdx).
		Scan(&row).Error
	if err != nil {
		return err
	}
	if row.KeyID == "" {
		return nil // First attempt: no stored mapping yet
	}
	pairs, err := privacy.OpenMapping(r.svc.Crypter, privacy.EnvelopeBinding{
		ReviewRunID: runID, StatementIdx: mappingEnvelopeStatementIdx,
	}, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: row.KeyID, Algorithm: row.Algorithm,
	})
	if err != nil {
		return fmt.Errorf("review: reopen stored identifier mapping: %w", err)
	}
	return mapper.Restore(pairs)
}

// persistIdentifierMapping idempotently persists the Run-scoped mapping envelope from the current
// RunMapper snapshot (called after stage facts are persisted; NEW-AI-01).
//   - Empty mapper (no tokenization has occurred in this Run yet) is skipped — no row created for zero mappings;
//   - First persistence: inserts a SealMapping envelope (AAD binds Run + sentinel statement index);
//   - Already exists: row-locked read inside transaction -> decrypt -> merge with current snapshot ->
//     reseal only when changed (idempotent across crash replay/retries, never losing tokens from prior attempts);
//   - Merge conflicts (same name with different token, or stored entry missing from current snapshot)
//     fail closed and return an error without overwriting the stored envelope.
func (r *AgentRunner) persistIdentifierMapping(ctx context.Context, db *gorm.DB, runID string, mapper *privacy.RunMapper) error {
	if mapper == nil || mapper.Len() == 0 {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return persistIdentifierMappingTx(ctx, tx, r.svc.Crypter, runID, mapper)
	})
}

// persistIdentifierMappingTx idempotently writes the Run-scoped mapping envelope inside a transaction
// already opened by the caller. The grouping phase uses this to bind the Run, statements, groups, and
// restorable tokens into a single atomic watermark; the AgentRunner phase wraps it via persistIdentifierMapping.
func persistIdentifierMappingTx(
	ctx context.Context,
	tx *gorm.DB,
	crypter *crypto.Crypter,
	runID string,
	mapper *privacy.RunMapper,
) error {
	if mapper == nil || mapper.Len() == 0 {
		return nil
	}
	binding := privacy.EnvelopeBinding{ReviewRunID: runID, StatementIdx: mappingEnvelopeStatementIdx}
	var existing struct {
		ID         string
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Algorithm  string
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as id, identifier_mapping_ciphertext as ciphertext,
		       identifier_mapping_nonce as nonce, identifier_mapping_key_id as key_id,
		       identifier_mapping_algorithm as algorithm
		from review_identifier_mappings
		where review_run_id = ? and statement_idx = ? for update`,
		runID, mappingEnvelopeStatementIdx).Scan(&existing).Error; err != nil {
		return err
	}
	current := mapper.Snapshot()
	if existing.ID == "" {
		env, err := privacy.SealMapping(crypter, binding, current)
		if err != nil {
			return err
		}
		res := tx.WithContext(ctx).Exec(`
			insert into review_identifier_mappings (id, review_run_id, statement_idx,
				identifier_mapping_ciphertext, identifier_mapping_nonce,
				identifier_mapping_key_id, identifier_mapping_algorithm,
				mapping_entry_count, created_at)
			values (?, ?, ?, ?, ?, ?, 'aes-256-gcm', ?, ?)`,
			newID(), runID, mappingEnvelopeStatementIdx,
			env.Ciphertext, env.Nonce, env.KeyID, len(current), nowUTC())
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConcurrent
		}
		return nil
	}
	stored, err := privacy.OpenMapping(crypter, binding, crypto.Envelope{
		Ciphertext: existing.Ciphertext, Nonce: existing.Nonce,
		KeyID: existing.KeyID, Algorithm: existing.Algorithm,
	})
	if err != nil {
		// Key rotation / corrupted envelope: preserve the old envelope (whose information is non-renewable)
		// and fail closed — never overwrite with an incomplete snapshot.
		return fmt.Errorf("review: open stored identifier mapping: %w", err)
	}
	merged, changed, err := mergeMappingPairs(stored, current)
	if err != nil {
		return err
	}
	if !changed {
		return nil // Idempotent: zero writes when retrying with an identical snapshot
	}
	env, err := privacy.SealMapping(crypter, binding, merged)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		update review_identifier_mappings set identifier_mapping_ciphertext = ?,
			identifier_mapping_nonce = ?, identifier_mapping_key_id = ?,
			mapping_entry_count = ?
		where id = ?`,
		env.Ciphertext, env.Nonce, env.KeyID, len(merged), existing.ID).Error
}

// mergeMappingPairs merges stored mappings with the current snapshot (the pure-function core of
// NEW-AI-01 idempotent resealing):
//   - Every stored entry must appear verbatim in the current snapshot (replay attempts have already
//     Restored stored state, keeping tokens consistent); missing entries or token drift always return
//     an error — overwriting would permanently lose identifier restoration capability, so fail instead;
//   - New entries in the current snapshot (identifiers newly tokenized in this attempt) are appended
//     in order, setting changed=true whenever any new entry is present;
//   - When the current snapshot matches the stored mapping pair-for-pair, changed=false (idempotent, skipping reseal).
func mergeMappingPairs(stored, current []privacy.MappingPair) ([]privacy.MappingPair, bool, error) {
	storedByKey := make(map[string]privacy.MappingPair, len(stored))
	for _, p := range stored {
		storedByKey[string(p.Kind)+"\x00"+p.Scope+"\x00"+p.Original] = p
	}
	merged := make([]privacy.MappingPair, 0, len(stored)+len(current))
	matched, changed := 0, false
	for _, p := range current {
		if s, ok := storedByKey[string(p.Kind)+"\x00"+p.Scope+"\x00"+p.Original]; ok {
			if s.Token != p.Token {
				return nil, false, fmt.Errorf(
					"review: stored identifier mapping token conflict for %s identifier (value suppressed)", p.Kind)
			}
			matched++
		} else {
			changed = true
		}
		merged = append(merged, p)
	}
	if matched < len(stored) {
		return nil, false, fmt.Errorf("review: stored identifier mapping entries missing from current snapshot")
	}
	return merged, changed, nil
}
