package privacy

import (
	"fmt"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// Package agent constructs the context and requests for controlled model review (PRD 9.1 agent: model loop and tool scheduling).
//
// Three categories of review inputs converge deterministically during request assembly:
//  1. Toolset evidence collection (normalized Evidence references, never carrying raw payloads);
//  2. Audit skills (enabled Prompt Tools bound to the rule set, injected via knowledge_text);
//  3. Internal experience (scope-matched knowledge entries, injected and rendered).
//
// Lexical deterministic rules (rule_sets.config) are executed by code and never enter the model context.
type EnvelopeBinding struct {
	ReviewRunID  string
	StatementIdx int
}

// RuntimeBudget defines the technical safety boundaries for each Run (PRD 9.5; no admin-configurable per-order token or cost budget is provided).
const (
	MappingTable  = "review_identifier_mappings"
	MappingColumn = "identifier_mapping"
)

// seconds
func SealMapping(c *crypto.Crypter, binding EnvelopeBinding, pairs []MappingPair) (crypto.Envelope, error) {
	if c == nil {
		return crypto.Envelope{}, fmt.Errorf("privacy: nil crypter")
	}
	if binding.ReviewRunID == "" {
		return crypto.Envelope{}, fmt.Errorf("privacy: empty review run id")
	}
	aad := crypto.AAD{
		DeploymentID: c.DeploymentID(),
		Table:        MappingTable,
		PrimaryKey:   fmt.Sprintf("%s#%d", binding.ReviewRunID, binding.StatementIdx),
		Column:       MappingColumn,
		Purpose:      crypto.PurposePayload,
	}
	return c.Seal(crypto.PurposePayload, MarshalMapping(pairs), aad)
}

// seconds
func OpenMapping(c *crypto.Crypter, binding EnvelopeBinding, env crypto.Envelope) ([]MappingPair, error) {
	if c == nil {
		return nil, fmt.Errorf("privacy: nil crypter")
	}
	if binding.ReviewRunID == "" {
		return nil, fmt.Errorf("privacy: empty review run id")
	}
	if binding.StatementIdx < 1 {
		return nil, fmt.Errorf("privacy: statement index must be positive")
	}
	aad := crypto.AAD{
		DeploymentID: c.DeploymentID(),
		Table:        MappingTable,
		PrimaryKey:   fmt.Sprintf("%s#%d", binding.ReviewRunID, binding.StatementIdx),
		Column:       MappingColumn,
		Purpose:      crypto.PurposePayload,
	}
	plain, err := c.Open(crypto.PurposePayload, env, aad)
	if err != nil {
		return nil, err
	}
	return UnmarshalMapping(plain)
}
