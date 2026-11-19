package privacy

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func TestValidateRawAssistantSQL(t *testing.T) {
	mysqlProf := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	pgProf := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	ledgerScope := RawSQLScope{
		AuthorizedSchema: "ledger_db",
		AllowedColumnsByTable: map[string]map[string]map[string]struct{}{
			"ledger_db": {
				"transfers": {
					"id": {}, "status": {}, "amount": {}, "account_id": {}, "created_at": {},
				},
			},
		},
	}
	ordersScope := RawSQLScope{
		AuthorizedSchema: "public",
		AllowedColumnsByTable: map[string]map[string]map[string]struct{}{
			"public": {
				"orders": {
					"id": {}, "amount": {}, "user_id": {}, "status": {}, "note": {},
				},
			},
		},
	}

	t.Run("valid select extracts referenced tables and warnings", func(t *testing.T) {
		res, err := ValidateRawAssistantSQL("SELECT * FROM ledger_db.transfers WHERE status = 'pending'", mysqlProf, ledgerScope)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.StatementKind != domain.StmtSelect || res.RiskCategory != domain.RiskReadOnly {
			t.Fatalf("unexpected classification: kind=%s risk=%s", res.StatementKind, res.RiskCategory)
		}
		if len(res.ReferencedTables) != 1 || res.ReferencedTables[0] != "ledger_db.transfers" {
			t.Fatalf("unexpected referenced tables: %+v", res.ReferencedTables)
		}
		if len(res.SemanticWarnings) == 0 {
			t.Fatalf("expected SELECT * warning for unbounded query")
		}
	})

	t.Run("valid dml/ddl classifies write risk and pg qualified table", func(t *testing.T) {
		res, err := ValidateRawAssistantSQL("UPDATE public.orders SET amount = 10 WHERE id = 42", pgProf, ordersScope)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.StatementKind != domain.StmtUpdate || res.RiskCategory != domain.RiskDML {
			t.Fatalf("unexpected classification: kind=%s risk=%s", res.StatementKind, res.RiskCategory)
		}
		if len(res.ReferencedTables) != 1 || res.ReferencedTables[0] != "public.orders" {
			t.Fatalf("unexpected referenced tables: %+v", res.ReferencedTables)
		}

		ddlRes, err := ValidateRawAssistantSQL("CREATE INDEX idx_orders_uid ON public.orders (user_id)", pgProf, ordersScope)
		if err != nil {
			t.Fatalf("unexpected DDL error: %v", err)
		}
		if ddlRes.StatementKind != domain.StmtCreateIndex || ddlRes.RiskCategory != domain.RiskDDL {
			t.Fatalf("unexpected DDL classification: kind=%s risk=%s", ddlRes.StatementKind, ddlRes.RiskCategory)
		}
		if len(ddlRes.ReferencedTables) != 1 || ddlRes.ReferencedTables[0] != "public.orders" {
			t.Fatalf("unexpected DDL referenced tables: %+v", ddlRes.ReferencedTables)
		}
	})

	t.Run("supports aliases, CTEs, catalog lazy lookup, EXTRACT FROM, and authorized DDL", func(t *testing.T) {
		scopeWithLookup := RawSQLScope{
			AuthorizedSchema:      "ledger_db",
			AllowedColumnsByTable: ledgerScope.AllowedColumnsByTable,
			LookupTableColumns: func(schema, table string) ([]string, bool) {
				if schema == "ledger_db" && table == "accounts" {
					return []string{"id", "owner_name"}, true
				}
				return nil, false
			},
		}
		validCases := []struct {
			sql     string
			profile fingerprint.Profile
			scope   RawSQLScope
		}{
			{
				sql:     "SELECT t.status AS st, EXTRACT(YEAR FROM t.created_at) AS yr FROM ledger_db.transfers t ORDER BY st",
				profile: mysqlProf,
				scope:   ledgerScope,
			},
			{
				sql:     "SELECT t.id, a.owner_name FROM ledger_db.transfers t JOIN ledger_db.accounts a ON a.id = t.account_id",
				profile: mysqlProf,
				scope:   scopeWithLookup,
			},
			{
				sql:     "WITH pending AS (SELECT id, status FROM ledger_db.transfers WHERE status = 'pending') SELECT p.id, p.status FROM pending p",
				profile: mysqlProf,
				scope:   ledgerScope,
			},
			{
				sql:     "CREATE TABLE ledger_db.audit_log (id BIGINT PRIMARY KEY, note VARCHAR(255) NOT NULL)",
				profile: mysqlProf,
				scope:   ledgerScope,
			},
			{
				sql:     "ALTER TABLE public.orders ADD COLUMN priority INT DEFAULT 0",
				profile: pgProf,
				scope:   ordersScope,
			},
		}
		for _, tc := range validCases {
			if _, err := ValidateRawAssistantSQL(tc.sql, tc.profile, tc.scope); err != nil {
				t.Fatalf("expected valid SQL %q to pass: %v", tc.sql, err)
			}
		}
	})

	t.Run("rejects forbidden multi-statement, dangerous DDL, outfile, unbound params, leaked anon tokens, and unauthorized scope", func(t *testing.T) {
		cases := []struct {
			name  string
			sql   string
			scope RawSQLScope
		}{
			{name: "empty_scope_fail_closed", sql: "SELECT id FROM ledger_db.transfers", scope: RawSQLScope{}},
			{name: "empty", sql: "   ", scope: ledgerScope},
			{name: "multi_statement", sql: "SELECT id FROM ledger_db.transfers; SELECT 2", scope: ledgerScope},
			{name: "truncate", sql: "TRUNCATE TABLE ledger_db.transfers", scope: ledgerScope},
			{name: "drop_database", sql: "DROP DATABASE ledger_db", scope: ledgerScope},
			{name: "grant", sql: "GRANT ALL ON ledger_db.transfers TO 'app'", scope: ledgerScope},
			{name: "into_outfile", sql: "SELECT id INTO OUTFILE '/tmp/x' FROM ledger_db.transfers", scope: ledgerScope},
			{name: "colon_val_placeholder", sql: "SELECT id FROM ledger_db.transfers WHERE id = :val_num_1", scope: ledgerScope},
			{name: "pg_positional_param", sql: "SELECT id FROM ledger_db.transfers WHERE id = $1", scope: ledgerScope},
			{name: "question_mark_param", sql: "SELECT id FROM ledger_db.transfers WHERE id = ?", scope: ledgerScope},
			{name: "unreplaced_anon_table", sql: "SELECT id FROM table_1 WHERE column_1 = 1", scope: ledgerScope},
			{name: "cross_schema_mysql_user", sql: "SELECT user, authentication_string FROM mysql.user", scope: ledgerScope},
			{name: "cross_schema_3part_col", sql: "SELECT other_db.transfers.id FROM ledger_db.transfers", scope: ledgerScope},
			{name: "cross_schema_create_table", sql: "CREATE TABLE mysql.evil (id INT)", scope: ledgerScope},
			{name: "create_table_as_select_unauthorized", sql: "CREATE TABLE ledger_db.evil AS SELECT user FROM mysql.user", scope: ledgerScope},
			{name: "unauthorized_same_schema_table", sql: "SELECT id FROM ledger_db.secret_audit", scope: ledgerScope},
			{name: "unauthorized_unqualified_table", sql: "SELECT id FROM users", scope: ledgerScope},
			{name: "hallucinated_column", sql: "SELECT password_hash FROM ledger_db.transfers", scope: ledgerScope},
			{name: "hallucinated_qualified_column", sql: "SELECT t.ssn FROM ledger_db.transfers t", scope: ledgerScope},
			{name: "hallucinated_cte_column", sql: "WITH c AS (SELECT status FROM ledger_db.transfers) SELECT c.evil_col FROM c", scope: ledgerScope},
			{name: "unauthorized_udf", sql: "SELECT pg_read_file('/etc/passwd') FROM ledger_db.transfers", scope: ledgerScope},
		}
		for _, tc := range cases {
			if _, err := ValidateRawAssistantSQL(tc.sql, mysqlProf, tc.scope); err == nil {
				t.Errorf("%s: expected error for %q", tc.name, tc.sql)
			}
		}
	})
}

func TestValidateAndCanonicalizeRawSQL_AdditionalCoverage(t *testing.T) {
	mysqlProf := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	pgProf := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	mixedCaseScope := RawSQLScope{
		AuthorizedSchema: "inventory_core",
		AllowedColumnsByTable: map[string]map[string]map[string]struct{}{
			"Inventory_Core": {
				"orders": {
					"id": {}, "user_id": {}, "status": {}, "amount": {}, "note": {},
				},
				"order_items": {
					"id": {}, "order_id": {}, "user_id": {}, "sku": {},
				},
			},
		},
	}

	validCases := []struct {
		name    string
		sql     string
		profile fingerprint.Profile
		scope   RawSQLScope
	}{
		{
			name:    "mixed_case_schema_and_repeated_table_cache_hit",
			sql:     "SELECT o.id, o2.status FROM inventory_core.orders o JOIN inventory_core.orders o2 ON o2.user_id = o.user_id",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "pg_3part_qualified_column_and_star",
			sql:     "SELECT inventory_core.orders.id, o.* FROM inventory_core.orders AS o",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "cte_def_list_and_alias",
			sql:     "WITH active_orders(order_num) AS (SELECT id FROM inventory_core.orders WHERE status = 'paid'), joined AS (SELECT order_num FROM active_orders) SELECT j.order_num FROM joined j",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "join_using_comma_from_and_select_alias",
			sql:     "SELECT o.id AS order_pk, i.sku FROM inventory_core.orders o JOIN inventory_core.order_items i USING (user_id) WHERE o.status = 'open'",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "insert_on_conflict_excluded",
			sql:     "INSERT INTO inventory_core.orders (id, user_id, status) VALUES (1, 2, 'paid') ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "update_without_where_warning_and_delete",
			sql:     "UPDATE inventory_core.orders SET status = 'archived'",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "delete_with_where",
			sql:     "DELETE FROM inventory_core.orders WHERE id = 10",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "alter_table_add_and_rename_column_and_quoted_ident",
			sql:     "ALTER TABLE `inventory_core`.`orders` ADD COLUMN IF NOT EXISTS note VARCHAR(64)",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "alter_table_rename_column",
			sql:     "ALTER TABLE inventory_core.orders RENAME COLUMN note TO memo_text",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "create_table_with_constraint_and_alias_star",
			sql:     "CREATE TABLE inventory_core.audit_tmp (id BIGINT, ref_code VARCHAR(32), CONSTRAINT uk_audit_ref UNIQUE (ref_code))",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "trim_from_inside_function_parens",
			sql:     "SELECT TRIM(' ' FROM o.status) FROM inventory_core.orders o",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
	}

	for _, tc := range validCases {
		t.Run("valid_"+tc.name, func(t *testing.T) {
			res, err := ValidateRawAssistantSQL(tc.sql, tc.profile, tc.scope)
			if err != nil {
				t.Fatalf("expected valid SQL %q to succeed: %v", tc.sql, err)
			}
			if res == nil || len(res.ReferencedTables) == 0 {
				t.Fatalf("expected non-empty ReferencedTables, got %+v", res)
			}
		})
	}

	invalidCases := []struct {
		name    string
		sql     string
		profile fingerprint.Profile
		scope   RawSQLScope
	}{
		{
			name:    "invalid_profile",
			sql:     "SELECT id FROM inventory_core.orders",
			profile: fingerprint.Profile{Engine: "oracle", Mode: "oracle"},
			scope:   mixedCaseScope,
		},
		{
			name:    "3part_unknown_table_qualifier",
			sql:     "SELECT inventory_core.unknown_tbl.id FROM inventory_core.orders",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "3part_unauthorized_column",
			sql:     "SELECT inventory_core.orders.secret_col FROM inventory_core.orders",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "cte_alias_unauthorized_column",
			sql:     "WITH c AS (SELECT id FROM inventory_core.orders) SELECT ca.nope FROM c AS ca",
			profile: pgProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "ddl_new_table_unauthorized_column",
			sql:     "SELECT a.bad_col FROM inventory_core.orders o",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "lookup_missing_table_cached",
			sql:     "SELECT a.id FROM inventory_core.orders o JOIN inventory_core.nonexistent a ON a.id = o.id",
			profile: pgProf,
			scope: RawSQLScope{
				AuthorizedSchema: "inventory_core",
				LookupTableColumns: func(schema, table string) ([]string, bool) {
					if table == "orders" {
						return []string{"id"}, true
					}
					return nil, false
				},
			},
		},
		{
			name:    "create_table_empty_no_table_ref",
			sql:     "SELECT 1 + 1",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "join_using_bad_column",
			sql:     "SELECT o.id FROM inventory_core.orders o JOIN inventory_core.order_items i USING (secret_token)",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
		{
			name:    "comma_from_unauthorized_table",
			sql:     "SELECT o.id FROM inventory_core.orders o, inventory_core.secret_users u",
			profile: mysqlProf,
			scope:   mixedCaseScope,
		},
	}

	for _, tc := range invalidCases {
		t.Run("invalid_"+tc.name, func(t *testing.T) {
			if _, err := ValidateRawAssistantSQL(tc.sql, tc.profile, tc.scope); err == nil {
				t.Fatalf("expected error for invalid SQL %q", tc.sql)
			}
		})
	}
}
