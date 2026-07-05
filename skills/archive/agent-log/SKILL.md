---
name: agent-log
description: Query logs via natural language — VictoriaLogs (structured app logs) and Docker container logs. Use when user asks about errors, incidents, service health, "cek log", "ada error di", "kenapa service X", or any log investigation question.
when_to_use: Error investigation, incident triage, service health check, log pattern search, cross-service error correlation.
---

## How it works

`agent-log` is a Go CLI at `/usr/local/bin/agent-log`. It runs an agentic loop:
LLM emits JSON tool calls → real HTTP/shell → results fed back → repeat until answer.

Tools available inside the loop:
- `query_vlogs` — LogsQL query to VictoriaLogs at `http://localhost:9428`
- `list_containers` — list running Docker containers (via gasslog.sh)
- `container_logs` — filtered container logs (via gasslog.sh): SIGNAL mode (errors/warns only), ALL mode, or regex content search

## When NOT to Use

- **Reasoning/causation questions** — agent-log is FETCH only. Never ask "kenapa error naik?" or "apa penyebab X?"; it returns log lines, not root causes. Ask "count ERROR per jam, 3 jam terakhir, service=X" — you reason from the numbers.
- **DB queries** — use `agent-db` instead; agent-log has no database access.
- **Events not in VictoriaLogs or Docker** — agent-log only reaches VictoriaLogs (`http://localhost:9428`) and Docker container logs via gasslog.sh. External log platforms, files, or metrics are out of scope.
- **Bulk export** — `limit` caps at 500 lines per query_vlogs call. Not suitable for downloading full log archives.

## End-to-end example

```
# Count errors per hour across two services
agent-log "count ERROR per hour last 3 hours for service api-gateway and service sync-service"

# Check recent container crash markers
agent-log "any OOMKilled or panic in report-worker container last 30 minutes"

# Override VictoriaLogs URL for a different environment
agent-log --vlogs http://staging:9428 "level:error last 1h"

# Full trace for debugging
agent-log --verbose "what errors hit webhook-ingestion in the last 2 hours"
```

For independent service/window queries, emit two `Bash(...)` calls in one response — they run concurrently.

## Usage from main agent

```bash
agent-log "your natural language question"
agent-log --vlogs http://localhost:9428 "question"  # override VictoriaLogs URL
```

Call `Bash("agent-log 'question'")` directly from main agent — not blocked by gather guard.

For parallel queries (independent services/windows), emit two `Bash(...)` calls in one response.

## Known services in VictoriaLogs

sync-service, message-service, webhook-ingestion, rabbit-bridge-local, visitor-ingestion, report-worker-hatchet, event-processor, report-consumer, api-gateway, cs-event-consumer, cs-service, report-service, management-service

## LogsQL quick reference

```
service:api-gateway AND level:error
level:(error OR warn)
_msg:~"timeout.*"
service:sync-service AND _msg:~"gap"
```

Default time window: last 1 hour. Override with `start` param in the query description.

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
