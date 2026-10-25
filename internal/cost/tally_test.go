package cost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedPricing returns $2 in / $8 out per 1M for everything. Tests can
// validate dollar math against round numbers.
func fixedPricing(string, string) (float64, float64) { return 2.00, 8.00 }

func TestTally_LoadMissingReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	tally, err := Load(path)
	require.NoError(t, err)
	assert.NotNil(t, tally.Sessions)
	assert.Empty(t, tally.Sessions)
	assert.Greater(t, tally.StartTime, int64(0))
}

func TestTally_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	t1, _ := Load(path)
	t1.Sessions["abc"] = SessionCost{Input: 100, Output: 200, Provider: "openai", Model: "gpt-4.1"}
	require.NoError(t, t1.Save(path))

	t2, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 100, t2.Sessions["abc"].Input)
	assert.Equal(t, "openai", t2.Sessions["abc"].Provider)
}

func TestTracker_HighWaterMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	tr, err := NewTracker(path, fixedPricing)
	require.NoError(t, err)

	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1000, 500))
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 800, 400))  // smaller — should be ignored
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1200, 600)) // larger — should win

	in, out := tr.SessionTokens("s1")
	assert.Equal(t, 1200, in)
	assert.Equal(t, 600, out)
}

func TestTracker_SessionCost(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1_000_000, 500_000))

	// 1M * $2/M + 0.5M * $8/M = $6.00
	assert.InDelta(t, 6.00, tr.SessionCost("s1"), 1e-9)
}

func TestTracker_TotalCostAggregates(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1_000_000, 0))
	require.NoError(t, tr.RecordUsage("s2", "openai", "gpt-4.1", 0, 1_000_000))
	// $2 + $8 = $10
	assert.InDelta(t, 10.00, tr.TotalCost(), 1e-9)
}

func TestTracker_Reset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	tr, _ := NewTracker(path, fixedPricing)
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 100, 50))
	require.NoError(t, tr.Reset())
	in, out := tr.SessionTokens("s1")
	assert.Zero(t, in)
	assert.Zero(t, out)
}

func TestTracker_Breakdown(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1000, 500))
	require.NoError(t, tr.RecordUsage("s2", "gemini", "gemini-2.5-pro", 2000, 1000))

	rows := tr.Breakdown()
	require.Len(t, rows, 2)
}

func TestTracker_SessionCostsForIDs(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1_000_000, 0)) // $2
	require.NoError(t, tr.RecordUsage("s2", "openai", "gpt-4.1", 0, 1_000_000)) // $8
	require.NoError(t, tr.RecordUsage("s3", "openai", "gpt-4.1", 500_000, 0))   // $1

	// Two known ids → sum.
	got := tr.SessionCostsForIDs([]string{"s1", "s2"})
	assert.InDelta(t, 10.00, got, 1e-9)

	// Including an unknown id contributes 0, no error.
	got = tr.SessionCostsForIDs([]string{"s1", "missing", "s3"})
	assert.InDelta(t, 3.00, got, 1e-9)

	// Empty input → 0.
	assert.InDelta(t, 0.0, tr.SessionCostsForIDs(nil), 1e-9)
}

// TestTracker_CacheCountsAreDiscounted pins the pricing invariant: cached
// tokens are already inside Input, so they are never added on top -- and
// they are billed at the cache rate, not the fresh one. The old formula
// charged the whole cache-inclusive Input at full price, which is how /cost
// came to disagree with the session's own total by ~6x on a cache-heavy run.
func TestTracker_CacheCountsAreDiscounted(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsageWithCache("s1", "anthropic", "claude", 1_000_000, 500_000, 200_000, 700_000))

	// 100k fresh * $2/M = 0.20; 700k reads * $2/M * 0.1 = 0.14;
	// 200k writes * $2/M * 1.0 = 0.40; 500k out * $8/M = 4.00.
	assert.InDelta(t, 4.74, tr.SessionCost("s1"), 1e-9)

	// A provider-specific rate lookup takes precedence over the defaults.
	tr.SetCacheRates(func(string, string) (float64, float64) { return 0.5, 2.0 })
	// 0.20 + 700k*2*0.5/M (0.70) + 200k*2*2.0/M (0.80) + 4.00.
	assert.InDelta(t, 5.70, tr.SessionCost("s1"), 1e-9)
	tr.SetCacheRates(nil)
	assert.InDelta(t, 4.74, tr.SessionCost("s1"), 1e-9)

	e := tr.Breakdown()
	require.Len(t, e, 1)
	assert.Equal(t, 200_000, e[0].CacheCreation)
	assert.Equal(t, 700_000, e[0].CacheRead)
	assert.LessOrEqual(t, e[0].CacheCreation+e[0].CacheRead, e[0].Input)
}

// TestTracker_CacheHighWaterMark confirms the cache counters follow the same
// only-ever-increase rule as input and output, so an out-of-order or replayed
// usage report cannot walk them backwards.
func TestTracker_CacheHighWaterMark(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsageWithCache("s1", "anthropic", "claude", 1000, 500, 100, 800))
	require.NoError(t, tr.RecordUsageWithCache("s1", "anthropic", "claude", 800, 400, 50, 600))
	require.NoError(t, tr.RecordUsageWithCache("s1", "anthropic", "claude", 1200, 600, 150, 900))

	e := tr.Breakdown()
	require.Len(t, e, 1)
	assert.Equal(t, 1200, e[0].Input)
	assert.Equal(t, 150, e[0].CacheCreation)
	assert.Equal(t, 900, e[0].CacheRead)
}

// TestTracker_RecordUsagePreservesCacheCounts covers the narrow-signature
// caller: it has no cache figures to offer, and passing implicit zeros would
// be indistinguishable from a provider reporting no cache hits. It must
// leave whatever cache counts are already recorded alone.
func TestTracker_RecordUsagePreservesCacheCounts(t *testing.T) {
	tr, _ := NewTracker(filepath.Join(t.TempDir(), "tally.json"), fixedPricing)
	require.NoError(t, tr.RecordUsageWithCache("s1", "anthropic", "claude", 1000, 500, 100, 800))
	require.NoError(t, tr.RecordUsage("s1", "anthropic", "claude", 2000, 900))

	e := tr.Breakdown()
	require.Len(t, e, 1)
	assert.Equal(t, 2000, e[0].Input)
	assert.Equal(t, 100, e[0].CacheCreation)
	assert.Equal(t, 800, e[0].CacheRead)
}

// TestTally_LoadWithoutCacheFields confirms tallies written before cache
// plumbing still decode: the absent fields become zero and the existing
// counts survive untouched.
func TestTally_LoadWithoutCacheFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	legacy := `{"sessions":{"s1":{"input":1000,"output":500,"provider":"openai","model":"gpt-4.1"}},"start_time":1736942400}`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o600))

	tr, err := NewTracker(path, fixedPricing)
	require.NoError(t, err)
	in, out := tr.SessionTokens("s1")
	assert.Equal(t, 1000, in)
	assert.Equal(t, 500, out)

	e := tr.Breakdown()
	require.Len(t, e, 1)
	assert.Zero(t, e[0].CacheCreation)
	assert.Zero(t, e[0].CacheRead)

	// Re-saving a cache-free record must not introduce the new keys, so an
	// older build reading the file back sees exactly what it wrote.
	require.NoError(t, tr.RecordUsage("s1", "openai", "gpt-4.1", 1100, 550))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "cache_creation")
	assert.NotContains(t, string(raw), "cache_read")
}

func TestTracker_ModelSwitchPreservesAttributionAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	pricing := func(slug, model string) (float64, float64) {
		if slug == "premium" && model == "large" {
			return 10, 20
		}
		return 1, 2
	}
	tr, err := NewTracker(path, pricing)
	require.NoError(t, err)
	require.NoError(t, tr.RecordUsage("mixed", "premium", "large", 1_000_000, 0))
	// Selecting another model without using tokens must not relabel the
	// previous model's usage, even though the aggregate compatibility label
	// records the most recent report.
	require.NoError(t, tr.RecordUsage("mixed", "budget", "small", 1_000_000, 0))
	rows := tr.Breakdown()
	require.Len(t, rows, 1)
	assert.Equal(t, "premium", rows[0].Provider)
	assert.Equal(t, "large", rows[0].Model)
	assert.Equal(t, 1, rows[0].ModelCount)
	assert.InDelta(t, 10, rows[0].USD, 1e-9)
	require.NoError(t, tr.RecordUsage("mixed", "budget", "small", 2_000_000, 0))
	assert.InDelta(t, 11, tr.SessionCost("mixed"), 1e-9, "$10 of premium usage plus $1 of budget usage must not become $2")

	tr, err = NewTracker(path, pricing)
	require.NoError(t, err)
	assert.InDelta(t, 11, tr.SessionCost("mixed"), 1e-9)
	// Switching back merges new deltas into the original model's bucket.
	require.NoError(t, tr.RecordUsage("mixed", "premium", "large", 3_000_000, 0))
	// A replayed report must neither add usage nor reprice previous tokens.
	require.NoError(t, tr.RecordUsage("mixed", "budget", "small", 2_500_000, 0))
	assert.InDelta(t, 21, tr.SessionCost("mixed"), 1e-9)
	assert.InDelta(t, 21, tr.TotalCost(), 1e-9)
	assert.InDelta(t, 21, tr.SessionCostsForIDs([]string{"mixed", "missing"}), 1e-9)
	in, out := tr.SessionTokens("mixed")
	assert.Equal(t, 3_000_000, in)
	assert.Zero(t, out)
	rows = tr.Breakdown()
	require.Len(t, rows, 1)
	assert.Equal(t, 2, rows[0].ModelCount)
	assert.InDelta(t, 21, rows[0].USD, 1e-9)
}

func TestTracker_LegacyTallySeedsRecordedModelBeforeSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	legacy := `{"sessions":{"s1":{"input":1000000,"output":0,"provider":"api","model":"large"}},"start_time":1736942400}`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o600))
	pricing := func(_, model string) (float64, float64) {
		if model == "large" {
			return 10, 0
		}
		return 1, 0
	}
	tr, err := NewTracker(path, pricing)
	require.NoError(t, err)
	assert.InDelta(t, 10, tr.SessionCost("s1"), 1e-9)
	require.NoError(t, tr.RecordUsage("s1", "api", "small", 2_000_000, 0))
	assert.InDelta(t, 11, tr.SessionCost("s1"), 1e-9)
	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, int64(1736942400), loaded.StartTime)
	require.Len(t, loaded.Sessions["s1"].UsageByModel, 2)
	assert.Equal(t, ModelUsage{Input: 1_000_000, Provider: "api", Model: "large"}, loaded.Sessions["s1"].UsageByModel[0])
	assert.Equal(t, ModelUsage{Input: 1_000_000, Provider: "api", Model: "small"}, loaded.Sessions["s1"].UsageByModel[1])
}

func TestTracker_ModelSwitchUsesEachModelsCacheRates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tally.json")
	pricing := func(_, model string) (float64, float64) {
		if model == "large" {
			return 10, 20
		}
		return 1, 2
	}
	cacheRates := func(_, model string) (float64, float64) {
		if model == "large" {
			return 0.1, 2
		}
		return 0.5, 1
	}
	tr, err := NewTracker(path, pricing)
	require.NoError(t, err)
	tr.SetCacheRates(cacheRates)
	// Large: 100k fresh + 200k cache writes + 700k reads + 100k output = $7.70.
	require.NoError(t, tr.RecordUsageWithCache("s1", "api", "large", 1_000_000, 100_000, 200_000, 700_000))
	// Small adds 400k fresh + 100k writes + 500k reads + 200k output = $1.15.
	require.NoError(t, tr.RecordUsageWithCache("s1", "api", "small", 2_000_000, 300_000, 300_000, 1_200_000))
	assert.InDelta(t, 8.85, tr.SessionCost("s1"), 1e-9)
	tr, err = NewTracker(path, pricing)
	require.NoError(t, err)
	tr.SetCacheRates(cacheRates)
	assert.InDelta(t, 8.85, tr.SessionCost("s1"), 1e-9)
	// Return to large: 100k fresh + 400k reads = $1.40 more.
	require.NoError(t, tr.RecordUsageWithCache("s1", "api", "large", 2_500_000, 300_000, 300_000, 1_600_000))
	assert.InDelta(t, 10.25, tr.SessionCost("s1"), 1e-9)
	rows := tr.Breakdown()
	require.Len(t, rows, 1)
	assert.Equal(t, 300_000, rows[0].CacheCreation)
	assert.Equal(t, 1_600_000, rows[0].CacheRead)
}
