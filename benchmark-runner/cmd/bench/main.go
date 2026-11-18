// cmd/bench runs the experiment matrix: for each (task, tool variant, chaos
// scenario) it resets the board, runs one agent, and records the outcome.
// Results are written to a JSON file the dashboard reads — no database server
// required, so anyone can clone the repo and run it.
//
// It is flag-driven so we can run a tiny slice cheaply first, then the full
// matrix once we're happy:
//
//	go run ./cmd/bench -tasks=log_followup -variants=naive,hardened \
//	    -scenarios=clean,silent_timeout_after_write -repeats=1 -out=results.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"integration-harness/benchmark-runner/internal/linearadmin"
	"integration-harness/benchmark-runner/internal/runner"
	"integration-harness/benchmark-runner/internal/scoring"
	"integration-harness/benchmark-runner/internal/tasks"
)

const followupPrefix = "Investigate flaky deploy pipeline"

type Result struct {
	Task      string `json:"task"`
	Variant   string `json:"variant"`
	Scenario  string `json:"scenario"`
	Repeat    int    `json:"repeat"`
	Success   bool   `json:"success"`
	Duplicate bool   `json:"duplicate_write"`
	Recovered bool   `json:"recovered_from_fault"`
	Triaged   int    `json:"triaged,omitempty"` // triage task: tickets actually assigned
	Total     int    `json:"total,omitempty"`   // triage task: total tickets on the board
	ToolCalls int    `json:"tool_calls"`
	Retries   int    `json:"retries"`
	WallMS    int64  `json:"wall_ms"`
	Note      string `json:"note,omitempty"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tasksFlag := flag.String("tasks", "triage_unassigned,log_followup", "comma-separated task IDs")
	variantsFlag := flag.String("variants", "naive,hardened", "comma-separated variants")
	scenariosFlag := flag.String("scenarios", "clean", "comma-separated chaos scenarios")
	repeats := flag.Int("repeats", 1, "repetitions per cell")
	out := flag.String("out", "results.json", "output JSON path")
	flag.Parse()

	toolURL := mustEnv(logger, "TOOLSERVER_URL")
	key := mustEnv(logger, "ANTHROPIC_API_KEY")
	team := mustEnv(logger, "LINEAR_TEAM_ID")
	linKey := mustEnv(logger, "LINEAR_API_KEY")
	model := os.Getenv("CLAUDE_MODEL")

	admin := linearadmin.New(linKey)
	ctx := context.Background()

	variants := split(*variantsFlag)
	scenarios := split(*scenariosFlag)
	taskIDs := split(*tasksFlag)

	var results []Result
	for _, taskID := range taskIDs {
		task := tasks.ByID(taskID)
		if task == nil {
			logger.Error("unknown task", "task", taskID)
			continue
		}
		basePrompt := strings.ReplaceAll(task.PromptTemplate, "{{team_id}}", team)
		for _, scenario := range scenarios {
			for _, variant := range variants {
				for i := 0; i < *repeats; i++ {
					// Clean slate before every run.
					if err := admin.ResetBoard(ctx, team, followupPrefix); err != nil {
						logger.Error("reset failed", "err", err)
					}
					time.Sleep(600 * time.Millisecond) // let Linear settle

					seed := fmt.Sprintf("%s-%s-%s-%d", taskID, variant, scenario, i)

					// Give each create-task run a UNIQUE ticket title, so the
					// duplicate count is isolated per run and can't be polluted
					// by leftover tickets from an earlier run that is still
					// being cleaned up on Linear's side.
					runTitle := followupPrefix
					prompt := basePrompt
					if taskID == "log_followup" {
						runTitle = fmt.Sprintf("%s [%s]", followupPrefix, seed)
						prompt = strings.ReplaceAll(basePrompt, "{{issue_title}}", runTitle)
					}

					scope := ""
					if variant == "hardened" {
						scope = "write"
					}
					// Triage walks many tickets (list pages + assign + comment
					// each), so it needs a higher turn budget than the short
					// create task.
					maxTurns := 10
					if taskID == "triage_unassigned" {
						maxTurns = 45
					}
					cfg := runner.Config{
						ToolServerBaseURL: toolURL,
						ClaudeAPIKey:      key,
						ClaudeModel:       model,
						Variant:           variant,
						WriteScope:        scope,
						Scenario:          scenario,
						Seed:              seed,
						TeamID:            team,
						MaxTurns:          maxTurns,
						ToolCallTimeout:   12 * time.Second,
					}

					logger.Info("RUN", "task", taskID, "variant", variant, "scenario", scenario, "i", i)
					o, err := runner.Run(ctx, cfg, prompt, scoring.Scorers[taskID], logger)
					r := Result{Task: taskID, Variant: variant, Scenario: scenario, Repeat: i}
					if err != nil {
						r.Note = "runner error: " + err.Error()
						results = append(results, r)
						logger.Error("run error", "err", err)
						continue
					}
					r.ToolCalls, r.Retries, r.WallMS = o.TotalCalls, o.TotalRetries, o.WallClock.Milliseconds()

					if taskID == "log_followup" {
						// Measure duplicates from the BOARD, not the transcript:
						// under silent-timeout the agent may create issues it
						// never got a success response for.
						count, cerr := admin.CountFollowups(ctx, team, runTitle)
						if cerr != nil {
							r.Note = "count error: " + cerr.Error()
						}
						r.Success = count >= 1
						r.Duplicate = count > 1
						r.Recovered = r.Success && !r.Duplicate && scenario != "clean"
						r.Note = fmt.Sprintf("board follow-up count = %d", count)
					} else {
						// Triage: measure completeness from the board — how many
						// of the tickets did the agent actually assign? This is
						// the honest pagination signal: the naive tool hides that
						// more pages exist, so the agent leaves tickets untouched.
						assigned, total, berr := admin.BoardStats(ctx, team)
						if berr != nil {
							r.Note = "boardstats error: " + berr.Error()
						}
						r.Triaged, r.Total = assigned, total
						r.Success = total > 0 && assigned == total
						r.Recovered = o.Score.RecoveredFromFault
						r.Note = fmt.Sprintf("triaged %d of %d tickets", assigned, total)
					}
					results = append(results, r)
					logger.Info("DONE", "success", r.Success, "duplicate", r.Duplicate, "calls", r.ToolCalls, "note", r.Note)
				}
			}
		}
	}

	// Clean up the board at the end.
	_ = admin.ResetBoard(ctx, team, followupPrefix)

	buf, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		logger.Error("write results", "err", err)
		os.Exit(1)
	}
	printSummary(results)
	fmt.Printf("\nWrote %d results to %s\n", len(results), *out)
}

func printSummary(results []Result) {
	fmt.Println("\n=== SUMMARY (success / duplicate-write rate per variant x scenario) ===")
	type agg struct{ n, success, dup int }
	m := map[string]*agg{}
	order := []string{}
	for _, r := range results {
		k := r.Scenario + " | " + r.Variant
		if m[k] == nil {
			m[k] = &agg{}
			order = append(order, k)
		}
		a := m[k]
		a.n++
		if r.Success {
			a.success++
		}
		if r.Duplicate {
			a.dup++
		}
	}
	for _, k := range order {
		a := m[k]
		fmt.Printf("  %-45s  runs=%d  success=%d/%d  duplicates=%d/%d\n", k, a.n, a.success, a.n, a.dup, a.n)
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mustEnv(logger *slog.Logger, k string) string {
	v := os.Getenv(k)
	if v == "" {
		logger.Error("missing env var", "key", k)
		os.Exit(1)
	}
	return v
}
