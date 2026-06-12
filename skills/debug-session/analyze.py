#!/usr/bin/env python3
"""
Scan Claude Code session transcripts and flag anomalies.

Detects:
  1. Subagent stuck / spawn fail  — Agent tool_use without tool_result, or
     subagent transcript missing/empty/no final stopReason.
  2. Skill trigger miss            — Skill tool_use that errored, hook errors on
     skill dispatch, or sessions where no skill fired at all (informational).
  3. Tool errors (general)         — any tool_result with is_error, hook failures.
  4. Subagent transcript dump      — extract a subagent's prompt + final output.

Layout assumed:
  <projDir>/<sessionId>.jsonl                              main transcript
  <projDir>/<sessionId>/subagents/agent-*.jsonl           subagent transcripts

Usage:
  analyze.py                       scan all sessions in current project dir, report anomalies
  analyze.py --project <slug>      scan a specific project slug under ~/.claude/projects
  analyze.py --session <id|file>   scan one session (deep)
  analyze.py --dump <agentFile>    dump one subagent transcript (prompt + result)
  analyze.py --all-projects        scan every project
  analyze.py --json                machine-readable output
"""
import argparse
import glob
import json
import os
import sys
from collections import Counter

HOME = os.path.expanduser("~")
PROJECTS = os.path.join(HOME, ".claude", "projects")


def cwd_to_slug(path):
    # Claude encodes cwd as the dir name: / and . -> -
    return path.replace("/", "-").replace(".", "-")


def default_project_dir():
    slug = cwd_to_slug(os.getcwd())
    d = os.path.join(PROJECTS, slug)
    if os.path.isdir(d):
        return d
    # fallback: newest project dir by mtime
    dirs = [p for p in glob.glob(os.path.join(PROJECTS, "*")) if os.path.isdir(p)]
    if not dirs:
        return None
    return max(dirs, key=os.path.getmtime)


def load_jsonl(path):
    out = []
    try:
        with open(path) as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except json.JSONDecodeError:
                    continue
    except OSError:
        pass
    return out


# Known-benign error snippets: hook redirects, retryable harness nudges.
# These are NOT real failures — they're the harness steering, immediately retried.
BENIGN = (
    "main gather blocked",          # ALLOW_MAIN_GATHER hook redirect
    "DILARANG",                     # opus-edit redirect to sonnet-editor
    "context-mode: WebFetch redirected",
    "File has not been read yet",   # Read-before-Edit nudge, retried
    "No such tool available",       # tool-name typo, retried
    "doesn't want to proceed",      # user rejection, intentional
)


def is_benign(snippet):
    s = snippet or ""
    return any(b in s for b in BENIGN)


def iter_content(rec):
    msg = rec.get("message")
    if not isinstance(msg, dict):
        return
    content = msg.get("content")
    if not isinstance(content, list):
        return
    for c in content:
        if isinstance(c, dict):
            yield c


def subagent_dir(session_file):
    base = session_file[:-6] if session_file.endswith(".jsonl") else session_file
    return os.path.join(base, "subagents")


def analyze_session(session_file):
    recs = load_jsonl(session_file)
    sid = os.path.basename(session_file).replace(".jsonl", "")

    tool_use = {}          # id -> name
    tool_use_input = {}    # id -> input (for Agent: subagent_type/description)
    tool_result_ids = set()
    error_results = []     # (name, snippet)
    hook_errors = []       # (toolName?, errorsnippet)
    skill_calls = []       # skill names fired
    skill_errors = []
    user_prompts = 0
    first_ts = last_ts = None

    for r in recs:
        ts = r.get("timestamp")
        if ts:
            first_ts = first_ts or ts
            last_ts = ts
        if r.get("type") == "user":
            # count real user prompts (not tool_result-only turns)
            if any(c.get("type") == "text" for c in iter_content(r)):
                user_prompts += 1
        # hook errors live on records
        he = r.get("hookErrors")
        if he:
            hook_errors.append((r.get("type"), json.dumps(he)[:200]))

        for c in iter_content(r):
            t = c.get("type")
            if t == "tool_use":
                tool_use[c["id"]] = c.get("name", "?")
                tool_use_input[c["id"]] = c.get("input", {})
                if c.get("name") == "Skill":
                    skill_calls.append(c.get("input", {}).get("skill", "?"))
            elif t == "tool_result":
                tid = c.get("tool_use_id")
                tool_result_ids.add(tid)
                if c.get("is_error"):
                    nm = tool_use.get(tid, "?")
                    body = c.get("content")
                    snip = json.dumps(body)[:200] if body else ""
                    error_results.append((nm, snip, is_benign(snip)))
                    if nm == "Skill":
                        skill_errors.append(snip)

        # toolUseResult shape (top-level) sometimes carries is_error
        tur = r.get("toolUseResult")
        if isinstance(tur, dict) and (tur.get("is_error") or tur.get("isError")):
            snip = json.dumps(tur)[:200]
            error_results.append((r.get("type", "?"), snip, is_benign(snip)))

    real_errors = [(n, s) for n, s, b in error_results if not b]
    benign_errors = [(n, s) for n, s, b in error_results if b]

    # unpaired tool_use = stuck / interrupted
    unpaired = [(i, n) for i, n in tool_use.items() if i not in tool_result_ids]
    unpaired_agents = [(i, n) for i, n in unpaired if n in ("Agent", "Task")]

    # subagent spawn check
    agent_uses = [(i, n) for i, n in tool_use.items() if n in ("Agent", "Task")]
    sdir = subagent_dir(session_file)
    agent_files = glob.glob(os.path.join(sdir, "agent-*.jsonl")) if os.path.isdir(sdir) else []
    spawn_mismatch = None
    if agent_uses and not agent_files:
        spawn_mismatch = f"{len(agent_uses)} Agent call(s) but NO subagent transcript dir/files (spawn fail?)"

    # stuck subagents: transcript present but no final assistant/stopReason
    stuck_subagents = []
    for af in agent_files:
        arecs = load_jsonl(af)
        if not arecs:
            stuck_subagents.append((os.path.basename(af), "empty transcript"))
            continue
        has_stop = any(
            r.get("type") == "assistant" and r.get("stopReason")
            for r in arecs
        )
        last_type = arecs[-1].get("type")
        if not has_stop and last_type != "assistant":
            stuck_subagents.append((os.path.basename(af), f"no stopReason, last={last_type}"))

    report = {
        "session": sid,
        "file": session_file,
        "user_prompts": user_prompts,
        "first_ts": first_ts,
        "last_ts": last_ts,
        "tool_use_total": len(tool_use),
        "tool_counts": dict(Counter(tool_use.values())),
        "agent_calls": len(agent_uses),
        "agent_files": len(agent_files),
        "skill_calls": skill_calls,
        # anomalies:
        "unpaired_agents": [{"id": i, "name": n} for i, n in unpaired_agents],
        "unpaired_other": [{"id": i, "name": n} for i, n in unpaired if n not in ("Agent", "Task")],
        "spawn_mismatch": spawn_mismatch,
        "stuck_subagents": [{"file": f, "why": w} for f, w in stuck_subagents],
        "tool_errors": [{"tool": n, "snippet": s} for n, s in real_errors],
        "benign_error_count": len(benign_errors),
        "skill_errors": skill_errors,
        "hook_errors": [{"on": o, "err": e} for o, e in hook_errors],
    }
    flags = []
    if unpaired_agents:
        flags.append(f"{len(unpaired_agents)} STUCK agent(s) (no result)")
    if spawn_mismatch:
        flags.append("SPAWN-FAIL")
    if stuck_subagents:
        flags.append(f"{len(stuck_subagents)} stuck subagent transcript(s)")
    if real_errors:
        flags.append(f"{len(real_errors)} tool error(s)")
    if skill_errors:
        flags.append(f"{len(skill_errors)} skill error(s)")
    if hook_errors:
        flags.append(f"{len(hook_errors)} hook error(s)")
    report["flags"] = flags
    report["has_anomaly"] = bool(flags)
    return report


def dump_subagent(agent_file):
    recs = load_jsonl(agent_file)
    print(f"=== {agent_file} ({len(recs)} records) ===")
    if not recs:
        print("(empty)")
        return
    # first user text = the dispatch prompt
    for r in recs:
        if r.get("type") == "user":
            for c in iter_content(r):
                if c.get("type") == "text":
                    print("\n--- DISPATCH PROMPT ---")
                    print(c["text"][:2000])
                    break
            break
    # last assistant text = final output
    final = None
    stop = None
    for r in recs:
        if r.get("type") == "assistant":
            if r.get("stopReason"):
                stop = r.get("stopReason")
            for c in iter_content(r):
                if c.get("type") == "text" and c.get("text", "").strip():
                    final = c["text"]
    print(f"\n--- FINAL OUTPUT (stopReason={stop}) ---")
    print((final or "(no final assistant text — likely stuck/interrupted)")[:3000])


def short(ts):
    return (ts or "")[:19].replace("T", " ")


def print_report(rep, verbose=False):
    tag = "⚠️  ANOMALY" if rep["has_anomaly"] else "✅ clean"
    print(f"\n{tag}  {rep['session']}  ({short(rep['first_ts'])} → {short(rep['last_ts'])})")
    print(f"   prompts={rep['user_prompts']} tools={rep['tool_use_total']} "
          f"agents={rep['agent_calls']}/{rep['agent_files']}files skills={rep['skill_calls']} "
          f"(benign-errs={rep['benign_error_count']})")
    for f in rep["flags"]:
        print(f"   🔴 {f}")
    if verbose or rep["has_anomaly"]:
        for a in rep["unpaired_agents"]:
            print(f"      stuck-agent: {a['name']} {a['id']}")
        if rep["spawn_mismatch"]:
            print(f"      spawn: {rep['spawn_mismatch']}")
        for s in rep["stuck_subagents"]:
            print(f"      stuck-subagent: {s['file']} — {s['why']}")
        for e in rep["tool_errors"][:8]:
            print(f"      tool-err [{e['tool']}]: {e['snippet']}")
        for h in rep["hook_errors"][:5]:
            print(f"      hook-err [{h['on']}]: {h['err']}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--project")
    ap.add_argument("--session")
    ap.add_argument("--dump")
    ap.add_argument("--all-projects", action="store_true")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args()

    if args.dump:
        dump_subagent(args.dump)
        return

    # build session file list
    files = []
    if args.session:
        if os.path.isfile(args.session):
            files = [args.session]
        else:
            # treat as sessionId; search projects
            hits = glob.glob(os.path.join(PROJECTS, "*", args.session + ".jsonl"))
            files = hits
            if not files:
                print(f"session not found: {args.session}", file=sys.stderr)
                sys.exit(1)
    elif args.all_projects:
        files = glob.glob(os.path.join(PROJECTS, "*", "*.jsonl"))
    else:
        pdir = (os.path.join(PROJECTS, args.project) if args.project
                else default_project_dir())
        if not pdir or not os.path.isdir(pdir):
            print("no project dir found", file=sys.stderr)
            sys.exit(1)
        files = glob.glob(os.path.join(pdir, "*.jsonl"))

    files = sorted(files, key=os.path.getmtime, reverse=True)
    reports = [analyze_session(f) for f in files]

    if args.json:
        print(json.dumps(reports, indent=2))
        return

    anom = [r for r in reports if r["has_anomaly"]]
    print(f"scanned {len(reports)} session(s) — {len(anom)} with anomalies")
    for r in reports:
        print_report(r, verbose=args.verbose)

    if anom:
        print("\n── next: deep-dive a stuck subagent ──")
        for r in anom:
            if r["stuck_subagents"] or r["unpaired_agents"]:
                sdir = subagent_dir(r["file"])
                print(f"  analyze.py --dump {sdir}/agent-<id>.jsonl")
                break


if __name__ == "__main__":
    main()
