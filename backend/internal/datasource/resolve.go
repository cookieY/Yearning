package datasource

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
)

// ResolvedCredential holds a decrypted target database credential together with the
// purpose row from which it was loaded.
type ResolvedCredential struct {
	Username string
	Password string
	Purpose  string
}

// CredentialResolveOptions configures priority-ordered credential lookup across purposes.
type CredentialResolveOptions struct {
	// Purposes lists candidate credential purposes in priority order (e.g. "review", "query").
	Purposes []string
	// SkipUnreadable controls whether decryption or payload unmarshal errors skip to the next
	// candidate purpose instead of failing closed on the first matching row.
	SkipUnreadable bool
}

// OpenCredentialEnvelope decrypts and unmarshals a datasource_credentials row using the
// canonical datasource_credential AAD contract.
func OpenCredentialEnvelope(crypter *crypto.Crypter, row *DatasourceCredentialModel) (database.Credential, error) {
	if crypter == nil {
		return database.Credential{}, errors.New("datasource: nil crypter")
	}
	if row == nil {
		return database.Credential{}, ErrCredentialMissing
	}
	alg := row.CredentialAlgorithm
	if alg == "" {
		alg = "aes-256-gcm"
	}
	plain, err := crypter.OpenColumn(crypto.PurposeDatasourceCredential, crypto.ColumnRef{
		Table:      "datasource_credentials",
		PrimaryKey: row.ID,
		Column:     "credential",
	}, crypto.Envelope{
		Ciphertext: row.CredentialCiphertext,
		Nonce:      row.CredentialNonce,
		KeyID:      row.CredentialKeyID,
		Algorithm:  alg,
	})
	if err != nil {
		return database.Credential{}, err
	}
	var payload credentialPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return database.Credential{}, errors.New("datasource: credential payload malformed")
	}
	return database.Credential{
		Username: payload.Username,
		Password: payload.Password,
	}, nil
}

// SplitTLSMaterial splits concatenated PEM blocks into CA chain, optional mTLS client certificate,
// and optional private key using the canonical datasource TLS rules:
// when a private key is present, the last certificate block is the client certificate (a single
// cert + key yields pure mTLS client material) and preceding certificates form the CA chain; when
// no private key is present, all certificates form the CA chain. Client certificates are never
// mixed into RootCAs. If multiple private key blocks appear, the last key block wins.
func SplitTLSMaterial(verifyServer bool, serverName string, plain []byte) (*database.TLSMaterial, error) {
	mat := &database.TLSMaterial{VerifyServer: verifyServer, ServerName: serverName}
	var certs [][]byte
	rest := plain
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			certs = append(certs, pem.EncodeToMemory(block))
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			mat.KeyPEM = pem.EncodeToMemory(block)
		}
	}
	if len(certs) > 0 {
		caCerts := certs
		if mat.KeyPEM != nil {
			mat.CertPEM = certs[len(certs)-1]
			caCerts = certs[:len(certs)-1]
		}
		for _, c := range caCerts {
			mat.CAPEM = append(mat.CAPEM, c...)
		}
	}
	return mat, nil
}

// ValidatePEMBlocks verifies that material contains at least one PEM block and that every block
// matches wantCert (CERTIFICATE when wantCert is true, non-CERTIFICATE private key blocks otherwise).
func ValidatePEMBlocks(material []byte, wantCert bool) bool {
	rest := material
	blocks := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		blocks++
		isCert := block.Type == "CERTIFICATE"
		if isCert != wantCert {
			return false
		}
	}
	return blocks > 0
}

// OpenTLSEnvelope decrypts a datasource_tls_materials row using the canonical datasource_tls
// AAD contract and splits its PEM bundle. Returns (nil, nil) when TLS material is absent/unpopulated.
func OpenTLSEnvelope(crypter *crypto.Crypter, row *DatasourceTLSMaterialModel) (*database.TLSMaterial, error) {
	if row == nil || len(row.MaterialCiphertext) == 0 || row.MaterialKeyID == nil || *row.MaterialKeyID == "" {
		return nil, nil
	}
	if crypter == nil {
		return nil, errors.New("datasource: nil crypter")
	}
	alg := ptr.Deref(row.MaterialAlgorithm)
	if alg == "" {
		alg = "aes-256-gcm"
	}
	plain, err := crypter.OpenColumn(crypto.PurposeDatasourceTLS, crypto.ColumnRef{
		Table:      "datasource_tls_materials",
		PrimaryKey: row.ID,
		Column:     "material",
	}, crypto.Envelope{
		Ciphertext: row.MaterialCiphertext,
		Nonce:      row.MaterialNonce,
		KeyID:      *row.MaterialKeyID,
		Algorithm:  alg,
	})
	if err != nil {
		return nil, err
	}
	return SplitTLSMaterial(row.VerifyServer, ptr.Deref(row.ServerName), plain)
}

// ResolveCredential loads and decrypts the first enabled datasource credential matching
// opts.Purposes in priority order. Returns ErrCredentialMissing when no usable row is found.
func ResolveCredential(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	datasourceID string,
	opts CredentialResolveOptions,
) (ResolvedCredential, error) {
	if db == nil {
		return ResolvedCredential{}, errors.New("datasource: nil db")
	}
	if crypter == nil {
		return ResolvedCredential{}, errors.New("datasource: nil crypter")
	}
	for _, purpose := range opts.Purposes {
		if purpose == "" {
			continue
		}
		var rows []DatasourceCredentialModel
		err := db.WithContext(ctx).
			Where("datasource_id = ? and purpose = ? and enabled = ?", datasourceID, purpose, true).
			Limit(1).
			Find(&rows).Error
		if err != nil {
			return ResolvedCredential{}, fmt.Errorf("datasource: query credential failed: %w", err)
		}
		if len(rows) == 0 {
			continue
		}
		cred, oerr := OpenCredentialEnvelope(crypter, &rows[0])
		if oerr != nil {
			if opts.SkipUnreadable {
				continue
			}
			return ResolvedCredential{}, oerr
		}
		return ResolvedCredential{
			Username: cred.Username,
			Password: cred.Password,
			Purpose:  purpose,
		}, nil
	}
	return ResolvedCredential{}, ErrCredentialMissing
}

// ResolveTLSMaterial loads and decrypts TLS material for datasourceID.
// When purposes is empty, it loads the first TLS row configured for datasourceID; otherwise it
// searches purposes in priority order and opens the first matching row. Returns (nil, nil) if none exists.
func ResolveTLSMaterial(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	datasourceID string,
	purposes ...string,
) (*database.TLSMaterial, error) {
	if db == nil {
		return nil, errors.New("datasource: nil db")
	}
	if crypter == nil {
		return nil, errors.New("datasource: nil crypter")
	}
	if len(purposes) == 0 {
		var rows []DatasourceTLSMaterialModel
		if err := db.WithContext(ctx).
			Where("datasource_id = ?", datasourceID).
			Limit(1).
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("datasource: query tls material failed: %w", err)
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return OpenTLSEnvelope(crypter, &rows[0])
	}
	for _, purpose := range purposes {
		if purpose == "" {
			continue
		}
		var rows []DatasourceTLSMaterialModel
		if err := db.WithContext(ctx).
			Where("datasource_id = ? and purpose = ?", datasourceID, purpose).
			Limit(1).
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("datasource: query tls material failed: %w", err)
		}
		if len(rows) == 0 {
			continue
		}
		return OpenTLSEnvelope(crypter, &rows[0])
	}
	return nil, nil
}
