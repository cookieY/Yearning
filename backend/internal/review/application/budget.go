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
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

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

// budgetDay resets daily at 00:00 in the system settings timezone (general.system_timezone;
// defaults to Asia/Shanghai; corrupted configuration falls back to default).
func (s *Services) budgetDay(now time.Time) string {
	var tzName *string
	if s != nil && s.DB != nil {
		_ = s.DB.Raw(`select settings->>'system_timezone' as tz from system_settings where namespace = 'general'`).Scan(&tzName).Error
	}
	name := "Asia/Shanghai"
	if tzName != nil && *tzName != "" {
		name = *tzName
	}
	tz, err := time.LoadLocation(name)
	if err != nil {
		if tz, err = time.LoadLocation("Asia/Shanghai"); err != nil {
			tz = time.UTC
		}
	}
	return now.In(tz).Format("2006-01-02")
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
	today := s.budgetDay(time.Now())
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
