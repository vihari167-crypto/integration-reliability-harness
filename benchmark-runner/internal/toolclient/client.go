// Package toolclient calls the tool-server over HTTP, the same way any
// agent framework would call a remote tool. It also carries the chaos
// headers (X-Chaos-Scenario, X-Chaos-Seed) through to the tool server,
// which forwards them to the chaos proxy sitting in front of Linear --
// this is the one place those two concerns (which tools exist, and what
// faults they face) meet.
package toolclient

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
	baseURL string
	variant string
	scope   string // "" (read-only) or "write"
	http    *http.Client
}

func New(baseURL, variant, scope string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		variant: variant,
		scope:   scope,
		http:    &http.Client{Timeout: timeout},
	}
}

type ToolListing struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (c *Client) ListTools(ctx context.Context) ([]ToolListing, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/%s/tools", c.baseURL, c.variant), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []ToolListing
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

type CallResult struct {
	HTTPStatus int
	OK         bool            `json:"ok"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      *struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		Retryable  bool   `json:"retryable"`
		RetryAfter string `json:"retry_after,omitempty"`
	} `json:"error,omitempty"`
	LatencyMS int64
}

// CallTool invokes one tool and returns its structured result. seed and
// scenario select the chaos behavior the underlying Linear call will see;
// they're set once per run and passed on every call within that run so
// the fault sequence for a given seed is deterministic and comparable
// across tool variants.
func (c *Client) CallTool(ctx context.Context, name string, input json.RawMessage, scenario, seed string) (*CallResult, error) {
	start := time.Now()
	url := fmt.Sprintf("%s/v1/%s/tools/%s/call", c.baseURL, c.variant, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chaos-Scenario", scenario)
	req.Header.Set("X-Chaos-Seed", seed)
	if c.scope != "" {
		req.Header.Set("X-Scope", c.scope)
	}

	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		// A transport-level failure (timeout, connection reset) IS a real
		// outcome the agent must handle -- most notably, this is exactly
		// what the silent_timeout_after_write chaos scenario produces.
		return &CallResult{
			HTTPStatus: 0,
			OK:         false,
			Error: &struct {
				Code       string `json:"code"`
				Message    string `json:"message"`
				Retryable  bool   `json:"retryable"`
				RetryAfter string `json:"retry_after,omitempty"`
			}{Code: "transport_error", Message: err.Error(), Retryable: true},
			LatencyMS: elapsed.Milliseconds(),
		}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var cr CallResult
	_ = json.Unmarshal(body, &cr)
	cr.HTTPStatus = resp.StatusCode
	cr.LatencyMS = elapsed.Milliseconds()
	return &cr, nil
}
