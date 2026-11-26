package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestClassifyDefaultBehaviorAllBranches(t *testing.T) {
	if got := ClassifyDefaultBehavior("", "VIRTUAL GENERATED"); got != DefaultGenerated {
		t.Fatalf("expected generated, got %q", got)
	}
	if got := ClassifyDefaultBehavior("nextval('seq_id'::regclass)", ""); got != DefaultSequence {
		t.Fatalf("expected sequence, got %q", got)
	}
	if got := ClassifyDefaultBehavior("NULL", ""); got != DefaultNone {
		t.Fatalf("expected none, got %q", got)
	}
}

type failingCatalog struct {
	err error
}

func (f *failingCatalog) ListSchemas(ctx context.Context, ds DatasourceTarget) ([]string, error) {
	return nil, f.err
}

func (f *failingCatalog) ListTables(ctx context.Context, ds DatasourceTarget, schema string) ([]string, error) {
	return nil, f.err
}

func (f *failingCatalog) ListColumns(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataColumn, error) {
	return nil, f.err
}

func (f *failingCatalog) ListIndexes(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataIndex, error) {
	return nil, f.err
}

type includedIndexCatalog struct{}

func (i *includedIndexCatalog) ListSchemas(ctx context.Context, ds DatasourceTarget) ([]string, error) {
	return []string{"public"}, nil
}

func (i *includedIndexCatalog) ListTables(ctx context.Context, ds DatasourceTarget, schema string) ([]string, error) {
	return []string{"items"}, nil
}

func (i *includedIndexCatalog) ListColumns(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataColumn, error) {
	return []database.MetadataColumn{
		{ColumnName: "id", Ordinal: 1, DatabaseType: "int8", ColumnKey: "PK"},
		{ColumnName: "sku", Ordinal: 2, DatabaseType: "text"},
	}, nil
}

func (i *includedIndexCatalog) ListIndexes(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataIndex, error) {
	return []database.MetadataIndex{
		{
			IndexName:       "idx_items_sku",
			Primary:         false,
			NonUnique:       true,
			ColumnNames:     []string{"id"},
			IncludedColumns: []string{"sku"},
		},
	}, nil
}

func TestDescribeAuthorizedTableEdgeCases(t *testing.T) {
	ctx := context.Background()
	var nilAuth *Authorizer
	if _, err := nilAuth.DescribeAuthorizedTable(ctx, nil, DatasourceTarget{}, "s", "t"); err == nil {
		t.Fatal("expected error for nil authorizer")
	}

	var nilReader *AdapterCatalogReader
	if _, err := nilReader.ListSchemas(ctx, DatasourceTarget{DatasourceID: "x"}); err == nil {
		t.Fatal("expected error on nil reader")
	}
	if _, err := nilReader.ListTables(ctx, DatasourceTarget{DatasourceID: "x"}, "public"); err == nil {
		t.Fatal("expected error on nil reader")
	}
	if _, err := nilReader.ListColumns(ctx, DatasourceTarget{DatasourceID: "x"}, "public", "users"); err == nil {
		t.Fatal("expected error on nil reader")
	}
	if _, err := nilReader.ListIndexes(ctx, DatasourceTarget{DatasourceID: "x"}, "public", "users"); err == nil {
		t.Fatal("expected error on nil reader")
	}

	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	authFail := &Authorizer{Catalog: &failingCatalog{err: errors.New("catalog failed")}}
	if _, err := authFail.DescribeAuthorizedTable(ctx, sess, DatasourceTarget{CompatibilityMode: "postgresql"}, "", "items"); err == nil {
		t.Fatal("expected error when ListColumns fails")
	}
	if _, err := authFail.DescribeAuthorizedTable(ctx, sess, DatasourceTarget{CompatibilityMode: "mysql"}, "", "items"); err == nil {
		t.Fatal("expected error when schema is empty for mysql")
	}
	if _, err := authFail.DescribeAuthorizedTable(ctx, sess, DatasourceTarget{CompatibilityMode: "postgresql"}, "public", ""); err == nil {
		t.Fatal("expected error when table is empty")
	}

	authInc := &Authorizer{Catalog: &includedIndexCatalog{}}
	desc, err := authInc.DescribeAuthorizedTable(ctx, sess, DatasourceTarget{CompatibilityMode: "postgresql"}, "", "items")
	if err != nil {
		t.Fatalf("DescribeAuthorizedTable with included columns failed: %v", err)
	}
	if len(desc.Indexes) != 1 || len(desc.Indexes[0].IncludedTokens) != 1 {
		t.Fatalf("expected 1 index with 1 included column token, got %+v", desc.Indexes)
	}
}
