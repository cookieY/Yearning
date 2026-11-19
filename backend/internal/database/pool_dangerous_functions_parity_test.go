// pool_dangerous_functions_parity_test.go Allowlist/denylist parity gate: cursorDangerousFunctions
// in pool.go of this package (adapter-layer second line of defense) and dangerousFunctions in
// query/domain/safety.go (strong classifier) must be set-equal (after lowercase normalization).
// Historically the two lists drifted unilaterally (6 PG directory/replication functions added on
// the domain side were not synced to the adapter layer); this test ensures any unilateral addition
// or removal fails immediately at test time.
//
// Without an import cycle (query/domain does not depend on database), this test imports query/domain
// directly; because the domain list is unexported, the test anchors its real list behaviorally:
// for every function name in the union, domain.CheckSingleSelect must reject it (proving mirror <=
// real domain list), while benign functions must pass (proving the probe methodology works).
package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	domain "github.com/cookieY/YearningX/backend/internal/query/domain"
)

// domainDangerousFunctions is a mirror copy of dangerousFunctions in query/domain/safety.go
// (which is unexported). Modifying either list requires updating both this mirror and
// cursorDangerousFunctions in pool.go; behavioral anchoring assertions catch any item missing
// from this mirror relative to the real domain list.
var domainDangerousFunctions = map[string]bool{
	// PostgreSQL: file/directory, large objects, process, advisory locks, sequences, session config, replication.
	"dblink": true, "pg_read_file": true, "pg_read_binary_file": true,
	"pg_ls_dir": true, "pg_stat_file": true, "pg_ls_logdir": true,
	"pg_ls_waldir": true, "pg_ls_archive_statusdir": true,
	"lo_import": true, "lo_export": true,
	"pg_terminate_backend": true, "pg_cancel_backend": true,
	"pg_reload_conf": true, "pg_switch_wal": true, "pg_rotate_logfile": true,
	"pg_advisory_lock": true, "pg_advisory_xact_lock": true,
	"pg_advisory_unlock": true, "pg_try_advisory_lock": true,
	"pg_try_advisory_xact_lock": true,
	"setval":                    true, "nextval": true, "set_config": true,
	"pg_notify": true, "pg_logical_emit_message": true,
	"pg_replication_origin_advance": true,
	"pg_sleep":                      true, "pg_sleep_for": true, "pg_sleep_until": true,
	// MySQL: file read, locks, sleep, benchmark, counters.
	"load_file": true, "get_lock": true, "release_lock": true,
	"is_free_lock": true, "is_used_lock": true,
	"benchmark": true, "sleep": true, "uuid_short": true,
	"master_pos_wait": true, "source_pos_wait": true,
	"wait_for_executed_gtid_set": true,
	"sys_exec":                   true, "sys_eval": true,
	// Common to both.
	"currval": true,
}

// TestCursorDangerousFunctionsParity Set parity check (case-normalized):
// failure messages list unilateral diffs pointing to the side that needs synchronization.
func TestCursorDangerousFunctionsParity(t *testing.T) {
	a := normalizeLower(cursorDangerousFunctions)
	b := normalizeLower(domainDangerousFunctions)
	var onlyAdapter, onlyCopy []string
	for k := range a {
		if !b[k] {
			onlyAdapter = append(onlyAdapter, k)
		}
	}
	for k := range b {
		if !a[k] {
			onlyCopy = append(onlyCopy, k)
		}
	}
	if len(onlyAdapter) > 0 || len(onlyCopy) > 0 {
		sort.Strings(onlyAdapter)
		sort.Strings(onlyCopy)
		t.Fatalf("dangerous functions list drift: only in database/pool.go cursorDangerousFunctions=%v; "+
			"only in query/domain/safety.go dangerousFunctions mirror=%v "+
			"(both sets must be equal; see header note in pool_dangerous_functions_parity_test.go)",
			onlyAdapter, onlyCopy)
	}
	if len(a) == 0 {
		t.Fatal("list is empty: parity check is meaningless (accidental clearing must also fail)")
	}
}

// TestDomainClassifierRejectsAdapterList Behavioral anchoring: for every function name in the union
// of the mirror and adapter lists, the strong classifier domain.CheckSingleSelect must reject it — proving
// mirror subset <= real domain list (any missing item in the mirror fails); benign functions must pass,
// proving the probe SQL shape is valid.
func TestDomainClassifierRejectsAdapterList(t *testing.T) {
	for name := range domainDangerousFunctions {
		for _, postgres := range []bool{true, false} {
			probe := fmt.Sprintf("SELECT %s(1) FROM t", name)
			if err := domain.CheckSingleSelect(probe, postgres); err == nil {
				t.Errorf("domain.CheckSingleSelect must reject %q (%s is in the mirror list but passed the strong classifier — "+
					"check query/domain/safety.go and this test mirror)", probe, name)
			}
		}
	}
	for _, benign := range []string{"coalesce", "count", "lower", "now"} {
		probe := fmt.Sprintf("SELECT %s(1) FROM t", benign)
		if err := domain.CheckSingleSelect(probe, false); err != nil {
			t.Errorf("benign function %s should not be rejected by strong classifier (probe methodology broken): %v", benign, err)
		}
	}
}

func normalizeLower(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if v {
			out[strings.ToLower(k)] = true
		}
	}
	return out
}
