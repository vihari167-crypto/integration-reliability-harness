// Package runner drives one (task, tool variant, chaos scenario) agent
// run end to end: send the task to Claude with the variant's tools
// attached, execute whatever tools it calls against the (possibly
// chaos-afflicted) tool server, feed results back, repeat until the
// agent stops or a turn budget is exhausted, then hand the transcript
// to the scoring package.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"integration-harness/benchmark-runner/internal/anthropic"
	"integration-harness/benchmark-runner/internal/scoring"
	"integration-harness/benchmark-runner/internal/toolclient"
)

const systemPrompt = `You are an AI agent that completes business workflow tasks by calling the tools ` +
	`provided to you. Use only the tools given. Read each tool's description carefully -- some tools ` +
	`describe specific requirements (such as pagination or idempotency) that you must follow exactly as ` +
	`described, not based on general assumptions about how APIs usually work. When a tool call returns an ` +
	`error, the error includes a "code" and a "retryable" flag: retry only when retryable is true, and if a ` +
	`retry_after value is given, wait before retrying. Do not report a task as complete unless your tool ` +
	`calls actually confirm it succeeded.`

type Config struct {
	ToolServerBaseURL string
	ClaudeAPIKey      string
	ClaudeModel       string
	Variant           string // "naive" | "hardened"
	WriteScope        string // "write" for hardened write calls; ignored by naive
	Scenario          string
	Seed              string
	TeamID            string
	MaxTurns          int
	ToolCallTimeout   time.Duration
}

type Outcome struct {
	Score        scoring.Result
	Calls        []scoring.ToolCallRecord
	TotalCalls   int
	TotalRetries int
	WallClock    time.Duration
	TimedOut     bool
}

// requestSignature gives a cheap, good-enough key for "is this the same
// logical call as before" -- used only to flag likely retries for
// observability (is_retry on the logged row), never to change behavior.
func requestSignature(name string, input json.RawMessage) string {
	return name + ":" + string(input)
}

func Run(ctx context.Context, cfg Config, prompt string, scorer scoring.Scorer, logger *slog.Logger) (*Outcome, error) {
	claude := anthropic.NewClient(cfg.ClaudeAPIKey, cfg.ClaudeModel)
	tc := toolclient.New(cfg.ToolServerBaseURL, cfg.Variant, cfg.WriteScope, cfg.ToolCallTimeout)

	listed, err := tc.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	tools := make([]anthropic.Tool, 0, len(listed))
	for _, t := range listed {
		tools = append(tools, anthropic.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}

	messages := []anthropic.Message{
		{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: prompt}}},
	}

	var calls []scoring.ToolCallRecord
	seenSignatures := map[string]bool{}
	start := time.Now()

	for turn := 0; turn < cfg.MaxTurns; turn++ {
		assistantMsg, stopReason, err := claude.SendMessages(ctx, systemPrompt, messages, tools)
		if err != nil {
			return nil, fmt.Errorf("claude call failed on turn %d: %w", turn, err)
		}
		messages = append(messages, *assistantMsg)

		if stopReason != "tool_use" {
			break
		}

		var toolResultBlocks []anthropic.ContentBlock
		for _, block := range assistantMsg.Content {
			if block.Type != "tool_use" {
				continue
			}

			sig := requestSignature(block.Name, block.Input)
			isRetry := seenSignatures[sig]
			seenSignatures[sig] = true

			result, err := tc.CallTool(ctx, block.Name, block.Input, cfg.Scenario, cfg.Seed)
			if err != nil {
				return nil, fmt.Errorf("tool call transport error: %w", err)
			}

			idemKey := extractIdempotencyKey(block.Input)
			rec := scoring.ToolCallRecord{
				Seq:        len(calls),
				ToolName:   block.Name,
				RequestRaw: block.Input,
				OK:         result.OK,
			}
			if result.Error != nil {
				rec.ErrorCode = result.Error.Code
				rec.Retryable = result.Error.Retryable
			}
			if result.OK {
				rec.DataRaw = result.Data
				if block.Name == "list_issues" {
					extractHasMore(result.Data, &rec)
				}
			}
			calls = append(calls, rec)

			logger.Info("tool call", "turn", turn, "tool", block.Name, "ok", result.OK,
				"error_code", rec.ErrorCode, "is_retry", isRetry, "idempotency_key", idemKey)

			resultJSON, _ := json.Marshal(result)
			toolResultBlocks = append(toolResultBlocks, anthropic.ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   string(resultJSON),
				IsError:   !result.OK,
			})
		}
		messages = append(messages, anthropic.Message{Role: "user", Content: toolResultBlocks})
	}

	wallClock := time.Since(start)
	score := scorer(cfg.Variant, calls)
	retries := countActualRetries(calls)

	return &Outcome{
		Score:        score,
		Calls:        calls,
		TotalCalls:   len(calls),
		TotalRetries: retries,
		WallClock:    wallClock,
	}, nil
}

func countActualRetries(calls []scoring.ToolCallRecord) int {
	seen := map[string]int{}
	retries := 0
	for _, c := range calls {
		key := c.ToolName + ":" + string(c.RequestRaw)
		seen[key]++
		if seen[key] > 1 {
			retries++
		}
	}
	return retries
}

func extractIdempotencyKey(input json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if v, ok := m["idempotency_key"].(string); ok {
		return v
	}
	return ""
}

func extractHasMore(data json.RawMessage, rec *scoring.ToolCallRecord) {
	var wrapped struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil {
		// Only the hardened variant's response actually has this field;
		// for naive it will unmarshal as false/"" (the zero value), which
		// is indistinguishable from "no more pages" -- exactly the naive
		// variant's design gap the scorer is built to notice.
		hm := wrapped.HasMore
		rec.HasMore = &hm
		rec.NextCursor = wrapped.NextCursor
	}
}
