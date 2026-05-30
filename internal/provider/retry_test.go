package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetry(attempts int) RetryConfig {
	return RetryConfig{MaxAttempts: attempts, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
}

func newReqFor(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, url, nil)
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504}
	for _, c := range retryable {
		if !isRetryableStatus(c) {
			t.Errorf("status %d should be retryable", c)
		}
	}
	notRetryable := []int{200, 201, 400, 401, 403, 404, 422}
	for _, c := range notRetryable {
		if isRetryableStatus(c) {
			t.Errorf("status %d should not be retryable", c)
		}
	}
}

func TestDoWithRetrySucceedsAfterTransient429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := DoWithRetry(context.Background(), srv.Client(), fastRetry(3), newReqFor(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("server calls = %d, want 2", got)
	}
}

func TestDoWithRetryExhaustsAndReturnsLastResponse(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	resp, err := DoWithRetry(context.Background(), srv.Client(), fastRetry(3), newReqFor(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("server calls = %d, want 3 (all attempts)", got)
	}
}

func TestDoWithRetryNonRetryableStatusNoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := DoWithRetry(context.Background(), srv.Client(), fastRetry(3), newReqFor(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestDoWithRetryCancelledContext(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := DoWithRetry(ctx, srv.Client(), fastRetry(3), newReqFor(srv.URL))
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("server calls = %d, want 0 (cancelled before dispatch)", got)
	}
}

func TestDoWithRetryTransportErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // force connection-refused transport errors

	_, err := DoWithRetry(context.Background(), http.DefaultClient, fastRetry(2), newReqFor(url))
	if err == nil {
		t.Fatal("expected transport error after exhausting retries")
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Errorf("parseRetryAfter(2) = %v, want 2s", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(empty) = %v, want 0", got)
	}
	if got := parseRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT"); got != 0 {
		t.Errorf("parseRetryAfter(http-date) = %v, want 0", got)
	}
}

func TestRetryConfigForAttempts(t *testing.T) {
	if c := RetryConfigForAttempts(0); c.MaxAttempts != 1 {
		t.Errorf("attempts(0) MaxAttempts = %d, want 1", c.MaxAttempts)
	}
	if c := RetryConfigForAttempts(5); c.MaxAttempts != 5 {
		t.Errorf("attempts(5) MaxAttempts = %d, want 5", c.MaxAttempts)
	}
}
