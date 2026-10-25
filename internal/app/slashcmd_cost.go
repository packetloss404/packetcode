package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/cost"
)

// handleCostCommand renders the cost breakdown (bare) or resets the
// tally (reset, with --yes required). Reset is gated because resetting
// StartTime is unrecoverable.
func (a *App) handleCostCommand(args []string) (tea.Model, tea.Cmd) {
	if a.deps.CostTracker == nil {
		a.conversation.AppendSystem("cost: cost tracker not available")
		return a, nil
	}
	reset, yes, err := parseCostArgs(args)
	if err != nil {
		a.conversation.AppendSystem("cost: " + err.Error())
		return a, nil
	}
	if !reset {
		a.conversation.AppendSystem(renderCostTable(a.deps.CostTracker))
		return a, nil
	}
	if !yes {
		a.conversation.AppendSystem("cost: refusing to reset without --yes; re-run: /cost reset --yes")
		return a, nil
	}
	if err := a.deps.CostTracker.Reset(); err != nil {
		a.conversation.AppendSystem("cost: reset failed: " + err.Error())
		return a, nil
	}
	a.refreshTopBar()
	a.conversation.AppendSystem("cost tally cleared")
	return a, nil
}

// renderCostTable formats the bare /cost output: total line, top-5 rows,
// optional "[showing top 5 of N]" footer.
func renderCostTable(tracker *cost.Tracker) string {
	total := tracker.TotalCost()
	entries := tracker.Breakdown()
	start := time.Unix(tracker.StartTime(), 0).UTC().Format("2006-01-02 15:04 UTC")

	if len(entries) == 0 {
		return "no usage recorded yet"
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].USD > entries[j].USD
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Total: $%.4f (since %s)\n\n", total, start)
	b.WriteString("SESSION   PROV/MODEL              TOK(IN/OUT)   CACHE(R/W)    USD\n")

	top := entries
	if len(top) > 5 {
		top = top[:5]
	}
	for _, e := range top {
		provModel := e.Provider
		if e.Model != "" {
			if provModel != "" {
				provModel += "/" + e.Model
			} else {
				provModel = e.Model
			}
		}
		if provModel == "" {
			provModel = "(none)"
		}
		if e.ModelCount > 1 {
			provModel = fmt.Sprintf("mixed (%d models)", e.ModelCount)
		}
		tok := fmt.Sprintf("%s/%s", fmtTokensShort(e.Input), fmtTokensShort(e.Output))
		// Read/write, not in/out: cached tokens are already inside e.Input, so
		// the column reports how much of that input was served from cache
		// (read) versus written into it (creation), never a further total.
		cache := fmt.Sprintf("%s/%s", fmtTokensShort(e.CacheRead), fmtTokensShort(e.CacheCreation))
		fmt.Fprintf(&b, "%s  %s  %s  %s  $%.4f\n",
			padRight(shortID(e.SessionID), 8),
			padRight(trunc(provModel, 22), 22),
			padRight(tok, 12),
			padRight(cache, 12),
			e.USD,
		)
	}
	if len(entries) > 5 {
		fmt.Fprintf(&b, "[showing top 5 of %d sessions]\n", len(entries))
	}
	return strings.TrimRight(b.String(), "\n")
}

// fmtTokensShort renders a token count with a K suffix at ≥10k or an M
// suffix at ≥1M. Below 10k, the raw integer is returned.
func fmtTokensShort(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dK", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
