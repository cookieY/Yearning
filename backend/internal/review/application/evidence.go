// evidence.go Evidence read Use Cases (pre-submit draft_owner relation path;
// post-submit frozen relation path is implemented by B9 in the order domain).
// Raw Reveal is permitted only within the 7-day window (D006); no-store, watermarking, and per-access
// auditing are enforced at the HTTP layer, while the service layer handles relation checks, decryption, and window validation.
package application

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// hexOf encodes bytes as hexadecimal.
func hexOf(b []byte) string { return hex.EncodeToString(b) }

// auditInputOf assembles audit input (adapting pointer fields; empty string fields become NULL).
func auditInputOf(eventType, action, actorID, actorName, resourceType, resourceID,
	requestID, sourceIP, userAgent string, metadata map[string]any) auditlog.AuditInput {
	return auditlog.AuditInput{
		EventType: eventType, ActorKind: "user",
		ActorID:       strPtrOrNil(actorID),
		ActorUsername: strPtrOrNil(actorName),
		ResourceType:  resourceType,
		ResourceID:    strPtrOrNil(resourceID),
		Action:        action, Outcome: "succeeded",
		RequestID: strPtrOrNil(requestID),
		SourceIP:  strPtrOrNil(sourceIP),
		UserAgent: strPtrOrNil(userAgent),
		Metadata:  metadata,
	}
}

// strPtrOrNil converts an empty string to NULL (uuid/inet columns do not accept "").
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EvidenceView is the ReviewEvidence view.
type EvidenceView struct {
	ID             string `json:"id"`
	SourceKind     string `json:"source_kind"`
	SourceRef      string `json:"source_reference"`
	FactStatus     string `json:"fact_status"`
	NormalizedFact any    `json:"normalized_fact"`
	HasRawPayload  bool   `json:"has_raw_payload"`
	CollectedAt    string `json:"collected_at"`
}

// rawRevealResult holds the raw reveal response material.
type rawRevealResult struct {
	RevealID    string
	ContentType string
	RawPayload  []byte
	Watermark   string
	ValidUntil  time.Time
}

// findingOwnership resolves finding -> (stage -> run -> draft) and verifies the owner.
func (s *Services) findingOwnership(ctx context.Context, db *gorm.DB, findingID string) (*persistence.ReviewFindingModel, *persistence.StageReviewSnapshotModel, *persistence.ReviewRunModel, *persistence.ChangeDraftModel, error) {
	var f persistence.ReviewFindingModel
	if err := db.WithContext(ctx).Where("id = ?", findingID).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, nil, ErrNotFound
		}
		return nil, nil, nil, nil, err
	}
	var snap persistence.StageReviewSnapshotModel
	if err := db.WithContext(ctx).Where("id = ?", f.StageReviewID).First(&snap).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	run, err := s.loadRun(ctx, db, snap.ReviewRunID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	draft, err := s.loadDraft(ctx, db, run.DraftID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return &f, &snap, run, draft, nil
}

// ListFindingEvidence returns normalized evidence referenced by a finding (excluding raw payloads).
func (s *Services) ListFindingEvidence(ctx context.Context, actor Actor, findingID string) ([]EvidenceView, error) {
	_, snap, _, draft, err := s.findingOwnership(ctx, s.DB, findingID)
	if err != nil {
		return nil, err
	}
	if draft.OwnerUserID != actor.UserID {
		return nil, ErrNotFound
	}
	// Return only evidence referenced by this finding (linked via review_finding_evidence).
	var factIDs []string
	if err := s.DB.WithContext(ctx).Raw(
		`select evidence_fact_id::text from review_finding_evidence where finding_id = ?
		 order by evidence_fact_id`, findingID).Scan(&factIDs).Error; err != nil {
		return nil, err
	}
	var facts []persistence.EvidenceFactModel
	if len(factIDs) > 0 {
		if err := s.DB.WithContext(ctx).Where("id in ?", factIDs).
			Order("created_at, id").Find(&facts).Error; err != nil {
			return nil, err
		}
	}
	// Mark raw as available only if it exists and has not expired.
	out := make([]EvidenceView, 0, len(facts))
	for i := range facts {
		fact := &facts[i]
		var raw persistence.EvidenceRawPayloadModel
		hasRaw := false
		if err := s.DB.WithContext(ctx).Where("evidence_fact_id = ?", fact.ID).
			First(&raw).Error; err == nil {
			hasRaw = raw.ExpiresAt.After(nowUTC())
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		plaintext, err := openPayload(s.Crypter, "evidence_facts", fact.ID, "normalized_fact_ciphertext", crypto.Envelope{
			Ciphertext: fact.NormalizedFactCiphertext, Nonce: fact.NormalizedFactNonce,
			KeyID: fact.NormalizedFactKeyID, Algorithm: fact.NormalizedFactAlgorithm,
		})
		if err != nil {
			return nil, err
		}
		var normalized any
		if err := jsonUnmarshalStrict(plaintext, &normalized); err != nil {
			return nil, err
		}
		_ = snap
		out = append(out, EvidenceView{
			ID: fact.ID, SourceKind: fact.SourceKind, SourceRef: fact.SourceRef,
			FactStatus: "known", NormalizedFact: normalized,
			HasRawPayload: hasRaw, CollectedAt: fact.CreatedAt.UTC().Format(timeFormat),
		})
	}
	return out, nil
}

// evidenceOwnership resolves evidence -> fact -> stage -> run -> draft.
func (s *Services) evidenceOwnership(ctx context.Context, db *gorm.DB, evidenceID string) (*persistence.EvidenceFactModel, *persistence.ChangeDraftModel, *persistence.ReviewRunModel, error) {
	var fact persistence.EvidenceFactModel
	if err := db.WithContext(ctx).Where("id = ?", evidenceID).First(&fact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, ErrNotFound
		}
		return nil, nil, nil, err
	}
	var snap persistence.StageReviewSnapshotModel
	if err := db.WithContext(ctx).Where("id = ?", fact.StageReviewID).First(&snap).Error; err != nil {
		return nil, nil, nil, err
	}
	run, err := s.loadRun(ctx, db, snap.ReviewRunID)
	if err != nil {
		return nil, nil, nil, err
	}
	draft, err := s.loadDraft(ctx, db, run.DraftID)
	if err != nil {
		return nil, nil, nil, err
	}
	return &fact, draft, run, nil
}

// RevealRawEvidence decrypts the raw payload within the 7-day window (owner only).
// Ephemeral facts (unsubmitted precheck) take the in-memory branch: the raw envelope is sealed at stage
// commit time using the exact same sealRawEvidence convention (AAD = factID), keeping window, HMAC, and
// auditing isomorphic to the DB path.
func (s *Services) RevealRawEvidence(ctx context.Context, actor Actor, meta RequestMeta, evidenceID, purpose string) (*rawRevealResult, error) {
	if er, ef := s.Ephemeral.FindFact(evidenceID, actor.UserID); er != nil && ef != nil {
		return s.revealEphemeralRaw(ctx, actor, meta, er, ef, purpose)
	}
	fact, draft, _, err := s.evidenceOwnership(ctx, s.DB, evidenceID)
	if err != nil {
		return nil, err
	}
	if draft.OwnerUserID != actor.UserID {
		return nil, ErrNotFound
	}
	var raw persistence.EvidenceRawPayloadModel
	if err := s.DB.WithContext(ctx).Where("evidence_fact_id = ?", evidenceID).
		First(&raw).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// D006: always reject outside the window (restore replays are likewise rejected by the original timestamp).
	if !raw.ExpiresAt.After(nowUTC()) {
		return nil, &domain.Error{Code: 1002, Name: "RESOURCE_NOT_FOUND"}
	}
	// The raw envelope was sealed during the review phase with factID as AAD (the evidence_raw_payloads row
	// has no independent business primary-key semantics), so it is opened here with the same factID.
	plaintext, err := openPayload(s.Crypter, "evidence_raw_payloads", fact.ID, "raw_payload_ciphertext", crypto.Envelope{
		Ciphertext: raw.RawPayloadCiphertext, Nonce: raw.RawPayloadNonce,
		KeyID: raw.RawPayloadKeyID, Algorithm: raw.RawPayloadAlgorithm,
	})
	if err != nil {
		return nil, err
	}
	mac, err := s.Crypter.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hexOf(mac), raw.PayloadHash) {
		return nil, &domain.Error{Code: domain.CodeReviewSchemaInvalid, Name: "REVIEW_SCHEMA_INVALID"}
	}
	revealID := newID()
	now := nowUTC()
	actorID, actorName := actor.UserID, actor.Username
	resourceID := evidenceID
	requestID, sourceIP, userAgent := meta.RequestID, meta.SourceIP, meta.UserAgent
	if err := s.Recorder.Audit(ctx, nil, auditInputOf("review_evidence.raw_revealed", "reveal", actorID, actorName,
		"review_evidence", resourceID, requestID, sourceIP, userAgent, map[string]any{
			"reveal_id": revealID, "purpose": purpose,
			"fact_hash":  fact.FactHash,
			"expires_at": raw.ExpiresAt.UTC().Format(timeFormat),
		})); err != nil {
		return nil, err
	}
	return &rawRevealResult{
		RevealID:    revealID,
		ContentType: "application/json",
		RawPayload:  plaintext,
		Watermark:   buildWatermark(actor.Username, now),
		ValidUntil:  now.Add(5 * time.Minute),
	}, nil
}

// revealEphemeralRaw reveals the raw payload of an ephemeral fact (in-memory envelope; window, HMAC, and
// auditing are isomorphic to the DB path — although precheck writes zero business rows, reveal audits are still recorded).
func (s *Services) revealEphemeralRaw(ctx context.Context, actor Actor, meta RequestMeta,
	er *EphemeralRun, fact *ephemeralFact, purpose string) (*rawRevealResult, error) {
	// D006: 7-day window (measured from the fact collection timestamp).
	expiresAt := fact.CreatedAt.Add(7 * 24 * time.Hour)
	if !expiresAt.After(nowUTC()) {
		return nil, &domain.Error{Code: 1002, Name: "RESOURCE_NOT_FOUND"}
	}
	plaintext, err := openPayload(s.Crypter, "evidence_raw_payloads", fact.ID, "raw_payload_ciphertext", fact.RawEnv)
	if err != nil {
		return nil, err
	}
	mac, err := s.Crypter.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hexOf(mac), fact.RawHash) {
		return nil, &domain.Error{Code: domain.CodeReviewSchemaInvalid, Name: "REVIEW_SCHEMA_INVALID"}
	}
	revealID := newID()
	now := nowUTC()
	actorID, actorName := actor.UserID, actor.Username
	resourceID := fact.ID
	requestID, sourceIP, userAgent := meta.RequestID, meta.SourceIP, meta.UserAgent
	if err := s.Recorder.Audit(ctx, nil, auditInputOf("review_evidence.raw_revealed", "reveal", actorID, actorName,
		"review_evidence", resourceID, requestID, sourceIP, userAgent, map[string]any{
			"reveal_id": revealID, "purpose": purpose,
			"fact_hash":  fact.FactMAC,
			"expires_at": expiresAt.UTC().Format(timeFormat),
		})); err != nil {
		return nil, err
	}
	return &rawRevealResult{
		RevealID:    revealID,
		ContentType: "application/json",
		RawPayload:  plaintext,
		Watermark:   buildWatermark(actor.Username, now),
		ValidUntil:  now.Add(5 * time.Minute),
	}, nil
}

// RecordEvidenceCopy records a copy audit event (without storing the copied content).
// SEC-0035: source reveal ownership check — the referenced reveal must be a raw_revealed audit record
// issued to the current user for this evidence, preventing cross-user or cross-evidence audit chain spoofing via arbitrary reveal_id.
func (s *Services) RecordEvidenceCopy(ctx context.Context, actor Actor, meta RequestMeta, evidenceID, sourceRevealID string) error {
	// Ephemeral fact: ownership is determined in memory (owner filter); reveal ownership verification still
	// queries the audit row (ephemeral reveals also write audit rows, so verifyEvidenceRevealOwnership is isomorphic for both).
	if er, _ := s.Ephemeral.FindFact(evidenceID, actor.UserID); er == nil {
		_, draft, _, err := s.evidenceOwnership(ctx, s.DB, evidenceID)
		if err != nil {
			return err
		}
		if draft.OwnerUserID != actor.UserID {
			return ErrNotFound
		}
	}
	if err := s.verifyEvidenceRevealOwnership(ctx, actor.UserID, evidenceID, sourceRevealID); err != nil {
		return err
	}
	actorID, actorName := actor.UserID, actor.Username
	resourceID := evidenceID
	requestID, sourceIP, userAgent := meta.RequestID, meta.SourceIP, meta.UserAgent
	return s.Recorder.Audit(ctx, nil, auditInputOf("review_evidence.raw_copied", "copy", actorID, actorName,
		"review_evidence", resourceID, requestID, sourceIP, userAgent, map[string]any{
			"source_reveal_id": sourceRevealID,
		}))
}

// verifyEvidenceRevealOwnership verifies that sourceRevealID was issued by a review_evidence.raw_revealed
// audit record for this user and this evidence — the resource_id of a reveal audit is the evidence_id,
// so this query simultaneously binds actor and evidence (non-existent / other user / other evidence ->
// ErrNotFound mapped to 1002 without leaking existence).
func (s *Services) verifyEvidenceRevealOwnership(ctx context.Context, userID, evidenceID, revealID string) error {
	if revealID == "" {
		return ErrNotFound
	}
	var n int64
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from audit_events
		where event_type = 'review_evidence.raw_revealed'
		  and actor_kind = 'user' and actor_id = ?
		  and resource_type = 'review_evidence' and resource_id = ?
		  and metadata->>'reveal_id' = ?`,
		userID, evidenceID, revealID).Scan(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
