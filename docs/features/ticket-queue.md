# Ticket queue

In progress tickets and tasks with no assignee, queued/live agent run, or live
bound session show a small **stale** hint. Queue returns them to Open and adds
work for the next free agent, after the usual estimate and acceptance checks.
The confirmation offers Undo, including after readiness fixes. Undo restores
that addition's prior state and fields only while its original run is queued
and its audited ticket revision is unchanged; permissions are checked again.
Assigned or active In progress work remains disabled, with a display-name reason.
