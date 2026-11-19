package fingerprint

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func analyzeObjects(t *testing.T, sql string, mapper *privacy.RunMapper) Result {
	t.Helper()
	res, err := testEngine(t).Analyze(
		Profile{Engine: "postgresql", Mode: "postgresql"},
		[]byte(sql),
		mapper,
	)
	if err != nil {
		t.Fatalf("analyze %q: %v", sql, err)
	}
	return res
}

func findObject(res Result, kind string, relation Relation) (ObjectReference, bool) {
	for _, object := range res.Objects {
		if object.Kind == kind && object.Relation == relation {
			return object, true
		}
	}
	return ObjectReference{}, false
}

func TestDropQualifiedTableProducesTargetAndSchemaReference(t *testing.T) {
	res := analyzeObjects(t, "DROP TABLE app.orders", privacy.NewRunMapper())
	if res.AnonymizedSQL != "drop table schema_1 . table_1" {
		t.Fatalf("anonymized SQL = %q", res.AnonymizedSQL)
	}
	table, ok := findObject(res, "table", RelationTarget)
	if !ok || table.Token != "table_1" || table.SchemaToken != "schema_1" || table.StatementOrdinal != 0 {
		t.Fatalf("target table object = %+v, ok=%v", table, ok)
	}
	schema, ok := findObject(res, "schema", RelationReference)
	if !ok || schema.Token != "schema_1" {
		t.Fatalf("schema reference object = %+v, ok=%v", schema, ok)
	}
	assertObjectsPrivate(t, res)
}

func TestCreateTableDefinesTableAndColumns(t *testing.T) {
	res := analyzeObjects(t, "CREATE TABLE app.orders (id int, amount decimal(10,2))", privacy.NewRunMapper())
	table, ok := findObject(res, "table", RelationDefine)
	if !ok || table.SchemaToken != "schema_1" {
		t.Fatalf("defined table object = %+v, ok=%v", table, ok)
	}
	var columns []ObjectReference
	for _, object := range res.Objects {
		if object.Kind == "column" && object.Relation == RelationDefine {
			columns = append(columns, object)
		}
	}
	if len(columns) != 2 {
		t.Fatalf("defined columns = %+v", columns)
	}
	for _, column := range columns {
		if column.ParentToken != table.Token || column.SchemaToken != "schema_1" {
			t.Fatalf("column parent/schema = %+v table=%+v", column, table)
		}
	}
	assertObjectsPrivate(t, res)
}

func TestAlterTableDropsColumnWithParent(t *testing.T) {
	res := analyzeObjects(t, "ALTER TABLE app.orders DROP COLUMN old_name", privacy.NewRunMapper())
	table, ok := findObject(res, "table", RelationTarget)
	if !ok {
		t.Fatalf("missing target table: %+v", res.Objects)
	}
	column, ok := findObject(res, "column", RelationTarget)
	if !ok || column.ParentToken != table.Token || column.SchemaToken != table.SchemaToken {
		t.Fatalf("target column = %+v table=%+v", column, table)
	}
	assertObjectsPrivate(t, res)
}

func TestAlterTableAddsColumnAsDefinition(t *testing.T) {
	res := analyzeObjects(t, "ALTER TABLE app.orders ADD COLUMN amount decimal(10,2)", privacy.NewRunMapper())
	table, ok := findObject(res, "table", RelationTarget)
	if !ok {
		t.Fatalf("missing target table: %+v", res.Objects)
	}
	column, ok := findObject(res, "column", RelationDefine)
	if !ok || column.ParentToken != table.Token {
		t.Fatalf("defined column = %+v table=%+v", column, table)
	}
}

func TestCreateIndexLinksIndexTableAndColumns(t *testing.T) {
	res := analyzeObjects(t, "CREATE INDEX idx_orders ON app.orders (customer_id)", privacy.NewRunMapper())
	index, ok := findObject(res, "index", RelationDefine)
	if !ok {
		t.Fatalf("missing index define: %+v", res.Objects)
	}
	table, ok := findObject(res, "table", RelationReference)
	if !ok || index.ParentToken != table.Token || index.SchemaToken != table.SchemaToken {
		t.Fatalf("index = %+v table=%+v", index, table)
	}
	column, ok := findObject(res, "column", RelationReference)
	if !ok || column.ParentToken != table.Token || column.SchemaToken != table.SchemaToken {
		t.Fatalf("column = %+v table=%+v", column, table)
	}
	assertObjectsPrivate(t, res)
}

func TestUpdatePreservesAliasTokensAndRelations(t *testing.T) {
	res := analyzeObjects(t, "UPDATE app.orders o SET o.amount = 1 WHERE o.id = 2", privacy.NewRunMapper())
	if !strings.Contains(res.AnonymizedSQL, "alias_1 . column_1") {
		t.Fatalf("qualified alias rendering lost alias role: %s", res.AnonymizedSQL)
	}
	table, ok := findObject(res, "table", RelationTarget)
	if !ok {
		t.Fatalf("missing target table: %+v", res.Objects)
	}
	alias, ok := findObject(res, "alias", RelationReference)
	if !ok || alias.ParentToken != table.Token || alias.SchemaToken != table.SchemaToken {
		t.Fatalf("alias object = %+v table=%+v", alias, table)
	}
	write, ok := findObject(res, "column", RelationWrite)
	if !ok || write.ParentToken != table.Token {
		t.Fatalf("write column = %+v", write)
	}
	filter, ok := findObject(res, "column", RelationFilter)
	if !ok || filter.ParentToken != table.Token {
		t.Fatalf("filter column = %+v", filter)
	}
	assertObjectsPrivate(t, res)
}

func TestDeleteFilterColumnIsLinked(t *testing.T) {
	res := analyzeObjects(t, "DELETE FROM app.orders WHERE id = ?", privacy.NewRunMapper())
	table, ok := findObject(res, "table", RelationTarget)
	if !ok {
		t.Fatalf("missing target table: %+v", res.Objects)
	}
	filter, ok := findObject(res, "column", RelationFilter)
	if !ok || filter.ParentToken != table.Token || filter.SchemaToken != table.SchemaToken {
		t.Fatalf("filter column = %+v table=%+v", filter, table)
	}
}

func TestInsertColumnsAreWrites(t *testing.T) {
	res := analyzeObjects(t, "INSERT INTO app.orders (id, amount) VALUES (1, 2)", privacy.NewRunMapper())
	table, ok := findObject(res, "table", RelationWrite)
	if !ok {
		t.Fatalf("missing write table: %+v", res.Objects)
	}
	var writes []ObjectReference
	for _, object := range res.Objects {
		if object.Kind == "column" && object.Relation == RelationWrite {
			writes = append(writes, object)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("write columns = %+v", writes)
	}
	for _, column := range writes {
		if column.ParentToken != table.Token {
			t.Fatalf("write column parent = %+v, table=%+v", column, table)
		}
	}
}

func TestTruncateQualifiedTableIsTarget(t *testing.T) {
	res := analyzeObjects(t, "TRUNCATE TABLE app.orders", privacy.NewRunMapper())
	table, ok := findObject(res, "table", RelationTarget)
	if !ok || table.SchemaToken == "" {
		t.Fatalf("truncate target = %+v, ok=%v", table, ok)
	}
}

func TestJoinAliasesAndSchemasRemainDistinct(t *testing.T) {
	res := analyzeObjects(t, `
SELECT o.id, u.name
FROM app.orders o
JOIN audit.users u ON u.order_id = o.id
WHERE u.active = true`, privacy.NewRunMapper())
	orders, ok := findObjectWithTokenKind(res, "table", RelationRead)
	if !ok || orders.SchemaToken != "schema_1" {
		t.Fatalf("orders read object = %+v, ok=%v", orders, ok)
	}
	users, ok := findObject(res, "table", RelationJoin)
	if !ok || users.Token == orders.Token || users.SchemaToken == orders.SchemaToken {
		t.Fatalf("users join object = %+v orders=%+v", users, orders)
	}
	var aliases []ObjectReference
	parents := map[string]bool{}
	tableCount := 0
	for _, object := range res.Objects {
		if object.Kind == "table" {
			tableCount++
		}
		if object.Kind == "alias" {
			aliases = append(aliases, object)
			parents[object.ParentToken] = true
		}
		if object.Kind == "column" && object.Relation == RelationFilter && object.ParentToken == "" {
			t.Fatalf("filter column lacks parent: %+v", object)
		}
	}
	if tableCount != 2 {
		t.Fatalf("join table count = %d objects=%+v", tableCount, res.Objects)
	}
	if len(aliases) < 2 || !parents[orders.Token] || !parents[users.Token] {
		t.Fatalf("alias graph = %+v parents=%v", aliases, parents)
	}
	if !strings.Contains(res.AnonymizedSQL, "alias_1 . column_1") ||
		!strings.Contains(res.AnonymizedSQL, "alias_2 . column_2") {
		t.Fatalf("join aliases were collapsed: %s", res.AnonymizedSQL)
	}
	if !strings.Contains(res.AnonymizedSQL, ":bool") {
		t.Fatalf("word literal was rendered as object token: %s", res.AnonymizedSQL)
	}
	assertObjectsPrivate(t, res)
}

func TestSameTableBasenameUsesDistinctSchemaTokens(t *testing.T) {
	res := analyzeObjects(t, "SELECT * FROM app.orders a JOIN audit.orders b ON a.id = b.id", privacy.NewRunMapper())
	var tables []ObjectReference
	for _, object := range res.Objects {
		if object.Kind == "table" && (object.Relation == RelationRead || object.Relation == RelationJoin) {
			tables = append(tables, object)
		}
	}
	if len(tables) != 2 {
		t.Fatalf("tables = %+v", tables)
	}
	if tables[0].Token != tables[1].Token {
		t.Fatalf("same basename may reuse table token: %+v", tables)
	}
	if tables[0].SchemaToken == tables[1].SchemaToken {
		t.Fatalf("distinct schemas must have distinct schema tokens: %+v", tables)
	}
}

func TestQuotedAndUnquotedIdentities(t *testing.T) {
	mapper := privacy.NewRunMapper()
	lower := analyzeObjects(t, "SELECT id FROM orders", mapper)
	upper := analyzeObjects(t, "SELECT id FROM ORDERS", mapper)
	lowerTable, _ := findObject(lower, "table", RelationRead)
	upperTable, _ := findObject(upper, "table", RelationRead)
	if lowerTable.Token != upperTable.Token {
		t.Fatalf("unquoted case must fold: %s != %s", lowerTable.Token, upperTable.Token)
	}

	quotedLower := analyzeObjects(t, `SELECT "id" FROM "orders"`, mapper)
	quotedUpper := analyzeObjects(t, `SELECT "id" FROM "Orders"`, mapper)
	qLowerTable, _ := findObject(quotedLower, "table", RelationRead)
	qUpperTable, _ := findObject(quotedUpper, "table", RelationRead)
	if qLowerTable.Token == lowerTable.Token || qUpperTable.Token == lowerTable.Token ||
		qLowerTable.Token == qUpperTable.Token {
		t.Fatalf("quoted identities collapsed: folded=%s lower=%s upper=%s",
			lowerTable.Token, qLowerTable.Token, qUpperTable.Token)
	}
}

func TestQualifiedQuotedAliasResolves(t *testing.T) {
	res := analyzeObjects(t, `SELECT "o"."id" FROM "App"."Orders" AS "o"`, privacy.NewRunMapper())
	if !strings.Contains(res.AnonymizedSQL, "alias_1 . column_1") {
		t.Fatalf("quoted alias was not preserved: %s", res.AnonymizedSQL)
	}
	table, ok := findObject(res, "table", RelationRead)
	if !ok || table.SchemaToken == "" {
		t.Fatalf("quoted table = %+v, ok=%v", table, ok)
	}
	alias, ok := findObject(res, "alias", RelationReference)
	if !ok || alias.ParentToken != table.Token {
		t.Fatalf("quoted alias = %+v table=%+v", alias, table)
	}
}

func TestCTENameDoesNotCollideWithPhysicalTable(t *testing.T) {
	res := analyzeObjects(t, "WITH orders AS (SELECT id FROM app.orders) SELECT id FROM orders", privacy.NewRunMapper())
	var definitions []ObjectReference
	var physicalReads []ObjectReference
	var cteReads []ObjectReference
	for _, object := range res.Objects {
		switch {
		case object.Kind == "cte" && object.Relation == RelationDefine:
			definitions = append(definitions, object)
		case object.Kind == "cte" && object.Relation == RelationRead:
			cteReads = append(cteReads, object)
		case object.Kind == "table" && object.Relation == RelationRead:
			physicalReads = append(physicalReads, object)
		}
	}
	if len(definitions) != 1 || len(physicalReads) != 1 || len(cteReads) != 1 {
		t.Fatalf("CTE refs definitions=%+v physical=%+v cte=%+v", definitions, physicalReads, cteReads)
	}
	if definitions[0].Token != cteReads[0].Token {
		t.Fatalf("CTE define/read must share token: %+v %+v", definitions[0], cteReads[0])
	}
	if definitions[0].Token == physicalReads[0].Token {
		t.Fatalf("CTE and physical table collided on %s", definitions[0].Token)
	}
}

func TestSelectScopesKeepCTEColumnsBoundToTheirOwnRelation(t *testing.T) {
	res := analyzeObjects(t, `
WITH recent AS (
  SELECT id AS event_id
  FROM events
)
SELECT event_id
FROM recent`, privacy.NewRunMapper())
	cte, ok := findObject(res, "cte", RelationRead)
	if !ok {
		t.Fatalf("missing CTE read object: %+v", res.Objects)
	}
	physical, ok := findObject(res, "table", RelationRead)
	if !ok {
		t.Fatalf("missing physical read object: %+v", res.Objects)
	}
	var physicalColumn, cteColumn bool
	for _, object := range res.Objects {
		if object.Kind != "column" {
			continue
		}
		switch object.ParentToken {
		case physical.Token:
			physicalColumn = true
		case cte.Token:
			cteColumn = true
		}
	}
	if !physicalColumn || !cteColumn {
		t.Fatalf("CTE/physical column parents incomplete: objects=%+v physical=%s cte=%s",
			res.Objects, physical.Token, cte.Token)
	}
}

func TestInnerAliasShadowsOuterWithoutRebindingOuterColumns(t *testing.T) {
	res := analyzeObjects(t, `
SELECT outer_o.id
FROM app.orders outer_o
WHERE EXISTS (
  SELECT 1
  FROM audit.logs outer_o
  WHERE outer_o.id = outer_o.order_id
)`, privacy.NewRunMapper())
	var tables []ObjectReference
	seenTables := map[string]bool{}
	for _, object := range res.Objects {
		if object.Kind == "table" && !seenTables[object.Token] {
			seenTables[object.Token] = true
			tables = append(tables, object)
		}
	}
	if len(tables) != 2 {
		t.Fatalf("missing scoped tables: %+v", res.Objects)
	}
	var readParent, filterParent string
	for _, table := range tables {
		var readBound, filterBound bool
		for _, object := range res.Objects {
			if object.Kind != "column" || object.ParentToken != table.Token {
				continue
			}
			switch object.Relation {
			case RelationRead:
				readBound = true
			case RelationFilter:
				filterBound = true
			}
		}
		if readBound {
			readParent = table.Token
		}
		if filterBound {
			filterParent = table.Token
		}
	}
	if readParent == "" || filterParent == "" || readParent == filterParent {
		t.Fatalf("scope columns bound to wrong tables: read=%q filter=%q objects=%+v",
			readParent, filterParent, res.Objects)
	}
}

func TestCorrelatedColumnKeepsOuterParentWithoutLocalShadow(t *testing.T) {
	res := analyzeObjects(t, `
SELECT outer_o.id
FROM app.orders outer_o
WHERE EXISTS (
  SELECT 1
  FROM audit.logs inner_l
  WHERE inner_l.order_id = outer_o.id
)`, privacy.NewRunMapper())
	var tables []ObjectReference
	for _, object := range res.Objects {
		if object.Kind == "table" {
			tables = append(tables, object)
		}
	}
	if len(tables) != 2 {
		t.Fatalf("missing scoped tables: %+v", res.Objects)
	}
	var readAndFilterTables, filterOnlyTables int
	for _, table := range tables {
		var readBound, filterBound bool
		for _, object := range res.Objects {
			if object.Kind != "column" || object.ParentToken != table.Token {
				continue
			}
			switch object.Relation {
			case RelationRead:
				readBound = true
			case RelationFilter:
				filterBound = true
			}
		}
		if readBound && filterBound {
			readAndFilterTables++
		} else if filterBound {
			filterOnlyTables++
		}
	}
	if readAndFilterTables != 1 || filterOnlyTables != 1 {
		t.Fatalf("correlated outer column lost its parent: read+filter=%d filter-only=%d objects=%+v",
			readAndFilterTables, filterOnlyTables, res.Objects)
	}
}

func TestLocalTableShadowsOuterAliasOfSameName(t *testing.T) {
	res := analyzeObjects(t, `
SELECT logs.id
FROM app.orders logs
WHERE EXISTS (
  SELECT 1
  FROM audit.logs
  WHERE logs.id = 1
)`, privacy.NewRunMapper())
	var tables []ObjectReference
	seenTables := map[string]bool{}
	for _, object := range res.Objects {
		if object.Kind == "table" && !seenTables[object.Token] {
			seenTables[object.Token] = true
			tables = append(tables, object)
		}
	}
	if len(tables) != 2 {
		t.Fatalf("missing scoped tables: %+v", res.Objects)
	}
	var readParent, filterParent string
	for _, table := range tables {
		for _, object := range res.Objects {
			if object.Kind != "column" || object.ParentToken != table.Token {
				continue
			}
			switch object.Relation {
			case RelationRead:
				readParent = table.Token
			case RelationFilter:
				filterParent = table.Token
			}
		}
	}
	if readParent == "" || filterParent == "" || readParent == filterParent {
		t.Fatalf("local table did not shadow outer alias: read=%q filter=%q objects=%+v",
			readParent, filterParent, res.Objects)
	}
}

func TestMySQLBacktickIdentityIsCasePreserving(t *testing.T) {
	e := testEngine(t)
	mapper := privacy.NewRunMapper()
	lower, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"},
		[]byte("SELECT id FROM `orders`"), mapper)
	if err != nil {
		t.Fatal(err)
	}
	upper, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"},
		[]byte("SELECT id FROM `Orders`"), mapper)
	if err != nil {
		t.Fatal(err)
	}
	lowerTable, _ := findObject(lower, "table", RelationRead)
	upperTable, _ := findObject(upper, "table", RelationRead)
	if lowerTable.Token == upperTable.Token {
		t.Fatalf("backtick case-sensitive identities collapsed: %+v %+v", lowerTable, upperTable)
	}
}

func TestMetadataContextCompleteness(t *testing.T) {
	exact := analyzeObjects(t, "SELECT 1", privacy.NewRunMapper())
	if !exact.MetadataContextComplete {
		t.Fatalf("exact object-free statement must prove complete metadata context")
	}
	if len(exact.Objects) != 0 {
		t.Fatalf("object-free statement refs = %+v", exact.Objects)
	}
	if exact.Objects == nil {
		t.Fatalf("object-free exact statement must carry an empty array, not nil")
	}
	fallback, err := testEngine(t).Analyze(
		Profile{Engine: "postgresql", Mode: "postgresql"},
		[]byte("CHECKPOINT"),
		privacy.NewRunMapper(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Quality != QualityLexer || fallback.MetadataContextComplete || len(fallback.Objects) != 0 {
		t.Fatalf("fallback completeness = quality=%s complete=%v objects=%+v",
			fallback.Quality, fallback.MetadataContextComplete, fallback.Objects)
	}
	if fallback.Objects == nil {
		t.Fatalf("fallback must carry an empty object array, not nil")
	}
}

func TestAnonymizedSQLAndObjectsExcludeRawReviewMaterial(t *testing.T) {
	res := analyzeObjects(t, `
SELECT o.secret_column
FROM app.secret_table o
WHERE o.password = 'hidden' -- internal-comment`, privacy.NewRunMapper())
	payload, err := json.Marshal(struct {
		SQL     string            `json:"sql"`
		Objects []ObjectReference `json:"objects"`
	}{
		SQL:     res.AnonymizedSQL,
		Objects: res.Objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"secret_column", "secret_table", "hidden", "internal-comment", "password", "app", " o ",
	} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("review payload leaked %q: %s", forbidden, payload)
		}
	}
	if !res.MetadataContextComplete {
		t.Fatalf("exact review context must be complete")
	}
}

func assertObjectsPrivate(t *testing.T, res Result) {
	t.Helper()
	raw, err := json.Marshal(res.Objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{"app", "audit", "orders", "users", "amount", "customer_id", "old_name"} {
		if strings.Contains(strings.ToLower(string(raw)), original) {
			t.Fatalf("object references leaked %q: %s", original, raw)
		}
	}
	for _, object := range res.Objects {
		if object.Token == "" || object.Kind == "" || object.Relation == "" || object.StatementOrdinal != 0 {
			t.Fatalf("incomplete object reference: %+v", object)
		}
	}
}

func findObjectWithTokenKind(res Result, kind string, relation Relation) (ObjectReference, bool) {
	return findObject(res, kind, relation)
}
