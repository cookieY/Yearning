package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type metadataEstimateAdapterStub struct {
	Adapter
	rows      *int64
	err       error
	supported bool
}

func (s *metadataEstimateAdapterStub) EstimateTableRows(context.Context, *PurposePool, string, string) (*int64, error) {
	if !s.supported {
		panic("unsupported estimator must not execute")
	}
	return s.rows, s.err
}

type metadataEstimateUnsupportedStub struct {
	Adapter
}

func TestTableRowEstimateCapabilityAndUnknown(t *testing.T) {
	rows := int64(42)
	value, supported, err := TableRowEstimate(context.Background(),
		&metadataEstimateAdapterStub{rows: &rows, supported: true}, nil, "app", "items")
	if err != nil || !supported || value == nil || *value != rows {
		t.Fatalf("supported estimate = %v, %v, %v", value, supported, err)
	}

	value, supported, err = TableRowEstimate(context.Background(),
		&metadataEstimateAdapterStub{supported: true}, nil, "app", "items")
	if err != nil || !supported || value != nil {
		t.Fatalf("null estimate must stay unknown, not zero: %v, %v, %v", value, supported, err)
	}

	value, supported, err = TableRowEstimate(context.Background(),
		&metadataEstimateUnsupportedStub{}, nil, "app", "items")
	if err != nil || supported || value != nil {
		t.Fatalf("unsupported estimate = %v, %v, %v", value, supported, err)
	}
}

func TestTableRowEstimatePropagatesReadFailure(t *testing.T) {
	readErr := errors.New("metadata query failed")
	_, supported, err := TableRowEstimate(context.Background(),
		&metadataEstimateAdapterStub{err: readErr, supported: true}, nil, "app", "items")
	if !supported || !errors.Is(err, readErr) {
		t.Fatalf("read failure = supported:%v err:%v", supported, err)
	}
}

func TestMetadataEstimateQueriesUseCatalogMetadataOnly(t *testing.T) {
	if !strings.Contains(strings.ToUpper(mysqlTableRowEstimateSQL), "INFORMATION_SCHEMA.TABLES") {
		t.Fatalf("mysql estimate must use information_schema.tables: %s", mysqlTableRowEstimateSQL)
	}
	if !strings.Contains(strings.ToLower(mysqlTableRowEstimateSQL), "table_rows") ||
		strings.Contains(strings.ToLower(mysqlTableRowEstimateSQL), "count(") {
		t.Fatalf("mysql estimate must use TABLE_ROWS without business COUNT: %s", mysqlTableRowEstimateSQL)
	}
	if !strings.Contains(strings.ToLower(pgTableRowEstimateSQL), "pg_class") ||
		!strings.Contains(strings.ToLower(pgTableRowEstimateSQL), "reltuples") ||
		strings.Contains(strings.ToLower(pgTableRowEstimateSQL), "count(") {
		t.Fatalf("postgres estimate must use pg_class.reltuples without business COUNT: %s", pgTableRowEstimateSQL)
	}
}

func TestPostgresTableExistenceUsesCatalogPlaceholdersAndPermissionEvidence(t *testing.T) {
	if !strings.Contains(pgTableExistenceFactsSQL, "pg_catalog.pg_class") ||
		!strings.Contains(pgTableExistenceFactsSQL, "pg_catalog.pg_namespace") ||
		!strings.Contains(pgTableExistenceFactsSQL, "has_schema_privilege") {
		t.Fatalf("postgres existence query must use catalog and permission evidence: %s", pgTableExistenceFactsSQL)
	}
	if !strings.Contains(pgTableExistenceFactsSQL, "$1") || !strings.Contains(pgTableExistenceFactsSQL, "$2") {
		t.Fatalf("postgres existence query must use $n placeholders: %s", pgTableExistenceFactsSQL)
	}
	if strings.Contains(pgTableExistenceFactsSQL, "?") {
		t.Fatalf("postgres existence query must not use mysql placeholders: %s", pgTableExistenceFactsSQL)
	}
}

func TestMySQLTableExistenceRequiresSQLPrivilegeEvidence(t *testing.T) {
	lower := strings.ToLower(mysqlTableExistenceFactsSQL)
	for _, marker := range []string{
		"information_schema.user_privileges",
		"information_schema.schema_privileges",
		"privilege_type = 'select'",
	} {
		if !strings.Contains(lower, marker) {
			t.Fatalf("mysql existence query missing visibility evidence %q: %s",
				marker, mysqlTableExistenceFactsSQL)
		}
	}
}

func TestTableExistenceNeverTreatsOtherRelationKindsAsAbsent(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql"} {
		query := mysqlTableExistenceFactsSQL
		facts := tableExistenceObservation{
			unsupportedRelationCount: 1,
			schemaCount:              1,
			metadataPrivilege:        1,
		}
		if engine == "postgresql" {
			query = pgTableExistenceFactsSQL
		}
		if !strings.Contains(strings.ToLower(query), "unsupported_relation_count") {
			t.Fatalf("%s existence query does not observe non-base relations", engine)
		}
		got, err := classifyTableExistence(engine, facts)
		if got || !errors.Is(err, ErrUnsupportedRelationType) {
			t.Fatalf("%s relation result = %v, %v; want unsupported relation", engine, got, err)
		}
	}
}

func TestClassifyTableExistenceNeverTurnsHiddenObjectsIntoAbsent(t *testing.T) {
	tests := []struct {
		name      string
		engine    string
		facts     tableExistenceObservation
		want      bool
		wantError bool
	}{
		{
			name:   "mysql visible absent",
			engine: "mysql",
			facts:  tableExistenceObservation{metadataPrivilege: 1},
		},
		{
			name:      "mysql hidden absent is unknown",
			engine:    "mysql",
			wantError: true,
		},
		{
			name:   "postgres missing schema is absent",
			engine: "postgresql",
			facts:  tableExistenceObservation{},
		},
		{
			name:      "postgres existing schema without usage is unknown",
			engine:    "postgresql",
			facts:     tableExistenceObservation{schemaCount: 1},
			wantError: true,
		},
		{
			name:   "postgres visible absent",
			engine: "postgresql",
			facts:  tableExistenceObservation{schemaCount: 1, metadataPrivilege: 1},
		},
		{
			name:      "postgres present without usage is unknown",
			engine:    "postgresql",
			facts:     tableExistenceObservation{tableCount: 1, schemaCount: 1},
			wantError: true,
		},
		{
			name:   "postgres present with usage",
			engine: "postgresql",
			facts: tableExistenceObservation{
				tableCount: 1, schemaCount: 1, metadataPrivilege: 1,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyTableExistence(tt.engine, tt.facts)
			if tt.wantError {
				if !errors.Is(err, ErrMetadataVisibilityUnproven) {
					t.Fatalf("error = %v, want ErrMetadataVisibilityUnproven", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("result = %v, %v, want %v, nil", got, err, tt.want)
			}
		})
	}
}
