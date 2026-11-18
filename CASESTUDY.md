# Case study: building the Integration Reliability Harness

A short account of why I built this, what I found, and the decisions along the way.

## Why this problem

I started from Dataleap's own description of the work: integrations are "the hands and eyes of an AI agent," and the hard part isn't connecting an API, it's crafting the tools so that *"every word in a tool description, every system hint"* makes the agent more reliable.

That's a claim about tool *design* mattering more than most people assume. I wanted to know: is it measurable? Before building anything, I checked whether the idea was already solved. Auto-*generating* agent tools from API specs turned out to be crowded, open-source generators, funded startups, even academic papers with self-improving loops. But *measuring* whether a given tool design actually makes an agent reliable, dynamically, under real failure conditions, was far less commoditized. That gap is where I aimed.

## What I built

One real integration (Linear), implemented twice against the same backend client:
- a **naive** version, the thin wrapper most first-pass integrations actually are, and
- a **hardened** version, the same actions, but with explicit pagination, typed errors, idempotency, and least-privilege scoping.

Around it, a harness that drives a real LLM agent through realistic multi-step tasks against both versions, while a proxy injects deterministic faults (latency, 429s, 500s, and the subtle "write committed but the response was lost" case). It measures outcomes from the **real board state**, not just the agent's transcript, and writes the numbers to a results file a dashboard reads.

## Key technical decisions

- **Separate the two variables.** Tool design lives in the tool-server; failure conditions live in a standalone chaos proxy. Keeping them independent is what makes the comparison fair and the results interpretable.
- **Deterministic, seeded faults.** Both variants face the identical fault sequence for a given seed, so a difference in outcome can't be blamed on luck.
- **Measure side-effects, not self-reports.** Early on I scored the triage task from the agent's transcript, but under a lost-response fault the agent's view and reality diverge. Counting the actual tickets on the board is the only honest measure.
- **Real idempotency, not a toy.** The hardened `create` embeds its idempotency key in the ticket itself, so even when a write's response is lost it can rediscover the ticket and avoid a duplicate, rather than relying on in-memory state that a dropped response would skip.

## The most useful thing I learned (a hypothesis that failed)

My first headline was going to be "sloppy tools cause duplicate writes." It didn't hold up: even with the naive tools, the agent tended to check before retrying, so it rarely duplicated. I kept that finding visible rather than burying it.

The real, robust differences turned out to be elsewhere, and they're stronger:
- **Completeness.** The naive `list` tool returns one page with no signal that more exist, so the agent handled **33% of a 15-ticket backlog, every single run**. The hardened tool paged through everything: **100%.** This is structural, no smarter model can page through data a tool refuses to reveal.
- **Error recovery.** Under intermittent 500s, generic errors left the agent unsure whether to retry; typed, retryable errors let it recover. Success went from **1/3 to 3/3.**

To make sure these weren't just "the good bundle beats the bad bundle," I ran an **ablation**: versions that change exactly one thing on top of the naive baseline. Adding *only* pagination recovered the whole completeness gap (33% → 100%); adding *only* typed errors recovered the whole reliability gap (1/3 → 3/3). So each effect is attributable to one specific design property, which is the part I'd actually want to defend in a review.

## How I tested reliability

Unit tests cover the error classification, the idempotency store, the scoring logic, and, importantly, the determinism of the fault injection (same seed ⇒ same faults). The benchmark itself is the integration test: it runs the whole stack against the live API.

## What I'd do next

- Extend the ablation to every dimension (idempotency, permission scoping) and rank them by impact.
- Extend to a second integration to show the mechanism isn't Linear-specific.
- Run a larger model and more repeats to separate tool-design effects from small-model execution variance.
- Add an automatic "diagnosis" step: given a failed run, attribute the failure to the specific tool-contract defect and suggest the fix.

## Honest scope

This is a measurement harness and a demonstration, not production software, and the numbers are from one model on one API. I used AI coding tools to move quickly, but I made the architectural calls and can walk through any decision here. I'm not claiming to be a seasoned Go engineer, I'm showing that I understand how agents actually fail at the integration layer, and that I can build something real to prove a point.
