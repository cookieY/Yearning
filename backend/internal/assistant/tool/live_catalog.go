// live_catalog.go implements a read-only target datasource catalog reader backed by database.Adapter.
package tool

import (
	"context"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
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
	res, err := datasource.ResolveCredential(ctx, r.DB, r.Crypter, datasourceID, datasource.CredentialResolveOptions{
		Purposes:       []string{string(database.PurposeReview), string(database.PurposeQuery)},
		SkipUnreadable: true,
	})
	if err != nil {
		return database.Credential{}, "", domain.ErrDatasourceForbidden()
	}
	return database.Credential{Username: res.Username, Password: res.Password}, database.Purpose(res.Purpose), nil
}

func (r *AdapterCatalogReader) openTLSMaterial(ctx context.Context, datasourceID string) (*database.TLSMaterial, error) {
	return datasource.ResolveTLSMaterial(ctx, r.DB, r.Crypter, datasourceID)
}
