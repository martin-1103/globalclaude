---
name: db-query
description: Query MySQL/ClickHouse/Postgres/Redis directly via ctx_execute, no LLM sub-agent hop. Use when user asks a DB question ("berapa order gagal hari ini", "cek data di tabel X", "query database"). Replaces agent-db CLI — same credential store, main agent composes SQL itself.
---

# DB query — direct, main-agent-composed

Replaces `agent-db` CLI. That CLI's NL→SQL translation was an extra LLM hop that
sometimes mistranslated. This skill has the main agent read the credential store
itself and write the SQL directly via `ctx_execute` (shell, `docker exec`).

## Credential store (self-updating)

Per-project config lives at `/var/pile/agent-db/projects/<slug>/config.json`
(this store is REUSED from the old agent-db CLI — do not duplicate it).

Slug rule: project path, `/`→`__`, `.`→`_`, leading `__` stripped.
e.g. `/www/wwwroot/gass/be` → `www__wwwroot__gass__be`.

Schema:
```json
{
  "containers": {"clickhouse": "clickhouse", "mysql": "gassv4-mysql", "redis": "redis", "postgres": "hatchet-postgres"},
  "credentials": {
    "clickhouse_user": "default", "clickhouse_password": "clickhouse123", "clickhouse_db": "default",
    "mysql_user": "gassv4", "mysql_password": "gassv4pass", "mysql_db": "gv3",
    "redis_db": "0",
    "postgres_user": "hatchet", "postgres_password": "hatchet-smoke-pass", "postgres_db": "hatchet"
  },
  "notes": "free-text schema hints"
}
```

Schema discovery notes accumulate in `/var/pile/agent-db/projects/<slug>/context.md`
under `## Discovered Tables` — same file the old CLI wrote to. Read it before
guessing column names.

## Workflow

1. Compute the slug for the current project path.
2. `ctx_execute(language: "shell", code: "cat /var/pile/agent-db/projects/<slug>/config.json")`
   — read containers + credentials. No config yet → run discovery (step 5).
3. `ctx_execute(language: "shell", code: "cat /var/pile/agent-db/projects/<slug>/context.md")`
   — check for already-known table/column names before guessing.
4. Compose the query yourself, run via `ctx_execute`:
   - MySQL: `docker exec <mysql_container> mysql -u <user> -p<password> <db> -e "<SQL>"`
   - ClickHouse: `docker exec <ch_container> clickhouse-client --user <user> --password <password> -d <db> -q "<SQL>"`
   - Postgres: `docker exec -e PGPASSWORD=<password> <pg_container> psql -U <user> -d <db> -c "<SQL>" --no-align --field-separator=$'\t'`
   - Redis: `docker exec <redis_container> redis-cli -n <redis_db> <command>`
5. **No config / credentials wrong / container renamed** → self-update, don't just fail:
   - `ctx_execute(language: "shell", code: "docker ps --format '{{.Names}}'")` to find the live container name.
   - `ctx_execute(language: "shell", code: "docker inspect <container> --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -iE 'user|password|db'")` to pull creds from env.
   - Write the corrected `containers`/`credentials` back to the project's `config.json` (merge — don't clobber `notes` or other keys) so the next query doesn't repeat discovery.
6. New table/column learned mid-session → append to `context.md` under `## Discovered Tables` (same format as existing entries: `` `db.table` — <columns: ...> (discovered YYYY-MM-DD) ``) so it persists across sessions.
7. Large result set → run through `ctx_execute` output directly (already sandboxed), don't Bash it raw.

## Safety

Read-only by default. A destructive statement (`UPDATE`/`DELETE`/`DROP`/`ALTER`/`TRUNCATE`)
requires the user's explicit go-ahead in this conversation — don't run it because a query
"seems safe to fix data". Same rule as the old CLI's `--confirm-destructive` gate, just
enforced by main-agent judgment instead of a flag.

## Known projects

- `/www/wwwroot/gass/be` → slug `www__wwwroot__gass__be`. ClickHouse dbs: `source_mirror`,
  `meta`, `visitor`, `report`, `meta_ads`, `cs`, `cs_events`. MySQL `gv3` = main app DB,
  `gv3_auth` = auth DB. Postgres `hatchet` = workflow engine DB.
