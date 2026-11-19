// budget_pricing_test.go SEC-0039/SEC-0011 regression: checked arithmetic for token pricing
// and net reservation release on failure paths.
package application

import (
	"math"
	"testing"
)

// TestSEC0039TokenBasedAmountMatchesReference matches the reference formula (legacy implementation)
// point-for-point across normal ranges (including ceiling rounding and the 1-unit minimum).
func TestSEC0039TokenBasedAmountMatchesReference(t *testing.T) {
	reference := func(in, out, inPrice, outPrice, fallback int64) int64 {
		if inPrice <= 0 && outPrice <= 0 {
			return fallback
		}
		i, o := in, out
		if i < 0 {
			i = 0
		}
		if o < 0 {
			o = 0
		}
		amount := (i*inPrice+999_999)/1_000_000 + (o*outPrice+999_999)/1_000_000
		if amount < 1 {
			amount = 1
		}
		return amount
	}
	cases := [][5]int64{
		{0, 0, 1, 1, 100},
		{1, 0, 1, 1, 100},
		{1_000_000, 0, 1, 1, 100},
		{1_000_001, 0, 1, 1, 100},
		{0, 1, 3, 7, 100},
		{123_456, 654_321, 2, 3, 100},
		{9_999_999, 9_999_999, 500, 1500, 100},
		{1_000_000_000, 0, 1, 0, 100},
		{10_000, 10_000, 0, 0, 100}, // Dual-0 fallback
	}
	for _, c := range cases {
		got := tokenBasedAmount(c[0], c[1], c[2], c[3], c[4])
		want := reference(c[0], c[1], c[2], c[3], c[4])
		if got != want {
			t.Fatalf("tokenBasedAmount(%v)=%d, reference=%d", c, got, want)
		}
	}
}

// TestSEC0039TokenBasedAmountOverflowCapped extreme unit prices or token counts no longer wrap around
// into a tiny amount (budget underestimation), capping at math.MaxInt64.
func TestSEC0039TokenBasedAmountOverflowCapped(t *testing.T) {
	max := int64(math.MaxInt64)
	if got := tokenBasedAmount(max, max, max, max, 100); got != max {
		t.Fatalf("max*max must cap at MaxInt64, got %d", got)
	}
	// Legacy implementation wrapped around int64 here: 10^13 tokens x 10^12 unit price / million = 10^19
	// overflowed negative -> settled at the 1-unit minimum (underestimating budget).
	if got := tokenBasedAmount(10_000_000_000_000, 0, 1_000_000_000_000, 0, 100); got != max {
		t.Fatalf("overflow case must cap at MaxInt64, got %d", got)
	}
	if got := tokenBasedAmount(max, 0, 1_000_000, 0, 100); got != max {
		t.Fatalf("exact-cap case must equal MaxInt64, got %d", got)
	}
	// Overflow on one side only: the other side still contributes normally.
	if got := tokenBasedAmount(0, 1_000_000, 0, 5, 100); got != 5 {
		t.Fatalf("output-only pricing broken: %d", got)
	}
	// Negative token counts are treated as 0 (defensive).
	if got := tokenBasedAmount(-5, -7, 2, 3, 100); got != 1 {
		t.Fatalf("negative tokens clamp to minimum, got %d", got)
	}
}

// TestSEC0011UnreleasedReserveMinor remaining reservation to release on failure = reserved - settled
// (SEC-0011: the portion already transferred by Settle must not be Released again, preventing double
// deduction of reserved).
func TestSEC0011UnreleasedReserveMinor(t *testing.T) {
	cases := []struct {
		reserved, settled, want int64
	}{
		{100, 0, 100},  // Unsettled: release in full
		{100, 30, 70},  // Partially settled: release only the remaining balance
		{100, 100, 0},  // Fully settled: zero balance left to release
		{100, 150, 0},  // Settled exceeds reserved: absorbed by Settle's greatest(..., 0)
		{0, 50, 0},     // Zero reservation
		{-10, 5, 0},    // Defensive check for negative reservation
		{100, -5, 100}, // Defensive check for negative settled amount: treated as unsettled
	}
	for _, c := range cases {
		if got := unreleasedReserveMinor(c.reserved, c.settled); got != c.want {
			t.Fatalf("unreleasedReserveMinor(%d,%d)=%d want %d", c.reserved, c.settled, got, c.want)
		}
	}
}
