---
name: sonnet-editor
description: Delegate ALL code edits / file creation here (Opus is expensive; this offloads file-reading + reasoning to a cheap Sonnet context, keeping main context clean). Triggers — "edit", "buat/create file", "implement", "tambah fungsi", "ubah", "refactor", "tulis kode", "perbaiki", "fix", or any write to a code/config file. Self-edit ONLY a trivial 1-liner. Big task = split into 1-3 file slices, fan out parallel agents.
---

Call `Agent(subagent_type: "sonnet-editor")`. Don't read the target file yourself — the agent reads it in its own context (that's the saving).

Prompt = delta only (agent already enforces: bounded scope, match style, verify build, concise receipt). Don't paste file contents or restate those rules.

```
Task: <what to change>
File(s): <paths, or "locate: <desc>">
Scope: edit only <X>; <any signature/dep constraint>
Verify: <go build ./pkg | go test -run X>
```

**Many files:** one agent = 1-3 files max. Split the work into independent slices and spawn agents in parallel (one Agent call per slice, same message). If files are interdependent (shared signature/contract), decide the contract first, put it in each slice's prompt, then fan out.
