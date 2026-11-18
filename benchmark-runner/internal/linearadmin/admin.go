// Package linearadmin talks to Linear directly (not through the tooled agent
// or the chaos proxy) to do two things the harness needs out-of-band:
//   1. Reset the board to a clean baseline between runs, so every run starts
//      from the same state (all seeded issues unassigned, any follow-up issues
//      created by a previous run removed).
//   2. Measure real board side-effects after a run — specifically, how many
//      "follow-up" issues actually exist. This matters because under the
//      silent-timeout fault the agent may create duplicates it never got a
//      success response for, so the only honest place to count them is the
//      board itself, not the agent's transcript.
package linearadmin

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
	key  string
	http *http.Client
}

func New(key string) *Client {
	return &Client{key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("linear admin error %d: %s", resp.StatusCode, string(raw))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("graphql: %s", env.Errors[0].Message)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

type issue struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Assignee *struct {
		ID string `json:"id"`
	} `json:"assignee"`
}

func (c *Client) listTeamIssues(ctx context.Context, teamID string) ([]issue, error) {
	const q = `query($f:IssueFilter){issues(filter:$f, first:250){nodes{id title assignee{id}}}}`
	var out struct {
		Issues struct {
			Nodes []issue `json:"nodes"`
		} `json:"issues"`
	}
	filter := map[string]any{"team": map[string]any{"id": map[string]any{"eq": teamID}}}
	if err := c.do(ctx, q, map[string]any{"f": filter}, &out); err != nil {
		return nil, err
	}
	return out.Issues.Nodes, nil
}

// ResetBoard restores the baseline: delete follow-up issues (title starts with
// followupPrefix), and unassign every remaining issue so triage tasks have
// un-triaged work again.
func (c *Client) ResetBoard(ctx context.Context, teamID, followupPrefix string) error {
	issues, err := c.listTeamIssues(ctx, teamID)
	if err != nil {
		return err
	}
	for _, is := range issues {
		if followupPrefix != "" && strings.HasPrefix(is.Title, followupPrefix) {
			if err := c.deleteIssue(ctx, is.ID); err != nil {
				return err
			}
			continue
		}
		if is.Assignee != nil {
			if err := c.unassign(ctx, is.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// CountFollowups returns how many issues whose title starts with the prefix
// currently exist — the honest, board-level duplicate count for the create task.
func (c *Client) CountFollowups(ctx context.Context, teamID, prefix string) (int, error) {
	issues, err := c.listTeamIssues(ctx, teamID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, is := range issues {
		if strings.HasPrefix(is.Title, prefix) {
			n++
		}
	}
	return n, nil
}

// BoardStats returns how many issues are currently assigned and the total
// count — the honest completeness measure for the triage task: did the agent
// actually assign every ticket, or silently leave some un-triaged?
func (c *Client) BoardStats(ctx context.Context, teamID string) (assigned, total int, err error) {
	issues, err := c.listTeamIssues(ctx, teamID)
	if err != nil {
		return 0, 0, err
	}
	for _, is := range issues {
		total++
		if is.Assignee != nil {
			assigned++
		}
	}
	return assigned, total, nil
}

func (c *Client) unassign(ctx context.Context, issueID string) error {
	const m = `mutation($id:String!){issueUpdate(id:$id, input:{assigneeId:null}){success}}`
	return c.do(ctx, m, map[string]any{"id": issueID}, nil)
}

func (c *Client) deleteIssue(ctx context.Context, issueID string) error {
	const m = `mutation($id:String!){issueDelete(id:$id){success}}`
	return c.do(ctx, m, map[string]any{"id": issueID}, nil)
}
