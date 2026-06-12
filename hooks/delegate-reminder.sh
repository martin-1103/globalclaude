#!/usr/bin/env bash
# PostToolUse hook (matcher: Edit|Write).
# Fires only when the MAIN thread edits/creates a file directly — subagent
# tool calls run in their own context and do not trigger this. So this nudges
# exactly when a delegation decision may have been skipped.
#
# Emits a short reminder back to the model as additional context.

read -r _stdin   # consume hook payload on stdin (unused)

cat <<'JSON'
{
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "additionalContext": "💸 Opus edit. → sonnet-editor (fan out parallel for many files); self only if 1-liner."
  }
}
JSON
