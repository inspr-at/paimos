# Conditional status transitions

AEON-1345 adds an optional current-status precondition to `PATCH /api/nodes/{nodeId}`.
The node contract calls a ticket's status `state`.

```json
{"state":"in_progress","expected_states":["open","qa"]}
```

The server checks the current state after acquiring the existing tenant/tree
fence and node row lock, in the final mutation transaction. The caller must have
`nodes.write` on the current target. A cancelled ticket therefore stays cancelled
when a dispatcher tries to claim an earlier open/QA snapshot. Parent statuses
remain derived; the existing leaf-only restriction still applies.

A mismatch returns HTTP 409, for example:

```json
{"error":"status precondition failed: current state is cancelled","code":"status_mismatch","current_state":"cancelled"}
```

The whole patch is refused: title, fields, revision, queue membership and events
are unchanged. The status comparison precedes `If-Unmodified-Since`, so a request
with both stale status and stale revision receives the specific 409. A matching
status with a stale revision still receives 412. Omitting `expected_states`
preserves the existing behavior.

The list must contain 1–32 nonblank strings, each at most 128 Unicode characters,
and requires `state` in the same patch. Null, an empty list, and malformed values
return 400. Values match canonical stored states exactly; display names are not
accepted as aliases. No new route, permission, migration or response-contract
version is introduced; this node response is outside the pinned reporter surfaces.

The CLI sends the precondition to the server, retaining its revision header:

```sh
aeon issue update AEON-123 --status in_progress --if-status open,qa
```

`--if-status` can be repeated. It requires `--status`. Conflicts exit nonzero,
never retry unconditionally, and include `code` and `current_state` in JSON error
output on stderr. Ordinary issue updates keep their prior payload.

## LEAD integration bundle

The delivery bundle consists of `scripts/lead/conditional-status.patch`,
`scripts/lead/conditional-status.sh`, and `scripts/lead/conditional_status.py`.
The patch targets LEAD's `dispatch.sh`, `worker-run.sh`, `start-queue.sh` and
`autopilot.py`. All changes are supplied in this worktree; live harness files and
loops are untouched. The API and installed CLI must ship before the coordinator
activates this bundle, after the r138 cut and the required cross-family gate.

The coordinator applies the patch to candidate files and installs the two new
helpers and the four patched candidates through the existing `harness-install.sh`
process. Workers must not invoke that installer: it runs a model review and
activates live scripts. Patch context is checked exactly; if LEAD scripts changed,
refresh the candidate hunks and rerun the scenarios before installation. The
parked AEON-1342 client-side transition retries/settle delays are superseded.

The dispatcher performs one conditional `open`/`qa` → `in_progress` claim for a
first build; fix, merge and evidence rounds write no status. Cancelled tickets
are refused. An unreadable status or refused conditional claim defers the queue
entry. The worker reads again after registration and marker calls, immediately
before the builder. Cancelled or unreadable tickets get no builder and receive a
stop record; session cleanup has three bounded attempts and escalates failure.

Autopilot first-build and follow-up paths, precheck requests, CI-first pushes and
green-CI gates honour stop records and current cancellation. Unreadable tracker
status retains pending work for retry. Stop records belong to the dispatched
brief and epoch; older records do not suppress a later dispatch. Checks repeat
after GitHub/CI reads before push or gate. The delivery QA write also uses a
precondition; failure defers enqueue, and completed sibling tickets keep their
terminal status. These external actions cannot share
the API transaction; the atomic guarantee applies to the ticket status claim.

Run the stateful stub scenarios with the real integration patch applied only in
memory; tracker, push, builder and gate effects stay stubbed:

```sh
python3 scripts/lead/test_conditional_status.py --harness-source /Users/markus/.aeon/lead/aeon
```

The API and CLI regression tests and the LEAD helper suite are registered as
ESSENTIAL. The API test proves the
cancel/start overlap by observing PostgreSQL's lock wait, then checks the 409,
unchanged fields, audit count, permission/tenant boundary and revision behavior.
The CLI test checks conditional payloads, structured errors, no retry, validation
and unconditional compatibility. `scripts/test-conditional-status-negative.py`
proves the database test fails when the comparison is disabled in a temporary
overlay, then runs the fixed test binary through the approved remote runner.
