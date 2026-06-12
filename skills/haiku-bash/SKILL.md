---
name: haiku-bash
description: Run shell commands with large/verbose output. Use for builds, tests, log tails, docker logs, find/grep producing >50 lines, curl with long responses, docker ps, service status checks.
when_to_use: Any command whose raw output would pollute main context — builds, test runs, docker commands, service health checks, multi-line CLI output.
---

Spawn a subagent to run the command. Call the Agent tool with:
- `subagent_type`: `"haiku-bash"`
- `prompt`: craft using the template below

## Prompt Template (pass to agent)

```
Run this command:
<exact command(s) to run>

Return ONLY:
1. Exit status: OK or FAILED (exit N)
2. If FAILED: error message verbatim
3. Key findings: <what specifically to extract — errors, metrics, final status line>
4. If output >100 lines: "N lines total. Key findings:" then summarize

Suppress: progress bars, verbose INFO logs, repetitive lines, routine health checks.
Show verbatim: any ERROR/WARN/FATAL/panic line, final result line, stack traces.
```
