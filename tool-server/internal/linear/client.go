// Package linear is a minimal GraphQL client for the real Linear API.
// It knows nothing about "naive" vs "hardened" tool design — that
// distinction lives one layer up, in internal/tools. This client is
// just correct, idiomatic access to Linear's actual API surface.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

// NewClient builds a Linear client. baseURL is overridable so the
// benchmark harness can point it at the chaos proxy instead of the
// real api.linear.app — the client code itself never has to know.
func NewClient(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.linear.app/graphql"
	}
	return &Client{
		apiKey:   apiKey,
		endpoint: baseURL,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors,omitempty"`
}

// APIError carries the real HTTP status through so callers (naive vs
// hardened tool wrappers) can decide how much of it to expose to the agent.
type APIError struct {
	StatusCode int
	Body       string
	RetryAfter string // from the Retry-After header, when present (e.g. on 429)
}

func (e *APIError) Error() string {
	return fmt.Sprintf("linear API error: status=%d body=%s", e.StatusCode, e.Body)
}

// chaosCtxKey carries the chaos scenario/seed from the agent's incoming call
// down to this outgoing Linear request, so the chaos proxy standing in front
// of Linear knows which fault profile to apply for this specific run.
type chaosCtxKey struct{}

type ChaosHeaders struct{ Scenario, Seed string }

// WithChaos returns a context carrying the chaos headers. The tool server sets
// these from the agent's request; the client below forwards them to the proxy.
func WithChaos(ctx context.Context, scenario, seed string) context.Context {
	return context.WithValue(ctx, chaosCtxKey{}, ChaosHeaders{Scenario: scenario, Seed: seed})
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	reqBody, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	// Forward chaos headers to the proxy in front of Linear, when present.
	if ch, ok := ctx.Value(chaosCtxKey{}).(ChaosHeaders); ok {
		if ch.Scenario != "" {
			req.Header.Set("X-Chaos-Scenario", ch.Scenario)
		}
		if ch.Seed != "" {
			req.Header.Set("X-Chaos-Seed", ch.Seed)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}

	var gr gqlResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return fmt.Errorf("unmarshal response: %w (body=%s)", err, string(body))
	}
	if len(gr.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", gr.Errors[0].Message)
	}
	if out != nil {
		if err := json.Unmarshal(gr.Data, out); err != nil {
			return fmt.Errorf("unmarshal data: %w", err)
		}
	}
	return nil
}

type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"` // e.g. "ENG-123"
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"createdAt"`
	Assignee    *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"assignee"`
	Team struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
}

type issuesPage struct {
	Nodes    []Issue `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

// ListIssues returns one page of issues matching the given filter.
// cursor == "" fetches the first page. Callers must follow PageInfo
// to get subsequent pages — this is the exact behavior whose correct
// handling by an agent the hardened/naive tool contracts will differ on.
func (c *Client) ListIssues(ctx context.Context, filter map[string]any, cursor string, pageSize int) (*issuesPage, error) {
	const q = `
	query Issues($filter: IssueFilter, $after: String, $first: Int) {
	  issues(filter: $filter, after: $after, first: $first) {
	    nodes {
	      id identifier title priority createdAt
	      assignee { id name }
	      team { id key }
	    }
	    pageInfo { hasNextPage endCursor }
	  }
	}`
	vars := map[string]any{"filter": filter, "first": pageSize}
	if cursor != "" {
		vars["after"] = cursor
	}
	var out struct {
		Issues issuesPage `json:"issues"`
	}
	if err := c.do(ctx, q, vars, &out); err != nil {
		return nil, err
	}
	return &out.Issues, nil
}

func (c *Client) GetIssue(ctx context.Context, id string) (*Issue, error) {
	const q = `
	query Issue($id: String!) {
	  issue(id: $id) {
	    id identifier title priority createdAt
	    assignee { id name }
	    team { id key }
	  }
	}`
	var out struct {
		Issue Issue `json:"issue"`
	}
	if err := c.do(ctx, q, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	return &out.Issue, nil
}

// FindIssueByMarker returns the first issue in the team whose description
// contains marker, or nil if none. This is how the hardened create_issue
// achieves true idempotency even when a write's RESPONSE is lost: the key is
// embedded in the created issue's description, so a retry (or a recovery after
// a dropped response) can discover that the create already happened and avoid
// making a duplicate. Linear's own API has no idempotency-key concept, so we
// build one on top of it.
func (c *Client) FindIssueByMarker(ctx context.Context, teamID, marker string) (*Issue, error) {
	const q = `
	query Find($filter: IssueFilter, $first: Int) {
	  issues(filter: $filter, first: $first) {
	    nodes { id identifier title description priority createdAt team { id key } assignee { id name } }
	  }
	}`
	filter := map[string]any{"team": map[string]any{"id": map[string]any{"eq": teamID}}}
	var out struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, q, map[string]any{"filter": filter, "first": 250}, &out); err != nil {
		return nil, err
	}
	for i := range out.Issues.Nodes {
		if strings.Contains(out.Issues.Nodes[i].Description, marker) {
			return &out.Issues.Nodes[i], nil
		}
	}
	return nil, nil
}

// CreateIssue has no built-in idempotency — Linear's API itself doesn't
// dedupe by client-supplied key. That's precisely why the *hardened*
// tool wrapper implements its own idempotency layer on top of this,
// and the *naive* wrapper doesn't: this call will happily create two
// identical issues if invoked twice.
func (c *Client) CreateIssue(ctx context.Context, teamID, title, description string) (*Issue, error) {
	const m = `
	mutation CreateIssue($teamId: String!, $title: String!, $description: String) {
	  issueCreate(input: { teamId: $teamId, title: $title, description: $description }) {
	    success
	    issue { id identifier title priority createdAt team { id key } assignee { id name } }
	  }
	}`
	var out struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.do(ctx, m, map[string]any{"teamId": teamID, "title": title, "description": description}, &out); err != nil {
		return nil, err
	}
	return &out.IssueCreate.Issue, nil
}

func (c *Client) AssignIssue(ctx context.Context, issueID, assigneeID string) error {
	const m = `
	mutation AssignIssue($id: String!, $assigneeId: String!) {
	  issueUpdate(id: $id, input: { assigneeId: $assigneeId }) { success }
	}`
	return c.do(ctx, m, map[string]any{"id": issueID, "assigneeId": assigneeID}, nil)
}

func (c *Client) AddComment(ctx context.Context, issueID, body string) error {
	const m = `
	mutation AddComment($issueId: String!, $body: String!) {
	  commentCreate(input: { issueId: $issueId, body: $body }) { success }
	}`
	return c.do(ctx, m, map[string]any{"issueId": issueID, "body": body}, nil)
}

type Member struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) ListTeamMembers(ctx context.Context, teamID string) ([]Member, error) {
	const q = `
	query TeamMembers($teamId: String!) {
	  team(id: $teamId) { members { nodes { id name } } }
	}`
	var out struct {
		Team struct {
			Members struct {
				Nodes []Member `json:"nodes"`
			} `json:"members"`
		} `json:"team"`
	}
	if err := c.do(ctx, q, map[string]any{"teamId": teamID}, &out); err != nil {
		return nil, err
	}
	return out.Team.Members.Nodes, nil
}
