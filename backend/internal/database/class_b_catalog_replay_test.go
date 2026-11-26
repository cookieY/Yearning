package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

type classBReplayResponse struct {
	cols []string
	rows [][]driver.Value
	err  error
}

type classBReplayConnector struct {
	recordedQueries *[]string
	responder       func(query string) classBReplayResponse
}

func (c *classBReplayConnector) Connect(context.Context) (driver.Conn, error) {
	return &classBReplayConn{recordedQueries: c.recordedQueries, responder: c.responder}, nil
}
func (c *classBReplayConnector) Driver() driver.Driver { return nil }

type classBReplayConn struct {
	recordedQueries *[]string
	responder       func(query string) classBReplayResponse
}

func (c *classBReplayConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *classBReplayConn) Close() error                        { return nil }
func (c *classBReplayConn) Begin() (driver.Tx, error)           { return &classBReplayTx{}, nil }
func (c *classBReplayConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &classBReplayTx{}, nil
}
func (c *classBReplayConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (c *classBReplayConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.recordedQueries != nil {
		*c.recordedQueries = append(*c.recordedQueries, query)
	}
	resp := c.responder(query)
	if resp.err != nil {
		return nil, resp.err
	}
	return &classBReplayRows{cols: resp.cols, rows: resp.rows}, nil
}

type classBReplayTx struct{}

func (*classBReplayTx) Commit() error   { return nil }
func (*classBReplayTx) Rollback() error { return nil }

type classBReplayRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *classBReplayRows) Columns() []string { return r.cols }
func (r *classBReplayRows) Close() error      { return nil }
func (r *classBReplayRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func openClassBReplayPool(engine string, recorded *[]string, responder func(string) classBReplayResponse) *PurposePool {
	db := sql.OpenDB(&classBReplayConnector{recordedQueries: recorded, responder: responder})
	return &PurposePool{
		engine:  engine,
		purpose: PurposeReview,
		db:      db,
	}
}

func TestClassBCatalogMetadataRecordReplay(t *testing.T) {
	// 1. DEF-21 root@9481dd4 guard: MySQL 5.7 lacks CTEs; mysqlPolicyCatalogSQL must avoid WITH CTEs
	// and apply direct table_schema IN (...) pruning on every information_schema branch.
	qSQL, _, err := policyCatalogQuery("mysql", 2)
	if err != nil {
		t.Fatalf("policyCatalogQuery(mysql, 2): %v", err)
	}
	upperQ := strings.ToUpper(qSQL)
	if strings.Contains(upperQ, "WITH REQUESTED") || strings.Contains(upperQ, "\nWITH ") {
		t.Fatalf("mysqlPolicyCatalogSQL must remain non-CTE for MySQL 5.7 compatibility: %s", qSQL)
	}
	for _, pruning := range []string{"t.table_schema IN (", "c.table_schema IN (", "st.table_schema IN (", "tc.table_schema IN ("} {
		if !strings.Contains(qSQL, pruning) {
			t.Fatalf("mysqlPolicyCatalogSQL missing branch pruning %q", pruning)
		}
	}

	ctx := context.Background()

	// 2. MySQL 5.7 uppercase INFORMATION_SCHEMA metadata replay across ReadPolicyCatalog and Adapter readers.
	var mysql57Queries []string
	mysql57Pool := openClassBReplayPool("mysql", &mysql57Queries, func(query string) classBReplayResponse {
		up := strings.ToUpper(query)
		switch {
		case strings.Contains(up, "FROM INFORMATION_SCHEMA.SCHEMATA") && strings.Contains(up, "UNION ALL"):
			return classBReplayResponse{
				cols: []string{"ROW_KIND", "KIND", "SCHEMA_NAME", "RELATION_NAME", "OBJECT_NAME", "SCHEMA_STATUS"},
				rows: [][]driver.Value{
					{"_schema_status", "", "shop_57", "", "", "visible"},
					{"identifier", "schema", "shop_57", "", "shop_57", ""},
					{"identifier", "table", "shop_57", "", "orders", ""},
					{"identifier", "column", "shop_57", "orders", "id", ""},
					{"identifier", "index", "shop_57", "orders", "PRIMARY", ""},
				},
			}
		case strings.Contains(up, "FROM INFORMATION_SCHEMA.SCHEMATA"):
			return classBReplayResponse{cols: []string{"SCHEMA_NAME"}, rows: [][]driver.Value{{"shop_57"}}}
		case strings.Contains(up, "FROM INFORMATION_SCHEMA.TABLES"):
			return classBReplayResponse{cols: []string{"TABLE_NAME"}, rows: [][]driver.Value{{"orders"}}}
		case strings.Contains(up, "FROM INFORMATION_SCHEMA.COLUMNS"):
			return classBReplayResponse{
				cols: []string{"COLUMN_NAME", "ORDINAL_POSITION", "DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY", "COLUMN_DEFAULT", "EXTRA", "COLUMN_COMMENT"},
				rows: [][]driver.Value{{"id", int64(1), "bigint", false, "PRI", "", "auto_increment", ""}},
			}
		default:
			return classBReplayResponse{}
		}
	})
	defer func() { _ = mysql57Pool.Close() }()

	mysqlAdapter := NewMySQLAdapter(Endpoint{Host: "mysql57.internal", Port: 3306, CompatibilityMod: "mysql"}, AdapterOptions{})
	cat57, err := ReadPolicyCatalog(ctx, mysqlAdapter, mysql57Pool, []string{"shop_57"})
	if err != nil {
		t.Fatalf("ReadPolicyCatalog(mysql 5.7 uppercase replay): %v", err)
	}
	if cat57.SchemaCount != 1 || len(cat57.Identifiers) != 4 {
		t.Fatalf("mysql 5.7 catalog unexpected: %+v", cat57)
	}
	schemas, err := mysqlAdapter.ListSchemas(ctx, mysql57Pool)
	if err != nil || len(schemas) != 1 || schemas[0] != "shop_57" {
		t.Fatalf("ListSchemas(mysql 5.7 uppercase) = %v, err=%v", schemas, err)
	}
	tables, err := mysqlAdapter.ListTables(ctx, mysql57Pool, "shop_57")
	if err != nil || len(tables) != 1 || tables[0] != "orders" {
		t.Fatalf("ListTables(mysql 5.7 uppercase) = %v, err=%v", tables, err)
	}
	cols, err := mysqlAdapter.ListColumns(ctx, mysql57Pool, "shop_57", "orders")
	if err != nil || len(cols) != 1 || cols[0].ColumnName != "id" {
		t.Fatalf("ListColumns(mysql 5.7 uppercase) = %+v, err=%v", cols, err)
	}

	// 3. MySQL 8.4 LTS / MariaDB 10.11 / PolarDB MySQL catalog replay (lowercased headers).
	for _, flavor := range []string{"mysql_8_4_lts", "mariadb_10_11", "polardb_mysql"} {
		t.Run(flavor, func(t *testing.T) {
			pool := openClassBReplayPool("mysql", nil, func(query string) classBReplayResponse {
				if strings.Contains(strings.ToUpper(query), "UNION ALL") {
					return classBReplayResponse{
						cols: []string{"row_kind", "kind", "schema_name", "relation_name", "object_name", "schema_status"},
						rows: [][]driver.Value{
							{"_schema_status", "", "app_db", "", "", "visible"},
							{"identifier", "table", "app_db", "", "ledger", ""},
						},
					}
				}
				return classBReplayResponse{}
			})
			defer func() { _ = pool.Close() }()
			cat, err := ReadPolicyCatalog(ctx, mysqlAdapter, pool, []string{"app_db"})
			if err != nil || len(cat.Identifiers) != 1 || cat.Identifiers[0].Name != "ledger" {
				t.Fatalf("%s ReadPolicyCatalog replay failed: cat=%+v err=%v", flavor, cat, err)
			}
		})
	}

	// 4. TiDB EXPLAIN replay.
	tidbFact, err := parseMySQLExplain(
		[]string{"id", "estRows", "task", "access object", "operator info"},
		[][]string{
			{"IndexLookUp_10", "128.00", "root", "", "limit(offset:0, count:10)"},
			{"└─IndexRangeScan_8", "128.00", "cop[tikv]", "table:orders, index:idx_tenant_status(tenant_id, status)", "range:[42,42]"},
		},
	)
	if err != nil || tidbFact.EstimatedRows != 128 || len(tidbFact.IndexNames) != 1 || tidbFact.IndexNames[0] != "idx_tenant_status" {
		t.Fatalf("TiDB EXPLAIN replay mismatch: fact=%+v err=%v", tidbFact, err)
	}

	// 5. OceanBase MySQL mode ER 1193 sysvar tolerance & Query Plan replay.
	obPool := &PurposePool{engine: "mysql", purpose: PurposeExecution, opts: AdapterOptions{StatementTimeoutMS: 30000, LockTimeoutMS: 2000}}
	obErrRec := &recordingExecer{fail: map[string]error{
		"SET max_execution_time = 30000":   &mysql.MySQLError{Number: 1193, Message: "Unknown system variable 'max_execution_time'"},
		"SET innodb_lock_wait_timeout = 2": &mysql.MySQLError{Number: 1193, Message: "Unknown system variable 'innodb_lock_wait_timeout'"},
	}}
	if err := obPool.applySessionDefaults(ctx, obErrRec); err != nil {
		t.Fatalf("OceanBase ER 1193 tolerance failed: %v", err)
	}
	obFact, err := parseMySQLExplain(
		[]string{"Query Plan"},
		[][]string{{strings.Join([]string{
			"|ID|OPERATOR       |NAME                 |EST.ROWS|EST.TIME(us)|",
			"-------------------------------------------------------------------",
			"|0 |TABLE RANGE GET|ledger(idx_acct)     |250     |12          |",
			"===================================================================",
		}, "\n")}},
	)
	if err != nil || obFact.EstimatedRows != 250 || len(obFact.IndexNames) != 1 || obFact.IndexNames[0] != "idx_acct" {
		t.Fatalf("OceanBase EXPLAIN replay mismatch: fact=%+v err=%v", obFact, err)
	}

	// 6. PolarDB PostgreSQL catalog replay.
	pgAdapter := NewPostgreSQLAdapter(Endpoint{Host: "polardb-pg.internal", Port: 5432, CompatibilityMod: "postgresql"}, AdapterOptions{})
	pgPool := openClassBReplayPool("postgresql", nil, func(query string) classBReplayResponse {
		return classBReplayResponse{
			cols: []string{"row_kind", "kind", "schema_name", "relation_name", "object_name", "schema_status"},
			rows: [][]driver.Value{
				{"_schema_status", "", "public", "", "", "visible"},
				{"identifier", "table", "public", "", "orders", ""},
				{"identifier", "column", "public", "orders", "external_code", ""},
			},
		}
	})
	defer func() { _ = pgPool.Close() }()
	catPG, err := ReadPolicyCatalog(ctx, pgAdapter, pgPool, []string{"public"})
	if err != nil || catPG.SchemaCount != 1 || len(catPG.Identifiers) != 2 {
		t.Fatalf("PolarDB PG ReadPolicyCatalog replay failed: cat=%+v err=%v", catPG, err)
	}
}
