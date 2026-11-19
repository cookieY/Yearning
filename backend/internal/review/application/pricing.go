// pricing.go S008 reference pricing: Provider rate versions + time-of-day window selection, fixed-point
// reference calculation, per-HTTP-call ledger, and budget currency consistency.
//
// Boundaries (PRD ai-provider-generation-pricing sections 3-5): rate versions are append-only
// (modifications create a new version without rewriting historical snapshots); cross-midnight windows
// are normalized into two same-day window rows upon write, and overlaps are rejected; currencies are
// strictly CNY/USD without FX conversion; internal amounts use bounded fixed-point arithmetic throughout
// (int64 micro-units + big.Int multiply/divide, no binary floats); legacy global prices degrade to a
// compatible default with a source marker; tri-state usage (nil = unknown) is never converted to 0;
// historical ledger rows are never rewritten or recomputed. The integer minor-currency-unit convention
// for budget reservation/settlement (budget.go) remains independent and unaffected by this file.
package application

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// Rate source (ai_call_usage.rate_source).
const (
	RateSourceProviderVersion = "provider_version"
	RateSourceGlobalSettings  = "global_settings"
	RateSourceNone            = "none"
)

// Reference cost calculation basis (ai_call_usage.calc_basis).
const (
	CalcBasisFullDetail    = "full_detail"
	CalcBasisPartialDetail = "partial_detail"
	CalcBasisBaseOnly      = "base_only"
	CalcBasisNotComputable = "not_computable"
)

// Fixed-point price scales: price = currency units per million Tokens, represented internally as
// micro-unit (x 1e6) integers (numeric(18,6)); reference amounts are represented as nano-unit (x 1e8)
// integers (numeric(24,8)). Both are bounded fixed-point integers with no binary floating point.
const (
	priceScale  = 1_000_000
	amountScale = 100_000_000
)

var pricePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)

// parseDecimalMicro parses a bounded decimal string into a fixed-point micro-unit integer
// (0 <= v < 1e12, <= 6 decimal places; rejects negatives, scientific notation, redundant leading zeros, and out-of-range values).
func parseDecimalMicro(s string) (int64, error) {
	if !pricePattern.MatchString(s) {
		return 0, fmt.Errorf("invalid decimal %q", s)
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	var v int64
	if intPart != "0" {
		n, err := strconv.ParseInt(intPart, 10, 64)
		if err != nil {
			return 0, err
		}
		v = n
	}
	frac := fracPart + strings.Repeat("0", 6-len(fracPart))
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, err
	}
	return v*priceScale + f, nil
}

// formatMicroDecimal formats micro-units -> canonical decimal string (always 6 decimal places).
func formatMicroDecimal(v int64) string {
	neg := ""
	if v < 0 {
		neg = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%06d", neg, v/priceScale, v%priceScale)
}

// ParsePriceMicro is the price payload parsing entry point (httpapi layer): bounded decimal -> micro-units,
// returning 1001 (VALIDATION_FAILED) on invalid values with field-level detail supplied by the caller.
func ParsePriceMicro(s string) (int64, error) {
	v, err := parseDecimalMicro(s)
	if err != nil {
		return 0, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "price must be a decimal with at most 6 fraction digits"}
	}
	return v, nil
}

// formatNanoAmount formats nano-units -> canonical decimal string (always 8 decimal places,
// matching numeric(24,8) column values).
func formatNanoAmount(v *big.Int) string {
	sign := ""
	if v.Sign() < 0 {
		sign = "-"
	}
	abs := new(big.Int).Abs(v)
	q, r := new(big.Int).QuoRem(abs, big.NewInt(amountScale), new(big.Int))
	return fmt.Sprintf("%s%s.%08s", sign, q.String(), fmt.Sprintf("%08d", r.Int64()))
}

// RateWindowInput holds input parameters for writing a time window ([StartMinute, EndMinute) minutes of day;
// End < Start = cross-midnight, split into two same-day window rows upon write).
type RateWindowInput struct {
	StartMinute           int    // 0..1439
	EndMinute             int    // 1..1440
	InputPriceMicro       int64  // Standard input price (in micro-units)
	OutputPriceMicro      int64  // Output price
	CachedReadPriceMicro  *int64 // Cache read price (nil = unconfigured)
	CachedWritePriceMicro *int64 // Cache write price (nil = unconfigured)
}

// CreateRateVersionInput holds input parameters for writing a rate version.
type CreateRateVersionInput struct {
	ProviderID     string
	Currency       string // CNY|USD
	Timezone       string // IANA
	EffectiveFrom  time.Time
	EffectiveUntil *time.Time // nil = open-ended
	Windows        []RateWindowInput
}

// normalizeWindows validates and normalizes time windows: splits cross-midnight windows into two rows
// and returns the sorted set of same-day windows. Overlaps (beyond touching boundaries) are rejected with 1001.
func normalizeWindows(in []RateWindowInput) ([]RateWindowInput, error) {
	if len(in) == 0 || len(in) > 24 {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "windows must contain 1..24 entries",
			Fields: map[string][]string{"windows": {"must contain 1..24 entries"}}}
	}
	out := make([]RateWindowInput, 0, len(in)*2)
	for _, w := range in {
		if w.StartMinute < 0 || w.StartMinute > 1439 || w.EndMinute < 1 || w.EndMinute > 1440 {
			return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "window minutes out of range",
				Fields: map[string][]string{"windows": {"start_minute must be 0..1439 and end_minute 1..1440"}}}
		}
		if w.EndMinute == w.StartMinute {
			return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "window end must differ from start",
				Fields: map[string][]string{"windows": {"end_minute must differ from start_minute"}}}
		}
		if w.InputPriceMicro < 0 || w.OutputPriceMicro < 0 ||
			(w.CachedReadPriceMicro != nil && *w.CachedReadPriceMicro < 0) ||
			(w.CachedWritePriceMicro != nil && *w.CachedWritePriceMicro < 0) {
			return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "prices must be non-negative",
				Fields: map[string][]string{"windows": {"prices must be non-negative"}}}
		}
		if w.EndMinute > w.StartMinute {
			out = append(out, w)
			continue
		}
		// Cross-midnight: [start, 1440) + [0, end). Both segments retain the same price group.
		out = append(out, RateWindowInput{
			StartMinute: w.StartMinute, EndMinute: 1440,
			InputPriceMicro: w.InputPriceMicro, OutputPriceMicro: w.OutputPriceMicro,
			CachedReadPriceMicro: w.CachedReadPriceMicro, CachedWritePriceMicro: w.CachedWritePriceMicro,
		}, RateWindowInput{
			StartMinute: 0, EndMinute: w.EndMinute,
			InputPriceMicro: w.InputPriceMicro, OutputPriceMicro: w.OutputPriceMicro,
			CachedReadPriceMicro: w.CachedReadPriceMicro, CachedWritePriceMicro: w.CachedWritePriceMicro,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartMinute < out[j].StartMinute })
	for i := 1; i < len(out); i++ {
		if out[i].StartMinute < out[i-1].EndMinute {
			return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "rate windows overlap after cross-midnight normalization",
				Fields: map[string][]string{"windows": {"windows must not overlap after cross-midnight normalization"}}}
		}
	}
	return out, nil
}

// RateVersionView is the read view of a rate version (including time windows and fixed-point price strings).
type RateVersionView struct {
	ID             string           `json:"id"`
	ProviderID     string           `json:"provider_id"`
	Currency       string           `json:"currency"`
	Timezone       string           `json:"timezone"`
	EffectiveFrom  time.Time        `json:"effective_from"`
	EffectiveUntil *time.Time       `json:"effective_until,omitempty"`
	Version        int64            `json:"version"`
	Windows        []RateWindowView `json:"windows"`
	CreatedAt      time.Time        `json:"created_at"`
}

// RateWindowView is the read view of a time window (normalized same-day storage shape).
type RateWindowView struct {
	ID               string  `json:"id"`
	StartMinute      int     `json:"start_minute"`
	EndMinute        int     `json:"end_minute"`
	InputPrice       string  `json:"input_price"`
	OutputPrice      string  `json:"output_price"`
	CachedReadPrice  *string `json:"cached_read_price,omitempty"`
	CachedWritePrice *string `json:"cached_write_price,omitempty"`
}

// CreateRateVersion creates a rate version (version number = max + 1 for the same Provider; overlapping
// effective intervals with existing versions are rejected; when budget is enabled and the Provider is
// enabled, currency must match the budget currency — Price-04). Historical versions are never updated (new versions are append-only).
func (s *Services) CreateRateVersion(ctx context.Context, actorUserID string, in CreateRateVersionInput) (*RateVersionView, error) {
	if in.ProviderID == "" || (in.Currency != "CNY" && in.Currency != "USD") {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
			Detail: "currency must be CNY or USD",
			Fields: map[string][]string{"currency": {"must be CNY or USD"}}}
	}
	if in.EffectiveFrom.IsZero() {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
			Detail: "effective_from must be an RFC 3339 date-time",
			Fields: map[string][]string{"effective_from": {"must be an RFC 3339 date-time"}}}
	}
	if in.EffectiveUntil != nil && !in.EffectiveUntil.After(in.EffectiveFrom) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "effective_until must be after effective_from",
			Fields: map[string][]string{"effective_until": {"must be after effective_from"}}}
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "timezone must be a valid IANA name",
			Fields: map[string][]string{"timezone": {"must be a valid IANA name"}}}
	}
	windows, err := normalizeWindows(in.Windows)
	if err != nil {
		return nil, err
	}
	var out *RateVersionView
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var provider struct {
			ID      string
			Enabled bool
		}
		// R1 M-1: lock the Provider row with FOR UPDATE to serialize rate writes for the same Provider
		// (otherwise concurrent transactions reading stale snapshots could commit mutually overlapping effective
		// intervals or overwrite each other's closing bounds, creating pricing gaps).
		if err := tx.Raw(`select id::text as id, enabled from ai_providers where id = ? for update`, in.ProviderID).Scan(&provider).Error; err != nil {
			return err
		}
		if provider.ID == "" {
			return ErrNotFound
		}
		// Effective interval (unique price selection per Provider at any instant): genuine interval conflicts
		// are rejected; a previously open-ended version (effective_until NULL) whose start precedes the new
		// version's start is automatically closed at the new version's start — the open end is merely a
		// placeholder, so closing it never alters any historical application period (Price-01: history is never recomputed under new rates).
		var existing []struct {
			ID             string
			EffectiveFrom  time.Time
			EffectiveUntil *time.Time
		}
		if err := tx.Raw(`
			select id::text as id, effective_from, effective_until
			from ai_provider_rate_versions where provider_id = ?
			order by effective_from`, in.ProviderID).Scan(&existing).Error; err != nil {
			return err
		}
		newEnd := derefTime(in.EffectiveUntil, maxTime())
		for _, ex := range existing {
			exEnd := derefTime(ex.EffectiveUntil, maxTime())
			if !ex.EffectiveFrom.Before(newEnd) || !in.EffectiveFrom.Before(exEnd) {
				continue // No overlap
			}
			if ex.EffectiveUntil == nil && ex.EffectiveFrom.Before(in.EffectiveFrom) {
				// Close the prior open-ended version at the start of the new version.
				if err := tx.Exec(`update ai_provider_rate_versions
					set effective_until = ?, updated_at = ? where id = ?`,
					in.EffectiveFrom, nowUTC(), ex.ID).Error; err != nil {
					return err
				}
				continue
			}
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "effective range overlaps an existing bounded rate version; create a disjoint future version instead"}
		}
		// Price-04: budget enabled and this Provider enabled -> enforce currency consistency.
		if err := s.checkRateCurrencyConsistency(ctx, tx, in.ProviderID, provider.Enabled, in.Currency); err != nil {
			return err
		}
		var nextVersion int64
		if err := tx.Raw(`select coalesce(max(version), 0) + 1 from ai_provider_rate_versions where provider_id = ?`,
			in.ProviderID).Scan(&nextVersion).Error; err != nil {
			return err
		}
		id := newID()
		now := nowUTC()
		if err := tx.Exec(`
			insert into ai_provider_rate_versions (id, provider_id, currency, timezone,
				effective_from, effective_until, version, created_by_user_id, created_at, updated_at)
			values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.ProviderID, in.Currency, in.Timezone,
			in.EffectiveFrom, in.EffectiveUntil, nextVersion, actorUserID, now, now).Error; err != nil {
			// R1 M-1: concurrent version-number race at the same instant maps to 1005 (readable), never a raw 500.
			if isUniqueViolationErr(err) {
				return &domain.Error{Code: 1005, Name: "RESOURCE_ALREADY_EXISTS",
					Detail: "concurrent rate version creation; retry"}
			}
			return err
		}
		for _, w := range windows {
			wid := newID()
			if err := tx.Exec(`
				insert into ai_provider_rate_windows (id, rate_version_id, start_minute, end_minute,
					input_price, output_price, cached_read_price, cached_write_price)
				values (?, ?, ?, ?, ?, ?, ?, ?)`,
				wid, id, w.StartMinute, w.EndMinute,
				formatMicroDecimal(w.InputPriceMicro), formatMicroDecimal(w.OutputPriceMicro),
				derefPrice(w.CachedReadPriceMicro), derefPrice(w.CachedWritePriceMicro)).Error; err != nil {
				return err
			}
		}
		view := &RateVersionView{
			ID: id, ProviderID: in.ProviderID, Currency: in.Currency, Timezone: in.Timezone,
			EffectiveFrom: in.EffectiveFrom, EffectiveUntil: in.EffectiveUntil,
			Version: nextVersion, Windows: make([]RateWindowView, 0, len(windows)), CreatedAt: now,
		}
		for _, w := range windows {
			view.Windows = append(view.Windows, RateWindowView{
				StartMinute: w.StartMinute, EndMinute: w.EndMinute,
				InputPrice: formatMicroDecimal(w.InputPriceMicro), OutputPrice: formatMicroDecimal(w.OutputPriceMicro),
				CachedReadPrice: derefPriceString(w.CachedReadPriceMicro), CachedWritePrice: derefPriceString(w.CachedWritePriceMicro),
			})
		}
		out = view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkRateCurrencyConsistency Price-04: when budget is enabled and the Provider is enabled,
// the new rate currency must equal the budget currency; also rejects writes whose currency differs from
// the currently effective rate currency of other enabled Providers (the save side of dual save/enable validation).
func (s *Services) checkRateCurrencyConsistency(ctx context.Context, tx *gorm.DB, providerID string, providerEnabled bool, currency string) error {
	settings, err := s.LoadBudgetSettings(ctx, tx)
	if err != nil {
		return err
	}
	if !settings.Enforced || !providerEnabled {
		return nil
	}
	if currency != settings.Currency {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
			Detail: fmt.Sprintf("budget is enforced in %s; enabled provider rates must use the same currency", settings.Currency)}
	}
	var others []struct{ Currency string }
	if err := tx.Raw(`
		select distinct v.currency from ai_provider_rate_versions v
		join ai_providers p on p.id = v.provider_id
		where p.enabled and v.provider_id <> ?
		  and v.effective_from <= now()
		  and (v.effective_until is null or v.effective_until > now())`, providerID).Scan(&others).Error; err != nil {
		return err
	}
	for _, o := range others {
		if o.Currency != currency {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
				Detail: fmt.Sprintf("another enabled provider has an effective rate in %s while the enforced budget currency is %s", o.Currency, settings.Currency)}
		}
	}
	return nil
}

// CheckProviderEnableRateCurrency validates on the enable side (called by providerstore when enabling
// a Provider): when budget is enabled and the Provider has a currently effective rate, the rate currency
// must equal the budget currency.
func (s *Services) CheckProviderEnableRateCurrency(ctx context.Context, db *gorm.DB, providerID string) error {
	settings, err := s.LoadBudgetSettings(ctx, db)
	if err != nil {
		return err
	}
	if !settings.Enforced {
		return nil
	}
	var currency *string
	if err := db.WithContext(ctx).Raw(`
		select v.currency from ai_provider_rate_versions v
		where v.provider_id = ?
		  and v.effective_from <= now()
		  and (v.effective_until is null or v.effective_until > now())
		order by v.effective_from desc limit 1`, providerID).Scan(&currency).Error; err != nil {
		return err
	}
	if currency != nil && *currency != settings.Currency {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
			Detail: fmt.Sprintf("provider effective rate is in %s while the enforced budget currency is %s", *currency, settings.Currency)}
	}
	return nil
}

// ListRateVersions lists Provider rate versions (newest -> oldest).
func (s *Services) ListRateVersions(ctx context.Context, providerID string) ([]*RateVersionView, error) {
	var versions []struct {
		ID             string
		ProviderID     string
		Currency       string
		Timezone       string
		EffectiveFrom  time.Time
		EffectiveUntil *time.Time
		Version        int64
		CreatedAt      time.Time
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text as id, provider_id::text as provider_id, currency, timezone,
		       effective_from, effective_until, version, created_at
		from ai_provider_rate_versions where provider_id = ?
		order by version desc`, providerID).Scan(&versions).Error; err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return []*RateVersionView{}, nil
	}
	var windows []struct {
		RateVersionID string
		ID            string
		StartMinute   int
		EndMinute     int
		InputPrice    string
		OutputPrice   string
		CachedRead    *string
		CachedWrite   *string
	}
	ids := make([]string, 0, len(versions))
	for _, v := range versions {
		ids = append(ids, v.ID)
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select rate_version_id::text as rate_version_id, id::text as id, start_minute, end_minute,
		       input_price::text as input_price, output_price::text as output_price,
		       cached_read_price::text as cached_read, cached_write_price::text as cached_write
		from ai_provider_rate_windows where rate_version_id in ?
		order by start_minute`, ids).Scan(&windows).Error; err != nil {
		return nil, err
	}
	byVersion := map[string][]RateWindowView{}
	for _, w := range windows {
		byVersion[w.RateVersionID] = append(byVersion[w.RateVersionID], RateWindowView{
			ID: w.ID, StartMinute: w.StartMinute, EndMinute: w.EndMinute,
			InputPrice: w.InputPrice, OutputPrice: w.OutputPrice,
			CachedReadPrice: w.CachedRead, CachedWritePrice: w.CachedWrite,
		})
	}
	out := make([]*RateVersionView, 0, len(versions))
	for _, v := range versions {
		out = append(out, &RateVersionView{
			ID: v.ID, ProviderID: v.ProviderID, Currency: v.Currency, Timezone: v.Timezone,
			EffectiveFrom: v.EffectiveFrom, EffectiveUntil: v.EffectiveUntil,
			Version: v.Version, Windows: byVersion[v.ID], CreatedAt: v.CreatedAt,
		})
	}
	return out, nil
}

// EffectiveRate is the rate selected at a call timestamp (source hierarchy: Provider version
// window > legacy global price > unconfigured).
type EffectiveRate struct {
	Source                string // provider_version|global_settings|none
	VersionID             string
	WindowID              string
	Currency              string
	InputPriceMicro       int64
	OutputPriceMicro      int64
	CachedReadPriceMicro  *int64
	CachedWritePriceMicro *int64
}

// SelectRateAt selects the effective rate at the request timestamp (Price-02: version effective interval ->
// minute of day in version timezone -> window; no version / no matching window -> legacy global price
// compatible default with source marker; global dual-0 = unconfigured).
func (s *Services) SelectRateAt(ctx context.Context, db *gorm.DB, providerID string, at time.Time) (EffectiveRate, error) {
	var version struct {
		ID       string
		Currency string
		Timezone string
	}
	err := db.WithContext(ctx).Raw(`
		select id::text as id, currency, timezone from ai_provider_rate_versions
		where provider_id = ? and effective_from <= ? and (effective_until is null or effective_until > ?)
		order by effective_from desc, version desc limit 1`, providerID, at, at).Scan(&version).Error
	if err != nil {
		return EffectiveRate{}, err
	}
	if version.ID != "" {
		tz, tzErr := time.LoadLocation(version.Timezone)
		if tzErr == nil {
			local := at.In(tz)
			minute := local.Hour()*60 + local.Minute()
			var windows []struct {
				ID          string
				StartMinute int
				EndMinute   int
				InputPrice  string
				OutputPrice string
				CachedRead  *string
				CachedWrite *string
			}
			if err := db.WithContext(ctx).Raw(`
				select id::text as id, start_minute, end_minute,
				       input_price::text as input_price, output_price::text as output_price,
				       cached_read_price::text as cached_read, cached_write_price::text as cached_write
				from ai_provider_rate_windows where rate_version_id = ?
				order by start_minute`, version.ID).Scan(&windows).Error; err != nil {
				return EffectiveRate{}, err
			}
			for _, w := range windows {
				if minute >= w.StartMinute && minute < w.EndMinute {
					rate := EffectiveRate{
						Source: RateSourceProviderVersion, VersionID: version.ID, WindowID: w.ID,
						Currency: version.Currency,
					}
					if r, err := parseNumericMicro(w.InputPrice); err == nil {
						rate.InputPriceMicro = r
					}
					if r, err := parseNumericMicro(w.OutputPrice); err == nil {
						rate.OutputPriceMicro = r
					}
					if w.CachedRead != nil {
						if r, err := parseNumericMicro(*w.CachedRead); err == nil {
							rate.CachedReadPriceMicro = &r
						}
					}
					if w.CachedWrite != nil {
						if r, err := parseNumericMicro(*w.CachedWrite); err == nil {
							rate.CachedWritePriceMicro = &r
						}
					}
					return rate, nil
				}
			}
		}
		// Version exists but has no window covering this minute (configuration gap): degrade as unconfigured rather than fabricating a rate.
	}
	return s.legacyGlobalRate(ctx, db)
}

// legacyGlobalRate returns the legacy global price compatible default (settings ai-budget dual-0 = unconfigured;
// marked with source global_settings, and since cache prices do not exist there, reported cache details
// can only produce a partial calculation). R1 H-1: settings fields are denominated in minor currency
// units per million Tokens (*_minor, the budget settlement convention), whereas prices in this file are
// uniformly currency micro-units (x 1e6), converting via x 1e4 (1 minor = 1e-2 currency unit = 1e4 micro)
// with overflow capping against extreme configurations.
func (s *Services) legacyGlobalRate(ctx context.Context, db *gorm.DB) (EffectiveRate, error) {
	settings, err := s.LoadBudgetSettings(ctx, db)
	if err != nil {
		return EffectiveRate{}, err
	}
	if settings.InputPricePerMillionMinor <= 0 && settings.OutputPricePerMillionMinor <= 0 {
		return EffectiveRate{Source: RateSourceNone}, nil
	}
	return EffectiveRate{
		Source:           RateSourceGlobalSettings,
		Currency:         settings.Currency,
		InputPriceMicro:  minorToMicro(settings.InputPricePerMillionMinor),
		OutputPriceMicro: minorToMicro(settings.OutputPricePerMillionMinor),
	}, nil
}

// minorToMicro converts minor currency units / million -> currency micro-units / million (x 1e4, checked cap).
func minorToMicro(minor int64) int64 {
	if minor <= 0 {
		return 0
	}
	return mulCeil(minor, 10_000)
}

// parseNumericMicro parses PG numeric text (which may have trailing zeros or be an integer without a decimal point).
func parseNumericMicro(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty numeric")
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(fracPart) > 6 {
		fracPart = fracPart[:6] // numeric(18,6) will not exceed this; defensive truncation
	}
	if fracPart == "" {
		return parseDecimalMicro(intPart + ".0")
	}
	return parseDecimalMicro(intPart + "." + fracPart)
}

// ReferenceCost holds the reference calculation result (fixed-point nano-unit amount + calculation basis + missing notes).
type ReferenceCost struct {
	AmountNano *big.Int // Currency nano-units (x 1e8); nil = uncalculable
	Basis      string   // full_detail|partial_detail|base_only|not_computable
	Note       string
}

// ComputeReferenceCost implements the section 4.3 reference formula: base = I*Pu + O*Po; each cache
// category that is both reported and priced appends a quantity * (categoryPrice - Pu) adjustment
// (I already includes R/W, avoiding double-charging standard input); reasoning is recorded as detail only
// without extra charge. Uses big.Int fixed-point arithmetic throughout with half-up rounding at the nano-unit scale (reproducible).
func ComputeReferenceCost(detail *provider.UsageDetail, rate EffectiveRate) ReferenceCost {
	if detail == nil {
		return ReferenceCost{Basis: CalcBasisNotComputable, Note: "usage_not_reported"}
	}
	if detail.InputTokens == nil || detail.OutputTokens == nil {
		// When total usage is absent, do not falsely report even base Token cost as computed (section 4.2).
		return ReferenceCost{Basis: CalcBasisNotComputable, Note: "usage_totals_unknown"}
	}
	if rate.Source == RateSourceNone {
		return ReferenceCost{Basis: CalcBasisNotComputable, Note: "no_rate_configured"}
	}
	input, output := *detail.InputTokens, *detail.OutputTokens
	var cachedRead, cachedWrite int64
	cacheReported := false
	if detail.CachedTokens != nil {
		cachedRead = *detail.CachedTokens
		cacheReported = true
	}
	if detail.CacheWriteTokens != nil {
		cachedWrite = *detail.CacheWriteTokens
		cacheReported = true
	}
	// Relational validity: the sum of mutually exclusive sub-items cannot exceed total input; on violation ->
	// discard cache sub-items and record a note.
	if cacheReported && (cachedRead < 0 || cachedWrite < 0 || cachedRead+cachedWrite > input) {
		return ReferenceCost{
			AmountNano: baseReferenceAmount(input, output, rate),
			Basis:      CalcBasisBaseOnly, Note: "cache_detail_exceeds_input_total",
		}
	}
	amount := baseReferenceAmount(input, output, rate)
	if !cacheReported {
		// Cache fields completely absent: retain base reference cost and note that cache impact was not computed (Cache-03).
		return ReferenceCost{AmountNano: amount, Basis: CalcBasisBaseOnly, Note: "no_cache_detail_reported"}
	}
	basis := CalcBasisFullDetail
	note := ""
	if detail.CachedTokens != nil {
		if rate.CachedReadPriceMicro == nil {
			basis = CalcBasisPartialDetail
			note = "reported_category_price_unconfigured"
		} else {
			amount.Add(amount, adjustAmount(cachedRead, *rate.CachedReadPriceMicro, rate.InputPriceMicro))
		}
	}
	if detail.CacheWriteTokens != nil {
		if rate.CachedWritePriceMicro == nil {
			basis = CalcBasisPartialDetail
			note = "reported_category_price_unconfigured"
		} else {
			amount.Add(amount, adjustAmount(cachedWrite, *rate.CachedWritePriceMicro, rate.InputPriceMicro))
		}
	}
	return ReferenceCost{AmountNano: amount, Basis: basis, Note: note}
}

// baseReferenceAmount computes base reference = I*Pu + O*Po (in currency nano-units).
// amountNano = tokens * priceMicro * 1e8 / 1e6 / 1e6 = tokens * priceMicro / 1e4.
func baseReferenceAmount(input, output int64, rate EffectiveRate) *big.Int {
	total := new(big.Int)
	total.Add(total, tokensTimesPrice(input, rate.InputPriceMicro))
	total.Add(total, tokensTimesPrice(output, rate.OutputPriceMicro))
	return total
}

// adjustAmount computes cache category adjustment = tokens * (categoryPrice - Pu) (may be negative = cache discount;
// tokensTimesPrice preserves sign for negative price deltas).
func adjustAmount(tokens int64, categoryPrice, inputPrice int64) *big.Int {
	delta := categoryPrice - inputPrice
	if delta == 0 || tokens == 0 {
		return new(big.Int)
	}
	return tokensTimesPrice(tokens, delta)
}

// tokensTimesPrice performs fixed-point multiply-divide: tokens * priceMicro / 1e4, rounded half-up.
func tokensTimesPrice(tokens int64, priceMicro int64) *big.Int {
	x := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(priceMicro))
	return roundDiv(x, 10000)
}

// roundDiv performs integer division with half-up rounding (rounds away from zero for negative numbers, reproducible).
func roundDiv(x *big.Int, d int64) *big.Int {
	q, r := new(big.Int).QuoRem(x, big.NewInt(d), new(big.Int))
	// Round away from zero by 1 when |2r| >= d.
	twice := new(big.Int).Lsh(r, 1)
	abs := new(big.Int).Abs(twice)
	if abs.Cmp(big.NewInt(d)) >= 0 {
		if x.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	return q
}

// RecordCallUsage writes a per-HTTP-call reference ledger row (append-only; includes reported usage
// from failed calls). Amount and calculation basis are frozen using the rate at the call timestamp; history is never rewritten.
func (s *Services) RecordCallUsage(ctx context.Context, db *gorm.DB, runID string,
	stagePosition int32, modelTurn, callIndex int, rec provider.CallRecord) error {
	rate, err := s.SelectRateAt(ctx, db, rec.ProviderID, rec.SentAt)
	if err != nil {
		return err
	}
	cost := ComputeReferenceCost(rec.Detail, rate)
	var amount any // numeric(24,8) text; nil = uncalculable
	if cost.AmountNano != nil {
		amount = formatNanoAmount(cost.AmountNano)
	}
	usageReported := rec.Detail != nil
	inTok, outTok, cached, cacheWrite, reasoning := nullableCount(rec.Detail)
	// R1 M-2 (Usage-03 idempotency): call event identity = (run, stage, turn, index, sent_at).
	// Replaying the exact same event (multi-replica / post-crash reprocessing of the same in-memory record)
	// skips on conflict; a re-executed turn after a crash is a genuine new call (new sent_at) -> booked
	// normally without loss, consistent with actual-call accounting in the budget ledger.
	return db.WithContext(ctx).Exec(`
		insert into ai_call_usage (id, review_run_id, stage_position, model_turn, call_index,
			provider_id, model_name, sent_at, reasoning_effort, max_tokens, temperature,
			rate_version_id, rate_window_id, rate_source, currency,
			input_tokens, output_tokens, cached_tokens, cache_write_tokens, reasoning_tokens,
			usage_reported, reference_amount, calc_basis, detail_note, outcome, http_status, created_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict (review_run_id, stage_position, model_turn, call_index, sent_at) do nothing`,
		newID(), runID, stagePosition, modelTurn, callIndex,
		rec.ProviderID, rec.ModelName, rec.SentAt,
		nullableString(rec.ReasoningEffort), nullableInt(rec.MaxTokens), nullableTemperature(rec.Temperature),
		nullableUUID(rate.VersionID), nullableUUID(rate.WindowID), rate.Source, nullableCurrency(rate),
		inTok, outTok, cached, cacheWrite, reasoning,
		usageReported, amount, cost.Basis, cost.Note, rec.Outcome, nullableHTTPStatus(rec.HTTPStatus), nowUTC()).Error
}

// nullableCount extracts a nullable count value from tri-state detail (passing nil through as NULL).
func nullableCount(d *provider.UsageDetail) (in, out, cached, cacheWrite, reasoning any) {
	if d == nil {
		return nil, nil, nil, nil, nil
	}
	return ptrAny(d.InputTokens), ptrAny(d.OutputTokens), ptrAny(d.CachedTokens),
		ptrAny(d.CacheWriteTokens), ptrAny(d.ReasoningTokens)
}

func ptrAny(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableTemperature(f *float64) any {
	if f == nil {
		return nil
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// nullableHTTPStatus R1 C-1: HTTPStatus=0 indicates no response was received (network error /
// timeout / cancellation) — persisted as NULL (permitted by CHECK) rather than writing 0 and violating the constraint.
func nullableHTTPStatus(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func nullableCurrency(rate EffectiveRate) any {
	if rate.Currency == "" {
		return nil
	}
	return rate.Currency
}

func derefTime(t *time.Time, fallback time.Time) time.Time {
	if t == nil {
		return fallback
	}
	return *t
}

func maxTime() time.Time {
	return time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
}

func derefPrice(p *int64) any {
	if p == nil {
		return nil
	}
	return formatMicroDecimal(*p)
}

func derefPriceString(p *int64) *string {
	if p == nil {
		return nil
	}
	v := formatMicroDecimal(*p)
	return &v
}

// CallUsageView is the per-call detail read view (admin surface; passes tri-state nullable fields through).
type CallUsageView struct {
	ID               string    `json:"id"`
	ReviewRunID      string    `json:"review_run_id"`
	StagePosition    int32     `json:"stage_position"`
	ModelTurn        int       `json:"model_turn"`
	CallIndex        int       `json:"call_index"`
	ProviderID       string    `json:"provider_id"`
	ModelName        string    `json:"model_name"`
	SentAt           time.Time `json:"sent_at"`
	ReasoningEffort  *string   `json:"reasoning_effort,omitempty"`
	MaxTokens        *int64    `json:"max_tokens,omitempty"`
	Temperature      *string   `json:"temperature,omitempty"`
	RateVersionID    *string   `json:"rate_version_id,omitempty"`
	RateWindowID     *string   `json:"rate_window_id,omitempty"`
	RateSource       string    `json:"rate_source"`
	Currency         *string   `json:"currency,omitempty"`
	InputTokens      *int64    `json:"input_tokens,omitempty"`
	OutputTokens     *int64    `json:"output_tokens,omitempty"`
	CachedTokens     *int64    `json:"cached_tokens,omitempty"`
	CacheWriteTokens *int64    `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  *int64    `json:"reasoning_tokens,omitempty"`
	UsageReported    bool      `json:"usage_reported"`
	ReferenceAmount  *string   `json:"reference_amount,omitempty"`
	CalcBasis        string    `json:"calc_basis"`
	DetailNote       string    `json:"detail_note"`
	Outcome          string    `json:"outcome"`
	HTTPStatus       *int      `json:"http_status,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ListCallUsage paginates per-call details (sent_at + id cursor; provider_id / run_id /
// currency filters; provider_id added per RCP-20261102-S008).
func (s *Services) ListCallUsage(ctx context.Context, limit int, after, runID, providerID, currency string) ([]*CallUsageView, *string, error) {
	q := s.DB.WithContext(ctx).Table("ai_call_usage")
	if after != "" {
		if sent, id, ok := splitCursor(after); ok {
			q = q.Where("(sent_at, id) < (?, ?)", sent, id)
		}
	}
	if runID != "" {
		q = q.Where("review_run_id = ?", runID)
	}
	if providerID != "" {
		q = q.Where("provider_id = ?", providerID)
	}
	if currency != "" {
		q = q.Where("currency = ?", currency)
	}
	var rows []struct {
		ID               string
		ReviewRunID      string
		StagePosition    int32
		ModelTurn        int
		CallIndex        int
		ProviderID       string
		ModelName        string
		SentAt           time.Time
		ReasoningEffort  *string
		MaxTokens        *int64
		Temperature      *string
		RateVersionID    *string
		RateWindowID     *string
		RateSource       string
		Currency         *string
		InputTokens      *int64
		OutputTokens     *int64
		CachedTokens     *int64
		CacheWriteTokens *int64
		ReasoningTokens  *int64
		UsageReported    bool
		ReferenceAmount  *string
		CalcBasis        string
		DetailNote       string
		Outcome          string
		HTTPStatus       *int
		CreatedAt        time.Time
	}
	if err := q.Select("id::text as id, review_run_id::text as review_run_id, stage_position, model_turn, call_index, " +
		"provider_id::text as provider_id, model_name, sent_at, reasoning_effort, max_tokens, temperature::text as temperature, " +
		"rate_version_id::text as rate_version_id, rate_window_id::text as rate_window_id, rate_source, currency::text as currency, " +
		"input_tokens, output_tokens, cached_tokens, cache_write_tokens, reasoning_tokens, usage_reported, " +
		"reference_amount::text as reference_amount, calc_basis, detail_note, outcome, http_status, created_at").
		Order("sent_at desc").Order("id desc").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		v := rows[len(rows)-1].SentAt.UTC().Format("2006-01-02T15:04:05.999999Z") + "|" + rows[len(rows)-1].ID
		next = &v
	}
	out := make([]*CallUsageView, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		view := &CallUsageView{
			ID: r.ID, ReviewRunID: r.ReviewRunID, StagePosition: r.StagePosition,
			ModelTurn: r.ModelTurn, CallIndex: r.CallIndex,
			ProviderID: r.ProviderID, ModelName: r.ModelName, SentAt: r.SentAt,
			ReasoningEffort: r.ReasoningEffort, MaxTokens: r.MaxTokens, Temperature: r.Temperature,
			RateVersionID: r.RateVersionID, RateWindowID: r.RateWindowID,
			RateSource: r.RateSource, Currency: r.Currency,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
			CachedTokens: r.CachedTokens, CacheWriteTokens: r.CacheWriteTokens,
			ReasoningTokens: r.ReasoningTokens, UsageReported: r.UsageReported,
			ReferenceAmount: r.ReferenceAmount, CalcBasis: r.CalcBasis, DetailNote: r.DetailNote,
			Outcome: r.Outcome, HTTPStatus: r.HTTPStatus, CreatedAt: r.CreatedAt,
		}
		out = append(out, view)
	}
	return out, next, nil
}

// UsageSummaryItem aggregates usage by currency (reference convention: amount is the fixed-point sum;
// hit-rate denominator counts only known samples — missing values are neither treated as 0 nor averaged as percentages, Cache-06).
type UsageSummaryItem struct {
	Currency            string `json:"currency"`
	Calls               int64  `json:"calls"`
	ReferenceAmountNano string `json:"reference_amount"` // Fixed-point decimal string (in currency units)
	ComputableCalls     int64  `json:"computable_calls"` // Number of calls with non-null reference_amount.
	// InputTokensKnown is the sum of all known input tokens; Cache-06 hit rate
	// = CachedTokensKnown / InputTokensCacheKnown — the denominator counts only calls that reported
	// cache details, neither treating missing samples as 0 nor averaging percentages.
	InputTokensKnown      int64 `json:"input_tokens_known"`
	InputTokensCacheKnown int64 `json:"input_tokens_cache_known"`
	CachedTokensKnown     int64 `json:"cached_tokens_known"`
	CacheDetailSamples    int64 `json:"cache_detail_samples"` // Number of calls that reported cache details (numeric fields do not count calls)
}

// SummarizeCallUsage aggregates usage by currency within a time window (from..to half-open interval;
// multiple currencies are tallied separately without FX conversion).
func (s *Services) SummarizeCallUsage(ctx context.Context, from, to time.Time) ([]*UsageSummaryItem, error) {
	var rows []struct {
		Currency              *string
		Calls                 int64
		AmountSum             *string
		ComputableCalls       int64
		InputTokensKnown      int64
		InputTokensCacheKnown int64
		CachedKnown           int64
		CacheSamples          int64
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select currency::text as currency,
		       count(*) as calls,
		       sum(reference_amount)::text as amount_sum,
		       count(reference_amount) as computable_calls,
		       coalesce(sum(input_tokens), 0) as input_tokens_known,
		       coalesce(sum(input_tokens) filter (where cached_tokens is not null or cache_write_tokens is not null), 0) as input_tokens_cache_known,
		       coalesce(sum(cached_tokens), 0) as cached_known,
		       count(*) filter (where cached_tokens is not null or cache_write_tokens is not null) as cache_samples
		from ai_call_usage
		where sent_at >= ? and sent_at < ? and currency is not null
		group by currency order by currency`, from, to).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*UsageSummaryItem, 0, len(rows))
	for _, r := range rows {
		currency := ""
		if r.Currency != nil {
			currency = *r.Currency
		}
		item := &UsageSummaryItem{
			Currency: currency, Calls: r.Calls,
			ComputableCalls:       r.ComputableCalls,
			InputTokensKnown:      r.InputTokensKnown,
			InputTokensCacheKnown: r.InputTokensCacheKnown,
			CachedTokensKnown:     r.CachedKnown,
			CacheDetailSamples:    r.CacheSamples,
		}
		if r.AmountSum != nil {
			item.ReferenceAmountNano = normalizeAmountText(*r.AmountSum)
		}
		out = append(out, item)
	}
	return out, nil
}

// normalizeAmountText formats PG numeric sum text -> canonical 8-decimal-place string (empty string stays empty).
func normalizeAmountText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, fracPart := s, "0"
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	fracPart = (fracPart + strings.Repeat("0", 8))[:8]
	whole := new(big.Int)
	if _, ok := whole.SetString(intPart, 10); !ok {
		return s
	}
	x := whole.Mul(whole, big.NewInt(amountScale))
	frac := new(big.Int)
	if _, ok := frac.SetString(fracPart, 10); ok {
		x.Add(x, frac)
	}
	out := formatNanoAmount(x)
	if neg {
		return "-" + out
	}
	return out
}
