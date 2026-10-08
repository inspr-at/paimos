# Pause and wind down agents

The Agents header groups Start agent and Attach a running session under New.
More agent actions includes Pause all, Resume all, Wind down and Agent keys.
Closing a dialog opened from either menu returns focus to its header trigger.

The Agents page offers Pause and Stop now per controllable session, plus quiet
Pause all and Resume all actions. Pause asks for a level and an optional handover
note; Pause all supports per-agent overrides and Keep running. A lead can include
its workers. Personal settings choose the default pause level. Paused generations
keep the saved handover, next steps, questions and WIP commit. Resume saves a
continuation request; a launcher must start the successor, including after a reboot.

Interrupt this step lives in the session menu. Stop now uses the shared pause
dialog and confirms a saved request, rather than a process exit. Dialog headings
keep one visible line for long session names; the full name remains available to
screen readers and on hover, while Close and the action bar stay in place.

Wind down starts only after its host-grouped preview is confirmed. The host picker
uses personal computer names, tri-state host selection, individual agents and All /
None quick picks; its State view groups running agents, idle hosts and offline
hosts, omitting empty sections. Preview and selection count only the caller-owned
sessions with control permission, matching the session's `owner_principal_id`
against the canonical owner returned by the leaving report for linked identities.
The request carries `deadline_at` plus
`hosts: "all" | [...]` and optional explicit `agents`. Host scope is persisted, but
new-start blocking remains launcher follow-up work: new agents can still start.
Missing or stale planning reports never imply a finish estimate. The plan reads
back durable backend outcomes; a deadline or queued stop alone cannot mark it done.
Cancellation withdraws pending requests, keeps existing handovers and reports stops
already in flight, with Undo while the cancelled deadline is still ahead.
Dismissing a completed report clears its deadline quietly, without withdrawal
feedback or Undo. Partial batch writes name accepted and failed requests.

Dialogs anchor their controls above variable content on desktop and pin their
footer on phone sheets. Submit uses Command+Enter on Apple platforms and Ctrl+Enter
elsewhere; Escape first leaves a field, then closes. API rows pass through the
canonical session ledger and controls stay bound to the captured generation/run.
