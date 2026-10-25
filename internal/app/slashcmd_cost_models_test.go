package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/cost"
)

func TestCostTableLabelsMixedModelUsage(t *testing.T) {
	tracker, err := cost.NewTracker(filepath.Join(t.TempDir(), "tally.json"), func(_, model string) (float64, float64) {
		if model == "large" {
			return 10, 0
		}
		return 1, 0
	})
	require.NoError(t, err)
	require.NoError(t, tracker.RecordUsage("session-1", "api", "large", 1_000_000, 0))
	require.NoError(t, tracker.RecordUsage("session-1", "api", "small", 1_000_000, 0))
	output := renderCostTable(tracker)
	assert.Contains(t, output, "api/large")
	assert.Contains(t, output, "Total: $10.0000")
	assert.NotContains(t, output, "api/small")
	assert.NotContains(t, output, "mixed")
	require.NoError(t, tracker.RecordUsage("session-1", "api", "small", 2_000_000, 0))
	output = renderCostTable(tracker)
	assert.Contains(t, output, "Total: $11.0000")
	assert.Contains(t, output, "mixed (2 models)")
	assert.Contains(t, output, "2.0M/0")
	assert.NotContains(t, output, "api/small")
}
