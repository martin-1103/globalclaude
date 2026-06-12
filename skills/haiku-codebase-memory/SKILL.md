---
name: haiku-codebase-memory
description: Structural code exploration via knowledge graph — architecture overview, symbol search, caller/callee chains, route graph, impact analysis. Use when user asks "who calls X", "what does Y depend on", "show architecture", "trace call chain", "impact of changing Z", "find all callers of".
when_to_use: Call graph traversal, symbol search by name/label, architecture overview, dependency mapping, impact analysis, dead code detection, cross-service route tracing.
---

Spawn a subagent to handle this code graph task. Call the Agent tool with:
- `subagent_type`: `"haiku-codebase-memory"`
- `prompt`: craft using the template below

## Prompt Template (pass to agent)

```
Project: gass-be
Root: /www/wwwroot/gass/be
Task: <restate user's exact request>

Use these tools in this priority order:
1. search_graph(query="<symbol name>", project="gass-be") — find exact qualified_name first
2. trace_path(function_name="<qname>", project="gass-be", direction=<inbound|outbound|both>, depth=3)
3. get_code_snippet(qualified_name="<qname>", project="gass-be") — only after step 1 confirms name
4. get_architecture(project="gass-be") — only for broad architecture questions

Task type → tool mapping:
  "who calls X" → trace_path direction=inbound
  "what does X call" → trace_path direction=outbound
  "full chain" → trace_path direction=both
  "impact of changing X" → trace_path direction=inbound depth=4
  "cross-service" → trace_path mode=cross_service
  "architecture overview" → get_architecture

Return format — EXACTLY:
  Symbol: <file>:<line> — <signature>

  Callers (depth N):
    depth 1: <func> (<file>:<line>)
    depth 2: <func> (<file>:<line>)

  OR for architecture:
    services/
      <name> — <one-line purpose>

Rules:
  - Always search_graph first — never guess qualified_name
  - If project not indexed: run index_status first
  - Max depth: 3 (go to 4 only for impact analysis)
  - No explanations — only facts + locations
```
