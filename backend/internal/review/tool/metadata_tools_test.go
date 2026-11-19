package tool

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type scopedMetadataSource struct {
	recordingSource
	schemaCalls int
	tableCalls  int
	gotSchema   string
	gotTable    string
}

func (s *scopedMetadataSource) SchemaMetadata(context.Context, string) (map[string]any, error) {
	s.schemaCalls++
	return map[string]any{"tables": []string{"table_1"}, "metadata_complete": true, "truncated": false}, nil
}

func (s *scopedMetadataSource) TableMetadata(_ context.Context, schema, table string) (map[string]any, error) {
	s.tableCalls++
	s.gotSchema = schema
	s.gotTable = table
	return map[string]any{
		"schema":            schema,
		"table":             table,
		"existence":         "present",
		"columns":           []map[string]any{{"column_name": "column_1", "type_class": "integer"}},
		"metadata_complete": true,
		"truncated":         false,
		"estimate":          false,
	}, nil
}

func TestSchemaMetadataCannotDiscoverUnreferencedSchemaTables(t *testing.T) {
	src := &scopedMetadataSource{}
	res := NewRegistry().Execute(context.Background(), src, "call-1", NameSchemaMetadata,
		map[string]any{"schema": "schema_1"})
	if res.Success || res.ErrorCode == nil || *res.ErrorCode != 2012 {
		t.Fatalf("unscoped schema discovery must be rejected: %+v", res)
	}
	if src.schemaCalls != 0 || src.tableCalls != 0 {
		t.Fatalf("schema-only routing calls: schema=%d table=%d", src.schemaCalls, src.tableCalls)
	}
}

func TestSchemaMetadataScopedUsesOptionalTableSource(t *testing.T) {
	src := &scopedMetadataSource{}
	res := NewRegistry().Execute(context.Background(), src, "call-1", NameSchemaMetadata,
		map[string]any{"schema": "schema_1", "table": "table_1"})
	if !res.Success {
		t.Fatalf("scoped call failed: %+v", res)
	}
	if src.schemaCalls != 0 || src.tableCalls != 1 {
		t.Fatalf("scoped routing calls: schema=%d table=%d", src.schemaCalls, src.tableCalls)
	}
	if src.gotSchema != "schema_1" || src.gotTable != "table_1" {
		t.Fatalf("scoped args = %q.%q", src.gotSchema, src.gotTable)
	}
	if res.FactSchemaVersion != 2 {
		t.Fatalf("new metadata fact schema version = %d, want 2", res.FactSchemaVersion)
	}
}

func TestSchemaMetadataScopedFailsClosedWithoutOptionalSource(t *testing.T) {
	src := &recordingSource{}
	res := NewRegistry().Execute(context.Background(), src, "call-1", NameSchemaMetadata,
		map[string]any{"schema": "schema_1", "table": "table_1"})
	if res.Success || res.ErrorCode == nil || *res.ErrorCode != 2012 || res.NormalizedFact != nil {
		t.Fatalf("old source must fail scoped metadata closed: %+v", res)
	}
}

func TestFactSchemaVersionMatchesRuntimeContract(t *testing.T) {
	raw, err := os.ReadFile("../../../../api/contracts/ai-review-runtime-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		ToolRuntime struct {
			CurrentVersion int    `json:"fact_schema_version_current"`
			Evolution      string `json:"fact_schema_version_evolution"`
		} `json:"tool_runtime"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.ToolRuntime.CurrentVersion != FactSchemaVersion {
		t.Fatalf("contract fact schema version = %d, runtime = %d",
			contract.ToolRuntime.CurrentVersion, FactSchemaVersion)
	}
	if !strings.Contains(contract.ToolRuntime.Evolution, "historical Evidence retains") ||
		!strings.Contains(contract.ToolRuntime.Evolution, "never rewritten") {
		t.Fatalf("fact schema version evolution is not historical-safe: %q",
			contract.ToolRuntime.Evolution)
	}
}
