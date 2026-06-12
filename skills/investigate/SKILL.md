---
name: investigate
description: Investigate runtime errors, crashes, and data anomalies end-to-end. Triage logs/DB/metrics, branch to root cause, write an incident report. Use when user says "trace", "trace root cause", "cari root cause", "investigate", "kenapa error", "service mati", "anomali", "data salah", "root cause", "RCA", "gagal terus", "drop", "spike", or reports a crash/panic/OOM/mismatch/parity divergence.
when_to_use: runtime error spike, service crash/panic/OOM, data anomaly, backfill/sync mismatch, parity divergence, row-count weirdness, unexplained metric jump, "why is X broken" investigations
---

# Investigate — error & anomaly RCA orchestrator

You are an incident investigator. Goal: vague symptom → confirmed **root cause backed
by evidence** (file:line + numbers + log lines), then an incident report. Run
autonomously through all phases — stop to ask only when blocked (missing access,
ambiguous target service).

> **Two-agent split — read the "Execution model" section below BEFORE acting.** This skill
> runs across TWO agents: the **main agent** (Phase 0, 3, 4, 5) and a **nested Opus
> orchestrator** you spawn for the gather-heavy Phase 1+2. "You" means whichever agent owns
> the current phase. Do NOT run Phase 1+2 in main context — spawn the orchestrator. The
> per-phase headers tag which agent owns them.

## Operating rules

- **Delegate all gathering — enforced, not optional.** The main agent's own
  Read/Grep/Glob/Bash/codebase-memory calls are blocked by a PreToolUse hook
  (`main-agent-gather-guard.sh`) — direct gather is denied. Gather through subagents;
  this also keeps main context clean. Subagents:
  - `haiku-logs` — service/container logs, error tailing, crash traces, request tracing.
  - `haiku-db` — ClickHouse / MySQL / Redis queries, row counts, parity checks.
  - `haiku-codebase-memory` — who-calls-X, trace_path, impact analysis, find the code
    behind an error string. **Always pass project `www-wwwroot-gass-be`** so the agent
    does not guess the project.
  - `haiku-explorer` — file/symbol/pattern discovery when the code graph misses.
  - `haiku-bash` — any other verbose shell (docker ps, service status, disk, builds).
- **Haiku FETCHES, you REASON — keep the line sharp.** Subagents are cheap fetchers:
  they pull raw signal (log lines, row counts, query text, call edges) and return it
  verbatim. They do NOT form hypotheses, judge root cause, or say "EUREKA". YOU (the
  main agent) do ALL correlation, hypothesis-forming, and confirm/kill — over the
  collected evidence. Pushing judgment into a haiku = Opus's job at haiku quality:
  shallow, drift-prone. Gather wide and dumb; keep reasoning central.
- **Fan out WIDE — many narrow haiku in parallel.** Haiku is cheap and parallel spawns
  run concurrently (wall-clock ≈ slowest agent, not the sum), so for N independent facts
  spawn N agents in ONE batch — never serialize. The only limit is per-agent and it's
  about quality: ONE bounded objective, **≤8 tool calls** each. A single 28-call
  open-ended agent is the anti-pattern — that's where haiku drifts and cost grows
  super-linearly (each call re-feeds its whole transcript). Split it into narrow
  fetchers. Never resume a finished agent to "save a spawn" — re-hydrating its fat
  transcript costs MORE than a fresh narrow spawn (measured).
- **Output contract on every spawn.** End each subagent prompt with: "Return ONLY
  `file:line` + verbatim quotes + numbers. No assessment, no narration, no 'EUREKA'."
  This keeps prose out of main context — you supply the judgment, they supply the facts.
- **Evidence or it didn't happen.** Every claim needs a citation: `file:line`, a
  metric/row count, or an exact quoted log line. Unsure → say "belum yakin" + what to check.
- **Quote errors exactly.** Never paraphrase an error message or stack frame.
- **One hypothesis at a time** (default flow). State it, test it, confirm or kill it.
  Don't list five guesses — pursue the strongest, fall back only if killed. The sole
  exception is Deep mode (below), which is explicitly multi-hypothesis.

## Execution model — hybrid nested orchestrator

Gather-heavy correlation phases run **inside a nested Opus orchestrator** so the raw
fan-out never touches main context. Decision + user-interaction phases stay in **main**.
Split is fixed:

| Phase | Runs where | Why |
|---|---|---|
| 0 Recall | main | cheap `ctx_search`, no fan-out |
| 1 Triage | **orchestrator** | wide parallel fetch + signature triage |
| 2A/2B Root cause | **orchestrator** | heavy trace + correlate, no user input mid-phase |
| Deep mode | **orchestrator** | N-hypothesis fan-out |
| 3 Confirm | main | judgment + disprove + may need user |
| 4 Report | main | writes the incident file |
| 5 Chat / approval | main | only main can ask the user |

**Spawn the orchestrator** — use the dedicated `plan-orchestrator` agent (its system
prompt already carries the fan-out rules, the output contract, the return contract, and
the BLOCKED protocol — do NOT re-paste them here). One spawn per gather-heavy phase, or
one spanning 1→2 if the target is unambiguous:

```
Agent(subagent_type="plan-orchestrator", model="opus", prompt=<phase brief>)
```

The phase brief you pass MUST include, since the orchestrator sees none of this
conversation:
- The **symptom** to investigate (what/when/which service if known).
- The **project** to pass to `haiku-codebase-memory` (`www-wwwroot-gass-be`).
- The **slug** + scratch path `project-docs/incidents/.<slug>-scratch.md` to write.
- Which phase output you want back (Phase 1 signature table + label-input, OR Phase 2
  hypothesis + mechanism).

It reasons as Opus, fans out the `haiku-*` fetchers, and returns **decision + raw
citations + scratch-file path** (the return contract lives in its system prompt). On a
blocker it returns `BLOCKED: <question>` and you ask the user, then **respawn** it with
the answer — respawn means its context is gone, only the scratch file survives. Keep
blockers rare; if a phase needs the user 3×, it belonged in main.

## Phase 0 — Recall (cheap, do first)

Before gathering anything new, check if this happened before:
- `ctx_search(sort: "timeline", queries: ["<symptom keywords>", "<service name> error"])` — prior incidents, decisions, fixes from session memory.
- Skim `MEMORY.md` index for related incident notes.

If a prior fix matches the symptom, verify it still applies before re-investigating from scratch.

## Phase 1 — Triage (parallel gather, then classify)

> Runs inside the nested Opus orchestrator (see Execution model). The orchestrator
> fans out the haiku below, then returns the signature table + raw citations to main.
> **Main makes the classification call** — it is the routing decision and the one spot
> a user may be asked (ambiguous target). If the target is ambiguous, the orchestrator
> returns `BLOCKED` instead of guessing.

Pin down: **which service, what symptom, when did it start, blast radius.**

Fan out in ONE batch (parallel subagents):
- `haiku-logs` — **enumerate ALL distinct error signatures** in the incident window,
  not just the loudest/topmost one. For each: first-occurrence timestamp + verbatim
  signature + rough count. The noisiest error often masks a quieter independent one
  that surfaces only after you fix the loud one (the "fix → new error" trap, cause #3).
  List every distinct signature now so multi-cause incidents are caught up front, not
  one fix later.
- `haiku-bash` — service/container status (up? restarting? OOMKilled? exit code?).
- `haiku-db` (only if symptom smells like data) — quick sanity counts on the
  relevant table(s) around the affected time window.

**Triage the signature list before classifying.** If ≥2 distinct signatures with
independent first-occurrence times / unrelated stack frames coexist in the window,
flag **multi-cause** — they are likely separate root causes, not one. Pursue the
dominant one through Phase 2, but record the others as open fronts; do NOT assume one
fix clears all. (This is what later shows up as "fixed it, but a new error appeared" —
it was already in the logs at minute zero.)

Then **classify the incident — pick exactly ONE label before continuing. Do not
proceed to Phase 2 without a label.**

- **RUNTIME** — panic, crash, error spike, latency, OOM, restart loop → go to Phase 2A.
- **DATA** — wrong/missing rows, backfill/sync mismatch, parity divergence, count
  drift → go to Phase 2B.
- **MIXED** — symptom has both a runtime fault AND a data effect → do 2A first, then
  2B (the runtime error usually *causes* the data anomaly).
- **HEALTHY** — no error, metrics normal (services up, data fresh, counts in range) →
  **STOP. Do not enter Phase 2.** Report "sehat" with the proving evidence (status +
  freshness + counts). Investigating a non-incident chases a ghost.

<examples>
  <example>
    <symptom>report-service panic + restart loop every ~2 min, no row changes reported</symptom>
    <label>RUNTIME</label>
    <route>Phase 2A</route>
  </example>
  <example>
    <symptom>source_mirror row count < source for one cs_phone; no crash in logs</symptom>
    <label>DATA</label>
    <route>Phase 2B</route>
  </example>
  <example>
    <symptom>sync-service OOMKilled at 03:00, after restart backfill stopped writing rows for that window</symptom>
    <label>MIXED</label>
    <route>Phase 2A (the OOM) then 2B (the missing rows)</route>
  </example>
</examples>

## Phase 2A — Runtime root cause

> Runs inside the orchestrator. It owns the trace+correlate loop end-to-end (no user
> input needed mid-phase), then returns hypothesis + mechanism + raw citations to main.

1. From the log, extract the exact error string + top stack frame / file:line.
2. Map error → code via `haiku-codebase-memory`: `search_code("<error string>")` or
   `search_graph` to find the function.
3. Trace both directions in one spawn: `trace_path(function, mode=calls,
   direction=both, risk_labels=true)` + `get_code_snippet` of the implicated lines +
   the confirm-facts you'll need (e.g. "is there a handler/recovery path for the error
   case?"). Inbound = trigger path; outbound = code-level blast radius.
   `mode=cross_service` if it crosses a service boundary.
4. Reason over the returned facts: what input/state makes this path fail? State the hypothesis.
5. Confirm from facts in hand. Do failing requests match the hypothesized condition
   (timing, payload, config, deploy)? Correlate first-occurrence with
   `detect_changes(since="<last deploy ref/date>")`. Spawn a new agent only for a fact
   you don't have yet.

## Phase 2B — Data anomaly root cause

> Runs inside the orchestrator (same contract as 2A): owns the quantify+locate+map loop,
> returns hypothesis + mechanism + raw citations to main.

1. Quantify precisely with `haiku-db`: expected vs actual counts, the exact
   rows/keys that diverge, the time window.
2. Locate the divergence across the pipeline: source vs mirror vs target. Which hop
   dropped/duplicated/mangled the data? Compare row counts at each stage.
3. Map to the code in one spawn: `trace_path(<writer/loader fn>, mode=data_flow,
   direction=both)` — data_flow shows the arg expression at each hop, so you see where
   the value is mutated upstream AND where it propagates downstream. `mode=cross_service`
   when data crosses services (sync → mirror → target). In the same spawn fetch the
   confirm-facts too (e.g. "does a sweeper/recovery path exist? quote the exact WHERE clause").
4. Confirm from facts in hand: reproduce the bad transform logically against a known-bad
   key. Tie the anomaly to a specific code path, config, or backfill run (correlate with
   `detect_changes` if a deploy is suspected). Spawn a new agent only for a missing fact.

## Deep mode — competing hypotheses (opt-in)

Branch here from Phase 2 when the root cause is unclear and the flat flow has stalled
(multiple plausible causes, no single error string, cross-layer symptom), or when the
user says "investigate deep" / "competing hypotheses". Otherwise skip to Phase 3 — the
default single-hypothesis flow handles ~90% of incidents.

How deep mode runs (parallel haiku fan-out, stays flat):
1. Enumerate 2-4 competing hypotheses (e.g. "config regression from last deploy",
   "data-shape change upstream", "resource exhaustion", "race in writer").
2. Spawn ONE haiku subagent per hypothesis **in a single parallel batch**. Each
   gathers evidence (logs/DB/code) for its theory AND notes what would disprove it.
3. Collect all reports, play them against each other adversarially — which
   hypothesis survives the evidence, which are ruled out and why.
4. Fall through to Phase 3. The report (Phase 4) must note which hypotheses were
   tested and why the losers were ruled out.

## Phase 3 — Confirm root cause

> Runs in **main**, over the orchestrator's returned citations. This is the disprove
> pass — it must see raw evidence, not just the orchestrator's conclusion. If a needed
> fact is missing, spawn a fresh narrow orchestrator/haiku for that one fact; do not
> trust an unbacked claim.

Before locking the root cause, answer two mandatory questions — they catch the two ways
RCA fails (stopping at a symptom, and confirmation bias):

1. **Symptom or real cause?** Is this the proximate mechanism or the underlying cause?
   Ask "why does this condition exist?" until the answer is something that, if fixed,
   stops the whole class from recurring — not just this instance. If your suggested fix
   only masks the symptom, name the deeper cause too (e.g. "missing sweeper" is
   proximate; "the two-step process can be interrupted mid-way" is the root).

   **Pre-fix next-layer check (catches the symptom-layer "fix → new error" trap, cause
   #1).** Before locking the fix, simulate it: "if this fix lands, is the condition
   *behind* it already healthy?" Check that backing condition NOW, with evidence, not
   after deploy. Example: raising a backfill timeout → first verify the connection pool
   has headroom *right now*; if the pool is already maxed, the timeout was a symptom and
   "pool exhausted" will surface the moment you fix it. If the next layer is already
   unhealthy, you stopped the why-chain too early — keep going. Stop only when the layer
   behind the fix is provably sound.
2. **Try to disprove it.** If your hypothesis were true, what evidence SHOULD exist that
   you haven't checked yet? Go check it. Is there an alternative explanation for the same
   evidence? If you can't find disproving evidence after looking, confidence is earned —
   otherwise say "belum yakin" and what's missing.

3. **Blast-radius of the FIX, not just the bug (reduces regression, cause #2).** Before
   suggesting the change, run impact analysis on the code the fix will touch:
   `trace_path(<fn to be changed>, mode=calls, direction=inbound)` to see every caller,
   and check whether the fix mutates shared state, a config used elsewhere, or a function
   on other hot paths. Name any caller/path that could break. This cannot rule out
   regression fully — runtime effects (timing, load, data-shape) only surface after the
   fix runs — but it catches the statically-visible breakage before deploy. If the fix
   touches shared state on multiple paths, say so in the report's Suggested fix section
   and flag what to watch after rollout.

Then state the root cause in one sentence, backed by:
- **Evidence**: `file:line` of the bug, the exact log line(s), the numbers.
- **Mechanism**: why this code/config produces this symptom.
- **Trigger**: what started it (deploy, data shape, load, config) + first-seen time.
- **Blast radius**: what/who is affected, how many rows/requests (use the downstream
  trace + risk_labels from Phase 2).

If evidence is circumstantial, say so — "belum yakin, butuh X to confirm" beats fake confidence.

## Phase 4 — Write incident report

Write to `project-docs/incidents/YYYY-MM-DD-<slug>.md` (use today's date from
context; create dir if absent). Structure:

```markdown
# Incident: <short title>

- **Date**: <date>  **Service**: <svc>  **Severity**: <high/med/low>
  - high = service down or data loss/corruption; med = degraded/partial; low = cosmetic/no user impact
- **Status**: root cause confirmed | suspected | unresolved

## Symptom
<what was observed — quote errors/numbers exactly>

## Timeline
- <first-seen ts> — <event>
- ...

## Root cause
<one-paragraph mechanism, with file:line + numbers>
<if the proximate cause differs from the deeper root, name both — proximate (what
directly produced the symptom) and root (what made that condition possible)>

## Evidence
- `path/to/file.go:NN` — <what it shows>
- log: `<exact quoted line>`
- db: <expected vs actual numbers>

## Blast radius
<affected rows / requests / users — upstream trigger + downstream impact>

## Suggested fix
<concrete change — NOT applied yet, leave for approval>

## Open questions
<anything unconfirmed>
```

After the report is written, **delete the scratch handoff file**
(`project-docs/incidents/.<slug>-scratch.md`) — its content is now folded into the
incident report; leaving it is an orphan. If the investigation ended BLOCKED/unresolved,
keep the scratch file and note its path in `## Open questions` so a respawn can resume.

## Phase 5 — Chat summary

Reply in chat (Bahasa Indonesia, terse): root cause in 1-2 lines, the key
evidence, blast radius, report file path, and the suggested fix. Then **stop** —
do not apply the fix unless the user approves.

End with the handoff bridge: **"Mau rencanakan fix-nya? `/fix-plan <report-path>`."**
The incident file (with `## Root cause` + `## Suggested fix`) is the handoff —
`/fix-plan` reads it as input and won't re-investigate.
