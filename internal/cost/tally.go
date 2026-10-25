// Package cost owns the per-session and global token-usage tally that the
// status line and /cost slash command surface.
//
// On-disk format (~/.packetcode/cost-tally.json):
//
//	{
//	  "sessions": {
//	    "<session-uuid>": {
//	      "input": 84000, "output": 12000,
//	      "provider": "openai", "model": "gpt-4.1"
//	    }
//	  },
//	  "start_time": 1736942400
//	}
//
// Per-session counts are recorded as high-water marks (only ever increase
// within a session) — the same pattern the existing Claude Code status
// line bash script uses.
package cost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/packetcode/packetcode/internal/atomicfile"
	"github.com/packetcode/packetcode/internal/provider"
)

// Tally is the on-disk root document.
type Tally struct {
	Sessions  map[string]SessionCost `json:"sessions"`
	StartTime int64                  `json:"start_time"`
}

// SessionCost keeps aggregate high-water marks for callers passing cumulative
// session usage, and attributes each increase to the provider/model that used
// it. Provider and Model retain the most recently recorded label for older
// readers; new readers price UsageByModel instead.
type SessionCost struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	// CacheCreation and CacheRead are the cached-input subsets of Input, not
	// additions to it — every provider that reports them counts them inside
	// its prompt total. priced() bills them at the cache rates rather than
	// the fresh input rate, and never adds them on top. Omitted when zero so
	// tallies written before cache plumbing keep their original shape, and
	// so absent fields decode to zero rather than to a wrong number.
	CacheCreation int          `json:"cache_creation,omitempty"`
	CacheRead     int          `json:"cache_read,omitempty"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	UsageByModel  []ModelUsage `json:"usage_by_model,omitempty"`
}

// ModelUsage is one provider/model's share of a session's aggregate counts.
// Legacy tallies have no shares; their existing usage remains attributed to
// the label they stored, since earlier model switches cannot be recovered.
type ModelUsage struct {
	Input         int    `json:"input"`
	Output        int    `json:"output"`
	CacheCreation int    `json:"cache_creation,omitempty"`
	CacheRead     int    `json:"cache_read,omitempty"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
}

// Empty returns a fresh tally rooted at the current time.
func Empty() *Tally {
	return &Tally{
		Sessions:  map[string]SessionCost{},
		StartTime: time.Now().Unix(),
	}
}

// Load reads a tally from path. Missing file returns Empty() with no error
// — first-run is normal.
func Load(path string) (*Tally, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Empty(), nil
		}
		return nil, fmt.Errorf("load tally: %w", err)
	}
	t := Empty()
	if err := json.Unmarshal(data, t); err != nil {
		return nil, fmt.Errorf("decode tally: %w", err)
	}
	if t.Sessions == nil {
		t.Sessions = map[string]SessionCost{}
	}
	return t, nil
}

// Save writes the tally atomically: temp file, fsync, rename, the same way
// sessions, job records, and config.toml are written. The previous
// temp-then-rename had no fsync, so a crash could publish a correctly named
// empty tally -- which Load then refuses to decode, taking /cost and the
// statusline down with it until the file is deleted by hand.
func (t *Tally) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600, ".tally.*.json.tmp")
}

// PricingFunc returns USD per 1M tokens for (provider, model). Callers (the
// agent or app shell) supply this lookup so the tally can be re-priced
// against whichever provider is active now.
type PricingFunc func(providerSlug, modelID string) (inputPer1M, outputPer1M float64)

// CacheRateFunc returns the multipliers on the input rate for tokens served
// from (read) and written into (write) the provider's prompt cache. Optional:
// a tracker without one uses the provider package defaults, which are right
// for OpenAI and for Anthropic reads.
type CacheRateFunc func(providerSlug, modelID string) (readMultiplier, writeMultiplier float64)

// Tracker is a thread-safe wrapper around a Tally tied to a single
// session. It implements the high-water-mark logic and is the type the
// agent loop and status line both consume.
type Tracker struct {
	path       string
	pricing    PricingFunc
	cacheRates CacheRateFunc
	mu         sync.Mutex
	tally      *Tally
}

// SetCacheRates installs the cache-multiplier lookup used when pricing
// cached input. Safe to call before any usage is recorded.
func (t *Tracker) SetCacheRates(fn CacheRateFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cacheRates = fn
}

func NewTracker(path string, pricing PricingFunc) (*Tracker, error) {
	t, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Tracker{path: path, pricing: pricing, tally: t}, nil
}

// RecordUsage records a session's totals without cache detail. It is the
// pre-cache-plumbing form, kept so callers that have no cache figures to
// offer are not forced to pass two zeros; those zeros would be
// indistinguishable from a provider genuinely reporting no cache hits, so
// it leaves any previously recorded cache counts alone.
func (t *Tracker) RecordUsage(sessionID, providerSlug, modelID string, input, output int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := t.tally.Sessions[sessionID]
	return t.recordUsageLocked(sessionID, providerSlug, modelID, input, output, cur.CacheCreation, cur.CacheRead)
}

// RecordUsageWithCache applies the high-water-mark rule for the given
// session: each count is *replaced* by the max of the existing value and the
// new value. This matches the Claude Code status line behaviour where a
// stream completion's running totals are the source of truth.
//
// cacheCreation and cacheRead are subsets of input, never addends — the
// caller passes the same cumulative figures the session tracks, and pricing
// still derives from input alone.
func (t *Tracker) RecordUsageWithCache(sessionID, providerSlug, modelID string, input, output, cacheCreation, cacheRead int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.recordUsageLocked(sessionID, providerSlug, modelID, input, output, cacheCreation, cacheRead)
}

func (t *Tracker) recordUsageLocked(sessionID, providerSlug, modelID string, input, output, cacheCreation, cacheRead int) error {
	cur := t.tally.Sessions[sessionID]
	if len(cur.UsageByModel) == 0 && (cur.Input > 0 || cur.Output > 0 || cur.CacheCreation > 0 || cur.CacheRead > 0) {
		cur.UsageByModel = []ModelUsage{{
			Input: cur.Input, Output: cur.Output, CacheCreation: cur.CacheCreation, CacheRead: cur.CacheRead,
			Provider: cur.Provider, Model: cur.Model,
		}}
	}
	delta := ModelUsage{
		Input: max(0, input-cur.Input), Output: max(0, output-cur.Output),
		CacheCreation: max(0, cacheCreation-cur.CacheCreation), CacheRead: max(0, cacheRead-cur.CacheRead),
		Provider: providerSlug, Model: modelID,
	}
	if delta.Input > 0 || delta.Output > 0 || delta.CacheCreation > 0 || delta.CacheRead > 0 {
		index := -1
		for i, usage := range cur.UsageByModel {
			if usage.Provider == providerSlug && usage.Model == modelID {
				index = i
				break
			}
		}
		if index < 0 {
			cur.UsageByModel = append(cur.UsageByModel, delta)
		} else {
			usage := &cur.UsageByModel[index]
			usage.Input += delta.Input
			usage.Output += delta.Output
			usage.CacheCreation += delta.CacheCreation
			usage.CacheRead += delta.CacheRead
		}
	}
	if input > cur.Input {
		cur.Input = input
	}
	if output > cur.Output {
		cur.Output = output
	}
	if cacheCreation > cur.CacheCreation {
		cur.CacheCreation = cacheCreation
	}
	if cacheRead > cur.CacheRead {
		cur.CacheRead = cacheRead
	}
	cur.Provider = providerSlug
	cur.Model = modelID
	t.tally.Sessions[sessionID] = cur
	return t.tally.Save(t.path)
}

// SessionCost returns the cumulative USD cost for the named session.
func (t *Tracker) SessionCost(sessionID string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	sc := t.tally.Sessions[sessionID]
	return t.priced(sc)
}

// TotalCost sums every session's cost using the current pricing function.
// Pricing changes propagate immediately — historical token counts stay,
// but their dollar value is computed at read time.
func (t *Tracker) TotalCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total float64
	for _, sc := range t.tally.Sessions {
		total += t.priced(sc)
	}
	return total
}

// SessionCostsForIDs sums the cost of every named session id. Unknown
// ids contribute 0 silently — used by the /jobs panel to subtotal the
// cost across a job's sub-session ids.
func (t *Tracker) SessionCostsForIDs(ids []string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total float64
	for _, id := range ids {
		sc, ok := t.tally.Sessions[id]
		if !ok {
			continue
		}
		total += t.priced(sc)
	}
	return total
}

// SessionTokens returns the (input, output) token counts for a session.
func (t *Tracker) SessionTokens(sessionID string) (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sc := t.tally.Sessions[sessionID]
	return sc.Input, sc.Output
}

// Reset clears the tally and resets start_time.
func (t *Tracker) Reset() error {
	t.mu.Lock()
	t.tally = Empty()
	defer t.mu.Unlock()
	return t.tally.Save(t.path)
}

// StartTime returns the unix timestamp the current tally window began at.
func (t *Tracker) StartTime() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tally.StartTime
}

// Breakdown returns a snapshot of every session's cost record.
func (t *Tracker) Breakdown() []Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Entry, 0, len(t.tally.Sessions))
	for id, sc := range t.tally.Sessions {
		providerSlug, modelID := sc.Provider, sc.Model
		if len(sc.UsageByModel) == 1 {
			// A no-usage switch or stale report can update the compatibility
			// label without adding a bucket. Label the actual usage instead.
			providerSlug, modelID = sc.UsageByModel[0].Provider, sc.UsageByModel[0].Model
		}
		out = append(out, Entry{
			SessionID:     id,
			Input:         sc.Input,
			Output:        sc.Output,
			CacheCreation: sc.CacheCreation,
			CacheRead:     sc.CacheRead,
			Provider:      providerSlug,
			Model:         modelID,
			ModelCount:    len(sc.UsageByModel),
			USD:           t.priced(sc),
		})
	}
	return out
}

// Entry is a per-session row in the breakdown. CacheCreation and CacheRead
// are subsets of Input; a row where they sum close to Input is one the
// provider served almost entirely from cache.
type Entry struct {
	SessionID     string
	Input         int
	Output        int
	CacheCreation int
	CacheRead     int
	Provider      string
	Model         string
	ModelCount    int
	USD           float64
}

func (t *Tracker) priced(sc SessionCost) float64 {
	if len(sc.UsageByModel) > 0 {
		var total float64
		for _, usage := range sc.UsageByModel {
			total += t.pricedUsage(usage)
		}
		return total
	}
	return t.pricedUsage(ModelUsage{
		Input: sc.Input, Output: sc.Output, CacheCreation: sc.CacheCreation, CacheRead: sc.CacheRead,
		Provider: sc.Provider, Model: sc.Model,
	})
}

func (t *Tracker) pricedUsage(sc ModelUsage) float64 {
	if t.pricing == nil {
		return 0
	}
	in, out := t.pricing(sc.Provider, sc.Model)
	// Cached input is a subset of Input billed at a fraction of the fresh
	// rate. The session and job tallies were corrected to use
	// provider.EstimateCost; this tally carried the old formula, so /cost and
	// the statusline showed a different dollar figure from the session for
	// the same tokens -- roughly 6x on a cache-heavy run.
	read, write := provider.CacheReadMultiplier, provider.CacheWriteMultiplier
	if t.cacheRates != nil {
		read, write = t.cacheRates(sc.Provider, sc.Model)
	}
	return provider.EstimateCost(sc.Input, sc.Output, sc.CacheRead, sc.CacheCreation, in, out, read, write)
}
