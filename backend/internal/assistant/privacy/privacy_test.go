package privacy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func TestTurnPrivacySession_AnonymizeAndRestoreSQL_MySQLAndPostgres(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	schemaTok, err := sess.RegisterSchema("logistics")
	if err != nil {
		t.Fatalf("RegisterSchema: %v", err)
	}
	tableTok, err := sess.RegisterTable("logistics", "warehouse_inventory")
	if err != nil {
		t.Fatalf("RegisterTable: %v", err)
	}
	colCodeTok, err := sess.RegisterColumn("logistics", "warehouse_inventory", "warehouse_code")
	if err != nil {
		t.Fatalf("RegisterColumn code: %v", err)
	}
	colNameTok, err := sess.RegisterColumn("logistics", "warehouse_inventory", "warehouse_name")
	if err != nil {
		t.Fatalf("RegisterColumn name: %v", err)
	}
	colCapTok, err := sess.RegisterColumn("logistics", "warehouse_inventory", "capacity_tons")
	if err != nil {
		t.Fatalf("RegisterColumn cap: %v", err)
	}

	rawSQL := `INSERT INTO logistics.warehouse_inventory (warehouse_code, warehouse_name, capacity_tons) -- secret comment
VALUES ('WH-HZ-01', 'Hangzhou Hub 01', 12000)`
	mysqlProfile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}

	anonSQL, err := sess.AnonymizeSQL(rawSQL, mysqlProfile)
	if err != nil {
		t.Fatalf("AnonymizeSQL: %v", err)
	}

	// Verify that real table names, column names, comments, and business literals never appear in the anonymized SQL.
	for _, secret := range []string{"logistics", "warehouse_inventory", "warehouse_code", "warehouse_name", "capacity_tons", "WH-HZ-01", "Hangzhou Hub 01", "12000", "secret comment"} {
		if strings.Contains(anonSQL, secret) {
			t.Fatalf("anonymized SQL leaked %q: %s", secret, anonSQL)
		}
	}
	// Verify that two string literals of the same type receive distinguishable placeholders :val_str_1 and :val_str_2.
	if !strings.Contains(anonSQL, ":val_str_1") || !strings.Contains(anonSQL, ":val_str_2") || !strings.Contains(anonSQL, ":val_int_1") {
		t.Fatalf("expected distinguishable placeholders in anonymized SQL, got: %s", anonSQL)
	}
	if !strings.Contains(anonSQL, schemaTok) || !strings.Contains(anonSQL, tableTok) ||
		!strings.Contains(anonSQL, colCodeTok) || !strings.Contains(anonSQL, colNameTok) || !strings.Contains(anonSQL, colCapTok) {
		t.Fatalf("expected mapped object tokens in anonymized SQL, got: %s", anonSQL)
	}

	// Verify MySQL structured restoration.
	res, err := privacy.ValidateAndRestoreSQL(sess, anonSQL, mysqlProfile)
	if err != nil {
		t.Fatalf("ValidateAndRestoreSQL mysql: %v", err)
	}
	if res.StatementKind != domain.StmtInsert || res.RiskCategory != domain.RiskDML {
		t.Fatalf("unexpected statement classification: kind=%s risk=%s", res.StatementKind, res.RiskCategory)
	}
	wantMySQL := "INSERT INTO `logistics`.`warehouse_inventory` (`warehouse_code`, `warehouse_name`, `capacity_tons`) VALUES ('WH-HZ-01', 'Hangzhou Hub 01', 12000)"
	if res.RestoredSQL != wantMySQL {
		t.Fatalf("RestoredSQL mysql mismatch:\ngot:  %s\nwant: %s", res.RestoredSQL, wantMySQL)
	}

	// Verify PostgreSQL structured restoration (double-quoted identifiers).
	pgProfile := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	pgRes, err := privacy.ValidateAndRestoreSQL(sess, anonSQL, pgProfile)
	if err != nil {
		t.Fatalf("ValidateAndRestoreSQL pg: %v", err)
	}
	wantPG := `INSERT INTO "logistics"."warehouse_inventory" ("warehouse_code", "warehouse_name", "capacity_tons") VALUES ('WH-HZ-01', 'Hangzhou Hub 01', 12000)`
	if pgRes.RestoredSQL != wantPG {
		t.Fatalf("RestoredSQL pg mismatch:\ngot:  %s\nwant: %s", pgRes.RestoredSQL, wantPG)
	}
}

func TestTurnPrivacySession_AnonymizeNaturalLanguageAndZeroLeak(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	_, _ = sess.RegisterSchema("crm_prod")
	_, _ = sess.RegisterTable("crm_prod", "customer_orders")
	_, _ = sess.RegisterColumn("crm_prod", "customer_orders", "buyer_mobile")

	prompt := `Please insert a row into crm_prod.customer_orders with buyer_mobile='13800138000' and note "EastRegionVIPAccount"`
	anonPrompt, err := sess.AnonymizeNaturalLanguage(prompt)
	if err != nil {
		t.Fatalf("AnonymizeNaturalLanguage: %v", err)
	}
	for _, secret := range []string{"crm_prod", "customer_orders", "buyer_mobile", "13800138000", "EastRegionVIPAccount"} {
		if strings.Contains(anonPrompt, secret) {
			t.Fatalf("anonymized prompt leaked %q: %s", secret, anonPrompt)
		}
	}

	// Credential leakage must fail closed with 6003.
	_, err = sess.AnonymizeNaturalLanguage("Please connect to postgres://admin:SuperSecret123@10.0.0.1:5432/prod and query tables")
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeAssistantAnonymizationFailed {
		t.Fatalf("expected 6003 ASSISTANT_ANONYMIZATION_FAILED on credential leak, got: %v", err)
	}
}

func TestValidateAndRestoreSQL_RejectsForbiddenAndUnboundConstructs(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	_, _ = sess.RegisterSchema("public")
	_, _ = sess.RegisterTable("public", "orders")
	_, _ = sess.RegisterColumn("public", "orders", "order_id")
	_, _ = sess.RegisterValue(privacy.ValInt, "42")

	pgProfile := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	cases := []struct {
		name string
		sql  string
	}{
		{
			name: "multi_statement_stacking",
			sql:  "SELECT column_1 FROM table_1 WHERE column_1 = :val_int_1; DROP TABLE table_1",
		},
		{
			name: "forbidden_truncate",
			sql:  "TRUNCATE TABLE table_1",
		},
		{
			name: "forbidden_drop_database",
			sql:  "DROP DATABASE schema_1",
		},
		{
			name: "forbidden_grant",
			sql:  "GRANT ALL ON table_1 TO column_1",
		},
		{
			name: "unknown_table_token",
			sql:  "SELECT column_1 FROM table_99 WHERE column_1 = :val_int_1",
		},
		{
			name: "unbound_value_placeholder",
			sql:  "SELECT column_1 FROM table_1 WHERE column_1 = :val_str_99",
		},
		{
			name: "unbound_question_mark",
			sql:  "SELECT column_1 FROM table_1 WHERE column_1 = ?",
		},
		{
			name: "unmapped_raw_identifier",
			sql:  "SELECT secret_col FROM table_1 WHERE column_1 = :val_int_1",
		},
		{
			name: "unparameterized_string_literal",
			sql:  "SELECT column_1 FROM table_1 WHERE column_1 = 'raw_literal'",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := privacy.ValidateAndRestoreSQL(sess, tc.sql, pgProfile)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeAssistantSQLRestorationInvalid {
				t.Fatalf("case %s: expected 6004 ASSISTANT_SQL_RESTORATION_INVALID, got %v", tc.name, err)
			}
		})
	}
}

func TestValidateAndRestoreSQL_EscapesInjectionPayloadSafely(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	_, _ = sess.RegisterTable("app", "users")
	_, _ = sess.RegisterColumn("app", "users", "nickname")
	ph, err := sess.RegisterValue(privacy.ValString, `admin'; DROP TABLE users; -- \`)
	if err != nil {
		t.Fatalf("RegisterValue: %v", err)
	}

	mysqlProfile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	res, err := privacy.ValidateAndRestoreSQL(sess, "SELECT column_1 FROM table_1 WHERE column_1 = "+ph, mysqlProfile)
	if err != nil {
		t.Fatalf("ValidateAndRestoreSQL: %v", err)
	}
	// Both single quotes and backslashes are safely escaped, and re-lexing still yields a single SELECT.
	if !strings.Contains(res.RestoredSQL, `'admin''; DROP TABLE users; -- \\'`) {
		t.Fatalf("expected MySQL backslash and single-quote escaping, got: %s", res.RestoredSQL)
	}
}

func TestTurnPrivacySession_ZeroAndRelease(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	_, _ = sess.RegisterTable("public", "accounts")
	_, _ = sess.RegisterValue(privacy.ValString, "sensitive")
	sess.ZeroAndRelease()

	if !sess.IsReleased() {
		t.Fatalf("expected session to be marked released")
	}
	if _, _, ok := sess.LookupIdentifier("table_1"); ok {
		t.Fatalf("expected identifier lookup to fail after ZeroAndRelease")
	}
	if _, ok := sess.LookupValue(":val_str_1"); ok {
		t.Fatalf("expected value lookup to fail after ZeroAndRelease")
	}
}

func TestTurnPrivacySession_QuotedAndUnquotedConsistencyAcrossTurns(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	_, _ = sess.RegisterTable("core_db", "warehouses")
	_, _ = sess.RegisterColumn("core_db", "warehouses", "role")

	// Turn 1: user mentions a literal without quotes; Turn 2: user quotes the same literal and repeats it unquoted.
	turn1, err := sess.AnonymizeNaturalLanguage("Can AnalystGroup view the warehouses table and role column?")
	if err != nil {
		t.Fatalf("AnonymizeNaturalLanguage turn1: %v", err)
	}
	turn2, err := sess.AnonymizeNaturalLanguage(`Filter warehouses where role = "AnalystGroup" or AnalystGroup is active`)
	if err != nil {
		t.Fatalf("AnonymizeNaturalLanguage turn2: %v", err)
	}
	asstReply, err := sess.AnonymizeAssistantHistory(
		"In YearningX v4.0.0, fixed role names (\u201cAnalystGroup\u201d\u201cReadOnlyRole\u201d) and \u201cNoDatasourceVisible\u201d= MissingGrant do not trigger false-positive 6003.",
	)
	if err != nil {
		t.Fatalf("AnonymizeAssistantHistory: %v", err)
	}

	turn1 = sess.ScrubRegisteredTerms(turn1)
	asstReply = sess.ScrubRegisteredTerms(asstReply)

	for _, s := range []string{turn1, turn2, asstReply} {
		if err := sess.AssertZeroLeak(s); err != nil {
			t.Fatalf("AssertZeroLeak failed on %q: %v", s, err)
		}
	}
}

func TestTurnPrivacySession_NumberParameterizationBoundVsPlatformHelp(t *testing.T) {
	t.Run("bound tables parameterize platform caps and single-digit business numbers", func(t *testing.T) {
		sess := privacy.NewTurnPrivacySession()
		defer sess.ZeroAndRelease()

		_, _ = sess.RegisterSchema("shop")
		_, _ = sess.RegisterTable("shop", "accounts")
		_, _ = sess.RegisterColumn("shop", "accounts", "balance")
		_, _ = sess.RegisterColumn("shop", "accounts", "tier")
		_, _ = sess.RegisterColumn("shop", "accounts", "rate")

		prompt := "Optimize SQL:\n2. SELECT balance, tier FROM accounts WHERE balance >= 500 AND tier >= 3 AND rate > 0.5 LIMIT 0, 20 (v4.0.0)"
		anon, err := sess.AnonymizeNaturalLanguage(prompt)
		if err != nil {
			t.Fatalf("AnonymizeNaturalLanguage: %v", err)
		}
		// 500, 3, 0.5, 20 must all be parameterized; v4.0.0 and line-start "2. " must remain intact.
		for _, leaked := range []string{"500", "0.5", " 3 ", " 20 "} {
			if strings.Contains(anon, leaked) {
				t.Fatalf("expected numeric literal %q to be parameterized in: %s", leaked, anon)
			}
		}
		if !strings.Contains(anon, "v4.0.0") || !strings.Contains(anon, "\n2. ") {
			t.Fatalf("expected version triple and list marker to stay intact, got: %s", anon)
		}

		// Model returns anonymized SQL using the bound placeholders and LIMIT 0, 20.
		mysqlProf := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
		res, err := privacy.ValidateAndRestoreSQL(
			sess,
			"SELECT column_1, column_2 FROM table_1 WHERE column_1 >= :val_int_1 AND column_2 >= :val_int_2 AND column_3 > :val_num_1 LIMIT 0, 20",
			mysqlProf,
			"zh-CN",
		)
		if err != nil {
			t.Fatalf("ValidateAndRestoreSQL failed: %v", err)
		}
		if !strings.Contains(res.RestoredSQL, "`balance` >= 500") ||
			!strings.Contains(res.RestoredSQL, "`tier` >= 3") ||
			!strings.Contains(res.RestoredSQL, "`rate` > 0.5") ||
			!strings.Contains(res.RestoredSQL, "LIMIT 0, 20") {
			t.Fatalf("unexpected restored SQL: %s", res.RestoredSQL)
		}
	})

	t.Run("platform_help without bound tables keeps public platform numbers and error codes", func(t *testing.T) {
		sess := privacy.NewTurnPrivacySession()
		defer sess.ZeroAndRelease()

		prompt := "In YearningX v4.0.0, why does error 6004 happen with limit 500 or 100000 on port 3306?"
		anon, err := sess.AnonymizeNaturalLanguage(prompt)
		if err != nil {
			t.Fatalf("AnonymizeNaturalLanguage: %v", err)
		}
		for _, kept := range []string{"v4.0.0", "6004", "500", "100000", "3306"} {
			if !strings.Contains(anon, kept) {
				t.Fatalf("expected platform_help constant %q to be preserved, got: %s", kept, anon)
			}
		}
	})
}

func TestValidateAndRestoreSQL_OptimizeConstructsAndSecurityBoundaries(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	_, _ = sess.RegisterSchema("app")
	_, _ = sess.RegisterTable("app", "orders")
	_, _ = sess.RegisterColumn("app", "orders", "id")
	_, _ = sess.RegisterColumn("app", "orders", "balance")
	_, _ = sess.RegisterColumn("app", "orders", "status")
	_, _ = sess.RegisterValue(privacy.ValInt, "500")
	_, _ = sess.RegisterValue(privacy.ValInt, "10")

	mysqlProf := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	pgProf := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	allowed := []struct {
		name    string
		profile fingerprint.Profile
		sql     string
		wantSub string
	}{
		{
			name:    "mysql_limit_offset_count_numbers",
			profile: mysqlProf,
			sql:     "SELECT column_1, column_2 FROM table_1 WHERE column_2 >= :val_int_1 LIMIT 0, 20",
			wantSub: "LIMIT 0, 20",
		},
		{
			name:    "mysql_limit_placeholder_offset_and_count",
			profile: mysqlProf,
			sql:     "SELECT column_1 FROM table_1 WHERE column_2 >= :val_int_1 LIMIT :val_int_2, 50",
			wantSub: "LIMIT 10, 50",
		},
		{
			name:    "create_index_with_new_name",
			profile: mysqlProf,
			sql:     "CREATE INDEX idx_orders_balance_status ON table_1 (column_2, column_3)",
			wantSub: "CREATE INDEX `idx_orders_balance_status` ON `orders` (`balance`, `status`)",
		},
		{
			name:    "create_unique_index_pg",
			profile: pgProf,
			sql:     "CREATE UNIQUE INDEX idx_orders_id_uniq ON table_1 (column_1)",
			wantSub: `CREATE UNIQUE INDEX "idx_orders_id_uniq" ON "orders" ("id")`,
		},
		{
			name:    "alter_table_add_index",
			profile: mysqlProf,
			sql:     "ALTER TABLE table_1 ADD INDEX idx_orders_bal (column_2)",
			wantSub: "ALTER TABLE `orders` ADD INDEX `idx_orders_bal` (`balance`)",
		},
		{
			name:    "drop_index_mysql",
			profile: mysqlProf,
			sql:     "DROP INDEX idx_legacy_bal ON table_1",
			wantSub: "DROP INDEX `idx_legacy_bal` ON `orders`",
		},
		{
			name:    "table_alias_and_derived_subquery_join",
			profile: mysqlProf,
			sql:     "SELECT a.column_1, a.column_2 FROM table_1 a JOIN (SELECT column_1 FROM table_1 WHERE column_2 >= :val_int_1 ORDER BY column_1 DESC LIMIT 0, 20) t ON a.column_1 = t.column_1",
			wantSub: "ON `a`.`id` = `t`.`id`",
		},
		{
			name:    "cte_with_alias_and_select_alias",
			profile: pgProf,
			sql:     "WITH high_balance AS (SELECT column_1, column_2 AS amt FROM table_1 WHERE column_2 >= :val_int_1) SELECT hb.column_1, hb.amt FROM high_balance AS hb ORDER BY hb.amt DESC FETCH NEXT 20 ROWS ONLY",
			wantSub: `WITH "high_balance" AS`,
		},
		{
			name:    "select_alias_unqualified_in_order_by",
			profile: mysqlProf,
			sql:     "SELECT column_1, column_2 AS amt FROM table_1 ORDER BY amt DESC LIMIT 0, 20",
			wantSub: "ORDER BY `amt` DESC",
		},
	}

	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			res, err := privacy.ValidateAndRestoreSQL(sess, tc.sql, tc.profile, "zh-CN")
			if err != nil {
				t.Fatalf("expected valid SQL, got err=%v", err)
			}
			if !strings.Contains(res.RestoredSQL, tc.wantSub) {
				t.Fatalf("restored SQL %q missing expected %q", res.RestoredSQL, tc.wantSub)
			}
		})
	}

	// Strict fail-closed security boundaries must reject unauthorized objects even with aliases/CTEs/indexes.
	rejected := []struct {
		name string
		sql  string
	}{
		{
			name: "table_alias_with_unmapped_column",
			sql:  "SELECT a.secret_col FROM table_1 a",
		},
		{
			name: "table_alias_name_collision_in_where",
			sql:  "SELECT column_1 FROM table_1 AS secret_col WHERE secret_col = :val_int_1",
		},
		{
			name: "cte_name_collision_in_column_pos",
			sql:  "WITH secret_col AS (SELECT column_1 FROM table_1) SELECT secret_col FROM table_1",
		},
		{
			name: "select_alias_collision_in_where",
			sql:  "SELECT column_1 AS secret_col FROM table_1 WHERE secret_col = :val_int_1",
		},
		{
			name: "index_name_collision_in_where",
			sql:  "CREATE INDEX secret_col ON table_1 (column_1) WHERE secret_col = :val_int_1",
		},
		{
			name: "cte_shadowed_by_table_alias",
			sql:  "WITH a AS (SELECT column_1 AS secret_col FROM table_1) SELECT a.secret_col FROM table_1 a",
		},
		{
			name: "join_unmapped_table",
			sql:  "SELECT a.column_1 FROM table_1 a JOIN secret_table b ON a.column_1 = b.column_1",
		},
		{
			name: "spoofed_unknown_table_token_as_alias",
			sql:  "SELECT column_1 FROM table_1 AS table_99",
		},
		{
			name: "create_index_on_unmapped_table",
			sql:  "CREATE INDEX idx_bad ON secret_table (column_1)",
		},
		{
			name: "non_limit_comma_number_still_forbidden",
			sql:  "SELECT column_1 FROM table_1 WHERE column_2 IN (500, 999)",
		},
	}

	for _, tc := range rejected {
		t.Run("reject_"+tc.name, func(t *testing.T) {
			_, err := privacy.ValidateAndRestoreSQL(sess, tc.sql, mysqlProf, "zh-CN")
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeAssistantSQLRestorationInvalid {
				t.Fatalf("expected 6004 ASSISTANT_SQL_RESTORATION_INVALID, got %v", err)
			}
		})
	}
}

func TestValidateAndRestoreSQL_LocalizedSemanticWarnings(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	_, _ = sess.RegisterTable("app", "users")
	_, _ = sess.RegisterColumn("app", "users", "status")
	_, _ = sess.RegisterValue(privacy.ValInt, "2")
	mysqlProf := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}

	zhSel, err := privacy.ValidateAndRestoreSQL(sess, "SELECT * FROM table_1 LIMIT 10", mysqlProf, "zh-CN")
	if err != nil {
		t.Fatalf("zh select: %v", err)
	}
	if len(zhSel.SemanticWarnings) != 1 || !strings.Contains(zhSel.SemanticWarnings[0], "`SELECT *`") || strings.Contains(zhSel.SemanticWarnings[0], "Statement uses") {
		t.Fatalf("expected localized zh-CN SELECT * warning, got: %v", zhSel.SemanticWarnings)
	}

	enSel, err := privacy.ValidateAndRestoreSQL(sess, "SELECT * FROM table_1 LIMIT 10", mysqlProf, "en-US")
	if err != nil {
		t.Fatalf("en select: %v", err)
	}
	if len(enSel.SemanticWarnings) != 1 || !strings.Contains(enSel.SemanticWarnings[0], "Statement uses `SELECT *`") {
		t.Fatalf("expected localized en-US SELECT * warning, got: %v", enSel.SemanticWarnings)
	}

	zhUpd, err := privacy.ValidateAndRestoreSQL(sess, "UPDATE table_1 SET column_1 = :val_int_1", mysqlProf, "zh-CN")
	if err != nil {
		t.Fatalf("zh update: %v", err)
	}
	if len(zhUpd.SemanticWarnings) != 1 || strings.Contains(zhUpd.SemanticWarnings[0], "Statement has no WHERE") {
		t.Fatalf("expected localized zh-CN no-WHERE warning, got: %v", zhUpd.SemanticWarnings)
	}

	enUpd, err := privacy.ValidateAndRestoreSQL(sess, "UPDATE table_1 SET column_1 = :val_int_1", mysqlProf, "en-US")
	if err != nil {
		t.Fatalf("en update: %v", err)
	}
	if len(enUpd.SemanticWarnings) != 1 || !strings.Contains(enUpd.SemanticWarnings[0], "Statement has no WHERE filter") {
		t.Fatalf("expected localized en-US no-WHERE warning, got: %v", enUpd.SemanticWarnings)
	}
}

func TestTurnPrivacySession_TrivialKeywordLiteralsBoundVsPlatformHelp(t *testing.T) {
	t.Run("bound tables parameterize quoted and kv platform enum literals without AssertZeroLeak false positives", func(t *testing.T) {
		sess := privacy.NewTurnPrivacySession()
		defer sess.ZeroAndRelease()

		_, _ = sess.RegisterSchema("app")
		_, _ = sess.RegisterTable("app", "orders")
		_, _ = sess.RegisterColumn("app", "orders", "status")
		_, _ = sess.RegisterColumn("app", "orders", "engine")

		prompt := "Optimize SELECT status, engine FROM orders WHERE status = 'failed' OR status = \"approved\" OR status = \u201cerror\u201d; check status = failed and engine = 'mysql', keep unquoted failed and mysql prose intact"
		anon, err := sess.AnonymizeNaturalLanguage(prompt)
		if err != nil {
			t.Fatalf("AnonymizeNaturalLanguage: %v", err)
		}
		for _, leaked := range []string{"'failed'", `"approved"`, "\u201cerror\u201d", "status = failed", "'mysql'"} {
			if strings.Contains(anon, leaked) {
				t.Fatalf("expected literal/kv %q to be bound as :val_str_n in: %s", leaked, anon)
			}
		}
		if err := sess.AssertZeroLeak(anon); err != nil {
			t.Fatalf("AssertZeroLeak unexpectedly failed: %v", err)
		}

		// Model returns anonymized SQL referencing all bound placeholders (:val_str_1='failed', :val_str_2='approved', :val_str_3='error', :val_str_4='mysql').
		mysqlProf := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
		res, err := privacy.ValidateAndRestoreSQL(
			sess,
			"SELECT column_1, column_2 FROM table_1 WHERE column_1 IN (:val_str_1, :val_str_2, :val_str_3) AND column_2 = :val_str_4",
			mysqlProf,
			"zh-CN",
		)
		if err != nil {
			t.Fatalf("ValidateAndRestoreSQL failed: %v", err)
		}
		for _, wantLit := range []string{"'failed'", "'approved'", "'error'", "'mysql'"} {
			if !strings.Contains(res.RestoredSQL, wantLit) {
				t.Fatalf("restored SQL %q missing expected literal %s", res.RestoredSQL, wantLit)
			}
		}
	})

	t.Run("platform_help without bound tables keeps trivial keyword literals unmasked", func(t *testing.T) {
		sess := privacy.NewTurnPrivacySession()
		defer sess.ZeroAndRelease()

		anon, err := sess.AnonymizeNaturalLanguage("Why does 'failed' status show up on 'mysql' in 'draft' mode?")
		if err != nil {
			t.Fatalf("AnonymizeNaturalLanguage: %v", err)
		}
		for _, kept := range []string{"failed", "mysql", "draft"} {
			if !strings.Contains(anon, kept) {
				t.Fatalf("expected platform_help literal %q to stay unmasked, got: %s", kept, anon)
			}
		}
		if strings.Contains(anon, ":val_str_") {
			t.Fatalf("expected platform_help not to parameterize trivial keywords, got: %s", anon)
		}
	})
}

func TestTurnPrivacySession_AnonymizeKnowledgeEntryText(t *testing.T) {
	sess := privacy.NewTurnPrivacySession()
	defer sess.ZeroAndRelease()

	schTok, err := sess.RegisterSchema("app")
	if err != nil {
		t.Fatalf("RegisterSchema: %v", err)
	}
	tblTok, err := sess.RegisterTable("app", "orders")
	if err != nil {
		t.Fatalf("RegisterTable: %v", err)
	}
	colTok, err := sess.RegisterColumn("app", "orders", "status")
	if err != nil {
		t.Fatalf("RegisterColumn: %v", err)
	}

	rawText := `Check {{ref:table:["app","orders"]}} and {{ref:column:["app","orders","status"]}} plus extra table secret_audit; avoid status = 'pending' and keep LIMIT <= 1000.`
	anon, err := sess.AnonymizeKnowledgeEntryText(rawText, []string{"secret_audit"})
	if err != nil {
		t.Fatalf("AnonymizeKnowledgeEntryText: %v", err)
	}
	if !strings.Contains(anon, schTok+"."+tblTok) || !strings.Contains(anon, schTok+"."+tblTok+"."+colTok) {
		t.Fatalf("expected policy refs resolved to anonymous tokens, got: %s", anon)
	}
	if strings.Contains(anon, "secret_audit") || !strings.Contains(anon, "[redacted_table]") {
		t.Fatalf("expected un-described catalog table scrubbed to [redacted_table], got: %s", anon)
	}
	if strings.Contains(anon, ":val_") {
		t.Fatalf("knowledge thresholds/literals must not register turn placeholders, got: %s", anon)
	}
	if err := sess.AssertZeroLeak(anon); err != nil {
		t.Fatalf("AssertZeroLeak unexpectedly failed: %v", err)
	}
	if _, ok := sess.LookupValue(":val_str_1"); ok {
		t.Fatal("knowledge anonymization must not register :val_str_1 in turn session")
	}

	// Out-of-scope table reference must return ErrKnowledgeRefOutOfScope.
	if _, err := sess.AnonymizeKnowledgeEntryText(`See {{ref:table:["other_db","hidden_tbl"]}}`, nil); !errors.Is(err, privacy.ErrKnowledgeRefOutOfScope) {
		t.Fatalf("expected ErrKnowledgeRefOutOfScope, got: %v", err)
	}

	// Credential leak must fail closed with 6003 ASSISTANT_ANONYMIZATION_FAILED.
	_, err = sess.AnonymizeKnowledgeEntryText("Use api_key=sk-live-secret-token-1234567890 in app.orders", nil)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeAssistantAnonymizationFailed {
		t.Fatalf("expected 6003 ASSISTANT_ANONYMIZATION_FAILED, got: %v", err)
	}
}
