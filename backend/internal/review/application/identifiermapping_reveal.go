package application

import (
	"context"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// identifierMappingRevealMaxTokens caps the number of tokens per reveal request,
// preventing bulk export of the entire mapping in one shot (the "all" mode is equally bounded by total mapping size).
const identifierMappingRevealMaxTokens = 200

// identifierMappingRevealResult holds the reveal response (plaintext mappings exist only in this struct
// and the HTTP response, never written to logs, Traces, or model context).
type identifierMappingRevealResult struct {
	RunID      string
	Mappings   []identifierMappingRevealed
	RevealedAt string
}

type identifierMappingRevealed struct {
	Token      string `json:"token"`
	Kind       string `json:"kind"`
	Identifier string `json:"identifier"`
}

// RevealIdentifierMapping decrypts anonymous identifier mappings for a Run (RCP-20261022-IDENTIFIER-MAPPING-REVEAL,
// approved by owner on 2026-10-29). Authorization follows the exact same convention as raw reveal: draft owner only,
// while non-owners uniformly receive RESOURCE_NOT_FOUND (never leaking Run existence). Every invocation audits
// review_evidence.mapping_revealed; plaintext mappings must never enter logs or model context.
func (s *Services) RevealIdentifierMapping(ctx context.Context, actor Actor, meta RequestMeta, runID string, tokens []string, all bool) (*identifierMappingRevealResult, error) {
	run, err := s.loadRun(ctx, s.DB, runID)
	if err != nil {
		return nil, err
	}
	draft, err := s.loadDraft(ctx, s.DB, run.DraftID)
	if err != nil {
		return nil, err
	}
	if draft.OwnerUserID != actor.UserID {
		return nil, ErrNotFound
	}
	if !all && len(tokens) == 0 {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if len(tokens) > identifierMappingRevealMaxTokens {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}

	var row struct {
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Algorithm  string
	}
	err = s.DB.WithContext(ctx).Raw(`
		select identifier_mapping_ciphertext as ciphertext,
		       identifier_mapping_nonce as nonce,
		       identifier_mapping_key_id as key_id,
		       identifier_mapping_algorithm as algorithm
		from review_identifier_mappings
		where review_run_id = ? and statement_idx = ?`,
		runID, mappingEnvelopeStatementIdx).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.Ciphertext == nil {
		return nil, ErrNotFound
	}
	binding := privacy.EnvelopeBinding{ReviewRunID: runID, StatementIdx: mappingEnvelopeStatementIdx}
	pairs, err := privacy.OpenMapping(s.Crypter, binding, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce,
		KeyID: row.KeyID, Algorithm: row.Algorithm,
	})
	if err != nil {
		// Envelope integrity failure (AAD mismatch / unknown key) is rejected under the controlled vocabulary's integrity semantics.
		return nil, &domain.Error{Code: domain.CodeReviewSchemaInvalid, Name: "REVIEW_SCHEMA_INVALID"}
	}

	byToken := make(map[string]privacy.MappingPair, len(pairs))
	for _, p := range pairs {
		byToken[p.Token] = p
	}
	var selected []privacy.MappingPair
	if all {
		selected = pairs
	} else {
		selected = make([]privacy.MappingPair, 0, len(tokens))
		for _, t := range tokens {
			p, ok := byToken[t]
			if !ok {
				// Fail-closed: any unknown token in the requested set rejects the entire request without returning partial mappings.
				return nil, ErrNotFound
			}
			selected = append(selected, p)
		}
	}

	out := make([]identifierMappingRevealed, 0, len(selected))
	for _, p := range selected {
		out = append(out, identifierMappingRevealed{Token: p.Token, Kind: string(p.Kind), Identifier: p.Original})
	}
	now := nowUTC()
	actorID, actorName := actor.UserID, actor.Username
	resourceID := runID
	requestID, sourceIP, userAgent := meta.RequestID, meta.SourceIP, meta.UserAgent
	if err := s.Recorder.Audit(ctx, nil, auditInputOf(
		"review_evidence.mapping_revealed", "reveal", actorID, actorName,
		"review_run", resourceID, requestID, sourceIP, userAgent, map[string]any{
			"requested_form": map[bool]string{true: "all", false: "tokens"}[all],
			"token_count":    len(out),
			"mapping_rows":   len(pairs),
		})); err != nil {
		return nil, err
	}
	return &identifierMappingRevealResult{
		RunID:      runID,
		Mappings:   out,
		RevealedAt: now.Format(timeFormat),
	}, nil
}
