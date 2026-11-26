package fingerprint

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestV2ZeroDriftGuardCorpus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id         string
		profile    string
		sql        string
		quality    string
		kind       string
		canonical  string
		dialect    string
		shape      string
		anonymized string
	}{
		{
			id:         "select_as_alias",
			profile:    "mysql/mysql",
			sql:        "SELECT o.id, o.status AS st FROM shop.orders AS o WHERE o.id = 42",
			quality:    QualityExact,
			kind:       "select",
			canonical:  "bdc702fcabb71d8d7f613275296578331308840776d99765539665e6551734f0",
			dialect:    "cdc1e637343e40ac01a93a8612c1346ff84a4e3c3befabdb796ef5ec2bd1c5de",
			shape:      "5a629a2ceb31b1361287dd4e27daf6de5442360c83d3a0b36db1b494e355cc6d",
			anonymized: "select alias_1 . column_1 , alias_1 . column_2 as alias_2 from schema_1 . table_1 as alias_1 where alias_1 . column_1 = :int",
		},
		{
			id:         "select_implicit_alias_join",
			profile:    "mysql/mysql",
			sql:        "SELECT o.id, c.name FROM shop.orders o INNER JOIN shop.customers c ON c.id = o.customer_id WHERE c.active = 1",
			quality:    QualityExact,
			kind:       "select",
			canonical:  "715bb857fb21b4290c3280c6ff68c4be3a3d1d84f7d462e0ddb28c4bf62ec35c",
			dialect:    "59d6cc36f1ae1d01c5debdbe33b15f1b9965f6b245a212fbc34c35fcd29f9fa1",
			shape:      "f54f6e3bddd2f9116b07ec291b8f89443ea5621a5658659ed9321ab983c18ad1",
			anonymized: "select alias_1 . column_1 , alias_2 . column_2 from schema_1 . table_1 alias_1 inner join schema_1 . table_2 alias_2 on alias_2 . column_1 = alias_1 . column_3 where alias_2 . column_4 = :int",
		},
		{
			id:         "select_derived_as_alias",
			profile:    "mysql/mysql",
			sql:        "SELECT sub.id FROM (SELECT id FROM shop.orders WHERE status = 'paid') AS sub WHERE sub.id > 10",
			quality:    QualityExact,
			kind:       "select",
			canonical:  "560fb8f044c1b53e9bad109c91710edccf724f5b4304b558a81c0ffc7ab2195c",
			dialect:    "bedf14bcfc86efbaa3068fa3ab3acb0e0292c3432c665ab50f4a1819c9a4400d",
			shape:      "3c6e2d06ff2568b4d7d345e6cc7e6eab4e08d7331724113d2dfb957bd5f11855",
			anonymized: "select table_1 . column_1 from ( select column_1 from schema_1 . table_2 where column_2 = :string ) as alias_1 where table_1 . column_1 > :int",
		},
		{
			id:         "select_derived_quoted_keyword_alias",
			profile:    "mysql/mysql",
			sql:        "SELECT `update`.id FROM (SELECT id FROM shop.orders) AS `update`",
			quality:    QualityExact,
			kind:       "select",
			canonical:  "6eb2157db613dd493d9eae04200e5a3b4672d977fb41a5d1429ef1266b1815b6",
			dialect:    "eb489fbbbeebc22c5b9fea0f244a6db840d3f774f4d52f2ae768b41384f4d1b9",
			shape:      "9f3bd0a0b3a8be57332860311262fe11cc19f06925d747e308fbef0f30a596d0",
			anonymized: "select table_1 . column_1 from ( select column_1 from schema_1 . table_2 ) as alias_1",
		},
		{
			id:         "select_with_cte",
			profile:    "mysql/mysql",
			sql:        "WITH recent AS (SELECT id FROM shop.orders WHERE id > 10) SELECT r.id FROM recent AS r",
			quality:    QualityExact,
			kind:       "select",
			canonical:  "e8dcc1d6a1d554aac0887bd559d549260c590b74a12757bc5d221a7e3f1dc465",
			dialect:    "e3ac6331d6687680f86eccb87d8c5ec1c91c7075cd5791bb2b1e6cc641ece409",
			shape:      "515bc4dfaa6cc95aaa1fb4a2a1102e7598996208bdc057d8526735a37abbbf66",
			anonymized: "with cte_1 as ( select column_1 from schema_1 . table_1 where column_1 > :int ) select alias_1 . column_1 from cte_1 as alias_1",
		},
		{
			id:         "update_as_alias",
			profile:    "mysql/mysql",
			sql:        "UPDATE shop.orders AS o SET o.status = 'done' WHERE o.id = 1",
			quality:    QualityExact,
			kind:       "update",
			canonical:  "ef0c1ba66a6ada055c4e6e46a899b1f86827f4d1c8ccdea6f57322c43d7cd0da",
			dialect:    "794cc76b12961e1f87b7df7089cdece7d9c634afbb2b13ad6745290e0c6b79ed",
			shape:      "4de0fb5b49cd2d7cb30136768dad782bf5ffb94f0b963c1ad06d18e3cf34cabb",
			anonymized: "update schema_1 . table_1 as alias_1 set alias_1 . column_1 = :string where alias_1 . column_2 = :int",
		},
		{
			id:         "delete_as_alias",
			profile:    "mysql/mysql",
			sql:        "DELETE FROM shop.orders AS o WHERE o.id = 1",
			quality:    QualityExact,
			kind:       "delete",
			canonical:  "cfddfc7628d623425f80c12951543f8f329db215c99d262da541de0e96044ea8",
			dialect:    "43e5646fe319562cb90872200a6d55894fce92086d4324f096e9d31020a5e4e9",
			shape:      "caeea595707d58cd77eba598c6474400337f5560775b02a69df152e97d5f4a21",
			anonymized: "delete from schema_1 . table_1 as alias_1 where alias_1 . column_1 = :int",
		},
		{
			id:         "insert_as_alias",
			profile:    "mysql/mysql",
			sql:        "INSERT INTO shop.orders AS o (id, status) VALUES (1, 'new')",
			quality:    QualityExact,
			kind:       "insert",
			canonical:  "60e4b5275954f913f93450382b6ceebcb1f03eb0b573ff7915f3e46d90dab7da",
			dialect:    "05ca934ea5f9e2cf3691319cb354523703d15c039193fcded631171f783cb812",
			shape:      "f5b9ecfeeeecabf153788c42eaa406f816f016d7b7fc42ce6ce3923fffe48c5f",
			anonymized: "insert into schema_1 . table_1 as alias_1 ( column_1 , column_2 ) values ( :int , :string ) [1]",
		},
		{
			id:         "merge_as_alias",
			profile:    "postgresql/postgresql",
			sql:        "MERGE INTO shop.orders AS t USING shop.staging AS s ON t.id = s.id WHEN MATCHED THEN UPDATE SET status = s.status",
			quality:    QualityExact,
			kind:       "merge",
			canonical:  "b3528a99db60aee09b0128c07c8ef04d0ecca82cf7369a075e4eafe29c7328e1",
			dialect:    "d6de7c9e105021996cb3b22cb10955e9bbc42c47ef29bf3b793cfe74675ec0af",
			shape:      "2678a66ae78f06fa2a5c9e46d5f1866e14c29fe8880d6f38042a4aacd702b289",
			anonymized: "merge into schema_1 . table_1 as alias_1 using schema_1 . table_2 as alias_2 on alias_1 . column_1 = alias_2 . column_1 when matched then update set column_2 = alias_2 . column_2",
		},
		{
			id:         "create_table_plain",
			profile:    "mysql/mysql",
			sql:        "CREATE TABLE shop.shipments (id BIGINT PRIMARY KEY, order_id BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
			quality:    QualityExact,
			kind:       "create",
			canonical:  "b0fc2975b08350ee9d63e2335bb560602fedd7e8db3fc134576771184899afb2",
			dialect:    "09e51818ef2e11a22c97249892ef27bfe28e2300b8fd06caf9c623772474f796",
			shape:      "82aba003ed2a843f2aed89c1a573d75a5281c5706ad22e8150454bcfd64574f8",
			anonymized: "create table schema_1 . table_1 ( column_1 bigint primary key , column_2 bigint not :null ) engine = innodb default charset = utf8mb4",
		},
		{
			id:         "create_table_like",
			profile:    "mysql/mysql",
			sql:        "CREATE TABLE shop.orders_copy LIKE shop.orders",
			quality:    QualityExact,
			kind:       "create",
			canonical:  "a1b21248b5297963dee383b2ea50bb73622e8b048492d9d6b9a503be4a8d0d34",
			dialect:    "c31ec25441e9a8fccf7bb75b9e5340f4b556b9e1aa5cbe70386d0946f299e20e",
			shape:      "e20aec3c7ac3470e4be9cf6dc4635cdf615d2c9bd846b5156db0bd9c96fa2882",
			anonymized: "create table schema_1 . table_1 like schema_1 . table_2",
		},
		{
			id:         "create_index",
			profile:    "mysql/mysql",
			sql:        "CREATE INDEX idx_orders_status ON shop.orders (status)",
			quality:    QualityExact,
			kind:       "create",
			canonical:  "7f2319d311e121afca2f825af5dc5f70ed6e5c30a47ebd772110368d544aef9e",
			dialect:    "90fc457482f01b877e83f4456fcc8e8167df174b0b3edbdd8c66768adf26b29a",
			shape:      "7dc5576c20dc16bbae523e335180718c206c2a203c1bd38ebe4a14c984dea5be",
			anonymized: "create index index_1 on schema_1 . table_1 ( column_1 )",
		},
		{
			id:         "create_view_with_columns",
			profile:    "mysql/mysql",
			sql:        "CREATE OR REPLACE VIEW shop.v_active_orders (id, status, amount) AS SELECT * FROM shop.orders WHERE status = 'active'",
			quality:    QualityExact,
			kind:       "create",
			canonical:  "69f90c2547b150f89c1aae2cef8bdd060dd76029c8ad6f60f8bcab8cdca3bf24",
			dialect:    "812bd773ec2d843c452df8aac28c0908bf8055497fdacb824355899a9b899237",
			shape:      "6bd8bd3c069dbd6f2ddd69817957a0840add4fb3267b23573fae0fb32a1fba30",
			anonymized: "create or replace view schema_1 . table_1 ( column_1 , column_2 , column_3 ) as select * from schema_1 . table_2 where column_2 = :string",
		},
		{
			id:         "alter_table_rename_to",
			profile:    "mysql/mysql",
			sql:        "ALTER TABLE shop.orders RENAME TO shop.orders_v2",
			quality:    QualityExact,
			kind:       "alter",
			canonical:  "7bfafe8491a49adf367329326eef9012a7a2a1ad3b35bc29170d4e559326c868",
			dialect:    "f1f53437f82c85553e3e8aa28b8e35dc9ad6ee08d9dce8e8593a00af7f17d5af",
			shape:      "70b47acf9e805fb1e5bb8fd9d92b2688f3c646ac4dec6b5854ece9fc9a551863",
			anonymized: "alter table schema_1 . table_1 rename ident_1 schema_1 . table_2",
		},
		{
			id:         "drop_table",
			profile:    "mysql/mysql",
			sql:        "DROP TABLE IF EXISTS shop.tmp_orders",
			quality:    QualityExact,
			kind:       "drop",
			canonical:  "3441508e7052463c635d43b6e37b43354e38e42a0fb97b3149bdf05aaf2ba587",
			dialect:    "60b7161845eda06394928d0cf90897d86bdbcfa35b99609c544b0c7196dc822a",
			shape:      "1e6e0a49cfed3b598a2e166725063617bde5cbe7299125c62080a8e488c29e27",
			anonymized: "drop table ident_1 exists schema_1 . table_1",
		},
		{
			id:         "truncate_table",
			profile:    "mysql/mysql",
			sql:        "TRUNCATE TABLE shop.staging_events",
			quality:    QualityExact,
			kind:       "truncate",
			canonical:  "da1149ab62cfc1d0863f0e2086223f6cdb4e2db8cdc66707e89c2323d7b72d9f",
			dialect:    "9e410a7719f6e62d24534a8a9f21a24abca53032d6290c41e403734d3d63a937",
			shape:      "c9db8c678696105397f25a61189449a25e268f3f661e4ea56b4ddca6ac291d50",
			anonymized: "truncate table schema_1 . table_1",
		},
	}

	e := testEngine(t)
	for _, tc := range cases {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			profile, err := ParseProfile(tc.profile)
			if err != nil {
				t.Fatalf("ParseProfile(%q): %v", tc.profile, err)
			}
			res, err := e.Analyze(profile, []byte(tc.sql), privacy.NewRunMapper())
			if err != nil {
				t.Fatalf("Analyze(%q): %v", tc.id, err)
			}
			if res.Quality != tc.quality {
				t.Fatalf("quality = %q, want %q", res.Quality, tc.quality)
			}
			if res.StatementKind != tc.kind {
				t.Fatalf("kind = %q, want %q", res.StatementKind, tc.kind)
			}
			if res.CanonicalFingerprintHMAC != tc.canonical {
				t.Fatalf("canonical hash drifted:\n got  %s\n want %s", res.CanonicalFingerprintHMAC, tc.canonical)
			}
			if res.DialectFingerprintHMAC != tc.dialect {
				t.Fatalf("dialect hash drifted:\n got  %s\n want %s", res.DialectFingerprintHMAC, tc.dialect)
			}
			if res.ExecutionShapeHMAC != tc.shape {
				t.Fatalf("shape hash drifted:\n got  %s\n want %s", res.ExecutionShapeHMAC, tc.shape)
			}
			if res.AnonymizedSQL != tc.anonymized {
				t.Fatalf("anonymized SQL drifted:\n got  %q\n want %q", res.AnonymizedSQL, tc.anonymized)
			}
		})
	}
}

func TestV2FixedRenameViewAndCTAS(t *testing.T) {
	t.Parallel()

	e := testEngine(t)
	mysqlProfile := Profile{Engine: "mysql", Mode: "mysql"}

	t.Run("rename_cross_db", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("RENAME TABLE shop.orders_old TO archive.orders_old"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityExact {
			t.Fatalf("quality = %q, want %q", res.Quality, QualityExact)
		}
		if res.StatementKind != "rename" {
			t.Fatalf("kind = %q, want rename", res.StatementKind)
		}
		if !res.MetadataContextComplete {
			t.Fatal("expected MetadataContextComplete=true for RENAME TABLE")
		}
		if res.AnonymizedSQL != "rename table schema_1 . table_1 to schema_2 . table_1" {
			t.Fatalf("anonymized SQL = %q", res.AnonymizedSQL)
		}
		var hasTarget, hasDefine bool
		for _, obj := range res.Objects {
			if obj.Kind == "table" && obj.Relation == RelationTarget && obj.SchemaToken == "schema_1" {
				hasTarget = true
			}
			if obj.Kind == "table" && obj.Relation == RelationDefine && obj.SchemaToken == "schema_2" {
				hasDefine = true
			}
		}
		if !hasTarget || !hasDefine {
			t.Fatalf("expected target (schema_1) and define (schema_2) tables, got %+v", res.Objects)
		}
	})

	t.Run("rename_same_db_unqualified", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("RENAME TABLE orders_old TO orders_archive"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "rename" || !res.MetadataContextComplete {
			t.Fatalf("unexpected result: quality=%q kind=%q complete=%v", res.Quality, res.StatementKind, res.MetadataContextComplete)
		}
		if res.AnonymizedSQL != "rename table table_1 to table_2" {
			t.Fatalf("anonymized SQL = %q", res.AnonymizedSQL)
		}
		var hasTarget, hasDefine bool
		for _, obj := range res.Objects {
			if obj.Kind == "table" && obj.Relation == RelationTarget && obj.Token == "table_1" {
				hasTarget = true
			}
			if obj.Kind == "table" && obj.Relation == RelationDefine && obj.Token == "table_2" {
				hasDefine = true
			}
		}
		if !hasTarget || !hasDefine {
			t.Fatalf("expected table_1 target and table_2 define, got %+v", res.Objects)
		}
	})

	t.Run("rename_multi_pair", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("RENAME TABLE shop.a TO shop.b, shop.c TO archive.d"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "rename" || !res.MetadataContextComplete {
			t.Fatalf("unexpected result: quality=%q kind=%q complete=%v", res.Quality, res.StatementKind, res.MetadataContextComplete)
		}
		var targets, defines int
		for _, obj := range res.Objects {
			if obj.Kind == "table" && obj.Relation == RelationTarget {
				targets++
			}
			if obj.Kind == "table" && obj.Relation == RelationDefine {
				defines++
			}
		}
		if targets != 2 || defines != 2 {
			t.Fatalf("expected 2 target and 2 define tables, got targets=%d defines=%d (%+v)", targets, defines, res.Objects)
		}
	})

	t.Run("rename_user_fallback", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("RENAME USER 'old'@'localhost' TO 'new'@'localhost'"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityLexer || res.MetadataContextComplete || len(res.Objects) != 0 {
			t.Fatalf("expected compatible_lexer fallback for RENAME USER, got quality=%q complete=%v objects=%d",
				res.Quality, res.MetadataContextComplete, len(res.Objects))
		}
	})

	t.Run("rename_malformed_fallback", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("RENAME TABLE shop.a"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityLexer || res.MetadataContextComplete || len(res.Objects) != 0 {
			t.Fatalf("expected compatible_lexer fallback for malformed RENAME TABLE, got quality=%q complete=%v objects=%d",
				res.Quality, res.MetadataContextComplete, len(res.Objects))
		}
	})

	t.Run("bare_create_view", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("CREATE OR REPLACE VIEW shop.v_active_orders AS SELECT * FROM shop.orders WHERE status = 'active'"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "create" {
			t.Fatalf("unexpected quality/kind: %q/%q", res.Quality, res.StatementKind)
		}
		if res.MetadataContextComplete {
			t.Fatal("expected MetadataContextComplete=false for CREATE VIEW")
		}
		if res.Validity.Complete || res.Validity.Unsupported != "unsupported_view" {
			t.Fatalf("unexpected validity: %+v", res.Validity)
		}
	})

	t.Run("bare_create_view_with", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("CREATE VIEW shop.v_recent AS WITH recent AS (SELECT id FROM shop.orders) SELECT id FROM recent"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "create" || res.MetadataContextComplete {
			t.Fatalf("unexpected result: quality=%q kind=%q complete=%v", res.Quality, res.StatementKind, res.MetadataContextComplete)
		}
		if res.Validity.Unsupported != "unsupported_view" {
			t.Fatalf("unexpected validity: %+v", res.Validity)
		}
	})

	t.Run("ctas_select_star", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("CREATE TABLE shop.orders_bak AS SELECT * FROM shop.orders WHERE status = 'paid'"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "create" || !res.MetadataContextComplete {
			t.Fatalf("unexpected result: quality=%q kind=%q complete=%v", res.Quality, res.StatementKind, res.MetadataContextComplete)
		}
		if res.Validity.Complete || res.Validity.Unsupported != "create_table_as_select" {
			t.Fatalf("unexpected validity: %+v", res.Validity)
		}
		if res.AnonymizedSQL != "create table schema_1 . table_1 as select * from schema_1 . table_2 where column_1 = :string" {
			t.Fatalf("anonymized SQL = %q", res.AnonymizedSQL)
		}
		var hasDefine, hasRead bool
		for _, obj := range res.Objects {
			if obj.Kind == "table" && obj.Relation == RelationDefine {
				hasDefine = true
			}
			if obj.Kind == "table" && obj.Relation == RelationRead {
				hasRead = true
			}
		}
		if !hasDefine || !hasRead {
			t.Fatalf("expected defined target table and read source table, got %+v", res.Objects)
		}
	})

	t.Run("ctas_nested_select", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("CREATE TABLE shop.orders_bak AS SELECT sub.id FROM (SELECT id FROM shop.orders WHERE status = 'paid') AS sub"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze nested CTAS failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "create" {
			t.Fatalf("unexpected quality/kind: %q/%q", res.Quality, res.StatementKind)
		}
		if res.Validity.Complete || res.Validity.Unsupported != "create_table_as_select" {
			t.Fatalf("unexpected validity: %+v", res.Validity)
		}
	})

	t.Run("ctas_with_options_and_select", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("CREATE TABLE shop.orders_bak (id BIGINT NOT NULL) ENGINE=InnoDB AS SELECT id FROM shop.orders"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze CTAS with options failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "create" || res.Validity.Unsupported != "create_table_as_select" {
			t.Fatalf("unexpected result: quality=%q kind=%q validity=%+v", res.Quality, res.StatementKind, res.Validity)
		}
	})

	t.Run("ctas_with", func(t *testing.T) {
		t.Parallel()
		res, err := e.Analyze(mysqlProfile, []byte("CREATE TABLE shop.orders_bak AS WITH recent AS (SELECT id FROM shop.orders) SELECT id FROM recent"), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("Analyze CTAS WITH failed: %v", err)
		}
		if res.Quality != QualityExact || res.StatementKind != "create" {
			t.Fatalf("unexpected quality/kind: %q/%q", res.Quality, res.StatementKind)
		}
		if res.Validity.Complete || res.Validity.Unsupported != "create_table_as_select" {
			t.Fatalf("unexpected validity: %+v", res.Validity)
		}
	})
}
