# Subagents on Agents (AEON-1141)

An Agent-tool child in a registered Claude Code session appears under its parent
on `/agents`. Install or refresh the standard hooks with
`aeon hook install --harness claude`. The parent must keep its existing
`aeon harness run-heartbeat --source-session <Claude session UUID>` helper running.
No index entry, a stopped generation, or a dead parent owner produces a quiet
no-op; an inbox environment binding alone does not enable child registration.

`SubagentStart` registers a worker generation with harness `claude`, the parent's
project and ticket, and work shape `scout` (`ship` when the parent is bound as
`ship`). The label uses `agent_type`, plus `description` when supplied and short
enough; `model` is recorded only when supplied. The official hook schema does not
promise either optional field. The child advertises status only and cannot
consume the parent's inbox or control a process. `SubagentStop` records a stop
even when registration is still in flight. Stopped children use the ordinary
Agents stale cleanup.

CLI-written child state lives in owned `0700` directories under the parent's
state directory, with `0600` regular files. Agent IDs select hashed names rather
than filesystem paths. The hook finishes within its existing 2.5-second budget
and always returns success on runtime failure, with content-free diagnostics.
The parent helper maintains children on its heartbeat cadence and retries
persisted lifecycle requests; failed stops remain pending. Parent shutdown also
attempts to stop its children within a bounded budget. Large accumulated child
directories are scanned in bounded batches; interrupted or failed cleanup can
fall back to ordinary stale cleanup.

For Codex and other external harness launches, register the process explicitly:

```sh
aeon harness run --parent-session "$AEON_SESSION_ID" --role worker \
  --ticket AEON-1141 --work-shape scout --harness codex -- codex exec "Inspect the task"
```

Use the actual parent generation and assigned ticket; retain the existing
account's project and worker permissions. Claude lifecycle hook registration
does not extend paired-hook consent or grant any new permissions.

Payloads were verified on 2026-10-10 against the official
[SubagentStart](https://code.claude.com/docs/en/hooks#subagentstart) and
[SubagentStop](https://code.claude.com/docs/en/hooks#subagentstop) references:
both carry the common `session_id` and event name plus `agent_id` and
`agent_type`; stop additionally carries `stop_hook_active`,
`agent_transcript_path`, and `last_assistant_message`. Transcript paths and
response text are not read by this feature.
