# Integration Reliability Harness

**Does the *design* of an AI agent's tools, not the model, decide how reliably it works? This measures it, instead of asserting it.**

An AI agent is only as good as its "hands and eyes": the tools it's given to act on the outside world. Building a tool that *works* is easy. Building one that makes an agent *reliable*, under pagination, rate limits, server errors, and lost responses, is the hard part, and it's mostly invisible until something breaks in production.

This project takes one real integration (Linear, the issue tracker), implements it **twice**, a `naive` version and a `hardened` version that expose the *same* underlying actions, then runs a real LLM agent against both under deliberately injected faults, and measures the difference.

The headline: **with the same model, the same tasks, and the same tickets, changing only the tool design moved the agent from handling 33% of the work to 100%.**

---

## Results

32 agent runs, model `claude-haiku-5-5`, against the real Linear API.

### 1. Completeness (normal conditions)
Task: triage a backlog of 15 tickets, assign and comment on each.

| Tool design | Tickets handled | |
|---|---|---|
| Naive | **5 of 15 (33%)** | every run |
| Hardened | **15 of 15 (100%)** | every run |

The naive `list` tool returns one page and gives the agent **no signal that more tickets exist**, so the agent confidently stops early, every single time. The hardened tool exposes `has_more` + a cursor, so the agent pages through everything. This gap is *structural*: no amount of model intelligence can fix a tool that hides information.

### 2. Reliability under server errors
Task: create a follow-up ticket while the API intermittently returns `500`s.

| Tool design | Succeeded |
|---|---|
| Naive | **1 of 3** |
| Hardened | **3 of 3** |

The naive tool collapses every failure into one generic message, so the agent can't tell "retry me" from "give up." The hardened tool returns typed, retryable errors with a `retry_after` hint, so the agent recovers correctly.

### Which single design choice causes it? (ablation)
To rule out "you just built one good tool and one bad one," each effect is isolated to a *single* change on top of the naive baseline:

| Completeness | | | Success under 500s | |
|---|---|---|---|---|
| Naive | 33% | | Naive | 1/3 |
| **+ pagination only** | **100%** | | **+ typed errors only** | **3/3** |
| Hardened (all) | 100% | | Hardened (all) | 3/3 |

Adding *only* pagination recovers the entire completeness gap; adding *only* typed errors recovers the entire error-recovery gap. Each effect traces to a specific design property, not to the bundle as a whole. This also answers the "the naive tool is a strawman" objection: independent 2026 research finds ~97% of real agent tool descriptions carry at least one defect and 56% don't even state what the tool does, the naive version is representative of how integrations actually ship, not a rigged baseline.

### Full fault picture (create task)
| Scenario | Naive | Hardened |
|---|---|---|
| Clean | 3/3 | 3/3 |
| Lost response after write | 3/3 | 3/3 |
| Rate limiting (429) | 3/3 | 3/3 |
| Server errors (500) | **1/3** | **3/3** |

**Honest finding:** neither design produced duplicate writes, even the naive agent tended to double-check before retrying, so idempotency wasn't the differentiator here. The real differentiators were **completeness** and **error recovery**. (The hardened `create` still implements true idempotency, see below, it just wasn't the deciding factor in these runs.)

Open `dashboard/index.html` in a browser for the interactive version.

---

## What actually differs between the two tool designs

Both variants call the *same* Linear client underneath. Only the agent-facing contract changes:

| Dimension | Naive | Hardened |
|---|---|---|
| **Pagination** | Returns one page, no signal more exists | Exposes `has_more` + `next_cursor`; description tells the agent to keep paging |
| **Errors** | One generic string for every failure | Typed codes (`rate_limited`, `server_error`, `not_found`, …) with a `retryable` flag and `retry_after` |
| **Idempotency** | None, a retried create can duplicate | `create_issue` requires an idempotency key, embedded in the ticket so a retry (even after a *lost response*) rediscovers the ticket instead of duplicating |
| **Permissions** | One credential does everything | Write tools require a separate write-scoped credential |
| **Descriptions** | Terse ("Lists issues.") | Written for the model: states failure modes and how to handle them |

---

## Architecture

```
 benchmark-runner  ──HTTP──▶  tool-server  ──HTTP──▶  chaos-proxy  ──HTTPS──▶  Linear API
 (drives Claude as         (naive + hardened        (deterministic,          (real issues)
  the agent under test)     tool variants)            seeded fault injection)
```

Three small Go services, deliberately separated so the two variables, **tool design** and **failure conditions**, can be changed independently:

- **`tool-server/`**, exposes both tool variants over HTTP, sharing one Linear client. The naive/hardened difference lives entirely in `internal/tools/`.
- **`chaos-proxy/`**, sits in front of Linear and injects faults (latency, 429s, 500s, and the subtle "write committed but response lost" case) deterministically per `(seed, call-index)`, so both variants face the *identical* fault sequence for a fair comparison.
- **`benchmark-runner/`**, drives Claude through a fixed battery of tasks against each variant × scenario, scores the outcomes (completeness from the real board, not just the transcript), and writes `results.json`.
- **`dashboard/index.html`**, a dependency-free page that reads the results. No build step.

Stack: **Go** backend, **TypeScript/Vite-free self-contained HTML** frontend, **real Linear API**, **Anthropic API** for the agent. (Go + a lightweight TS-friendly frontend mirrors the kind of stack these integrations live in.)

---

## Running it

Prerequisites: Go 1.24+, a free Linear workspace + personal API key, an Anthropic API key.

```bash
# 1. Configure secrets (never committed; see .gitignore)
cp .env.example .env    # then fill in LINEAR_API_KEY, LINEAR_TEAM_ID, ANTHROPIC_API_KEY

# 2. Seed a realistic backlog into your Linear sandbox
./scripts/seed_linear.sh

# 3. Start the servers (two terminals, or background them)
LINEAR_UPSTREAM_URL=https://api.linear.app CHAOS_PROXY_ADDR=:8082 go run ./chaos-proxy
LINEAR_BASE_URL=http://localhost:8082/graphql TOOLSERVER_ADDR=:8081 go run ./tool-server/cmd/toolserver

# 4. Run the benchmark (results -> results.json)
cd benchmark-runner
TOOLSERVER_URL=http://localhost:8081 CLAUDE_MODEL=claude-haiku-5-5 \
  go run ./cmd/bench -tasks=triage_unassigned,log_followup \
  -variants=naive,hardened -scenarios=clean,flaky_500 -repeats=3

# 5. Rebuild the dashboard from the latest results
python3 scripts/build_dashboard.py
```

`cmd/smoke` runs a single agent against one ticket, the cheapest way to confirm your keys work end to end.

---

## Agent-facing tools

`list_issues`, `list_team_members`, `assign_issue`, `add_comment`, `create_issue`, exposed by both variants. The agent calls them over HTTP exactly as any tool-use framework would; see `tool-server/internal/tools/schema.go` for the contract and the per-variant descriptions/input schemas in `naive.go` and `hardened.go`.

## Security

- Secrets live only in `.env` (git-ignored); they never enter the repo.
- The agent never holds the Linear credential, only the tool-server does.
- The hardened variant models least-privilege: write tools require a write-scoped credential, read tools don't.

## Testing

`go test ./tool-server/... ./chaos-proxy/... ./benchmark-runner/...`, covers error classification, idempotency, the scoring logic, and the determinism of the fault injection (same seed ⇒ same fault sequence).

## Known limitations & honest caveats

- **One integration, one model.** Results are demonstrated on Linear with a single small model. The *mechanism* generalizes, but the exact numbers would shift with other APIs/models.
- **Small-model variance.** The cheap model occasionally fumbles execution independently of tool design; the benchmark reports per-run outcomes rather than hiding this.
- **Page size is set small** (5) so the multi-page case is visible with a modest backlog, the pagination effect is structural, not an artifact of the page size.
- **Not production software.** It's a measurement harness and a demonstration, not a drop-in library.
