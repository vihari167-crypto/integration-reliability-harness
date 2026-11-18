// Package tools defines the agent-facing tool contracts for Linear,
// in two parallel variants: naive and hardened. Both variants call the
// same underlying linear.Client — the only thing that differs is tool
// design: descriptions, error shape, idempotency, pagination contract,
// and permission scoping. That difference is the entire point of this
// project, so it lives here, in one place, side by side.
package tools

import (
	"context"
	"encoding/json"
)

// ToolDef mirrors the shape agent frameworks (incl. Claude's tool-use API)
// expect: a name, a natural-language description the *model* reads to decide
// when/how to call the tool, and a JSON schema for its input.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Handler     func(ctx context.Context, input json.RawMessage) (*ToolResult, error) `json:"-"`
}

// ToolResult is the structured outcome of one tool call. Errors are
// deliberately part of the normal result shape (not always a Go `error`
// bubbling up as a 500) for the hardened variant, because an agent needs
// a machine-readable error it can reason about ("rate_limited, retry after
// 2s" vs. "permission_denied, do not retry") — that distinction is exactly
// what the naive variant fails to provide.
type ToolResult struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *ToolError      `json:"error,omitempty"`
}

type ToolError struct {
	Code       string `json:"code"`        // e.g. "rate_limited", "not_found", "permission_denied", "unknown"
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	RetryAfter string `json:"retry_after,omitempty"`
}

// Variant bundles a named set of tools plus metadata used by the benchmark
// runner to report which design produced which outcome.
type Variant struct {
	ID          string
	Description string
	Tools       []ToolDef
}

func (v *Variant) ToolByName(name string) *ToolDef {
	for i := range v.Tools {
		if v.Tools[i].Name == name {
			return &v.Tools[i]
		}
	}
	return nil
}
