#!/usr/bin/env python3
"""Builds a self-contained dashboard (dashboard/index.html) from results.json.

Results are embedded directly into the page, so it opens by double-clicking.
No web server, no build step, no dependencies. Re-run after a new benchmark.
"""
import json
import pathlib

results = json.load(open("results.json"))

TEMPLATE = r"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Integration Reliability Harness</title>
<style>
  :root{
    --ink:#15171c; --muted:#6a7280; --faint:#99a1ad;
    --line:#e8eaed; --track:#f1f2f4; --bg:#f6f7f8;
    --sloppy:#b4472b; --careful:#1f7a46; --lever:#2a5bd7;
  }
  *{box-sizing:border-box}
  html{-webkit-font-smoothing:antialiased}
  body{margin:0;background:var(--bg);color:var(--ink);
    font:15px/1.6 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif}
  .wrap{max-width:720px;margin:0 auto;padding:64px 28px 96px}
  .eyebrow{font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--faint);font-weight:600;margin-bottom:14px}
  h1{font-family:Georgia,"Iowan Old Style","Times New Roman",serif;
    font-weight:600;font-size:30px;line-height:1.2;letter-spacing:-.01em;margin:0 0 12px}
  .lede{color:var(--muted);font-size:16px;margin:0 0 18px;max-width:60ch}
  .meta{color:var(--faint);font-size:13px;margin:0 0 48px}
  section{margin:0 0 46px}
  .num{font-family:Georgia,serif;color:var(--faint);font-size:15px;margin-right:10px}
  h2{font-size:17px;font-weight:650;margin:0 0 4px;letter-spacing:-.01em}
  .ask{color:var(--muted);font-size:14px;margin:0 0 20px;max-width:58ch}
  .grouplbl{font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:var(--faint);font-weight:600;margin:0 0 10px}
  .row{display:flex;align-items:center;gap:16px;margin:9px 0}
  .lbl{width:124px;flex:none;text-align:right;color:var(--muted);font-size:13.5px}
  .track{flex:1;background:var(--track);height:30px;border-radius:4px;overflow:hidden}
  .fill{height:100%;display:flex;align-items:center;justify-content:flex-end;
    padding:0 11px;color:#fff;font-weight:600;font-size:13px;font-variant-numeric:tabular-nums;white-space:nowrap}
  .caption{color:var(--muted);font-size:13.5px;line-height:1.65;margin:20px 0 0;max-width:62ch}
  table{width:100%;border-collapse:collapse;margin-top:8px;font-size:14px}
  th,td{text-align:center;padding:11px 8px;border-bottom:1px solid var(--line)}
  th:first-child,td:first-child{text-align:left;color:var(--muted);font-weight:500}
  thead th{color:var(--faint);font-weight:600;font-size:12.5px;letter-spacing:.03em;border-bottom:1.5px solid var(--line)}
  .tag{display:inline-block;min-width:48px;padding:2px 9px;border-radius:5px;font-weight:600;
    font-size:12.5px;font-variant-numeric:tabular-nums;color:#fff}
  .foot{color:var(--muted);font-size:13px;line-height:1.7;margin-top:12px;padding-top:26px;border-top:1px solid var(--line);max-width:64ch}
  code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.92em;background:var(--track);padding:1px 5px;border-radius:4px}
</style>
</head>
<body>
<div class="wrap">
  <div class="eyebrow">Benchmark</div>
  <h1>Does the design of its tools decide how reliably an AI agent works?</h1>
  <p class="lede">One Linear integration, built two ways, measured against a real agent while failures are injected underneath it.</p>
  <p class="meta" id="meta"></p>

  <section>
    <h2><span class="num">01</span>Completeness under normal conditions</h2>
    <p class="ask">A backlog of 15 tickets to triage. How many does the agent actually handle?</p>
    <div id="completeness"></div>
    <p class="caption" id="completeness-cap"></p>
  </section>

  <section>
    <h2><span class="num">02</span>Reliability under server errors</h2>
    <p class="ask">The same create task, but the API intermittently returns 500s. Does the agent still finish the job?</p>
    <div id="errors"></div>
    <p class="caption" id="errors-cap"></p>
  </section>

  <section>
    <h2><span class="num">03</span>Which single design choice causes it?</h2>
    <p class="ask">Each middle bar changes one thing on top of the sloppy baseline, so the gain traces to that choice alone, not to the careful version as a whole.</p>
    <div class="grouplbl">Completeness, tickets handled</div>
    <div id="abl-complete"></div>
    <div class="grouplbl" style="margin-top:22px">Reliability under 500s, success rate</div>
    <div id="abl-errors"></div>
    <p class="caption" id="abl-cap"></p>
  </section>

  <section>
    <h2><span class="num">04</span>Every fault scenario</h2>
    <p class="ask">Create task success rate across each failure we injected.</p>
    <table id="scenario-table"></table>
  </section>

  <p class="foot" id="foot"></p>
</div>

<script>
const DATA = __DATA__;
const SLOPPY="var(--sloppy)", CAREFUL="var(--careful)", LEVER="var(--lever)";

function row(label, pct, color, text){
  return `<div class="row"><div class="lbl">${label}</div>
    <div class="track"><div class="fill" style="width:${Math.max(pct,6)}%;background:${color}">${text}</div></div></div>`;
}

document.getElementById('meta').textContent =
  `${DATA.length} agent runs. Model claude-haiku-5-5. Real Linear API, with deterministic fault injection.`;

function triageAvg(variant){
  const rows = DATA.filter(r=>r.task==="triage_unassigned" && r.variant===variant && r.scenario==="clean");
  if(!rows.length) return null;
  const avg = rows.reduce((s,r)=>s+r.triaged,0)/rows.length;
  const total = rows[0].total;
  return {avg, total, pct: Math.round(100*avg/total)};
}
function successRate(variant, scenario){
  const rows = DATA.filter(r=>r.task==="log_followup" && r.scenario===scenario && r.variant===variant);
  const s = rows.filter(r=>r.success).length;
  return {s, n: rows.length, pct: rows.length? Math.round(100*s/rows.length):0};
}

const tn = triageAvg("naive"), th = triageAvg("hardened");
document.getElementById('completeness').innerHTML =
  row("Sloppy tools", tn.pct, SLOPPY, `${tn.pct}%, ${tn.avg.toFixed(0)} of ${tn.total}`) +
  row("Careful tools", th.pct, CAREFUL, `${th.pct}%, ${th.avg.toFixed(0)} of ${th.total}`);
document.getElementById('completeness-cap').innerHTML =
  `The sloppy <code>list</code> tool returns one page and never signals that more tickets exist, so the agent stops early on every run, handling ${tn.pct}% of the backlog. The careful tool reports <code>has_more</code> and a cursor, so the agent works through all of it. Same model, same tickets.`;

const en = successRate("naive","flaky_500"), eh = successRate("hardened","flaky_500");
document.getElementById('errors').innerHTML =
  row("Sloppy tools", en.pct, SLOPPY, `${en.s} of ${en.n}`) +
  row("Careful tools", eh.pct, CAREFUL, `${eh.s} of ${eh.n}`);
document.getElementById('errors-cap').innerHTML =
  `One generic error message leaves the agent unsure whether a failure is worth retrying. Typed, retryable errors with a <code>retry_after</code> hint tell it exactly what to do, so it recovers.`;

const pagOnly = triageAvg("pagination_only");
const typedOnly = successRate("typed_errors_only","flaky_500");
document.getElementById('abl-complete').innerHTML =
  row("Sloppy", tn.pct, SLOPPY, `${tn.pct}%`) +
  row("Add pagination", pagOnly.pct, LEVER, `${pagOnly.pct}%`) +
  row("Careful (all)", th.pct, CAREFUL, `${th.pct}%`);
document.getElementById('abl-errors').innerHTML =
  row("Sloppy", en.pct, SLOPPY, `${en.s} of ${en.n}`) +
  row("Add typed errors", typedOnly.pct, LEVER, `${typedOnly.s} of ${typedOnly.n}`) +
  row("Careful (all)", eh.pct, CAREFUL, `${eh.s} of ${eh.n}`);
document.getElementById('abl-cap').innerHTML =
  `Adding only pagination to the sloppy tool recovers the whole completeness gap, from ${tn.pct}% to ${pagOnly.pct}%. Adding only typed errors recovers the whole reliability gap, from ${en.s}/${en.n} to ${typedOnly.s}/${typedOnly.n}. Each effect traces to one specific design property.`;

const scenarios = [...new Set(DATA.filter(r=>r.task==="log_followup").map(r=>r.scenario))];
const pretty = {clean:"Clean, no faults", silent_timeout_after_write:"Lost response after write",
  rate_limit_429:"Rate limiting (429)", flaky_500:"Server errors (500)"};
let html = `<thead><tr><th>Fault scenario</th><th>Sloppy tools</th><th>Careful tools</th></tr></thead><tbody>`;
for(const sc of scenarios){
  const n = successRate("naive",sc), h = successRate("hardened",sc);
  const tag = o => { const c = o.pct===100?CAREFUL:(o.pct<=50?SLOPPY:"var(--muted)");
    return `<span class="tag" style="background:${c}">${o.s} / ${o.n}</span>`; };
  html += `<tr><td>${pretty[sc]||sc}</td><td>${tag(n)}</td><td>${tag(h)}</td></tr>`;
}
html += `</tbody>`;
document.getElementById('scenario-table').innerHTML = html;

document.getElementById('foot').innerHTML =
  `Neither design produced duplicate writes in these runs. Even the sloppy agent tended to check before retrying, so idempotency was not the deciding factor here; completeness and error recovery were. Every run used the same model against the real Linear API, changing only the tool design.`;
</script>
</body>
</html>
"""

out = TEMPLATE.replace("__DATA__", json.dumps(results))
pathlib.Path("dashboard").mkdir(exist_ok=True)
pathlib.Path("dashboard/index.html").write_text(out)
print("wrote dashboard/index.html (" + str(len(results)) + " runs embedded)")
