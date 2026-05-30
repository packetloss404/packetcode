package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/packetcode/packetcode/internal/provider"
)

func TestChatCompletionStreamsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, APIKey: "test"}
	ev, err := client.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	var text string
	var sawDone bool
	for e := range ev {
		switch e.Type {
		case provider.EventTextDelta:
			text += e.TextDelta
		case provider.EventDone:
			sawDone = true
		case provider.EventError:
			t.Fatalf("unexpected stream error: %v", e.Error)
		}
	}
	if text != "Hello" {
		t.Fatalf("text = %q, want %q", text, "Hello")
	}
	if !sawDone {
		t.Fatal("expected an EventDone")
	}
}

// TestChatCompletionStallTimeoutAborts drives a real stalled stream end-to-end:
// the server opens the SSE response (so ChatCompletion returns successfully and
// the parse loop is reading the body) and then goes silent forever. The stall
// guard must cancel the request within the configured window, close the
// connection, unblock the parse loop's read, and surface an EventError.
//
// This is the integration test the per-call stall timeout round requires: it
// exercises the actual adapter parse loop against a genuine stalled HTTP body,
// not the StallGuard in isolation.
func TestChatCompletionStallTimeoutAborts(t *testing.T) {
	prev := provider.ConfiguredStallTimeout()
	provider.SetConfiguredStallTimeout(150 * time.Millisecond)
	defer provider.SetConfiguredStallTimeout(prev)

	// release lets the handler return once the client side is done, so the
	// test server can shut down cleanly.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush() // open the stream, then send nothing (silent stall).
		}
		select {
		case <-r.Context().Done(): // client cancelled (the stall guard fired).
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)

	client := &Client{BaseURL: srv.URL, APIKey: "test"}
	ev, err := client.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-ev:
			if !ok {
				t.Fatal("stream closed without a stall EventError")
			}
			if e.Type == provider.EventError {
				if e.Error == nil {
					t.Fatal("EventError with nil error")
				}
				// Parent context was never cancelled, so this must be the
				// stall message rather than a propagated cancellation.
				if got := e.Error.Error(); got != "provider stream stalled (no data received)" {
					t.Fatalf("unexpected error %q, want the stall message", got)
				}
				return
			}
		case <-deadline:
			t.Fatal("stall timeout did not fire within the window")
		}
	}
}

// TestChatCompletionParentCancelPropagates verifies that cancelling the parent
// context (e.g. Ctrl+C) still aborts the stream and surfaces the parent's
// cancellation cause rather than the stall message.
func TestChatCompletionParentCancelPropagates(t *testing.T) {
	prev := provider.ConfiguredStallTimeout()
	provider.SetConfiguredStallTimeout(10 * time.Second) // long: stall must not fire first.
	defer provider.SetConfiguredStallTimeout(prev)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{BaseURL: srv.URL, APIKey: "test"}
	ev, err := client.ChatCompletion(ctx, provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		cancel()
		t.Fatalf("ChatCompletion: %v", err)
	}

	time.AfterFunc(100*time.Millisecond, cancel)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-ev:
			if !ok {
				t.Fatal("stream closed without a cancellation EventError")
			}
			if e.Type == provider.EventError {
				if e.Error != context.Canceled {
					t.Fatalf("error = %v, want context.Canceled", e.Error)
				}
				return
			}
		case <-deadline:
			cancel()
			t.Fatal("parent cancellation did not abort the stream")
		}
	}
}
