// cmd/smoke is the cheapest possible end-to-end proof: one agent run,
// hardened tools, no chaos, no database. It asks the agent to triage a
// single ticket so we can watch the whole machinery (Claude -> tool server
// -> Linear and back) work before spending anything on the full benchmark.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"integration-harness/benchmark-runner/internal/runner"
	"integration-harness/benchmark-runner/internal/scoring"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	toolURL := os.Getenv("TOOLSERVER_URL")
	key := os.Getenv("ANTHROPIC_API_KEY")
	team := os.Getenv("LINEAR_TEAM_ID")
	model := os.Getenv("CLAUDE_MODEL")
	if toolURL == "" || key == "" || team == "" {
		logger.Error("need TOOLSERVER_URL, ANTHROPIC_API_KEY, LINEAR_TEAM_ID set")
		os.Exit(1)
	}

	prompt := strings.ReplaceAll(
		"You are triaging a support backlog. Using team {{team_id}}: list the issues, "+
			"pick the ONE unassigned issue with the highest priority, assign it to any member "+
			"of the team, and leave a short comment saying it has been triaged. Then stop and "+
			"report in one sentence what you did.",
		"{{team_id}}", team)

	cfg := runner.Config{
		ToolServerBaseURL: toolURL,
		ClaudeAPIKey:      key,
		ClaudeModel:       model,
		Variant:           "hardened",
		WriteScope:        "write",
		Scenario:          "clean",
		Seed:              "smoke",
		TeamID:            team,
		MaxTurns:          8,
		ToolCallTimeout:   15 * time.Second,
	}

	fmt.Println("=== SMOKE TEST: one agent run (hardened tools, no chaos) ===")
	out, err := runner.Run(context.Background(), cfg, prompt, scoring.Scorers["triage_unassigned"], logger)
	if err != nil {
		logger.Error("run failed", "err", err)
		os.Exit(1)
	}
	fmt.Printf("\n=== RESULT ===\nsucceeded=%v  duplicate_write=%v  tool_calls=%d  retries=%d  wall=%s\n",
		out.Score.Succeeded, out.Score.DuplicateWrite, out.TotalCalls, out.TotalRetries, out.WallClock)
	if out.Score.FailureReason != "" {
		fmt.Println("note:", out.Score.FailureReason)
	}
}
