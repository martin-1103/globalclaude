---
name: gass-edge-ssh
description: Use when checking the GASS edge VM (Asia, 34.101.227.172) over SSH, inspecting Caddy, the cf-forward controller, client-domain SSL/renewal, or "Edge Delivery" status. Trigger on cek edge, cek Caddy, edge.gass.co.id, renewal failed, edge delivery blocked, or gass-caddy-us-20260930-054947.
---

# GASS Edge SSH

## Scope

The edge in use is the **Asia VM**. Default actions are read-only. Do not cut over DNS/IP, restart services, install packages, edit config, or delete resources without explicit confirmation.

The US VM (`gass-caddy-us`, us-central1) is **excluded by the user (2026-10-09)**: do not audit, edit, or treat it as source/production.

## Target (production edge)

- Host: `34.101.227.172` (`edge.gass.co.id` A record; client domains CNAME to it)
- User: `sharegass`
- Instance: `gass-caddy-us-20260930-054947`
- Zone: `asia-southeast2-b`
- Key: `~/.ssh/id_ed25519`
- Secret env: `/etc/caddy/edge.env` (never print)

Re-check the IP in GCP if the VM was stopped or recreated; ephemeral IP can change.

## How the edge actually works

- **Client domains are NOT in the Caddyfile.** The cf-forward controller (`/opt/cf-forward/controller`, Node) pushes the whole config to Caddy's admin API (`127.0.0.1:2019/load`). The Caddyfile only holds 3 static hosts.
- Three systemd timers (every 15 min): `cf-forward-renewer` (ACME via `lego`), `cf-forward-installer` (manual certs + full reload), `cf-forward-delivery` (DNS+TLS probe → panel "Edge Delivery" status). Env: `/etc/cf-forward/renewal.env`.
- Client certs live in `/var/lib/cf-forward/certificates/`; per-domain backoff in `.renewal-backoff.json` there.
- `lego` must be **v4** (`/usr/local/bin/lego`, 4.35.2). The controller passes flags before `run` (v4 style); v5 breaks it.
- Panel "blocked / renewal failed" = renewer outcome. Read it from `journalctl -u cf-forward-renewer.service`, not from guesses.

## Never do

- **Never edit the Caddyfile to add a client domain** — the controller overwrites it, and it does not go through the cf-forward cert pipeline.
- Reload is safe only via the drop-in `caddy.service.d/resume-live-config.conf` (reload from `autosave.json`, start with `--resume`). Before any `systemctl reload/restart caddy`, confirm that drop-in still exists. Without it, reload loads the 3-host Caddyfile and every client domain goes TLS-dead (happened 2026-10-09, ~19 min).

## Connect

```bash
ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 \
  -i ~/.ssh/id_ed25519 sharegass@34.101.227.172
```

## Read-only checks

```bash
ssh -o ConnectTimeout=10 -i ~/.ssh/id_ed25519 sharegass@34.101.227.172 \
  'hostname; systemctl is-active caddy; systemctl cat caddy | grep -E "^Exec"; \
   command -v lego && lego --version; \
   curl -s localhost:2019/config/apps/tls/certificates/load_files | python3 -c "import json,sys;print(len(json.load(sys.stdin)),\"live certs\")"; \
   systemctl list-timers --no-pager | grep cf-forward; \
   sudo journalctl -u cf-forward-renewer.service -n 3 --no-pager'
```

Report exact output. Never print `/etc/caddy/edge.env` or any private key.

## Safety

- Treat SSH failure as a blocker; do not infer service state.
- Before any mutation, state the exact command, impact, and rollback, then ask for confirmation.
- After any change, verify every client domain over HTTPS (domain list: `runtime.installerRegistry.list()` or the live `load_files`), not just the one you touched.
