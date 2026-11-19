// safety_exec_comment_test.go NEW-QUERY-01 two-layer classifier adversarial verification:
// MySQL/MariaDB versionless executable comments `/*!` and MariaDB `/*M!` have their bodies executed by
// the server -- dangerous payloads such as sleep/benchmark/load_file/INTO OUTFILE hiding inside executable
// comment bodies must be rejected; versioned `/*!50000` serves as the existing regression anchor;
// ordinary comments and optimizer hints remain allowed; PG `/*!` is an ordinary comment and remains allowed.
package domain

import "testing"

func TestCheckSingleSelectExecutableCommentBypass(t *testing.T) {
	bad := []string{
		"select 1 /*! , sleep(10) */",
		"select 1 /*! , benchmark(100000000, md5('x')) */",
		"select 1 /*! , load_file('/etc/passwd') */",
		"select 1 /*! into outfile '/tmp/x' */",
		"select 1 /*M! , sleep(10) */",
		"select 1 /*M!100100 , sleep(10) */",
		"select 1 /*!50000 , sleep(10) */",
		"select 1 /*!50000 into outfile '/tmp/x' */",
	}
	for _, sql := range bad {
		if err := CheckSingleSelect(sql, false); err == nil {
			t.Errorf("CheckSingleSelect(%q) accepted; want reject", sql)
		}
	}
	ok := []struct {
		sql      string
		postgres bool
	}{
		{"select 1 /* ordinary */", false},
		{"select 1 /*+ MAX_EXECUTION_TIME(1000) */", false},
		// PostgreSQL family: `/*!` is an ordinary comment not executed by the server, so its body is not scanned.
		{"select 1 /*! , pg_sleep(10) */", true},
		{"select 1 /*!50000 , pg_sleep(10) */", true},
	}
	for _, c := range ok {
		if err := CheckSingleSelect(c.sql, c.postgres); err != nil {
			t.Errorf("CheckSingleSelect(%q, postgres=%v) = %v; want accept", c.sql, c.postgres, err)
		}
	}
}

// TestCheckSingleSelectExecutableCommentQuoteDesync NEW-QUERY-05 regression:
// `*/` inside a quoted literal within an executable comment body must not trick the classifier into scanning only
// the prefix; sleep/INTO OUTFILE later in the comment body must be rejected, and unclosed literals must also fail closed.
func TestCheckSingleSelectExecutableCommentQuoteDesync(t *testing.T) {
	bad := []string{
		"select 1 /*! , 'a*/', sleep(0.2), 1 -- '\n*/",
		"select 1 /*! , 'a*/' into outfile '/tmp/r2x' -- '\n*/",
		"select 1 /*! , 'a*/",
		`select 1 /*M! , 'a\', benchmark(1, md5('x')) */`,
	}
	for _, sql := range bad {
		if err := CheckSingleSelect(sql, false); err == nil {
			t.Errorf("CheckSingleSelect(%q) accepted; want reject", sql)
		}
	}
	if err := CheckSingleSelect("select 1 /*! , 'a*/' */", false); err != nil {
		t.Errorf("benign quoted */ inside executable comment must stay accepted: %v", err)
	}
	// With sql_mode pinned by the connection contract, backslash-escaped quotes have a single deterministic meaning:
	// sleep(0.2) remains inside the string literal and must not be expanded into an executable token.
	if err := CheckSingleSelect(`select 1 /*! , 'a\', sleep(0.2), 'b*/`, false); err != nil {
		t.Errorf("escaped-quote literal under deterministic sql_mode must stay accepted: %v", err)
	}
}
