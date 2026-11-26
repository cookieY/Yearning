// session_knowledge.go handles default-track desensitization of internal experience notes
// without registering thresholds/enums into the turn placeholder map.
package privacy

import (
	"errors"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// ErrKnowledgeRefOutOfScope indicates an internal experience entry references an object outside
// the current turn's authorized schema/table/column/index scope and should be skipped safely.
var ErrKnowledgeRefOutOfScope = errors.New("privacy: knowledge policy reference out of authorized scope")

// AnonymizeKnowledgeEntryText desensitizes an internal experience entry for default-profile endpoints:
//  1. Rejects credential patterns fail-closed (6003);
//  2. Resolves {{ref:...}} typed references to this turn's anonymous tokens (returning ErrKnowledgeRefOutOfScope if outside authorized scope);
//  3. Scrubs registered object identifiers and same-schema un-described catalog tables without polluting bound_placeholders;
//  4. Asserts zero leakage against all turn-forbidden values (6003).
func (s *TurnPrivacySession) AnonymizeKnowledgeEntryText(text string, extraCatalogTables []string) (string, error) {
	if s == nil {
		return "", domain.ErrAnonymizationFailed("privacy_session_unavailable")
	}
	if credentialLeakRe.MatchString(text) {
		return "", domain.ErrAnonymizationFailed("credential_detected_in_knowledge")
	}
	if strings.TrimSpace(text) == "" {
		return "", nil
	}

	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return "", domain.ErrAnonymizationFailed("session_released")
	}

	out := text
	if strings.Contains(out, "{{ref:") {
		rewritten, err := revprivacy.RewritePolicyReferences(out, func(ref revprivacy.PolicyObjectReference) (string, error) {
			return s.resolvePolicyRefLocked(ref)
		})
		if err != nil {
			s.mu.Unlock()
			return "", ErrKnowledgeRefOutOfScope
		}
		out = rewritten
	}
	out = s.scrubRegisteredLocked(out)
	out = s.scrubExtraCatalogTablesLocked(out, extraCatalogTables)
	s.mu.Unlock()

	if err := s.AssertZeroLeak(out); err != nil {
		return "", err
	}
	return out, nil
}

func (s *TurnPrivacySession) resolvePolicyRefLocked(ref revprivacy.PolicyObjectReference) (string, error) {
	schOrig, ok := s.authSchemas[strings.ToLower(strings.TrimSpace(ref.Schema))]
	if !ok || schOrig == "" {
		return "", ErrKnowledgeRefOutOfScope
	}
	schTok := s.mapper.Lookup(revprivacy.KindSchema, schOrig)
	if schTok == "" {
		return "", ErrKnowledgeRefOutOfScope
	}
	if ref.Kind == revprivacy.PolicyReferenceSchema {
		return schTok, nil
	}
	tblLow := strings.ToLower(strings.TrimSpace(ref.Relation))
	tblOrig, ok := s.authTables[tblLow]
	if !ok || tblOrig == "" {
		return "", ErrKnowledgeRefOutOfScope
	}
	tblTok := s.mapper.Lookup(revprivacy.KindTable, tblOrig)
	if tblTok == "" {
		return "", ErrKnowledgeRefOutOfScope
	}
	switch ref.Kind {
	case revprivacy.PolicyReferenceTable, revprivacy.PolicyReferenceView:
		return schTok + "." + tblTok, nil
	case revprivacy.PolicyReferenceColumn:
		colLow := strings.ToLower(strings.TrimSpace(ref.Name))
		colOrig := ""
		if cols := s.authColumns[tblLow]; cols != nil {
			colOrig = cols[colLow]
		}
		if colOrig == "" {
			return "", ErrKnowledgeRefOutOfScope
		}
		colTok := s.mapper.Lookup(revprivacy.KindColumn, colOrig)
		if colTok == "" {
			return "", ErrKnowledgeRefOutOfScope
		}
		return schTok + "." + tblTok + "." + colTok, nil
	case revprivacy.PolicyReferenceIndex, revprivacy.PolicyReferenceConstraint:
		idxOrig, ok := s.authIndexes[strings.ToLower(strings.TrimSpace(ref.Name))]
		if !ok || idxOrig == "" {
			return "", ErrKnowledgeRefOutOfScope
		}
		idxTok := s.mapper.Lookup(revprivacy.KindIndex, idxOrig)
		if idxTok == "" {
			return "", ErrKnowledgeRefOutOfScope
		}
		return schTok + "." + tblTok + "." + idxTok, nil
	default:
		return "", ErrKnowledgeRefOutOfScope
	}
}

func (s *TurnPrivacySession) scrubExtraCatalogTablesLocked(out string, extraCatalogTables []string) string {
	if len(extraCatalogTables) == 0 || out == "" {
		return out
	}
	cleanTables := make([]string, 0, len(extraCatalogTables))
	seen := make(map[string]bool, len(extraCatalogTables))
	for _, tbl := range extraCatalogTables {
		trimmed := strings.TrimSpace(tbl)
		if trimmed == "" {
			continue
		}
		low := strings.ToLower(trimmed)
		if seen[low] || s.authTables[low] != "" {
			continue
		}
		seen[low] = true
		cleanTables = append(cleanTables, trimmed)
	}
	sort.Slice(cleanTables, func(i, j int) bool {
		if len(cleanTables[i]) != len(cleanTables[j]) {
			return len(cleanTables[i]) > len(cleanTables[j])
		}
		return cleanTables[i] < cleanTables[j]
	})
	for _, tbl := range cleanTables {
		out = replaceAsciiIdentifierBoundary(out, tbl, "[redacted_table]")
	}
	return out
}
