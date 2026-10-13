# Owned native session starts (AEON-1267 / P1b)

This is disabled groundwork against C0's `native-session/1.0` contract. I1 owns
production mounting, provider qualification, existing-grant adapters and adding
root occupancy to every legacy account/host admission query. Automatic routine
launches remain disabled. No live account, daemon, model or login is changed.

`store.New` requires an existing-grant `Authority` and a paired local-ledger
`Observer`. No default grants or permissive implementations exist. The native
module stays disabled. I1 composes `Start`, `Read` and `Claim` with the other
slices' mutation/receipt/command methods to implement the complete C0 Store.
`store.WithProject` carries the validated route project for `Start`; `View`
reads an accepted logical session and `ByHarness` supplies native-context lookup.

A start persists one logical conversation and one request-ID intent. Exact
retries retain both IDs. Changed payload, actor, key ceiling or grant conflicts.
Role/project/work/account/computer/workspace/model/policy and actor snapshots
are immutable. Coordinator starts require the owning person and a current
person-invoked project grant. Workers require a current leaf and coordinator
parent plus an existing leaf run reservation. Resume is supplied by P3/I1.

The daemon observes its private local ledger before the final transaction.
Claim then takes tenant/tree/pairing fences, rechecks the current daemon and
original actor with `RequireTx`, validates current grants, target revision,
parent/leaf, pairing/ledger generations, own account/context/model, account and
host capacity, and only then registers a fresh harness/runtime epoch and root
hold. The registration principal is the paired computer principal. The existing
tenant event log receives content-free metadata last. Claim commits before a
launcher can open a process. Replayed claims retain their original expiry and
binding; expired claims never renew by retry.

Coordinator roots use `native_session_roots`, with a null `run_id`. Workers use
their account-backed `agent_runs` and never receive a root reservation. All
unreleased null-run roots count irrespective of display state or heartbeat:
queued, starting, running, idle, taken and exit_unconfirmed retain occupancy.
`NativeRootCountTx` is I1's account/host integration seam. `SetNativeRootStateTx`
cannot free capacity; confirmed no-fork or exact owned root/group exit must be
validated by the owning lifecycle service before release. API outage, turn
completion, generation change or viewer detach never releases a hold.

`start.Starter` uses the already committed server claim, P4's local profile
resolver, the existing owning supervisor's Launcher and a mandatory LaunchGuard.
The guard keeps the local shared ledger/writer lock across the final expiry and
possible-fork check and open. Account homes and physical cwd are resolved only
locally; cwd symlinks cannot escape the approved workspace. Browser start DTOs
contain no argv, env, cwd, config path, vendor reference or credential. Process
lifetime belongs to the supervisor, separately from the request or viewer.
The request's elapsed monotonic time consumes the server TTL.

The publishing daemon may provide `Observation.TerminalViewerURL` at registration.
`Options.TerminalViewerHosts` is the setting seam (I1 owns config wiring); nil
defaults to `100.64.0.0/10` and `*.ts.net`, an empty list denies all URLs.
Only HTTPS URLs of at most 200 bytes, with one ASCII Zellij session path
`[A-Za-z0-9._-]{1,64}`, are accepted. Explicit ports are limited to 443. Query,
fragment, userinfo, path escaping, nested paths and dot traversal are rejected
with C0's allowlisted `invalid_input`, without echoing the URL. SQL fetches the
URL only for an authorized person; agent projections remove it independently.
The URL is not a credential. Zellij's read-only login token stays in the person's
browser and is never sent to or stored by PAIMOS.

## R0 integration rows (same PR, before merge)

P1b's exclusive write set excludes all shared inventories. R0 must apply these
rows in this PR; this is a merge prerequisite, not work deferred to I2.

- Source ownership: add `internal/nativesessions/start/**` and
  `internal/nativesessions/store/**` to the existing native-session source slice.
  `internal/agentaccounts/native_root*.go` and migration 1346 already match S1's
  account/db globs. No new route, permission, contract pin or direct
  `db.LockTenant`/`db.LockTree` caller was added; store uses `agentpairing.Lock`.
- Register the following Go cases as ESSENTIAL (no browser/shard rows):
  `internal/nativesessions/store:TestStartClaimRevocationBarrier`,
  `internal/nativesessions/store:TestStartRegistrationIdempotencyAndPersonViewer`,
  `internal/nativesessions/store:TestTerminalViewerURLStrictAllowlist`,
  `internal/nativesessions/start:TestStartRequiresDurableClaimAndLocalLaunchFence`,
  `internal/agentaccounts:TestNativeRootHoldsCountUntilVerifiedRelease`.
- DSAR: all three new tables are `personal`, with locator
  `{ "tenant_column": "tenant_id", "person_column": "owner_person_id" }`.
  Complete column classifications follow below. Existing harness proof digests
  retain the existing harness inventory classification. No raw native ID,
  credential, prompt, tool body, transcript or local path is added to SQL.

| Table | Personal columns | Metadata columns |
| --- | --- | --- |
| `native_session_intents` | `owner_person_id`, `actor_principal_id`, `actor_authority`, `created_at`, `claim_expires_at` | `tenant_id`, `id`, `request_id`, `owned_session_id`, `project_id`, `actor_kind`, `actor_key_id`, `actor_key_ceiling`, `actor_grant_id`, `actor_grant_revision`, `request_digest`, `intent`, `policy`, `launch_config`, `claim_request_id`, `claim_digest` |
| `native_session_bindings` | `owner_person_id`, `actor_principal_id`, `view`, `terminal_viewer_url`, `created_at` | `tenant_id`, `id`, `project_id`, `role`, `harness`, `account_id`, `computer_id`, `paired_principal_id`, `workspace_id`, `model_profile_id`, `work_id`, `parent_harness_session_id`, `policy_id`, `run_id`, `current_harness_session_id`, `runtime_epoch`, `daemon_id`, `daemon_generation`, `ledger_generation`, `binding_revision` |
| `native_session_roots` | `owner_person_id`, `created_at`, `released_at` | `tenant_id`, `id`, `owned_session_id`, `project_id`, `account_id`, `computer_id`, `run_id`, `runtime_epoch`, `hold_state` |

The named risk is retaining the old registration principal or treating a queued
start as permanent authority. The database test's explicit local-read barrier
lets revocation commit before final claim; it checks the exact refusal and no
new registration, root reservation, claim or event, with one intent on retry.
Other fixtures exercise exact registration retries, person-only URL reads,
every URL rejection class, retained root occupancy, worker double-count
prevention, physical cwd escape and monotonic expiry at the local launch fence.
Only fake grant/native transports are used; no subscription/model call occurs.

Verification results are recorded in the handoff and ignored progress file.
Browser testing is not applicable. R0 inventories and authoritative PR CI are
required before merge; this groundwork is hidden from release notes.
