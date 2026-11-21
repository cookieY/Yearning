package privacy

import (
	"fmt"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// EnvelopeBinding identifies the Review Run and 1-based statement index authenticated by the AEAD AAD.
type EnvelopeBinding struct {
	ReviewRunID  string
	StatementIdx int
}

// MappingTable and MappingColumn define the fixed AEAD AAD target table and column constants.
const (
	MappingTable  = "review_identifier_mappings"
	MappingColumn = "identifier_mapping"
)

// SealMapping encrypts identifier mapping pairs into an AEAD envelope bound to the Review Run and statement.
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

// OpenMapping decrypts and decodes an identifier mapping AEAD envelope after verifying its AAD binding.
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
