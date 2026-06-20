---
name: fastcontext
description: Cheap multi-hop repo explorer (own model). Use when path/symbol unknown and requires search→trace→read across files. Skip if file known, single grep, or project indexed in graph.
allowed-tools: Bash(fastcontext *)
---

# fastcontext

Autonomous repo explorer that runs as a separate subprocess with its own model. Keeps raw file bytes out of main context — returns only citations (`file:line`) and summary.

## When to use

- Path/symbol unknown, requires exploration across multiple files
- "How does X work end-to-end?" — needs search + trace + read in one shot
- Cross-cutting questions: "what calls Y?", "where is Z defined?", "map this flow"
- Cheapest option for open-ended exploration (uses cheap model, not Claude)

## When NOT to use

- File/path already known → use Read directly
- Single grep/symbol lookup → use Bash(rg) or haiku-codebase-memory
- Graph queries (callers, impact, architecture) when project is indexed → use haiku-codebase-memory (faster, no subprocess)
- Needs judgment/reasoning over findings → use sonnet-explorer instead

## Priority order (cheapest → most capable)

```
grep/rg (1 known pattern)
  → fastcontext (unknown path, multi-hop exploration, cheap model)
    → haiku-codebase-memory (graph: callers/callees/impact, indexed project)
      → sonnet-explorer (needs reasoning, fastcontext insufficient)
```

## Usage

```bash
# Default (12 turns)
fastcontext -q "<detailed question>" --citation

# Deep traces or architecture questions
fastcontext -q "<complex question>" --max-turns 16 --citation

# Quick lookup
fastcontext -q "<simple question>" --max-turns 4 --citation
```
