package provider

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	configuredRetryMu sync.RWMutex
	configuredRetry   = DefaultRetryConfig()
)

// ConfiguredRetry returns the process-wide retry policy applied to provider
// streaming requests. Adapters pass it to DoWithRetry as their default.
func ConfiguredRetry() RetryConfig {
	configuredRetryMu.RLock()
	defer configuredRetryMu.RUnlock()
	return configuredRetry
}

// SetConfiguredRetry sets the process-wide retry policy. BuildRegistry calls
// it once at startup from config; it is safe for concurrent use.
func SetConfiguredRetry(c RetryConfig) {
	configuredRetryMu.Lock()
	defer configuredRetryMu.Unlock()
	configuredRetry = c.normalized()
}

// RetryConfig governs transient-failure retries for streaming provider
// requests. A request is retried only before its response stream begins —
// a mid-stream failure is surfaced to the caller, never silently replayed.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts including the first.
	// Values < 1 are treated as 1 (no retry).
	MaxAttempts int
	// BaseDelay is the backoff before the second attempt; it grows
	// exponentially (with jitter) up to MaxDelay.
	BaseDelay time.Duration
	// MaxDelay caps the exponential backoff. A server-supplied Retry-After
	// hint is honored even when it exceeds MaxDelay.
	MaxDelay time.Duration
}

// DefaultRetryConfig is the retry policy used when none is configured:
// three total attempts with exponential backoff and jitter.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    8 * time.Second,
	}
}

// RetryConfigForAttempts returns the default-timed policy with the given
// attempt budget. attempts < 1 is clamped to 1 (retries disabled).
func RetryConfigForAttempts(attempts int) RetryConfig {
	c := DefaultRetryConfig()
	if attempts < 1 {
		attempts = 1
	}
	c.MaxAttempts = attempts
	return c
}

// normalized fills zero-value fields with defaults so a zero RetryConfig
// behaves like DefaultRetryConfig rather than disabling backoff timing.
func (c RetryConfig) normalized() RetryConfig {
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 1
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = 500 * time.Millisecond
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 8 * time.Second
	}
	return c
}

// isRetryableStatus reports whether an HTTP status is worth retrying:
// rate limiting (429) and transient upstream failures (5xx).
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// DoWithRetry issues the request produced by newReq, retrying transient
// failures with exponential backoff and jitter. It returns the first
// response whose status is not retryable (including any 2xx) for the caller
// to handle, or the last transport error if every attempt failed before a
// response arrived.
//
// newReq must build a fresh *http.Request on each call because the request
// body is consumed per attempt. Retries happen only before the response
// body is read, so a streamed response is never replayed. The returned
// response body is owned by the caller.
func DoWithRetry(ctx context.Context, client *http.Client, cfg RetryConfig, newReq func() (*http.Request, error)) (*http.Response, error) {
	cfg = cfg.normalized()
	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := newReq()
		if err != nil {
			// Request construction errors are deterministic, not transient.
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			// A transport error occurs before any stream byte, so it is safe
			// to retry — unless the context itself is done.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == cfg.MaxAttempts {
				return nil, err
			}
			if werr := waitBackoff(ctx, cfg, attempt, 0); werr != nil {
				return nil, werr
			}
			continue
		}
		if isRetryableStatus(resp.StatusCode) && attempt < cfg.MaxAttempts {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			// Drain a bounded prefix and close so the connection can be
			// reused, then back off before the next attempt.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if werr := waitBackoff(ctx, cfg, attempt, retryAfter); werr != nil {
				return nil, werr
			}
			continue
		}
		// 2xx, a non-retryable status, or a retryable status on the final
		// attempt: hand the open response back for the caller to format.
		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("provider request failed after retries")
}

// waitBackoff sleeps for the backoff appropriate to attempt n (1-based),
// honoring an explicit Retry-After hint when present. It returns ctx.Err()
// if the context is cancelled while waiting.
func waitBackoff(ctx context.Context, cfg RetryConfig, attempt int, retryAfter time.Duration) error {
	delay := retryAfter
	if delay <= 0 {
		backoff := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt-1))
		if backoff > float64(cfg.MaxDelay) {
			backoff = float64(cfg.MaxDelay)
		}
		delay = time.Duration(backoff)
		// Half jitter: sleep a random duration in [delay/2, delay].
		half := delay / 2
		if half > 0 {
			delay = half + time.Duration(rand.Int63n(int64(half)+1))
		}
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// parseRetryAfter parses an HTTP Retry-After header value in delta-seconds
// form. The HTTP-date form is not supported and yields 0.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
