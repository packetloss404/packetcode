package provider

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// waitCancelled blocks until ctx is done or the deadline elapses, returning
// whether ctx was cancelled.
func waitCancelled(ctx context.Context, within time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(within):
		return false
	}
}

func TestStallGuard_AbsentTickCancels(t *testing.T) {
	const timeout = 25 * time.Millisecond
	g, ctx := NewStallGuard(context.Background(), timeout)
	defer g.Stop()

	if ctx.Err() != nil {
		t.Fatalf("ctx should not be cancelled immediately: %v", ctx.Err())
	}

	// No Tick: ctx must cancel shortly after the timeout.
	if !waitCancelled(ctx, timeout+200*time.Millisecond) {
		t.Fatalf("expected ctx to be cancelled after stall timeout with no Tick")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", ctx.Err())
	}
}

func TestStallGuard_TickKeepsAlive(t *testing.T) {
	const timeout = 30 * time.Millisecond
	g, ctx := NewStallGuard(context.Background(), timeout)
	defer g.Stop()

	// Tick several times at sub-timeout intervals so the guard never fires.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 8; i++ {
			time.Sleep(timeout / 3)
			g.Tick()
		}
	}()

	// Across a span well beyond a single timeout, ctx must stay alive because
	// of regular Ticks.
	if waitCancelled(ctx, 8*(timeout/3)+timeout/2) {
		t.Fatalf("ctx was cancelled despite regular Ticks: %v", ctx.Err())
	}
	<-done

	// After Ticks stop, the guard should eventually fire.
	if !waitCancelled(ctx, timeout+200*time.Millisecond) {
		t.Fatalf("expected ctx to cancel after Ticks ceased")
	}
}

func TestStallGuard_StopPreventsCancellation(t *testing.T) {
	const timeout = 25 * time.Millisecond
	g, ctx := NewStallGuard(context.Background(), timeout)

	g.Stop()

	// After Stop, ctx is cancelled by Stop's teardown (context.Canceled), but
	// it must NOT be a delayed stall-driven cancellation — it is immediate.
	if ctx.Err() == nil {
		t.Fatalf("expected ctx cancelled by Stop teardown")
	}

	// Stop is idempotent: calling again must not panic.
	g.Stop()
	g.Stop()

	// Tick after Stop is a no-op and must not panic.
	g.Tick()
}

func TestStallGuard_StopBeforeFireNoLeak(t *testing.T) {
	const timeout = 20 * time.Millisecond
	before := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		g, ctx := NewStallGuard(context.Background(), timeout)
		g.Tick()
		g.Stop()
		_ = ctx
	}

	// Allow any finished timers/goroutines to be reaped.
	time.Sleep(2 * timeout)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("possible goroutine leak: before=%d after=%d", before, after)
	}
}

func TestStallGuard_DisabledZeroTimeoutReturnsParent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
	}{
		{"zero", 0},
		{"negative", -10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()

			g, ctx := NewStallGuard(parent, tc.timeout)
			defer g.Stop()

			// Must be the parent context unchanged.
			if ctx != parent {
				t.Fatalf("disabled guard must return the parent ctx unchanged")
			}

			// Tick/Stop are no-ops and must not panic or cancel.
			g.Tick()
			g.Tick()

			// The guard must never cancel on its own, even past any plausible
			// timeout window.
			if waitCancelled(ctx, 60*time.Millisecond) {
				t.Fatalf("disabled guard cancelled ctx; err=%v", ctx.Err())
			}

			// Stop must not panic and must be idempotent on a disabled guard.
			g.Stop()
			g.Stop()
		})
	}
}

func TestStallGuard_ConcurrentTicks(t *testing.T) {
	const timeout = 40 * time.Millisecond
	g, ctx := NewStallGuard(context.Background(), timeout)
	defer g.Stop()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					g.Tick()
					time.Sleep(timeout / 10)
				}
			}
		}()
	}

	// With many concurrent Ticks the ctx must stay alive (and the race
	// detector must find no data races on Tick).
	if waitCancelled(ctx, 3*timeout) {
		t.Fatalf("ctx cancelled despite continuous concurrent Ticks: %v", ctx.Err())
	}
	close(stop)
	wg.Wait()
}

func TestConfiguredStallTimeout_DefaultAndRoundTrip(t *testing.T) {
	// Save and restore process-wide state so the test is self-contained.
	orig := func() time.Duration {
		configuredStallTimeoutMu.RLock()
		defer configuredStallTimeoutMu.RUnlock()
		return configuredStallTimeout
	}()
	t.Cleanup(func() { SetConfiguredStallTimeout(orig) })

	// Unset (zero) -> default.
	SetConfiguredStallTimeout(0)
	if got := ConfiguredStallTimeout(); got != DefaultStallTimeout {
		t.Fatalf("unset: got %v, want default %v", got, DefaultStallTimeout)
	}
	if DefaultStallTimeout != 60*time.Second {
		t.Fatalf("DefaultStallTimeout = %v, want 60s", DefaultStallTimeout)
	}

	// Positive value round-trips.
	SetConfiguredStallTimeout(5 * time.Second)
	if got := ConfiguredStallTimeout(); got != 5*time.Second {
		t.Fatalf("round-trip: got %v, want 5s", got)
	}

	// Negative value is stored verbatim (the "disable at call site" sentinel).
	SetConfiguredStallTimeout(-1)
	if got := ConfiguredStallTimeout(); got != -1 {
		t.Fatalf("negative: got %v, want -1", got)
	}

	// Reset back to default behavior.
	SetConfiguredStallTimeout(0)
	if got := ConfiguredStallTimeout(); got != DefaultStallTimeout {
		t.Fatalf("after reset: got %v, want default %v", got, DefaultStallTimeout)
	}
}
