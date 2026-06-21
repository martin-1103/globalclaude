---
name: agent-db
description: Query databases (ClickHouse, MySQL, Redis) via natural language. Use when user asks to query/inspect any DB, mentions "cek CH", "query mysql", "berapa row", "check clickhouse", "check redis", or any DB investigation question.
when_to_use: DB investigation, row counts, data inspection, schema exploration, query execution, backfill/sync data verification, cross-table analysis.
---

## How it works

`agent-db` is a Go CLI at `/usr/local/bin/agent-db`. It runs an agentic loop:
LLM emits JSON tool calls → real `docker exec` → results fed back → repeat until answer.

Tools available inside the loop: `query_clickhouse`, `query_mysql`, `query_redis`, `show_tables`, `describe_table`, `count_rows`.

Self-learning: after `describe_table`/`show_tables`, discovered schema is written to per-project `context.md` and injected into system prompt on future runs — LLM skips re-discovery for known tables.

## Usage from main agent

```bash
agent-db --project /path/to/project "your natural language question"
```

`--project` defaults to cwd. agent-db resolves per-project config from `/var/pile/agent-db/projects/<slug>/config.json`.

## When to call

Call `Bash("agent-db --project /path/to/project 'question'")` directly from main agent — the hook allows this command through (not blocked by gather guard). No subagent wrapper needed.

For parallel queries (independent questions), emit two `Bash(...)` calls in one response — they run concurrently.

## Per-project setup

```bash
agent-db init --project /path/to/project   # creates config + context.md template
agent-db projects                          # list known project slugs
```

Config path: `/var/pile/agent-db/projects/<slug>/config.json`  
Context path: `/var/pile/agent-db/projects/<slug>/context.md`

## Output format

**Default (quiet)** — answer only. Use this from main agent to keep context clean:
```
<answer text>
```

**`--verbose`** — full agentic trace (Step N, tool JSON, result tables, Chain). For debugging only:
```
Step 1: <intent>
<tool call JSON>
[tool_name]
<result>

Finding: <answer>
Chain: N steps — <step intents>
```

`--json` dumps the full Result struct (all steps + answer) as JSON.
