// Package openaicompat implements the request, response, and SSE streaming
// logic shared by every OpenAI-compatible chat-completions endpoint.
//
// Three of packetcode's built-in providers (OpenAI itself, MiniMax, OpenRouter)
// speak this protocol; each one wraps a Client with provider-specific base
// URL, headers, model list, and pricing. The wrapper implements the public
// provider.Provider interface; this package never imports lipgloss or
// otherwise concerns itself with branding or UI.
package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/packetcode/packetcode/internal/provider"
)

// HeaderFunc lets a wrapper inject extra headers (e.g. OpenRouter's
// HTTP-Referer and X-Title) on every outbound request.
type HeaderFunc func(req *http.Request)

// Client speaks the OpenAI chat-completions protocol against a configurable
// base URL. It is safe for concurrent use.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// ExtraHeaders is invoked just before each request is sent. Wrappers
	// use it to add provider-specific headers without mutating Client state.
	ExtraHeaders HeaderFunc
}

// NewClient returns a Client with a sensible default HTTP client. A nil
// HTTPClient is replaced lazily on first use.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: 0, // streaming — no overall timeout, rely on context
		},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient == nil {
		return http.DefaultClient
	}
	return c.HTTPClient
}

// modelsResponse is the OpenAI /v1/models payload.
type modelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// ListModels calls GET <base>/models. The response is intentionally
// model-agnostic: each wrapper layers its own filtering, pricing, and
// context-window metadata on top of the bare ID list.
func (c *Client) ListModels(ctx context.Context) ([]provider.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req, false)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := provider.ReadErrorBody(resp.Body)
		return nil, fmt.Errorf("list models: status %d: %s", resp.StatusCode, extractAPIErrorMessage(body))
	}

	var parsed modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	out := make([]provider.Model, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, provider.Model{
			ID:          m.ID,
			DisplayName: m.ID,
		})
	}
	return out, nil
}

// ValidateKey performs a 5-second HEAD-equivalent (a GET /models with a
// short timeout) to confirm the key authenticates. Any 2xx is success.
func (c *Client) ValidateKey(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("api key is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	c.applyHeadersWithKey(req, false, apiKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("validate key: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body := provider.ReadErrorBody(resp.Body)
		return fmt.Errorf("validate key: status %d: %s", resp.StatusCode, extractAPIErrorMessage(body))
	}
	return nil
}

// chatRequestBody is the wire format for POST /chat/completions.
type chatRequestBody struct {
	Model         string          `json:"model"`
	Messages      []wireMessage   `json:"messages"`
	Tools         []wireTool      `json:"tools,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions *wireStreamOpts `json:"stream_options,omitempty"`
}

type wireStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

func (m wireMessage) MarshalJSON() ([]byte, error) {
	type wireMessageAlias wireMessage
	if m.Role == string(provider.RoleTool) {
		return json.Marshal(struct {
			Role       string         `json:"role"`
			Content    string         `json:"content"`
			ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
			ToolCallID string         `json:"tool_call_id,omitempty"`
			Name       string         `json:"name,omitempty"`
		}{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		})
	}
	return json.Marshal(wireMessageAlias(m))
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string         `json:"type"`
	Function wireToolSchema `json:"function"`
}

type wireToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func toWireMessages(msgs []provider.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		wm := wireMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if len(m.ToolCalls) > 0 {
			wm.ToolCalls = make([]wireToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				wm.ToolCalls[i] = wireToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: wireFunctionCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				}
			}
		}
		out = append(out, wm)
	}
	return out
}

func toWireTools(tools []provider.ToolDefinition) []wireTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireTool, len(tools))
	for i, t := range tools {
		out[i] = wireTool{
			Type: "function",
			Function: wireToolSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		}
	}
	return out
}

// ChatCompletion opens a streaming chat completion. The returned channel is
// closed when the stream terminates. Errors before the first byte arrives
// are returned synchronously; errors mid-stream surface as EventError.
func (c *Client) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	body := chatRequestBody{
		Model:         req.Model,
		Messages:      toWireMessages(req.Messages),
		Tools:         toWireTools(req.Tools),
		Stream:        true,
		StreamOptions: &wireStreamOpts{IncludeUsage: true},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}

	newReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		c.applyHeaders(httpReq, true)
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	}
	resp, err := provider.DoWithRetry(ctx, c.httpClient(), provider.ConfiguredRetry(), newReq)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		errBody := provider.ReadErrorBody(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %d: %s",
			resp.StatusCode, extractAPIErrorMessage(errBody))
	}

	ch := make(chan provider.StreamEvent, 8)
	go parseSSE(ctx, resp.Body, ch)
	return ch, nil
}

// extractAPIErrorMessage pulls the human-readable message out of a JSON
// error body if it has the canonical {error:{message:...}} shape (OpenAI,
// MiniMax, OpenRouter all do). Falls back to the raw trimmed body so
// non-JSON responses still render something useful.
func extractAPIErrorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	var wrapper struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Error.Message != "" {
		return wrapper.Error.Message
	}
	return trimmed
}

func (c *Client) applyHeaders(req *http.Request, json bool) {
	c.applyHeadersWithKey(req, json, c.APIKey)
}

func (c *Client) applyHeadersWithKey(req *http.Request, json bool, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if json {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.ExtraHeaders != nil {
		c.ExtraHeaders(req)
	}
}

// chatStreamChunk is one SSE frame from /chat/completions.
type chatStreamChunk struct {
	Choices []struct {
		Index        int             `json:"index"`
		Delta        chatStreamDelta `json:"delta"`
		FinishReason *string         `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage,omitempty"`
}

type chatStreamDelta struct {
	Role      string               `json:"role,omitempty"`
	Content   string               `json:"content,omitempty"`
	ToolCalls []chatStreamToolCall `json:"tool_calls,omitempty"`
}

type chatStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// parseSSE reads OpenAI-style SSE frames and translates them into
// provider.StreamEvent values on ch. The function closes ch and the body
// before returning.
//
// ctx is checked once per scanner iteration so Ctrl+C from the App layer
// unblocks the parser even when the server's TCP keep-alive hides the
// body close from bufio.Scanner. On cancel we emit EventError with the
// ctx.Err() cause (context.Canceled / DeadlineExceeded) so the agent
// path surfaces the friendlier "turn cancelled" rendering.
func parseSSE(ctx context.Context, body io.ReadCloser, ch chan<- provider.StreamEvent) {
	defer close(ch)
	defer body.Close()

	// activeCalls tracks which indices we've already emitted Start for.
	activeCalls := map[int]bool{}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
			return
		}
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			// End any tool calls still open (defensive — most providers
			// emit finish_reason before [DONE]).
			for idx := range activeCalls {
				ch <- provider.StreamEvent{
					Type:     provider.EventToolCallEnd,
					ToolCall: &provider.ToolCallDelta{Index: idx},
				}
			}
			ch <- provider.StreamEvent{Type: provider.EventDone}
			return
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("parse SSE chunk: %w", err)}
			return
		}

		for _, choice := range chunk.Choices {
			hasToolCalls := len(choice.Delta.ToolCalls) > 0
			if choice.Delta.Content != "" && !hasToolCalls {
				ch <- provider.StreamEvent{
					Type:      provider.EventTextDelta,
					TextDelta: choice.Delta.Content,
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				if !activeCalls[tc.Index] {
					ch <- provider.StreamEvent{
						Type: provider.EventToolCallStart,
						ToolCall: &provider.ToolCallDelta{
							Index: tc.Index,
							ID:    tc.ID,
							Name:  tc.Function.Name,
						},
					}
					activeCalls[tc.Index] = true
				}
				if tc.Function.Arguments != "" || tc.Function.Name != "" || tc.ID != "" {
					ch <- provider.StreamEvent{
						Type: provider.EventToolCallDelta,
						ToolCall: &provider.ToolCallDelta{
							Index:          tc.Index,
							ID:             tc.ID,
							Name:           tc.Function.Name,
							ArgumentsDelta: tc.Function.Arguments,
						},
					}
				}
			}
			if choice.FinishReason != nil {
				reason := *choice.FinishReason
				if reason == "length" || reason == "content_filter" {
					ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("tool call stream stopped with finish_reason %q", reason)}
					return
				}
				if reason != "tool_calls" && len(activeCalls) > 0 {
					ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("tool call stream stopped with finish_reason %q", reason)}
					return
				}
				for idx := range activeCalls {
					ch <- provider.StreamEvent{
						Type:     provider.EventToolCallEnd,
						ToolCall: &provider.ToolCallDelta{Index: idx},
					}
					delete(activeCalls, idx)
				}
			}
		}

		if chunk.Usage != nil {
			ch <- provider.StreamEvent{
				Type: provider.EventDone,
				Usage: &provider.Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
				},
			}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
		return
	}
	if len(activeCalls) > 0 {
		ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("tool call stream ended before completion")}
		return
	}
	ch <- provider.StreamEvent{Type: provider.EventDone}
}
