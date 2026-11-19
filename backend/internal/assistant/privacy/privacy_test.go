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
