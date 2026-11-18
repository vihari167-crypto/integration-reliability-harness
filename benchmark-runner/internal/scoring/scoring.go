// Package scoring turns a run's raw tool-call transcript into the three
// numbers this whole project exists to produce: did the task succeed,
// did it duplicate a write it shouldn't have, and if a fault occurred,
// did the agent recover from it correctly. Each task defines its own
// scorer because "success" is task-specific, but all scorers work from
// the same transcript shape so they stay honest -- no scorer gets to
// peek at hidden state the agent didn't also have access to.
package scoring

import "encoding/json"

type ToolCallRecord struct {
	Seq            int
	ToolName       string
	RequestRaw     json.RawMessage
	OK             bool
	ErrorCode      string
	Retryable      bool
	HasMore        *bool // set only for list_issues responses, nil otherwise
	NextCursor     string
	DataRaw        json.RawMessage
}

type issueLite struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Result struct {
	Succeeded          bool
	DuplicateWrite      bool
	RecoveredFromFault bool
	FailureReason       string
}

type Scorer func(variant string, calls []ToolCallRecord) Result

// anyFaultOccurred reports whether the transcript contains at least one
// tool-level error -- used to decide whether "recovered from fault" is
// even a meaningful question for this run (it isn't, in the clean scenario).
func anyFaultOccurred(calls []ToolCallRecord) bool {
	for _, c := range calls {
		if !c.OK {
			return true
		}
	}
	return false
}

// extractIssues parses whatever shape a list_issues or create_issue
// response has (naive: bare array or bare object; hardened: wrapped
// with has_more/next_cursor) into a flat list of (id, title) pairs.
// Both variants carry the same underlying fields, just organized
// differently -- the scorer deliberately doesn't care which variant
// produced the data, only what issues it names.
func extractIssues(raw json.RawMessage) []issueLite {
	if len(raw) == 0 {
		return nil
	}
	// Try hardened list_issues shape: {"issues": [...], "has_more": bool, ...}
	var wrapped struct {
		Issues []issueLite `json:"issues"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Issues != nil {
		return wrapped.Issues
	}
	// Try naive list_issues shape: bare array.
	var arr []issueLite
	if err := json.Unmarshal(raw, &arr); err == nil && arr != nil {
		return arr
	}
	// Try single-issue shape (create_issue response).
	var single issueLite
	if err := json.Unmarshal(raw, &single); err == nil && single.ID != "" {
		return []issueLite{single}
	}
	return nil
}

// ScoreTriageUnassigned: every issue seen via list_issues (across every
// page the agent actually fetched) must receive both a successful
// assign_issue and a successful add_comment call. If any list_issues
// response signaled more pages were available (hardened variant only --
// naive never signals this), the transcript must show the agent actually
// followed up with a subsequent call using that cursor; otherwise the
// task is scored as incomplete, because issues on unfetched pages were
// silently never triaged.
func ScoreTriageUnassigned(variant string, calls []ToolCallRecord) Result {
	seenIssueIDs := map[string]bool{}
	assignedIDs := map[string]bool{}
	commentedIDs := map[string]bool{}
	sawUnconsumedHasMore := false

	for i, c := range calls {
		switch c.ToolName {
		case "list_issues":
			if !c.OK {
				continue
			}
			for _, issue := range extractIssues(c.DataRaw) {
				seenIssueIDs[issue.ID] = true
			}
			if c.HasMore != nil && *c.HasMore {
				// Did a *later* list_issues call pick up where this one left off?
				followedUp := false
				for _, later := range calls[i+1:] {
					if later.ToolName == "list_issues" {
						followedUp = true
						break
					}
				}
				if !followedUp {
					sawUnconsumedHasMore = true
				}
			}
		case "assign_issue":
			if c.OK {
				if id := extractField(c.RequestRaw, "issue_id"); id != "" {
					assignedIDs[id] = true
				}
			}
		case "add_comment":
			if c.OK {
				if id := extractField(c.RequestRaw, "issue_id"); id != "" {
					commentedIDs[id] = true
				}
			}
		}
	}

	if len(seenIssueIDs) == 0 {
		return Result{Succeeded: false, FailureReason: "no issues were ever listed"}
	}
	for id := range seenIssueIDs {
		if !assignedIDs[id] || !commentedIDs[id] {
			return Result{
				Succeeded:          false,
				RecoveredFromFault: false,
				FailureReason:      "issue " + id + " was listed but not fully triaged (assign+comment)",
			}
		}
	}
	if sawUnconsumedHasMore {
		return Result{Succeeded: false, FailureReason: "agent stopped paginating while more issues were available"}
	}
	return Result{Succeeded: true, RecoveredFromFault: anyFaultOccurred(calls)}
}

// ScoreLogFollowup: the task is to create exactly one issue with a given
// title. Success requires at least one successful create. The headline
// metric is DuplicateWrite: if the transcript shows more than one
// distinct issue ID created, the agent duplicated a write it shouldn't
// have -- almost always because it retried a create_issue call after an
// ambiguous failure (see silent_timeout_after_write) without a way to
// tell "already done" from "not done yet."
func ScoreLogFollowup(variant string, calls []ToolCallRecord) Result {
	createdIDs := map[string]bool{}
	for _, c := range calls {
		if c.ToolName != "create_issue" || !c.OK {
			continue
		}
		for _, issue := range extractIssues(c.DataRaw) {
			createdIDs[issue.ID] = true
		}
	}
	if len(createdIDs) == 0 {
		return Result{Succeeded: false, FailureReason: "no issue was ever successfully created"}
	}
	if len(createdIDs) > 1 {
		return Result{
			Succeeded:          true, // the task did get done...
			DuplicateWrite:      true, // ...but at the cost of a duplicate
			RecoveredFromFault: false,
			FailureReason:      "",
		}
	}
	return Result{Succeeded: true, RecoveredFromFault: anyFaultOccurred(calls)}
}

func extractField(raw json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

var Scorers = map[string]Scorer{
	"triage_unassigned": ScoreTriageUnassigned,
	"log_followup":      ScoreLogFollowup,
}
