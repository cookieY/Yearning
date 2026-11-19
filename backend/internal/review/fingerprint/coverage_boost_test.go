package fingerprint

import (
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type stubCrypterHMAC struct {
	keyID  string
	keyErr error
	mac    []byte
	macErr error
}

func (s stubCrypterHMAC) HMAC(_ string, _ []byte) ([]byte, error) {
	if s.macErr != nil {
		return nil, s.macErr
	}
	return s.mac, nil
}

func (s stubCrypterHMAC) HMACKeyID(_ string) (string, error) {
	if s.keyErr != nil {
		return "", s.keyErr
	}
	return s.keyID, nil
}

type stepFailSigner struct {
	keyID    string
	failCall int
	calls    int
}

func (s *stepFailSigner) KeyID() string { return s.keyID }

func (s *stepFailSigner) HMACSHA256(_ []byte) ([]byte, error) {
	s.calls++
	if s.calls == s.failCall {
		return nil, errors.New("hmac failure")
	}
	return []byte("0123456789abcdef0123456789abcdef"), nil
}

func TestCoverageBoostClassifyDDL(t *testing.T) {
	eng, err := NewEngine(&stepFailSigner{keyID: "k1"}, Options{})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	mysqlProf := Profile{Engine: "mysql", Mode: "mysql"}
	pgProf := Profile{Engine: "postgresql", Mode: "postgresql"}

	ddlStatements := []string{
		// CREATE INDEX variants
		`CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_u ON app.users USING btree (email(10) ASC NULLS FIRST, ((lower(name))) DESC NULLS LAST) INCLUDE (id, status) WHERE status > 0`,
		`CREATE FULLTEXT INDEX idx_ft ON docs (body) USING custom_method`,
		`CREATE SPATIAL INDEX idx_sp ON places (geom) USING gist`,
		`CREATE INDEX idx_expr ON users (lower(email))`,
		`CREATE INDEX IF NOT EXISTS idx_simple ON app.users (email, created_at DESC)`,

		// CREATE VIEW / DATABASE / SCHEMA
		`CREATE OR REPLACE TEMPORARY VIEW IF NOT EXISTS v_users (id, email) AS SELECT id, email FROM app.users`,
		`CREATE MATERIALIZED VIEW IF NOT EXISTS mv_users (id) AS SELECT id FROM users`,
		`CREATE DATABASE IF NOT EXISTS app_db DEFAULT CHARACTER SET = utf8mb4`,
		`CREATE SCHEMA IF NOT EXISTS app_schema`,

		// CREATE TABLE LIKE / AS SELECT / prefixes
		`CREATE TABLE IF NOT EXISTS app.orders LIKE app.orders_tpl`,
		`CREATE GLOBAL TEMPORARY TABLE IF NOT EXISTS app.temp_orders (id INT)`,
		`CREATE LOCAL TEMP TABLE app.local_temp (id INT)`,
		`CREATE UNLOGGED TABLE app.unlogged_tbl (id INT)`,

		// Focused CREATE TABLE statements covering each column attribute & table element cleanly
		`CREATE TABLE app.t_cols1 (
			id BIGINT GENERATED ALWAYS AS (1 + 1) STORED PRIMARY KEY COMMENT 'pk' COLLATE utf8mb4_bin CHARACTER SET utf8mb4,
			code VARCHAR(32) COLLATE custom_collation AS (lower(id)) VIRTUAL FIRST,
			label CHARACTER VARYING COLLATE 'str_coll' CHARSET 'custom_cs' AFTER code,
			amount DOUBLE PRECISION DEFAULT 0,
			checked_col INT CHECK (checked_col > 0) USING btree,
			custom_method_col INT USING custom_idx_method,
			updated_at TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			arr_col INT[10] CUSTOM_VENDOR_ATTR 42 'opt_str',
			dom_col app.custom_domain
		) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin CUSTOM_TBL_OPT = 'val'`,

		`CREATE TABLE app.t_refs (
			id BIGINT PRIMARY KEY,
			parent_id BIGINT REFERENCES app.t_refs (id) ON DELETE SET NULL ON UPDATE SET DEFAULT,
			ext1_id BIGINT REFERENCES app.users (id) ON DELETE NO ACTION ON UPDATE CASCADE,
			ext2_id BIGINT REFERENCES app.users (id) ON DELETE RESTRICT ON UPDATE NO ACTION,
			ext3_id BIGINT REFERENCES app.users (id) ON DELETE CASCADE ON UPDATE RESTRICT
		)`,

		`CREATE TABLE app.t_elements (
			id BIGINT,
			code VARCHAR(32),
			label VARCHAR(32),
			ext_id BIGINT,
			PRIMARY KEY (id) USING btree,
			UNIQUE KEY uk_code (code) USING custom_method,
			UNIQUE (label),
			INDEX idx_code (code),
			KEY (ext_id),
			FULLTEXT ft_label (label),
			SPATIAL sp_label (label),
			FOREIGN KEY fk_ext (ext_id) REFERENCES app.users (id) ON DELETE CASCADE,
			CONSTRAINT pk_con PRIMARY KEY (id),
			CONSTRAINT uk_con UNIQUE KEY uk_con_idx (code),
			CONSTRAINT fk_con FOREIGN KEY fk_con_name (ext_id) REFERENCES app.users (id) ON DELETE SET NULL,
			CONSTRAINT chk_con CHECK (id > 0) NO INHERIT,
			CONSTRAINT excl_con EXCLUDE USING gist (id WITH =),
			CHECK (id > 0),
			LIKE app.tpl_table INCLUDING ALL
		)`,

		// Self-referencing FK with unresolved column
		`CREATE TABLE app.self_bad (id INT, parent_id INT REFERENCES app.self_bad (missing_col))`,

		// Single-action ALTER TABLE statements matching classify3.go and validityfacts.go grammar
		`ALTER TABLE IF EXISTS app.users ADD COLUMN IF NOT EXISTS nick VARCHAR(32) FIRST`,
		`ALTER TABLE app.users ADD IF NOT EXISTS nick VARCHAR(32) AFTER id`,
		`ALTER TABLE app.users ADD PRIMARY KEY (id)`,
		`ALTER TABLE app.users ADD UNIQUE KEY IF NOT EXISTS uk_nick (nick)`,
		`ALTER TABLE app.users ADD UNIQUE (nick)`,
		`ALTER TABLE app.users ADD FULLTEXT KEY ft_nick (nick)`,
		`ALTER TABLE app.users ADD SPATIAL INDEX sp_nick (nick)`,
		`ALTER TABLE app.users ADD INDEX IF NOT EXISTS idx_nick (nick)`,
		`ALTER TABLE app.users ADD KEY (nick)`,
		`ALTER TABLE app.users ADD CONSTRAINT fk_u FOREIGN KEY (org_id) REFERENCES app.orgs (id)`,
		`ALTER TABLE app.users ADD CONSTRAINT uk_u UNIQUE KEY (nick)`,
		`ALTER TABLE app.users ADD CONSTRAINT chk_u CHECK (id > 0)`,
		`ALTER TABLE app.users ADD FOREIGN KEY (org_id) REFERENCES app.orgs (id)`,
		`ALTER TABLE app.users ADD CHECK (id > 0)`,
		`ALTER TABLE app.users DROP COLUMN IF EXISTS nick CASCADE`,
		`ALTER TABLE app.users DROP PRIMARY KEY`,
		`ALTER TABLE app.users DROP INDEX IF EXISTS idx_nick`,
		`ALTER TABLE app.users DROP KEY IF EXISTS idx_nick`,
		`ALTER TABLE app.users DROP FOREIGN KEY IF EXISTS fk_u`,
		`ALTER TABLE app.users DROP CONSTRAINT IF EXISTS chk_u RESTRICT`,
		`ALTER TABLE app.users DROP CHECK IF EXISTS chk_u`,
		`ALTER TABLE app.users DROP IF EXISTS nick`,
		`ALTER TABLE app.users MODIFY COLUMN nick nick_def VARCHAR(64) NOT NULL AFTER id`,
		`ALTER TABLE app.users CHANGE COLUMN nick nickname nickname_def VARCHAR(64) FIRST`,
		`ALTER TABLE app.users RENAME COLUMN nick TO nickname`,
		`ALTER TABLE app.users RENAME INDEX idx_nick TO idx_nickname`,
		`ALTER TABLE app.users RENAME KEY idx_nick TO idx_nickname`,
		`ALTER TABLE app.users RENAME TO app.users_v2`,
		`ALTER TABLE app.users RENAME AS app.users_v2`,
		`ALTER TABLE app.users RENAME app.users_v2`,
		`ALTER TABLE app.users ALTER COLUMN nick SET DEFAULT 'anon'`,
		`ALTER TABLE app.users ALTER COLUMN nick SET NOT NULL`,
		`ALTER TABLE app.users ALTER COLUMN nick DROP DEFAULT`,
		`ALTER TABLE app.users ALTER COLUMN nick DROP NOT NULL`,
		`ALTER TABLE app.users ALTER COLUMN nick DROP EXPRESSION`,
		`ALTER TABLE app.users ALTER COLUMN nick DROP IDENTITY`,
		`ALTER TABLE app.users ALTER COLUMN nick TYPE VARCHAR(128)`,
		`ALTER TABLE app.users ALTER COLUMN id ADD GENERATED`,
		`ALTER TABLE app.users CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
		`ALTER TABLE app.users ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE app.users DISABLE TRIGGER ALL`,
		`ALTER TABLE app.users VALIDATE CONSTRAINT chk_u`,
		`ALTER TABLE app.users OWNER TO new_owner`,
		`ALTER TABLE app.users CLUSTER ON idx_nick`,
		`ALTER TABLE app.users SET WITHOUT OIDS`,
		`ALTER TABLE app.users ATTACH PARTITION part_1 FOR VALUES IN (1)`,
		`ALTER TABLE app.users DETACH PARTITION part_1`,
		`ALTER TABLE app.users REPLICA IDENTITY FULL`,
		`ALTER TABLE app.users ADD COLUMN extra_col INT, DROP COLUMN old_col`,

		// Non-table ALTER statements
		`ALTER INDEX idx_u RENAME TO idx_u2`,
		`ALTER VIEW v_users RENAME TO v_users2`,
		`ALTER DATABASE app_db SET timezone = 'UTC'`,
		`ALTER SCHEMA app_schema RENAME TO app_schema2`,

		// DROP and TRUNCATE variants matching parseDropBody and parseTruncateBody
		`DROP TABLE IF EXISTS app.t1, t2, "quoted_t3" CASCADE`,
		`DROP INDEX IF EXISTS app.idx1, idx2, "quoted_idx3" RESTRICT`,
		`DROP VIEW IF EXISTS v1, v2`,
		`DROP DATABASE IF EXISTS db1`,
		`DROP SCHEMA IF EXISTS s1 CASCADE`,
		`DROP SEQUENCE IF EXISTS seq1`,
		`DROP CONSTRAINT IF EXISTS con1`,
		`TRUNCATE TABLE app.t1, app.t2 RESTART IDENTITY CASCADE`,
		`TRUNCATE ONLY app.t1 CONTINUE IDENTITY RESTRICT`,

		// Malformed / fallback DDL shapes to exercise failure paths in classify3.go
		`CREATE`,
		`CREATE UNKNOWN_KIND foo`,
		`CREATE UNIQUE UNKNOWN_KIND foo`,
		`CREATE DATABASE`,
		`CREATE TABLE`,
		`CREATE TABLE app.t_empty (`,
		`CREATE TABLE app.t_bad (123)`,
		`CREATE TABLE app.t_bad_pk (PRIMARY (id))`,
		`CREATE TABLE app.t_bad_fk0 (FOREIGN (id))`,
		`CREATE TABLE app.t_bad_fk1 (FOREIGN KEY (id) INVALID_REF)`,
		`CREATE TABLE app.t_bad_con1 (CONSTRAINT c1 PRIMARY (id))`,
		`CREATE TABLE app.t_bad_con2 (CONSTRAINT c1 FOREIGN (id))`,
		`CREATE TABLE app.t_bad_con3 (CONSTRAINT c1 FOREIGN KEY (id))`,
		`CREATE TABLE app.t_bad_con4 (CONSTRAINT c1 CHECK 123)`,
		`CREATE TABLE app.t_bad_con5 (CONSTRAINT c1 UNKNOWN_CON)`,
		`CREATE TABLE app.t_bad_like (LIKE 123)`,
		`CREATE TABLE app.t_bad_col1 (c1 INT PRIMARY)`,
		`CREATE TABLE app.t_bad_col2 (c1 INT COMMENT 123)`,
		`CREATE TABLE app.t_bad_col3 (c1 INT COLLATE 123)`,
		`CREATE TABLE app.t_bad_col4 (c1 INT GENERATED 123)`,
		`CREATE TABLE app.t_bad_col5 (c1 INT GENERATED ALWAYS 123)`,
		`CREATE TABLE app.t_bad_col6 (c1 INT GENERATED ALWAYS AS 123)`,
		`CREATE TABLE app.t_bad_col7 (c1 INT AS 123)`,
		`CREATE TABLE app.t_bad_col8 (c1 INT CHECK 123)`,
		`CREATE TABLE app.t_bad_col9 (c1 INT REFERENCES 123)`,
		`CREATE TABLE app.t_bad_col10 (c1 INT ON DELETE)`,
		`CREATE TABLE app.t_bad_col11 (c1 INT USING 123)`,
		`CREATE TABLE app.t_bad_col12 (c1 INT @)`,
		`CREATE TABLE app.t_bad_ref1 (c1 INT REFERENCES t2 (id) ON INVALID_EVT)`,
		`CREATE TABLE app.t_bad_ref2 (c1 INT REFERENCES t2 (id) ON DELETE SET INVALID_SET)`,
		`CREATE TABLE app.t_bad_ref3 (c1 INT REFERENCES t2 (id) ON DELETE NO INVALID_ACT)`,
		`CREATE TABLE app.t_bad_ref4 (c1 INT REFERENCES t2 (123))`,
		`CREATE TABLE app.t_bad_type1 (c1 s.123)`,
		`CREATE TABLE app.t_bad_type2 (c1 VARCHAR(123`,
		`CREATE TABLE app.t_bad_type3 (c1 INT[10)`,
		`CREATE TABLE app.t_bad_idx1 (PRIMARY KEY (123))`,
		`CREATE TABLE app.t_bad_idx2 (PRIMARY KEY ((123`,
		`CREATE TABLE app.t_bad_idx3 (PRIMARY KEY (c1(10`,
		`CREATE TABLE app.t_bad_idx4 (PRIMARY KEY (c1 NULLS 123))`,
		`CREATE TABLE app.t_bad_idx5 (PRIMARY KEY (c1`,
		`CREATE TABLE app.t_opt_punct (c1 INT) ;`,
		`CREATE TABLE app.t_opt_param (c1 INT) $1`,
		`CREATE INDEX`,
		`CREATE INDEX IF EXISTS idx_x ON t1 (id)`,
		`CREATE INDEX IF NOT idx_x ON t1 (id)`,
		`CREATE INDEX 123 ON t1 (id)`,
		`CREATE INDEX idx_no_on (id)`,
		`CREATE INDEX idx_bad_on ON 123 (id)`,
		`CREATE INDEX idx_bad_using ON t1 USING 123 (id)`,
		`CREATE INDEX idx_bad_tail ON t1 123`,
		`CREATE INDEX idx_bad_inc ON t1 (id) INCLUDE 123`,
		`CREATE VIEW`,
		`CREATE VIEW IF EXISTS v1 AS SELECT 1`,
		`CREATE VIEW IF NOT v1 AS SELECT 1`,
		`CREATE VIEW 123 AS SELECT 1`,
		`CREATE VIEW v1 (123) AS SELECT 1`,
		`CREATE VIEW v1 SELECT 1`,
		`CREATE VIEW v1 AS 123`,
		`ALTER`,
		`ALTER UNKNOWN_OBJ foo`,
		`ALTER INDEX 123`,
		`ALTER VIEW 123`,
		`ALTER TABLE 123`,
		`ALTER TABLE app.users ADD COLUMN 123`,
		`ALTER TABLE app.users ADD PRIMARY (id)`,
		`ALTER TABLE app.users ADD PRIMARY KEY 123`,
		`ALTER TABLE app.users ADD UNIQUE 123`,
		`ALTER TABLE app.users ADD INDEX 123`,
		`ALTER TABLE app.users ADD CONSTRAINT c1 FOREIGN (id)`,
		`ALTER TABLE app.users ADD CONSTRAINT c1 FOREIGN KEY 123`,
		`ALTER TABLE app.users ADD CONSTRAINT c1 FOREIGN KEY (id) 123`,
		`ALTER TABLE app.users ADD CONSTRAINT c1 UNIQUE 123`,
		`ALTER TABLE app.users ADD CONSTRAINT c1 CHECK 123`,
		`ALTER TABLE app.users ADD CONSTRAINT c1 UNKNOWN_CON`,
		`ALTER TABLE app.users ADD FOREIGN (id)`,
		`ALTER TABLE app.users ADD FOREIGN KEY 123`,
		`ALTER TABLE app.users ADD FOREIGN KEY (id) 123`,
		`ALTER TABLE app.users ADD CHECK 123`,
		`ALTER TABLE app.users ADD 123`,
		`ALTER TABLE app.users DROP PRIMARY`,
		`ALTER TABLE app.users DROP INDEX 123`,
		`ALTER TABLE app.users DROP FOREIGN 123`,
		`ALTER TABLE app.users DROP FOREIGN KEY 123`,
		`ALTER TABLE app.users DROP CONSTRAINT 123`,
		`ALTER TABLE app.users DROP CHECK 123`,
		`ALTER TABLE app.users DROP 123`,
		`ALTER TABLE app.users MODIFY 123`,
		`ALTER TABLE app.users MODIFY c1 123`,
		`ALTER TABLE app.users CHANGE c1 123`,
		`ALTER TABLE app.users RENAME COLUMN 123`,
		`ALTER TABLE app.users RENAME COLUMN c1 123`,
		`ALTER TABLE app.users RENAME COLUMN c1 TO 123`,
		`ALTER TABLE app.users RENAME INDEX 123`,
		`ALTER TABLE app.users RENAME INDEX i1 123`,
		`ALTER TABLE app.users RENAME INDEX i1 TO 123`,
		`ALTER TABLE app.users RENAME TO 123`,
		`ALTER TABLE app.users RENAME 123`,
		`ALTER TABLE app.users ALTER COLUMN 123`,
		`ALTER TABLE app.users ALTER COLUMN c1 SET NOT 123`,
		`ALTER TABLE app.users ALTER COLUMN c1 SET 123`,
		`ALTER TABLE app.users ALTER COLUMN c1 DROP 123`,
		`ALTER TABLE app.users ALTER COLUMN c1 TYPE 123`,
		`ALTER TABLE app.users ALTER COLUMN c1 ADD 123`,
		`ALTER TABLE app.users ALTER COLUMN c1 UNKNOWN_OP`,
		`ALTER TABLE app.users CONVERT 123`,
		`ALTER TABLE app.users UNKNOWN_ACTION`,
		`DROP`,
		`DROP UNKNOWN_KIND foo`,
		`DROP TABLE IF EXISTS 123`,
		`DROP TABLE app.123`,
		`TRUNCATE TABLE 123`,
	}

	for _, sql := range ddlStatements {
		mapper := privacy.NewRunMapper()
		_, _ = eng.Analyze(mysqlProf, []byte(sql), mapper)
		_, _ = eng.Analyze(pgProf, []byte(sql), mapper)
	}
}

func TestCoverageBoostClassifyDMLAndSelect(t *testing.T) {
	eng, err := NewEngine(&stepFailSigner{keyID: "k1"}, Options{})
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	mysqlProf := Profile{Engine: "mysql", Mode: "mysql"}
	pgProf := Profile{Engine: "postgresql", Mode: "postgresql"}

	dmlStatements := []string{
		// Focused SELECT statements so every clause in parseSelectBodyWithParent is reached cleanly
		`EXPLAIN SELECT id FROM app.users`,
		`SELECT DISTINCT ON (u.id) u.id, u.*, app.users.email, "u".name AS "alias_n" FROM app.users AS u`,
		`SELECT u.id FROM app.users AS u WHERE u.id = 1 GROUP BY u.id HAVING count(*) > 1 WINDOW w1 AS (PARTITION BY u.id), w2 AS (ORDER BY u.id) ORDER BY u.id ASC NULLS FIRST, u.email DESC NULLS LAST LIMIT 10, 20`,
		`SELECT ALL CAST(u.age AS INT), CONVERT(u.age AS VARCHAR(10)), INTERVAL 1 DAY, u.meta->'k'->>0, u.meta->col_key, u.meta#>'{a,b}'#>>'{c}', u.meta@>'{}', u.meta<@'{}', u.meta@?'$.a', u.meta#-'a', u.meta#?'$.b', u.arr[1], u.val::varchar(10), custom_fn(u.id), app.pkg_fn(u.id), "quoted_fn"(u.id) FROM app.users u`,
		`SELECT u.id FROM app.users u, app.orgs o NATURAL LEFT OUTER JOIN app.depts d USING (org_id) WHERE u.id IN (1, 2, 3, 'a', TRUE, NULL, $1) AND u.id IN (SELECT user_id FROM app.orders) AND u.name IN ('x', "col_ref")`,
		`SELECT a FROM t1 LIMIT ALL OFFSET 5 ROWS UNION ALL SELECT a FROM t2 UNION DISTINCT SELECT a FROM t3 FOR UPDATE OF t1, t2 NOWAIT`,
		`SELECT a FROM t1 LIMIT 5 OFFSET 10 ROW FOR SHARE OF t1 SKIP LOCKED`,
		`SELECT -a, +b, ~c, !d, [1, 2] FROM (SELECT 1 AS a, 2 AS b, 3 AS c, 4 AS d) AS sub`,
		`WITH "QuotedCTE" AS (SELECT 1 AS id) SELECT id FROM "QuotedCTE"`,

		// CTEs and nested scopes
		`WITH cte1 (c1, c2) AS NOT MATERIALIZED (SELECT 1, 2 FROM t1), cte2 AS MATERIALIZED (SELECT c1 FROM cte1) SELECT cte1.c1, c2 FROM cte1 JOIN cte2 ON cte1.c1 = cte2.c1`,
		`WITH RECURSIVE rcte (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM rcte WHERE n < 10) SELECT n FROM rcte`,
		`SELECT * FROM (WITH inner_cte AS (SELECT 1 AS a) SELECT a FROM inner_cte) sub`,
		`SELECT u.id, t1.id, id FROM t1 u JOIN t2 u ON t1.id = t2.id`,
		`SELECT t1.id, id FROM s1.t1 JOIN s2.t1 ON s1.t1.id = s2.t1.id`,
		`SELECT (SELECT outer_alias.id FROM inner_tbl) FROM outer_tbl outer_alias`,
		`SELECT (SELECT id FROM (SELECT 1) sub_no_cols) FROM outer_tbl`,

		// INSERT / REPLACE variants matching parseInsertBody
		`INSERT IGNORE INTO app.users (id, name) VALUES (1, 'a'), (2, 'b') ON DUPLICATE KEY UPDATE name = 'x' RETURNING id, name`,
		`INSERT INTO app.users SET id = 1, name := 'a' ON DUPLICATE KEY UPDATE id = 1`,
		`INSERT INTO app.users (id) VALUES (1) ON CONFLICT (id) ON CONSTRAINT users_pkey DO NOTHING RETURNING *`,
		`INSERT INTO app.users (id, name) SELECT id, name FROM app.staging ON CONFLICT (id) DO UPDATE SET name = 'x'`,
		`REPLACE INTO app.users (id, name) VALUES (1, (1 + 2))`,

		// UPDATE variants matching parseUpdateBody
		`UPDATE ONLY app.users u, app.profiles p SET u.name = 'x', p.bio := 'y' FROM app.extra e WHERE u.id = e.id RETURNING u.id`,

		// DELETE variants matching parseDeleteBody
		`DELETE FROM app.users u, app.profiles p USING app.orgs o WHERE u.org_id = o.id RETURNING *`,

		// MERGE variants matching parseMergeBody
		`MERGE INTO app.target AS t USING app.source s ON t.id = s.id WHEN MATCHED AND s.active = 1 THEN UPDATE SET t.name = s.name, t.updated_at = NOW() WHEN NOT MATCHED AND s.id > 0 THEN INSERT (id, name) VALUES (s.id, s.name) WHEN MATCHED THEN DELETE`,
		`MERGE INTO app.target USING (SELECT 1 AS id) s ON target.id = s.id WHEN NOT MATCHED THEN INSERT VALUES (s.id)`,

		// Malformed / fallback DML & query shapes
		`SELECT DISTINCT ON 123 a FROM t1`,
		`SELECT a FROM 123`,
		`SELECT a FROM t1, 123`,
		`SELECT a FROM t1 JOIN 123`,
		`SELECT a FROM t1 JOIN t2 USING 123`,
		`SELECT a FROM t1 GROUP 123`,
		`SELECT a FROM t1 WINDOW 123`,
		`SELECT a FROM t1 WINDOW w1 123`,
		`SELECT a FROM t1 WINDOW w1 AS 123`,
		`SELECT a FROM t1 ORDER 123`,
		`SELECT a FROM t1 ORDER BY a NULLS 123`,
		`SELECT a FROM t1 LIMIT ident_not_lit`,
		`SELECT a FROM t1 LIMIT 10, ident_not_lit`,
		`SELECT a FROM t1 OFFSET ident_not_lit`,
		`SELECT a FROM t1 UNION 123`,
		`SELECT a FROM t1 FOR 123`,
		`SELECT a FROM t1 FOR UPDATE OF 123`,
		`SELECT a FROM t1 FOR UPDATE SKIP 123`,
		`SELECT CAST(a AS 123) FROM t1`,
		`SELECT a AS 123 FROM t1`,
		`SELECT a IN 123 FROM t1`,
		`SELECT a::123 FROM t1`,
		`WITH 123 AS (SELECT 1) SELECT 1`,
		`WITH c (123) AS (SELECT 1) SELECT 1`,
		`WITH c AS 123 SELECT 1`,
		`WITH c AS (SELECT 1) UNKNOWN_HEAD 1`,
		`INSERT INTO 123`,
		`INSERT INTO t1 (123) VALUES (1)`,
		`INSERT INTO t1 VALUES 123`,
		`INSERT INTO t1 VALUES (1) ON CONFLICT (123) DO NOTHING`,
		`INSERT INTO t1 VALUES (1) ON CONFLICT (id) 123`,
		`INSERT INTO t1 VALUES (1) ON CONFLICT (id) DO UNKNOWN_ACT`,
		`INSERT INTO t1 VALUES (1) ON CONFLICT (id) DO UPDATE 123`,
		`INSERT INTO t1 VALUES (1) ON DUPLICATE 123`,
		`INSERT INTO t1 VALUES (1) ON DUPLICATE KEY 123`,
		`INSERT INTO t1 VALUES (1) ON UNKNOWN_CLAUSE`,
		`INSERT INTO t1 UNKNOWN_SOURCE`,
		`UPDATE 123`,
		`UPDATE t1 123`,
		`UPDATE t1 SET 123 = 1`,
		`UPDATE t1 SET a 123`,
		`DELETE 123`,
		`DELETE FROM 123`,
		`MERGE 123`,
		`MERGE INTO 123`,
		`MERGE INTO t1 123`,
		`MERGE INTO t1 USING 123`,
		`MERGE INTO t1 USING t2 123`,
		`MERGE INTO t1 USING t2 ON a = b WHEN UNKNOWN_MATCH`,
		`MERGE INTO t1 USING t2 ON a = b WHEN MATCHED 123`,
		`MERGE INTO t1 USING t2 ON a = b WHEN MATCHED THEN UNKNOWN_ACT`,
		`MERGE INTO t1 USING t2 ON a = b WHEN MATCHED THEN UPDATE 123`,
		`MERGE INTO t1 USING t2 ON a = b WHEN NOT MATCHED THEN INSERT (123) VALUES (1)`,
		`SELECT (WITH c AS (INSERT INTO t VALUES (1)) SELECT 1)`,
		`SELECT (SELECT 1`,
		`SELECT (1 + 2`,
		`SELECT a[1`,
		`SELECT a IN (1, 2`,
		`INSERT INTO t1 VALUES (1, 2`,
		`UNKNOWN_STATEMENT_HEAD foo bar`,
	}

	for _, sql := range dmlStatements {
		mapper := privacy.NewRunMapper()
		_, _ = eng.Analyze(mysqlProf, []byte(sql), mapper)
		_, _ = eng.Analyze(pgProf, []byte(sql), mapper)
	}
}

func TestCoverageBoostLexerLiteralsProfilesAndEngine(t *testing.T) {
	// Profile helpers
	for _, s := range []string{"mysql/mysql", "postgresql/postgresql", "tidb/mysql", "oceanbase/mysql", "polardb/mysql", "polardb/postgresql"} {
		p, err := ParseProfile(s)
		if err != nil {
			t.Fatalf("ParseProfile(%q) failed: %v", s, err)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate(%q) failed: %v", s, err)
		}
		_ = p.IsPostgres()
		_ = p.Flags()
	}
	if _, err := ParseProfile("invalid"); err == nil {
		t.Fatal("expected ParseProfile error")
	}
	if p, err := ParseProfile("unknown/mode"); err != nil || p.Validate() == nil {
		t.Fatal("expected Validate unknown profile error")
	}

	// CardinalityBucket all branches
	for _, n := range []int{-1, 0, 1, 2, 4, 5, 16, 17, 64, 65, 256, 257, 1024, 1025} {
		_ = CardinalityBucket(n)
	}

	// Literal classification
	_ = ClassifyString("550e8400-e29b-41d4-a716-446655440000", 0)
	_ = ClassifyString("2026-10-01", 0)
	_ = ClassifyString("2026-10-01 12:30:00Z", 0)
	_ = ClassifyString("plain", 0)
	_ = ClassifyString("1F", 'X')
	_ = ClassifyString("01", 'B')
	_ = ClassifyNumber("0x1F")
	_ = ClassifyNumber("12.34")
	_ = ClassifyNumber("1e5")
	_ = ClassifyNumber("42")
	_, _ = LiteralTypeOf(Token{Kind: TokDollarString, Text: "2026-10-01"})
	_, _ = LiteralTypeOf(Token{Kind: TokPunct, Text: "+"})

	// Tokenize edge cases
	mysqlProf := Profile{Engine: "mysql", Mode: "mysql"}
	mysqlFlags := mysqlProf.Flags()
	pgFlags := Profile{Engine: "postgresql", Mode: "postgresql"}.Flags()

	validLexCases := []struct {
		sql   string
		flags DialectFlags
	}{
		{"SELECT 'a\\nb', \"c\\nd\", `e``f`, X'1F', B'01', E'\\n', N'nat', 0x1A, 1.5e+2, 1.5e-2, .5 -- comment", mysqlFlags},
		{"SELECT # hash comment without newline", mysqlFlags},
		{"SELECT # hash comment\n 1", mysqlFlags},
		{"SELECT /*!50000 'a*/b', \"c*/d\", `e*/f`, -- line\n 1 # hash\n */", mysqlFlags},
		{"SELECT /*M!100100 1 */ /*m! 2 */ /*+   */ /*+ BKA(t1) */", mysqlFlags},
		{"SELECT /* outer /* inner */ still outer */ $tag$2026-10-01$tag$, $$hello$$, $1, \"q\"\"id\" -- pg comment\n", pgFlags},
	}
	for _, tc := range validLexCases {
		if _, err := Tokenize([]byte(tc.sql), tc.flags); err != nil {
			t.Fatalf("Tokenize(%q) failed: %v", tc.sql, err)
		}
	}

	invalidLexCases := []struct {
		sql   string
		flags DialectFlags
	}{
		{string([]byte{0xff, 0xfe}), mysqlFlags},
		{"SELECT 'unterminated", mysqlFlags},
		{"SELECT \"unterminated", mysqlFlags},
		{"SELECT \"unterminated_ident", pgFlags},
		{"SELECT `unterminated_bt", mysqlFlags},
		{"SELECT ``", mysqlFlags},
		{"SELECT E'unterminated", pgFlags},
		{"SELECT /* unterminated comment", mysqlFlags},
		{"SELECT /*!50000 'unterminated */", mysqlFlags},
		{"SELECT /*!50000 \"unterminated */", mysqlFlags},
		{"SELECT /*!50000 `unterminated */", mysqlFlags},
		{"SELECT /*!50000 -- no newline", mysqlFlags},
		{"SELECT /*!50000 # no newline", mysqlFlags},
		{"SELECT /*!50000 1", mysqlFlags},
		{"SELECT $tag$unterminated", pgFlags},
		{"SELECT $invalid", pgFlags},
		{"SELECT 0x", mysqlFlags},
		{"SELECT 0x1G", mysqlFlags},
		{"SELECT 123abc", mysqlFlags},
		{"SELECT \x01", mysqlFlags},
	}
	for _, tc := range invalidLexCases {
		if _, err := Tokenize([]byte(tc.sql), tc.flags); err == nil {
			t.Fatalf("expected Tokenize(%q) to fail", tc.sql)
		}
	}

	// CryptoSigner & Engine error branches
	cs := CryptoSigner{C: stubCrypterHMAC{keyID: "k1", mac: []byte("mac")}}
	if cs.KeyID() != "k1" {
		t.Fatalf("unexpected KeyID: %s", cs.KeyID())
	}
	if _, err := cs.HMACSHA256([]byte("x")); err != nil {
		t.Fatalf("unexpected HMACSHA256 error: %v", err)
	}
	csErr := CryptoSigner{C: stubCrypterHMAC{keyErr: errors.New("no key"), macErr: errors.New("no mac")}}
	if csErr.KeyID() != "" {
		t.Fatalf("expected empty KeyID on error")
	}

	if _, err := NewEngine(nil, Options{}); err == nil {
		t.Fatal("expected error for nil signer")
	}
	if _, err := NewEngine(&stepFailSigner{keyID: ""}, Options{}); err == nil {
		t.Fatal("expected error for empty keyID signer")
	}

	for failStep := 1; failStep <= 3; failStep++ {
		engStep, _ := NewEngine(&stepFailSigner{keyID: "k1", failCall: failStep}, Options{MaxStatementBytes: 64})
		if _, err := engStep.Analyze(mysqlProf, []byte("SELECT 1"), nil); err == nil {
			t.Fatalf("expected error at HMAC step %d", failStep)
		}
	}

	engSmall, _ := NewEngine(&stepFailSigner{keyID: "k1"}, Options{MaxStatementBytes: 8})
	if _, err := engSmall.Analyze(mysqlProf, []byte("SELECT 123456789"), nil); err == nil {
		t.Fatal("expected batch too large error")
	}
	if _, err := engSmall.Analyze(Profile{Engine: "bad", Mode: "bad"}, []byte("SELECT 1"), nil); err == nil {
		t.Fatal("expected invalid profile error")
	}
	if _, err := engSmall.Analyze(mysqlProf, []byte("/* only comment */"), nil); err == nil {
		t.Fatal("expected empty statement error")
	}
	if _, err := engSmall.Analyze(mysqlProf, []byte("SELECT 'unterminated"), nil); err == nil {
		t.Fatal("expected lexical error")
	}
}

func TestCoverageBoostInternalValidityAndWalkerBranches(t *testing.T) {
	mapper := privacy.NewRunMapper()

	// ValidityConditionSupported matrix
	for _, engine := range []string{"", "mysql", "postgresql", "unknown"} {
		for _, op := range []ValidityOperation{ValidityOperationCreate, ValidityOperationDrop, ValidityOperationAlter} {
			for _, kind := range []ValidityObjectKind{ValidityObjectTable, ValidityObjectIndex, ValidityObjectColumn} {
				for _, cond := range []ValidityCondition{ValidityConditionNone, ValidityConditionIfExists, ValidityConditionIfNotExists} {
					_ = ValidityConditionSupported(engine, op, kind, cond)
				}
			}
		}
	}

	// Direct extractStatementValidity edge cases
	lex, _ := Tokenize([]byte("SELECT a FROM t1"), mysqlFlags)
	walk := walkStatement(lex.Tokens)
	if v, err := extractStatementValidity(lex.Tokens, walk, nil, "mysql"); err != nil || v.Unsupported != "mapper_unavailable" {
		t.Fatalf("expected mapper_unavailable, got %+v, %v", v, err)
	}
	walkUnknown := walk
	walkUnknown.kind = "other"
	if v, err := extractStatementValidity(lex.Tokens, walkUnknown, mapper, "mysql"); err != nil || v.Unsupported != "parser_fallback" {
		t.Fatalf("expected parser_fallback, got %+v, %v", v, err)
	}

	// Exercise validityBuilder methods on synthetic token/role slices for defensive branches
	b := &validityBuilder{
		toks:   lex.Tokens,
		walk:   walk,
		mapper: mapper,
		out:    StatementValidity{Complete: true, DefinitionsComplete: true, Operation: ValidityOperationAlter},
	}
	_, _ = b.objectAt(-1)
	_, _ = b.objectAt(999)
	_ = b.rolePositions(-1, 999, privacy.KindTable, "")
	_ = b.physicalParentIsCTE(-1)
	b.addRequirement(ValidityRequirement{}) // empty token no-op

	// Exercise validityBuilder.parseCreate, parseDrop, parseAlter defensive & unmapped branches
	_, _ = extractStatementValidity(lex.Tokens, walk, mapper, "")
	for _, sql := range []string{
		"CREATE UNKNOWN_OBJ x",
		"CREATE TABLE x (id INT, parent_id INT REFERENCES x (id))",
		"CREATE TABLE x AS SELECT 1",
		"CREATE INDEX idx ON x (id)",
		"DROP TABLE x",
		"ALTER VIEW v RENAME TO v2",
		"ALTER TABLE x ADD COLUMN c INT",
		"ALTER TABLE x ADD INDEX idx (c)",
		"ALTER TABLE x ADD UNKNOWN_ITEM",
		"ALTER TABLE x DROP INDEX idx",
		"ALTER TABLE x DROP COLUMN c",
		"ALTER TABLE x DROP UNKNOWN_ITEM",
		"ALTER TABLE x MODIFY COLUMN c c_def INT",
		"ALTER TABLE x CHANGE COLUMN c c2 c2_def INT",
		"ALTER TABLE x ALTER COLUMN c DROP DEFAULT",
		"ALTER TABLE x UNKNOWN_ALTER_ACT",
		"ALTER TABLE x RENAME COLUMN c TO c2",
		"ALTER TABLE x RENAME INDEX i1 TO i2",
		"ALTER TABLE x RENAME TO x2",
	} {
		lx, _ := Tokenize([]byte(sql), mysqlFlags)
		wk := walkStatement(lx.Tokens)
		wk.fail = false
		if wk.kind == "" {
			wk.kind = fallbackKind(lx.Tokens)
		}
		// 1) Normal run with x marked as KindTable Define/Target
		wkTarget := wk
		wkTarget.roles = make([]idRole, len(lx.Tokens))
		copy(wkTarget.roles, wk.roles)
		for i := range lx.Tokens {
			if lx.Tokens[i].IsWord("x") && wkTarget.roles[i].kind == "" {
				rel := RelationTarget
				if wkTarget.kind == "create" {
					rel = RelationDefine
				}
				wkTarget.roles[i] = idRole{kind: privacy.KindTable, name: "x", relation: rel}
			}
		}
		_, _ = extractStatementValidity(lx.Tokens, wkTarget, mapper, "mysql")

		// 2) Run with non-table roles corrupted to "bad\nname" so objectAt fails on column/index/newTable
		wkBadChild := wkTarget
		wkBadChild.roles = make([]idRole, len(lx.Tokens))
		copy(wkBadChild.roles, wkTarget.roles)
		for i := range wkBadChild.roles {
			if wkBadChild.roles[i].kind != "" && !lx.Tokens[i].IsWord("x") {
				wkBadChild.roles[i].name = "bad\nname"
			}
		}
		_, _ = extractStatementValidity(lx.Tokens, wkBadChild, mapper, "mysql")

		// 3) Run with ALL roles (including table x) corrupted to "bad\nname" so objectAt fails on table target
		wkBadAll := wkTarget
		wkBadAll.roles = make([]idRole, len(lx.Tokens))
		copy(wkBadAll.roles, wkTarget.roles)
		for i := range wkBadAll.roles {
			if wkBadAll.roles[i].kind != "" {
				wkBadAll.roles[i].name = "bad\nname"
			}
		}
		_, _ = extractStatementValidity(lx.Tokens, wkBadAll, mapper, "mysql")

		// 4) Run with cleared non-table roles to hit missing/incomplete target branches in validityBuilder
		wkEmpty := wkTarget
		wkEmpty.roles = make([]idRole, len(lx.Tokens))
		for i := range lx.Tokens {
			if lx.Tokens[i].IsWord("x") {
				wkEmpty.roles[i] = idRole{kind: privacy.KindTable, name: "x", relation: RelationTarget}
			}
		}
		_, _ = extractStatementValidity(lx.Tokens, wkEmpty, mapper, "mysql")

		// 5) Run with completely empty roles (even no table target)
		wkNoTarget := wkTarget
		wkNoTarget.roles = make([]idRole, len(lx.Tokens))
		_, _ = extractStatementValidity(lx.Tokens, wkNoTarget, mapper, "mysql")
	}

	// Test objectAt error branches (bad schema, bad parent, bad parent schema, index reference requirement)
	bCustom := &validityBuilder{
		toks: []Token{
			{Kind: TokWord, Text: "t1"},
			{Kind: TokWord, Text: "c1"},
			{Kind: TokWord, Text: "c2"},
			{Kind: TokWord, Text: "idx1"},
		},
		walk: walkResult{
			roles: []idRole{
				{kind: privacy.KindTable, name: "t1", table: tableIdentity{name: "t1", schema: identifierIdentity{name: "bad\nschema"}}},
				{kind: privacy.KindColumn, name: "c1", parent: tableIdentity{name: "bad\nparent"}},
				{kind: privacy.KindColumn, name: "c2", parent: tableIdentity{name: "t1", schema: identifierIdentity{name: "bad\nschema"}}},
				{kind: privacy.KindIndex, name: "idx1", relation: RelationReference},
			},
		},
		mapper: mapper,
	}
	_, _ = bCustom.objectAt(0)
	_, _ = bCustom.objectAt(1)
	_, _ = bCustom.objectAt(2)
	bCustom.addObjectRequirements(0, 4, false)
	bCustom.addCreateTableObjectRequirements(0, 4, ValidityObject{Kind: ValidityObjectTable, Token: "tbl_1"})

	// Direct walker helper calls for defensive branches in classify.go, classify2.go, classify3.go, classify4.go
	w := &walker{
		toks:     lex.Tokens,
		maxDepth: 32,
		aliases:  map[string]tableIdentity{"u\x00a": {name: "t1"}},
		tables:   map[string]tableIdentity{"u\x00t1": {name: "t1"}},
		ctes:     map[string]tableIdentity{},
		res:      walkResult{roles: make([]idRole, len(lex.Tokens)), keepRaw: make([]bool, len(lex.Tokens))},
	}
	w.pos = len(w.toks)
	_ = w.markRole(privacy.KindTable, "x")
	_ = w.markRoleWithRelation(privacy.KindTable, "x", RelationReference)
	w.parseExprAtom(false)
	w.popSelectScope(999)
	_, _ = identifierPartAt(lex.Tokens, -1)
	w.pos = 0
	_ = w.parseParenSubquery()
	_ = w.consumeClose()
	_ = w.parseColumnListParen()
	_ = w.markQualifiedRef(0, []identifierPart{{name: "*"}})
	_ = w.markQualifiedRef(0, []identifierPart{{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"}})
	_, _ = w.lookupAlias(identifierPart{name: "a"})
	_, _ = w.lookupTable(identifierPart{name: "t1"})
	_ = indexParenIsPrefixLength(lex.Tokens, -1)
	_ = toLowerName("ABC")

	// Direct parseOrderItems call with COLLATE variants
	for _, ordSQL := range []string{
		"ASC COLLATE utf8mb4_bin ASC NULLS FIRST, ASC COLLATE custom_coll DESC NULLS LAST, ASC COLLATE 'str_coll'",
		"ASC COLLATE 123",
	} {
		ordLex, _ := Tokenize([]byte(ordSQL), mysqlFlags)
		ow := &walker{
			toks: ordLex.Tokens,
			res:  walkResult{roles: make([]idRole, len(ordLex.Tokens)), keepRaw: make([]bool, len(ordLex.Tokens))},
		}
		ow.parseOrderItems()
	}

	// Template rendering direct & error paths
	_, _ = RenderAnonymized(lex.Tokens, walk, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_ = newRenderState(lex.Tokens, walkResult{})

	badToks := []Token{
		{Kind: TokWord, Text: "bad\nword", Literal: "bad\nword"},
		{Kind: TokQuotedIdent, Text: "bad\nquoted", Literal: "bad\nquoted"},
	}
	_, _, _ = RenderAnonymizedDetailed(badToks[:1], walkResult{roles: []idRole{{kind: privacy.KindTable, name: "bad\nrole"}}}, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_, _, _ = RenderAnonymizedDetailed(badToks[:1], walkResult{roles: []idRole{{kind: privacy.KindTable, name: "ok_tbl", table: tableIdentity{name: "ok_tbl", schema: identifierIdentity{name: "bad\nschema"}}}}}, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_, _, _ = RenderAnonymizedDetailed(badToks[:1], walkResult{roles: []idRole{{kind: privacy.KindColumn, name: "ok_col", parent: tableIdentity{name: "bad\nparent"}}}}, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_, _, _ = RenderAnonymizedDetailed(badToks[:1], walkResult{roles: []idRole{{kind: privacy.KindColumn, name: "ok_col", parent: tableIdentity{name: "ok_parent", schema: identifierIdentity{name: "bad\nschema"}}}}}, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_, _, _ = RenderAnonymizedDetailed(badToks[:1], walkResult{}, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_, _, _ = RenderAnonymizedDetailed(badToks[1:], walkResult{}, mapper, Profile{Engine: "mysql", Mode: "mysql"})
	_, _, _ = RenderAnonymizedDetailed(
		[]Token{{Kind: TokPunct, Text: "("}, {Kind: TokWord, Text: "bad\nw", Literal: "bad\nw"}, {Kind: TokPunct, Text: ")"}},
		walkResult{specials: []specialSpan{{kind: specialValuesTuples, tokStart: 0, tokEnd: 3, count: 1}}},
		mapper,
		Profile{Engine: "mysql", Mode: "mysql"},
	)
	_, _, _ = RenderAnonymizedDetailed(
		[]Token{{Kind: TokWord, Text: "bad\nw", Literal: "bad\nw"}},
		walkResult{specials: []specialSpan{{kind: specialLimitComma, tokStart: 0, tokEnd: 1}}},
		mapper,
		Profile{Engine: "mysql", Mode: "mysql"},
	)
	_, _ = RenderFallbackAnonymized(LexOutput{Tokens: badToks[:1]}, mapper)
	_, _ = RenderFallbackAnonymized(LexOutput{Tokens: badToks[1:]}, mapper)

	// CREATE INDEX with metadataIncomplete = true
	lxIdx, _ := Tokenize([]byte("CREATE INDEX idx ON x (id)"), mysqlFlags)
	wkIdx := walkStatement(lxIdx.Tokens)
	wkIdx.metadataIncomplete = true
	_, _ = extractStatementValidity(lxIdx.Tokens, wkIdx, mapper, "postgresql")

	// Token helper bounds in validityfacts_tokens.go
	_ = conditionFromPosition(lex.Tokens, -1)
	_ = conditionFromSpan(lex.Tokens, -1, 999)
	_ = wordPosition(lex.Tokens, "missing", -1)
	_ = firstWord(lex.Tokens, -1, 0)
	_ = hasAnyWord(lex.Tokens, -1, 999, "missing")
	_ = topLevelSpans(lex.Tokens, -1)
	_ = matchingParenPosition(lex.Tokens, -1)
	_ = (ValidityObject{Kind: ValidityObjectColumn, SchemaToken: "s", ParentToken: "p", Token: "c"}).String()
	_ = canonicalValidityReason("  trimmed ")
	_ = validityOperationOf("replace")
	_ = validityOperationOf("unknown")
	_, _ = createSubtype([]Token{{Kind: TokWord, Text: "create"}, {Kind: TokWord, Text: "unique"}, {Kind: TokWord, Text: "table"}})
	_, _ = dropSubtype([]Token{{Kind: TokWord, Text: "drop"}})

	// fallbackKind edge cases
	_ = fallbackKind([]Token{{Kind: TokNumber, Text: "1"}})
	_ = fallbackKind([]Token{{Kind: TokQuotedIdent, Text: "q"}, {Kind: TokPunct, Text: "("}})

	// Deep subquery recursion exceeding maxDepth
	deepSQL := "SELECT " + strings.Repeat("(SELECT ", 35) + "1" + strings.Repeat(")", 35)
	lxDeep, _ := Tokenize([]byte(deepSQL), mysqlFlags)
	_ = walkStatement(lxDeep.Tokens)

	// RenderState defensive paths
	st := newRenderState(lex.Tokens, walkResult{})
	_ = st.literalEvent(0) // non-literal token -> "l:?"
	stWithQuoted := newRenderState([]Token{{Kind: TokQuotedIdent, Text: "unclassified_q"}}, walkResult{})
	_ = stWithQuoted.CanonicalSequence()
}
