package database

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestExplainCandidateRejectsExecutionPrefixesBeforePoolAccess(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql"} {
		for _, statement := range []string{
			"ANALYZE DELETE FROM t WHERE id=1", "EXPLAIN ANALYZE DELETE FROM t WHERE id=1",
			"/* misleading */ ANALYSE DELETE FROM t WHERE id=1", "(ANALYZE DELETE FROM t)",
			"VERBOSE DELETE FROM t", "EXECUTE prepared_statement", "SELECT 1; DELETE FROM t",
			"SELECT 1;", "/* unclosed", "-- comment only", "",
		} {
			t.Run(engine+"/"+statement, func(t *testing.T) {
				adapter := &baseAdapter{ep: Endpoint{CompatibilityMod: engine}}
				if _, err := adapter.ExplainWithoutExecution(context.Background(), nil, "app", statement); err == nil {
					t.Fatal("unsafe candidate reached the database pool")
				}
			})
		}
	}
}

func TestExplainFinalSQLHasOnlyFixedOptions(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql"} {
		for _, statement := range []string{
			"SELECT 'ANALYZE; DELETE FROM t'", "/* ANALYZE */ DELETE FROM t WHERE id=1",
			"WITH x AS (SELECT 1) SELECT * FROM x", "UPDATE t SET value=1 WHERE id=1",
		} {
			candidate, err := nonExecutingExplainSQL(engine, statement)
			if err != nil {
				t.Fatal(err)
			}
			if candidate != explainPrefix(engine)+strings.TrimSpace(statement) {
				t.Fatalf("candidate changed statement: %q", candidate)
			}
		}
	}
	if _, err := nonExecutingExplainSQL("mysql", "/*!80000 ANALYZE */ DELETE FROM t"); err == nil {
		t.Fatal("executable comment smuggled an execution prefix")
	}
}

func TestParseMySQLExplain_NativeAndPolarDB(t *testing.T) {
	columns := []string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}
	rows := [][]string{
		{"1", "UPDATE", "orders", "NULL", "range", "idx_user,idx_status", "idx_user", "8", "const", "1250", "100.00", "Using where"},
		{"2", "DEPENDENT SUBQUERY", "audit_logs", "NULL", "ALL", "NULL", "idx_a,idx_b", "NULL", "NULL", "4800", "10.00", "Using where"},
	}
	fact, err := parseMySQLExplain(columns, rows)
	if err != nil {
		t.Fatalf("parseMySQLExplain failed: %v", err)
	}
	if fact.AccessMethod != "range" {
		t.Fatalf("AccessMethod = %q, want range", fact.AccessMethod)
	}
	if fact.EstimatedRows != 4800 {
		t.Fatalf("EstimatedRows = %d, want 4800", fact.EstimatedRows)
	}
	wantIndexes := []string{"idx_user", "idx_a", "idx_b"}
	if !reflect.DeepEqual(fact.IndexNames, wantIndexes) {
		t.Fatalf("IndexNames = %v, want %v", fact.IndexNames, wantIndexes)
	}
}

func TestParseMySQLExplain_OceanBase4xAnd3x(t *testing.T) {
	t.Run("oceanbase 4.x multi-row query plan with secondary index", func(t *testing.T) {
		columns := []string{"Query Plan"}
		rows := [][]string{
			{"============================================================="},
			{"|ID|OPERATOR                 |NAME            |EST.ROWS|EST.TIME(us)|"},
			{"-------------------------------------------------------------"},
			{"|0 |UPDATE                   |                |2500    |4512        |"},
			{"|1 |└─TABLE RANGE SCAN       |orders(idx_uid,Reverse)|2500 |3210    |"},
			{"============================================================="},
			{"Outputs & filters:"},
			{"-------------------------------------------------------------"},
			{"  0 - output(nil), filter(nil)"},
			{"  1 - output([orders.id]), filter([orders.user_id = 42])"},
		}
		fact, err := parseMySQLExplain(columns, rows)
		if err != nil {
			t.Fatalf("parseMySQLExplain failed: %v", err)
		}
		if fact.AccessMethod != "TABLE RANGE SCAN" {
			t.Fatalf("AccessMethod = %q, want TABLE RANGE SCAN", fact.AccessMethod)
		}
		if fact.EstimatedRows != 2500 {
			t.Fatalf("EstimatedRows = %d, want 2500", fact.EstimatedRows)
		}
		if !reflect.DeepEqual(fact.IndexNames, []string{"idx_uid"}) {
			t.Fatalf("IndexNames = %v, want [idx_uid]", fact.IndexNames)
		}
	})

	t.Run("oceanbase 3.x single-cell multi-line query plan with full table scan", func(t *testing.T) {
		columns := []string{"Query Plan"}
		planText := strings.Join([]string{
			"================================================",
			"|ID|OPERATOR        |NAME      |EST. ROWS|COST |",
			"------------------------------------------------",
			"|0 |TABLE FULL SCAN |orders    |15000    |1923 |",
			"================================================",
		}, "\n")
		fact, err := parseMySQLExplain(columns, [][]string{{planText}})
		if err != nil {
			t.Fatalf("parseMySQLExplain failed: %v", err)
		}
		if fact.AccessMethod != "TABLE FULL SCAN" {
			t.Fatalf("AccessMethod = %q, want TABLE FULL SCAN", fact.AccessMethod)
		}
		if fact.EstimatedRows != 15000 {
			t.Fatalf("EstimatedRows = %d, want 15000", fact.EstimatedRows)
		}
		if fact.EstimatedCost != 1923 {
			t.Fatalf("EstimatedCost = %v, want 1923", fact.EstimatedCost)
		}
		// Bare table name "orders" without parentheses must never be added as an index name.
		if len(fact.IndexNames) != 0 {
			t.Fatalf("IndexNames = %v, want empty for bare table name", fact.IndexNames)
		}
	})
}

func TestParseMySQLExplain_TiDB(t *testing.T) {
	columns := []string{"id", "estRows", "task", "access object", "operator info"}
	rows := [][]string{
		{"Update_4", "N/A", "root", "", "N/A"},
		{"└─IndexLookUp_11", "3200.00", "root", "", ""},
		{"  ├─IndexRangeScan_8(Build)", "3200.00", "cop[tikv]", "table:orders, index:idx_orders_status(status)", "range:[1,1], keep order:false"},
		{"  └─TableRowIDScan_9(Probe)", "3200.00", "cop[tikv]", "table:orders", "keep order:false"},
	}
	fact, err := parseMySQLExplain(columns, rows)
	if err != nil {
		t.Fatalf("parseMySQLExplain failed: %v", err)
	}
	if fact.AccessMethod != "IndexLookUp" {
		t.Fatalf("AccessMethod = %q, want IndexLookUp", fact.AccessMethod)
	}
	if fact.EstimatedRows != 3200 {
		t.Fatalf("EstimatedRows = %d, want 3200", fact.EstimatedRows)
	}
	if !reflect.DeepEqual(fact.IndexNames, []string{"idx_orders_status"}) {
		t.Fatalf("IndexNames = %v, want [idx_orders_status]", fact.IndexNames)
	}
}
