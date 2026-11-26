package crypto

import "errors"

// Purpose identifies the domain/column AAD purpose bound into authenticated envelopes.
// All column envelopes encrypt under the keyring's PurposePayload AEAD key while binding
// Purpose into AAD so ciphertexts cannot be substituted across domains.
type Purpose string

// Domain AAD purpose constants used across column envelopes.
const (
	PurposeProviderSecret               = "provider_secret"
	PurposeDatasourceCredential         = "datasource_credential"
	PurposeDatasourceTLS                = "datasource_tls"
	PurposeDiagnosticsAIReport          = "diagnostics_ai_report"
	PurposeDiagnosticsCandidateSQL      = "diagnostics_candidate_sql"
	PurposeDiagnosticsStatementTemplate = "diagnostics_statement_template"
	PurposeFlowSchemaMapping            = "flow_schema_mapping"
)

// ErrNilCrypter indicates SealColumn or OpenColumn was called on a nil Crypter.
var ErrNilCrypter = errors.New("crypto: nil crypter")

// ColumnRef identifies the database table, row primary key, and logical column bound into AAD.
type ColumnRef struct {
	Table      string
	PrimaryKey string
	Column     string
}

// ColumnAAD constructs the canonical AAD for a column envelope bound to deploymentID, purpose, and ref.
func ColumnAAD(deploymentID string, purpose Purpose, ref ColumnRef) AAD {
	return AAD{
		DeploymentID: deploymentID,
		Table:        ref.Table,
		PrimaryKey:   ref.PrimaryKey,
		Column:       ref.Column,
		Purpose:      string(purpose),
	}
}

// SealColumn encrypts plaintext under PurposePayload with AAD bound to c.DeploymentID(), purpose, and ref.
func (c *Crypter) SealColumn(purpose Purpose, ref ColumnRef, plaintext []byte) (Envelope, error) {
	if c == nil || c.ring == nil {
		return Envelope{}, ErrNilCrypter
	}
	return c.Seal(PurposePayload, plaintext, ColumnAAD(c.DeploymentID(), purpose, ref))
}

// OpenColumn authenticates and decrypts a column envelope bound to c.DeploymentID(), purpose, and ref.
func (c *Crypter) OpenColumn(purpose Purpose, ref ColumnRef, env Envelope) ([]byte, error) {
	if c == nil || c.ring == nil {
		return nil, ErrNilCrypter
	}
	return c.Open(PurposePayload, env, ColumnAAD(c.DeploymentID(), purpose, ref))
}
