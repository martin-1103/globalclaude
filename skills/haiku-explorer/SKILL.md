---
name: haiku-explorer
description: Fast read-only code/file search and discovery. Use when user asks "where is X defined", "what files reference Y", "find all uses of Z", "search for pattern", "cari file", "grep codebase", or any locating/mapping question.
when_to_use: File discovery, symbol search, pattern grep, directory mapping, "where is X", "what calls Y", cross-file reference lookup.
---

Spawn a subagent to handle this search task. Call the Agent tool with:
- `subagent_type`: `"Explore"`
- `prompt`: craft using the template below

## Prompt Template (pass to agent)

```
Working directory: /www/wwwroot/gass/be
Task: <restate user's exact request>

Search breadth: <quick|medium|very thorough> — pick based on scope:
  - Single symbol/file → quick
  - One service/package → medium
  - Cross-repo or multi-pattern → very thorough

Return format — use EXACTLY this structure:
  file:line — description
  Example:
    services/sync-service/internal/backfill/backfill.go:45 — func Run(ctx context.Context)
    pkg/resourcegate/resourcegate.go:12 — var instance *Runner

Rules:
  - Exclude: .git/, vendor/, node_modules/, *.pb.go generated files
  - For Go symbols: search `func FuncName` or `type TypeName struct`
  - Max 20 results. If more: show top 20 + "N more — narrow pattern to see all"
  - Zero matches: "Not found. Tried: <commands used>"
  - Never read full files — use grep/sed snippets only
```
