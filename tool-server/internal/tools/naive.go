package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"integration-harness/tool-server/internal/linear"
)

// NaiveVariant is deliberately how a first-pass integration usually looks:
// it works on the happy path, and every property the Dataleap job posting
// calls out as the hard part is either missing or silently wrong.
//   - Tool descriptions: minimal, no guidance on failure modes.
//   - Errors: every failure collapses to one generic, non-actionable message.
//   - Idempotency: none. A retried create is a duplicate create.
//   - Pagination: silently returns one page with no signal that more exist.
//   - Permissions: one tool set, no read/write separation.
func NaiveVariant(client *linear.Client) *Variant {
	return &Variant{
		ID:          "naive",
		Description: "Thin wrapper over the Linear API with no hardening.",
		Tools: []ToolDef{
			naiveListIssues(client),
			naiveListTeamMembers(client),
			naiveAssignIssue(client),
			naiveAddComment(client),
			naiveCreateIssue(client),
		},
	}
}

// naiveError always collapses to the same shape, whatever actually went
// wrong underneath (timeout, 429, 500, real 4xx). An agent has no way to
// tell "retry me" from "don't bother."
func naiveError(err error) *ToolResult {
	return &ToolResult{
		OK: false,
		Error: &ToolError{
			Code:    "error",
			Message: fmt.Sprintf("request failed: %v", err),
		},
	}
}

func naiveListIssues(client *linear.Client) ToolDef {
	type input struct {
		TeamID string `json:"team_id"`
	}
	return ToolDef{
		Name:        "list_issues",
		Description: "Lists issues for a team.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "team_id": { "type": "string" } },
			"required": ["team_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			// No cursor ever passed, no pageInfo ever surfaced: whatever the
			// first page contains is presented as "the issues," full stop.
			// Page size is deliberately small so the multi-page case shows up
			// with a modest dataset; the naive tool gives the agent NO signal
			// that more issues exist beyond this page.
			page, err := client.ListIssues(ctx, map[string]any{"team": map[string]any{"id": map[string]any{"eq": in.TeamID}}}, "", 5)
			if err != nil {
				return naiveError(err), nil
			}
			data, _ := json.Marshal(page.Nodes)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}

func naiveListTeamMembers(client *linear.Client) ToolDef {
	type input struct {
		TeamID string `json:"team_id"`
	}
	return ToolDef{
		Name:        "list_team_members",
		Description: "Lists members of a team.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "team_id": { "type": "string" } },
			"required": ["team_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			members, err := client.ListTeamMembers(ctx, in.TeamID)
			if err != nil {
				return naiveError(err), nil
			}
			data, _ := json.Marshal(members)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}

func naiveAssignIssue(client *linear.Client) ToolDef {
	type input struct {
		IssueID    string `json:"issue_id"`
		AssigneeID string `json:"assignee_id"`
	}
	return ToolDef{
		Name:        "assign_issue",
		Description: "Assigns an issue to a user.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "issue_id": { "type": "string" }, "assignee_id": { "type": "string" } },
			"required": ["issue_id", "assignee_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			if err := client.AssignIssue(ctx, in.IssueID, in.AssigneeID); err != nil {
				return naiveError(err), nil
			}
			return &ToolResult{OK: true}, nil
		},
	}
}

func naiveAddComment(client *linear.Client) ToolDef {
	type input struct {
		IssueID string `json:"issue_id"`
		Body    string `json:"body"`
	}
	return ToolDef{
		Name:        "add_comment",
		Description: "Adds a comment to an issue.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "issue_id": { "type": "string" }, "body": { "type": "string" } },
			"required": ["issue_id", "body"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			if err := client.AddComment(ctx, in.IssueID, in.Body); err != nil {
				return naiveError(err), nil
			}
			return &ToolResult{OK: true}, nil
		},
	}
}

// naiveCreateIssue has no idempotency parameter at all. If the HTTP call
// times out after the write actually landed, and the agent retries (a very
// ordinary thing for an agent to do on a timeout), this creates a second,
// identical issue. There is nothing in the tool contract to prevent it.
func naiveCreateIssue(client *linear.Client) ToolDef {
	type input struct {
		TeamID      string `json:"team_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	return ToolDef{
		Name:        "create_issue",
		Description: "Creates a new issue.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"team_id": { "type": "string" },
				"title": { "type": "string" },
				"description": { "type": "string" }
			},
			"required": ["team_id", "title"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return naiveError(err), nil
			}
			issue, err := client.CreateIssue(ctx, in.TeamID, in.Title, in.Description)
			if err != nil {
				return naiveError(err), nil
			}
			data, _ := json.Marshal(issue)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}
