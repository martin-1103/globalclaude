---
name: codebrain
description: "Use when user needs documentation research, library best practices, or technical answers from crawled docs and web search. Triggers on '/codebrain', 'ask codebrain', or when needing fresh documentation context for a library or framework."
---

# Codebrain

Documentation research via doc-cache API + web search, synthesized into actionable answers. Delegated entirely to the `gemini-codebrain` subagent (Gemini Flash synthesis) — main agent never touches the raw wrapper output.

## Usage

```
/codebrain <question>
```

## Instructions

Spawn the `gemini-codebrain` subagent with the user's question. The wrapper ALWAYS saves research to a per-task artifact file and prints its path. The subagent relays that artifact path to pi-codebrain (Gemini Flash) for synthesis — it never pipes inline content. Returns a compact synthesized answer plus the artifact path. The main agent never sees raw wrapper output.

Pass in the spawn prompt:
- `query`: the user's research question (frame it specifically — domain + use case + output constraint)
- Optional `focus`: aspect to prioritize (e.g., "pitfalls", "API surface", "perf trade-offs", "version differences")
- Optional `max_bullets`: override default 3 bullets (1-7 range)

If the subagent returns `ERROR:` or `NEEDS NARROWER QUERY:`, surface the message to user and stop. Do not retry without query reformulation.

## Query craft (apply BEFORE spawning)

Good queries are specific. Bad queries are vague.

| Bad | Good |
|-----|------|
| `Go concurrency` | `Go HTTP client connection pooling — idle timeout, max conns per host, leak detection. For production server 10k req/s.` |
| `React hooks` | `React useEffect cleanup on dependency change — memory leak pitfalls in WebSocket subscriptions. Latest year only.` |
| `Postgres indexing` | `Postgres B-tree vs GIN index trade-offs for jsonb columns. Use case: filter on nested keys, 100M rows.` |

Pattern: `<topic> — <specific aspect> + <constraint or context>. <output shape>.`

If user's incoming question is vague, reformulate before spawning. Surface the reformulation to user for transparency.

## Artifact location

Codebrain research is ALWAYS saved by the wrapper to a per-task artifact file:
```
.planning/tasks/{active-task-id}/research/codebrain-{slug}-{HHMMSS}.md
```
The subagent relays this artifact path to pi-codebrain for synthesis; it never pipes inline content.

When no active task is set, wrapper falls back to a timestamp-only folder. The subagent returns the artifact path in its synthesis; the main agent can Read selectively with `offset`/`limit` if a section beyond the 3-bullet synthesis is needed.

## Never

- Run `codebrain-artifact.sh` directly from main agent — always go through the subagent
- Dump raw artifact content into main context
- Loop the subagent on the same query — reformulate query or accept the result
