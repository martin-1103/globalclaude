---
name: agent-plan-worker
description: Use when an approved plan or task breakdown already exists and Claude needs compact execution worker to validate, critique, or apply step-by-step code changes with read-before-update safety, rollback artifacts, and minimal-noise output. Best for running tasks produced by fix-plan or impl-plan across many projects.
allowed-tools: Bash(agent-plan-worker *),Bash(/usr/local/bin/agent-plan-worker *),Bash(/var/pile/agent-plan-worker/*),Bash(go run /var/pile/agent-plan-worker/cmd/agent-plan-worker *),Bash(cd /var/pile/agent-plan-worker && go run ./cmd/agent-plan-worker *)
when_to_use: a task breakdown already exists and the main agent wants a reusable execution worker with compact output, strict file-state checks, patch safety, diff review, rollback artifacts, and optional write application
---

# Agent Plan Worker

## Overview

Use `agent-plan-worker` as executor/critic, not as planner and not as retrieval engine.
It is for the phase after the task breakdown already exists.

Target use:
- execute approved `fix-plan` or `impl-plan` tasks
- validate a proposed change bundle before write
- produce compact agent-to-agent output with low noise
- enforce read-before-update, exact-anchor patching, and rollback artifacts

Do not use it to:
- discover the codebase from scratch
- design the plan itself
- replace code search, semantic retrieval, or architecture tracing

## Default Mode

Prefer compact output for main-agent consumption:

```bash
agent-plan-worker \
  -request /abs/path/request.json \
  -output-detail compact
```

Resolved path:

```bash
/usr/local/bin/agent-plan-worker \
  -request /abs/path/request.json \
  -output-detail compact
```

Fallback if wrapper missing:

```bash
/var/pile/agent-plan-worker/agent-plan-worker \
  -request /abs/path/request.json \
  -output-detail compact
```

Fallback if binary is not built yet:

```bash
cd /var/pile/agent-plan-worker && go run ./cmd/agent-plan-worker \
  -request /abs/path/request.json \
  -output-detail compact
```

`compact` is the default choice for Claude/main-agent use because it strips most noise.

## Mental Model

`agent-plan-worker` expects a concrete task payload, then does:

1. normalize request
2. inspect live file state first
3. reject unsafe create/update assumptions early
4. send bounded context to model
5. diff-review model output
6. write rollback artifact before filesystem mutation
7. apply writes only when request explicitly allows it

This means the worker is useful both for:
- dry-run critique
- real execution with controlled writes

## Input Contracts

Typical request sources:
- `*.tasks.json` from `fix-plan`
- `*.tasks.json` from `impl-plan`
- direct JSON request assembled by the caller

Common examples live under:

```text
/var/pile/agent-plan-worker/examples/
```

Important request patterns:
- use absolute file paths when possible
- pass only the tasks/files relevant to this execution slice
- keep one request focused; do not mix unrelated changes
- prefer planner-produced `*.worker.request.json`, not mixed `*.tasks.json`

## Request / Result Contract (singular, CLI-owned)

The CLI owns the entire execution. The skill (or planner) only assembles the request
JSON and invokes `agent-plan-worker -request <file>`. There is no split where the skill
re-executes part of the work: the CLI normalizes the request, inspects file state, calls
the model, diff-reviews, writes rollback artifacts, and applies writes. Do not re-run or
re-interpret the steps in the skill.

### Request file

Canonical location and name (planner-produced):

```text
project-docs/plans/<slug>.worker.request.json
```

The CLI decodes this file into its request struct (`internal/app.Request`). Schema:

```json
{
  "mode": "tasks",                    // required; doctor rejects anything but "tasks"
  "project_id": "your-project-id",    // required, non-empty
  "repo_root": "/abs/path/repo",      // required, absolute, must exist
  "tasks_path": "/abs/.../<slug>.worker.tasks.json", // required, absolute, must end in .worker.tasks.json
  "plan_path": "",                    // optional (markdown plan; used in plan/hybrid modes)
  "profile_path": "/abs/.../profile.json", // optional; absolute if set
  "run_id": "your-run-id",            // optional; defaults to "bootstrap"
  "load_provider_config": true,       // load DEEPSEEK_* env/provider config
  "model_step_preview": false,        // preview-only model step (no live run)
  "model_step_mode": "deepseek",      // "deepseek" for live provider, else stub
  "apply_writes": false,              // false = dry-run/critique; true = mutate files
  "output_detail": "compact"          // "compact" (default for agents) or "full"
}
```

Validate a request before running it:

```bash
agent-plan-worker -request /abs/.../<slug>.worker.request.json -doctor-handoff -format text
```

`-doctor-handoff` enforces the contract (mode, absolute paths, `.worker.tasks.json`
suffix, worker-only lane, master/strong-editor partition). Fix the request, do not work
around a failing doctor.

### Per-task model pinning

A worker task may pin its model in the tasks JSON via an optional `model` field. A
task-pinned model always wins over the env default (`DEEPSEEK_MODEL`); when absent, the
env default applies. The resolved provider and model are recorded on every request in the
run's `llm.jsonl` (`resolved_provider`, `resolved_model`, `resolved_model_source`).

### Result

The CLI emits a single JSON result (`internal/output.Result`). Fields the main agent
reads:

- `status` — `ready` | `partial` | `blocked` | `ready_idle`. `ready_idle` means the
  worker is healthy but had no actionable work this run (pending items remain but none
  were runnable, e.g. dependencies unsatisfied). It is NOT completion — do not treat it
  as done.
- `execution_status` — `ready` | `partial` | `blocked` | `ready_idle` | `idle`. `idle`
  means no pending work remains (run complete); `ready_idle` means pending-but-not-actionable.
- `confidence_band`, `health`, `next_action`
- `processed_items`, `failed_items`, `blocked_items`, `task_lifecycle`
- `step_execution[]` — per task: `model_status`, `next_action`, `blocker`,
  `files_touched`, `reason_codes`
- `diff_review[]` — `verdict`, `recommended_action`, and on failure a structured
  `reason_codes` entry of the form `model_step_error:<stage>:<kind>` where `<stage>` is
  `provider` | `planner` | `executor` | `config`, so provider-fail vs planner-fail vs
  executor-fail are distinguishable without parsing the message.
- `apply[].rollback_artifact` — rollback path on write runs.

## Write Policy

By default, treat the worker as critique/validation first.

Real file mutation should happen only when the request explicitly opts in to writes.
If you only need review/patch proposal, keep it in dry-run style.

The worker already enforces:
- read-before-update
- create blocked if file already exists with different content
- update blocked if file missing or state unknown
- replace/insert/delete patch ops require exact existing anchor
- rollback artifact emitted before mutation
- conflicting patch ops on same file rejected by diffreview
- weak verification or stale/contradictory context should stop run, not trigger guessing

## Recommended Main-Agent Workflow

1. Build or obtain a task breakdown first.
2. Ensure planner already split lanes and produced worker-only request artifact.
3. Run `agent-plan-worker` with `-request ... -output-detail compact`.
4. Inspect verdict, confidence, touched files, rollback path, and verify steps.
5. Only if contract looks correct, allow apply-writes path.

For larger plans:
- run one wave at a time
- keep unrelated files in separate requests
- preserve the planner's dependency order

## Useful Commands

Run:

```bash
agent-plan-worker \
  -request /abs/path/request.json \
  -output-detail compact
```

Config doctor:

```bash
agent-plan-worker -doctor-config
```

Live step doctor:

```bash
agent-plan-worker -doctor-live-step
```

Handoff doctor:

```bash
agent-plan-worker \
  -request /abs/path/plan.worker.request.json \
  -doctor-handoff \
  -format text
```

Trace a prior run:

```bash
agent-plan-worker \
  -trace-repo /abs/path/repo \
  -trace-symbol your.qualified.Symbol \
  -format text
```

Learning maintenance:

```bash
agent-plan-worker -maintain-learning -project-id your-project-id
```

Cleanup archived learning items:

```bash
agent-plan-worker -cleanup-learning -project-id your-project-id
```

## Output Expectations

Prefer `compact` output for agent use.

What the main agent should care about:
- final verdict/status
- confidence band
- touched files
- verify notes
- rollback artifact path
- blocked reason if rejected
- stop/abstain signal when verification or boundary evidence weak

What to ignore unless debugging:
- verbose previews
- extra observability detail
- learning hints in non-compact flows

## Routing Guidance

Use `agent-plan-worker` when:
- the plan already exists
- the next step is execution or execution-grade critique
- you want safety rails around file mutation
- you need reusable behavior across many repos/projects

Use another tool first when:
- you still need retrieval/exploration
- you still need architecture reasoning
- the task is not yet broken into executable steps

Best pairing:
- `fix-plan` or `impl-plan` creates the task breakdown
- `fix-plan` or `impl-plan` also owns lane split into worker-only artifact
- `agent-plan-worker` executes or critiques worker-safe lane only

## Common Mistakes

- Do not use it as a substitute for planning.
- Do not feed it a vague goal like "fix auth".
- Do not bundle many unrelated files into one request.
- Do not rely on worker to infer missing routing fields; that is planner bug.
- Do not force execution through weak verify or stale request; blocked is correct outcome.
- Do not assume create/update silently self-heals; file state mismatch is supposed to block.
- Do not ignore rollback artifact path on write runs.
- Do not use verbose output by default for main-agent orchestration.

## Quick Reference

- Best default: `agent-plan-worker -request /abs/path/request.json -output-detail compact`
- Planner comes first, worker second
- Use small focused requests
- Dry-run/critique first, write second
- If blocked on file state, fix the request assumptions instead of forcing through
