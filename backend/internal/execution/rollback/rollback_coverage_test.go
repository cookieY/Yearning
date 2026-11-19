package rollback

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func strPtr(s string) *string { return &s }

func TestRollbackCoverageDDLHelpers(t *testing.T) {
	// Error formatting
	capErr := &CapabilityError{Reason: "some_reason"}
	if !strings.Contains(capErr.Error(), "some_reason") {
		t.Fatalf("unexpected CapabilityError: %s", capErr.Error())
	}
	capErrMsg := &CapabilityError{Reason: "r", Message: "explicit msg"}
	if capErrMsg.Error() != "explicit msg" {
		t.Fatalf("unexpected CapabilityError message: %s", capErrMsg.Error())
	}
	confErr := &ConflictError{Reason: "conf_reason"}
	if !strings.Contains(confErr.Error(), "conf_reason") {
		t.Fatalf("unexpected ConflictError: %s", confErr.Error())
	}
	confErrMsg := &ConflictError{Reason: "r", Message: "explicit conf"}
	if confErrMsg.Error() != "explicit conf" {
		t.Fatalf("unexpected ConflictError message: %s", confErrMsg.Error())
	}

	// synthesizeOSCInverseDDL across all kinds and engines
	before := &database.TableRollbackMetadata{
		Exists:         true,
		Engine:         "mysql",
		SchemaName:     "app",
		TableName:      "orders",
		CreateTableDDL: "CREATE TABLE `orders` (`id` bigint NOT NULL, PRIMARY KEY (`id`))",
		Columns: []database.RollbackColumnMeta{
			{
				Name:        "status",
				DataType:    "varchar",
				ColumnType:  "varchar(32)",
				Nullable:    false,
				DefaultExpr: strPtr("pending"),
				Comment:     "order's status\\note",
			},
			{
				Name:        "amount",
				DataType:    "int",
				ColumnType:  "int",
				Nullable:    true,
				DefaultExpr: strPtr("10"),
			},
			{
				Name:        "created_at",
				DataType:    "timestamp",
				ColumnType:  "timestamp(3)",
				Nullable:    false,
				DefaultExpr: strPtr("CURRENT_TIMESTAMP(3)"),
			},
		},
		Indexes: []database.RollbackIndexMeta{
			{Name: "idx_status", CreateDDL: "CREATE INDEX `idx_status` ON `app`.`orders` (`status`)"},
		},
		Constraints: []database.RollbackConstraintMeta{
			{Name: "chk_amt", Type: "CHECK", Definition: "amount >= 0"},
			{Name: "uq_status", Type: "UNIQUE", Columns: []string{"status"}},
		},
	}

	oscCases := []struct {
		engine  string
		parsed  *ParsedStatement
		wantErr bool
	}{
		{"mysql", &ParsedStatement{Kind: KindAddColumn, SchemaName: "app", TableName: "orders", ColumnName: "extra"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropColumn, SchemaName: "app", TableName: "orders", ColumnName: "status"}, false},
		{"postgresql", &ParsedStatement{Kind: KindDropColumn, SchemaName: "app", TableName: "orders", ColumnName: "status"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropColumn, SchemaName: "app", TableName: "orders", ColumnName: "missing"}, true},
		{"mysql", &ParsedStatement{Kind: KindRenameColumn, SchemaName: "app", TableName: "orders", ColumnName: "status", PostColumnName: "state"}, false},
		{"mysql", &ParsedStatement{Kind: KindAddIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_new"}, false},
		{"postgresql", &ParsedStatement{Kind: KindAddIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_new"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_status"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropIndex, SchemaName: "app", TableName: "orders", IndexName: "missing_idx"}, true},
		{"mysql", &ParsedStatement{Kind: KindModifyColumn, SchemaName: "app", TableName: "orders", ColumnName: "status"}, false},
		{"postgresql", &ParsedStatement{Kind: KindModifyColumn, SchemaName: "app", TableName: "orders", ColumnName: "status"}, false},
		{"mysql", &ParsedStatement{Kind: KindModifyColumn, SchemaName: "app", TableName: "orders", ColumnName: "missing"}, true},
		{"mysql", &ParsedStatement{Kind: KindAddConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "uq_new"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "chk_amt"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "uq_status"}, false},
		{"postgresql", &ParsedStatement{Kind: KindDropConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "chk_amt"}, false},
		{"mysql", &ParsedStatement{Kind: KindDropConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "missing_con"}, true},
		{"mysql", &ParsedStatement{Kind: KindCreateTable, SchemaName: "app", TableName: "orders"}, true},
	}
	for _, tc := range oscCases {
		stmts, _, err := synthesizeOSCInverseDDL(tc.engine, tc.parsed, before)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("expected error for %+v", tc.parsed)
			}
		} else if err != nil || len(stmts) == 0 {
			t.Fatalf("unexpected error for %+v: %v", tc.parsed, err)
		}
	}

	// verifyDiffAndGenerateInverseDDL across all kinds and error branches
	afterExists := *before
	afterExists.Columns = append([]database.RollbackColumnMeta(nil), before.Columns...)
	afterExists.Columns = append(afterExists.Columns, database.RollbackColumnMeta{Name: "added_col", ColumnType: "varchar(64)", Nullable: true})
	afterExists.Indexes = append([]database.RollbackIndexMeta(nil), before.Indexes...)
	afterExists.Indexes = append(afterExists.Indexes, database.RollbackIndexMeta{Name: "idx_added", CreateDDL: "CREATE INDEX idx_added ON orders(id)"})
	afterExists.Constraints = append([]database.RollbackConstraintMeta(nil), before.Constraints...)
	afterExists.Constraints = append(afterExists.Constraints, database.RollbackConstraintMeta{Name: "con_added", Type: "CHECK", Definition: "(id > 0)"})
	afterNotExists := &database.TableRollbackMetadata{Exists: false, SchemaName: "app", TableName: "orders"}

	diffCases := []struct {
		engine  string
		parsed  *ParsedStatement
		before  *database.TableRollbackMetadata
		after   *database.TableRollbackMetadata
		wantErr bool
	}{
		{"mysql", &ParsedStatement{Kind: KindCreateTable, SchemaName: "app", TableName: "orders"}, afterNotExists, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindCreateTable, SchemaName: "app", TableName: "orders"}, afterNotExists, afterNotExists, true},
		{"mysql", &ParsedStatement{Kind: KindDropTable, SchemaName: "app", TableName: "orders"}, before, afterNotExists, false},
		{"postgresql", &ParsedStatement{Kind: KindDropTable, SchemaName: "app", TableName: "orders"}, before, afterNotExists, false},
		{"mysql", &ParsedStatement{Kind: KindDropTable, SchemaName: "app", TableName: "orders"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindAddColumn, SchemaName: "app", TableName: "orders", ColumnName: "added_col"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindAddColumn, SchemaName: "app", TableName: "orders", ColumnName: "missing_col"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindDropColumn, SchemaName: "app", TableName: "orders", ColumnName: "status"}, before, afterNotExists, false},
		{"mysql", &ParsedStatement{Kind: KindDropColumn, SchemaName: "app", TableName: "orders", ColumnName: "missing_col"}, before, afterNotExists, true},
		{"mysql", &ParsedStatement{Kind: KindDropColumn, SchemaName: "app", TableName: "orders", ColumnName: "status"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindRenameColumn, SchemaName: "app", TableName: "orders", ColumnName: "old_c", PostColumnName: "added_col"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindRenameColumn, SchemaName: "app", TableName: "orders", ColumnName: "old_c", PostColumnName: "missing"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindRenameTable, SchemaName: "app", TableName: "orders", PostSchemaName: "app", PostTableName: "orders_v2"}, before, &afterExists, false},
		{"postgresql", &ParsedStatement{Kind: KindRenameTable, SchemaName: "app", TableName: "orders", PostSchemaName: "app", PostTableName: "orders_v2"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindRenameTable, SchemaName: "app", TableName: "orders", PostSchemaName: "app", PostTableName: "orders_v2"}, before, afterNotExists, true},
		{"mysql", &ParsedStatement{Kind: KindAddIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_added"}, before, &afterExists, false},
		{"postgresql", &ParsedStatement{Kind: KindAddIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_added"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindAddIndex, SchemaName: "app", TableName: "orders", IndexName: "missing_idx"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindDropIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_status"}, before, afterNotExists, false},
		{"mysql", &ParsedStatement{Kind: KindDropIndex, SchemaName: "app", TableName: "orders", IndexName: "missing_idx"}, before, afterNotExists, true},
		{"mysql", &ParsedStatement{Kind: KindDropIndex, SchemaName: "app", TableName: "orders", IndexName: "idx_status"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindModifyColumn, SchemaName: "app", TableName: "orders", ColumnName: "status", PostColumnName: "added_col"}, before, &afterExists, false},
		{"postgresql", &ParsedStatement{Kind: KindModifyColumn, SchemaName: "app", TableName: "orders", ColumnName: "status", PostColumnName: "added_col"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindModifyColumn, SchemaName: "app", TableName: "orders", ColumnName: "missing", PostColumnName: "added_col"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindAddConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "con_added"}, before, &afterExists, false},
		{"postgresql", &ParsedStatement{Kind: KindAddConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "con_added"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindAddConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "idx_added"}, before, &afterExists, false},
		{"mysql", &ParsedStatement{Kind: KindAddConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "missing_con"}, before, &afterExists, true},
		{"mysql", &ParsedStatement{Kind: KindDropConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "chk_amt"}, before, afterNotExists, false},
		{"mysql", &ParsedStatement{Kind: KindDropConstraint, SchemaName: "app", TableName: "orders", ConstraintName: "missing_con"}, before, afterNotExists, true},
		{"mysql", &ParsedStatement{Kind: StatementKind("UNKNOWN"), SchemaName: "app", TableName: "orders"}, before, &afterExists, true},
	}
	for _, tc := range diffCases {
		_, err := verifyDiffAndGenerateInverseDDL(tc.engine, tc.parsed, tc.before, tc.after)
		if tc.wantErr && err == nil {
			t.Fatalf("expected error for %+v", tc.parsed)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected error for %+v: %v", tc.parsed, err)
		}
	}

	// buildInverseDropTableDDL edge cases
	if _, err := buildInverseDropTableDDL("mysql", nil); err == nil {
		t.Fatal("expected error on nil metadata")
	}
	if _, err := buildInverseDropTableDDL("mysql", &database.TableRollbackMetadata{Exists: true}); err == nil {
		t.Fatal("expected error on empty CreateTableDDL")
	}
	pgBefore := &database.TableRollbackMetadata{
		Exists:         true,
		SchemaName:     "public",
		TableName:      "items",
		CreateTableDDL: `CREATE TABLE "public"."items" ("id" int PRIMARY KEY)`,
		Indexes: []database.RollbackIndexMeta{
			{Name: "items_pkey", Primary: true, CreateDDL: "CREATE UNIQUE INDEX items_pkey ON public.items (id)"},
			{Name: "uq_con_idx", Primary: false, CreateDDL: "CREATE UNIQUE INDEX uq_con_idx ON public.items (id)"},
			{Name: "idx_extra", Primary: false, CreateDDL: "CREATE INDEX idx_extra ON public.items (id)"},
		},
		Constraints: []database.RollbackConstraintMeta{
			{Name: "uq_con_idx", Type: "UNIQUE"},
		},
	}
	pgDropStmts, err := buildInverseDropTableDDL("postgresql", pgBefore)
	if err != nil || len(pgDropStmts) != 2 {
		t.Fatalf("expected 2 statements for pg drop table, got %v (err=%v)", pgDropStmts, err)
	}

	// buildInverseModifyColumnDDL PostgreSQL combinations
	colBase := database.RollbackColumnMeta{Name: "c", ColumnType: "varchar(32)", Nullable: false, DefaultExpr: nil}
	colAfter := database.RollbackColumnMeta{Name: "c", ColumnType: "varchar(64)", Nullable: true, DefaultExpr: strPtr("'x'")}
	stmts, err := buildInverseModifyColumnDDL("postgresql", `"public"."t"`, colBase, colAfter)
	if err != nil || len(stmts) != 3 {
		t.Fatalf("expected 3 pg alter column statements, got %v", stmts)
	}
	stmtsRev, err := buildInverseModifyColumnDDL("postgresql", `"public"."t"`, colAfter, colBase)
	if err != nil || len(stmtsRev) != 3 {
		t.Fatalf("expected 3 pg reverse alter column statements, got %v", stmtsRev)
	}

	// formatMySQLColumnDefault, canReconstructConstraint, buildDropConstraintDDL, buildAddConstraintDDL
	if got := formatMySQLColumnDefault(database.RollbackColumnMeta{DefaultExpr: nil}); got != "NULL" {
		t.Fatalf("expected NULL, got %s", got)
	}
	if got := formatMySQLColumnDefault(database.RollbackColumnMeta{DefaultExpr: strPtr("null")}); got != "NULL" {
		t.Fatalf("expected NULL, got %s", got)
	}
	if got := formatMySQLColumnDefault(database.RollbackColumnMeta{DataType: "datetime", DefaultExpr: strPtr("CURRENT_TIMESTAMP")}); got != "CURRENT_TIMESTAMP" {
		t.Fatalf("expected CURRENT_TIMESTAMP, got %s", got)
	}
	if got := formatMySQLColumnDefault(database.RollbackColumnMeta{DataType: "int", DefaultExpr: strPtr("42")}); got != "42" {
		t.Fatalf("expected 42, got %s", got)
	}

	if !canReconstructConstraint("postgresql", database.RollbackConstraintMeta{Definition: "CHECK (a > 0)"}) {
		t.Fatal("expected pg constraint reconstructible")
	}
	if !canReconstructConstraint("mysql", database.RollbackConstraintMeta{Type: "CHECK", Definition: "a > 0"}) {
		t.Fatal("expected mysql CHECK reconstructible")
	}
	if !canReconstructConstraint("mysql", database.RollbackConstraintMeta{Type: "UNIQUE", Columns: []string{"a"}}) {
		t.Fatal("expected mysql UNIQUE reconstructible")
	}
	if canReconstructConstraint("mysql", database.RollbackConstraintMeta{Type: "FOREIGN KEY"}) {
		t.Fatal("expected mysql FK not reconstructible")
	}

	for _, ct := range []string{"FOREIGN KEY", "CHECK", "UNIQUE", "PRIMARY KEY"} {
		_ = buildDropConstraintDDL("mysql", "`app`.`t`", database.RollbackConstraintMeta{Name: "c1", Type: ct})
		_ = buildAddConstraintDDL("mysql", "`app`.`t`", database.RollbackConstraintMeta{Name: "c1", Type: ct, Definition: "x > 0", Columns: []string{"x"}})
	}

	// isLosslessColumnModification & classifyColumnType
	colVarchar := database.RollbackColumnMeta{DataType: "varchar", ColumnType: "varchar(32)"}
	if !isLosslessColumnModification("mysql", colVarchar, "varchar(64)") {
		t.Fatal("expected varchar(32) -> varchar(64) lossless")
	}
	if !isLosslessColumnModification("mysql", colVarchar, "text") {
		t.Fatal("expected varchar(32) -> text lossless")
	}
	if isLosslessColumnModification("mysql", colVarchar, "varchar(16)") {
		t.Fatal("expected varchar(32) -> varchar(16) lossy")
	}
	if isLosslessColumnModification("mysql", colVarchar, "int") {
		t.Fatal("expected varchar -> int lossy")
	}
	for _, intSpec := range []string{"tinyint", "smallint", "mediumint", "int", "integer", "bigint"} {
		f, l := classifyColumnType(intSpec)
		if f != "int" || l <= 0 {
			t.Fatalf("unexpected classifyColumnType(%s) = %s, %d", intSpec, f, l)
		}
	}
	colSmallInt := database.RollbackColumnMeta{DataType: "smallint", ColumnType: "smallint"}
	if !isLosslessColumnModification("postgresql", colSmallInt, "SET DATA TYPE bigint") {
		t.Fatal("expected pg smallint -> bigint lossless")
	}
	if !isLosslessColumnModification("postgresql", colSmallInt, "TYPE integer") {
		t.Fatal("expected pg smallint -> integer lossless")
	}
	for _, pgClause := range []string{"SET DEFAULT 0", "DROP DEFAULT", "DROP NOT NULL", "SET NOT NULL"} {
		if !isLosslessColumnModification("postgresql", colSmallInt, pgClause) {
			t.Fatalf("expected pg %s lossless", pgClause)
		}
	}
	if extractLeadingDataType("") != "" || extractLeadingDataType("numeric(10,2) NOT NULL") != "numeric" {
		t.Fatal("unexpected extractLeadingDataType result")
	}
}

func TestRollbackCoverageDMLAndParserHelpers(t *testing.T) {
	// validateDMLTableCapabilities
	parsedUpd := &ParsedStatement{Kind: KindUpdate, SchemaName: "app", TableName: "t", UpdatedColumns: []string{"id"}}
	if err := validateDMLTableCapabilities(nil, parsedUpd); err == nil {
		t.Fatal("expected error for nil metadata")
	}
	if err := validateDMLTableCapabilities(&database.TableRollbackMetadata{Exists: true}, parsedUpd); err == nil {
		t.Fatal("expected error for missing identity keys")
	}
	if err := validateDMLTableCapabilities(&database.TableRollbackMetadata{
		Exists:             true,
		IdentityKeyColumns: []string{"id"},
		TriggerCount:       1,
	}, parsedUpd); err == nil {
		t.Fatal("expected error for triggers")
	}
	if err := validateDMLTableCapabilities(&database.TableRollbackMetadata{
		Exists:             true,
		Engine:             "postgresql",
		IdentityKeyColumns: []string{"id"},
		IsPartitioned:      true,
	}, parsedUpd); err == nil {
		t.Fatal("expected error for partitioned pg table")
	}
	if err := validateDMLTableCapabilities(&database.TableRollbackMetadata{
		Exists:             true,
		IdentityKeyColumns: []string{"id"},
		IncomingForeignKeys: []database.RollbackForeignKeyMeta{
			{ConstraintName: "fk_child", DeleteRule: "CASCADE"},
		},
	}, &ParsedStatement{Kind: KindDelete}); err == nil {
		t.Fatal("expected error for cascading FK")
	}
	if err := validateDMLTableCapabilities(&database.TableRollbackMetadata{
		Exists:             true,
		IdentityKeyColumns: []string{"id"},
	}, parsedUpd); err == nil {
		t.Fatal("expected error for PK modification")
	}

	// extractExplicitInsertPKPredicates
	meta := &database.TableRollbackMetadata{
		Exists:             true,
		IdentityKeyColumns: []string{"id"},
		Columns: []database.RollbackColumnMeta{
			{Name: "id", IsPrimaryKey: true},
			{Name: "name"},
		},
	}
	preds, ok := extractExplicitInsertPKPredicates("mysql", &ParsedStatement{
		InsertTuples: [][]string{{"10", "'alice'"}, {"20", "'bob'"}},
	}, meta)
	if !ok || len(preds) != 2 {
		t.Fatalf("expected 2 explicit PK predicates, got %v (ok=%v)", preds, ok)
	}
	if _, ok := extractExplicitInsertPKPredicates("mysql", &ParsedStatement{
		InsertColumns: []string{"name"},
		InsertTuples:  [][]string{{"'alice'"}},
	}, meta); ok {
		t.Fatal("expected false when PK column omitted")
	}
	if _, ok := extractExplicitInsertPKPredicates("mysql", &ParsedStatement{
		InsertColumns: []string{"id"},
		InsertTuples:  [][]string{{"NULL"}},
	}, meta); ok {
		t.Fatal("expected false when PK is NULL")
	}
	if _, ok := extractExplicitInsertPKPredicates("mysql", &ParsedStatement{
		InsertColumns: []string{"id"},
		InsertTuples:  [][]string{{"NOW()"}},
	}, meta); ok {
		t.Fatal("expected false when PK is function")
	}

	// buildInverseUpdateStatements, buildInverseDeleteStatements, buildInverseInsertStatements
	metaFull := &database.TableRollbackMetadata{
		Exists:             true,
		SchemaName:         "public",
		TableName:          "users",
		IdentityKeyColumns: []string{"id", "tenant_id"},
		Columns: []database.RollbackColumnMeta{
			{Name: "id", IsPrimaryKey: true, IsIdentity: true, IdentityGeneration: "ALWAYS"},
			{Name: "tenant_id", IsPrimaryKey: true},
			{Name: "name"},
			{Name: "gen_col", IsGenerated: true},
		},
	}
	rowBefore := database.RollbackRow{
		Cells: []database.RollbackCell{
			{Column: "id", Text: "1.00", DatabaseType: "NUMERIC"},
			{Column: "tenant_id", Text: "10", DatabaseType: "INT"},
			{Column: "name", Text: "alice", DatabaseType: "VARCHAR"},
			{Column: "gen_col", Text: "gen", DatabaseType: "VARCHAR"},
		},
	}
	rowAfterSame := database.RollbackRow{
		Cells: []database.RollbackCell{
			{Column: "id", Text: "1", DatabaseType: "NUMERIC"},
			{Column: "tenant_id", Text: "10", DatabaseType: "INT"},
			{Column: "name", Text: "alice", DatabaseType: "VARCHAR"},
			{Column: "gen_col", Text: "gen", DatabaseType: "VARCHAR"},
		},
	}
	updStmts, err := buildInverseUpdateStatements("postgresql", metaFull, []database.RollbackRow{rowBefore}, []database.RollbackRow{rowAfterSame})
	if err != nil || len(updStmts) != 1 {
		t.Fatalf("expected 1 idempotent inverse update statement, got %v (err=%v)", updStmts, err)
	}
	delStmts, err := buildInverseDeleteStatements("postgresql", metaFull, []database.RollbackRow{rowBefore})
	if err != nil || len(delStmts) != 1 || !strings.Contains(delStmts[0], "OVERRIDING SYSTEM VALUE") {
		t.Fatalf("expected OVERRIDING SYSTEM VALUE in pg delete inverse, got %v (err=%v)", delStmts, err)
	}
	insStmts, err := buildInverseInsertStatements("postgresql", metaFull, []database.RollbackRow{rowAfterSame})
	if err != nil || len(insStmts) != 1 {
		t.Fatalf("expected 1 inverse insert statement, got %v (err=%v)", insStmts, err)
	}
	multiWhere, err := BuildMultiRowPKWhereClause("postgresql", metaFull.IdentityKeyColumns, []database.RollbackRow{rowBefore})
	if err != nil || !strings.Contains(multiWhere, "AND") {
		t.Fatalf("unexpected composite BuildMultiRowPKWhereClause: %s (err=%v)", multiWhere, err)
	}
	if emptyWhere, err := BuildMultiRowPKWhereClause("mysql", []string{"id"}, nil); err != nil || emptyWhere != "1 = 0" {
		t.Fatalf("expected 1 = 0, got %s", emptyWhere)
	}

	// FormatCellLiteral branches (NULL, HexBytes, BOOL, numeric, escaped string)
	_ = FormatCellLiteral("mysql", database.RollbackCell{IsNull: true})
	_ = FormatCellLiteral("mysql", database.RollbackCell{HexBytes: "deadbeef"})
	_ = FormatCellLiteral("postgresql", database.RollbackCell{HexBytes: "deadbeef"})
	_ = FormatCellLiteral("mysql", database.RollbackCell{DatabaseType: "TINYINT(1)", Text: "true"})
	_ = FormatCellLiteral("postgresql", database.RollbackCell{DatabaseType: "BOOL", Text: "true"})
	_ = FormatCellLiteral("mysql", database.RollbackCell{DatabaseType: "TINYINT(1)", Text: "false"})
	_ = FormatCellLiteral("postgresql", database.RollbackCell{DatabaseType: "BOOL", Text: "false"})
	_ = FormatCellLiteral("mysql", database.RollbackCell{DatabaseType: "VARCHAR", Text: `a'b\c`})

	// buildPG18OldNewProjection & classifyPGStatementFailure
	proj := buildPG18OldNewProjection(metaFull.Columns)
	if !strings.Contains(proj, "__yr_old") || !strings.Contains(proj, "__yr_new") {
		t.Fatalf("unexpected pg18 projection: %s", proj)
	}
	_ = classifyPGStatementFailure(time.Now(), errors.New("syntax error"))

	// hasMySQLConditionalComment & diagnoseMySQLParseFailure
	if !hasMySQLConditionalComment("SELECT 'a\\'b', \"c\\\"d\", `e` -- comment\n # hash\n /*!50000 DROP TABLE t */") {
		t.Fatal("expected conditional comment detection")
	}
	if diagnoseMySQLParseFailure("MERGE INTO t USING s ON t.id = s.id", errors.New("parse err")) == nil {
		t.Fatal("expected MERGE error diagnosis")
	}
	if diagnoseMySQLParseFailure("DROP INDEX idx_a", errors.New("parse err")) == nil {
		t.Fatal("expected DROP INDEX without ON diagnosis")
	}
}

func TestRollbackCoverageASTBranches(t *testing.T) {
	if _, err := AnalyzeStatement("sqlite", "main", "SELECT 1"); err == nil {
		t.Fatal("expected unsupported engine error")
	}
	if _, err := AnalyzeStatement("mysql", "app", "   ; ; "); err == nil {
		t.Fatal("expected empty statement error")
	}

	mysqlCases := []struct {
		sql     string
		wantOK  bool
		wantRsn string
	}{
		{"INSERT INTO t (id, name) VALUES (1, 'a')", true, ""},
		{"UPDATE app.t AS u SET u.name = 'b' WHERE u.id = 1", true, ""},
		{"DELETE FROM app.t WHERE id = 1", true, ""},
		{"CREATE TABLE app.t (id INT PRIMARY KEY)", true, ""},
		{"DROP TABLE app.t", true, ""},
		{"CREATE UNIQUE INDEX idx_u ON app.t (name)", true, ""},
		{"DROP INDEX idx_u ON app.t", true, ""},
		{"RENAME TABLE app.t TO app.t2", true, ""},
		{"ALTER TABLE app.t ADD COLUMN c INT NOT NULL DEFAULT 1", true, ""},
		{"ALTER TABLE app.t DROP COLUMN c", true, ""},
		{"ALTER TABLE app.t MODIFY COLUMN c BIGINT", true, ""},
		{"ALTER TABLE app.t CHANGE COLUMN c c BIGINT", true, ""},
		{"ALTER TABLE app.t RENAME COLUMN c TO c2", true, ""},
		{"ALTER TABLE app.t RENAME TO app.t2", true, ""},
		{"ALTER TABLE app.t DROP INDEX idx_u", true, ""},
		{"ALTER TABLE app.t DROP FOREIGN KEY fk_1", true, ""},
		{"ALTER TABLE app.t DROP CHECK chk_1", true, ""},
		{"ALTER TABLE app.t ADD INDEX idx_1 (c)", true, ""},
		{"ALTER TABLE app.t ADD UNIQUE INDEX uq_1 (c)", true, ""},
		{"ALTER TABLE app.t ADD CONSTRAINT uq_con UNIQUE (c)", true, ""},
		{"ALTER TABLE app.t ADD CONSTRAINT chk_con CHECK (c > 0)", true, ""},
		{"ALTER TABLE app.t ADD CONSTRAINT fk_con FOREIGN KEY (c) REFERENCES t2(id)", true, ""},
		{"ALTER TABLE app.t ADD CONSTRAINT pk_con PRIMARY KEY (c)", true, ""},
		// Rejection cases
		{"REPLACE INTO t VALUES (1)", false, "replace_into_unsupported"},
		{"INSERT IGNORE INTO t VALUES (1)", false, "insert_ignore_unsupported"},
		{"INSERT INTO t VALUES (1) ON DUPLICATE KEY UPDATE id = 2", false, "upsert_on_conflict_unsupported"},
		{"INSERT INTO t SELECT * FROM t2", false, "insert_select_unsupported"},
		{"INSERT INTO t SET id = 1", false, "insert_non_values_unsupported"},
		{"INSERT INTO t PARTITION (p0) VALUES (1)", false, "insert_partition_unsupported"},
		{"INSERT INTO t (id, name) VALUES (1)", false, "insert_column_value_count_mismatch"},
		{"INSERT INTO t (id) VALUES (UUID())", false, "nondeterministic_function_uuid"},
		{"INSERT INTO t (id) VALUES ((SELECT 1))", false, "insert_subquery_value_unsupported"},
		{"WITH cte AS (SELECT 1) UPDATE t SET a = 1 WHERE id = 1", false, "cte_dml_unsupported"},
		{"UPDATE IGNORE t SET a = 1 WHERE id = 1", false, "update_ignore_unsupported"},
		{"UPDATE t1, t2 SET t1.a = 1 WHERE t1.id = t2.id", false, "update_join_unsupported"},
		{"UPDATE t1 JOIN t2 ON t1.id = t2.id SET t1.a = 1 WHERE t1.id = 1", false, "update_join_unsupported"},
		{"UPDATE t SET a = 1", false, "update_without_where_unsupported"},
		{"UPDATE t SET a = 1 WHERE id = 1 LIMIT 1", false, "update_order_limit_unsupported"},
		{"UPDATE t SET a = 1 WHERE id IN (SELECT id FROM t2)", false, "update_subquery_unsupported"},
		{"WITH cte AS (SELECT 1) DELETE FROM t WHERE id = 1", false, "cte_dml_unsupported"},
		{"DELETE IGNORE FROM t WHERE id = 1", false, "delete_modifier_unsupported"},
		{"DELETE t1 FROM t1 JOIN t2 ON t1.id = t2.id", false, "delete_using_or_join_unsupported"},
		{"DELETE FROM t", false, "delete_without_where_unsupported"},
		{"DELETE FROM t WHERE id = 1 LIMIT 5", false, "delete_order_limit_unsupported"},
		{"TRUNCATE TABLE t", false, "destructive_ddl_truncate_requires_backup"},
		{"DROP DATABASE app", false, "destructive_ddl_drop_schema_unsupported"},
		{"CREATE TEMPORARY TABLE t (id INT)", false, "create_temporary_or_unlogged_table_unsupported"},
		{"CREATE TABLE t LIKE t2", false, "create_table_as_select_or_like_unsupported"},
		{"DROP VIEW v1", false, "drop_view_unsupported"},
		{"DROP TEMPORARY TABLE t", false, "drop_temporary_table_unsupported"},
		{"DROP TABLE t1, t2", false, "multi_table_drop_unsupported"},
		{"RENAME TABLE a.t1 TO b.t2", false, "cross_schema_rename_table_unsupported"},
		{"RENAME TABLE t1 TO t2, t3 TO t4", false, "multi_table_rename_unsupported"},
		{"ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT", false, "multi_action_alter_table_unsupported"},
		{"ALTER TABLE t CHANGE COLUMN a b INT", false, "mysql_change_column_rename_and_type_combined_unsupported"},
		{"ALTER TABLE t ADD INDEX (a)", false, "add_index_requires_explicit_name"},
		{"ALTER TABLE t ADD UNIQUE INDEX (a)", false, "add_unique_index_requires_explicit_name"},
		{"ALTER TABLE t ADD CHECK (a > 0)", false, "unnamed_constraint_unsupported"},
		{"SELECT 1", false, "unsupported_mysql_ast_statement"},
	}
	for _, tc := range mysqlCases {
		_, err := AnalyzeStatement("mysql", "app", tc.sql)
		if tc.wantOK {
			if err != nil {
				t.Fatalf("mysql expected OK for %q, got %v", tc.sql, err)
			}
		} else {
			var capErr *CapabilityError
			if !errors.As(err, &capErr) || (tc.wantRsn != "" && capErr.Reason != tc.wantRsn) {
				t.Fatalf("mysql for %q expected reason %q, got %v", tc.sql, tc.wantRsn, err)
			}
		}
	}

	pgCases := []struct {
		sql     string
		wantOK  bool
		wantRsn string
	}{
		{"INSERT INTO public.t (id, name) VALUES (1, 'a')", true, ""},
		{"UPDATE public.t AS u SET name = 'b' WHERE u.id = 1", true, ""},
		{"DELETE FROM public.t AS u WHERE u.id = 1", true, ""},
		{"CREATE TABLE public.t (id INT PRIMARY KEY)", true, ""},
		{"CREATE UNIQUE INDEX idx_u ON public.t (name)", true, ""},
		{"DROP TABLE public.t", true, ""},
		{"DROP TABLE t", true, ""},
		{"DROP INDEX public.idx_u", true, ""},
		{"DROP INDEX idx_u", true, ""},
		{"ALTER TABLE public.t RENAME COLUMN a TO b", true, ""},
		{"ALTER TABLE public.t RENAME TO t2", true, ""},
		{"ALTER TABLE public.t ADD COLUMN c INT NOT NULL DEFAULT 1", true, ""},
		{"ALTER TABLE public.t DROP COLUMN c", true, ""},
		{"ALTER TABLE public.t ALTER COLUMN c TYPE BIGINT", true, ""},
		{"ALTER TABLE public.t ALTER COLUMN c SET DEFAULT 0", true, ""},
		{"ALTER TABLE public.t ALTER COLUMN c DROP NOT NULL", true, ""},
		{"ALTER TABLE public.t ALTER COLUMN c SET NOT NULL", true, ""},
		{"ALTER TABLE public.t ADD CONSTRAINT uq_1 UNIQUE (c)", true, ""},
		{"ALTER TABLE public.t ADD CONSTRAINT chk_1 CHECK (c > 0)", true, ""},
		{"ALTER TABLE public.t ADD CONSTRAINT fk_1 FOREIGN KEY (c) REFERENCES t2(id)", true, ""},
		{"ALTER TABLE public.t ADD CONSTRAINT pk_1 PRIMARY KEY (c)", true, ""},
		{"ALTER TABLE public.t DROP CONSTRAINT uq_1", true, ""},
		// Rejection cases
		{"MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE", false, "merge_unsupported"},
		{"WITH cte AS (SELECT 1) INSERT INTO t (id) VALUES (1)", false, "cte_dml_unsupported"},
		{"INSERT INTO t (id) VALUES (1) ON CONFLICT DO NOTHING", false, "upsert_on_conflict_unsupported"},
		{"INSERT INTO t (id) VALUES (1) RETURNING id", false, "dml_returning_clause_unsupported"},
		{"INSERT INTO t (id) OVERRIDING SYSTEM VALUE VALUES (1)", false, "insert_overriding_unsupported"},
		{"INSERT INTO t DEFAULT VALUES", false, "insert_non_values_unsupported"},
		{"INSERT INTO t SELECT * FROM t2", false, "insert_select_unsupported"},
		{"INSERT INTO t (id, name) VALUES (1)", false, "insert_column_value_count_mismatch"},
		{"INSERT INTO t (id) VALUES (gen_random_uuid())", false, "nondeterministic_function_gen_random_uuid"},
		{"WITH cte AS (SELECT 1) UPDATE t SET a = 1 WHERE id = 1", false, "cte_dml_unsupported"},
		{"UPDATE t SET a = t2.a FROM t2 WHERE t.id = t2.id", false, "update_from_unsupported"},
		{"UPDATE t SET a = 1 WHERE id = 1 RETURNING id", false, "dml_returning_clause_unsupported"},
		{"UPDATE t SET a = 1", false, "update_without_where_unsupported"},
		{"UPDATE t SET a[1] = 1 WHERE id = 1", false, "update_indirection_unsupported"},
		{"UPDATE t SET (a, b) = (1, 2) WHERE id = 1", false, "update_row_constructor_set_unsupported"},
		{"WITH cte AS (SELECT 1) DELETE FROM t WHERE id = 1", false, "cte_dml_unsupported"},
		{"DELETE FROM t USING t2 WHERE t.id = t2.id", false, "delete_using_or_join_unsupported"},
		{"DELETE FROM t WHERE id = 1 RETURNING id", false, "dml_returning_clause_unsupported"},
		{"DELETE FROM t", false, "delete_without_where_unsupported"},
		{"TRUNCATE TABLE t", false, "destructive_ddl_truncate_requires_backup"},
		{"CREATE TABLE t AS SELECT 1", false, "create_table_as_select_or_like_unsupported"},
		{"CREATE TEMP TABLE t (id INT)", false, "create_temporary_or_unlogged_table_unsupported"},
		{"CREATE TABLE t (LIKE t2)", false, "create_table_as_select_or_like_unsupported"},
		{"DROP TABLE t CASCADE", false, "drop_table_cascade_unsupported"},
		{"DROP TABLE t1, t2", false, "multi_table_drop_unsupported"},
		{"DROP SCHEMA s1", false, "destructive_ddl_drop_schema_unsupported"},
		{"DROP INDEX idx1 CASCADE", false, "drop_index_cascade_unsupported"},
		{"DROP INDEX idx1, idx2", false, "multi_index_drop_unsupported"},
		{"DROP VIEW v1", false, "unsupported_drop_object"},
		{"ALTER INDEX idx1 RENAME TO idx2", false, "unsupported_rename_object"},
		{"ALTER TABLE t DROP COLUMN c CASCADE", false, "drop_column_cascade_unsupported"},
		{"ALTER TABLE t ALTER COLUMN c TYPE INT USING c::int", false, "alter_column_type_using_expression_unsupported"},
		{"ALTER TABLE t ADD CHECK (c > 0)", false, "unnamed_constraint_unsupported"},
		{"ALTER TABLE t DROP CONSTRAINT c1 CASCADE", false, "drop_constraint_cascade_unsupported"},
	}
	for _, tc := range pgCases {
		_, err := AnalyzeStatement("postgresql", "public", tc.sql)
		if tc.wantOK {
			if err != nil {
				t.Fatalf("pg expected OK for %q, got %v", tc.sql, err)
			}
		} else {
			var capErr *CapabilityError
			if !errors.As(err, &capErr) || (tc.wantRsn != "" && capErr.Reason != tc.wantRsn) {
				t.Fatalf("pg for %q expected reason %q, got %v", tc.sql, tc.wantRsn, err)
			}
		}
	}

	explicitCases := []struct {
		sql          string
		wantSchema   string
		wantExplicit bool
	}{
		{"CREATE TABLE app.orders (id INT PRIMARY KEY)", "app", true},
		{"CREATE TABLE orders (id INT PRIMARY KEY)", "public", false},
		{"ALTER TABLE app.t1 RENAME TO orders", "app", true},
		{"ALTER TABLE t1 RENAME TO orders", "public", false},
		{"DROP TABLE app.orders", "app", true},
		{"DROP TABLE orders", "public", false},
		{"DROP INDEX app.idx_orders", "app", true},
		{"DROP INDEX idx_orders", "public", false},
		{"UPDATE app.orders SET v = 1 WHERE id = 1", "app", true},
		{"UPDATE orders SET v = 1 WHERE id = 1", "public", false},
		{"INSERT INTO app.orders (id) VALUES (1)", "app", true},
		{"INSERT INTO orders (id) VALUES (1)", "public", false},
		{"DELETE FROM app.orders WHERE id = 1", "app", true},
		{"DELETE FROM orders WHERE id = 1", "public", false},
	}
	for _, tc := range explicitCases {
		parsed, err := AnalyzeStatement("postgresql", "public", tc.sql)
		if err != nil {
			t.Fatalf("AnalyzeStatement(%q) unexpected error: %v", tc.sql, err)
		}
		if parsed.SchemaName != tc.wantSchema || parsed.SchemaExplicit != tc.wantExplicit {
			t.Fatalf("AnalyzeStatement(%q) got schema=%q explicit=%v, want schema=%q explicit=%v",
				tc.sql, parsed.SchemaName, parsed.SchemaExplicit, tc.wantSchema, tc.wantExplicit)
		}
	}
}
