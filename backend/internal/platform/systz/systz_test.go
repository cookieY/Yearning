package systz_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	"github.com/cookieY/YearningX/backend/internal/settings"
)

type stubTZDriver struct {
	mu      sync.Mutex
	tzValue *string
	queries int
	onQuery func()
}

type stubTZConnector struct{ st *stubTZDriver }

func (c *stubTZConnector) Connect(context.Context) (driver.Conn, error) {
	return &stubTZConn{st: c.st}, nil
}
func (c *stubTZConnector) Driver() driver.Driver { return nil }

type stubTZConn struct{ st *stubTZDriver }

func (c *stubTZConn) Prepare(string) (driver.Stmt, error) { return nil, nil }
func (c *stubTZConn) Close() error                        { return nil }
func (c *stubTZConn) Begin() (driver.Tx, error)           { return &stubTZTx{}, nil }
func (c *stubTZConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	c.st.mu.Lock()
	c.st.queries++
	val := c.st.tzValue
	hook := c.st.onQuery
	c.st.mu.Unlock()
	if hook != nil {
		hook()
	}
	if val == nil {
		return &stubTZRows{cols: []string{"tz"}, rows: nil}, nil
	}
	return &stubTZRows{cols: []string{"tz"}, rows: [][]driver.Value{{*val}}}, nil
}

type stubTZTx struct{}

func (*stubTZTx) Commit() error   { return nil }
func (*stubTZTx) Rollback() error { return nil }

type stubTZRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *stubTZRows) Columns() []string { return r.cols }
func (r *stubTZRows) Close() error      { return nil }
func (r *stubTZRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func openStubTZDB(t *testing.T, st *stubTZDriver) *gorm.DB {
	t.Helper()
	raw := sql.OpenDB(&stubTZConnector{st: st})
	t.Cleanup(func() {
		systz.Invalidate()
		_ = raw.Close()
	})
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: raw}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return gdb
}

func TestDefaultAndInvalidFallbacks(t *testing.T) {
	if got := systz.DefaultName(); got != settings.DefaultSystemTimezone {
		t.Fatalf("DefaultName() = %q, want %q", got, settings.DefaultSystemTimezone)
	}
	if loc := systz.Location(context.Background(), nil); loc.String() != "Asia/Shanghai" {
		t.Fatalf("nil DB fallback = %q, want Asia/Shanghai", loc.String())
	}

	empty := ""
	st := &stubTZDriver{tzValue: &empty}
	db := openStubTZDB(t, st)
	if name := systz.Name(context.Background(), db); name != "Asia/Shanghai" {
		t.Fatalf("empty setting = %q, want Asia/Shanghai", name)
	}

	bogus := "Mars/Olympus_Mons"
	st.mu.Lock()
	st.tzValue = &bogus
	st.mu.Unlock()
	systz.InvalidateDB(db)
	if loc := systz.Location(context.Background(), db); loc.String() != "Asia/Shanghai" {
		t.Fatalf("invalid tz fallback = %q, want Asia/Shanghai", loc.String())
	}
}

func TestCacheAndInvalidateOnSettingsChange(t *testing.T) {
	shanghai := "Asia/Shanghai"
	st := &stubTZDriver{tzValue: &shanghai}
	db := openStubTZDB(t, st)

	if got := systz.Name(context.Background(), db); got != "Asia/Shanghai" {
		t.Fatalf("first read = %q", got)
	}
	if got := systz.Name(context.Background(), db); got != "Asia/Shanghai" {
		t.Fatalf("cached read = %q", got)
	}
	st.mu.Lock()
	queriesBeforeInvalidate := st.queries
	utc := "UTC"
	st.tzValue = &utc
	st.mu.Unlock()
	if queriesBeforeInvalidate != 1 {
		t.Fatalf("expected 1 DB query before invalidation, got %d", queriesBeforeInvalidate)
	}
	systz.InvalidateDB(db)
	if got := systz.Name(context.Background(), db); got != "UTC" {
		t.Fatalf("post-invalidation read = %q, want UTC", got)
	}
}

func TestTimezoneMatrixDayRangeBusinessDayAndDST(t *testing.T) {
	cases := []struct {
		name      string
		tz        string
		date      string
		wantStart string
		wantEnd   string
	}{
		{
			name:      "shanghai_midnight_boundary",
			tz:        "Asia/Shanghai",
			date:      "2026-11-25",
			wantStart: "2026-11-24T16:00:00Z",
			wantEnd:   "2026-11-25T16:00:00Z",
		},
		{
			name:      "shanghai_year_rollover",
			tz:        "Asia/Shanghai",
			date:      "2026-01-01",
			wantStart: "2025-12-31T16:00:00Z",
			wantEnd:   "2026-01-01T16:00:00Z",
		},
		{
			name:      "utc_standard",
			tz:        "UTC",
			date:      "2026-11-25",
			wantStart: "2026-11-25T00:00:00Z",
			wantEnd:   "2026-11-26T00:00:00Z",
		},
		{
			name:      "la_spring_forward_23h_day",
			tz:        "America/Los_Angeles",
			date:      "2026-03-08",
			wantStart: "2026-03-08T08:00:00Z",
			wantEnd:   "2026-03-09T07:00:00Z",
		},
		{
			name:      "ny_fall_back_25h_day",
			tz:        "America/New_York",
			date:      "2026-11-01",
			wantStart: "2026-11-01T04:00:00Z",
			wantEnd:   "2026-11-02T05:00:00Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.tz)
			if err != nil {
				t.Fatal(err)
			}
			gotStart, gotEnd, err := systz.DayRange(tc.date, loc)
			if err != nil {
				t.Fatalf("DayRange(%q): %v", tc.date, err)
			}
			if gotStart.Format(time.RFC3339) != tc.wantStart || gotEnd.Format(time.RFC3339) != tc.wantEnd {
				t.Fatalf("DayRange(%s, %s) = [%s, %s), want [%s, %s)",
					tc.date, tc.tz,
					gotStart.Format(time.RFC3339), gotEnd.Format(time.RFC3339),
					tc.wantStart, tc.wantEnd)
			}
			// Lower bound belongs to tc.date; 1ns before end belongs to tc.date; end itself is next day.
			if day := systz.BusinessDay(gotStart, loc); day != tc.date {
				t.Fatalf("BusinessDay(start) = %q, want %q", day, tc.date)
			}
			if day := systz.BusinessDay(gotEnd.Add(-time.Second), loc); day != tc.date {
				t.Fatalf("BusinessDay(23:59:59) = %q, want %q", day, tc.date)
			}
			if day := systz.BusinessDay(gotEnd, loc); day == tc.date {
				t.Fatalf("BusinessDay(next 00:00) unexpectedly stayed on %q", tc.date)
			}
		})
	}

	// Shanghai 00:00-08:00 (2026-11-24T17:30:00Z = 2026-11-25 01:30:00 CST) belongs to 2026-11-25 in Shanghai, 2026-11-24 in UTC.
	earlyMorningCST := time.Date(2026, 11, 24, 17, 30, 0, 0, time.UTC)
	shLoc, _ := time.LoadLocation("Asia/Shanghai")
	if got := systz.BusinessDay(earlyMorningCST, shLoc); got != "2026-11-25" {
		t.Fatalf("BusinessDay(CST 01:30) = %q, want 2026-11-25", got)
	}
	if got := systz.BusinessDay(earlyMorningCST, time.UTC); got != "2026-11-24" {
		t.Fatalf("BusinessDay(UTC 17:30) = %q, want 2026-11-24", got)
	}
	if !strings.Contains(systz.ReanchorWallClock(time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC), shLoc).UTC().Format(time.RFC3339), "2026-11-25T02:00:00Z") {
		t.Fatalf("ReanchorWallClock unexpected result")
	}
}

func TestConcurrentResolveAndInvalidateDoesNotWriteStaleCache(t *testing.T) {
	shanghai := "Asia/Shanghai"
	utc := "UTC"
	inQuery := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once

	st := &stubTZDriver{
		tzValue: &shanghai,
		onQuery: func() {
			once.Do(func() {
				close(inQuery)
				<-proceed
			})
		},
	}
	db := openStubTZDB(t, st)

	done := make(chan string, 1)
	go func() {
		done <- systz.Name(context.Background(), db)
	}()

	<-inQuery
	st.mu.Lock()
	st.tzValue = &utc
	st.mu.Unlock()
	systz.Invalidate()
	close(proceed)

	if first := <-done; first != "Asia/Shanghai" {
		t.Fatalf("in-flight resolve returned %q, want Asia/Shanghai", first)
	}
	if next := systz.Name(context.Background(), db); next != "UTC" {
		t.Fatalf("post-invalidation Name() = %q, want UTC (stale value poisoned cache)", next)
	}
}
