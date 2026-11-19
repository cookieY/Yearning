// pricing_test.go S008 reference pricing pure-function unit tests: time window normalization and
// overlap rejection (Price-02), fixed-point parsing and reproducible rounding (Price-05), and reference
// calculation tri-state & formula (Cache-01..05, Usage-01, section 4.3). DB-bound paths (rate selection /
// ledger / summary) live in integration tests (//go:build integration).
package application

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func i64(v int64) *int64 { return &v }

// TestParseDecimalMicroBounds fixed-point price payload parsing: valid bounds, and rejection of
// negatives, scientific notation, redundant leading zeros, > 6 decimal places, and out-of-range values.
func TestParseDecimalMicroBounds(t *testing.T) {
	ok := map[string]int64{
		"0": 0, "0.0": 0, "0.000001": 1,
		"1": 1_000_000, "0.5": 500_000, "12.345678": 12_345_678,
		"999999999999": 999_999_999_999_000_000,
	}
	for in, want := range ok {
		got, err := parseDecimalMicro(in)
		if err != nil || got != want {
			t.Fatalf("parse(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	bad := []string{"", "-0.5", ".5", "1.", "01", "1e3", "1.2345678", "1000000000000", "0.5 "}
	for _, in := range bad {
		if _, err := parseDecimalMicro(in); err == nil {
			t.Fatalf("parse(%q) must fail", in)
		}
	}
}

func TestParseNumericMicroPGText(t *testing.T) {
	// PG numeric text may carry trailing zeros or have no decimal point.
	for in, want := range map[string]int64{"1.500000": 1_500_000, "2": 2_000_000, "0.2500": 250_000, "3.141592": 3_141_592} {
		got, err := parseNumericMicro(in)
		if err != nil || got != want {
			t.Fatalf("parseNumeric(%q)=%d,%v want %d", in, got, err, want)
		}
	}
}

// TestRoundDivReproducible deterministic rounding: half-up, symmetric away from zero for positive and negative values.
func TestRoundDivReproducible(t *testing.T) {
	cases := []struct {
		num, den, want int64
	}{
		{10, 4, 3}, {9, 4, 2}, {6, 4, 2}, // 2.5 -> 3 half-up
		{-10, 4, -3}, {-6, 4, -2},
		{0, 7, 0}, {3, 7, 0},
	}
	for _, c := range cases {
		got := roundDiv(big.NewInt(c.num), c.den)
		if got.Int64() != c.want {
			t.Fatalf("roundDiv(%d,%d)=%s want %d", c.num, c.den, got, c.want)
		}
	}
	// Huge inputs do not overflow: 9.2e18 tokens * 1e18 priceMicro / 1e4 = 9.2e32.
	x := new(big.Int).Mul(big.NewInt(9_223_372_036_854_775_807), big.NewInt(1_000_000_000_000_000_000))
	got := tokensTimesPrice(9_223_372_036_854_775_807, 1_000_000_000_000_000_000)
	if got.Cmp(new(big.Int).Quo(x, big.NewInt(10_000))) < 0 {
		t.Fatalf("huge fixed-point product must not overflow: %s", got)
	}
}

func rate(source string, in, out int64, cachedRead, cachedWrite *int64) EffectiveRate {
	return EffectiveRate{Source: source, Currency: "CNY",
		InputPriceMicro: in, OutputPriceMicro: out,
		CachedReadPriceMicro: cachedRead, CachedWritePriceMicro: cachedWrite}
}

// TestComputeReferenceCostBasisMatrix Cache-01/02/03/04 + section 4.3 formula +
// Usage-01 (reasoning is recorded as detail only without double charging — amount is composed solely of I/O/R/W).
func TestComputeReferenceCostBasisMatrix(t *testing.T) {
	// Pu=1, Po=2 (per million); in micro-units.
	base := rate(RateSourceProviderVersion, 1_000_000, 2_000_000, nil, nil)
	t.Run("not computable when usage unreported", func(t *testing.T) {
		for _, d := range []*provider.UsageDetail{nil, {InputTokens: i64(100)}, {OutputTokens: i64(5)}} {
			c := ComputeReferenceCost(d, base)
			if c.AmountNano != nil || c.Basis != CalcBasisNotComputable {
				t.Fatalf("unreported/partial totals must be not_computable: %+v", c)
			}
		}
	})
	t.Run("not computable when no rate", func(t *testing.T) {
		c := ComputeReferenceCost(&provider.UsageDetail{InputTokens: i64(10), OutputTokens: i64(1)}, EffectiveRate{Source: RateSourceNone})
		if c.AmountNano != nil || c.Note != "no_rate_configured" {
			t.Fatalf("no rate must be not_computable: %+v", c)
		}
	})
	t.Run("base only without cache detail", func(t *testing.T) {
		c := ComputeReferenceCost(&provider.UsageDetail{InputTokens: i64(1000), OutputTokens: i64(100)}, base)
		if c.Basis != CalcBasisBaseOnly || c.Note != "no_cache_detail_reported" {
			t.Fatalf("cache-03 basis mismatch: %+v", c)
		}
		// 1000*1 + 100*2 = 1200 micro -> tokens * priceMicro / 1e4:
		// 1 token * 1 unit/million = 1e-6 unit = 100 nano. 1000*100 + 100*200 = 120000 nano.
		if c.AmountNano == nil || c.AmountNano.Int64() != 120_000 {
			t.Fatalf("base amount wrong: %s", c.AmountNano)
		}
	})
	t.Run("full detail applies per-category adjustment", func(t *testing.T) {
		// Pu=1, Pr=0.1, Pw=2; I=1000 (including R=600, W=100), O=100.
		// Base = 1000*1 + 100*2 = 1200; adjustment R: 600*(0.1-1) = -540; W: 100*(2-1) = +100 -> 760 micro.
		// In nano (tokens * priceMicro / 1e4): base = 1000*1e6/1e4 + 100*2e6/1e4 = 1.2e5;
		// R: 600*(0.1e6-1e6)/1e4 = -5.4e4; W: 100*(2e6-1e6)/1e4 = 1e4;
		// total = 1.2e5 - 5.4e4 + 1e4 = 7.6e4 nano.
		r := rate(RateSourceProviderVersion, 1_000_000, 2_000_000, i64(100_000), i64(2_000_000))
		c := ComputeReferenceCost(&provider.UsageDetail{
			InputTokens: i64(1000), OutputTokens: i64(100),
			CachedTokens: i64(600), CacheWriteTokens: i64(100), ReasoningTokens: i64(50),
		}, r)
		if c.Basis != CalcBasisFullDetail || c.Note != "" {
			t.Fatalf("full detail mismatch: %+v", c)
		}
		if c.AmountNano.Int64() != 76_000 {
			t.Fatalf("full detail amount wrong: %s (reasoning must not add cost)", c.AmountNano)
		}
	})
	t.Run("partial when reported category unpriced", func(t *testing.T) {
		r := rate(RateSourceProviderVersion, 1_000_000, 2_000_000, nil, nil) // Cache price unconfigured
		c := ComputeReferenceCost(&provider.UsageDetail{
			InputTokens: i64(1000), OutputTokens: i64(100), CachedTokens: i64(600),
		}, r)
		if c.Basis != CalcBasisPartialDetail || c.Note != "reported_category_price_unconfigured" {
			t.Fatalf("partial basis mismatch: %+v", c)
		}
		if c.AmountNano.Int64() != 120_000 {
			t.Fatalf("unpriced category stays in base at Pu: %s", c.AmountNano)
		}
	})
	t.Run("relation violation abandons cache detail", func(t *testing.T) {
		r := rate(RateSourceProviderVersion, 1_000_000, 2_000_000, i64(100_000), nil)
		c := ComputeReferenceCost(&provider.UsageDetail{
			InputTokens: i64(500), OutputTokens: i64(10), CachedTokens: i64(600),
		}, r)
		if c.Basis != CalcBasisBaseOnly || c.Note != "cache_detail_exceeds_input_total" {
			t.Fatalf("relation violation must fall back to base: %+v", c)
		}
		if c.AmountNano.Int64() != 52_000 { // 500*1e6/1e4 = 5e4; 10*2e6/1e4 = 2e3
			t.Fatalf("base fallback amount wrong: %s", c.AmountNano)
		}
	})
	t.Run("global settings legacy source has no cache prices", func(t *testing.T) {
		g := rate(RateSourceGlobalSettings, 1_000_000, 2_000_000, nil, nil)
		c := ComputeReferenceCost(&provider.UsageDetail{
			InputTokens: i64(1000), OutputTokens: i64(100), CachedTokens: i64(700),
		}, g)
		if c.Basis != CalcBasisPartialDetail {
			t.Fatalf("legacy global price with cache detail is partial: %+v", c)
		}
	})
}

// TestNormalizeWindowsCrossMidnightAndOverlap Price-02: cross-midnight splits into two rows,
// post-split overlaps are rejected, end == start is rejected, and interval bounds are validated.
func TestNormalizeWindowsCrossMidnightAndOverlap(t *testing.T) {
	p := i64(1_000_000)
	mk := func(s, e int) RateWindowInput {
		return RateWindowInput{StartMinute: s, EndMinute: e, InputPriceMicro: *p, OutputPriceMicro: *p}
	}
	// Cross-midnight [1320,360) -> [1320,1440) + [0,360).
	out, err := normalizeWindows([]RateWindowInput{mk(1320, 360)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].StartMinute != 0 || out[0].EndMinute != 360 ||
		out[1].StartMinute != 1320 || out[1].EndMinute != 1440 {
		t.Fatalf("cross-midnight normalization wrong: %+v", out)
	}
	// Overlap with an existing window after splitting -> rejected.
	if _, err := normalizeWindows([]RateWindowInput{mk(1320, 360), mk(300, 400)}); err == nil {
		t.Fatal("overlap after normalization must be rejected")
	}
	// Valid all-day base rate + nighttime window.
	if _, err := normalizeWindows([]RateWindowInput{mk(0, 1440)}); err != nil {
		t.Fatal(err)
	}
	// end == start rejected; out-of-bounds endpoints rejected; empty or > 24 windows rejected.
	bad := [][]RateWindowInput{
		{mk(600, 600)},
		{mk(-1, 100)}, {mk(0, 1441)},
		{},
	}
	for i, ws := range bad {
		if _, err := normalizeWindows(ws); err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
	}
}

// TestConfigChangedEmptyOverrideTriggersProbe Param-04 core: explicit empty override {} = real change
// (triggers probe), whereas omitted nil = retain existing.
func TestConfigChangedEmptyOverrideTriggersProbe(t *testing.T) {
	row := replaceProviderRow{Name: "n", BaseURL: "https://x.example", ModelName: "m", SelectionPriority: 1}
	same := ReplaceAIProviderInput{Name: "n", BaseURL: "https://x.example", ModelName: "m",
		SelectionPriority: 1, Configuration: nil}
	if configChanged(row, same) {
		t.Fatal("omitted configuration must preserve (no change)")
	}
	empty := same
	empty.Configuration = map[string]any{}
	if !configChanged(row, empty) {
		t.Fatal("explicit empty override is a real change and must trigger probe")
	}
	valued := same
	valued.Configuration = map[string]any{"max_tokens": 1024.0}
	if !configChanged(row, valued) {
		t.Fatal("explicit values are a change")
	}
}

// TestValidateProviderConfigurationGenParams Param-05: reasoning_effort enum,
// system key rejection, and existing boundary checks unchanged.
func TestValidateProviderConfigurationGenParams(t *testing.T) {
	for _, level := range []string{"low", "medium", "high"} {
		if err := ValidateProviderConfiguration(map[string]any{"reasoning_effort": level}); err != nil {
			t.Fatalf("level %s must validate: %v", level, err)
		}
	}
	for _, prof := range []string{"default", "san"} {
		if err := ValidateProviderConfiguration(map[string]any{"system_prompt_profile": prof}); err != nil {
			t.Fatalf("system_prompt_profile=%v must validate: %v", prof, err)
		}
	}
	for _, bad := range []map[string]any{
		{"reasoning_effort": "ultra"},
		{"reasoning_effort": 1.0},
		{"structured_output_mode": "json_object"}, // System keys cannot be written by users
		{"reasoning_effort": ""},                  // Empty string = invalid (omission is the default)
		{"system_prompt_profile": "v4"},
		{"system_prompt_profile": ""},
		{"system_prompt_profile": nil},
		{"system_prompt_profile": 123},
	} {
		if err := ValidateProviderConfiguration(bad); err == nil {
			t.Fatalf("invalid config must be rejected: %v", bad)
		}
	}
	if v := configurationView([]byte(`{"system_prompt_profile":"san"}`)); v == nil || v.SystemPromptProfile == nil || *v.SystemPromptProfile != "san" {
		t.Fatalf("system_prompt_profile must read back strongly typed: %+v", v)
	}
	sanitized := sanitizeProviderConfiguration(map[string]any{"max_tokens": 4096.0, "system_prompt_profile": nil})
	if _, hasProfile := sanitized["system_prompt_profile"]; hasProfile || sanitized["max_tokens"] != 4096.0 {
		t.Fatalf("sanitizeProviderConfiguration must drop nil override and preserve other keys: %+v", sanitized)
	}
}

// TestConfigurationViewTypedReadBack Param-02: stored values are backfilled in full (including
// temperature=0), system keys are surfaced read-only, and unknown shapes are never fabricated.
func TestConfigurationViewTypedReadBack(t *testing.T) {
	cfg := configurationView([]byte(`{"max_tokens":8192,"temperature":0,"reasoning_effort":"high","structured_output_mode":"json_object"}`))
	if cfg == nil || cfg.MaxTokens == nil || *cfg.MaxTokens != 8192 ||
		cfg.Temperature == nil || *cfg.Temperature != 0 ||
		cfg.ReasoningEffort == nil || *cfg.ReasoningEffort != "high" ||
		cfg.StructuredOutputMode != "json_object" {
		t.Fatalf("typed read-back mismatch: %+v", cfg)
	}
	if configurationView(nil) != nil || configurationView([]byte(`{}`)) != nil {
		t.Fatal("empty configuration must read back as nil")
	}
	if v := configurationView([]byte(`{"structured_output_mode":"json_schema"}`)); v == nil || v.StructuredOutputMode != "json_schema" {
		t.Fatalf("system-only configuration must expose the mode: %+v", v)
	}
}

func TestFormatMicroAndNanoRoundTrip(t *testing.T) {
	if formatMicroDecimal(1_234_567) != "1.234567" || formatMicroDecimal(0) != "0.000000" {
		t.Fatal("micro format broken")
	}
	if formatNanoAmount(big.NewInt(120_000)) != "0.00120000" {
		t.Fatalf("nano format broken: %s", formatNanoAmount(big.NewInt(120_000)))
	}
	if formatNanoAmount(big.NewInt(-120_000)) != "-0.00120000" {
		t.Fatal("negative nano format broken")
	}
	if normalizeAmountText("12.50000000") != "12.50000000" || normalizeAmountText("3.5") != "3.50000000" ||
		normalizeAmountText("") != "" || normalizeAmountText("7") != "7.00000000" {
		t.Fatal("amount normalization broken")
	}
}

func assertValidation1001Field(t *testing.T, err error, field string) {
	t.Helper()
	var domErr *domain.Error
	if !errors.As(err, &domErr) || domErr.Code != 1001 {
		t.Fatalf("expected domain code 1001, got %v", err)
	}
	if field != "" {
		if _, ok := domErr.Fields[field]; !ok {
			t.Fatalf("expected 1001 meta.fields[%q], got %+v", field, domErr.Fields)
		}
	}
}

func TestAIProviderValidationGuards(t *testing.T) {
	ctx := context.Background()
	svc := &Services{}

	// CreateAIProvider guards
	if _, err := svc.CreateAIProvider(ctx, "u1", CreateAIProviderInput{}); err == nil {
		t.Fatal("expected 1001 on missing fields")
	}
	if _, err := svc.CreateAIProvider(ctx, "u1", CreateAIProviderInput{
		Name: "p1", ProviderKind: "openai_compatible", BaseURL: "https://api.example.com/v1",
		ModelName: "san", APIKey: "sk", Configuration: map[string]any{"system_prompt_profile": "v4"},
	}); err == nil {
		t.Fatal("expected 1001 on invalid profile in CreateAIProvider")
	} else {
		assertValidation1001Field(t, err, "system_prompt_profile")
	}
	if _, err := svc.CreateAIProvider(ctx, "u1", CreateAIProviderInput{
		Name: "p1", ProviderKind: "openai_compatible", BaseURL: "http://8.8.8.8/v1",
		ModelName: "san", APIKey: "sk",
	}); err == nil {
		t.Fatal("expected 1001 on public http:// BaseURL in CreateAIProvider")
	} else {
		assertValidation1001Field(t, err, "base_url")
	}
	if _, err := svc.CreateAIProvider(ctx, "u1", CreateAIProviderInput{
		Name: "p1", ProviderKind: "openai_compatible", BaseURL: "https://api.example.com/v1",
		ModelName: "san", APIKey: "sk", Configuration: map[string]any{"unknown_opt": true},
	}); err == nil {
		t.Fatal("expected 1001 on unknown configuration key in CreateAIProvider")
	} else {
		assertValidation1001Field(t, err, "unknown_opt")
	}

	// ValidateProviderConfiguration branches
	for _, tc := range []struct {
		cfg   map[string]any
		field string
	}{
		{map[string]any{"temperature": float64(9)}, "temperature"},
		{map[string]any{"max_tokens": float64(0)}, "max_tokens"},
		{map[string]any{"reasoning_effort": "extreme"}, "reasoning_effort"},
		{map[string]any{"system_prompt_profile": 123}, "system_prompt_profile"},
	} {
		err := ValidateProviderConfiguration(tc.cfg)
		if err == nil {
			t.Fatalf("expected error for config %+v", tc.cfg)
		}
		assertValidation1001Field(t, err, tc.field)
	}

	// ReplaceAIProvider guards
	if _, err := svc.ReplaceAIProvider(ctx, "id-1", ReplaceAIProviderInput{
		BaseURL: "https://api.example.com/v1", ModelName: "san",
		Configuration: map[string]any{"system_prompt_profile": nil},
	}); err == nil {
		t.Fatal("expected 1001 on null system_prompt_profile in ReplaceAIProvider")
	} else {
		assertValidation1001Field(t, err, "system_prompt_profile")
	}
	if _, err := svc.ReplaceAIProvider(ctx, "id-1", ReplaceAIProviderInput{
		BaseURL: "http://93.184.216.34/v1", ModelName: "san",
	}); err == nil {
		t.Fatal("expected 1001 on public http:// BaseURL in ReplaceAIProvider")
	} else {
		assertValidation1001Field(t, err, "base_url")
	}
	if _, err := svc.ReplaceAIProvider(ctx, "", ReplaceAIProviderInput{
		BaseURL: "https://api.example.com/v1", ModelName: "",
	}); err == nil {
		t.Fatal("expected 1001 on incomplete endpoint in ReplaceAIProvider")
	} else {
		assertValidation1001Field(t, err, "")
	}

	// ListProviderModels guards
	if _, err := svc.ListProviderModels(ctx, "", "sk", ""); err == nil {
		t.Fatal("expected 1001 on empty baseURL in ListProviderModels")
	}
	if _, err := svc.ListProviderModels(ctx, "http://93.184.216.34/v1", "sk", ""); err == nil {
		t.Fatal("expected 1001 on public http:// BaseURL in ListProviderModels")
	} else {
		assertValidation1001Field(t, err, "base_url")
	}
}
