# agent-log

Natural-language log query agent. Accepts a question, runs an agentic loop of LLM + real HTTP/shell tool calls, and returns an answer drawn from VictoriaLogs and Docker container logs.

## Run

```bash
agent-log [flags] "your question"
```

Binary: `/usr/local/bin/agent-log`

Flags:
- `--config PATH` — path to config.json (default: `/var/pile/agent-log/config.json`)
- `--vlogs URL` — override `vlogs_url` from config
- `--verbose` — print full agentic trace (steps + tool calls)
- `--json` — output raw JSON result struct
- `--timeout N` — overall timeout in seconds

## Config

Config file: `/var/pile/agent-log/config.json`

Required fields:
- `base_url` — LLM API base URL
- `api_key` — LLM API key
- `model` — model name

Optional fields with defaults:
- `vlogs_url` — VictoriaLogs endpoint (default: `http://localhost:9428`)
- `gasslog_path` — path to gasslog.sh for docker container log access (default: `~/.claude/skills/haiku-logs/gasslog.sh`)
- `max_display_lines` — max lines returned per tool call (default: 80)
- `tool_timeout_seconds` (default: 30), `llm_timeout_seconds` (default: 60), `max_turns` (default: 15)

## Run logs

Per-run logs are written to `/var/pile/agent-log/data/runs/<timestamp>-<query>/`:
- `request.json` — inputs
- `result.json` — answer + turn count (on success)
- `failure.json` — error message (on failure)

An `index.json` (last 200 entries) sits at `/var/pile/agent-log/data/runs/index.json`.

Prune runs automatically after each successful query: keeps the 50 most recent, drops any older than 14 days.

## Troubleshooting

**`config base_url empty` / `config api_key empty`** — config missing or invalid. Check `/var/pile/agent-log/config.json`.

**`ERROR: vlogs request: ...` or `ERROR: vlogs status 4xx`** — VictoriaLogs not reachable or query syntax invalid. Verify `vlogs_url` and that the VictoriaLogs instance is running. Check query with `curl` directly.

**`ERROR: docker unavailable or timed out`** — Docker is not running or `gasslog.sh` path is wrong. Check `gasslog_path` in config and verify `docker ps` works.
