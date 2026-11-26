// Package systz resolves and caches general.system_timezone from PostgreSQL,
// providing DST-safe day boundaries, business calendar days, and wall-clock re-anchoring.
package systz

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Ensure full IANA tzdata inside minimal containers.

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/settings"
)

const cacheTTL = 30 * time.Second

type cachedTZ struct {
	name      string
	loc       *time.Location
	expiresAt time.Time
}

var (
	locMu    sync.RWMutex
	locCache = map[string]*time.Location{}

	dbMu    sync.RWMutex
	dbGen   uint64
	dbCache = map[*sql.DB]cachedTZ{}
)

func init() {
	settings.RegisterPostReplaceHook(func(namespace string) {
		if namespace == settings.NsGeneral {
			Invalidate()
		}
	})
}

// DefaultName returns the canonical default timezone name from settings validation.
func DefaultName() string {
	return settings.DefaultSystemTimezone
}

// DefaultLocation returns the time.Location for DefaultName (falling back to time.UTC only on OS/tzdata corruption).
func DefaultLocation() *time.Location {
	if loc, ok := loadLocation(DefaultName()); ok {
		return loc
	}
	return time.UTC
}

// Invalidate clears all cached DB timezone entries (invoked automatically when general settings change).
func Invalidate() {
	dbMu.Lock()
	dbGen++
	clear(dbCache)
	dbMu.Unlock()
}

// InvalidateDB clears cached timezone state for a specific gorm.DB connection pool.
func InvalidateDB(db *gorm.DB) {
	key := sqlDBKey(db)
	if key == nil {
		Invalidate()
		return
	}
	dbMu.Lock()
	dbGen++
	delete(dbCache, key)
	dbMu.Unlock()
}

func sqlDBKey(db *gorm.DB) *sql.DB {
	if db == nil {
		return nil
	}
	raw, err := db.DB()
	if err != nil {
		return nil
	}
	return raw
}

func loadLocation(name string) (*time.Location, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, false
	}
	locMu.RLock()
	cached, ok := locCache[trimmed]
	locMu.RUnlock()
	if ok {
		return cached, true
	}
	loc, err := time.LoadLocation(trimmed)
	if err != nil {
		return nil, false
	}
	locMu.Lock()
	locCache[trimmed] = loc
	locMu.Unlock()
	return loc, true
}

func resolve(ctx context.Context, db *gorm.DB) (string, *time.Location) {
	key := sqlDBKey(db)
	now := time.Now()
	dbMu.RLock()
	startGen := dbGen
	if key != nil {
		if entry, ok := dbCache[key]; ok && now.Before(entry.expiresAt) {
			dbMu.RUnlock()
			return entry.name, entry.loc
		}
	}
	dbMu.RUnlock()

	name := DefaultName()
	loc := DefaultLocation()
	if db != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		var tzName *string
		if err := db.WithContext(ctx).Raw(
			`select settings->>'system_timezone' from system_settings where namespace = 'general'`,
		).Scan(&tzName).Error; err == nil && tzName != nil {
			if candidate := strings.TrimSpace(*tzName); candidate != "" {
				if parsed, ok := loadLocation(candidate); ok {
					name = candidate
					loc = parsed
				}
			}
		}
	}

	if key != nil {
		dbMu.Lock()
		if dbGen == startGen {
			dbCache[key] = cachedTZ{name: name, loc: loc, expiresAt: now.Add(cacheTTL)}
		}
		dbMu.Unlock()
	}
	return name, loc
}

// Name returns the active IANA timezone identifier for general.system_timezone.
func Name(ctx context.Context, db *gorm.DB) string {
	name, _ := resolve(ctx, db)
	return name
}

// Location returns the active *time.Location for general.system_timezone (never nil).
func Location(ctx context.Context, db *gorm.DB) *time.Location {
	_, loc := resolve(ctx, db)
	return loc
}

// DayRange parses YYYY-MM-DD in loc and returns [startUTC, endExclusiveUTC) spanning that local calendar day.
func DayRange(dateStr string, loc *time.Location) (time.Time, time.Time, error) {
	if loc == nil {
		loc = DefaultLocation()
	}
	localStart, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(dateStr), loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	localNext := time.Date(localStart.Year(), localStart.Month(), localStart.Day()+1, 0, 0, 0, 0, loc)
	return localStart.UTC(), localNext.UTC(), nil
}

// BusinessDay returns the YYYY-MM-DD calendar date of t in loc.
func BusinessDay(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = DefaultLocation()
	}
	return t.In(loc).Format(time.DateOnly)
}

// ReanchorWallClock reinterprets wall-clock Y-M-D H:M:S.ns components (expressed as UTC digits) in loc, returning UTC.
func ReanchorWallClock(wall time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = DefaultLocation()
	}
	w := wall.UTC()
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), loc).UTC()
}
