package rollback

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestAnalyzeStatement_SupportedDMLAndDDL(t *testing.T) {
	cases := []struct {
		name     string
		engine   string
		schema   string
		sql      string
		wantKind StatementKind
		wantType string
		wantTbl  string
	}{
		{
			name:     "mysql_insert_values",
			engine:   "mysql",
			schema:   "demo",
			sql:      "INSERT INTO `accounts` (`id`, `name`, `balance`) VALUES (101, 'alice', 500), (102, 'bob', 800);",
			wantKind: KindInsert,
			wantType: OperationTypeDML,
			wantTbl:  "accounts",
		},
		{
			name:     "pg_update_where",
			engine:   "postgresql",
			schema:   "public",
			sql:      `UPDATE "public"."accounts" SET "balance" = 900, "name" = 'alice_v2' WHERE "id" = 101;`,
			wantKind: KindUpdate,
			wantType: OperationTypeDML,
			wantTbl:  "accounts",
		},
		{
			name:     "mysql_delete_where",
			engine:   "mysql",
			schema:   "demo",
			sql:      "DELETE FROM demo.accounts WHERE id IN (101, 102);",
			wantKind: KindDelete,
			wantType: OperationTypeDML,
			wantTbl:  "accounts",
		},
		{
			name:     "create_table",
			engine:   "mysql",
			schema:   "demo",
			sql:      "CREATE TABLE orders_archive (id BIGINT PRIMARY KEY, note VARCHAR(64) NOT NULL);",
			wantKind: KindCreateTable,
			wantType: OperationTypeDDL,
			wantTbl:  "orders_archive",
		},
		{
			name:     "add_column",
			engine:   "postgresql",
			schema:   "public",
			sql:      "ALTER TABLE accounts ADD COLUMN phone VARCHAR(32) DEFAULT 'N/A';",
			wantKind: KindAddColumn,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "rename_column",
			engine:   "mysql",
			schema:   "demo",
			sql:      "ALTER TABLE accounts RENAME COLUMN phone TO mobile_phone;",
			wantKind: KindRenameColumn,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "rename_table_mysql",
			engine:   "mysql",
			schema:   "demo",
			sql:      "RENAME TABLE accounts TO accounts_v2;",
			wantKind: KindRenameTable,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "rename_table_pg",
			engine:   "postgresql",
			schema:   "public",
			sql:      "ALTER TABLE accounts RENAME TO accounts_v2;",
			wantKind: KindRenameTable,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "create_index",
			engine:   "postgresql",
			schema:   "public",
			sql:      "CREATE UNIQUE INDEX idx_accounts_email ON accounts (email);",
			wantKind: KindAddIndex,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "drop_index_mysql",
			engine:   "mysql",
			schema:   "demo",
			sql:      "DROP INDEX idx_accounts_email ON accounts;",
			wantKind: KindDropIndex,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "modify_column_mysql",
			engine:   "mysql",
			schema:   "demo",
			sql:      "ALTER TABLE accounts MODIFY COLUMN name VARCHAR(255) NOT NULL;",
			wantKind: KindModifyColumn,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "add_constraint",
			engine:   "postgresql",
			schema:   "public",
			sql:      "ALTER TABLE accounts ADD CONSTRAINT chk_balance_nonneg CHECK (balance >= 0);",
			wantKind: KindAddConstraint,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "drop_constraint",
			engine:   "postgresql",
			schema:   "public",
			sql:      "ALTER TABLE accounts DROP CONSTRAINT chk_balance_nonneg;",
			wantKind: KindDropConstraint,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "drop_table_mysql",
			engine:   "mysql",
			schema:   "demo",
			sql:      "DROP TABLE accounts;",
			wantKind: KindDropTable,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "drop_table_pg",
			engine:   "postgresql",
			schema:   "public",
			sql:      "DROP TABLE public.accounts;",
			wantKind: KindDropTable,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "drop_column_mysql",
			engine:   "mysql",
			schema:   "demo",
			sql:      "ALTER TABLE accounts DROP COLUMN balance;",
			wantKind: KindDropColumn,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
		{
			name:     "drop_column_pg",
			engine:   "postgresql",
			schema:   "public",
			sql:      "ALTER TABLE accounts DROP COLUMN balance;",
			wantKind: KindDropColumn,
			wantType: OperationTypeDDL,
			wantTbl:  "accounts",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := AnalyzeStatement(tc.engine, tc.schema, tc.sql)
			if err != nil {
				t.Fatalf("expected statement to be supported, got error: %v", err)
			}
			if parsed.Kind != tc.wantKind {
				t.Fatalf("Kind = %s, want %s", parsed.Kind, tc.wantKind)
			}
			if parsed.OperationType != tc.wantType {
				t.Fatalf("OperationType = %s, want %s", parsed.OperationType, tc.wantType)
			}
			if parsed.TableName != tc.wantTbl {
				t.Fatalf("TableName = %s, want %s", parsed.TableName, tc.wantTbl)
			}
		})
	}
}

func TestAnalyzeStatement_RejectsDestructiveAndUnsafeSQL(t *testing.T) {
	cases := []struct {
		name       string
		engine     string
		sql        string
		wantReason string
	}{
		{
			name:       "drop_database",
			engine:     "mysql",
			sql:        "DROP DATABASE demo;",
			wantReason: "destructive_ddl_drop_schema_unsupported",
		},
		{
			name:       "truncate_table",
			engine:     "postgresql",
			sql:        "TRUNCATE TABLE accounts;",
			wantReason: "destructive_ddl_truncate_requires_backup",
		},
		{
			name:       "drop_table_cascade_pg",
			engine:     "postgresql",
			sql:        "DROP TABLE accounts CASCADE;",
			wantReason: "drop_table_cascade_unsupported",
		},
		{
			name:       "update_without_where",
			engine:     "mysql",
			sql:        "UPDATE accounts SET balance = 0;",
			wantReason: "update_without_where_unsupported",
		},
		{
			name:       "delete_without_where",
			engine:     "postgresql",
			sql:        "DELETE FROM accounts;",
			wantReason: "delete_without_where_unsupported",
		},
		{
			name:       "pg_update_from",
			engine:     "postgresql",
			sql:        "UPDATE accounts SET balance = b.amount FROM bonuses b WHERE accounts.id = b.account_id;",
			wantReason: "update_from_unsupported",
		},
		{
			name:       "pg_delete_using",
			engine:     "postgresql",
			sql:        "DELETE FROM accounts USING blacklisted b WHERE accounts.id = b.account_id;",
			wantReason: "delete_using_or_join_unsupported",
		},
		{
			name:       "pg_insert_on_conflict",
			engine:     "postgresql",
			sql:        "INSERT INTO accounts (id, name) VALUES (1, 'a') ON CONFLICT (id) DO NOTHING;",
			wantReason: "upsert_on_conflict_unsupported",
		},
		{
			name:       "mysql_insert_on_duplicate",
			engine:     "mysql",
			sql:        "INSERT INTO accounts (id, name) VALUES (1, 'a') ON DUPLICATE KEY UPDATE name = 'a';",
			wantReason: "upsert_on_conflict_unsupported",
		},
		{
			name:       "merge_statement",
			engine:     "postgresql",
			sql:        "MERGE INTO accounts a USING staging s ON a.id = s.id WHEN MATCHED THEN DELETE;",
			wantReason: "merge_unsupported",
		},
		{
			name:       "nondeterministic_rand",
			engine:     "mysql",
			sql:        "UPDATE accounts SET balance = RAND() WHERE id = 1;",
			wantReason: "nondeterministic_function_rand",
		},
		{
			name:       "mysql_deep_nested_subquery_in_case_when",
			engine:     "mysql",
			sql:        "UPDATE accounts SET balance = CASE WHEN balance > 0 THEN (SELECT MAX(b) FROM other_tbl) ELSE 0 END WHERE id = 1;",
			wantReason: "update_subquery_unsupported",
		},
		{
			name:       "pg_deep_nested_subquery_in_coalesce",
			engine:     "postgresql",
			sql:        "UPDATE accounts SET balance = COALESCE(balance, (SELECT amount FROM bonus LIMIT 1)) WHERE id = 1;",
			wantReason: "update_subquery_unsupported",
		},
		{
			name:       "pg_deep_nested_nondeterministic_func",
			engine:     "postgresql",
			sql:        "UPDATE accounts SET name = md5(gen_random_uuid()::text) WHERE id = 1;",
			wantReason: "nondeterministic_function_gen_random_uuid",
		},
		{
			name:       "mysql_conditional_comment_rejected",
			engine:     "mysql",
			sql:        "DELETE FROM accounts /*!80000 WHERE id = 1 */;",
			wantReason: "mysql_conditional_comment_unsupported",
		},
		{
			name:       "pg_multi_action_alter_table_rejected",
			engine:     "postgresql",
			sql:        "ALTER TABLE accounts ADD COLUMN c1 INT, ADD COLUMN c2 INT;",
			wantReason: "multi_action_alter_table_unsupported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AnalyzeStatement(tc.engine, "demo", tc.sql)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.sql)
			}
			capErr, ok := err.(*CapabilityError)
			if !ok {
				t.Fatalf("expected *CapabilityError, got %T: %v", err, err)
			}
			if capErr.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", capErr.Reason, tc.wantReason)
			}
		})
	}
}

func TestAnalyzeStatement_PostgresDollarQuotingAndMySQLComments(t *testing.T) {
	// PostgreSQL tagged dollar-quoted strings containing keywords such as ON CONFLICT / SELECT / semicolons
	// must be accurately recognized as ordinary string literals by the official libpg_query AST without triggering false rejections.
	pgSQL := "UPDATE public.accounts SET note = $body$ON CONFLICT DO NOTHING; SELECT * FROM secret_table WHERE id = 1$body$ WHERE id = 42;"
	parsed, err := AnalyzeStatement("postgresql", "public", pgSQL)
	if err != nil {
		t.Fatalf("expected PostgreSQL dollar-quoted UPDATE to succeed, got error: %v", err)
	}
	if parsed.Kind != KindUpdate || parsed.TableName != "accounts" || len(parsed.UpdatedColumns) != 1 || parsed.UpdatedColumns[0] != "note" {
		t.Fatalf("unexpected parsed result for dollar-quoted UPDATE: %+v", parsed)
	}
	if parsed.WhereClause != "id = 42" {
		t.Fatalf("WhereClause = %q, want %q", parsed.WhereClause, "id = 42")
	}
}

func TestInverseDMLGeneration_Deterministic(t *testing.T) {
	meta := &database.TableRollbackMetadata{
		Engine:                "mysql",
		SchemaName:            "demo",
		TableName:             "accounts",
		Exists:                true,
		HasExplicitPrimaryKey: true,
		IdentityKeyColumns:    []string{"tenant_id", "id"},
		Columns: []database.RollbackColumnMeta{
			{Name: "tenant_id", Ordinal: 1, DataType: "int", ColumnType: "int", IsPrimaryKey: true, PKOrdinal: 1},
			{Name: "id", Ordinal: 2, DataType: "bigint", ColumnType: "bigint", IsPrimaryKey: true, PKOrdinal: 2},
			{Name: "name", Ordinal: 3, DataType: "varchar", ColumnType: "varchar(64)", Nullable: false},
			{Name: "balance", Ordinal: 4, DataType: "decimal", ColumnType: "decimal(10,2)", Nullable: false},
			{Name: "note", Ordinal: 5, DataType: "text", ColumnType: "text", Nullable: true},
		},
	}

	beforeRow := database.RollbackRow{
		Cells: []database.RollbackCell{
			{Column: "tenant_id", DatabaseType: "INT", Text: "7"},
			{Column: "id", DatabaseType: "BIGINT", Text: "1001"},
			{Column: "name", DatabaseType: "VARCHAR", Text: "O'Reilly"},
			{Column: "balance", DatabaseType: "DECIMAL", Text: "125.50"},
			{Column: "note", DatabaseType: "TEXT", IsNull: true},
		},
	}
	afterRow := database.RollbackRow{
		Cells: []database.RollbackCell{
			{Column: "tenant_id", DatabaseType: "INT", Text: "7"},
			{Column: "id", DatabaseType: "BIGINT", Text: "1001"},
			{Column: "name", DatabaseType: "VARCHAR", Text: "O'Reilly Updated"},
			{Column: "balance", DatabaseType: "DECIMAL", Text: "250.00"},
			{Column: "note", DatabaseType: "TEXT", Text: "paid"},
		},
	}

	updInverse, err := buildInverseUpdateStatements("mysql", meta, []database.RollbackRow{beforeRow}, []database.RollbackRow{afterRow})
	if err != nil {
		t.Fatalf("buildInverseUpdateStatements failed: %v", err)
	}
	if len(updInverse) != 1 {
		t.Fatalf("expected 1 inverse UPDATE, got %d", len(updInverse))
	}
	wantUpd := "UPDATE `demo`.`accounts` SET `name` = 'O''Reilly', `balance` = 125.50, `note` = NULL WHERE `tenant_id` = 7 AND `id` = 1001;"
	if updInverse[0] != wantUpd {
		t.Fatalf("inverse UPDATE = %s, want %s", updInverse[0], wantUpd)
	}

	delInverse, err := buildInverseDeleteStatements("mysql", meta, []database.RollbackRow{beforeRow})
	if err != nil {
		t.Fatalf("buildInverseDeleteStatements failed: %v", err)
	}
	wantDel := "INSERT INTO `demo`.`accounts` (`tenant_id`, `id`, `name`, `balance`, `note`) VALUES (7, 1001, 'O''Reilly', 125.50, NULL);"
	if delInverse[0] != wantDel {
		t.Fatalf("inverse DELETE = %s, want %s", delInverse[0], wantDel)
	}

	insInverse, err := buildInverseInsertStatements("mysql", meta, []database.RollbackRow{afterRow})
	if err != nil {
		t.Fatalf("buildInverseInsertStatements failed: %v", err)
	}
	wantIns := "DELETE FROM `demo`.`accounts` WHERE `tenant_id` = 7 AND `id` = 1001;"
	if insInverse[0] != wantIns {
		t.Fatalf("inverse INSERT = %s, want %s", insInverse[0], wantIns)
	}
}

func TestSealAndOpenEvidenceBundle(t *testing.T) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
		hmacKey[i] = byte(i + 65)
	}
	doc := map[string]any{
		"contract_version": 1,
		"deployment_id":    "deploy-test",
		"keys": []map[string]any{
			{
				"key_id":     "pay-1",
				"purpose":    "payload",
				"status":     "encrypt_decrypt",
				"created_at": "2026-08-25T00:00:00Z",
				"key_base64": base64.StdEncoding.EncodeToString(key),
			},
			{
				"key_id":     "hmac-1",
				"purpose":    "sql_fingerprint",
				"status":     "encrypt_decrypt",
				"created_at": "2026-08-25T00:00:00Z",
				"key_base64": base64.StdEncoding.EncodeToString(hmacKey),
			},
		},
	}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatalf("NewCrypter: %v", err)
	}

	bundle := &EvidenceBundle{
		Engine:             "postgresql",
		ServerVersion:      "18.6",
		ServerVersionNum:   180006,
		SchemaName:         "public",
		OperationType:      OperationTypeDML,
		Strategy:           StrategyPG18DMLReturning,
		OriginalStatements: []string{"DELETE FROM public.accounts WHERE id = 42;"},
		AffectedRows:       1,
		Statements: []StatementEvidence{
			{
				Ordinal:            1,
				OriginalSQL:        "DELETE FROM public.accounts WHERE id = 42;",
				Kind:               KindDelete,
				SchemaName:         "public",
				TableName:          "accounts",
				IdentityKeyColumns: []string{"id"},
				InverseStatements:  []string{`INSERT INTO "public"."accounts" ("id", "name") VALUES (42, 'alice');`},
			},
		},
	}

	evidenceID := "01960000-0000-7000-8000-000000000099"
	env, mac, err := SealEvidenceBundle(crypter, evidenceID, bundle)
	if err != nil {
		t.Fatalf("SealEvidenceBundle: %v", err)
	}
	if len(env.Ciphertext) == 0 || mac == "" {
		t.Fatalf("expected non-empty ciphertext and hmac")
	}

	opened, err := OpenEvidenceBundle(crypter, evidenceID, env, mac)
	if err != nil {
		t.Fatalf("OpenEvidenceBundle: %v", err)
	}
	if !strings.Contains(opened.RollbackSQL, `INSERT INTO "public"."accounts"`) {
		t.Fatalf("unexpected RollbackSQL: %s", opened.RollbackSQL)
	}

	// Cross-record AAD tampering must fail decryption
	if _, err := OpenEvidenceBundle(crypter, "01960000-0000-7000-8000-000000000000", env, mac); err == nil {
		t.Fatalf("expected wrong evidenceID AAD to fail decryption")
	}
}
