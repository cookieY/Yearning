// service_credentials.go holds service domain helpers and operations.
package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"gorm.io/gorm"
	"strings"
	"time"
)

// UnmarshalJSON distinguishes omitted (not called), null, and object states.
func (f *TLSWriteField) UnmarshalJSON(b []byte) error {
	f.Set = true
	if string(b) == "null" {
		f.Null = true
		f.Value = nil
		return nil
	}
	f.Null = false
	return json.Unmarshal(b, &f.Value)
}

func (t *TLSInput) ca() string {
	if t == nil || t.CAPem == nil {
		return ""
	}
	return t.CAPem.Value
}

func (t *TLSInput) clientCert() string {
	if t == nil || t.ClientCertPEM == nil {
		return ""
	}
	return t.ClientCertPEM.Value
}

func (t *TLSInput) clientKey() string {
	if t == nil || t.ClientKeyPEM == nil {
		return ""
	}
	return t.ClientKeyPEM.Value
}

// testPurposeOrDefault resolves the connection test purpose for a write payload.
func testPurposeOrDefault(in *WriteInput) string {
	if in.TestPurpose == nil || *in.TestPurpose == "" {
		return "review"
	}
	return *in.TestPurpose
}

func cloneCapabilitiesView(in *CapabilitiesView) CapabilitiesView {
	caps := make(map[string]bool, len(in.Capabilities))
	for k, v := range in.Capabilities {
		caps[k] = v
	}
	return CapabilitiesView{
		DatasourceID:        in.DatasourceID,
		DetectedVersion:     in.DetectedVersion,
		IdentityFingerprint: in.IdentityFingerprint,
		Capabilities:        caps,
		CheckedAt:           in.CheckedAt,
	}
}

// NewService constructs a Service.
func NewService(db *gorm.DB, factory *Factory, crypter *crypto.Crypter, audit *auditlog.Recorder) (*Service, error) {
	if db == nil || factory == nil || crypter == nil || audit == nil {
		return nil, errors.New("datasource: nil dependency")
	}
	return &Service{db: db, factory: factory, crypter: crypter, audit: audit}, nil
}

// Recorder returns the attached auditlog.Recorder.
func (s *Service) Recorder() *auditlog.Recorder {
	if s == nil {
		return nil
	}
	return s.audit
}

// engineModeOK mirrors the datasource_engine_mode CHECK constraint.
func engineModeOK(engine, mode string) bool {
	switch {
	case engine == "mysql" || engine == "tidb" || engine == "oceanbase":
		return mode == "mysql"
	case engine == "postgresql":
		return mode == "postgresql"
	case engine == "polardb":
		return mode == "mysql" || mode == "postgresql"
	default:
		return false
	}
}

// validateTLS validates the TLS replacement object shape (RCP-20261016): ca_pem is required (preventing
// all-null material objects from persisting empty bundle rows, as RootCAs are the foundation of verifying TLS),
// client cert and private key must appear as a pair, and each piece must be a parseable PEM block. The semantic
// invariant is enforced by connect.go: presence of material mandates verifying TLS with no skip-verification path.
func validateTLS(t *TLSInput) error {
	if t == nil {
		return nil
	}
	ca, cert, key := t.ca(), t.clientCert(), t.clientKey()
	if ca == "" {
		return bizerr.ErrValidationFailed
	}
	if (cert == "") != (key == "") {
		return bizerr.ErrValidationFailed
	}
	// Validate block types per field: block type must match the field (CA/client cert fields accept only
	// CERTIFICATE blocks, private key field accepts only PRIVATE KEY blocks), preventing syntactically valid
	// but semantically misplaced material (such as a certificate in the key field) from silently entering the splitter.
	for _, material := range []struct {
		value    string
		wantCert bool
	}{
		{ca, true}, {cert, true}, {key, false},
	} {
		if material.value == "" {
			continue
		}
		if !ValidatePEMBlocks([]byte(material.value), material.wantCert) {
			return bizerr.ErrValidationFailed
		}
	}
	return nil
}

// sealCredential encrypts a single Purpose credential with AAD bound to the row ID about to be written.
func (s *Service) sealCredential(datasourceID, purpose, username, password string) (*DatasourceCredentialModel, error) {
	plain, err := json.Marshal(credentialPayload{Username: username, Password: password})
	if err != nil {
		return nil, err
	}
	rowID := newCredentialID()
	env, err := s.crypter.SealColumn(crypto.PurposeDatasourceCredential, crypto.ColumnRef{
		Table:      "datasource_credentials",
		PrimaryKey: rowID,
		Column:     "credential",
	}, plain)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &DatasourceCredentialModel{
		ID:                   rowID,
		DatasourceID:         datasourceID,
		Purpose:              purpose,
		CredentialCiphertext: env.Ciphertext,
		CredentialNonce:      env.Nonce,
		CredentialKeyID:      env.KeyID,
		CredentialAlgorithm:  env.Algorithm,
		Enabled:              true,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

// replaceCredentials replaces all enabled credentials: old rows are marked enabled=false to preserve history.
// Three explicit modes: replace (new SecretInput); reuse (cross-purpose reference to an old row's password +
// new purpose's username); keep (self-reference — copies the complete credential including username from the
// old row of the same purpose; valid only when the old row exists, never on Create).
func (s *Service) replaceCredentials(ctx context.Context, tx *gorm.DB, dsID string, inputs []CredentialInput) error {
	// Load currently enabled credentials (source for keep and reuse).
	var existing []DatasourceCredentialModel
	if err := tx.WithContext(ctx).
		Where("datasource_id = ? and enabled = true", dsID).Find(&existing).Error; err != nil {
		return err
	}
	existingByPurpose := map[string]*DatasourceCredentialModel{}
	for i := range existing {
		existingByPurpose[existing[i].Purpose] = &existing[i]
	}
	if err := tx.WithContext(ctx).Model(&DatasourceCredentialModel{}).
		Where("datasource_id = ? and enabled = true", dsID).
		Update("enabled", false).Error; err != nil {
		return err
	}
	for _, in := range inputs {
		var username, password string
		hasPassword := in.Password != nil && in.Password.Value != ""
		switch {
		case hasPassword:
			username, password = in.Username, in.Password.Value
		case in.ReuseCredentialPurpose != nil && *in.ReuseCredentialPurpose == in.Purpose:
			src := existingByPurpose[in.Purpose]
			if src == nil {
				return bizerr.ErrValidationFailed
			}
			payload, err := s.factory.OpenCredential(src)
			if err != nil {
				return fmt.Errorf("datasource: keep source credential unreadable")
			}
			username, password = payload.Username, payload.Password
		default:
			src := existingByPurpose[*in.ReuseCredentialPurpose]
			if src == nil {
				return bizerr.ErrValidationFailed
			}
			payload, err := s.factory.OpenCredential(src)
			if err != nil {
				return fmt.Errorf("datasource: reuse source credential unreadable")
			}
			username, password = in.Username, payload.Password
		}
		row, err := s.sealCredential(dsID, in.Purpose, username, password)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			if txutil.IsUniqueViolation(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
	}
	return nil
}

// sealTLSMaterial encrypts concatenated PEM material; AAD matches the readers (factory/execution/query)
// field for field. VerifyServer is always true: presence of material mandates verifying TLS.
func (s *Service) sealTLSMaterial(datasourceID, purpose string, bundle []byte) (*DatasourceTLSMaterialModel, error) {
	rowID := newCredentialID()
	env, err := s.crypter.SealColumn(crypto.PurposeDatasourceTLS, crypto.ColumnRef{
		Table:      "datasource_tls_materials",
		PrimaryKey: rowID,
		Column:     "material",
	}, bundle)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &DatasourceTLSMaterialModel{
		ID: rowID, DatasourceID: datasourceID, Purpose: purpose,
		VerifyServer:       true,
		MaterialCiphertext: env.Ciphertext, MaterialNonce: env.Nonce,
		MaterialKeyID: &env.KeyID, MaterialAlgorithm: &env.Algorithm,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// openTLSBundleRaw decrypts an existing TLS material row into the raw concatenated bundle (used for
// re-sealing in keep mode without round-tripping material through the client). AAD matches sealTLSMaterial
// and readers field for field. Malformed rows (empty ciphertext/key_id) fail closed as corrupt rows and never
// participate in re-sealing (matching the nil guard in sibling readers, but rejecting writes rather than skipping).
func (s *Service) openTLSBundleRaw(m *DatasourceTLSMaterialModel) ([]byte, error) {
	if m == nil || m.MaterialCiphertext == nil || m.MaterialKeyID == nil {
		return nil, fmt.Errorf("datasource: tls material malformed")
	}
	plain, err := s.crypter.OpenColumn(crypto.PurposeDatasourceTLS, crypto.ColumnRef{
		Table:      "datasource_tls_materials",
		PrimaryKey: m.ID,
		Column:     "material",
	}, crypto.Envelope{
		Ciphertext: m.MaterialCiphertext,
		Nonce:      m.MaterialNonce,
		KeyID:      *m.MaterialKeyID,
		Algorithm:  ptr.Deref(m.MaterialAlgorithm),
	})
	return plain, err
}

// keepTLSMaterials implements the 'keep' state (RCP-20261016 tri-state + R1 High-1 fix):
// existing material content stays unchanged, but rows are re-sealed across the new credential purpose set —
// newly added purposes inherit the material and removed purposes have their rows deleted. Because material
// is sealed per (datasource_id, purpose) while replaceCredentials replaces the purpose set wholesale, leaving
// TLS rows untouched when tls is omitted while adding a new purpose would leave the new purpose without a TLS
// row — silently downgrading connections to plaintext while tls_verified stays true; re-sealing prevents this
// downgrade. No-op when no material exists; fails closed if existing rows cannot be decrypted (consistent with
// runtime read behavior: decryption failure = datasource unavailable, never falling back to plaintext).
func (s *Service) keepTLSMaterials(ctx context.Context, tx *gorm.DB, dsID string, inputs []CredentialInput) error {
	var rows []DatasourceTLSMaterialModel
	if err := tx.WithContext(ctx).Where("datasource_id = ?", dsID).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	bundle, err := s.openTLSBundleRaw(&rows[0])
	if err != nil {
		return fmt.Errorf("datasource: tls material unreadable")
	}
	if err := tx.WithContext(ctx).
		Where("datasource_id = ?", dsID).Delete(&DatasourceTLSMaterialModel{}).Error; err != nil {
		return err
	}
	for _, in := range inputs {
		row, err := s.sealTLSMaterial(dsID, in.Purpose, bundle)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// replaceTLSMaterials deletes existing TLS material and re-seals from the replacement object (handling
// replace/remove semantics; 'keep' is handled by keepTLSMaterials). Because connections read by
// (datasource_id, purpose), the same material is sealed into one row per credential purpose in the current write.
// Concatenation order is canonical: CA chain -> client certificate -> client private key (splitTLSMaterial
// treats the last certificate block as the client certificate when a private key is present, and puts the rest into RootCAs).
func (s *Service) replaceTLSMaterials(ctx context.Context, tx *gorm.DB, dsID string, inputs []CredentialInput, tls *TLSInput) error {
	if err := tx.WithContext(ctx).
		Where("datasource_id = ?", dsID).Delete(&DatasourceTLSMaterialModel{}).Error; err != nil {
		return err
	}
	if tls == nil {
		return nil
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{tls.ca(), tls.clientCert(), tls.clientKey()} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	bundle := []byte(strings.Join(parts, "\n"))
	for _, in := range inputs {
		row, err := s.sealTLSMaterial(dsID, in.Purpose, bundle)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// validateHostAddress SEC-0009/T05 configuration-surface address guard: validates hostname shape
// (rejecting loopback/link-local/metadata/unspecified/broadcast/Unix sockets) and checks each resolved IP
// when DNS resolution succeeds (DNS failure does not reject — preserving create-before-test semantics while
// dial-layer addrguard remains the authoritative defense). Private network datasource targets are allowed.
func (s *Service) validateTargetEndpoint(ctx context.Context, host string, port int, dbName string) error {
	if err := addrguard.ValidateEndpointFor(ctx, addrguard.PurposeDatasource, host, port, dbName, nil); err != nil {
		return ErrEndpointForbidden{Err: err}
	}
	return nil
}
