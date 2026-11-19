package rollback

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

const (
	EvidenceTable      = "order_rollback_evidences"
	EvidenceColumn     = "rollback_payload_ciphertext"
	RollbackHMACDomain = "sql_fingerprint"
)

// AssembleOrderEvidenceBundle assembles the whole-order rollback EvidenceBundle and complete rollback SQL in reverse statement execution order.
func AssembleOrderEvidenceBundle(operationType string, stmts []StatementEvidence) *EvidenceBundle {
	var all []string
	for i := len(stmts) - 1; i >= 0; i-- {
		all = append(all, stmts[i].InverseStatements...)
	}
	return &EvidenceBundle{
		Statements:         stmts,
		RollbackStatements: all,
		RollbackSQL:        strings.Join(all, "\n"),
	}
}

// SealEvidenceBundle serializes the EvidenceBundle, encrypts it in an AES-256-GCM envelope, and computes the HMAC of RollbackSQL.
func SealEvidenceBundle(crypter *crypto.Crypter, evidenceID string, bundle *EvidenceBundle) (crypto.Envelope, string, error) {
	if crypter == nil {
		return crypto.Envelope{}, "", errors.New("rollback: crypter is nil")
	}
	if bundle == nil {
		bundle = &EvidenceBundle{}
	}
	if len(bundle.RollbackStatements) == 0 && len(bundle.Statements) > 0 {
		var all []string
		for i := len(bundle.Statements) - 1; i >= 0; i-- {
			all = append(all, bundle.Statements[i].InverseStatements...)
		}
		bundle.RollbackStatements = all
	}
	if bundle.RollbackSQL == "" && len(bundle.RollbackStatements) > 0 {
		bundle.RollbackSQL = strings.Join(bundle.RollbackStatements, "\n")
	}
	bundle.RollbackSQL = strings.TrimSpace(bundle.RollbackSQL)
	raw, err := json.Marshal(bundle)
	if err != nil {
		return crypto.Envelope{}, "", err
	}
	aad := crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        EvidenceTable,
		PrimaryKey:   evidenceID,
		Column:       EvidenceColumn,
		Purpose:      crypto.PurposePayload,
	}
	env, err := crypter.Seal(crypto.PurposePayload, raw, aad)
	if err != nil {
		return crypto.Envelope{}, "", err
	}
	macBytes, err := crypter.HMAC(RollbackHMACDomain, []byte(bundle.RollbackSQL))
	if err != nil {
		return crypto.Envelope{}, "", err
	}
	return env, hex.EncodeToString(macBytes), nil
}

// OpenEvidenceBundle decrypts and validates the rollback evidence bundle stored in order_rollback_evidences.
func OpenEvidenceBundle(crypter *crypto.Crypter, evidenceID string, env crypto.Envelope, expectedHMAC string) (*EvidenceBundle, error) {
	if crypter == nil {
		return nil, errors.New("rollback: crypter is nil")
	}
	aad := crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        EvidenceTable,
		PrimaryKey:   evidenceID,
		Column:       EvidenceColumn,
		Purpose:      crypto.PurposePayload,
	}
	plain, err := crypter.Open(crypto.PurposePayload, env, aad)
	if err != nil {
		return nil, err
	}
	var bundle EvidenceBundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		return nil, err
	}
	if expectedHMAC != "" {
		macBytes, err := crypter.HMAC(RollbackHMACDomain, []byte(bundle.RollbackSQL))
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(hex.EncodeToString(macBytes), expectedHMAC) {
			return nil, errors.New("rollback: rollback_sql_hmac integrity check failed")
		}
	}
	return &bundle, nil
}
