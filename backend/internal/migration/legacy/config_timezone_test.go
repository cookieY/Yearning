package legacy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLoadConfigPreservesSourceTimezone(t *testing.T) {
	for _, tc := range []struct{ zone, wantUTC string }{
		{"Asia/Shanghai", "2026-01-15T00:30:00Z"},
		{"UTC", "2026-01-15T08:30:00Z"},
		{"America/New_York", "2026-01-15T13:30:00Z"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			cfg, err := LoadConfig(writeTimezoneConfig(t, tc.zone))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SourceTimezone != tc.zone {
				t.Errorf("source timezone = %q, want %q", cfg.SourceTimezone, tc.zone)
			}
			// Exercise the same timezone handoff used by Runner.runPass. A validated
			// but discarded zone silently treats old local timestamps as UTC.
			transform := &TransformContext{Timezone: mustLocation(cfg.SourceTimezone)}
			stamp, err := transform.parseLegacyTime("2026-01-15 08:30:00", "created_at")
			if err != nil {
				t.Fatal(err)
			}
			if stamp.Format(time.RFC3339) != tc.wantUTC {
				t.Errorf("migrated timestamp = %s, want %s", stamp.Format(time.RFC3339), tc.wantUTC)
			}
		})
	}
}

// TestParseLegacyTimeDriverForm covers the shape the MySQL driver actually
// hands to the transformer. go-sql-driver with parseTime=true and no loc
// parameter returns DATETIME as a time.Time already stamped UTC whose wall
// clock is the legacy local time; the zone tag is therefore meaningless and
// must not be trusted. This is the branch production uses — the string branch
// above it never sees a time.Time.
func TestParseLegacyTimeDriverForm(t *testing.T) {
	for _, tc := range []struct {
		zone    string
		driver  time.Time
		wantUTC string
	}{
		// Driver parsed "2026-01-15 08:30:00" as UTC; real zone is Shanghai.
		{"Asia/Shanghai", time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC), "2026-01-15T00:30:00Z"},
		// Zone is UTC, so the wall clock is already the UTC instant.
		{"UTC", time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC), "2026-01-15T08:30:00Z"},
		// Zone is New York (UTC-5 in January).
		{"America/New_York", time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC), "2026-01-15T13:30:00Z"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			transform := &TransformContext{Timezone: mustLocation(tc.zone)}
			stamp, err := transform.parseLegacyTime(tc.driver, "created_at")
			if err != nil {
				t.Fatal(err)
			}
			if got := stamp.Format(time.RFC3339); got != tc.wantUTC {
				t.Errorf("migrated timestamp = %s, want %s", got, tc.wantUTC)
			}
		})
	}
}

// TestParseLegacyTimeZeroValueIsEmpty: the driver turns '0000-00-00 00:00:00'
// into time.Time{}, which must be rejected exactly like the string branch
// rejects it. Otherwise callers' fallbacks never fire and a year-0000 instant
// is written into terminal_at/updated_at/started_at.
func TestParseLegacyTimeZeroValueIsEmpty(t *testing.T) {
	transform := &TransformContext{Timezone: mustLocation("Asia/Shanghai")}
	if _, err := transform.parseLegacyTime(time.Time{}, "execute_time"); err == nil {
		t.Fatal("zero time.Time accepted; want empty-value error")
	}
	// The string form of the same value must fail the same way.
	if _, err := transform.parseLegacyTime("0000-00-00 00:00:00", "execute_time"); err == nil {
		t.Fatal("zero string accepted; want empty-value error")
	}
}

// TestTerminalTimeFallsBackOnZeroValue proves the caller-level consequence:
// a zero execute_time must fall back to date rather than yield a year-0000 time.
func TestTerminalTimeFallsBackOnZeroValue(t *testing.T) {
	transform := &TransformContext{Timezone: mustLocation("Asia/Shanghai")}
	row := Row{
		"execute_time": time.Time{},
		"date":         "2026-01-15 08:30:00",
	}
	got := terminalTime(transform, row)
	if got == nil {
		t.Fatal("terminalTime returned nil")
	}
	if got.Year() < 2000 {
		t.Fatalf("terminalTime = %s, want the date fallback", got.Format(time.RFC3339))
	}
	if want := "2026-01-15T00:30:00Z"; got.UTC().Format(time.RFC3339) != want {
		t.Errorf("terminalTime = %s, want %s", got.UTC().Format(time.RFC3339), want)
	}
}

// TestParseLegacyTimeNilTimezoneFailsClosed: a nil location must not silently
// reproduce the corruption the fix removes. Both branches must agree.
func TestParseLegacyTimeNilTimezoneFailsClosed(t *testing.T) {
	transform := &TransformContext{}
	if _, err := transform.parseLegacyTime(time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC), "created_at"); err == nil {
		t.Fatal("nil timezone accepted on the time.Time branch; want an error")
	}
	if _, err := transform.parseLegacyTime("2026-01-15 08:30:00", "created_at"); err == nil {
		t.Fatal("nil timezone accepted on the string branch; want an error")
	}
}

// TestParseLegacyTimeRejectsNonUTCDriverZone: if a DSN ever gains loc=, the
// driver's zone tag becomes meaningful and reinterpreting the wall clock would
// shift by the offset difference. That must fail closed, not silently shift.
func TestParseLegacyTimeRejectsNonUTCDriverZone(t *testing.T) {
	shanghai := mustLocation("Asia/Shanghai")
	transform := &TransformContext{Timezone: mustLocation("UTC")}
	driverValue := time.Date(2026, 1, 15, 8, 30, 0, 0, shanghai)
	if _, err := transform.parseLegacyTime(driverValue, "created_at"); err == nil {
		t.Fatal("non-UTC driver zone accepted; want a refusal")
	}
}

// TestParseLegacyTimeExplicitOffsetHonoursOffset: an RFC3339 string carries its
// own offset, so source_timezone must not be applied on top of it.
func TestParseLegacyTimeExplicitOffsetHonoursOffset(t *testing.T) {
	transform := &TransformContext{Timezone: mustLocation("Asia/Shanghai")}
	stamp, err := transform.parseLegacyTime("2026-01-15T08:30:00+02:00", "created_at")
	if err != nil {
		t.Fatal(err)
	}
	if want := "2026-01-15T06:30:00Z"; stamp.Format(time.RFC3339) != want {
		t.Errorf("migrated timestamp = %s, want %s", stamp.Format(time.RFC3339), want)
	}
}

// TestParseLegacyTimeDSTAmbiguity pins the disambiguation rule so a future
// refactor cannot move instants silently. time.Date and ParseInLocation agree
// on both the ambiguous fall-back hour and the non-existent spring-forward gap.
func TestParseLegacyTimeDSTAmbiguity(t *testing.T) {
	transform := &TransformContext{Timezone: mustLocation("America/New_York")}
	for _, tc := range []struct{ wall, wantUTC string }{
		// Ambiguous: 01:30 occurs twice; Go picks the first (DST) offset.
		{"2026-11-01 01:30:00", "2026-11-01T05:30:00Z"},
		// Non-existent: 02:30 is skipped; Go normalizes forward.
		{"2026-03-08 02:30:00", "2026-03-08T06:30:00Z"},
	} {
		stamp, err := transform.parseLegacyTime(tc.wall, "created_at")
		if err != nil {
			t.Fatalf("%s: %v", tc.wall, err)
		}
		if got := stamp.Format(time.RFC3339); got != tc.wantUTC {
			t.Errorf("%s -> %s, want %s", tc.wall, got, tc.wantUTC)
		}
		// The driver form must agree with the string form for the same wall clock.
		wall, _ := time.Parse("2006-01-02 15:04:05", tc.wall)
		driverValue := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
		got, err := transform.parseLegacyTime(driverValue, "created_at")
		if err != nil {
			t.Fatalf("%s driver form: %v", tc.wall, err)
		}
		if got.Format(time.RFC3339) != tc.wantUTC {
			t.Errorf("%s driver form -> %s, want %s (branches disagree)", tc.wall, got.Format(time.RFC3339), tc.wantUTC)
		}
	}
}

func TestLoadConfigRejectsInvalidSourceTimezone(t *testing.T) {
	for _, zone := range []string{"", "Not/AZone"} {
		if _, err := LoadConfig(writeTimezoneConfig(t, zone)); err == nil {
			t.Errorf("invalid timezone %q accepted", zone)
		}
	}
}

func writeTimezoneConfig(t *testing.T, zone string) string {
	t.Helper()
	raw := fmt.Sprintf(`{
 "source":{"host":"mysql.internal","port":3306,"database":"legacy","username":"reader","password":{"environment":"SOURCE_PASSWORD"},"tls":{"mode":"verify_full","server_name":"mysql.internal"}},
 "target":{"host":"pg.internal","port":5432,"database":"v4","username":"writer","password":{"environment":"TARGET_PASSWORD"},"tls":{"mode":"verify_full","server_name":"pg.internal"}},
 "encryption":{"key_id":"test-key","key":{"environment":"PAYLOAD_KEY"},"deployment_id":"test"},
 "source_timezone":%q,"batch_rows":1000}`, zone)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDisplayNumberUsesSourceZoneDate pins the day boundary: a legacy wall clock
// of 2026-01-15 03:00 in Shanghai is 2026-01-14T19:00Z, but the legacy system
// showed 2026-01-15. The display number and budget day must keep the source-zone
// date, otherwise migration silently renames every early-morning order.
func TestDisplayNumberUsesSourceZoneDate(t *testing.T) {
	shanghai := mustLocation("Asia/Shanghai")
	transform := &TransformContext{Timezone: shanghai}
	driverValue := time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC)

	submitted, err := transform.parseLegacyTime(driverValue, "date")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := submitted.Format(time.RFC3339), "2026-01-14T19:00:00Z"; got != want {
		t.Fatalf("submittedAt = %s, want %s", got, want)
	}
	if got, want := legacyDisplayNumber("20260115000001", submitted, shanghai), "YR-20260115-000001"; got != want {
		t.Errorf("display_number = %s, want %s (source-zone date)", got, want)
	}
	if got, want := sourceWallClock(submitted, shanghai).Format("2006-01-02"), "2026-01-15"; got != want {
		t.Errorf("budget_day = %s, want %s (source-zone date)", got, want)
	}
	// A UTC source zone must keep the UTC date, so the rule is not "always +1 day".
	if got, want := legacyDisplayNumber("20260115000001", submitted, mustLocation("UTC")), "YR-20260114-000001"; got != want {
		t.Errorf("UTC display_number = %s, want %s", got, want)
	}
}

// TestTransformSQLOrderKeepsSourceZoneDate drives TransformSQLOrder end to end so
// the display_number / budget_day fix is covered on the real code path, not only
// through the legacyDisplayNumber helper. A legacy wall clock of 2026-01-15 03:00
// in Shanghai is 2026-01-14T19:00Z; the migrated row must still read YR-20260115.
func TestTransformSQLOrderKeepsSourceZoneDate(t *testing.T) {
	c, err := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	shanghai := mustLocation("Asia/Shanghai")
	tc := NewTransformContext(mustUUID("22222222-2222-2222-2222-222222222222"), c, shanghai)
	uid := mustUUID("33333333-3333-3333-3333-333333333333")
	tc.Users["alice"] = uid
	tc.Usernames[uid] = "alice"

	ev := &StatusEvidence{CoreSQLOrders: []StatusMapping{
		{LegacyValue: 0, Activity: "terminal", TargetValue: "completed", Confidence: "verified"},
	}}
	row := Row{
		"id": int64(7), "work_id": "20260115000001", "status": int64(0),
		"date":     time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC), // driver form, UTC tag
		"username": "alice", "real_name": "Alice", "type": int64(1),
		"sql": "update t set a = 1;", "execute_time": "2026-01-15 04:10:00",
		"source_id": "ds-1",
	}

	ok, blocking, err := tc.TransformSQLOrder(row, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || blocking {
		t.Fatalf("row must map cleanly: ok=%v blocking=%v quarantine=%+v", ok, blocking, tc.Quarantine)
	}

	var order *ChangeOrderModel
	for _, w := range tc.writes {
		if m, isOrder := w.Model.(ChangeOrderModel); isOrder {
			order = &m
			break
		}
	}
	if order == nil {
		t.Fatal("change_orders write missing")
	}
	if got, want := order.DisplayNumber, "YR-20260115-000001"; got != want {
		t.Errorf("display_number = %s, want %s (source-zone date)", got, want)
	}
	if got, want := order.SubmittedAt.Format(time.RFC3339), "2026-01-14T19:00:00Z"; got != want {
		t.Errorf("submitted_at = %s, want %s", got, want)
	}
	if order.TerminalAt == nil {
		t.Fatal("terminal_at missing")
	}
	if got, want := order.TerminalAt.Format(time.RFC3339), "2026-01-14T20:10:00Z"; got != want {
		t.Errorf("terminal_at = %s, want %s (execute_time reinterpreted in source zone)", got, want)
	}

	// budget_day rides the same day boundary inside the review run.
	var budgetDay string
	for _, w := range tc.writes {
		if m, isRun := w.Model.(ReviewRunModel); isRun {
			budgetDay = m.BudgetDay
			break
		}
	}
	if budgetDay != "2026-01-15" {
		t.Errorf("budget_day = %q, want 2026-01-15 (source-zone date)", budgetDay)
	}
}

// TestTransformSQLOrderQuarantinesZeroDate pins the T1 invariant on the real path:
// the driver's zero time must quarantine the row as an invalid date instead of
// migrating it as a year-0 instant.
func TestTransformSQLOrderQuarantinesZeroDate(t *testing.T) {
	c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	tc := NewTransformContext(mustUUID("44444444-4444-4444-4444-444444444444"), c, mustLocation("Asia/Shanghai"))
	uid := mustUUID("55555555-5555-5555-5555-555555555555")
	tc.Users["alice"] = uid
	tc.Usernames[uid] = "alice"

	ev := &StatusEvidence{CoreSQLOrders: []StatusMapping{
		{LegacyValue: 0, Activity: "terminal", TargetValue: "completed", Confidence: "verified"},
	}}
	row := Row{
		"id": int64(8), "work_id": "20260115000002", "status": int64(0),
		"date":     time.Time{}, // '0000-00-00 00:00:00' via parseTime
		"username": "alice", "type": int64(1), "sql": "select 1;",
	}

	ok, blocking, err := tc.TransformSQLOrder(row, ev)
	if err != nil {
		t.Fatal(err)
	}
	if ok || blocking {
		t.Fatalf("zero date must quarantine without blocking apply: ok=%v blocking=%v", ok, blocking)
	}
	if len(tc.Quarantine) != 1 || !strings.HasPrefix(tc.Quarantine[0].Reason, "date invalid") {
		t.Fatalf("quarantine = %+v, want a single 'date invalid' record", tc.Quarantine)
	}
	for _, w := range tc.writes {
		if _, isOrder := w.Model.(ChangeOrderModel); isOrder {
			t.Fatal("no change_orders row may be written for a zero date")
		}
	}
}

// TestSourceWallClockNilZoneFallsBackToUTC pins the defensive nil branch: the
// config layer guarantees a non-nil zone, but a nil zone must degrade to UTC
// rather than panic if a caller ever builds a TransformContext by hand.
func TestSourceWallClockNilZoneFallsBackToUTC(t *testing.T) {
	instant := time.Date(2026, 1, 14, 19, 0, 0, 0, time.UTC)
	if got, want := sourceWallClock(instant, nil).Format(time.RFC3339), "2026-01-14T19:00:00Z"; got != want {
		t.Errorf("nil zone = %s, want %s", got, want)
	}
	if got, want := sourceWallClock(instant, mustLocation("Asia/Shanghai")).Format(time.RFC3339), "2026-01-15T03:00:00+08:00"; got != want {
		t.Errorf("Shanghai wall clock = %s, want %s", got, want)
	}
}

// TestParseLegacyTimeRejectsPartialZeroFamily pins the residual family behind the
// zero-value rule: values whose year normalizes to <= 0 are invalid timestamps,
// not instants in year 0, and both branches must agree on that.
func TestParseLegacyTimeRejectsPartialZeroFamily(t *testing.T) {
	tc := &TransformContext{Timezone: mustLocation("Asia/Shanghai")}
	cases := []struct {
		name string
		in   any
	}{
		// '0000-00-00 12:34:56': not the driver's zero prefix, normalized to year<0.
		{"partial zero driver form", time.Date(0, 0, 0, 12, 34, 56, 0, time.UTC)},
		// '0000-01-15 00:00:00': a real month/day but year 0.
		{"year zero driver form", time.Date(0, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"year zero string", "0000-01-15 00:00:00"},
		{"partial zero string", "0000-00-00 12:34:56"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := tc.parseLegacyTime(c.in, "date"); err == nil {
				t.Fatalf("parseLegacyTime(%v) = %s, want an invalid-time error", c.in, got.Format(time.RFC3339))
			}
		})
	}
}

// TestQuarantineReasonCarriesParseError pins N1: the quarantine reason must carry
// the parser's message, so a misconfigured DSN or an odd legacy value is
// diagnosable from the quarantine table instead of reading a bare "date invalid".
func TestQuarantineReasonCarriesParseError(t *testing.T) {
	c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	tc := NewTransformContext(mustUUID("66666666-6666-6666-6666-666666666666"), c, nil) // nil zone forces the config error
	uid := mustUUID("77777777-7777-7777-7777-777777777777")
	tc.Users["alice"] = uid
	tc.Usernames[uid] = "alice"

	ev := &StatusEvidence{CoreSQLOrders: []StatusMapping{
		{LegacyValue: 0, Activity: "terminal", TargetValue: "completed", Confidence: "verified"},
	}}
	row := Row{
		"id": int64(9), "work_id": "20260115000003", "status": int64(0),
		"date":     time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC),
		"username": "alice", "type": int64(1), "sql": "select 1;",
	}
	if ok, _, err := tc.TransformSQLOrder(row, ev); err != nil || ok {
		t.Fatalf("row must quarantine: ok=%v err=%v", ok, err)
	}
	if len(tc.Quarantine) != 1 {
		t.Fatalf("quarantine = %+v, want one record", tc.Quarantine)
	}
	if got := tc.Quarantine[0].Reason; !strings.Contains(got, "source_timezone is not configured") {
		t.Fatalf("quarantine reason = %q, want it to carry the parse error", got)
	}
}

// TestInvalidTimeQuarantineCarriesReasonAcrossTables pins N1 on every legacy
// transform that parses a time column: a bad timestamp must quarantine the row
// with the parser's message attached, not a bare "time invalid". These are the
// operator's only clue when a DSN or a legacy value is wrong.
func TestInvalidTimeQuarantineCarriesReasonAcrossTables(t *testing.T) {
	shanghai := mustLocation("Asia/Shanghai")
	bad := "not-a-timestamp"

	newCtx := func() (*TransformContext, uuid.UUID) {
		c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
		tc := NewTransformContext(mustUUID("88888888-8888-8888-8888-888888888888"), c, shanghai)
		uid := mustUUID("99999999-9999-9999-9999-999999999999")
		tc.Users["alice"] = uid
		tc.Usernames[uid] = "alice"
		return tc, uid
	}

	t.Run("sql record", func(t *testing.T) {
		tc, _ := newCtx()
		tc.attemptOfOrder["w-1"] = mustUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
		ok, err := tc.TransformSQLRecord(Row{"id": int64(1), "work_id": "w-1", "time": bad})
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v, want quarantine", ok, err)
		}
		assertQuarantineCarries(t, tc, "core_sql_records", "time invalid")
	})

	t.Run("workflow detail", func(t *testing.T) {
		tc, _ := newCtx()
		if tc.TransformWorkflowDetail(Row{"id": int64(2), "work_id": "w-2", "time": bad}) {
			t.Fatal("want quarantine")
		}
		assertQuarantineCarries(t, tc, "core_workflow_details", "time invalid")
	})

	t.Run("order comment", func(t *testing.T) {
		tc, _ := newCtx()
		tc.Orders["w-3"] = mustUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
		if tc.TransformOrderComment(Row{"id": int64(3), "work_id": "w-3", "time": bad}) {
			t.Fatal("want quarantine")
		}
		assertQuarantineCarries(t, tc, "core_order_comments", "time invalid")
	})

	t.Run("query order", func(t *testing.T) {
		tc, _ := newCtx()
		ev := &StatusEvidence{CoreQueryOrders: []StatusMapping{
			{LegacyValue: 3, Activity: "terminal", TargetValue: "grant_active", Confidence: "verified"},
		}}
		ok, _, err := tc.TransformQueryOrder(Row{
			"id": int64(4), "work_id": "w-4", "status": int64(3), "date": bad,
			"username": "alice", "source_id": "ds-1",
		}, ev)
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v, want quarantine", ok, err)
		}
		assertQuarantineCarries(t, tc, "core_query_orders", "date invalid")
	})

	t.Run("query record", func(t *testing.T) {
		tc, _ := newCtx()
		tc.QueryOrders["w-5"] = mustUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
		ok, err := tc.TransformQueryRecord(Row{"id": int64(5), "work_id": "w-5", "time": bad, "username": "alice"})
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v, want quarantine", ok, err)
		}
		assertQuarantineCarries(t, tc, "core_query_records", "time invalid")
	})
}

func assertQuarantineCarries(t *testing.T, tc *TransformContext, table, prefix string) {
	t.Helper()
	for _, q := range tc.Quarantine {
		if q.Table != table {
			continue
		}
		if !strings.HasPrefix(q.Reason, prefix) {
			t.Fatalf("%s quarantine reason = %q, want prefix %q", table, q.Reason, prefix)
		}
		if q.Reason == prefix {
			t.Fatalf("%s quarantine reason = %q, want the parse error appended", table, q.Reason)
		}
		return
	}
	t.Fatalf("no %s quarantine record: %+v", table, tc.Quarantine)
}

// TestParseLegacyTimeRejectsRFC3339YearZeroFamily: strings carrying an explicit offset take the
// early time.Parse return branch and must reject invalid years just like the other two branches;
// otherwise '0001-01-01T00:00:00Z' (which parses to time.Time's zero value) would be returned as a valid
// instant and bypass the caller's fallback branch. Although this family can only arrive via SnapshotSource
// (and never from production MySQLSource), branch equivalence is part of this function's contract.
func TestParseLegacyTimeRejectsRFC3339YearZeroFamily(t *testing.T) {
	transform := &TransformContext{Timezone: mustLocation("Asia/Shanghai")}
	for _, value := range []string{
		"0000-01-15T00:00:00Z",
		"0001-01-01T00:00:00Z",
		"0000-01-15T08:30:00+08:00",
	} {
		if got, err := transform.parseLegacyTime(value, "date"); err == nil {
			t.Errorf("parseLegacyTime(%q) = %s, want an invalid-year error", value, got.Format(time.RFC3339))
		}
	}
	// RFC3339 strings with normal years are still interpreted using their embedded offset, unaffected by the new guard.
	got, err := transform.parseLegacyTime("2026-01-15T08:30:00+08:00", "date")
	if err != nil {
		t.Fatalf("valid RFC3339 rejected: %v", err)
	}
	if want := "2026-01-15T00:30:00Z"; got.Format(time.RFC3339) != want {
		t.Errorf("offset string = %s, want %s", got.Format(time.RFC3339), want)
	}
}

// TestQuarantineReasonHasNoPlaintextSourceValue: quarantine records are persisted into
// legacy_migration_runs.report and visible to admins, while the report contract mandates
// "no plaintext in logs or reports" and ManifestSensitiveData hardcodes PlaintextInReport=false.
// Therefore, unparseable raw source values must never appear in Reason.
func TestQuarantineReasonHasNoPlaintextSourceValue(t *testing.T) {
	transform := &TransformContext{Timezone: mustLocation("Asia/Shanghai")}
	// Variable name intentionally avoids credential terminology: this holds "raw source data that must not appear in reports"
	// rather than a credential, so use payload instead of secret.
	const payload = "not-a-date-with-sensitive-payload-0123456789"
	_, err := transform.parseLegacyTime(payload, "date")
	if err == nil {
		t.Fatal("unparseable value accepted")
	}
	if strings.Contains(err.Error(), payload) {
		t.Fatalf("parse error leaks the raw value: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "format unsupported") {
		t.Fatalf("parse error lost its diagnostic class: %q", err.Error())
	}
	// Length and printability are permitted diagnostic attributes. Assert printable=true
	// so that if isPrintableASCII ever degenerated to always returning false, the test would catch it.
	if !strings.Contains(err.Error(), "bytes") || !strings.Contains(err.Error(), "printable=true") {
		t.Fatalf("parse error dropped length/printability: %q", err.Error())
	}
	// Non-printable inputs must likewise never echo raw bytes and must report printable=false.
	control := "\x00\x01\x02 not a date \xff"
	_, err = transform.parseLegacyTime(control, "date")
	if err == nil {
		t.Fatal("control-byte value accepted")
	}
	if strings.Contains(err.Error(), control) || strings.Contains(err.Error(), "\xff") {
		t.Fatalf("parse error leaks non-printable raw value: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "printable=false") {
		t.Fatalf("printable flag not reported as false: %q", err.Error())
	}
}

// TestIsPrintableASCIIClassification directly pins the behavior of isPrintableASCII
// so that a regression in the quarantine reason's printable flag (always true or always false) cannot go unnoticed.
func TestIsPrintableASCIIClassification(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"not-a-date", true},
		{"2026-01-15 08:30:00", true},
		{"\x00", false},
		{"\x01\x02", false},
		{"\xff", false},
		{"\uff12\uff10\uff12\uff16", false}, // Fullwidth digits: human-readable UTF-8 but not ASCII
	}
	for _, c := range cases {
		if got := isPrintableASCII(c.value); got != c.want {
			t.Errorf("isPrintableASCII(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}
