# Owned worker dispatch (AEON-1302 / P5c)

P5c supplies the dispatcher, a C0 owner-socket CLI command factory, protocol
progress, a session-bound `report_progress` tool, and the server attention
selector. I2 owns production mounting in agentd, the CLI session command tree,
the harness heartbeat and server attention publisher. This package alone does
not enable live worker dispatch. Release notes remain hidden until that wiring
and the LEAD tooling cutover are qualified.

## Dispatch and attribution

`nativedispatch.New` requires an authenticated assignment `Authority`, the
owning C0 `Host`, and an `Ancestry` updater. There is no fallback launcher or
default authority. `Dispatch` validates `NativeLocalStart` and verifies the
existing leaf work item, immutable parent harness generation, physically
assigned worktree, owner and unexpired worker grant. The request contains the
exact account, computer, workspace, model profile and policy identifiers; it
never chooses another account on capacity failure.

The authority resolver is a preflight. P1b's final start/claim transaction must
still recheck `RequireTx`, leaf status, ancestry, owner account enrollment,
policy revision and admission under the existing tenant/tree/record locks.
The existing claim registers the managed harness child and reserves account
and host capacity before the supervisor can fork. P5c never calls the legacy
unmanaged `harness register` path and never launches a vendor binary.

Same-owner duplicate dispatches join the same pending operation. Changed
request content, actor, owner, key ceiling or grant revision conflicts. Durable
idempotency across restart remains in the C0 intent store, with the same
`request_id`. A transport loss is unconfirmed and cached locally; there is no
automatic retry or replacement worker. Reconcile through the owning supervisor
and durable receipts before taking further action.

Native child/task observations wait for the start receipt, match the exact
binding and approved parent, and call only `Ancestry.UpdateExisting`. Sequence
replays are idempotent; changed same-sequence payloads and other parents are
refused. The provider adapter authenticates the source; a public task reference
does not grant ancestry authority. Owned hook events must use this path instead
of registering another child. Synthetic hook suppression belongs to the
owner-approved per-session settings overlay, not global personal settings.

## Inbox, completion and occupancy

The returned worker reads status and routes input, interrupt and stop through
its registered C0 host. An accepted start can learn its registered binding
through an authenticated host snapshot for the same owned session; it cannot
adopt a successor generation. Caller changes to a returned snapshot cannot change that binding.
Input delivery requires correlated native receipts; accepted/queued means
intent only. Interrupt captures the exact active turn. Stop captures the
process identity digest; force retains the existing person step-up requirement.
The host performs the final monotonic deadline and ownership check under its
native write lock. No raw PID is signal authority.

Explicit final answers continue through the owned runtime's existing final
sink/outbox and drain-only authorization. Token chunks are never assembled
into a durable final. A final, completed task or `turn_completed` receipt is
not process exit. A stop request or ambiguous response retains occupancy;
only the exact owned `exit_confirmed` receipt releases the monitoring hold.
Rejected/expired start receipts and transport errors do not by themselves
prove no-fork abandonment. The owning supervisor must reconcile its actual
ledger using verified no-fork/exit evidence. Failure and uncertainty return errors.

`Worker.Reservation` is a content-free monitoring projection of one attempt:
tenant, account, computer, workspace, leaf, parent and held/uncertain state.
It is **not** an additional admission ledger. Server worker `agent_runs` count
once using AEON-1251's account-backed predicates; native root sidecars add
occupancy only for `run_id IS NULL`. The server and local shared writer ledger
remain the capacity authorities. There is no pooling, token intermediary,
automatic account hopping or foreign PID adoption.

Local bookkeeping is bounded to 256 dispatches. `Forget` removes only a
confirmed stopped entry, never an uncertain hold; server idempotency
still applies to later replays. A full cache refuses further dispatch visibly.

## Progress without a status file

The supervisor creates `NewProgress` with the exact binding, pinned native
identity, its runtime lifetime and monotonic grant deadline. Invalidate it on
exit, ownership loss or replacement; do not reuse its tools on a successor.
Observe plan frames locally before Expert/privacy export. `ObservePlan` accepts:

- Claude Agent SDK `assistant` snapshots for that conversation, containing
  `TodoWrite` todos with `pending`, `in_progress` and `completed` states. Subagent
  messages with `parent_tool_use_id` are excluded.
- Codex app-server `turn/plan/updated` for the pinned thread and the current
  adapter-correlated turn, with `pending`, `inProgress` and `completed` states.

The adapter supplies the current source identity and monotonic frame sequence.
Other bindings, processes, conversations and stale sequences are refused.
Frames are bounded to 64 KiB, depth 16 and 128 steps. Percent is the completed
step fraction. ETA uses elapsed time and newly observed completions in the same
plan; it stays absent until there is evidence for a rate. A changed plan or a new Codex turn resets
that baseline. Only a generic step-count note leaves this parser; private plan
text, explanations, reasoning and tool bodies do not enter the heartbeat.

`Progress.AddTool` registers `report_progress` on the existing session-scoped
tool server, subject to its approved tool policy. Other qualified C0 providers
can report explicitly while protocol derivation remains unsupported. Its strict schema is exactly
`pct` (integer 0–100), `remaining_min` (number 0–524160) and `note` (nonempty
public text, at most 512 bytes). Missing/unknown/duplicate fields, unsafe text,
closed lifetimes and expired grants are rejected. Session identity comes from
the daemon closure, not tool arguments. The agent receives no credential.

`Progress.Heartbeat` emits existing `progress_pct`, `eta_ready_at` and
`activity_note` fields. Explicit reports take precedence for ten minutes;
derived progress is the fallback. ETA stays anchored to the observation time,
not moved forward by every heartbeat. Missing/stale observations send explicit nulls for percent and ETA. This
clears the existing harness values rather than refreshing an old ETA when
only percent is reported; preserve those nulls in the heartbeat adapter. The
tool acknowledges local acceptance only; successful server heartbeat receipt
confirms persistence. Neither path reads `.agent-status.json` or a transcript
file. The ignored progress file used by this legacy builder remains a current
worker obligation until its coordinator cuts over.

`MissingProgressAttention` is a server-side selector. I2 supplies the currently
authorized owned generation, immutable parent and **server** accepted report
times, using the database clock. After ten minutes, missing or stale percent
or ETA produces one `native_missing_progress_eta` attention item routed to the
parent coordinator, with the child generation as its identity. Fresh zero
percent is valid. Scouts, legacy sessions, coordinators and stopped sessions
are excluded. Publish/update the item idempotently by child generation, and
resolve it when both fields become fresh or the child stops. Do not refresh
source age merely by retransmitting cached values. This internal selector code
is not a new value in the frozen public `HarnessAttentionReason.kind` enum;
I2 must use the existing authorized coordinator attention/inbox surface, or
request a coordinator contract amendment before adding a public enum value.

## Exact I2 wiring handoff

1. Add `rt.nativeWorkerDispatch()` to `cmdSession().subs` in the I2-owned
   `internal/cli/compat_cmds.go`. This mounts `aeon session dispatch --socket
   PATH`, reading one `NativeLocalStart` JSON object from stdin (16 KiB maximum).
   It sends the C0 `start` envelope with the stable request ID, null expected
   binding and `X-Aeon-Session-Protocol: aeon-native-session/1`.
2. In the existing owner-socket handler, derive authenticated tenant/project
   from the approved parent/workspace, resolve the immutable assignment and
   reuse one `Dispatcher` per authenticated owner. Connect `Host.Start` to P1b
   registration/claim and the existing agentd owning supervisor. Keep kernel
   peer, owner socket, paired epoch and grant checks; do not connect this CLI
   factory directly to an unmanaged model launcher.
3. Connect source-correlated child/task events to `ChildEvent`, and its updater
   to existing harness ancestry. Keep server authorization inside that write.
   No separate provider-hook registration for this owned child.
   Enqueue observations without blocking the native producer on start completion.
   For an accepted start without a binding, first read `Worker.Status` for the
   source's registered generation before forwarding its child observations.
4. Before provider export, pass the local pinned frame to `ObservePlan`.
   Non-plan frames simply bypass this parser. Add `report_progress` to the
   existing exact runtime's approved tool server. Cancel/close the progress
   tracker under the supervisor's ownership transition.
5. Copy `Progress.Heartbeat` fields into the existing harness heartbeat sent
   with the daemon's private session proof. Persist through the existing
   authorized transaction. Preserve explicit null progress/ETA fields so stale values are cleared. Do not
   invoke the legacy status-file reader or hand
   its worker lease to the agent. Apply the attention selector with server
   timestamps, current generation and parent routing as described above.
6. Route LEAD prompt/inbox input and interrupt/stop commands through the returned
   worker/C0 runtime, and consume existing durable final and exit receipts.
   Use bounded receipt pages (default 200, maximum 500) and SSE replay IDs;
   terminal viewer detach must not stop the worker.

No authored API, migration, permission, dependency or external script changes
are part of P5c. I2 owns wiring, not the provider qualification decisions.

## LEAD `worker-run.sh` replacement proposal

Only the LEAD tooling owner applies this proposal to
`/Users/markus/.aeon/lead/aeon/worker-run.sh`. Retain its eight positional
arguments, approved worktree/branch/brief ownership checks, prompt's no-push/no-
deploy/model-review rules, marker posting and caller-facing completion logging.
Keep existing vendor routing disabled unless `G_PROVIDER_PINS` is satisfied.

Replace these exact current sections:

| Current section | Owned replacement |
| --- | --- |
| `uuidgen` session ref, `openssl` lease and `harness register --management unmanaged` | LEAD resolves the existing leaf, assigned worktree, parent generation, owner account/computer/workspace, approved model profile and immutable worker policy. Create one stable C0 request ID for this attempt, persisted as nonsecret intent metadata. Submit the approved start once through agentd; use its registered harness child for the marker. |
| `case "$H"` subprocess launch, `CPID=$!`, PID file and `kill -0` loop | C0 dispatch plus receipt/event observation. Agentd alone owns the unmodified vendor process and deadline. Never launch a second foreground/exec writer. |
| Status-file note/ETA parsing, `harness heartbeat`, rollout-file usage scan | Agentd protocol progress/tool, heartbeat and provider usage events. No status file or native transcript scan. |
| `on_signal`, runtime-cap PID signals and unconditional `mark-stopped` retries | Submit a generation-bound graceful stop through the owning runtime. Record stopped only after owned exit is confirmed. Keep unknown exit as recovery-needed with account/host occupancy held. |
| `thisrun` capacity-text grep and recursive fallback `exec "$0"` | Use a qualified provider failure receipt. A separately approved fallback is a new explicit attempt only after verified old exit; it never changes the account of an existing native conversation or overwrites an uncertain attempt. |

After I2 mounts the command, the exact start invocation is:

```sh
AEON_CLI=/Users/markus/.local/share/aeon-cli/aeon
"$AEON_CLI" --json session dispatch --socket "$AGENTD_SOCKET" \
  < "$APPROVED_NATIVE_START" > "$ST/dispatch.json"
```

`APPROVED_NATIVE_START` is a private, bounded **intent** file prepared by LEAD,
not a progress file or credential. Its complete C0 body is:

```json
{
  "intent": {
    "request_id": "<stable attempt UUID>",
    "expected_project_revision": 1,
    "role": "worker",
    "profile": "native",
    "harness": "codex",
    "computer_id": "<approved owner computer UUID>",
    "account_id": "<owner account UUID>",
    "workspace_id": "<assigned workspace ID>",
    "model_profile_id": "<approved model profile UUID>",
    "work_id": "<existing leaf UUID>",
    "parent_harness_session_id": "<current coordinator generation UUID>",
    "resume_from_owned_session_id": null,
    "policy_id": "<immutable approved worker policy UUID>"
  },
  "cwd": "<assigned physical worktree>",
  "account_key": "<approved local profile label>",
  "native_resume_ref": null,
  "after_exit": null
}
```

Placeholder values must be resolved from the current approved assignment;
`expected_project_revision` is its actual revision. The dispatcher refuses
parent work items. Read the returned `session.current_binding` and use
`harness_session_id` for attribution, `owned_session_id` for C0 input/receipt
routes, and the entire binding for controls. An accepted receipt is pending
intent, not a successful build. The prompt from the current script is then an
authorized C0 input with a stable `input_id`; the launcher must await correlated
delivery evidence. Observe explicit final persistence and owned exit separately.
The current launcher completion log is emitted only after the latter, never
from a 100% report alone. A watcher disconnect has no stop authority.

This is a tooling proposal, not an installed replacement. The LEAD owner must
complete the C0 input/receipt watcher around the start command after I2 wiring
and verify marker-before-work, input delivery, final persistence, detach,
signals and cap expiry on an approved test worker. `G_LEAD_TOOLING` and
`G_PROVIDER_PINS` block live replacement until those checks are recorded.

## Rollback

Disable new native worker dispatch first. Keep durable bindings, receipts,
final history, account homes and native conversation files. Request graceful
stop through the owning daemon with the captured binding and process identity.
Wait for verified root/group exit and reconciled server/local occupancy before
restoring the previous launcher for **new** work or deliberately resuming the
same conversation on its exact authorized owner profile. Unknown exit means
hold and recovery, not PID adoption, cleanup, automatic fallback or a second
writer. The LEAD tooling owner restores its saved script; builders do not edit
launchd, live coordinators, external tooling or another worktree.

## Verification and metadata

- `TestDispatchDuplicateChildBarrierAndOccupancy` holds the fake host before
  fork and establishes both duplicate and child-event joins with explicit
  barriers. It proves one registered child/process/reservation, immutable
  ancestry, parent refusal, runtime-bound inbox/control and uncertain holds.
- `TestDispatchChangedGrantAndUnconfirmedStart` proves no automatic reinjection
  after ambiguous start and rejection of changed/expired grants.
- `TestNativeProgressPlansToolAndHeartbeat` exercises both native plan fixtures
  and a real in-memory MCP connection, strict arguments, source identity,
  anchored/stale ETA and cancellation. No model/subscription calls occur.
- `TestOwnedWorkerMissingProgressAttentionClock` tests the exact ten-minute
  boundary, partial/fresh/stale reports and owned build/ship scope.
- `TestNativeWorkerDispatchFrozenEnvelopeAndReceipt` verifies the CLI C0 body,
  stable ID, strict bounds and honest uncertain/mismatched/transport results.

R0 grant: these five tests add ESSENTIAL rows only to
`scripts/ci/go-test-tiers.json` using the canonical tier writer. Existing
`internal/native*/**` and `internal/cli/**` audit ownership covers all new
sources. No shared-fence caller, table, permission or web manifest row is added.
Required checks are guarded remote Go for `./internal/nativedispatch/...` and
`./internal/cli/...`, guarded static `--merge-main`, and strict tier inventory
checks. Refused/incomplete remote lanes remain incomplete; PR CI is authoritative.

### Builder evidence

At source commit `863e60943`:

- PASS: all `internal/nativedispatch` tests with `-race`, in the one permitted
  local package fallback after the guarded remote lane refused for load.
- PASS: `TestNativeWorkerDispatchFrozenEnvelopeAndReceipt` with `-race` through
  `remote-test.sh` on the approved test host.
- PASS: guarded `ci-static-locked.sh` / `--merge-main`, exit 0, 42 checks,
  zero optional skips. This includes strict Go and web tier inventories,
  source ownership, vet, shard manifests and API/migration checks.
- PASS: separate guarded `nix develop --offline -c node
  scripts/test-tiers/cli.mjs check go --strict`.
- PASS: the initial full targeted remote Go packages (`nativedispatch` and
  `cli`) at `eff20c089`; dispatcher race tests also passed remotely at
  `b1578df47`.

The additional full CLI `-race` run at `b1578df47` failed. Its large console
output was truncated before the failing case could be identified; a captured
diagnostic repeat was refused for load (exit 3). The final named CLI behavior
test passed separately, but the broader race failure still needs authoritative
PR CI diagnosis. No existing test was changed or relaxed. A separate source
mutation/negative-control run was not performed; wrong-binding, wrong-parent,
changed-grant and possible-fork rejection fixtures are covered by the named
behavior tests.

Acceptance is demonstrated at the package boundary: approved leaf dispatch,
pre-spawn registered ancestry and duplicate coalescing, owned status/inbox/
controls with conservative occupancy, file-free protocol/tool progress and
parent attention, and the exact external launcher/rollback proposal. I2 wiring,
`G_LEAD_TOOLING`, `G_PROVIDER_PINS` and authoritative PR CI remain release
prerequisites owned by the coordinator/integrator.
