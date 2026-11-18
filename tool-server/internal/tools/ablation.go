package tools

import (
	"context"
	"encoding/json"

	"integration-harness/tool-server/internal/linear"
)

// Ablation variants isolate ONE design dimension on top of the naive baseline,
// so a change in the agent's reliability can be attributed to a specific tool-
// design choice rather than to "the hardened bundle" as a whole. This is what
// turns "the careful version wins" (obvious) into "pagination alone accounts
// for the entire completeness gap" (a measured, non-obvious finding).

// PaginationOnlyVariant is naive in every respect EXCEPT that list_issues
// surfaces has_more + next_cursor. Everything else — generic errors, no
// idempotency — stays naive. If this alone recovers completeness, the exposure
// of pagination is the lever.
func PaginationOnlyVariant(client *linear.Client) *Variant {
	return &Variant{
		ID:          "pagination_only",
		Description: "Naive tools, but list_issues exposes pagination (has_more + cursor).",
		Tools: []ToolDef{
			paginatedNaiveListIssues(client),
			naiveListTeamMembers(client),
			naiveAssignIssue(client),
			naiveAddComment(client),
			naiveCreateIssue(client),
		},
	}
}

// TypedErrorsOnlyVariant is naive in every respect EXCEPT that create_issue
// returns typed, retryable errors instead of one generic string. If this alone
// recovers success under server errors, error typing is the lever.
func TypedErrorsOnlyVariant(client *linear.Client) *Variant {
	return &Variant{
		ID:          "typed_errors_only",
		Description: "Naive tools, but create_issue returns typed/retryable errors.",
		Tools: []ToolDef{
			naiveListIssues(client),
			naiveListTeamMembers(client),
			naiveAssignIssue(client),
			naiveAddComment(client),
			typedErrorsCreateIssue(client),
		},
	}
}

// paginatedNaiveListIssues: the hardened pagination contract, but with naive
// generic errors — so ONLY the pagination dimension differs from naive.
func paginatedNaiveListIssues(client *linear.Client) ToolDef {
	type input struct {
		TeamID string `json:"team_id"`
		Cursor string `json:"cursor,omitempty"`
	}
	type output struct {
		Issues     []linear.Issue `json:"issues"`
		HasMore    bool           `json:"has_more"`
		NextCursor string         `json:"next_cursor,omitempty"`
	}
	return ToolDef{
		Name: "list_issues",
		Description: "Lists issues for a team, 5 per page. PAGINATED: if has_more is true, call again " +
			"with cursor set to next_cursor to get the remaining issues.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"team_id":{"type":"string"},"cursor":{"type":"string"}},
			"required":["team_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			page, err := client.ListIssues(ctx, map[string]any{"team": map[string]any{"id": map[string]any{"eq": in.TeamID}}}, in.Cursor, 5)
			if err != nil {
				return naiveError(err), nil // errors kept naive on purpose
			}
			out := output{Issues: page.Nodes, HasMore: page.PageInfo.HasNextPage, NextCursor: page.PageInfo.EndCursor}
			data, _ := json.Marshal(out)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}

// typedErrorsCreateIssue: naive create (no idempotency), but failures are
// classified into the typed/retryable vocabulary — so ONLY error typing
// differs from naive.
func typedErrorsCreateIssue(client *linear.Client) ToolDef {
	type input struct {
		TeamID      string `json:"team_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	return ToolDef{
		Name:        "create_issue",
		Description: "Creates a new issue.", // same terse text as naive: only error typing differs
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{"team_id":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"}},
			"required":["team_id","title"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			issue, err := client.CreateIssue(ctx, in.TeamID, in.Title, in.Description)
			if err != nil {
				return &ToolResult{OK: false, Error: classifyError(err)}, nil
			}
			data, _ := json.Marshal(issue)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}
