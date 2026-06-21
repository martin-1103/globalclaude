---
name: browser-test
description: Test, audit, or scrape a live web app in a real browser via subagent. Use when the user says "test the site", "audit the UI", "audit all panels", "login and check", "click through the app", "scrape this page", "e2e test in browser", "cek tampilan", "audit semua panel", "test di browser", or hands a URL to drive interactively. Routes the verbose DOM/screenshot/network work to a subagent so it stays out of main context.
when_to_use: Any task that drives a real browser end-to-end — login, clicking panels, filling forms, capturing network/DOM, verifying UI behavior — where the raw output would otherwise flood main context.
---

# browser-test — drive the browser via subagent

Browser work is verbose (DOM dumps, screenshots, 10k-char JSON payloads). It nempel di
main context tiap turn = pajak token. Offload the driving + raw gather to a subagent;
keep only the findings.

## When to use which subagent

- **Multi-step scenario that needs judgment** (audit a site, click through panels, decide
  "bug or fixture?", "which field is the chat input?") → spawn **`browser-tester`** (model:
  sonnet). Browser testing reads screenshots/DOM and judges UI correctness — that is
  reasoning, NOT fetch. Do NOT route this to a haiku agent; haiku is fetch-only and will
  produce a cacat verdict.
- **One fixed browser call** (run a known JS snippet, dump one page's JSON, single
  `browser-harness` heredoc with no branching) → `haiku-bash` is enough.

The Chrome daemon is shared system-wide, so the subagent reaches the SAME logged-in
session. (This also sidesteps the dispatcher-only Bash block on the main thread — the
subagent is the correct lane to run `browser-harness`.)

## How to invoke

Call the Agent tool with `subagent_type: "browser-tester"`. In the prompt, give:
1. **Target URL** + any **credentials** (the agent will not invent them; without them it returns `BLOCKED`).
2. **The exact scenario / panels** to exercise, step by step.
3. **What counts as a finding** — e.g. "report any non-2xx API call, any console error, any malformed data shown in the UI, distinguish data bugs from UI bugs."
4. **Guardrails** — name any destructive action it must NOT trigger (send message, delete, payment).

The agent reads `skills/browser/SKILL.md` (the browser-harness reference) for harness
mechanics. You do not need to re-explain the CLI.

## Parallel / multiple sites

Each `browser-harness` daemon session is shared state — multiple subagents on the SAME
Chrome will collide (one tab, one navigation at a time). For true parallel runs, the
harness supports remote isolated browsers (`start_remote_daemon` with a distinct
`BU_NAME`, needs `BROWSER_USE_API_KEY`). Only go there if the user asks for parallel
multi-site testing; default is one subagent, sequential.

## Reporting back

The subagent returns a compact PASS/FAIL/BLOCKED report with evidence (endpoint+status,
DOM delta, console errors, bad data values). Relay that to the user. If it returns
`BLOCKED` (e.g. login wall, no credentials), surface that and ask the user — do not retry
blind.
