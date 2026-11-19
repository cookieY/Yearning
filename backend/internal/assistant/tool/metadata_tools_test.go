package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/database"
)

type stubCatalog struct {
	cols []database.MetadataColumn
	idxs []database.MetadataIndex
}

func (s *stubCatalog) ListSchemas(ctx context.Context, ds tool.DatasourceTarget) ([]string, error) {
	return []string{"logistics"}, nil
}

func (s *stubCatalog) ListTables(ctx context.Context, ds tool.DatasourceTarget, schema string) ([]string, error) {
	return []string{"warehouse_inventory"}, nil
}

func (s *stubCatalog) ListColumns(ctx context.Context, ds tool.DatasourceTarget, schema, table string) ([]database.MetadataColumn, error) {
	return s.cols, nil
}

func (s *stubCatalog) ListIndexes(ctx context.Context, ds tool.DatasourceTarget, schema, table string) ([]database.MetadataIndex, error) {
	return s.idxs, nil
}

func TestDescribeAuthorizedTable_StripsRemarksAndDefaultLiterals(t *testing.T) {
	cat := &stubCatalog{
		cols: []database.MetadataColumn{
			{
				SchemaName:   "logistics",
				TableName:    "warehouse_inventory",
				ColumnName:   "id",
				Ordinal:      1,
				DatabaseType: "bigint",
				Nullable:     false,
				ColumnKey:    "PRI",
				ColumnExtra:  "auto_increment",
				ColumnRemark: "Primary key ID, confidential internal remark",
			},
			{
				SchemaName:   "logistics",
				TableName:    "warehouse_inventory",
				ColumnName:   "warehouse_code",
				Ordinal:      2,
				DatabaseType: "varchar(64)",
				Nullable:     false,
				ColumnKey:    "UNI",
				ColumnDeflt:  "'WH-DEFAULT-SECRET'",
				ColumnRemark: "Warehouse code: default WH-DEFAULT-SECRET",
			},
		},
		idxs: []database.MetadataIndex{
			{
				SchemaName:  "logistics",
				TableName:   "warehouse_inventory",
				IndexName:   "idx_warehouse_code",
				NonUnique:   false,
				Primary:     false,
				ColumnNames: []string{"warehouse_code"},
			},
		},
	}

	auth := &tool.Authorizer{Catalog: cat}
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	desc, err := auth.DescribeAuthorizedTable(context.Background(), sess, tool.DatasourceTarget{
		DatasourceID:      "ds-1",
		Engine:            "mysql",
		CompatibilityMode: "mysql",
		DefaultDatabase:   "logistics",
	}, "logistics", "warehouse_inventory")
	if err != nil {
		t.Fatalf("DescribeAuthorizedTable: %v", err)
	}

	if desc.Columns[0].DefaultBehavior != tool.DefaultAutoIncrement {
		t.Fatalf("expected col[0] default_behavior=auto_increment, got %s", desc.Columns[0].DefaultBehavior)
	}
	if desc.Columns[1].DefaultBehavior != tool.DefaultServerDefault {
		t.Fatalf("expected col[1] default_behavior=server_default, got %s", desc.Columns[1].DefaultBehavior)
	}

	raw, _ := json.Marshal(desc)
	jsonStr := string(raw)
	for _, forbidden := range []string{"logistics", "warehouse_inventory", "warehouse_code", "idx_warehouse_code", "WH-DEFAULT-SECRET", "confidential internal remark"} {
		if strings.Contains(jsonStr, forbidden) {
			t.Fatalf("AnonymizedTableDescription leaked %q: %s", forbidden, jsonStr)
		}
	}
}
