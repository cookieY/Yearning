// pricing.go S008 reference pricing: Provider rate versions + time-of-day windows, fixed-point calculation,
// per-HTTP-call ledger, and budget currency consistency (CNY/USD, int64 micro-units + big.Int, tri-state usage).
// Historical ledger rows are immutable; budget reservation/settlement (budget.go) stays independent.
package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"gorm.io/gorm"
	"math/big"
	"regexp"
	"time"
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
			if txutil.IsUniqueViolation(err) {
				return &domain.Error{Code: 1005, Name: "RESOURCE_ALREADY_EXISTS",
					Detail: "concurrent rate version creation; retry"}
			}
			return err
		}
		view := &RateVersionView{
			ID: id, ProviderID: in.ProviderID, Currency: in.Currency, Timezone: in.Timezone,
			EffectiveFrom: in.EffectiveFrom, EffectiveUntil: in.EffectiveUntil,
			Version: nextVersion, Windows: make([]RateWindowView, 0, len(windows)), CreatedAt: now,
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
			view.Windows = append(view.Windows, RateWindowView{
				ID: wid, StartMinute: w.StartMinute, EndMinute: w.EndMinute,
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

// ReferenceCost holds the reference calculation result (fixed-point nano-unit amount + calculation basis + missing notes).
type ReferenceCost struct {
	AmountNano *big.Int // Currency nano-units (x 1e8); nil = uncalculable
	Basis      string   // full_detail|partial_detail|base_only|not_computable
	Note       string
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
