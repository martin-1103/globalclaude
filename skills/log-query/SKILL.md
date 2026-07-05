---
name: log-query
description: Query VictoriaLogs / docker container logs directly via ctx_execute curl, no LLM sub-agent hop. Use when user asks about logs, errors, container output ("cek logs", "kenapa error 500", "log container X"). Replaces agent-log CLI — main agent composes the LogsQL filter itself.
---

# Log query — direct, main-agent-composed

Replaces `agent-log` CLI. Main agent writes the LogsQL query or docker-log call
directly instead of going through an LLM sub-agent translation hop.

## VictoriaLogs (LogsQL)

Endpoint: `http://localhost:9428/select/logsql/query`

```
ctx_execute(language: "shell", code: "
  curl -s 'http://localhost:9428/select/logsql/query' -d 'query=<LogsQL>' | head -c 20000
")
```

LogsQL basics:
- Filter by phrase: `error`  — bare word matches log lines containing it.
- Field match: `_stream:{container=\"gassv4-mysql\"}` or `_msg:~\"regex\"`.
- Time range: append `_time:5m` (last 5 min), `_time:1h`, `_time:[2026-07-01, 2026-07-02]`.
- Combine with `AND`/`OR`/`NOT`: `error AND _stream:{container=\"sync-service\"}`.
- Full syntax: https://docs.victoriametrics.com/victorialogs/logsql/

## Docker container logs (no VictoriaLogs ingestion, or need raw tail)

Reuse `gasslog.sh` (`/var/pile/agent-log/gasslog.sh`) — deterministic, no LLM,
already handles error/warn dedup and hung-container timeouts:

```
ctx_execute(language: "shell", code: "/var/pile/agent-log/gasslog.sh list")            # discover containers
ctx_execute(language: "shell", code: "/var/pile/agent-log/gasslog.sh logs <name> 30m") # errors/warns, last 30m, deduped
ctx_execute(language: "shell", code: "/var/pile/agent-log/gasslog.sh logs <name> 1h ALL") # every level
ctx_execute(language: "shell", code: "/var/pile/agent-log/gasslog.sh logs <name> 1h 'projection_gap'") # content grep, deduped
```

`gasslog.sh logs` mode arg: default = errors/warns only, `ALL` = every level,
anything else = treated as a `grep -iE` regex against log content.

## Workflow

1. Don't guess container names — `gasslog.sh list [pattern]` or
   `docker ps --format '{{.Names}}'` via `ctx_execute` first.
2. Pick VictoriaLogs when the question needs cross-container search or a time-range
   aggregate; pick `gasslog.sh` for one container's raw/filtered tail.
3. Compose the query/filter yourself — don't paraphrase what you're looking for into
   an NL string for a sub-agent to reinterpret.
4. Large output → stays inside `ctx_execute` (auto-sandboxed); print only the
   matching lines / counts you need.

## What NOT to do

Don't fall back to bare `docker logs <container>` or manual `docker exec` log tails —
`gasslog.sh` already guards against a wedged `docker logs` call hanging the session
(`GASSLOG_TIMEOUT`, default 30s) and bounds the scan (`GASSLOG_TAIL`, default 5000 lines).
