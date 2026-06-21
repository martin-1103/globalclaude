#!/usr/bin/env bash
# main-agent-gather-guard.sh — PreToolUse(Read|Grep|Glob|Bash)
#
# Goal: keep the main thread CLEAN. Raw data gathering (file contents,
# search hits, listings, logs, query results, graph dumps) accumulates in the
# main context — resent every turn — even when each call is small. 5 lines x
# 50 calls = 250 lines polluting forever. Main agent is dispatcher-only:
# communicate with the user, then delegate to the right top-level lane.
#
# This hook only detects that a gather came from main. It does NOT infer intent.
# The deny message therefore points main back to top-level lanes rather than
# directly to worker agents.
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

deny() {
  jq -nc '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:"⛔ Dispatcher-only root: raw gather from main thread blocked. Main may only use bounded Read calls (use offset/limit). Full-file Read and other gather should go through the appropriate top-level lane instead (investigate, fix-plan, impl-plan, brainstorm-orchestrator, reviewer/code-reviewer, codebrain-researcher, plan-review-orchestrator, debug-subagent)."}}'
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
    OFFSET=$(printf '%s' "$INPUT" | jq -r '.tool_input.offset // empty' 2>/dev/null || echo "")
    LIMIT=$(printf '%s' "$INPUT" | jq -r '.tool_input.limit // empty' 2>/dev/null || echo "")
    # main edits .md/.claude/.planning/project-docs (per edit-guard exemptions) → must Read first
    if [[ "$FILE" == "$CLAUDE_HOME"* || "$FILE" == *.md || "$FILE" == */.claude/* || "$FILE" == */.planning/* || "$FILE" == */project-docs/* ]]; then
      exit 0
    fi
    # bounded main-thread read is allowed; full-file read is not
    if [[ -n "$OFFSET" || -n "$LIMIT" ]]; then
      exit 0
    fi
    SUB="agent-explorer"
    ;;
  Grep|Glob)
    SUB="agent-explorer"
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
          SUB="agent-bash"; break ;;
        journalctl|kubectl)
          SUB="agent-logs"; break ;;
        docker)
          case "$second" in logs|ps|compose|stats|top) SUB="agent-logs"; break ;; esac ;;
        psql|mysql|redis-cli|mongo|mongosh|sqlite3|clickhouse-client)
          SUB="agent-db"; break ;;
        clickhouse)
          [[ "$second" == "client" ]] && { SUB="agent-db"; break; } ;;
        go)
          case "$second" in build|test|vet) SUB="agent-bash"; break ;; esac ;;
        make)
          SUB="agent-bash"; break ;;
        npm|pnpm|yarn)
          case "$second" in test|run|build|audit|outdated|ls|list) SUB="agent-bash"; break ;; esac ;;
        gh)
          SUB="agent-bash"; break ;;
      esac
    done <<< "$SEGS"
    ;;
  *)
    exit 0 ;;  # unmatched tool → allow
esac

# Allow path → no poll, instant
[[ -n "$SUB" ]] || exit 0

# ── Deny-candidate: is a subagent the real caller? ───────────────────────────
# Verified live 2026-06-12 (payload probe): subagent payloads carry `agent_id`;
# main-thread payloads do not. Deterministic — replaces the old transcript-poll
# heuristic (200ms poll + 3s mtime gate) that mis-classified working subagents
# as main and triggered gather-agent spawn cascades.
AGENT_ID=$(printf '%s' "$INPUT" | jq -r '.agent_id // ""' 2>/dev/null || echo "")
[[ -n "$AGENT_ID" ]] && exit 0   # subagent owns this gather → allow

deny
