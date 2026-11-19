package privacy

import (
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestSessionLifecycleAndErrorBranches(t *testing.T) {
	var nilSess *TurnPrivacySession
	nilSess.ZeroAndRelease()
	if nilSess.ScrubRegisteredTerms("abc") != "abc" {
		t.Fatal("expected nil session ScrubRegisteredTerms to return input")
	}
	if nilSess.RestoreTextTokens("abc") != "abc" {
		t.Fatal("expected nil session RestoreTextTokens to return input")
	}
	if out := nilSess.RestoreTextSlice(nil); len(out) != 0 {
		t.Fatal("expected empty slice from RestoreTextSlice(nil)")
	}

	s := NewTurnPrivacySession()
	s.addForbiddenLocked("   ", "empty")

	// Invalid empty or forbidden-rune identifiers
	if _, err := s.RegisterSchema(""); err == nil {
		t.Fatal("expected error for empty schema")
	}
	if _, err := s.RegisterTable("public", ""); err == nil {
		t.Fatal("expected error for empty table")
	}
	if _, err := s.RegisterColumn("public", "t", ""); err == nil {
		t.Fatal("expected error for empty column")
	}
	if _, err := s.RegisterIndex("public", "t", ""); err == nil {
		t.Fatal("expected error for empty index")
	}

	// Valid registrations including SQL keyword names and empty table context
	if _, err := s.RegisterSchema("custom_schema"); err != nil {
		t.Fatalf("RegisterSchema: %v", err)
	}
	if _, err := s.RegisterTable("custom_schema", "custom_table"); err != nil {
		t.Fatalf("RegisterTable: %v", err)
	}
	if _, err := s.RegisterColumn("", "", "custom_col"); err != nil {
		t.Fatalf("RegisterColumn without table: %v", err)
	}
	if _, err := s.RegisterColumn("custom_schema", "new_table", "new_col"); err != nil {
		t.Fatalf("RegisterColumn with new table: %v", err)
	}
	if _, err := s.RegisterIndex("custom_schema", "custom_table", "custom_idx"); err != nil {
		t.Fatalf("RegisterIndex: %v", err)
	}

	// RegisterValue with empty kind, duplicate key, positional params
	ph1, err := s.RegisterValue("", "hello_world")
	if err != nil || ph1 != ":val_str_1" {
		t.Fatalf("unexpected ph1=%q err=%v", ph1, err)
	}
	ph1Dup, err := s.RegisterValue(ValString, "hello_world")
	if err != nil || ph1Dup != ph1 {
		t.Fatalf("expected duplicate value to reuse placeholder, got %q", ph1Dup)
	}
	if _, err := s.RegisterValue(ValString, "?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterValue(ValString, "$12"); err != nil {
		t.Fatal(err)
	}

	tables := s.AuthorizedTables()
	if len(tables) != 1 || tables[0] != "custom_table" {
		t.Fatalf("unexpected AuthorizedTables: %v", tables)
	}

	// LookupIdentifier for unmapped token and non-allowed non-alias token
	if _, _, ok := s.LookupIdentifier("table_999"); ok {
		t.Fatal("expected false for unknown token")
	}
	unallowedTok, _ := s.mapper.Token(revprivacy.KindDatabase, "secret_db")
	if _, _, ok := s.LookupIdentifier(unallowedTok); ok {
		t.Fatal("expected false for unallowed database token")
	}
	aliasTok, _ := s.mapper.Token(revprivacy.KindAlias, "t_alias")
	if kind, orig, ok := s.LookupIdentifier(aliasTok); !ok || kind != revprivacy.KindAlias || orig != "t_alias" {
		t.Fatalf("expected alias token to be allowed, got %v %q %v", kind, orig, ok)
	}

	// RestoreTextTokens and RestoreTextSlice
	restored := s.RestoreTextSlice([]string{
		"Query table_1 and column_1 with :val_str_1 and :val_str_999 and table_999",
	})
	if len(restored) != 1 || !strings.Contains(restored[0], "custom_table") || !strings.Contains(restored[0], "hello_world") {
		t.Fatalf("unexpected RestoreTextSlice output: %v", restored)
	}

	// Release session and verify all post-release guards
	s.ZeroAndRelease()
	if _, err := s.RegisterSchema("a"); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.RegisterTable("a", "b"); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.RegisterColumn("a", "b", "c"); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.RegisterIndex("a", "b", "c"); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.RegisterValue(ValString, "x"); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.AnonymizeSQL("SELECT 1", fingerprint.Profile{Engine: "mysql", Mode: "mysql"}); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.AnonymizeNaturalLanguage("hello"); err == nil {
		t.Fatal("expected error after release")
	}
	if _, err := s.AnonymizeAssistantHistory("hello"); err == nil {
		t.Fatal("expected error after release")
	}
	if s.ScrubRegisteredTerms("hello") != "hello" {
		t.Fatal("expected unchanged text after release")
	}
	if err := s.AssertZeroLeak("hello"); err == nil {
		t.Fatal("expected error after release")
	}
	if s.RestoreTextTokens("table_1") != "table_1" {
		t.Fatal("expected unchanged text from RestoreTextTokens after release")
	}
}

func TestAnonymizeSQLAndNaturalLanguageBranches(t *testing.T) {
	s := NewTurnPrivacySession()
	defer s.ZeroAndRelease()

	pg := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	if _, err := s.AnonymizeSQL("   ", pg); err == nil {
		t.Fatal("expected error for empty SQL")
	}
	if _, err := s.AnonymizeSQL("SELECT 1", fingerprint.Profile{Engine: "invalid"}); err == nil {
		t.Fatal("expected error for invalid profile")
	}
	if _, err := s.AnonymizeSQL("/* comment only */", pg); err == nil {
		t.Fatal("expected error for comment-only SQL")
	}
	if _, err := s.AnonymizeSQL("SELECT 1; SELECT 2", pg); err == nil {
		t.Fatal("expected error for multi-statement SQL")
	}

	_, _ = s.RegisterSchema("sales_schema")
	_, _ = s.RegisterTable("sales_schema", "orders_tbl")
	_, _ = s.RegisterColumn("sales_schema", "orders_tbl", "order_amt")
	_, _ = s.RegisterIndex("sales_schema", "orders_tbl", "idx_orders_amt")

	// Test diverse literals, quoted idents, aliases, indexes, booleans, nulls, and trailing semicolon
	complexSQL := `SELECT "sales_schema"."orders_tbl"."order_amt" AS my_alias,
		NULL, TRUE, FALSE, 12.34, 0x1a2b, X'deadbeef',
		'{"k":1}', '550e8400-e29b-41d4-a716-446655440000', '2026-11-10 10:20:30', $1, table_1
		FROM sales_schema.orders_tbl WHERE idx_orders_amt IS NOT NULL;`
	anonSQL, err := s.AnonymizeSQL(complexSQL, pg)
	if err != nil {
		t.Fatalf("AnonymizeSQL complex: %v", err)
	}
	if !strings.Contains(anonSQL, "NULL") || !strings.Contains(anonSQL, ":val_bool_1") ||
		!strings.Contains(anonSQL, ":val_num_1") || !strings.Contains(anonSQL, ":val_hex_1") ||
		!strings.Contains(anonSQL, ":val_json_1") || !strings.Contains(anonSQL, ":val_uuid_1") ||
		!strings.Contains(anonSQL, ":val_ts_1") {
		t.Fatalf("missing expected typed placeholders in: %s", anonSQL)
	}

	// Test DDL contextual identifier anonymization (IF NOT EXISTS, INDEX CONCURRENTLY, DROP INDEX IF EXISTS)
	ddlCases := []string{
		"CREATE TABLE IF NOT EXISTS dyn_table_one (dyn_col_one INT)",
		"CREATE INDEX CONCURRENTLY dyn_idx_one ON dyn_table_one (dyn_col_one)",
		"DROP INDEX IF EXISTS dyn_idx_one",
	}
	for _, ddl := range ddlCases {
		if _, err := s.AnonymizeSQL(ddl, pg); err != nil {
			t.Fatalf("AnonymizeSQL(%q) failed: %v", ddl, err)
		}
	}

	if _, err := s.anonymizeIdentifierContextualLocked("   ", nil, 0); err == nil {
		t.Fatal("expected error on empty identifier")
	}

	// AnonymizeNaturalLanguage rich patterns: UUID, timestamp, single/double/smart quotes, key=value, numbers
	nlInput := "Query 'sales_schema' and \"orders_tbl\" and '' and 'table_1' and \u201csales_schema\u201d and \u201chello_world\u201d " +
		"where table_1=foo and col=table_1 and col=orders_tbl and order_amt=9876543 and flag=true " +
		"at 2026-11-10T08:00:00Z for id 550e8400-e29b-41d4-a716-446655440000 and code 6003 and count 7654321"
	_, _ = s.RegisterValue(ValString, "hello_world")
	anonNL, err := s.AnonymizeNaturalLanguage(nlInput)
	if err != nil {
		t.Fatalf("AnonymizeNaturalLanguage: %v", err)
	}
	if strings.Contains(anonNL, "orders_tbl") || strings.Contains(anonNL, "9876543") {
		t.Fatalf("AnonymizeNaturalLanguage leaked sensitive tokens: %s", anonNL)
	}

	// AnonymizeAssistantHistory branches
	if _, err := s.AnonymizeAssistantHistory("connect with password=my_secret_pw"); err == nil {
		t.Fatal("expected error for credential in assistant history")
	}
	histOut, err := s.AnonymizeAssistantHistory("Generated at 2026-11-10 08:00:00 for 550e8400-e29b-41d4-a716-446655440000 on orders_tbl with 7654321 and port 3306")
	if err != nil {
		t.Fatalf("AnonymizeAssistantHistory: %v", err)
	}
	if strings.Contains(histOut, "orders_tbl") {
		t.Fatalf("AnonymizeAssistantHistory leaked orders_tbl: %s", histOut)
	}

	// AssertZeroLeak credential and forbidden value detection
	if err := s.AssertZeroLeak("api_key: secret123"); err == nil {
		t.Fatal("expected error on credential leak in AssertZeroLeak")
	}
	if err := s.AssertZeroLeak("leaked orders_tbl identifier"); err == nil {
		t.Fatal("expected error on forbidden identifier leak in AssertZeroLeak")
	}
}

func TestValidateAndRestoreSQLAllBranches(t *testing.T) {
	pg := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	mysql := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}

	if _, err := ValidateAndRestoreSQL(nil, "SELECT 1", pg); err == nil {
		t.Fatal("expected error on nil session")
	}

	s := NewTurnPrivacySession()
	defer s.ZeroAndRelease()

	_, _ = s.RegisterSchema("public")
	tblTok, _ := s.RegisterTable("public", "users")
	colTok, _ := s.RegisterColumn("public", "users", "age")
	idxTok, _ := s.RegisterIndex("public", "users", "idx_users_age")
	intPh, _ := s.RegisterValue(ValInt, "25")
	numPh, _ := s.RegisterValue(ValNum, "99.5")
	boolPh, _ := s.RegisterValue(ValBool, "true")
	strPh, _ := s.RegisterValue(ValString, "alice")

	if _, err := ValidateAndRestoreSQL(s, "SELECT 1", fingerprint.Profile{Engine: "bad"}); err == nil {
		t.Fatal("expected error on invalid profile")
	}
	if _, err := ValidateAndRestoreSQL(s, "   ", pg); err == nil {
		t.Fatal("expected error on empty SQL")
	}
	if _, err := ValidateAndRestoreSQL(s, "/* comment */", pg); err == nil {
		t.Fatal("expected error on comment-only SQL")
	}
	if _, err := ValidateAndRestoreSQL(s, ";", pg); err == nil {
		t.Fatal("expected error on semicolon-only SQL")
	}

	// Valid statement kinds and clauses:
	// 1. SELECT * with LIMIT/OFFSET/COUNT(1)/:: cast/quoted identifiers/string-wrapped placeholder/bare placeholder
	selStarSQL := `SELECT *, COUNT(1), "` + colTok + `"::INT FROM "` + tblTok + `" WHERE ` + colTok + ` = '` + strPh + `' AND ` + colTok + ` = ` + strings.TrimPrefix(intPh, ":") + ` AND ` + colTok + ` = ` + numPh + ` AND ` + colTok + ` = ` + boolPh + ` LIMIT 10 OFFSET 5;`
	resSel, err := ValidateAndRestoreSQL(s, selStarSQL, pg)
	if err != nil {
		t.Fatalf("ValidateAndRestoreSQL(selStarSQL) failed: %v", err)
	}
	if resSel.StatementKind != domain.StmtSelect || len(resSel.SemanticWarnings) == 0 {
		t.Fatalf("expected StmtSelect with SELECT * warning, got %+v", resSel)
	}

	// 2. WITH ... SELECT
	cteTok, _ := s.mapper.Token(revprivacy.KindCTE, "recent_users")
	withSQL := "WITH " + cteTok + " AS (SELECT 1 FROM " + tblTok + ") SELECT " + colTok + " FROM " + tblTok
	if res, err := ValidateAndRestoreSQL(s, withSQL, pg); err != nil || res.StatementKind != domain.StmtSelect {
		t.Fatalf("expected valid WITH SELECT, got %+v err=%v", res, err)
	}

	// 3. UPDATE without WHERE (triggers semantic warning)
	updNoWhere := "UPDATE " + tblTok + " SET " + colTok + " = " + intPh
	resUpd, err := ValidateAndRestoreSQL(s, updNoWhere, mysql)
	if err != nil || resUpd.StatementKind != domain.StmtUpdate || len(resUpd.SemanticWarnings) == 0 {
		t.Fatalf("expected StmtUpdate with no-WHERE warning, got %+v err=%v", resUpd, err)
	}

	// 4. DELETE with WHERE
	delWhere := "DELETE FROM " + tblTok + " WHERE " + colTok + " = 0"
	if res, err := ValidateAndRestoreSQL(s, delWhere, mysql); err != nil || res.StatementKind != domain.StmtDelete || len(res.SemanticWarnings) != 0 {
		t.Fatalf("expected StmtDelete without warnings, got %+v err=%v", res, err)
	}

	// 5. CREATE TABLE with VARCHAR(64) and DECIMAL(10, 2)
	createTbl := "CREATE TABLE " + tblTok + " (" + colTok + " VARCHAR(64), " + colTok + " DECIMAL(10, 2))"
	if res, err := ValidateAndRestoreSQL(s, createTbl, mysql); err != nil || res.StatementKind != domain.StmtCreateTable {
		t.Fatalf("expected StmtCreateTable, got %+v err=%v", res, err)
	}

	// 6. CREATE UNIQUE INDEX / ALTER TABLE / DROP INDEX
	createIdx := "CREATE UNIQUE INDEX " + idxTok + " ON " + tblTok + " (" + colTok + ")"
	if res, err := ValidateAndRestoreSQL(s, createIdx, pg); err != nil || res.StatementKind != domain.StmtCreateIndex {
		t.Fatalf("expected StmtCreateIndex, got %+v err=%v", res, err)
	}
	alterTbl := "ALTER TABLE " + tblTok + " ADD COLUMN " + colTok + " INT"
	if res, err := ValidateAndRestoreSQL(s, alterTbl, pg); err != nil || res.StatementKind != domain.StmtAlterTable {
		t.Fatalf("expected StmtAlterTable, got %+v err=%v", res, err)
	}
	dropIdx := "DROP INDEX " + idxTok + " ON " + tblTok
	if res, err := ValidateAndRestoreSQL(s, dropIdx, mysql); err != nil || res.StatementKind != domain.StmtDropIndex {
		t.Fatalf("expected StmtDropIndex, got %+v err=%v", res, err)
	}

	// Error cases in ValidateAndRestoreSQL
	invalidSQLCases := []string{
		"SELECT " + colTok + " INTO OUTFILE " + strPh + " FROM " + tblTok,
		"SELECT " + colTok + " FROM " + tblTok + " WHERE " + colTok + " = :",
		"SELECT " + colTok + " FROM " + tblTok + " WHERE " + colTok + " = :not_a_val",
		"SELECT " + colTok + " FROM " + tblTok + " WHERE " + colTok + " = $1",
		"SELECT " + colTok + " FROM " + tblTok + " WHERE " + colTok + " = ':val_str_999'",
		"SELECT " + colTok + " FROM " + tblTok + " WHERE " + colTok + " = val_str_999",
		"SELECT " + colTok + " FROM " + tblTok + " WHERE " + colTok + " = 99999",
		`SELECT "table_999" FROM ` + tblTok,
		`SELECT "unmapped_quoted" FROM ` + tblTok,
		"SELECT 1",
		"WITH cte_1 AS (DELETE FROM " + tblTok + ") SELECT 1 FROM " + tblTok,
		"WITH cte_1 AS (VALUES (1))",
		"CREATE",
		"CREATE VIEW " + tblTok + " AS SELECT 1",
		"ALTER VIEW " + tblTok,
		"DROP",
		"MERGE INTO " + tblTok,
	}
	for _, badSQL := range invalidSQLCases {
		if _, err := ValidateAndRestoreSQL(s, badSQL, pg); err == nil {
			t.Fatalf("expected error for invalid SQL: %s", badSQL)
		}
	}

	// Direct unit tests for classifyAllowedStatement and formatSafeLiteral error paths
	if _, _, err := classifyAllowedStatement(nil); err == nil {
		t.Fatal("expected error for empty tokens in classifyAllowedStatement")
	}
	if _, err := formatSafeLiteral(BoundValue{Kind: ValString, RawValue: "a\x00b"}, pg); err == nil {
		t.Fatal("expected error for null byte in bound value")
	}
	if _, err := formatSafeLiteral(BoundValue{Kind: ValInt, RawValue: "not_int"}, pg); err == nil {
		t.Fatal("expected error for invalid int bound value")
	}
	if _, err := formatSafeLiteral(BoundValue{Kind: ValNum, RawValue: "not_num"}, pg); err == nil {
		t.Fatal("expected error for invalid num bound value")
	}
	if _, err := formatSafeLiteral(BoundValue{Kind: ValBool, RawValue: "not_bool"}, pg); err == nil {
		t.Fatal("expected error for invalid bool bound value")
	}
	if lit, err := formatSafeLiteral(BoundValue{Kind: "custom_kind", RawValue: "abc"}, pg); err != nil || lit != "'abc'" {
		t.Fatalf("unexpected default formatSafeLiteral result: %q err=%v", lit, err)
	}
}
