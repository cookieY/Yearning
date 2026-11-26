//go:build integration

// pricing_integration_test.go S008 acceptance gates (real PostgreSQL@54330,
// migration 000022 applied):
//
//	P1 Rate version CRUD: monotonic version numbers, overlapping effective interval rejection, cross-midnight
//	   window normalization into two rows, and budget currency consistency (save side + enable side);
//	P2 Rate selection: version effective interval boundaries, minute-of-day matching in explicit IANA
//	   timezones (including DST summer/winter wall-clock differences at the same UTC instant), no version /
//	   no window -> legacy global price compatible default (with source marker), global dual-0 = none;
//	P3 Per-call ledger: tri-state nullable columns, rate version/window binding, parameter snapshots,
//	   detail/summary read surfaces (currencies separated, hit-rate sample denominator);
//	P4 Provider read-back: explicit empty override clears user parameters (Param-04 full flow).
package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func truncatePricingTables(t *testing.T, f *fx) {
	t.Helper()
	for _, tbl := range []string{"ai_call_usage", "ai_provider_rate_windows", "ai_provider_rate_versions",
		"rule_set_prompt_tools", "ai_providers", "ai_usage_ledger", "ai_daily_budget_usage", "system_settings"} {
		if err := f.db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
}

func seedPricingProvider(t *testing.T, f *fx, enabled bool) string {
	return seedPricingProviderNamed(t, f, enabled, "pricing-p", 1)
}

// seedPricingProviderNamed seeds a Provider with explicit name and priority (both name and
// selection_priority are unique on ai_providers; cross-provider filter tests require two Providers
// in the same DB with distinct values on both unique axes).
func seedPricingProviderNamed(t *testing.T, f *fx, enabled bool, name string, priority int) string {
	t.Helper()
	id := uuid.NewString()
	cfg := []byte("{}")
	if err := f.db.Exec(`insert into ai_providers (id, name, provider_kind, base_url, model_name, enabled,
		selection_priority, privacy_contract_hash, output_schema_hash, configuration,
		secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
		version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'openai_compatible', 'https://ai.example', 'm1', ?, ?, ?, ?, ?, ?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		id, name, enabled, priority, strings.Repeat("a", 64), strings.Repeat("b", 64), cfg,
		make([]byte, 32), make([]byte, 12), "pay-1", f.userID).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	return id
}

func setBudget(t *testing.T, f *fx, enforced bool, currency string, inputMinor, outputMinor int64) {
	t.Helper()
	if err := f.db.Exec(`insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('ai_budget', 1, jsonb_build_object('enforced',?::boolean,'currency',?::text,'daily_budget_minor',10000,
		'alert_threshold_percent',80,'input_price_per_million_minor',?::bigint,'output_price_per_million_minor',?::bigint), 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings, version = system_settings.version + 1, updated_at = now()`,
		enforced, currency, inputMinor, outputMinor, f.userID).Error; err != nil {
		t.Fatalf("seed budget: %v", err)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestRateVersionCRUDAndOverlap P1: version increment, overlap rejection, cross-midnight row splitting,
// and currency constraint (save side).
func TestRateVersionCRUDAndOverlap(t *testing.T) {
	f := newFixture(t)
	truncatePricingTables(t, f)
	pid := seedPricingProvider(t, f, false)
	ctx := context.Background()
	day := mustTime(t, "2026-11-01T00:00:00Z")
	p := int64(1_000_000)

	v1, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "Asia/Shanghai",
		EffectiveFrom: day,
		Windows:       []RateWindowInput{{StartMinute: 0, EndMinute: 1440, InputPriceMicro: p, OutputPriceMicro: p}},
	})
	if err != nil || v1.Version != 1 {
		t.Fatalf("v1 create: %v version=%d", err, v1.Version)
	}
	if len(v1.Windows) != 1 || v1.Windows[0].ID == "" {
		t.Fatalf("v1 window must include persisted UUID: %+v", v1.Windows)
	}
	// New version start is later than open-ended version start -> automatically closes v1 at 12-01 (the open
	// end is merely a placeholder, leaving historical application periods unchanged and never recomputing history per Price-01); cross-midnight [1320,360) -> two rows.
	v2, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "Asia/Shanghai",
		EffectiveFrom: mustTime(t, "2026-12-01T00:00:00Z"),
		Windows:       []RateWindowInput{{StartMinute: 1320, EndMinute: 360, InputPriceMicro: 2 * p, OutputPriceMicro: 2 * p}},
	})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	var v1Until *time.Time
	if err := f.db.Raw(`select effective_until from ai_provider_rate_versions where id = ?`, v1.ID).
		Scan(&v1Until).Error; err != nil || v1Until == nil || !v1Until.Equal(mustTime(t, "2026-12-01T00:00:00Z")) {
		t.Fatalf("open-ended v1 must auto-close at v2 start: %v %v", v1Until, err)
	}
	// Genuine conflict within a bounded interval is rejected: v3 starting at 11-15 falls inside v1's closed interval.
	if _, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "Asia/Shanghai", EffectiveFrom: day.Add(15 * 24 * time.Hour),
		Windows: []RateWindowInput{{StartMinute: 0, EndMinute: 60, InputPriceMicro: p, OutputPriceMicro: p}},
	}); err == nil {
		t.Fatal("range inside a bounded version must be rejected")
	}
	if v2.Version != 2 || len(v2.Windows) != 2 ||
		v2.Windows[0].StartMinute != 0 || v2.Windows[0].EndMinute != 360 ||
		v2.Windows[1].StartMinute != 1320 || v2.Windows[1].EndMinute != 1440 {
		t.Fatalf("cross-midnight must normalize to two windows: %+v", v2.Windows)
	}
	// Budget enabled: saving USD rate on an enabled Provider vs CNY budget -> 1001 (save side).
	f.db.Exec(`update ai_providers set enabled = true where id = ?`, pid)
	setBudget(t, f, true, "CNY", 0, 0)
	if _, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "USD", Timezone: "Asia/Shanghai",
		EffectiveFrom: mustTime(t, "2027-06-01T00:00:00Z"),
		Windows:       []RateWindowInput{{StartMinute: 0, EndMinute: 1440, InputPriceMicro: p, OutputPriceMicro: p}},
	}); err == nil {
		t.Fatal("enforced budget currency mismatch must be rejected at save")
	}
	// Enable side: CNY budget + currently effective CNY rate -> passes.
	f.db.Exec(`update ai_providers set enabled = false where id = ?`, pid)
	setBudget(t, f, true, "CNY", 0, 0)
	if err := f.svc.CheckProviderEnableRateCurrency(ctx, f.db, pid); err != nil {
		t.Fatalf("matching currency must enable: %v", err)
	}
	// List read-back: two rows ordered newest -> oldest, window IDs match CreateRateVersion return.
	list, err := f.svc.ListRateVersions(ctx, pid)
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[1].Version != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	if len(v2.Windows) != 2 || v2.Windows[0].ID == "" || v2.Windows[1].ID == "" ||
		v2.Windows[0].ID != list[0].Windows[0].ID || v2.Windows[1].ID != list[0].Windows[1].ID ||
		v1.Windows[0].ID != list[1].Windows[0].ID {
		t.Fatalf("returned window IDs must match persisted DB IDs: create_v1=%+v list_v1=%+v create_v2=%+v list_v2=%+v",
			v1.Windows, list[1].Windows, v2.Windows, list[0].Windows)
	}
}

// TestSelectRateAtWindowsAndTimezone P2: effective intervals, minute of day in timezone, DST,
// global price fallback, and source markers.
func TestSelectRateAtWindowsAndTimezone(t *testing.T) {
	f := newFixture(t)
	truncatePricingTables(t, f)
	pid := seedPricingProvider(t, f, false)
	ctx := context.Background()
	p := int64(1_000_000)

	// Version A (from 2026-11-01): Shanghai all-day 1 CNY + New York 9:00-10:00 window 2 CNY —
	// instead of using two Providers, create a "Shanghai all-day base" version and a "New York morning window"
	// version on the same Provider across different effective periods.
	if _, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "Asia/Shanghai",
		EffectiveFrom: mustTime(t, "2026-11-01T00:00:00Z"),
		Windows:       []RateWindowInput{{StartMinute: 0, EndMinute: 1440, InputPriceMicro: p, OutputPriceMicro: p}},
	}); err != nil {
		t.Fatal(err)
	}
	// New York window version starting 2026-12-01 (base 1 CNY across three disjoint segments + 9:00-10:00 = 2 CNY).
	if _, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "America/New_York",
		EffectiveFrom: mustTime(t, "2026-12-01T00:00:00Z"),
		Windows: []RateWindowInput{
			{StartMinute: 0, EndMinute: 540, InputPriceMicro: p, OutputPriceMicro: p},
			{StartMinute: 540, EndMinute: 600, InputPriceMicro: 2 * p, OutputPriceMicro: 2 * p},
			{StartMinute: 600, EndMinute: 1440, InputPriceMicro: p, OutputPriceMicro: p},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Before effective interval (October) -> no version -> global dual-0 -> none.
	setBudget(t, f, false, "USD", 0, 0)
	if r, err := f.svc.SelectRateAt(ctx, f.db, pid, mustTime(t, "2026-10-15T02:00:00Z")); err != nil ||
		r.Source != RateSourceNone {
		t.Fatalf("before any version with zero global price must be none: %+v %v", r, err)
	}
	// After configuring global prices -> global_settings fallback (source marker, never masquerading as a Provider rate).
	// R1 H-1: settings are denominated in minor currency units / million (minor), converted to reference
	// micro-units via x 1e4 — 3_000_000 minor = 30_000 currency/million = 3e10 micro.
	setBudget(t, f, false, "USD", 3_000_000, 4_000_000)
	if r, _ := f.svc.SelectRateAt(ctx, f.db, pid, mustTime(t, "2026-10-15T02:00:00Z")); r.Source != RateSourceGlobalSettings ||
		r.InputPriceMicro != 30_000_000_000 || r.OutputPriceMicro != 40_000_000_000 || r.Currency != "USD" {
		t.Fatalf("legacy global fallback unit conversion mismatch: %+v", r)
	}
	// Shanghai all-day version: any timestamp in November -> provider_version 1 CNY.
	if r, _ := f.svc.SelectRateAt(ctx, f.db, pid, mustTime(t, "2026-11-20T16:00:00Z")); r.Source != RateSourceProviderVersion ||
		r.InputPriceMicro != p || r.Currency != "CNY" {
		t.Fatalf("shanghai all-day window mismatch: %+v", r)
	}
	// New York version DST: 2026-07-01T13:30Z = 9:30 EDT -> hits the 2 CNY window;
	// same day 14:30Z = 10:30 EDT -> base 1 CNY; January (EST) 13:30Z = 8:30 -> base rate
	// (same UTC instant falls outside the window in winter time — proving DST wall-clock difference).
	// Note that both July 2027 and January 2027 fall after the 2026-12-01 version start (open-ended interval).
	if r, _ := f.svc.SelectRateAt(ctx, f.db, pid, mustTime(t, "2027-07-01T13:30:00Z")); r.InputPriceMicro != 2*p {
		t.Fatalf("DST summer 9:30 EDT must hit the morning window: %+v", r)
	}
	if r, _ := f.svc.SelectRateAt(ctx, f.db, pid, mustTime(t, "2027-07-01T14:30:00Z")); r.InputPriceMicro != p {
		t.Fatalf("10:30 EDT must fall to base: %+v", r)
	}
	if r, _ := f.svc.SelectRateAt(ctx, f.db, pid, mustTime(t, "2027-01-15T13:30:00Z")); r.InputPriceMicro != p {
		t.Fatalf("winter 8:30 EST must fall to base (same UTC, different wall clock): %+v", r)
	}
	// Cross-midnight version (December Shanghai version superseded by New York version from 12-01;
	// cross-midnight matching is tested separately below, while v2's two-row shape was already verified in the CRUD test).
}

// TestSelectRateAtCrossMidnightWindow cross-midnight window matching (Price-02):
// [22:00,06:00) 2 CNY, daytime 1 CNY; 23:30 and 04:50 hit the night rate, while 12:00 hits the base rate.
func TestSelectRateAtCrossMidnightWindow(t *testing.T) {
	f := newFixture(t)
	truncatePricingTables(t, f)
	pid := seedPricingProvider(t, f, false)
	ctx := context.Background()
	p := int64(1_000_000)
	if _, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "Asia/Shanghai",
		EffectiveFrom: mustTime(t, "2026-11-01T00:00:00Z"),
		Windows: []RateWindowInput{
			// Daytime 1 CNY (360..1320) + nighttime 2 CNY cross-midnight [1320,360).
			{StartMinute: 360, EndMinute: 1320, InputPriceMicro: p, OutputPriceMicro: p},
			{StartMinute: 1320, EndMinute: 360, InputPriceMicro: 2 * p, OutputPriceMicro: 2 * p},
		},
	}); err != nil {
		t.Fatal(err)
	}
	night := mustTime(t, "2026-11-20T15:30:00Z") // 23:30 CST
	dawn := mustTime(t, "2026-11-20T20:50:00Z")  // 04:50 CST next day (second half of cross-midnight window)
	noon := mustTime(t, "2026-11-20T04:00:00Z")  // 12:00 CST
	for _, c := range []struct {
		at    time.Time
		price int64
		what  string
	}{{night, 2 * p, "23:30"}, {dawn, 2 * p, "04:50"}, {noon, p, "12:00"}} {
		if r, _ := f.svc.SelectRateAt(ctx, f.db, pid, c.at); r.InputPriceMicro != c.price {
			t.Fatalf("%s window price mismatch: want %d got %+v", c.what, c.price, r)
		}
	}
}

// TestRecordCallUsageAndReadSurfaces P3: per-call ledger tri-state columns, rate binding,
// parameter snapshots, and detail/summary read surfaces (currencies separated, hit-rate sample denominator).
func TestRecordCallUsageAndReadSurfaces(t *testing.T) {
	f := newFixture(t)
	truncatePricingTables(t, f)
	pid := seedPricingProvider(t, f, false)
	ctx := context.Background()
	p := int64(1_000_000)
	if _, err := f.svc.CreateRateVersion(ctx, f.userID, CreateRateVersionInput{
		ProviderID: pid, Currency: "CNY", Timezone: "Asia/Shanghai",
		EffectiveFrom: mustTime(t, "2026-11-01T00:00:00Z"),
		Windows: []RateWindowInput{{StartMinute: 0, EndMinute: 1440, InputPriceMicro: p, OutputPriceMicro: 2 * p,
			CachedReadPriceMicro: ptrOf(int64(100_000))}},
	}); err != nil {
		t.Fatal(err)
	}
	runID := uuid.NewString()
	if err := f.db.Exec(`insert into review_runs (id, draft_id, state, review_mode, version, created_at, updated_at)
		values (?, ?, 'pending', 'standard', 1, now(), now())`, runID, uuid.NewString()).Error; err != nil {
		// When review_runs foreign key constraints are heavy, fall back to asserting on the ledger directly without blocking.
		t.Logf("seed review_run skipped: %v", err)
	}
	temp := 0.4
	newDetail := func(in, out, cached *int64) *provider.UsageDetail {
		return &provider.UsageDetail{InputTokens: in, OutputTokens: out, CachedTokens: cached}
	}
	// Call 1: full detail (including cache hit) + all three parameters (Param-03 snapshot).
	r1 := provider.CallRecord{
		ProviderID: pid, ModelName: "m1", SentAt: mustTime(t, "2026-11-20T04:00:00Z"),
		HTTPStatus: 200, Outcome: provider.CallOutcomeOK,
		Detail:          newDetail(ptrOf(1000), ptrOf(100), ptrOf(600)),
		ReasoningEffort: "high", MaxTokens: 4096, Temperature: &temp,
	}
	if err := f.svc.RecordCallUsage(ctx, f.db, runID, 1, 1, 1, r1); err != nil {
		t.Fatal(err)
	}
	// Call 2: no usage (unknown) + no parameters (Usage-02: failed call retains row and is never marked free).
	r2 := provider.CallRecord{
		ProviderID: pid, ModelName: "m1", SentAt: mustTime(t, "2026-11-20T04:01:00Z"),
		HTTPStatus: 500, Outcome: provider.CallOutcomeError,
	}
	if err := f.svc.RecordCallUsage(ctx, f.db, runID, 1, 1, 2, r2); err != nil {
		t.Fatal(err)
	}
	// Detail read surface.
	items, next, err := f.svc.ListCallUsage(ctx, 10, "", runID, "", "")
	if err != nil || len(items) != 2 || next != nil {
		t.Fatalf("list calls: %v n=%d", err, len(items))
	}
	first := items[1] // sent_at desc -> r1 appears second
	if first.RateSource != "provider_version" || first.RateVersionID == nil || first.Currency == nil || *first.Currency != "CNY" {
		t.Fatalf("rate binding mismatch: %+v", first)
	}
	if first.ReasoningEffort == nil || *first.ReasoningEffort != "high" || first.MaxTokens == nil ||
		*first.MaxTokens != 4096 || first.Temperature == nil {
		t.Fatalf("parameter snapshot mismatch: %+v", first)
	}
	if first.InputTokens == nil || *first.InputTokens != 1000 || first.CachedTokens == nil || *first.CachedTokens != 600 {
		t.Fatalf("usage detail mismatch: %+v", first)
	}
	if !first.UsageReported || first.CalcBasis != "full_detail" {
		t.Fatalf("basis mismatch: %+v", first)
	}
	// Full-detail amount: base I = 1000*1 + O = 100*2 = 1200 micro;
	// R = 600*(0.1-1) = -540 -> 660 micro = 0.00066 CNY.
	if first.ReferenceAmount == nil || *first.ReferenceAmount != "0.00066000" {
		t.Fatalf("reference amount mismatch: %v", first.ReferenceAmount)
	}
	second := items[0]
	if second.UsageReported || second.InputTokens != nil || second.CalcBasis != "not_computable" ||
		second.ReferenceAmount != nil || second.RateSource != "provider_version" {
		t.Fatalf("unknown-usage call must record three-state NULLs: %+v", second)
	}
	// R1 C-1: unresponded call (network error / timeout) has HTTPStatus=0 -> persisted as NULL without
	// violating CHECK; unknown usage still binds the rate version.
	r3 := provider.CallRecord{
		ProviderID: pid, ModelName: "m1", SentAt: mustTime(t, "2026-11-20T04:02:00Z"),
		HTTPStatus: 0, Outcome: provider.CallOutcomeError,
	}
	if err := f.svc.RecordCallUsage(ctx, f.db, runID, 1, 1, 3, r3); err != nil {
		t.Fatalf("no-response call must record with NULL http_status: %v", err)
	}
	// R1 M-2 (Usage-03 idempotency): replaying the exact same call event -> does not double-book.
	if err := f.svc.RecordCallUsage(ctx, f.db, runID, 1, 1, 1, r1); err != nil {
		t.Fatal(err)
	}
	// Summary (single currency; hit-rate denominator counts only samples reporting cache details, never treating missing as 0).
	summary, err := f.svc.SummarizeCallUsage(ctx,
		mustTime(t, "2026-11-20T00:00:00Z"), mustTime(t, "2026-11-21T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) != 1 || summary[0].Currency != "CNY" || summary[0].Calls != 3 ||
		summary[0].ComputableCalls != 1 || summary[0].CacheDetailSamples != 1 ||
		summary[0].CachedTokensKnown != 600 || summary[0].InputTokensKnown != 1000 ||
		summary[0].InputTokensCacheKnown != 1000 {
		t.Fatalf("summary mismatch: %+v", summary)
	}
	if summary[0].ReferenceAmountNano != "0.00066000" {
		t.Fatalf("summary amount mismatch: %s", summary[0].ReferenceAmountNano)
	}
	// Unresponded row has http_status = NULL.
	var hs *int
	if err := f.db.Raw(`select http_status from ai_call_usage where sent_at = ?`, r3.SentAt).Scan(&hs).Error; err != nil || hs != nil {
		t.Fatalf("no-response call http_status must be NULL: %v %v", hs, err)
	}
	// RCP-20261102-S008: provider_id filter (negative case for cross-provider mixing within the same run;
	// both name and selection_priority unique axes are distinct).
	pid2 := seedPricingProviderNamed(t, f, false, "pricing-p-other", 2)
	rOther := provider.CallRecord{
		ProviderID: pid2, ModelName: "m2", SentAt: mustTime(t, "2026-11-20T06:00:00Z"),
		HTTPStatus: 200, Outcome: provider.CallOutcomeOK,
	}
	if err := f.svc.RecordCallUsage(ctx, f.db, runID, 1, 2, 1, rOther); err != nil {
		t.Fatal(err)
	}
	mine, _, err := f.svc.ListCallUsage(ctx, 10, "", "", pid, "")
	if err != nil || len(mine) != 3 {
		t.Fatalf("provider filter primary: %v n=%d", err, len(mine))
	}
	for _, it := range mine {
		if it.ProviderID != pid {
			t.Fatalf("provider filter leaked a foreign-provider row: %+v", it)
		}
	}
	theirs, _, err := f.svc.ListCallUsage(ctx, 10, "", "", pid2, "")
	if err != nil || len(theirs) != 1 || theirs[0].ProviderID != pid2 {
		t.Fatalf("provider filter secondary: %v n=%d", err, len(theirs))
	}
}

// TestProviderConfigurationReadBackAndEmptyOverride P4 (Param-02/04 full flow):
// persisted parameters are read back in full (without dropping temperature=0); an explicit empty
// override {} clears all user parameters, and system keys cannot be spoofed (rejected on write surface).
func TestProviderConfigurationReadBackAndEmptyOverride(t *testing.T) {
	f := newFixture(t)
	truncatePricingTables(t, f)
	ctx := context.Background()
	view, err := f.svc.CreateAIProvider(ctx, f.userID, CreateAIProviderInput{
		Name: "p-rb", ProviderKind: "openai_compatible", BaseURL: "https://ai.example",
		ModelName: "m1", Enabled: false, SelectionPriority: 1, APIKey: "k",
		Configuration: map[string]any{"reasoning_effort": "medium", "max_tokens": 2048.0, "temperature": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.GetAIProvider(ctx, view.ID)
	if err != nil || got.Configuration == nil ||
		got.Configuration.ReasoningEffort == nil || *got.Configuration.ReasoningEffort != "medium" ||
		got.Configuration.MaxTokens == nil || *got.Configuration.MaxTokens != 2048 ||
		got.Configuration.Temperature == nil || *got.Configuration.Temperature != 0 {
		t.Fatalf("typed read-back mismatch: %+v %v", got.Configuration, err)
	}
	// Changing only the name does not clear parameters (Param-02: modifying other fields retains stored parameters).
	kept, err := f.svc.ReplaceAIProvider(ctx, view.ID, ReplaceAIProviderInput{
		ExpectVersion: got.Version, Name: "p-rb-2", ProviderKind: "openai_compatible",
		BaseURL: "https://ai.example", ModelName: "m1", Enabled: false, SelectionPriority: 1,
	})
	if err != nil || kept.Configuration == nil || kept.Configuration.MaxTokens == nil {
		t.Fatalf("unrelated edit must preserve parameters: %+v %v", kept.Configuration, err)
	}
	// Explicit empty override = clear all user parameters (disabled state skips probing and replaces directly in DB);
	// R1 L-4: the existing system key structured_output_mode is preserved server-side and not cleared.
	f.db.Exec(`update ai_providers set configuration = configuration || '{"structured_output_mode":"json_object"}'::jsonb where id = ?`, view.ID)
	cleared, err := f.svc.ReplaceAIProvider(ctx, view.ID, ReplaceAIProviderInput{
		ExpectVersion: kept.Version, Name: "p-rb-2", ProviderKind: "openai_compatible",
		BaseURL: "https://ai.example", ModelName: "m1", Enabled: false, SelectionPriority: 1,
		Configuration: map[string]any{},
	})
	if err != nil || cleared.Configuration == nil || cleared.Configuration.StructuredOutputMode != "json_object" ||
		cleared.Configuration.MaxTokens != nil {
		t.Fatalf("empty override must clear user parameters but preserve the system key: %+v %v", cleared.Configuration, err)
	}
	// System keys cannot be spoofed through the user write surface.
	if err := ValidateProviderConfiguration(map[string]any{"structured_output_mode": "json_object"}); err == nil {
		t.Fatal("system key must be rejected on the user write surface")
	}
}

func ptrOf(v int64) *int64 { return &v }
