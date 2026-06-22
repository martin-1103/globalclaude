# agent-db

Natural-language database query agent. Accepts a question, runs an agentic loop of LLM + real `docker exec` tool calls, and returns an answer.

## Run

```bash
agent-db [flags] "your question"
agent-db init --project /path/to/project
agent-db projects
```

Binary: `/usr/local/bin/agent-db`

Flags:
- `--project PATH` — project path for per-project config/context (default: cwd)
- `--config PATH` — global config path (default: `/var/pile/agent-db/config.json`)
- `--verbose` — print full agentic trace (steps + tool calls)
- `--json` — output raw JSON result struct
- `--timeout N` — overall timeout in seconds

## Config

**Global config**: `/var/pile/agent-db/config.json`

Required fields:
- `base_url` — LLM API base URL
- `api_key` — LLM API key
- `model` — model name

Optional fields with defaults: `query_limit` (50), `max_display_rows` (20), `tool_timeout_seconds` (30), `llm_timeout_seconds` (60), `max_turns` (8)

**Per-project config**: `/var/pile/agent-db/projects/<slug>/config.json`

Created by `agent-db init --project /path`. Contains `containers` (docker container names for clickhouse/mysql/redis/postgres), `credentials`, and optional `env_file`. Per-project values override the global config. Containers and credentials that are empty are auto-detected via `docker ps` and `docker inspect` at startup, then written back to the per-project config.

**Project context**: `/var/pile/agent-db/projects/<slug>/context.md`

Schema notes discovered by the agent (table/column descriptions) are appended here and injected into the system prompt on future runs, skipping re-discovery.

## Run logs

Per-run logs are written to `/var/pile/agent-db/data/runs/<slug>/<timestamp>-<query>/`:
- `request.json` — inputs
- `result.json` — answer + steps (on success)
- `failure.json` — error message (on failure)
- `summary.txt` — human-readable summary
- `llm.jsonl` — raw LLM request/response log

An `index.json` (last 200 entries) sits at `/var/pile/agent-db/data/runs/<slug>/index.json`.

Prune runs automatically after each successful query: keeps the 50 most recent, drops any older than 14 days.

## Troubleshooting

**`config base_url empty` / `config api_key empty`** — global config missing or invalid. Check `/var/pile/agent-db/config.json`.

**`ERROR: docker exec ... exit status 1`** — container name wrong or container not running. Run `agent-db init --project .` to create a per-project config, then fill in the correct container names. Or check `docker ps`.

**Answer is "I was unable to find..."** — the per-project context.md may be stale or empty. Delete it and let the agent rediscover schema, or edit it manually with correct table names.
