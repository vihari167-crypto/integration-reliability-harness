package scoring

import (
	"encoding/json"
	"testing"
)

func boolp(b bool) *bool { return &b }

func listCall(seq int, ids []string, hasMore *bool) ToolCallRecord {
	type lite struct {
		ID string `json:"id"`
	}
	nodes := make([]lite, len(ids))
	for i, id := range ids {
		nodes[i] = lite{ID: id}
	}
	data, _ := json.Marshal(nodes) // naive shape: bare array
	return ToolCallRecord{Seq: seq, ToolName: "list_issues", OK: true, DataRaw: data, HasMore: hasMore}
}

func writeCall(seq int, tool, issueID string) ToolCallRecord {
	req, _ := json.Marshal(map[string]string{"issue_id": issueID})
	return ToolCallRecord{Seq: seq, ToolName: tool, OK: true, RequestRaw: req}
}

func TestTriageSucceedsWhenEveryIssueAssignedAndCommented(t *testing.T) {
	calls := []ToolCallRecord{
		listCall(0, []string{"i1", "i2"}, boolp(false)),
		writeCall(1, "assign_issue", "i1"),
		writeCall(2, "add_comment", "i1"),
		writeCall(3, "assign_issue", "i2"),
		writeCall(4, "add_comment", "i2"),
	}
	if r := ScoreTriageUnassigned("hardened", calls); !r.Succeeded {
		t.Fatalf("expected success, got failure: %s", r.FailureReason)
	}
}

func TestTriageFailsWhenAnIssueIsLeftUntriaged(t *testing.T) {
	// i2 is listed but never assigned/commented — the task is incomplete.
	calls := []ToolCallRecord{
		listCall(0, []string{"i1", "i2"}, boolp(false)),
		writeCall(1, "assign_issue", "i1"),
		writeCall(2, "add_comment", "i1"),
	}
	if r := ScoreTriageUnassigned("naive", calls); r.Succeeded {
		t.Fatal("expected failure when an issue is left untriaged")
	}
}

func TestTriageFailsWhenAgentStopsWhileMorePagesExist(t *testing.T) {
	// has_more=true but no follow-up list call: the agent silently skipped
	// issues on later pages. This is the core pagination failure.
	calls := []ToolCallRecord{
		listCall(0, []string{"i1"}, boolp(true)),
		writeCall(1, "assign_issue", "i1"),
		writeCall(2, "add_comment", "i1"),
	}
	if r := ScoreTriageUnassigned("naive", calls); r.Succeeded {
		t.Fatal("expected failure when more pages existed but agent stopped")
	}
}

func createCall(seq, idSuffix string) ToolCallRecord {
	data, _ := json.Marshal(map[string]string{"id": "issue-" + idSuffix})
	return ToolCallRecord{ToolName: "create_issue", OK: true, DataRaw: data}
}

func TestLogFollowupSingleCreateIsCleanSuccess(t *testing.T) {
	calls := []ToolCallRecord{createCall("0", "A")}
	r := ScoreLogFollowup("hardened", calls)
	if !r.Succeeded || r.DuplicateWrite {
		t.Fatalf("expected clean success, got succeeded=%v duplicate=%v", r.Succeeded, r.DuplicateWrite)
	}
}

func TestLogFollowupTwoDistinctCreatesIsADuplicate(t *testing.T) {
	calls := []ToolCallRecord{createCall("0", "A"), createCall("1", "B")}
	r := ScoreLogFollowup("naive", calls)
	if !r.DuplicateWrite {
		t.Fatal("expected duplicate to be detected when two distinct issues were created")
	}
}
