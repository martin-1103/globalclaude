#!/usr/bin/env bash
# main-agent-gather-guard.sh — PreToolUse(Read|Grep|Glob|Bash|mcp__codebase-memory*)
#
# Goal: keep the main Opus thread CLEAN. Raw data gathering (file contents,
# search hits, listings, logs, query results, graph dumps) accumulates in the
# main context — resent every turn — even when each call is small. 5 lines x
# 50 calls = 250 lines polluting forever. Route ALL gathering to cheap Haiku
# subagents (isolated context, discarded after). Main = reason/plan/delegate.
#
# Routing (deny from main → spawn the named subagent):
#   Read / Grep / Glob               → haiku-explorer
#   mcp__codebase-memory*            → haiku-codebase-memory
#   Bash output-producing cmd        → haiku-bash / haiku-logs / haiku-db
#
# Detection (proven in main-agent-edit-guard.sh, live 2026-06-10):
#   CC flushes tool_use_id into <transcript>/subagents/agent-<id>.jsonl ~100ms
#   after PreToolUse. Poll ~200ms: found → subagent owns it → ALLOW; else main.
#   No subagents dir / stale (>=3s) → main.
#
# Latency: classify FIRST. Allow-path exits immediately (no poll). Only a
# deny-candidate pays the ~200ms subagent poll. Common `git`/`mkdir` = instant.
#
# Override: ALLOW_MAIN_GATHER=1 bypasses entirely.
# Fail-open: parse error / unknown command → ALLOW.

set -euo pipefail

INPUT=$(cat)

CLAUDE_HOME="${CLAUDE_CONFIG_DIR:-/root/.claude}"

deny() {  # $1 = subagent name
  jq -nc --arg s "$1" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:("⛔ Raw gather from main thread blocked. Spawn Agent(subagent_type=\"" + $s + "\") to fetch it — its context is isolated and discarded, keeping main clean.")}}'
  exit 0
}

# Global override
[[ "${ALLOW_MAIN_GATHER:-}" == "1" ]] && exit 0

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
[[ -n "$TOOL" ]] || exit 0

# ── Classify: set SUB (target subagent). Empty SUB = allow. ──────────────────
SUB=""

case "$TOOL" in
  Read)
    FILE=$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // ""' 2>/dev/null || echo "")
    # main edits .md/.claude/.planning/project-docs (per edit-guard exemptions) → must Read first
    if [[ "$FILE" == "$CLAUDE_HOME"* || "$FILE" == *.md || "$FILE" == */.claude/* || "$FILE" == */.planning/* || "$FILE" == */project-docs/* ]]; then
      exit 0
    fi
    SUB="haiku-explorer"
    ;;
  Grep|Glob)
    SUB="haiku-explorer"
    ;;
  mcp__codebase-memory*)
    SUB="haiku-codebase-memory"
    ;;
  Bash)
    CMD=$(printf '%s' "$INPUT" | jq -r '.command // .tool_input.command // ""' 2>/dev/null || echo "")
    [[ -n "$CMD" ]] || exit 0
    # strip quoted literals first — deny-words inside strings or jq/awk programs
    # (e.g. jq '.. | strings', git commit -m "cat fix") must NOT become heads.
    STRIPPED=$(printf '%s' "$CMD" | sed -E "s/'[^']*'//g; s/\"[^\"]*\"//g")
    # split on | && || ; — inspect each segment head (basename, after env assigns)
    SEGS=$(printf '%s' "$STRIPPED" | sed -E 's/(\|\||&&|\||;)/\n/g')
    while IFS= read -r seg; do
      seg="${seg#"${seg%%[![:space:]]*}"}"                       # ltrim
      while [[ "$seg" =~ ^[A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+ ]]; do
        seg="${seg#*[[:space:]]}"; seg="${seg#"${seg%%[![:space:]]*}"}"
      done
      head="${seg%%[[:space:]]*}"; head="${head##*/}"            # basename
      second=$(printf '%s' "$seg" | awk '{print $2}')
      case "$head" in
        cat|head|tail|less|more|bat|ls|find|fd|tree|grep|rg|ag|ack|egrep|fgrep|awk|cut|sort|uniq|wc|column|nl|xxd|hexdump|strings)
          SUB="haiku-bash"; break ;;
        journalctl|kubectl)
          SUB="haiku-logs"; break ;;
        docker)
          case "$second" in logs|ps|compose|stats|top) SUB="haiku-logs"; break ;; esac ;;
        psql|mysql|redis-cli|mongo|mongosh|sqlite3|clickhouse-client)
          SUB="haiku-db"; break ;;
        clickhouse)
          [[ "$second" == "client" ]] && { SUB="haiku-db"; break; } ;;
        go)
          case "$second" in build|test|vet) SUB="haiku-bash"; break ;; esac ;;
        make)
          SUB="haiku-bash"; break ;;
        npm|pnpm|yarn)
          case "$second" in test|run|build|audit|outdated|ls|list) SUB="haiku-bash"; break ;; esac ;;
        gh)
          SUB="haiku-bash"; break ;;
      esac
    done <<< "$SEGS"
    ;;
  *)
    exit 0 ;;  # unmatched tool → allow
esac

# Allow path → no poll, instant
[[ -n "$SUB" ]] || exit 0

# ── Deny-candidate: is a subagent the real caller? ───────────────────────────
TP=$(printf '%s' "$INPUT" | jq -r '.transcript_path // ""' 2>/dev/null || echo "")
TID=$(printf '%s' "$INPUT" | jq -r '.tool_use_id // ""' 2>/dev/null || echo "")
# fail-open: can't detect → allow (never wedge main flow on parse gaps)
[[ -n "$TP" && -n "$TID" ]] || exit 0

SUBDIR="${TP%.jsonl}/subagents"
[[ -d "$SUBDIR" ]] || deny "$SUB"

NEWEST=$(ls -t "$SUBDIR"/*.jsonl 2>/dev/null | head -1 || true)
[[ -n "$NEWEST" ]] || deny "$SUB"

NOW=$(date +%s 2>/dev/null || echo 0)
MTIME=$(stat -c %Y "$NEWEST" 2>/dev/null || echo 0)
(( NOW - MTIME < 3 )) || deny "$SUB"

MF=""
for _ in $(seq 1 8); do
  MF=$(grep -lF -- "$TID" "$SUBDIR"/*.jsonl 2>/dev/null | head -1 || true)
  [[ -n "$MF" ]] && break
  sleep 0.025
done

[[ -n "$MF" ]] && exit 0   # subagent owns this gather → allow
deny "$SUB"
