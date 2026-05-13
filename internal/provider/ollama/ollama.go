// Package ollama implements provider.Provider against an Ollama instance.
//
// Two characteristics make Ollama the odd one out:
//  1. There's no API key. ValidateKey is a no-op; "validation" really
//     means "is the daemon reachable on this host?"
//  2. The streaming format is NDJSON (one JSON object per line), not SSE.
//
// Tool calling support is per-model. SupportsTools lets callers decide
// whether to send native tool definitions or run without tools for that
// model.
package ollama

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

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/provider"
)

const (
	defaultBaseURL = "http://localhost:11434"
	slug           = "ollama"
	displayName    = "Ollama"
)

// brandColor: deliberately neutral white-ish to match the design tokens
// (Ollama doesn't really have a brand color in the same way the cloud
// providers do).
var brandColor = lipgloss.Color("#E1E1E8")

// modelsKnownToSupportTools is the conservative allow-list of locally-
// hostable model families that ship native tool calling. Anything not on
// this list still loads, but SupportsTools returns false so the agent loop
// omits native tool definitions.
var modelsKnownToSupportTools = []string{
	"qwen2.5", "qwen2.5-coder", "qwen3",
	"llama3.1", "llama3.2", "llama3.3",
	"mistral-nemo", "mistral-small",
	"firefunction",
	"command-r", "command-r-plus",
}

type Provider struct {
	baseURL    string
	httpClient *http.Client
}

func New(host string) *Provider {
	if host == "" {
		host = defaultBaseURL
	}
	return &Provider{
		baseURL:    normalizeHost(host),
		httpClient: &http.Client{},
	}
}

func (p *Provider) Name() string               { return displayName }
func (p *Provider) Slug() string               { return slug }
func (p *Provider) BrandColor() lipgloss.Color { return brandColor }

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.TrimRight(host, "/"))
	if host == "" {
		return defaultBaseURL
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	withoutScheme := strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
	if !strings.Contains(withoutScheme, ":") {
		host += ":11434"
	}
	return host
}

// ValidateKey ignores the apiKey argument and instead probes the daemon
// reachability. Returns nil iff GET /api/tags succeeds.
func (p *Provider) ValidateKey(ctx context.Context, _ string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ollama not reachable at %s: %w", p.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ollama not reachable at %s: status %d", p.baseURL, resp.StatusCode)
	}
	return nil
}

// tagsResponse is the GET /api/tags payload — pulled (local) models.
type tagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		Model      string `json:"model"`
		Size       int64  `json:"size"`
		ModifiedAt string `json:"modified_at"`
	} `json:"models"`
}

func (p *Provider) ListModels(ctx context.Context) ([]provider.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list ollama models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list ollama models: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode ollama models: %w", err)
	}
	out := make([]provider.Model, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		out = append(out, provider.Model{
			ID:            m.Name,
			DisplayName:   m.Name,
			ContextWindow: 0, // unknown — Ollama doesn't expose this
			SupportsTools: detectToolSupport(m.Name),
		})
	}
	return out, nil
}

// detectToolSupport matches the model name against the curated allow-list.
// We strip any tag suffix (":14b", ":latest") before comparing so e.g.
// "qwen2.5-coder:14b" still matches "qwen2.5-coder".
func detectToolSupport(modelName string) bool {
	base := modelName
	if i := strings.IndexByte(modelName, ':'); i != -1 {
		base = modelName[:i]
	}
	for _, supported := range modelsKnownToSupportTools {
		if base == supported {
			return true
		}
	}
	return false
}

// ────────────────────────────────────────────────────────────────────────────
// Streaming
// ────────────────────────────────────────────────────────────────────────────

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Tools    []chatTool    `json:"tools,omitempty"`
}

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	// Ollama uses "tool" role for tool responses, with "name" set to the
	// function name and content as the result text.
	Name string `json:"name,omitempty"`
}

type chatToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatChunk struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Message   struct {
		Role      string         `json:"role"`
		Content   string         `json:"content"`
		ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count,omitempty"`
	EvalCount       int  `json:"eval_count,omitempty"`
}

func toOllamaMessages(msgs []provider.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		om := chatMessage{
			Role:    string(m.Role),
			Content: m.Content,
			Name:    m.Name,
		}
		if len(m.ToolCalls) > 0 {
			om.ToolCalls = make([]chatToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Arguments)
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				om.ToolCalls[i].Function.Name = tc.Name
				om.ToolCalls[i].Function.Arguments = args
			}
		}
		out = append(out, om)
	}
	return out
}

func toOllamaTools(tools []provider.ToolDefinition) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, len(tools))
	for i, t := range tools {
		out[i].Type = "function"
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.Parameters
	}
	return out
}

func (p *Provider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	body := chatRequest{
		Model:    req.Model,
		Messages: toOllamaMessages(req.Messages),
		Stream:   true,
		Tools:    toOllamaTools(req.Tools),
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ollama chat: status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan provider.StreamEvent, 8)
	go parseOllamaStream(ctx, resp.Body, ch)
	return ch, nil
}

// parseOllamaStream reads NDJSON (one JSON object per line) and translates
// chunks into provider.StreamEvent values.
//
// Ollama emits tool calls as a single complete object on the message that
// also carries done=true (or earlier, for some models). We buffer them and
// emit Start/Delta/End as one unit per call to keep the stream protocol
// uniform with the other providers.
//
// ctx is checked once per NDJSON line so Ctrl+C from the App layer
// unblocks the parser promptly even when local daemon keep-alive hides
// the body-close from Scanner. On cancel we emit EventError with
// ctx.Err() so the agent surfaces the friendlier "turn cancelled" line.
func parseOllamaStream(ctx context.Context, body io.ReadCloser, ch chan<- provider.StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	toolIdx := 0
	var lastUsage *provider.Usage

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("parse ollama chunk: %w", err)}
			return
		}

		if chunk.Message.Content != "" && len(chunk.Message.ToolCalls) == 0 {
			ch <- provider.StreamEvent{
				Type:      provider.EventTextDelta,
				TextDelta: chunk.Message.Content,
			}
		}

		for _, tc := range chunk.Message.ToolCalls {
			id := fmt.Sprintf("call_%d", toolIdx)
			ch <- provider.StreamEvent{
				Type: provider.EventToolCallStart,
				ToolCall: &provider.ToolCallDelta{
					Index: toolIdx,
					ID:    id,
					Name:  tc.Function.Name,
				},
			}
			ch <- provider.StreamEvent{
				Type: provider.EventToolCallDelta,
				ToolCall: &provider.ToolCallDelta{
					Index:          toolIdx,
					ID:             id,
					Name:           tc.Function.Name,
					ArgumentsDelta: string(tc.Function.Arguments),
				},
			}
			ch <- provider.StreamEvent{
				Type:     provider.EventToolCallEnd,
				ToolCall: &provider.ToolCallDelta{Index: toolIdx},
			}
			toolIdx++
		}

		if chunk.Done {
			lastUsage = &provider.Usage{
				InputTokens:  chunk.PromptEvalCount,
				OutputTokens: chunk.EvalCount,
			}
			ch <- provider.StreamEvent{Type: provider.EventDone, Usage: lastUsage}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
		return
	}
	ch <- provider.StreamEvent{Type: provider.EventDone, Usage: lastUsage}
}

// Pricing — local inference is free.
func (p *Provider) Pricing(modelID string) (float64, float64) { return 0, 0 }

// ContextWindow — Ollama's API doesn't expose the model's context length.
// Return 0 to signal "unknown" so the UI can hide the bar or show a dash.
func (p *Provider) ContextWindow(modelID string) int { return 0 }

func (p *Provider) SupportsTools(modelID string) bool {
	return detectToolSupport(modelID)
}
