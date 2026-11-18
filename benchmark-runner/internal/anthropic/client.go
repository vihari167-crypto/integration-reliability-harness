// Package anthropic is a minimal client for the Claude Messages API,
// just enough for the tool-use loop this benchmark needs. Written by
// hand against the plain HTTPS/JSON API rather than pulling in the full
// SDK -- this project has exactly one call pattern (send messages, get
// back either text or tool_use blocks, send tool_result back), and a
// hand-rolled client keeps the dependency surface small and auditable.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewClient(apiKey, model string) *Client {
	if model == "" {
		model = "claude-sonnet-5"
	}
	return &Client{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Content blocks: a message is a list of these. We only need the
// subset of block types a tool-use loop actually produces/consumes.
type ContentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"

	// text
	Text string `json:"text,omitempty"`

	// tool_use (assistant -> us)
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result (us -> assistant)
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type thinkingConfig struct {
	Type string `json:"type"`
}

type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	// Thinking is disabled: for a tool-calling agent it adds cost and returns
	// thinking blocks that must be round-tripped intact. Disabling keeps
	// requests simple and cheap. (Haiku 5.5 supports thinking type "disabled".)
	Thinking *thinkingConfig `json:"thinking,omitempty"`
}

type messagesResponse struct {
	ID         string         `json:"id"`
	StopReason string         `json:"stop_reason"`
	Content    []ContentBlock `json:"content"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// SendMessages posts the full conversation so far (including any
// tool_result blocks the caller appended) and returns the assistant's
// next message. Tool-use orchestration (deciding whether to call again)
// lives in the runner, not here -- this client is a thin, honest wrapper.
func (c *Client) SendMessages(ctx context.Context, system string, messages []Message, tools []Tool) (*Message, string, error) {
	reqBody := messagesRequest{
		Model:     c.model,
		MaxTokens: 2048,
		System:    system,
		Messages:  messages,
		Tools:     tools,
		Thinking:  &thinkingConfig{Type: "disabled"},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}

	var mr messagesResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, "", fmt.Errorf("unmarshal response (status %d): %w (body=%s)", resp.StatusCode, err, string(body))
	}
	if mr.Error != nil {
		return nil, "", fmt.Errorf("anthropic API error (%s): %s", mr.Error.Type, mr.Error.Message)
	}

	return &Message{Role: "assistant", Content: mr.Content}, mr.StopReason, nil
}
