// plan_test.go unit tests for gh-ost plans: argv surface (required present, forbidden absent,
// unknown rejected, injection metacharacters rejected), deterministic ALTER parsing, and stable plan hash.
package osc

import (
	"strings"
	"testing"
)

func baseInput() PlanInput {
	return PlanInput{
		Database: "shop", Table: "orders", Alter: "ADD COLUMN c1 int",
		Host: "db.internal", Port: 3306, User: "exec_user",
	}
}

func TestBuildArgvRequiredFlags(t *testing.T) {
	argv, files, err := BuildArgv(baseInput(), "/tmp/att")
	if err != nil {
		t.Fatalf("build argv: %v", err)
	}
	if err := ValidateArgv(argv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--database=shop", "--table=orders", "--alter=ADD COLUMN c1 int",
		"--host=db.internal", "--port=3306", "--user=exec_user", "--ask-pass",
		"--assume-rbr", "--panic-flag-file=/tmp/att/panic-flag",
		"--postpone-cut-over-flag-file=/tmp/att/postpone-cutover",
		"--serve-socket-file=/tmp/att/serve.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %s: %v", want, argv)
		}
	}
	if files.PanicFlag == "" || files.PostponeCutover == "" || files.ServeSocket == "" {
		t.Fatal("control file paths must be inside attempt dir")
	}
}

func TestArgvRejectsForbiddenAndUnknown(t *testing.T) {
	for _, flag := range []string{
		"--password=sekret", "--allow-on-master", "--skip-foreign-key-checks",
		"--allow-nullable-unique-key", "--approve-renamed-columns",
		"--initially-drop-ghost-table", "--initially-drop-old-table",
	} {
		argv := []string{"--database=d", "--table=t", "--alter=a", "--host=h", "--port=1",
			"--user=u", "--ask-pass", "--assume-rbr", "--panic-flag-file=/p",
			"--postpone-cut-over-flag-file=/q", "--serve-socket-file=/s", flag}
		if err := ValidateArgv(argv); err == nil {
			t.Fatalf("forbidden flag %s must be rejected", flag)
		}
	}
	if err := ValidateArgv([]string{"--database=d", "--totally-unknown=1"}); err == nil {
		t.Fatal("unknown flag must be rejected")
	}
	if err := ValidateArgv([]string{"--database=d"}); err == nil {
		t.Fatal("missing required flags must be rejected")
	}
}

func TestBuildArgvRejectsInjection(t *testing.T) {
	cases := []PlanInput{
		func() PlanInput { in := baseInput(); in.Database = "shop; rm -rf"; return in }(),
		func() PlanInput { in := baseInput(); in.Table = "orders --password=x"; return in }(),
		func() PlanInput { in := baseInput(); in.Alter = "ADD COLUMN a int; DROP TABLE x"; return in }(),
		func() PlanInput { in := baseInput(); in.Host = ""; return in }(),
		func() PlanInput { in := baseInput(); in.Port = 0; return in }(),
		func() PlanInput { in := baseInput(); in.User = ""; return in }(),
	}
	for i, in := range cases {
		if _, _, err := BuildArgv(in, "/tmp/att"); err == nil {
			t.Fatalf("case %d must fail closed", i)
		}
	}
}

func TestParseAlterTable(t *testing.T) {
	cases := []struct {
		stmt string
		db   string
		tbl  string
		alt  string
	}{
		{"ALTER TABLE `shop`.`orders` ADD COLUMN c1 int", "shop", "orders", "ADD COLUMN c1 int"},
		{"ALTER TABLE shop.orders ADD INDEX idx_a (a)", "shop", "orders", "ADD INDEX idx_a (a)"},
		{"alter table orders drop column c2", "shop", "orders", "drop column c2"},
		{"ALTER TABLE `orders` ADD COLUMN `x` int;", "shop", "orders", "ADD COLUMN `x` int"},
		{"(ALTER TABLE shop.orders ENGINE=InnoDB)", "shop", "orders", "ENGINE=InnoDB"},
	}
	for _, c := range cases {
		db, tbl, alt, err := ParseAlterTable(c.stmt, "shop")
		if err != nil {
			t.Fatalf("parse %q: %v", c.stmt, err)
		}
		if db != c.db || tbl != c.tbl || alt != c.alt {
			t.Fatalf("parse %q = (%s,%s,%s) want (%s,%s,%s)", c.stmt, db, tbl, alt, c.db, c.tbl, c.alt)
		}
	}
	for _, bad := range []string{
		"UPDATE orders SET a=1", "ALTER TABLE shop.orders; DROP TABLE x",
		"ALTER TABLE", "ALTER TABLE `unterminated.orders ADD c int", "",
	} {
		if _, _, _, err := ParseAlterTable(bad, "shop"); err == nil {
			t.Fatalf("must reject %q", bad)
		}
	}
	// Missing default database when table is unqualified -> fail closed.
	if _, _, _, err := ParseAlterTable("ALTER TABLE orders ADD c int", ""); err == nil {
		t.Fatal("missing default database must fail closed")
	}
}

func TestPlanHashStable(t *testing.T) {
	argv, _, err := BuildArgv(baseInput(), "/tmp/att")
	if err != nil {
		t.Fatal(err)
	}
	h1 := PlanHash(argv, "/tmp/att")
	h2 := PlanHash(argv, "/tmp/att")
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("plan hash unstable: %s vs %s", h1, h2)
	}
	argv2, _, _ := BuildArgv(baseInput(), "/tmp/att2")
	if PlanHash(argv2, "/tmp/att") == h1 {
		t.Fatal("dir must participate in plan hash")
	}
}

func TestBuildPtOscArgv(t *testing.T) {
	chunk := 500
	lag := 30
	argv, err := BuildPtOscArgv(baseInput(), &Overrides{ChunkSize: &chunk, MaxLag: &lag})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePtOscArgv(argv); err != nil {
		t.Fatalf("validate: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--alter=ADD COLUMN c1 int", "--execute", "--ask-pass",
		"h=db.internal,P=3306,u=exec_user,D=shop,t=orders",
		"--chunk-size=500", "--max-lag=30",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("pt-osc argv missing %s: %v", want, argv)
		}
	}
	for _, forbidden := range []string{"--password", "--set-vars", "--recursion-method"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("pt-osc argv must not contain %s", forbidden)
		}
	}
}

func TestPtOscArgvRejectsMalformed(t *testing.T) {
	if _, err := BuildPtOscArgv(PlanInput{
		Database: "shop; rm", Table: "orders", Alter: "ADD c int",
		Host: "h", Port: 3306, User: "u",
	}, &Overrides{}); err == nil {
		t.Fatal("injection must fail closed")
	}
	if _, err := BuildPtOscArgv(baseInput(), &Overrides{}); err != nil {
		t.Fatalf("plain build: %v", err)
	}
}

func TestParseOverridesRejectsUnknownAndInjection(t *testing.T) {
	for _, bad := range []map[string]any{
		{"alter": "DROP TABLE x"}, {"password": "x"}, {"host": "evil"},
		{"max_load": "Threads_running=50; rm -rf /"},
		{"chunk_size": float64(-1)}, {"retries": float64(121)},
	} {
		if _, err := ParseOverrides(bad); err == nil {
			t.Fatalf("must reject %v", bad)
		}
	}
}
