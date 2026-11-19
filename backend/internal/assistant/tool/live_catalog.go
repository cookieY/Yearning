// live_catalog.go implements a read-only target datasource catalog reader backed by database.Adapter.
package tool

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
)

// AdapterCatalogReader reads catalog metadata using read-only datasource credentials (review/query).
type AdapterCatalogReader struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter
}

func (r *AdapterCatalogReader) ListSchemas(ctx context.Context, ds DatasourceTarget) ([]string, error) {
	adapter, pool, cleanup, err := r.openReadPool(ctx, ds.DatasourceID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return adapter.ListSchemas(ctx, pool)
}

func (r *AdapterCatalogReader) ListTables(ctx context.Context, ds DatasourceTarget, schema string) ([]string, error) {
	adapter, pool, cleanup, err := r.openReadPool(ctx, ds.DatasourceID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return adapter.ListTables(ctx, pool, schema)
}

func (r *AdapterCatalogReader) ListColumns(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataColumn, error) {
	adapter, pool, cleanup, err := r.openReadPool(ctx, ds.DatasourceID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return adapter.ListColumns(ctx, pool, schema, table)
}

func (r *AdapterCatalogReader) ListIndexes(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataIndex, error) {
	adapter, pool, cleanup, err := r.openReadPool(ctx, ds.DatasourceID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return adapter.ListIndexes(ctx, pool, schema, table)
}

func (r *AdapterCatalogReader) openReadPool(ctx context.Context, datasourceID string) (database.Adapter, *database.PurposePool, func(), error) {
	if r == nil || r.DB == nil || r.Crypter == nil {
		return nil, nil, nil, domain.ErrDatasourceForbidden()
	}
	var ds struct {
		ID                string  `gorm:"column:id"`
		Host              string  `gorm:"column:host"`
		Port              int     `gorm:"column:port"`
		DatabaseName      *string `gorm:"column:database_name"`
		CompatibilityMode string  `gorm:"column:compatibility_mode"`
		Enabled           bool    `gorm:"column:enabled"`
	}
	if err := r.DB.WithContext(ctx).Raw(`
		select id::text as id, host, port, database_name, compatibility_mode, enabled
		from datasources where id = ?`, datasourceID).Scan(&ds).Error; err != nil || ds.ID == "" || !ds.Enabled {
		return nil, nil, nil, domain.ErrDatasourceForbidden()
	}

	cred, purpose, err := r.openReadCredential(ctx, ds.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	mat, err := r.openTLSMaterial(ctx, ds.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	dbName := ""
	if ds.DatabaseName != nil {
		dbName = *ds.DatabaseName
	}
	ep := database.Endpoint{
		Host:             ds.Host,
		Port:             ds.Port,
		DatabaseName:     dbName,
		CompatibilityMod: ds.CompatibilityMode,
	}
	var adapter database.Adapter
	switch ds.CompatibilityMode {
	case "mysql":
		adapter = database.NewMySQLAdapter(ep, database.AdapterOptions{})
	case "postgresql":
		adapter = database.NewPostgreSQLAdapter(ep, database.AdapterOptions{})
	default:
		return nil, nil, nil, domain.ErrDatasourceForbidden()
	}
	pool, err := adapter.OpenPurposePool(ctx, purpose, cred, mat)
	if err != nil {
		_ = adapter.Close()
		return nil, nil, nil, err
	}
	return adapter, pool, func() {
		_ = pool.Close()
		_ = adapter.Close()
	}, nil
}

func (r *AdapterCatalogReader) openReadCredential(ctx context.Context, datasourceID string) (database.Credential, database.Purpose, error) {
	for _, p := range []database.Purpose{database.PurposeReview, database.PurposeQuery} {
		var row struct {
			ID         string
			Ciphertext []byte
			Nonce      []byte
			KeyID      string
		}
		err := r.DB.WithContext(ctx).Raw(`
			select id::text as id, credential_ciphertext as ciphertext, credential_nonce as nonce,
			       credential_key_id as key_id
			from datasource_credentials
			where datasource_id = ? and purpose = ? and enabled limit 1`,
			datasourceID, string(p)).Scan(&row).Error
		if err != nil || row.ID == "" {
			continue
		}
		plain, err := r.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
			Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: row.KeyID, Algorithm: "aes-256-gcm",
		}, crypto.AAD{
			DeploymentID: r.Crypter.DeploymentID(),
			Table:        "datasource_credentials", PrimaryKey: row.ID,
			Column: "credential", Purpose: "datasource_credential",
		})
		if err != nil {
			continue
		}
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.Unmarshal(plain, &payload) != nil {
			continue
		}
		return database.Credential{Username: payload.Username, Password: payload.Password}, p, nil
	}
	return database.Credential{}, "", domain.ErrDatasourceForbidden()
}

func (r *AdapterCatalogReader) openTLSMaterial(ctx context.Context, datasourceID string) (*database.TLSMaterial, error) {
	var row struct {
		ID         string
		Ciphertext []byte
		Nonce      []byte
		KeyID      *string
		Algorithm  *string
		Verify     bool
		ServerName *string
	}
	err := r.DB.WithContext(ctx).Raw(`
		select id::text as id, material_ciphertext as ciphertext, material_nonce as nonce,
		       material_key_id as key_id, material_algorithm as algorithm,
		       verify_server as verify, server_name
		from datasource_tls_materials where datasource_id = ? limit 1`, datasourceID).Scan(&row).Error
	if err != nil || row.ID == "" || row.KeyID == nil {
		return nil, err
	}
	alg := "aes-256-gcm"
	if row.Algorithm != nil && *row.Algorithm != "" {
		alg = *row.Algorithm
	}
	plain, err := r.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: *row.KeyID, Algorithm: alg,
	}, crypto.AAD{
		DeploymentID: r.Crypter.DeploymentID(),
		Table:        "datasource_tls_materials", PrimaryKey: row.ID,
		Column: "material", Purpose: "datasource_tls",
	})
	if err != nil {
		return nil, err
	}
	mat := &database.TLSMaterial{VerifyServer: row.Verify}
	if row.ServerName != nil {
		mat.ServerName = *row.ServerName
	}
	var certs [][]byte
	rest := plain
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		switch {
		case blk.Type == "CERTIFICATE":
			certs = append(certs, pem.EncodeToMemory(blk))
		case strings.HasSuffix(blk.Type, "PRIVATE KEY"):
			mat.KeyPEM = append(mat.KeyPEM, pem.EncodeToMemory(blk)...)
		}
	}
	if len(mat.KeyPEM) > 0 && len(certs) > 0 {
		mat.CertPEM = certs[len(certs)-1]
		for _, c := range certs[:len(certs)-1] {
			mat.CAPEM = append(mat.CAPEM, c...)
		}
	} else {
		for _, c := range certs {
			mat.CAPEM = append(mat.CAPEM, c...)
		}
	}
	return mat, nil
}
