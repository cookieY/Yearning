package fingerprint

import (
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func analyzePrivacySQL(t *testing.T, profile Profile, sql string) Result {
	t.Helper()
	res, err := testEngine(t).Analyze(profile, []byte(sql), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("analyze %q: %v", sql, err)
	}
	return res
}

func assertAnonymizedDoesNotContain(t *testing.T, res Result, banned ...string) {
	t.Helper()
	identifiers := strings.FieldsFunc(res.AnonymizedSQL, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	})
	visible := make(map[string]bool, len(identifiers))
	for _, identifier := range identifiers {
		visible[strings.ToLower(identifier)] = true
	}
	for _, value := range banned {
		if visible[strings.ToLower(value)] {
			t.Fatalf("anonymized SQL leaked %q: %s", value, res.AnonymizedSQL)
		}
	}
}

func TestModelVisibleSQLAnonymizesUnknownFunctions(t *testing.T) {
	mysql := Profile{Engine: "mysql", Mode: "mysql"}
	postgres := Profile{Engine: "postgresql", Mode: "postgresql"}
	cases := []struct {
		name                  string
		profile               Profile
		sql                   string
		banned                []string
		wantContextIncomplete bool
	}{
		{
			name: "unquoted UDF", profile: mysql,
			sql:                   "SELECT private_customer_func(id) FROM orders",
			banned:                []string{"private_customer_func", "orders", "id"},
			wantContextIncomplete: true,
		},
		{
			name: "quoted UDF", profile: postgres,
			sql:                   `SELECT "private_customer_func"(id) FROM orders`,
			banned:                []string{"private_customer_func", "orders", "id"},
			wantContextIncomplete: true,
		},
		{
			name: "schema-qualified UDF", profile: postgres,
			sql:                   "SELECT private_schema.private_customer_func(id) FROM orders",
			banned:                []string{"private_schema", "private_customer_func", "orders", "id"},
			wantContextIncomplete: true,
		},
		{
			name: "fallback UDF", profile: mysql,
			sql: "CREATE TRIGGER trigger_secret AFTER INSERT ON private_orders FOR EACH ROW " +
				"SET NEW.total = private_customer_func(NEW.qty)",
			banned: []string{
				"trigger_secret", "private_orders", "total", "private_customer_func", "qty",
			},
			wantContextIncomplete: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := analyzePrivacySQL(t, tc.profile, tc.sql)
			assertAnonymizedDoesNotContain(t, res, tc.banned...)
			if res.MetadataContextComplete != !tc.wantContextIncomplete {
				t.Fatalf("metadata context complete = %v, SQL=%s",
					res.MetadataContextComplete, res.AnonymizedSQL)
			}
		})
	}
}

func TestModelVisibleSQLAnonymizesCustomTypesAndDDLOptions(t *testing.T) {
	mysql := Profile{Engine: "mysql", Mode: "mysql"}
	postgres := Profile{Engine: "postgresql", Mode: "postgresql"}
	cases := []struct {
		name                  string
		profile               Profile
		sql                   string
		banned                []string
		wantContextIncomplete bool
	}{
		{
			name: "custom DDL type with modifier", profile: postgres,
			sql:                   "CREATE TABLE things (status private_status(10) NOT NULL)",
			banned:                []string{"things", "status", "private_status"},
			wantContextIncomplete: true,
		},
		{
			name: "custom cast type", profile: postgres,
			sql:                   "SELECT payload::private_status FROM documents",
			banned:                []string{"payload", "private_status", "documents"},
			wantContextIncomplete: true,
		},
		{
			name: "qualified custom cast type", profile: postgres,
			sql:                   "SELECT payload::private_schema.private_status FROM documents",
			banned:                []string{"payload", "private_schema", "private_status", "documents"},
			wantContextIncomplete: true,
		},
		{
			name: "custom cast type in CAST", profile: postgres,
			sql:                   "SELECT CAST(payload AS private_status) FROM documents",
			banned:                []string{"payload", "private_status", "documents"},
			wantContextIncomplete: true,
		},
		{
			name: "unknown storage engine", profile: mysql,
			sql:                   "CREATE TABLE things (id int) ENGINE=private_storage",
			banned:                []string{"things", "id", "private_storage"},
			wantContextIncomplete: true,
		},
		{
			name: "custom collation", profile: mysql,
			sql:                   "ALTER TABLE things ADD COLUMN name varchar(20) COLLATE private_collation",
			banned:                []string{"things", "name", "private_collation"},
			wantContextIncomplete: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := analyzePrivacySQL(t, tc.profile, tc.sql)
			assertAnonymizedDoesNotContain(t, res, tc.banned...)
			if res.MetadataContextComplete != !tc.wantContextIncomplete {
				t.Fatalf("metadata context complete = %v, SQL=%s",
					res.MetadataContextComplete, res.AnonymizedSQL)
			}
		})
	}
}

func TestModelVisibleSQLKeepsOnlyControlledBuiltinsAndMasksDDLLiterals(t *testing.T) {
	mysql := Profile{Engine: "mysql", Mode: "mysql"}
	postgres := Profile{Engine: "postgresql", Mode: "postgresql"}

	query := analyzePrivacySQL(t, postgres,
		"SELECT count(id), lower(trim(name)), amount::numeric(10,2) FROM private_orders")
	for _, builtin := range []string{"count", "lower", "trim", "numeric"} {
		if !strings.Contains(query.AnonymizedSQL, builtin) {
			t.Fatalf("controlled builtin %q missing: %s", builtin, query.AnonymizedSQL)
		}
	}
	assertAnonymizedDoesNotContain(t, query, "private_orders", "amount", "name")
	if !query.MetadataContextComplete {
		t.Fatalf("controlled builtins must retain complete context: %s", query.AnonymizedSQL)
	}

	ddl := analyzePrivacySQL(t, mysql,
		`CREATE TABLE private_orders (
			status enum('private_pending','private_done') DEFAULT 'private_default',
			CONSTRAINT private_check CHECK (status <> 'private_forbidden')
		)`)
	assertAnonymizedDoesNotContain(t, ddl,
		"private_orders", "status", "private_pending", "private_done",
		"private_default", "private_check", "private_forbidden")
	for _, marker := range []string{"enum", ":string"} {
		if !strings.Contains(ddl.AnonymizedSQL, marker) {
			t.Fatalf("typed DDL marker %q missing: %s", marker, ddl.AnonymizedSQL)
		}
	}
	if !ddl.MetadataContextComplete {
		t.Fatalf("controlled enum/check/default must retain complete context: %s", ddl.AnonymizedSQL)
	}
}

func TestModelVisibleSQLKeepsControlledDDLOptions(t *testing.T) {
	res := analyzePrivacySQL(t, Profile{Engine: "mysql", Mode: "mysql"},
		`CREATE TABLE private_orders (
			name varchar(20) COLLATE utf8mb4_bin
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	for _, controlled := range []string{"varchar", "utf8mb4_bin", "innodb", "utf8mb4"} {
		if !strings.Contains(res.AnonymizedSQL, controlled) {
			t.Fatalf("controlled DDL value %q missing: %s", controlled, res.AnonymizedSQL)
		}
	}
	assertAnonymizedDoesNotContain(t, res, "private_orders", "name")
	if !res.MetadataContextComplete {
		t.Fatalf("controlled DDL options must retain complete context: %s", res.AnonymizedSQL)
	}
}

func TestModelVisibleSQLDoesNotExposeBuiltinLookingColumns(t *testing.T) {
	res := analyzePrivacySQL(t, Profile{Engine: "postgresql", Mode: "postgresql"},
		"SELECT count, lower, auto_increment FROM private_orders")
	assertAnonymizedDoesNotContain(t, res, "count", "lower", "auto_increment", "private_orders")
	if !res.MetadataContextComplete {
		t.Fatalf("ordinary column references must retain complete context: %s", res.AnonymizedSQL)
	}
}

func TestModelVisibleSQLAllowsTableAutoIncrementOption(t *testing.T) {
	mysql := Profile{Engine: "mysql", Mode: "mysql"}

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{
			name: "equals_compact",
			sql: `CREATE TABLE t_idx_max (
				id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'row id',
				col_a VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'field a',
				PRIMARY KEY (id)
			) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='test tbl'`,
		},
		{
			name: "equals_spaced",
			sql:  `CREATE TABLE t_idx_max (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, PRIMARY KEY (id)) ENGINE=InnoDB AUTO_INCREMENT = 100 DEFAULT CHARSET=utf8mb4`,
		},
		{
			name: "no_equals",
			sql:  `CREATE TABLE t_idx_max (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, PRIMARY KEY (id)) ENGINE=InnoDB AUTO_INCREMENT 1 DEFAULT CHARSET=utf8mb4`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := analyzePrivacySQL(t, mysql, tc.sql)
			if res.Quality != QualityExact {
				t.Fatalf("Quality = %q, want %q", res.Quality, QualityExact)
			}
			if !res.MetadataContextComplete {
				t.Fatalf("AUTO_INCREMENT table option must keep MetadataContextComplete=true: %s", res.AnonymizedSQL)
			}
			assertAnonymizedDoesNotContain(t, res, "t_idx_max", "col_a", "row id", "test tbl")
			for _, want := range []string{"innodb", "utf8mb4", ":int"} {
				if !strings.Contains(res.AnonymizedSQL, want) {
					t.Fatalf("expected %q in anonymized SQL: %s", want, res.AnonymizedSQL)
				}
			}
		})
	}

	// Non-numeric AUTO_INCREMENT value must still fail closed on metadata completeness and anonymize the unknown token.
	invalid := analyzePrivacySQL(t, mysql,
		`CREATE TABLE t_idx_max (id BIGINT PRIMARY KEY) ENGINE=InnoDB AUTO_INCREMENT=secret_counter`)
	if invalid.MetadataContextComplete {
		t.Fatalf("non-numeric AUTO_INCREMENT option must mark MetadataContextComplete=false: %s", invalid.AnonymizedSQL)
	}
	assertAnonymizedDoesNotContain(t, invalid, "t_idx_max", "secret_counter")
}

func TestModelVisibleSQLAllowsCommonDDLOptionsAndPGConstructs(t *testing.T) {
	mysql := Profile{Engine: "mysql", Mode: "mysql"}
	pg := Profile{Engine: "postgresql", Mode: "postgresql"}

	cases := []struct {
		name    string
		profile Profile
		sql     string
		forbid  []string
	}{
		{
			name:    "mysql_table_options_row_format_and_stats",
			profile: mysql,
			sql: `CREATE TABLE orders_v2 (
				id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'pk',
				code VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'order code',
				shadow_flag TINYINT NOT NULL DEFAULT 0 INVISIBLE,
				PRIMARY KEY (id),
				KEY idx_code (code) COMMENT 'lookup by code' VISIBLE,
				KEY idx_shadow (shadow_flag) INVISIBLE
			) ENGINE=InnoDB AUTO_INCREMENT=1 ROW_FORMAT=DYNAMIC KEY_BLOCK_SIZE=8 STATS_PERSISTENT=1 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='orders'`,
			forbid: []string{"orders_v2", "code", "shadow_flag", "idx_code", "idx_shadow", "lookup by code"},
		},
		{
			name:    "mysql_alter_algorithm_lock_and_default_after",
			profile: mysql,
			sql:     `ALTER TABLE orders_v2 ADD COLUMN memo VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'memo' AFTER code, COMMENT='updated', ALGORITHM=INPLACE, LOCK=NONE`,
			forbid:  []string{"orders_v2", "memo", "code", "updated"},
		},
		{
			name:    "mysql_alter_engine_and_auto_increment",
			profile: mysql,
			sql:     `ALTER TABLE orders_v2 ENGINE=InnoDB, AUTO_INCREMENT=500, ROW_FORMAT=COMPACT`,
			forbid:  []string{"orders_v2", "500"},
		},
		{
			name:    "pg_generated_by_default_and_builtin_type_aliases",
			profile: pg,
			sql: `CREATE TABLE audit_events (
				id INT8 GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				tenant_id INT4 NOT NULL,
				client_ip INET NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			forbid: []string{"audit_events", "tenant_id", "client_ip", "created_at"},
		},
		{
			name:    "mysql_alter_whitespace_separated_table_options",
			profile: mysql,
			sql:     `ALTER TABLE orders_v2 ENGINE=InnoDB AUTO_INCREMENT=500 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			forbid:  []string{"orders_v2", "500"},
		},
		{
			name:    "mysql_alter_default_charset_collate_whitespace",
			profile: mysql,
			sql:     `ALTER TABLE orders_v2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			forbid:  []string{"orders_v2"},
		},
		{
			name:    "mysql_alter_default_none_with_lock_none_and_algorithm_column",
			profile: mysql,
			sql:     `ALTER TABLE jobs ADD COLUMN algorithm VARCHAR(32) NOT NULL DEFAULT 'none', ALGORITHM=INPLACE, LOCK=NONE`,
			forbid:  []string{"jobs", "algorithm", "none"},
		},
		{
			name:    "mysql_create_default_dynamic_with_row_format_dynamic",
			profile: mysql,
			sql:     `CREATE TABLE mode_tbl (id BIGINT PRIMARY KEY, mode VARCHAR(16) NOT NULL DEFAULT 'dynamic') ENGINE=InnoDB ROW_FORMAT=DYNAMIC`,
			forbid:  []string{"mode_tbl", "mode", "dynamic"},
		},
		{
			name:    "pg_identity_column_with_generated_as_identity",
			profile: pg,
			sql:     `CREATE TABLE accounts (id INT8 GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, identity VARCHAR(64) NOT NULL)`,
			forbid:  []string{"accounts", "identity"},
		},
		{
			name:    "pg_alter_generated_identity_and_drop_identity",
			profile: pg,
			sql:     `ALTER TABLE accounts ADD COLUMN new_id INT8 GENERATED BY DEFAULT AS IDENTITY, ALTER COLUMN old_id DROP IDENTITY`,
			forbid:  []string{"accounts", "new_id", "old_id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := analyzePrivacySQL(t, tc.profile, tc.sql)
			if res.Quality != QualityExact {
				t.Fatalf("Quality = %q, want %q (sql=%s)", res.Quality, QualityExact, res.AnonymizedSQL)
			}
			if !res.MetadataContextComplete {
				t.Fatalf("expected MetadataContextComplete=true, got false: %s", res.AnonymizedSQL)
			}
			assertAnonymizedDoesNotContain(t, res, tc.forbid...)
		})
	}

	// Privacy guard: option keywords used as SELECT column names must stay anonymized.
	sel := analyzePrivacySQL(t, mysql, `SELECT row_format, algorithm, lock, key_block_size, timestamptz, inet FROM secret_tbl`)
	assertAnonymizedDoesNotContain(t, sel, "row_format", "algorithm", "lock", "key_block_size", "timestamptz", "inet", "secret_tbl")

	// Unknown ROW_FORMAT value must fail closed on metadata completeness and never leak,
	// even when repeated after a valid ROW_FORMAT option in the same statement.
	for _, sql := range []string{
		`CREATE TABLE secret_tbl (id BIGINT PRIMARY KEY) ENGINE=InnoDB ROW_FORMAT=secret_fmt`,
		`CREATE TABLE secret_tbl (id BIGINT PRIMARY KEY) ENGINE=InnoDB ROW_FORMAT=DYNAMIC ROW_FORMAT=secret_fmt`,
	} {
		bad := analyzePrivacySQL(t, mysql, sql)
		if bad.MetadataContextComplete {
			t.Fatalf("unknown ROW_FORMAT must mark MetadataContextComplete=false: %s", bad.AnonymizedSQL)
		}
		assertAnonymizedDoesNotContain(t, bad, "secret_tbl", "secret_fmt")
	}
}
