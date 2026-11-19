package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
)

type metadataFactsAdapter struct {
	database.Adapter

	exists       bool
	existsErr    error
	existsCalls  int
	tables       []string
	tablesErr    error
	columns      []database.MetadataColumn
	columnsErr   error
	columnCalls  int
	indexes      []database.MetadataIndex
	indexesErr   error
	indexCalls   int
	estimateRows *int64
	estimateErr  error
}

func (a *metadataFactsAdapter) TableExists(context.Context, *database.PurposePool, string, string) (bool, error) {
	a.existsCalls++
	return a.exists, a.existsErr
}

func (a *metadataFactsAdapter) ListTables(context.Context, *database.PurposePool, string) ([]string, error) {
	return a.tables, a.tablesErr
}

func (a *metadataFactsAdapter) ListColumns(context.Context, *database.PurposePool, string, string) ([]database.MetadataColumn, error) {
	a.columnCalls++
	return a.columns, a.columnsErr
}

func (a *metadataFactsAdapter) ListIndexes(context.Context, *database.PurposePool, string, string) ([]database.MetadataIndex, error) {
	a.indexCalls++
	return a.indexes, a.indexesErr
}

type metadataEstimateAdapter struct {
	*metadataFactsAdapter
}

func (a *metadataEstimateAdapter) EstimateTableRows(context.Context, *database.PurposePool, string, string) (*int64, error) {
	return a.estimateRows, a.estimateErr
}

func newMetadataFactSource(adapter database.Adapter) *factSource {
	return &factSource{adapter: adapter}
}

func factMap(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func factList(t *testing.T, value any) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMetadataFactsSchemaMetadataBoundsTableList(t *testing.T) {
	tables := make([]string, maxSchemaMetadataTables+1)
	for i := range tables {
		tables[i] = "table"
	}
	src := newMetadataFactSource(&metadataFactsAdapter{tables: tables})
	fact, err := src.SchemaMetadata(context.Background(), "schema")
	if err != nil {
		t.Fatal(err)
	}
	if fact["table_count"] != len(tables) {
		t.Fatalf("table_count = %#v", fact["table_count"])
	}
	got := factListValue(t, fact["tables"])
	if len(got) != maxSchemaMetadataTables {
		t.Fatalf("bounded table list len = %d", len(got))
	}
	if fact["metadata_complete"] != false || fact["truncated"] != true {
		t.Fatalf("truncation state = %#v", fact)
	}
}

func factListValue(t *testing.T, value any) []any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMetadataFactsTablePresentReturnsControlledColumnProfile(t *testing.T) {
	adapter := &metadataFactsAdapter{
		exists: true,
		columns: []database.MetadataColumn{
			{
				ColumnName: "account_id", Ordinal: 1, DatabaseType: "bigint(20)",
				Nullable: false, ColumnDeflt: "0",
			},
			{
				ColumnName: "email", Ordinal: 2, DatabaseType: "varchar(320)",
				Nullable: true, ColumnRemark: "customer contact",
			},
			{
				ColumnName: "profile", Ordinal: 3, DatabaseType: "tenant_custom(secret)",
				Nullable: true, ColumnDeflt: "'literal-secret'", ColumnRemark: "do not export",
			},
		},
	}
	fact, err := newMetadataFactSource(adapter).TableMetadata(context.Background(), "app", "accounts")
	if err != nil {
		t.Fatal(err)
	}
	if fact["existence"] != "present" || fact["metadata_complete"] != false ||
		fact["truncated"] != false || fact["estimate"] != false {
		t.Fatalf("table metadata flags = %#v", fact)
	}
	cols := factList(t, fact["columns"])
	if len(cols) != 3 {
		t.Fatalf("columns = %#v", cols)
	}
	if cols[0]["column_name"] != "account_id" || cols[0]["type_class"] != "integer" ||
		cols[0]["type_status"] != "observed" || cols[0]["has_default"] != true {
		t.Fatalf("integer profile = %#v", cols[0])
	}
	if cols[1]["column_name"] != "email" || cols[1]["type_class"] != "string" ||
		cols[1]["type_status"] != "observed" || cols[1]["type_length"] != float64(320) ||
		cols[1]["has_comment"] != true {
		t.Fatalf("string profile = %#v", cols[1])
	}
	if cols[2]["column_name"] != "profile" || cols[2]["type_class"] != "other" ||
		cols[2]["type_status"] != "unknown" {
		t.Fatalf("custom type must be redacted to other: %#v", cols[2])
	}
	raw := mustJSON(t, fact)
	for _, forbidden := range []string{"tenant_custom", "literal-secret", "customer contact", "do not export"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("unsafe metadata %q leaked: %s", forbidden, raw)
		}
	}
	if adapter.existsCalls != 1 || adapter.columnCalls != 1 {
		t.Fatalf("calls: exists=%d columns=%d", adapter.existsCalls, adapter.columnCalls)
	}
}

func TestMetadataFactsKnownTypesRemainComplete(t *testing.T) {
	projected, total, complete := metadataColumnFacts([]database.MetadataColumn{
		{ColumnName: "id", Ordinal: 1, DatabaseType: "bigint"},
		{ColumnName: "label", Ordinal: 2, DatabaseType: "varchar(64)", Nullable: true},
	})
	if !complete || total != 2 {
		t.Fatalf("known types completeness = %v, total=%d", complete, total)
	}
	for _, column := range projected {
		if column["type_status"] != "observed" {
			t.Fatalf("known type status = %#v", column)
		}
	}
}

func TestMetadataFactsTableAbsentIsObservedNotFailure(t *testing.T) {
	adapter := &metadataFactsAdapter{exists: false, columnsErr: errors.New("must not query columns")}
	fact, err := newMetadataFactSource(adapter).TableMetadata(context.Background(), "app", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if fact["existence"] != "absent" || fact["metadata_complete"] != true || fact["truncated"] != false {
		t.Fatalf("absent fact = %#v", fact)
	}
	if adapter.existsCalls != 1 || adapter.columnCalls != 0 {
		t.Fatalf("absent calls: exists=%d columns=%d", adapter.existsCalls, adapter.columnCalls)
	}
}

func TestMetadataFactsReadFailureNeverBecomesAbsent(t *testing.T) {
	existsFailure := &metadataFactsAdapter{existsErr: errors.New("insufficient privilege")}
	if _, err := newMetadataFactSource(existsFailure).TableMetadata(context.Background(), "app", "items"); err == nil {
		t.Fatal("existence read failure must fail the tool")
	}
	columnFailure := &metadataFactsAdapter{exists: true, columnsErr: errors.New("metadata unreadable")}
	if _, err := newMetadataFactSource(columnFailure).TableMetadata(context.Background(), "app", "items"); err == nil {
		t.Fatal("column read failure must fail the tool")
	}
}

func TestMetadataFactsTableStatisticsEstimateStates(t *testing.T) {
	rows := int64(1200)
	present := &metadataEstimateAdapter{metadataFactsAdapter: &metadataFactsAdapter{
		exists: true,
		columns: []database.MetadataColumn{
			{ColumnName: "id", DatabaseType: "bigint"},
		},
		indexes: []database.MetadataIndex{
			{IndexName: "primary", Primary: true, ColumnNames: []string{"id"}},
		},
		estimateRows: &rows,
	}}
	fact, err := newMetadataFactSource(present).TableStatistics(context.Background(), "app", "items")
	if err != nil {
		t.Fatal(err)
	}
	if fact["existence"] != "present" || fact["row_estimate_status"] != "observed" ||
		fact["estimated_rows"] != rows || fact["has_primary_key"] != true {
		t.Fatalf("present statistics = %#v", fact)
	}

	unknown := &metadataEstimateAdapter{metadataFactsAdapter: &metadataFactsAdapter{exists: true}}
	fact, err = newMetadataFactSource(unknown).TableStatistics(context.Background(), "app", "items")
	if err != nil {
		t.Fatal(err)
	}
	if fact["row_estimate_status"] != "unknown" {
		t.Fatalf("null estimate must be unknown: %#v", fact)
	}
	if _, fabricated := fact["estimated_rows"]; fabricated {
		t.Fatalf("unknown estimate must not fabricate zero: %#v", fact)
	}

	unsupported := &metadataFactsAdapter{exists: true}
	fact, err = newMetadataFactSource(unsupported).TableStatistics(context.Background(), "app", "items")
	if err != nil {
		t.Fatal(err)
	}
	if fact["row_estimate_status"] != "unsupported" {
		t.Fatalf("missing estimator must be unsupported: %#v", fact)
	}
	if _, fabricated := fact["estimated_rows"]; fabricated {
		t.Fatalf("unsupported estimate must not fabricate zero: %#v", fact)
	}

	absent := &metadataFactsAdapter{exists: false}
	fact, err = newMetadataFactSource(absent).TableStatistics(context.Background(), "app", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if fact["existence"] != "absent" || fact["metadata_complete"] != true || fact["estimate"] != false {
		t.Fatalf("absent statistics = %#v", fact)
	}
	if _, irrelevantEstimate := fact["row_estimate_status"]; irrelevantEstimate {
		t.Fatalf("absent table must not pretend a row estimate was requested/observed: %#v", fact)
	}
	if !metadataFactComplete(fact) {
		t.Fatalf("observed absence must remain a complete metadata fact: %#v", fact)
	}
}

func TestMetadataFactsRuntimeRejectsUnknownOrUnsupportedRequestedEstimate(t *testing.T) {
	for _, status := range []string{"unknown", "unsupported"} {
		fact := map[string]any{
			"existence":           "present",
			"metadata_complete":   true,
			"truncated":           false,
			"row_estimate_status": status,
		}
		if metadataFactComplete(fact) {
			t.Fatalf("requested %s row estimate must make metadata incomplete", status)
		}
	}
	observed := map[string]any{
		"existence":           "present",
		"metadata_complete":   true,
		"truncated":           false,
		"row_estimate_status": "observed",
		"estimated_rows":      int64(0),
	}
	if !metadataFactComplete(observed) {
		t.Fatal("observed zero-row estimate must remain complete")
	}
}

func TestMetadataFactsIndexMetadataDistinguishesAbsentAndFailure(t *testing.T) {
	absent := &metadataFactsAdapter{exists: false}
	fact, err := newMetadataFactSource(absent).IndexMetadata(context.Background(), "app", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if fact["existence"] != "absent" || fact["metadata_complete"] != true {
		t.Fatalf("absent index fact = %#v", fact)
	}
	if absent.indexCalls != 0 {
		t.Fatal("absent table must not run index lookup")
	}

	failed := &metadataFactsAdapter{exists: true, indexesErr: errors.New("index metadata unreadable")}
	if _, err := newMetadataFactSource(failed).IndexMetadata(context.Background(), "app", "items"); err == nil {
		t.Fatal("index lookup failure must fail the tool")
	}
}

func TestMetadataFactsQualifiedIndexPartsAreNeverSilentlyOmitted(t *testing.T) {
	adapter := &metadataFactsAdapter{
		exists: true,
		indexes: []database.MetadataIndex{
			{
				IndexName:       "items_search",
				ColumnNames:     []string{"email"},
				IncludedColumns: []string{"id"},
				ExpressionCount: 1,
				PrefixLengths:   []int{12},
				Primary:         false,
				NonUnique:       true,
			},
		},
	}
	fact, err := newMetadataFactSource(adapter).IndexMetadata(context.Background(), "app", "items")
	if err != nil {
		t.Fatal(err)
	}
	if fact["metadata_complete"] != false {
		t.Fatalf("qualified index structure must be incomplete: %#v", fact)
	}
	indexes := factList(t, fact["indexes"])
	if len(indexes) != 1 {
		t.Fatalf("indexes = %#v", indexes)
	}
	index := indexes[0]
	if index["expression_count"] != float64(1) || index["has_expression"] != true {
		t.Fatalf("expression state = %#v", index)
	}
	if index["prefix_column_count"] != float64(1) ||
		index["has_prefix"] != true {
		t.Fatalf("prefix state = %#v", index)
	}
	included := factListValue(t, index["included_columns"])
	if len(included) != 1 || included[0] != "id" {
		t.Fatalf("included columns = %#v", included)
	}
	if metadataFactComplete(map[string]any{
		"existence":         "present",
		"metadata_complete": fact["metadata_complete"],
		"indexes":           fact["indexes"],
	}) {
		t.Fatal("qualified index fact must be rejected by the completeness gate")
	}
}

func TestMetadataFactsClassifiesOnlyControlledTypeNames(t *testing.T) {
	cases := []struct {
		raw     string
		class   string
		length  int
		prec    int
		scale   int
		present bool
	}{
		{raw: "varchar(255)", class: "string", length: 255, present: true},
		{raw: "numeric(18,4)", class: "decimal", prec: 18, scale: 4, present: true},
		{raw: "timestamp with time zone", class: "temporal"},
		{raw: "jsonb", class: "json"},
		{raw: "uuid", class: "uuid"},
		{raw: "name_with_secret(999)", class: "other"},
	}
	for _, tc := range cases {
		got := classifyMetadataType(tc.raw)
		if got.class != tc.class {
			t.Fatalf("%q class = %q, want %q", tc.raw, got.class, tc.class)
		}
		if tc.length > 0 && (!got.hasLength || got.length != tc.length) {
			t.Fatalf("%q length = %+v", tc.raw, got)
		}
		if tc.prec > 0 && (!got.hasPrecision || got.precision != tc.prec || got.scale != tc.scale) {
			t.Fatalf("%q precision = %+v", tc.raw, got)
		}
		if tc.class == "other" && (got.hasLength || got.hasPrecision || got.hasScale) {
			t.Fatalf("custom type leaked numeric suffix: %+v", got)
		}
	}
}

func TestMetadataFactsClassifiesBuiltinArraysWithoutLosingShape(t *testing.T) {
	projected, total, complete := metadataColumnFacts([]database.MetadataColumn{
		{ColumnName: "ids", Ordinal: 1, DatabaseType: "integer[]"},
		{ColumnName: "labels", Ordinal: 2, DatabaseType: "text[][]"},
	})
	if !complete || total != 2 {
		t.Fatalf("known array types completeness = %v, total=%d", complete, total)
	}
	if projected[0]["type_class"] != "array" ||
		projected[0]["element_type_class"] != "integer" ||
		projected[0]["array_dimensions"] != 1 ||
		projected[0]["type_status"] != "observed" {
		t.Fatalf("integer array profile = %#v", projected[0])
	}
	if projected[1]["element_type_class"] != "string" ||
		projected[1]["array_dimensions"] != 2 {
		t.Fatalf("multidimensional text array profile = %#v", projected[1])
	}
}

func TestMetadataFactsCustomArrayStaysUnknownWithoutLeakingName(t *testing.T) {
	projected, _, complete := metadataColumnFacts([]database.MetadataColumn{
		{ColumnName: "states", Ordinal: 1, DatabaseType: `"tenant"."private_state"[]`},
	})
	if complete || len(projected) != 1 {
		t.Fatalf("custom array result = %#v, complete=%v", projected, complete)
	}
	item := projected[0]
	if item["type_class"] != "other" || item["type_status"] != "unknown" {
		t.Fatalf("custom array type status = %#v", item)
	}
	raw := mustJSON(t, item)
	if strings.Contains(raw, "tenant") || strings.Contains(raw, "private_state") {
		t.Fatalf("custom array type leaked: %s", raw)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
