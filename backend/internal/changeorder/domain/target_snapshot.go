package domain

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

const (
	// TargetSnapshotTable names the table bound into order_stages.target_snapshot_ciphertext AAD.
	TargetSnapshotTable = "order_stages"
	// TargetSnapshotColumn names the column bound into order_stages.target_snapshot_ciphertext AAD.
	TargetSnapshotColumn = "target_snapshot_ciphertext"
)

// TargetSnapshot is the encrypted stage datasource target snapshot persisted on
// order_stages.target_snapshot_ciphertext across submission, rollback draft generation,
// and execution preflight.
type TargetSnapshot struct {
	DatasourceID        string `json:"datasource_id"`
	Engine              string `json:"engine"`
	IdentityFingerprint string `json:"identity_fingerprint"`
	TargetDatabase      string `json:"target_database,omitempty"`
}

func targetSnapshotRef(stageID string) crypto.ColumnRef {
	return crypto.ColumnRef{
		Table:      TargetSnapshotTable,
		PrimaryKey: stageID,
		Column:     TargetSnapshotColumn,
	}
}

// TargetSnapshotAAD returns the canonical authenticated additional data for
// order_stages.target_snapshot_ciphertext.
func TargetSnapshotAAD(deploymentID, stageID string) crypto.AAD {
	return crypto.ColumnAAD(deploymentID, crypto.PurposePayload, targetSnapshotRef(stageID))
}

// SealTargetSnapshot serializes and seals a stage TargetSnapshot envelope.
func SealTargetSnapshot(crypter *crypto.Crypter, stageID string, snap TargetSnapshot) (crypto.Envelope, error) {
	if crypter == nil {
		return crypto.Envelope{}, errors.New("changeorder: target snapshot crypter is required")
	}
	snap.TargetDatabase = strings.TrimSpace(snap.TargetDatabase)
	raw, err := json.Marshal(snap)
	if err != nil {
		return crypto.Envelope{}, err
	}
	return crypter.SealColumn(crypto.PurposePayload, targetSnapshotRef(stageID), raw)
}

// OpenTargetSnapshot authenticates, decrypts, and decodes a stage TargetSnapshot envelope.
func OpenTargetSnapshot(crypter *crypto.Crypter, stageID string, env crypto.Envelope) (TargetSnapshot, error) {
	if crypter == nil {
		return TargetSnapshot{}, errors.New("changeorder: target snapshot crypter is required")
	}
	plain, err := crypter.OpenColumn(crypto.PurposePayload, targetSnapshotRef(stageID), env)
	if err != nil {
		return TargetSnapshot{}, err
	}
	var snap TargetSnapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		return TargetSnapshot{}, err
	}
	return snap, nil
}
