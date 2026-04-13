package server

import (
	"math"
	"testing"

	"github.com/wesm/agentsview/internal/db"
)

// approxEqual returns true if a and b are within eps (for
// float comparisons that have rounding from division).
func approxEqual(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

func TestComputeCacheStats_ReadOnlySavings(t *testing.T) {
	// 1M cache-read tokens, no creations. Discount per token
	// is (3.00 - 0.30) / 1M = $2.70 total.
	cs := computeCacheStats(db.UsageTotals{
		InputTokens:     1_000_000,
		CacheReadTokens: 1_000_000,
	})
	if !approxEqual(cs.SavingsVsUncached, 2.70, 1e-9) {
		t.Errorf("SavingsVsUncached = %v, want ~2.70",
			cs.SavingsVsUncached)
	}
}

func TestComputeCacheStats_CreationOnlyIsNegative(t *testing.T) {
	// 1M cache-creation tokens, no reads. Premium is
	// (3.00 - 3.75) / 1M = -$0.75. The cache costs more than
	// an uncached run; savings must be negative.
	cs := computeCacheStats(db.UsageTotals{
		InputTokens:         1_000_000,
		CacheCreationTokens: 1_000_000,
	})
	if cs.SavingsVsUncached >= 0 {
		t.Errorf("SavingsVsUncached = %v, want < 0",
			cs.SavingsVsUncached)
	}
	if !approxEqual(cs.SavingsVsUncached, -0.75, 1e-9) {
		t.Errorf("SavingsVsUncached = %v, want ~-0.75",
			cs.SavingsVsUncached)
	}
}

func TestComputeCacheStats_MixedReadsAndCreations(t *testing.T) {
	// 2M reads save 2 * 2.70 = $5.40.
	// 1M creations cost 1 * 0.75 = -$0.75.
	// Net = $4.65.
	cs := computeCacheStats(db.UsageTotals{
		InputTokens:         3_000_000,
		CacheReadTokens:     2_000_000,
		CacheCreationTokens: 1_000_000,
	})
	if !approxEqual(cs.SavingsVsUncached, 4.65, 1e-9) {
		t.Errorf("SavingsVsUncached = %v, want ~4.65",
			cs.SavingsVsUncached)
	}
}

func TestComputeCacheStats_ZeroTotalsIsZero(t *testing.T) {
	cs := computeCacheStats(db.UsageTotals{})
	if cs.SavingsVsUncached != 0 {
		t.Errorf("SavingsVsUncached = %v, want 0",
			cs.SavingsVsUncached)
	}
	if cs.HitRate != 0 {
		t.Errorf("HitRate = %v, want 0", cs.HitRate)
	}
}

func TestComputeCacheStats_HitRate(t *testing.T) {
	// 800 cache reads, 200 uncached inputs -> 0.80 hit rate.
	// (The HitRate denominator in this code is
	// cacheRead + input where input already includes reads.)
	cs := computeCacheStats(db.UsageTotals{
		InputTokens:     200,
		CacheReadTokens: 800,
	})
	// denom = 800 + 200 = 1000; hit = 800/1000 = 0.80.
	if !approxEqual(cs.HitRate, 0.80, 1e-9) {
		t.Errorf("HitRate = %v, want ~0.80", cs.HitRate)
	}
}

func TestComputeCacheStats_UncachedInputNonNegative(t *testing.T) {
	// Pathological case where cache tokens exceed total
	// input (can happen because the tallies come from
	// different JSON fields). UncachedInputTokens must clamp
	// to 0, not go negative.
	cs := computeCacheStats(db.UsageTotals{
		InputTokens:         100,
		CacheReadTokens:     200,
		CacheCreationTokens: 50,
	})
	if cs.UncachedInputTokens != 0 {
		t.Errorf("UncachedInputTokens = %d, want 0",
			cs.UncachedInputTokens)
	}
}
