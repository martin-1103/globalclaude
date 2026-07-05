---
name: pi-intercom
description: Send messages to or list active pi agent sessions on this machine via pi-intercom broker. Use when user wants to contact a pi session, coordinate with a running pi agent, or check what pi sessions are active.
when_to_use: "contact pi agent", "kirim pesan ke pi", "list pi sessions", "coordinate dengan pi", "tanya pi session", cross-session coordination
---

# pi-intercom — Claude Code ↔ pi agent bridge

Bridge to the pi-intercom broker via `~/.pi/agent/intercom/cc-bridge.js`.

## Tool

Bash: `node /root/.pi/agent/intercom/cc-bridge.js <action> [args]`

## Actions

### List active sessions
```bash
node /root/.pi/agent/intercom/cc-bridge.js list
```
Returns JSON array. Important fields: `id`, `name`, `cwd`, `model`, `status`.

### Send message (fire-and-forget)
```bash
node /root/.pi/agent/intercom/cc-bridge.js send <id|name> "your message"
```
`to` can use `id` (UUID) or `name`. Returns `{"type":"delivered","messageId":"..."}` on success.

### Send message + expect reply
```bash
node /root/.pi/agent/intercom/cc-bridge.js send-ask <id|name> "your question"
```
Same as `send` but sets `expectsReply: true` — the pi agent knows you're waiting for a reply.

## Flow

1. `list` → pick the target session (`id` or `name`)
2. `send` / `send-ask` → check the result delivered/failed
3. If `delivery_failed`: the session may have disconnected, check `list` again

## Error handling

| Error | Meaning |
|-------|---------|
| `broker not reachable` | Broker isn't running yet; open a pi session first |
| `delivery_failed` | Target session disconnected from the broker |
| `send timeout` | Broker is running but not responding — try again |

## Example usage

**User asks**: "ask the pi session in gass/be about function X"
```bash
# 1. list sessions
node /root/.pi/agent/intercom/cc-bridge.js list
# output: [{id: "daa62b97-...", name: "subagent-chat-...", cwd: "/www/wwwroot/gass/be", ...}]

# 2. send
node /root/.pi/agent/intercom/cc-bridge.js send-ask daa62b97-c250-46c0-a77d-bcde4ddd59dd "Di fungsi handleVisit() di visit_processor.go, gimana flow-nya kalau lead sudah exist?"
# output: {"type":"delivered","messageId":"..."}
```

## Daemon (persistent listener)

The daemon stays connected all the time — pi can reply anytime, landing in the inbox.

```bash
node /root/.pi/agent/intercom/cc-daemon.js start        # start daemon background
node /root/.pi/agent/intercom/cc-daemon.js status       # cek running/pid
node /root/.pi/agent/intercom/cc-daemon.js stop         # stop daemon
node /root/.pi/agent/intercom/cc-daemon.js inbox [n]    # read the last n messages (default 10)
node /root/.pi/agent/intercom/cc-daemon.js inbox-clear  # clear the inbox
```

Inbox file: `~/.pi/agent/intercom/inbox.jsonl`
Log file: `~/.pi/agent/intercom/cc-daemon.log`
The daemon is registered under the name `claude-code` on the broker — pi can target it via `/intercom` → select `claude-code`.

## Flow with the daemon

1. `daemon start` → daemon up
2. `send-ask <id> <msg>` → send to pi (bridge one-shot)
3. Pi replies via `/intercom` → `claude-code` (to the daemon)
4. `daemon inbox` → read the reply

## Notes

- cc-bridge.js = one-shot (connect → action → disconnect)
- cc-daemon.js = persistent, auto-reconnects if the broker restarts
- Broker auto-starts when pi runs; if all pi sessions close, the broker dies
