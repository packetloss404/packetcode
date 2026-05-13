// Package anthropic implements provider.Provider for the Claude Messages API.
package anthropic

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
	defaultBaseURL   = "https://api.anthropic.com/v1"
	anthropicVersion = "2023-06-01"
	DefaultModel     = "claude-opus-4-7"
	defaultMaxTokens = 8192
	slug             = "anthropic"
	displayName      = "Anthropic"
)

var brandColor = lipgloss.Color("#D97757")

type Provider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func New(apiKey string) *Provider {
	return NewWithBaseURL(defaultBaseURL, apiKey)
}

func NewWithBaseURL(baseURL, apiKey string) *Provider {
	return &Provider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (p *Provider) Name() string               { return displayName }
func (p *Provider) Slug() string               { return slug }
func (p *Provider) BrandColor() lipgloss.Color { return brandColor }

func (p *Provider) ValidateKey(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("api key is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	p.setHeaders(req, apiKey)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("validate key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("validate key: status %d: %s", resp.StatusCode, extractErrorMessage(body))
	}
	return nil
}

type modelsResponse struct {
	Data []struct {
		ID             string `json:"id"`
		DisplayName    string `json:"display_name"`
		MaxInputTokens int    `json:"max_input_tokens"`
		MaxTokens      int    `json:"max_tokens"`
		Capabilities   struct {
			Tools bool `json:"tools"`
		} `json:"capabilities"`
	} `json:"data"`
}

func (p *Provider) ListModels(ctx context.Context) ([]provider.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(req, p.apiKey)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models: status %d: %s", resp.StatusCode, extractErrorMessage(body))
	}

	var parsed modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	out := make([]provider.Model, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		in, outRate := p.Pricing(m.ID)
		ctxWindow := m.MaxInputTokens
		if ctxWindow == 0 {
			ctxWindow = p.ContextWindow(m.ID)
		}
		supportsTools := m.Capabilities.Tools
		if !supportsTools {
			supportsTools = p.SupportsTools(m.ID)
		}
		display := m.DisplayName
		if display == "" {
			display = m.ID
		}
		out = append(out, provider.Model{
			ID:            m.ID,
			DisplayName:   display,
			ContextWindow: ctxWindow,
			SupportsTools: supportsTools,
			InputPer1M:    in,
			OutputPer1M:   outRate,
		})
	}
	return prioritizeDefaultModel(out), nil
}

func (p *Provider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	wire, err := toWireRequest(req)
	if err != nil {
		return nil, err
	}
	buf, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq, p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, extractErrorMessage(body))
	}

	ch := make(chan provider.StreamEvent, 8)
	go parseSSE(ctx, resp.Body, ch)
	return ch, nil
}

func (p *Provider) Pricing(modelID string) (float64, float64) {
	if entry, ok := pricingTable[modelID]; ok {
		return entry.Input, entry.Output
	}
	return 5.00, 25.00
}

func (p *Provider) ContextWindow(modelID string) int {
	if entry, ok := pricingTable[modelID]; ok {
		return entry.ContextWindow
	}
	if strings.HasPrefix(modelID, "claude-") {
		return 200_000
	}
	return 0
}

func (p *Provider) SupportsTools(modelID string) bool {
	if entry, ok := pricingTable[modelID]; ok {
		return entry.SupportsTools
	}
	return strings.HasPrefix(modelID, "claude-")
}

func (p *Provider) setHeaders(req *http.Request, apiKey string) {
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
}

func prioritizeDefaultModel(models []provider.Model) []provider.Model {
	for i, m := range models {
		if m.ID != DefaultModel {
			continue
		}
		if i == 0 {
			return models
		}
		out := make([]provider.Model, 0, len(models))
		out = append(out, m)
		out = append(out, models[:i]...)
		out = append(out, models[i+1:]...)
		return out
	}
	return models
}

func extractErrorMessage(body []byte) string {
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

type wireRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []wireMessage `json:"messages"`
	System    string        `json:"system,omitempty"`
	Tools     []wireTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type wireContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func toWireRequest(req provider.ChatRequest) (wireRequest, error) {
	wr := wireRequest{
		Model:     req.Model,
		MaxTokens: defaultMaxTokens,
		Stream:    true,
	}

	var system []string
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleSystem:
			if strings.TrimSpace(m.Content) != "" {
				system = append(system, m.Content)
			}
		case provider.RoleUser:
			content, err := marshalBlocks([]wireContentBlock{{Type: "text", Text: nonEmpty(m.Content, " ")}})
			if err != nil {
				return wr, err
			}
			wr.Messages = append(wr.Messages, wireMessage{Role: "user", Content: content})
		case provider.RoleAssistant:
			blocks := make([]wireContentBlock, 0, 1+len(m.ToolCalls))
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, wireContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args, err := normalizeToolInput(tc.Arguments)
				if err != nil {
					return wr, fmt.Errorf("tool call %q: %w", tc.Name, err)
				}
				blocks = append(blocks, wireContentBlock{
					Type:  "tool_use",
					ID:    nonEmpty(tc.ID, "toolu_"+tc.Name),
					Name:  tc.Name,
					Input: args,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, wireContentBlock{Type: "text", Text: " "})
			}
			content, err := marshalBlocks(blocks)
			if err != nil {
				return wr, err
			}
			wr.Messages = append(wr.Messages, wireMessage{Role: "assistant", Content: content})
		case provider.RoleTool:
			toolID := m.ToolCallID
			if toolID == "" {
				toolID = "toolu_" + m.Name
			}
			content, err := marshalBlocks([]wireContentBlock{{
				Type:      "tool_result",
				ToolUseID: toolID,
				Content:   m.Content,
			}})
			if err != nil {
				return wr, err
			}
			wr.Messages = append(wr.Messages, wireMessage{Role: "user", Content: content})
		}
	}
	wr.System = strings.Join(system, "\n\n")

	for _, t := range req.Tools {
		schema := t.Parameters
		if len(strings.TrimSpace(string(schema))) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wr.Tools = append(wr.Tools, wireTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	return wr, nil
}

func marshalBlocks(blocks []wireContentBlock) (json.RawMessage, error) {
	buf, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}

func normalizeToolInput(args string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return json.RawMessage("{}"), nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, err
	}
	return json.RawMessage(trimmed), nil
}

type streamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta,omitempty"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message,omitempty"`
	Usage *anthropicUsage `json:"usage,omitempty"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type activeToolBlock struct {
	id   string
	name string
}

func parseSSE(ctx context.Context, body io.ReadCloser, ch chan<- provider.StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	tools := map[int]activeToolBlock{}
	var usage *provider.Usage
	var dataLines []string

	flush := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(data) == "" {
			return true
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("parse anthropic chunk: %w", err)}
			return false
		}
		if ev.Error != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("%s: %s", ev.Error.Type, ev.Error.Message)}
			return false
		}
		if ev.Message != nil && ev.Message.Usage != nil {
			usage = toProviderUsage(ev.Message.Usage)
		}
		if ev.Usage != nil {
			if usage == nil {
				usage = &provider.Usage{}
			}
			usage.OutputTokens = ev.Usage.OutputTokens
			if ev.Usage.InputTokens != 0 {
				usage.InputTokens = ev.Usage.InputTokens
			}
			if ev.Usage.CacheCreationInputTokens != 0 {
				usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
			}
			if ev.Usage.CacheReadInputTokens != 0 {
				usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
			}
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				block := activeToolBlock{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				tools[ev.Index] = block
				ch <- provider.StreamEvent{
					Type: provider.EventToolCallStart,
					ToolCall: &provider.ToolCallDelta{
						Index: ev.Index,
						ID:    block.id,
						Name:  block.name,
					},
				}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				return true
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: ev.Delta.Text}
				}
			case "input_json_delta":
				block := tools[ev.Index]
				ch <- provider.StreamEvent{
					Type: provider.EventToolCallDelta,
					ToolCall: &provider.ToolCallDelta{
						Index:          ev.Index,
						ID:             block.id,
						Name:           block.name,
						ArgumentsDelta: ev.Delta.PartialJSON,
					},
				}
			}
		case "content_block_stop":
			if block, ok := tools[ev.Index]; ok {
				ch <- provider.StreamEvent{
					Type: provider.EventToolCallEnd,
					ToolCall: &provider.ToolCallDelta{
						Index: ev.Index,
						ID:    block.id,
						Name:  block.name,
					},
				}
				delete(tools, ev.Index)
			}
		case "message_stop":
			ch <- provider.StreamEvent{Type: provider.EventDone, Usage: usage}
			return false
		}
		return true
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
			return
		}
		line := scanner.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		ch <- provider.StreamEvent{Type: provider.EventError, Error: err}
		return
	}
	if len(dataLines) > 0 {
		if !flush() {
			return
		}
	}
	ch <- provider.StreamEvent{Type: provider.EventDone, Usage: usage}
}

func toProviderUsage(u *anthropicUsage) *provider.Usage {
	if u == nil {
		return nil
	}
	return &provider.Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
	}
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
