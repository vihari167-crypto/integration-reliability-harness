// Package tasks defines the fixed battery of realistic multi-step
// instructions given to the agent under test. Two tasks, deliberately:
// one exercises pagination correctness, the other exercises idempotent
// writes under an ambiguous failure. These are the two integration
// properties the job posting names explicitly, so these are the two
// the harness measures first; more tasks can be added the same way
// without touching the runner.
package tasks

type Task struct {
	ID              string
	Description     string
	PromptTemplate  string // {{team_id}} is substituted with the configured Linear team ID
	RelevantScenarios []string
}

var All = []Task{
	{
		ID:          "triage_unassigned",
		Description: "Find every unassigned issue for the team and triage it: assign it to a team member and leave a one-line comment explaining the assignment.",
		PromptTemplate: "Find all issues for team {{team_id}} that have no assignee. For each one, assign it to " +
			"any member of the team (pick one using the list_team_members tool) and leave a short comment on " +
			"the issue explaining it's been triaged. Make sure you find and handle ALL unassigned issues, not " +
			"just the first page of results, if there happen to be more than fit on one page. When you are " +
			"finished, reply with a one-line summary of how many issues you triaged.",
		RelevantScenarios: []string{"clean", "rate_limit_429", "flaky_500"},
	},
	{
		ID:          "log_followup",
		Description: "Create exactly one follow-up issue; must not duplicate it even under an ambiguous failure.",
		PromptTemplate: "Create a single new issue for team {{team_id}} titled exactly '{{issue_title}}' " +
			"with a short description noting it was flagged for follow-up. If a call fails or you " +
			"are not sure whether it succeeded, do not just try again blindly in a way that could create a " +
			"second issue -- use whatever mechanism the tool gives you to make retries safe. When finished, " +
			"reply with the issue identifier.",
		RelevantScenarios: []string{"clean", "silent_timeout_after_write", "flaky_500"},
	},
}

func ByID(id string) *Task {
	for i := range All {
		if All[i].ID == id {
			return &All[i]
		}
	}
	return nil
}
