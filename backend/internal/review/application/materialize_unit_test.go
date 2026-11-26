package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

func TestBuildSQLStatementModelTypedShapeParity(t *testing.T) {
	res := fingerprint.Result{
		AlgorithmVersion:         "v1",
		DialectProfile:           "mysql/mysql",
		StatementKind:            "update",
		FingerprintKeyID:         "fp-1",
		CanonicalFingerprintHMAC: strings.Repeat("a", 64),
		DialectFingerprintHMAC:   strings.Repeat("b", 64),
		ExecutionShapeHMAC:       strings.Repeat("c", 64),
		AnonymizedSQL:            "UPDATE tbl_1 SET col_1 = ? WHERE col_2 = ?",
		MetadataContextComplete:  true,
		TupleCount:               0,
		ParameterCount:           2,
		Quality:                  fingerprint.QualityExact,
		LiteralTypeCounts:        map[string]int{},
		Validity: fingerprint.StatementValidity{
			Complete:            true,
			DefinitionsComplete: true,
			Operation:           fingerprint.ValidityOperationUpdate,
			Requirements: []fingerprint.ValidityRequirement{
				{
					Object:    fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: "tbl_1"},
					State:     fingerprint.ValidityRequirementPresent,
					Condition: fingerprint.ValidityConditionNone,
				},
			},
		},
	}
	objs := []fingerprint.ObjectReference{
		{
			Token:                  "tbl_1",
			Kind:                   "table",
			Relation:               fingerprint.RelationTarget,
			StatementOrdinal:       7,
			StatementOrdinalRanges: []fingerprint.StatementOrdinalRange{{Start: 7, End: 7}},
		},
	}

	row, err := buildSQLStatementModel("run-1", "stmt-1", 7, res, "update", "dml", objs)
	if err != nil {
		t.Fatalf("buildSQLStatementModel: %v", err)
	}
	wantTyped, err := json.Marshal(map[string]any{
		"kind":                      res.StatementKind,
		"operation":                 "update",
		"dialect_profile":           res.DialectProfile,
		"quality":                   res.Quality,
		"tuple_count":               res.TupleCount,
		"parameter_count":           res.ParameterCount,
		"anonymized_sql":            res.AnonymizedSQL,
		"objects":                   objs,
		"statement_ordinal":         int32(7),
		"metadata_context_complete": res.MetadataContextComplete,
		"execution_validity":        res.Validity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(row.TypedShape) != string(wantTyped) {
		t.Fatalf("TypedShape JSON mismatch:\ngot:  %s\nwant: %s", row.TypedShape, wantTyped)
	}
	if string(row.LiteralTypeCounts) != "{}" {
		t.Fatalf("empty LiteralTypeCounts = %q, want {}", row.LiteralTypeCounts)
	}

	// Empty objects slice must serialize as [] (not null), matching objectsAtStatementOrdinal.
	res.LiteralTypeCounts = map[string]int{"int": 2}
	rowEmptyObjs, err := buildSQLStatementModel("run-1", "stmt-2", 8, res, "update", "dml", []fingerprint.ObjectReference{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rowEmptyObjs.TypedShape, &decoded); err != nil {
		t.Fatal(err)
	}
	rawObjs, ok := decoded["objects"].([]any)
	if !ok || len(rawObjs) != 0 {
		t.Fatalf("expected non-nil empty objects array, got %#v", decoded["objects"])
	}
	if string(rowEmptyObjs.LiteralTypeCounts) != `{"int":2}` {
		t.Fatalf("populated LiteralTypeCounts = %s, want {\"int\":2}", rowEmptyObjs.LiteralTypeCounts)
	}
}

func TestOperationLowerMatchesOperationOf(t *testing.T) {
	samples := [][]byte{
		[]byte("SELECT 1"),
		[]byte("  insert into t values (1)"),
		[]byte("-- comment\n/* block */ (Update t set a=1)"),
		[]byte("DELETE FROM t WHERE id = 1"),
		[]byte("CREATE TABLE t (id int)"),
		[]byte("ALTER TABLE t ADD COLUMN c int"),
		[]byte("DROP TABLE t"),
		[]byte("TRUNCATE TABLE t"),
		[]byte("WITH cte AS (SELECT 1) SELECT * FROM cte"),
		[]byte("EXPLAIN ANALYZE SELECT 1"),
		[]byte("SHOW TABLES"),
		[]byte("SUPERLONGSTATEMENTVERBNAME_123456 SELECT 1"),
		[]byte("   \n\t  "),
		[]byte(";"),
		[]byte("§"),
	}
	for _, sqlText := range samples {
		want := strings.ToLower(operationOf(sqlText))
		got := operationLower(sqlText)
		if got != want {
			t.Fatalf("operationLower(%q) = %q, want %q", sqlText, got, want)
		}
	}
}

type unnestStubCall struct {
	query string
	args  []any
}

type unnestStubFx struct {
	calls []unnestStubCall
}

func (f *unnestStubFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *unnestStubFx) Driver() driver.Driver                        { return unnestStubDriver{} }

type unnestStubDriver struct{}

func (unnestStubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused open") }
func (f *unnestStubFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused prepare")
}
func (f *unnestStubFx) Close() error              { return nil }
func (f *unnestStubFx) Begin() (driver.Tx, error) { return &unnestStubTx{}, nil }
func (f *unnestStubFx) CheckNamedValue(*driver.NamedValue) error {
	// Matches pgx/v5/stdlib CheckNamedValue allowing slice array args directly.
	return nil
}
func (f *unnestStubFx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	f.calls = append(f.calls, unnestStubCall{query: query, args: vals})
	return driver.RowsAffected(1), nil
}
func (f *unnestStubFx) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("unused query")
}

type unnestStubTx struct{}

func (*unnestStubTx) Commit() error   { return nil }
func (*unnestStubTx) Rollback() error { return nil }

func TestInsertSQLStatementsBulkTxPostgresUnnest(t *testing.T) {
	fx := &unnestStubFx{}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}

	const totalRows = 2500
	rows := make([]persistence.SQLStatementModel, totalRows)
	for i := range rows {
		rows[i] = persistence.SQLStatementModel{
			ID:                       "0198d9cc-0000-7000-8000-000000000001",
			ReviewRunID:              "0198d9cc-0000-7000-8000-000000000002",
			Ordinal:                  int32(i + 1),
			StatementKind:            "dml",
			Operation:                "update",
			SizeBytes:                42,
			AlgorithmVersion:         "v1",
			DialectProfile:           "mysql/mysql",
			FingerprintQuality:       "exact",
			FingerprintKeyID:         "fp-1",
			CanonicalFingerprintHMAC: strings.Repeat("a", 64),
			DialectFingerprintHMAC:   strings.Repeat("b", 64),
			ExecutionShapeHMAC:       strings.Repeat("c", 64),
			TypedShape:               []byte(`{"kind":"update"}`),
			LiteralTypeCounts:        nil,
			TupleCount:               0,
			ParameterCount:           1,
		}
	}

	if err := insertSQLStatementsBulkTx(context.Background(), db, rows); err != nil {
		t.Fatalf("insertSQLStatementsBulkTx: %v", err)
	}
	if len(fx.calls) != 2 {
		t.Fatalf("expected 2 UNNEST batches for 2500 rows (2048 + 452), got %d", len(fx.calls))
	}
	for batchIdx, wantLen := range []int{2048, 452} {
		call := fx.calls[batchIdx]
		if !strings.Contains(strings.ToLower(call.query), "from unnest(") {
			t.Fatalf("batch %d expected UNNEST insert query, got %s", batchIdx, call.query)
		}
		if len(call.args) != 17 {
			t.Fatalf("batch %d expected 17 array parameters, got %d", batchIdx, len(call.args))
		}
		ids, ok := call.args[0].([]string)
		if !ok || len(ids) != wantLen {
			t.Fatalf("batch %d arg[0] type=%T len=%d, want []string len=%d", batchIdx, call.args[0], len(ids), wantLen)
		}
		ordinals, ok := call.args[2].([]int32)
		if !ok || len(ordinals) != wantLen {
			t.Fatalf("batch %d arg[2] type=%T len=%d, want []int32 len=%d", batchIdx, call.args[2], len(ordinals), wantLen)
		}
		litCounts, ok := call.args[14].([]string)
		if !ok || len(litCounts) != wantLen || litCounts[0] != "{}" {
			t.Fatalf("batch %d arg[14] literal_type_counts = %T len=%d first=%q, want {}", batchIdx, call.args[14], len(litCounts), litCounts[0])
		}
	}
	if got := fx.calls[1].args[2].([]int32)[451]; got != 2500 {
		t.Fatalf("last batch final ordinal = %d, want 2500", got)
	}
}
