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
		"SELECT count, lower FROM private_orders")
	assertAnonymizedDoesNotContain(t, res, "count", "lower", "private_orders")
	if !res.MetadataContextComplete {
		t.Fatalf("ordinary column references must retain complete context: %s", res.AnonymizedSQL)
	}
}
