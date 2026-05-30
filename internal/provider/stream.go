package provider

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DefaultStallTimeout is the built-in default per-call stall timeout. If a
// streaming call produces no progress (no Tick) within this duration, the
// stall guard cancels the derived context so the caller can abort and retry.
const DefaultStallTimeout = 60 * time.Second

// configuredStallTimeout holds the process-wide stall timeout. It is set once
// during startup (from config) and read at the call site that builds a
// StallGuard. This mirrors the ConfiguredRetry pattern in retry.go.
//
// A zero (unset) value means "use the default": ConfiguredStallTimeout returns
// DefaultStallTimeout. A negative value is stored verbatim and returned as-is;
// callers treat timeout<=0 as "disable the guard" when they pass it to
// NewStallGuard.
var (
	configuredStallTimeout   time.Duration
	configuredStallTimeoutMu sync.RWMutex
)

// ConfiguredStallTimeout returns the process-wide stall timeout. If none has
// been configured (the stored value is zero), it returns DefaultStallTimeout.
func ConfiguredStallTimeout() time.Duration {
	configuredStallTimeoutMu.RLock()
	defer configuredStallTimeoutMu.RUnlock()
	if configuredStallTimeout == 0 {
		return DefaultStallTimeout
	}
	return configuredStallTimeout
}

// SetConfiguredStallTimeout sets the process-wide stall timeout. Passing zero
// resets it to the default behavior (ConfiguredStallTimeout returns
// DefaultStallTimeout). A negative value is stored verbatim; ConfiguredStallTimeout
// will return it unchanged, and a caller passing it to NewStallGuard disables
// the guard. Use a negative value to deliberately turn the guard off.
func SetConfiguredStallTimeout(d time.Duration) {
	configuredStallTimeoutMu.Lock()
	defer configuredStallTimeoutMu.Unlock()
	configuredStallTimeout = d
}

// StallGuard watches a streaming call for progress. The caller arms it with a
// timeout and calls Tick each time progress is observed (e.g. a chunk parsed).
// If timeout elapses with no Tick, the guard cancels its derived context,
// causing the in-flight call to abort. The caller MUST defer Stop() to release
// the underlying timer and any resources.
//
// All methods are safe for concurrent use. Tick is typically called from a
// parse goroutine while another goroutine consumes the derived context.
type StallGuard struct {
	timeout time.Duration

	cancel context.CancelFunc

	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

// NewStallGuard returns a StallGuard and a context derived from parent. If no
// Tick occurs within timeout, the derived context is cancelled (with
// context.Canceled). The caller MUST defer Stop() on the returned guard.
//
// If timeout <= 0 the guard is disabled: the parent context is returned
// unchanged and Tick/Stop are no-ops. (The returned guard is still valid and
// safe to call.)
func NewStallGuard(parent context.Context, timeout time.Duration) (*StallGuard, context.Context) {
	if timeout <= 0 {
		// Disabled guard: no derived context, no timer, Tick/Stop are no-ops.
		return &StallGuard{}, parent
	}

	ctx, cancel := context.WithCancel(parent)
	g := &StallGuard{
		timeout: timeout,
		cancel:  cancel,
	}
	g.timer = time.AfterFunc(timeout, func() {
		// Fired without a Tick within the window: cancel the derived context.
		g.cancel()
	})
	return g, ctx
}

// Tick resets the stall timer. It is safe to call concurrently. Calling Tick on
// a disabled or stopped guard is a no-op.
func (g *StallGuard) Tick() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer == nil || g.stopped {
		return
	}
	// Stop and reset. Even if the timer already fired (and cancelled the ctx),
	// resetting is harmless: the context is already cancelled and a later reset
	// will not "un-cancel" it.
	g.timer.Stop()
	g.timer.Reset(g.timeout)
}

// Stop releases the timer and cancels the derived context cleanup. It is
// idempotent and safe to call multiple times (typically via defer). Stop does
// not by itself cancel the derived context due to a stall; it only tears the
// guard down. Once Stop returns, the stall timer can no longer fire.
func (g *StallGuard) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	if g.timer != nil {
		g.timer.Stop()
	}
	// Release the context resources held by WithCancel. For a stall-cancelled
	// context this is a no-op; for a normally-finished call it frees the
	// goroutine/resources associated with the derived context.
	if g.cancel != nil {
		g.cancel()
	}
}

// StreamHaltError disambiguates why a streaming parse loop terminated when its
// stall-guarded context (sctx) is done. It returns nil if sctx is still live.
//
// If the parent context is also done, the halt was a genuine
// cancellation/deadline from the caller (e.g. Ctrl+C), and the parent's cause
// is returned so the agent path can render the friendlier "turn cancelled"
// message. Otherwise the derived context was cancelled by the StallGuard alone,
// meaning the provider connected but went silent, and a descriptive stall error
// is returned.
//
// Because the streaming HTTP request/body is bound to sctx, a StallGuard firing
// closes the underlying connection, which unblocks the parse loop's blocked read
// (bufio.Scanner). The loop then observes sctx.Err() (or a read error) and calls
// this helper to produce the right user-facing error.
func StreamHaltError(parent, sctx context.Context) error {
	if sctx.Err() == nil {
		return nil
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return fmt.Errorf("provider stream stalled (no data received)")
}
