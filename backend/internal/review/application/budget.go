// budget.go Global daily budget: atomic reservation + append-only ledger (budget_and_capacity).
//
// Rules: reservation happens before sending to the Provider (single-row UPDATE atomic check,
// never overspending under concurrency); actual usage is appended to the ledger and settled after
// receiving the response or known cost; exhaustion transitions the Run to partial/budget_exhausted (2010),
// blocking submission. Only a global daily budget exists (ai_budget namespace), with no per-order budget.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

// ErrBudgetServiceUnavailable is returned when daily AI budget enforcement cannot run because
// the review budget service or metadata DB handle is unconfigured (D14 fail-closed invariant).
var ErrBudgetServiceUnavailable = errors.New("review: daily budget service unavailable")

// BudgetSettings is the frozen read result of the ai_budget namespace.
type BudgetSettings struct {
	Enforced              bool
	Currency              string
	DailyBudgetMinor      int64
	AlertThresholdPercent int
	// OBS-002: token pricing unit rates (minor currency units per million tokens); both 0 = fixed estimate.
	InputPricePerMillionMinor  int64
	OutputPricePerMillionMinor int64
}

// ErrBudgetExhausted 2010 (fail closed).
var ErrBudgetExhausted = &domain.Error{Code: domain.CodeAIBudgetExhausted, Name: "AI_BUDGET_EXHAUSTED"}

// defaultBudgetSettings matches the defaults in settings-namespaces.json.
func defaultBudgetSettings() BudgetSettings {
	return BudgetSettings{Enforced: false, Currency: "USD", DailyBudgetMinor: 10000,
		AlertThresholdPercent: 80, InputPricePerMillionMinor: 0, OutputPricePerMillionMinor: 0}
}

// LoadBudgetSettings reads the ai_budget namespace (fault-tolerant defaults: missing fields use default values).
func (s *Services) LoadBudgetSettings(ctx context.Context, db *gorm.DB) (BudgetSettings, error) {
	var rawStr *string
	err := db.WithContext(ctx).Raw(
		`select settings::text from system_settings where namespace = 'ai_budget'`).Scan(&rawStr).Error
	if err != nil {
		return BudgetSettings{}, err
	}
	out := defaultBudgetSettings()
	if rawStr == nil || *rawStr == "" {
		return out, nil
	}
	raw := []byte(*rawStr)
	doc := struct {
		Enforced                   *bool   `json:"enforced"`
		Currency                   *string `json:"currency"`
		DailyBudgetMinor           *int64  `json:"daily_budget_minor"`
		AlertThresholdPercent      *int    `json:"alert_threshold_percent"`
		InputPricePerMillionMinor  *int64  `json:"input_price_per_million_minor"`
		OutputPricePerMillionMinor *int64  `json:"output_price_per_million_minor"`
	}{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out, nil // Corrupted configuration falls back to defaults; runtime execution remains protected by reservation caps
	}
	if doc.Enforced != nil {
		out.Enforced = *doc.Enforced
	}
	if doc.Currency != nil && len(*doc.Currency) == 3 {
		out.Currency = *doc.Currency
	}
	if doc.DailyBudgetMinor != nil && *doc.DailyBudgetMinor > 0 {
		out.DailyBudgetMinor = *doc.DailyBudgetMinor
	}
	if doc.AlertThresholdPercent != nil {
		out.AlertThresholdPercent = *doc.AlertThresholdPercent
	}
	if doc.InputPricePerMillionMinor != nil && *doc.InputPricePerMillionMinor >= 0 {
		out.InputPricePerMillionMinor = *doc.InputPricePerMillionMinor
	}
	if doc.OutputPricePerMillionMinor != nil && *doc.OutputPricePerMillionMinor >= 0 {
		out.OutputPricePerMillionMinor = *doc.OutputPricePerMillionMinor
	}
	return out, nil
}

// budgetDayWithDB resolves the budget calendar date under general.system_timezone using db
// (falling back to s.DB when db is nil; defaults to Asia/Shanghai on missing/corrupted config).
func (s *Services) budgetDayWithDB(ctx context.Context, db *gorm.DB, now time.Time) string {
	if db == nil && s != nil {
		db = s.DB
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return systz.BusinessDay(now, systz.Location(ctx, db))
}

// budgetDay resets daily at 00:00 in the system settings timezone (general.system_timezone;
// defaults to Asia/Shanghai; corrupted configuration falls back to default).
func (s *Services) budgetDay(now time.Time) string {
	return s.budgetDayWithDB(context.Background(), nil, now)
}

// BudgetDay resolves the current budget calendar date under general.system_timezone.
func (s *Services) BudgetDay(now time.Time) string { return s.budgetDay(now) }

// Reserve performs an atomic reservation: single-row conditional UPDATE (amount + reserved <= budget),
// with version CAS retry handled by row-lock semantics; failure = budget exhausted.
func (s *Services) Reserve(ctx context.Context, db *gorm.DB, day, currency string, settings BudgetSettings, amountMinor int64) error {
	if !settings.Enforced || amountMinor <= 0 {
		return nil
	}
	res := db.WithContext(ctx).Exec(`
		insert into ai_daily_budget_usage (budget_day, currency, amount_minor, reserved_minor, version, updated_at)
		values (?, ?, 0, ?, 1, ?)
		on conflict (budget_day, currency) do update
		  set reserved_minor = ai_daily_budget_usage.reserved_minor + excluded.reserved_minor,
		      version = ai_daily_budget_usage.version + 1,
		      updated_at = excluded.updated_at
		where ai_daily_budget_usage.amount_minor + ai_daily_budget_usage.reserved_minor + excluded.reserved_minor <= ?`,
		day, currency, amountMinor, nowUTC(), settings.DailyBudgetMinor)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrBudgetExhausted
	}
	return nil
}

// Release releases a reservation (when reservation is unconsumed: failure / cancellation paths).
func (s *Services) Release(ctx context.Context, db *gorm.DB, day, currency string, amountMinor int64) error {
	if amountMinor <= 0 {
		return nil
	}
	return db.WithContext(ctx).Exec(`
		update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor - ?, 0), version = version + 1, updated_at = ?
		where budget_day = ? and currency = ?`, amountMinor, nowUTC(), day, currency).Error
}

// Settle settles usage: appends ledger row + books incurred cost + releases reservation (wrapped in a single transaction by the caller).
func (s *Services) Settle(ctx context.Context, tx *gorm.DB, runID, providerID, modelName string,
	attempt int16, inputTokens, outputTokens, amountMinor int64, currency, day, outcome string) error {
	if err := tx.WithContext(ctx).Exec(`
		insert into ai_usage_ledger (id, review_run_id, provider_id, model_name, attempt_number,
			input_token_count, output_token_count, amount_minor, currency, budget_day, outcome, created_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), runID, providerID, modelName, attempt,
		inputTokens, outputTokens, amountMinor, currency, day, outcome, nowUTC()).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		insert into ai_daily_budget_usage (budget_day, currency, amount_minor, reserved_minor, version, updated_at)
		values (?, ?, ?, 0, 1, ?)
		on conflict (budget_day, currency) do update
		  set amount_minor = ai_daily_budget_usage.amount_minor + excluded.amount_minor,
		      reserved_minor = greatest(ai_daily_budget_usage.reserved_minor - excluded.amount_minor, 0),
		      version = ai_daily_budget_usage.version + 1,
		      updated_at = excluded.updated_at`,
		day, currency, amountMinor, nowUTC()).Error
}

// budgetDayOf is exposed for tests.
func (s *Services) budgetDayOf(now time.Time) string { return s.budgetDay(now) }

// ReconcileDailyUsage performs startup reconciliation (M5): recomputes today's incurred cost from
// append-only ledger facts and zeroes out orphaned reservations (leaked by crashes between Reserve and Settle).
// In-flight call reservations are briefly undercounted — bounded above by the per-stage call cap.
func (s *Services) ReconcileDailyUsage(ctx context.Context, db *gorm.DB, currency string) error {
	today := s.budgetDayWithDB(ctx, db, time.Now())
	return db.WithContext(ctx).Exec(`
		update ai_daily_budget_usage u
		set amount_minor = coalesce((
		        select sum(l.amount_minor) from ai_usage_ledger l
		        where l.budget_day = u.budget_day and l.currency = u.currency), 0),
		    reserved_minor = 0,
		    version = u.version + 1,
		    updated_at = ?
		where u.budget_day = ? and u.currency = ?`,
		nowUTC(), today, currency).Error
}

// BudgetLease represents a reserved daily AI budget allocation shared across AI surfaces
// (review, assistant, diagnostics). Finalize via either Settle (books actual usage and releases
// only unused excess reservedMinor - actualMinor) or Release (full release on abort/failure).
type BudgetLease struct {
	svc           *Services
	db            *gorm.DB
	day           string
	currency      string
	reservedMinor int64
	settings      BudgetSettings
	finalized     bool
}

// NewBudgetLease constructs a BudgetLease from explicit reservation parameters (for tests or
// callers holding an already-reserved allocation).
func NewBudgetLease(svc *Services, db *gorm.DB, day, currency string, reservedMinor int64, settings BudgetSettings) *BudgetLease {
	return &BudgetLease{
		svc:           svc,
		db:            db,
		day:           day,
		currency:      currency,
		reservedMinor: reservedMinor,
		settings:      settings,
	}
}

// AcquireBudgetLease loads ai_budget settings, resolves the business day under general.system_timezone,
// and atomically reserves estimated minor units when enforced. Fails closed with ErrBudgetServiceUnavailable
// whenever s == nil or no database handle is available.
func (s *Services) AcquireBudgetLease(ctx context.Context, db *gorm.DB, estTokens int64) (*BudgetLease, error) {
	if s == nil {
		return nil, ErrBudgetServiceUnavailable
	}
	if db == nil {
		db = s.DB
	}
	if db == nil {
		return nil, ErrBudgetServiceUnavailable
	}
	settings, err := s.LoadBudgetSettings(ctx, db)
	if err != nil {
		return nil, err
	}
	lease := &BudgetLease{svc: s, db: db, settings: settings}
	if !settings.Enforced {
		return lease, nil
	}
	amountMinor := int64(1)
	if settings.InputPricePerMillionMinor > 0 {
		if calc := (estTokens * settings.InputPricePerMillionMinor) / 1_000_000; calc > amountMinor {
			amountMinor = calc
		}
	}
	day := s.budgetDayWithDB(ctx, db, time.Now())
	if err := s.Reserve(ctx, db, day, settings.Currency, settings, amountMinor); err != nil {
		return nil, err
	}
	lease.day = day
	lease.currency = settings.Currency
	lease.reservedMinor = amountMinor
	return lease, nil
}

// Day returns the budget business day under general.system_timezone (empty when unenforced).
func (l *BudgetLease) Day() string {
	if l == nil {
		return ""
	}
	return l.day
}

// Currency returns the reserved currency code (empty when unenforced).
func (l *BudgetLease) Currency() string {
	if l == nil {
		return ""
	}
	return l.currency
}

// ReservedMinor returns the reserved minor units (0 when unenforced).
func (l *BudgetLease) ReservedMinor() int64 {
	if l == nil {
		return 0
	}
	return l.reservedMinor
}

// Settings returns the budget settings snapshot captured at reservation time.
func (l *BudgetLease) Settings() BudgetSettings {
	if l == nil {
		return BudgetSettings{}
	}
	return l.settings
}

// Settle atomically records actual usage into ai_usage_ledger + ai_daily_budget_usage and releases
// ONLY the unused excess (reservedMinor - actualMinor). If the settlement transaction fails, the
// full reservation is released outside the rolled-back transaction so reserved_minor never leaks.
func (l *BudgetLease) Settle(
	ctx context.Context,
	runID, providerID, modelName, defaultModel string,
	inputTokens, outputTokens int64,
	outcome string,
) error {
	if l == nil || l.finalized {
		return nil
	}
	if !l.settings.Enforced || l.reservedMinor <= 0 || l.day == "" {
		l.finalized = true
		return nil
	}
	if l.svc == nil || l.db == nil {
		return ErrBudgetServiceUnavailable
	}
	resolvedProviderID := uuid.Nil.String()
	if parsed, err := uuid.Parse(strings.TrimSpace(providerID)); err == nil {
		resolvedProviderID = parsed.String()
	}
	resolvedModel := strings.TrimSpace(modelName)
	if resolvedModel == "" {
		resolvedModel = defaultModel
	}
	actualMinor := TokenBasedAmount(
		inputTokens, outputTokens,
		l.settings.InputPricePerMillionMinor, l.settings.OutputPricePerMillionMinor,
		l.reservedMinor,
	)
	settleCtx := context.WithoutCancel(ctx)
	err := l.db.WithContext(settleCtx).Transaction(func(tx *gorm.DB) error {
		if err := l.svc.Settle(
			settleCtx, tx, runID, resolvedProviderID, resolvedModel,
			1, inputTokens, outputTokens, actualMinor, l.currency, l.day, outcome,
		); err != nil {
			return err
		}
		if rem := l.reservedMinor - actualMinor; rem > 0 {
			return l.svc.Release(settleCtx, tx, l.day, l.currency, rem)
		}
		return nil
	})
	l.finalized = true
	if err != nil {
		_ = l.svc.Release(settleCtx, l.db, l.day, l.currency, l.reservedMinor)
		return err
	}
	return nil
}

// Release releases the full unconsumed reservation on abort or provider failure. Idempotent after Settle or Release.
func (l *BudgetLease) Release(ctx context.Context) error {
	if l == nil || l.finalized {
		return nil
	}
	l.finalized = true
	if l.reservedMinor <= 0 || l.day == "" {
		return nil
	}
	if l.svc == nil || l.db == nil {
		return ErrBudgetServiceUnavailable
	}
	return l.svc.Release(context.WithoutCancel(ctx), l.db, l.day, l.currency, l.reservedMinor)
}
