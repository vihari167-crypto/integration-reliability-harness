package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"integration-harness/tool-server/internal/idempotency"
	"integration-harness/tool-server/internal/linear"
)

// HardenedVariant implements the same five operations as NaiveVariant,
// against the same Linear API, but with every property the naive variant
// skips:
//   - Descriptions tell the model the failure modes it will encounter and
//     exactly how to behave (retry vs. don't, how to page, what key to reuse).
//   - Errors are classified into a small typed vocabulary with a retryable
//     flag and, for rate limits, a retry_after hint.
//   - create_issue requires a client-supplied idempotency_key; replays with
//     the same key return the original issue instead of creating a new one.
//   - list_issues returns an explicit has_more/next_cursor contract.
//   - write tools are scope-checked separately from read tools (see server.go).
func HardenedVariant(client *linear.Client, idemStore *idempotency.Store) *Variant {
	return &Variant{
		ID:          "hardened",
		Description: "Linear tools designed for reliable agentic use: typed errors, idempotent writes, explicit pagination.",
		Tools: []ToolDef{
			hardenedListIssues(client),
			hardenedListTeamMembers(client),
			hardenedAssignIssue(client),
			hardenedAddComment(client),
			hardenedCreateIssue(client, idemStore),
		},
	}
}

// classifyError turns whatever the Linear client (or chaos proxy standing
// in for it) returned into the typed vocabulary the agent was told to expect.
// This is the single place that distinction lives — every hardened tool
// routes its errors through here so the classification can't drift tool-to-tool.
func classifyError(err error) *ToolError {
	if apiErr, ok := err.(*linear.APIError); ok {
		switch {
		case apiErr.StatusCode == 429:
			return &ToolError{
				Code:       "rate_limited",
				Message:    "Linear API rate limit hit.",
				Retryable:  true,
				RetryAfter: apiErr.RetryAfter,
			}
		case apiErr.StatusCode >= 500:
			return &ToolError{
				Code:      "server_error",
				Message:   "Linear API returned a server error.",
				Retryable: true,
			}
		case apiErr.StatusCode == 404:
			return &ToolError{
				Code:      "not_found",
				Message:   "The referenced resource does not exist.",
				Retryable: false,
			}
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			return &ToolError{
				Code:      "permission_denied",
				Message:   "Not authorized for this operation.",
				Retryable: false,
			}
		default:
			return &ToolError{
				Code:      "validation_error",
				Message:   apiErr.Body,
				Retryable: false,
			}
		}
	}
	return &ToolError{
		Code:      "unknown",
		Message:   fmt.Sprintf("%v", err),
		Retryable: true, // unclassified/transport-level errors (e.g. timeouts) are treated as safe to retry
	}
}

func hardenedListIssues(client *linear.Client) ToolDef {
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
		Description: "Lists issues for a team, 25 per page. This is PAGINATED: check `has_more` in the " +
			"result. If true, call this tool again with `cursor` set to the returned `next_cursor` to get " +
			"the next page. Do not assume the first page is the complete list.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"team_id": { "type": "string" },
				"cursor": { "type": "string", "description": "Pass the next_cursor from a previous call to continue paging." }
			},
			"required": ["team_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResult{OK: false, Error: &ToolError{Code: "validation_error", Message: err.Error()}}, nil
			}
			// Same small page size as the naive tool — the ONLY difference is
			// that this tool surfaces has_more + next_cursor so the agent knows
			// to keep paging.
			page, err := client.ListIssues(ctx, map[string]any{"team": map[string]any{"id": map[string]any{"eq": in.TeamID}}}, in.Cursor, 5)
			if err != nil {
				return &ToolResult{OK: false, Error: classifyError(err)}, nil
			}
			out := output{Issues: page.Nodes, HasMore: page.PageInfo.HasNextPage, NextCursor: page.PageInfo.EndCursor}
			data, _ := json.Marshal(out)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}

func hardenedListTeamMembers(client *linear.Client) ToolDef {
	type input struct {
		TeamID string `json:"team_id"`
	}
	return ToolDef{
		Name:        "list_team_members",
		Description: "Lists members of a team. Read-only.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "team_id": { "type": "string" } },
			"required": ["team_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResult{OK: false, Error: &ToolError{Code: "validation_error", Message: err.Error()}}, nil
			}
			members, err := client.ListTeamMembers(ctx, in.TeamID)
			if err != nil {
				return &ToolResult{OK: false, Error: classifyError(err)}, nil
			}
			data, _ := json.Marshal(members)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}

func hardenedAssignIssue(client *linear.Client) ToolDef {
	type input struct {
		IssueID    string `json:"issue_id"`
		AssigneeID string `json:"assignee_id"`
	}
	return ToolDef{
		Name: "assign_issue",
		Description: "Assigns an issue to a user. This operation is naturally idempotent (assigning the " +
			"same issue to the same user twice has no additional effect), so it is safe to retry without " +
			"an idempotency key.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "issue_id": { "type": "string" }, "assignee_id": { "type": "string" } },
			"required": ["issue_id", "assignee_id"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResult{OK: false, Error: &ToolError{Code: "validation_error", Message: err.Error()}}, nil
			}
			if err := client.AssignIssue(ctx, in.IssueID, in.AssigneeID); err != nil {
				return &ToolResult{OK: false, Error: classifyError(err)}, nil
			}
			return &ToolResult{OK: true}, nil
		},
	}
}

func hardenedAddComment(client *linear.Client) ToolDef {
	type input struct {
		IssueID string `json:"issue_id"`
		Body    string `json:"body"`
	}
	return ToolDef{
		Name:        "add_comment",
		Description: "Adds a comment to an issue. Each call creates a new comment; retrying after a timeout may create a duplicate comment, so only retry if you have not already seen a success response.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": { "issue_id": { "type": "string" }, "body": { "type": "string" } },
			"required": ["issue_id", "body"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResult{OK: false, Error: &ToolError{Code: "validation_error", Message: err.Error()}}, nil
			}
			if err := client.AddComment(ctx, in.IssueID, in.Body); err != nil {
				return &ToolResult{OK: false, Error: classifyError(err)}, nil
			}
			return &ToolResult{OK: true}, nil
		},
	}
}

// hardenedCreateIssue is the centerpiece of the hardened/naive comparison:
// a required idempotency_key makes retries safe. The *first* call with a
// given key performs the real creation and records the result; every
// subsequent call with that same key returns the original issue untouched,
// whether the retry was caused by a timeout, a 500, or the agent simply
// being unsure whether its last call landed.
func hardenedCreateIssue(client *linear.Client, idemStore *idempotency.Store) ToolDef {
	type input struct {
		TeamID         string `json:"team_id"`
		Title          string `json:"title"`
		Description    string `json:"description"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	return ToolDef{
		Name: "create_issue",
		Description: "Creates a new issue. REQUIRES idempotency_key: a unique string you generate once per " +
			"logical creation attempt (e.g. a UUID). If a network error or timeout occurs and you are unsure " +
			"whether the issue was created, retry this call with the SAME idempotency_key — you will get back " +
			"the original issue instead of creating a duplicate. Use a NEW key for each distinct issue you intend to create.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"team_id": { "type": "string" },
				"title": { "type": "string" },
				"description": { "type": "string" },
				"idempotency_key": { "type": "string", "description": "Required. Reuse on retry of the same logical creation; use a new value for a different issue." }
			},
			"required": ["team_id", "title", "idempotency_key"]
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (*ToolResult, error) {
			var in input
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResult{OK: false, Error: &ToolError{Code: "validation_error", Message: err.Error()}}, nil
			}
			if in.IdempotencyKey == "" {
				return &ToolResult{OK: false, Error: &ToolError{Code: "validation_error", Message: "idempotency_key is required", Retryable: false}}, nil
			}

			// The idempotency key is embedded in the issue description as a
			// marker. This is what makes the create safe even when a prior
			// attempt's RESPONSE was lost (the write landed but we never heard
			// back): we can rediscover it on the board by its marker.
			marker := "[idempotency-key: " + in.IdempotencyKey + "]"

			// Fast path: same process already recorded this key.
			if existingID, seen := idemStore.CheckAndReserve(in.IdempotencyKey); seen {
				if issue, err := client.GetIssue(ctx, existingID); err == nil {
					data, _ := json.Marshal(issue)
					return &ToolResult{OK: true, Data: data}, nil
				}
			}

			// Durable check: has an issue with this marker already been created
			// (e.g. by an earlier attempt whose response we lost)?
			if existing, _ := client.FindIssueByMarker(ctx, in.TeamID, marker); existing != nil {
				idemStore.Record(in.IdempotencyKey, existing.ID)
				data, _ := json.Marshal(existing)
				return &ToolResult{OK: true, Data: data}, nil
			}

			// Not created yet: create it, with the marker embedded.
			desc := strings.TrimSpace(in.Description + "\n\n" + marker)
			issue, err := client.CreateIssue(ctx, in.TeamID, in.Title, desc)
			if err != nil {
				// The write may have actually succeeded even though we got an
				// error (dropped response). Before reporting failure, try to
				// find it by marker — if it's there, this attempt effectively
				// succeeded and we must NOT let the agent retry into a duplicate.
				if recovered, ferr := client.FindIssueByMarker(ctx, in.TeamID, marker); ferr == nil && recovered != nil {
					idemStore.Record(in.IdempotencyKey, recovered.ID)
					data, _ := json.Marshal(recovered)
					return &ToolResult{OK: true, Data: data}, nil
				}
				return &ToolResult{OK: false, Error: classifyError(err)}, nil
			}
			idemStore.Record(in.IdempotencyKey, issue.ID)
			data, _ := json.Marshal(issue)
			return &ToolResult{OK: true, Data: data}, nil
		},
	}
}
