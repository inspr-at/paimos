# AEON-1471 · concurrent agent-key admission

Regular uses now take `pg_advisory_xact_lock_shared` on the existing
`hashtextextended(lower(tenant_id + ':' + key_id),615)` identity. Scope and
authority changes take `pg_advisory_xact_lock` in the same one-bigint advisory
namespace. Uses of one key can overlap; changes drain admitted uses and exclude
later uses through commit. The transaction lifetime and five-second acquisition
bound remain unchanged.

The usage timestamp debounce also needed a change: `ON CONFLICT DO UPDATE ...
WHERE` still locks a fresh row when the predicate prevents an update. An initial
`WHERE NOT EXISTS` now skips a fresh committed timestamp without taking that
tuple lock. Simultaneous first/expired uses retain the conditional upsert. The
one-minute debounce and trim's conservative 24-hour-plus-one-minute recent-use
guard remain unchanged.

## Ten-worker rehearsal

On 2026-10-17, the approved `remote-test.sh` ran commit `23b254906` on mbp2606,
Darwin arm64, Apple M5 Max, PostgreSQL 18.6, with `GOMAXPROCS=8`. Each arm runs
ten workers sharing one key, thirty transactions per worker, after establishing
ten pool connections and
warming the initial statements/usage timestamp. Each transaction uses the actual
`InTenant` and `RequireTx(nodes.read)` paths and a bounded event-count query.
The restricted app role has NOSUPERUSER/NOBYPASSRLS and FORCE RLS. Pool acquisition,
authorization, query execution and commit are included in each latency sample;
connection setup, fixture construction and warm-up are excluded. There are no
injected sleeps or network services in this workload.

The exclusive baseline reconstructs the original admission mode through the
exclusive context declaration and the exact original usage-upsert SQL through a
test-only transaction wrapper. All arms use the same schema, RLS, fixture,
authorization code and query. The measurements cover the database transaction
path; HTTP transport and production latency are unmeasured. Timings are
diagnostic observations, with no elapsed-time pass/fail threshold.

| Admission / usage recording | Requests | p50, ms | p95, ms | Mean / max advisory waiters | Mean / max row waiters | Activity samples |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Original exclusive fence and original upsert | 300 | 12.56 | 40.81 | 8.847 / 9 | 0 / 0 | 9,656 |
| Shared fence with original upsert | 300 | 7.056 | 11.63 | 0 / 0 | 7.556 / 9 | 3,231 |
| Shared fence with fresh-row debounce skip | 300 | 2.313 | 2.664 | 0 / 0 | 0 / 0 | 610 |

The final arm reduces p50 by 81.6% and p95 by 93.5% in this rehearsal. The middle
arm demonstrates why changing only the advisory mode moves serialization to the
usage row. An independent observer samples `pg_stat_activity`, restricted to
the disposable app role/database; advisory waiters have `wait_event='advisory'`,
and row waiters have `transactionid` or `tuple`. Counts are observation-weighted,
not a duration histogram; zero observed waiters is not a claim that all future
requests can never wait. Expired/first usage writes and actual resource
mutations can still contend on their own rows and tenant/tree fences.

The standalone verbose repeat, used to capture EXPLAIN, confirms the same wait
pattern despite different shared-host load:

| Admission / usage recording | p50, ms | p95, ms | Maximum advisory / row waiters | Activity samples |
| --- | ---: | ---: | ---: | ---: |
| Original exclusive fence and original upsert | 24.15 | 66.41 | 9 / 0 | 10,083 |
| Shared fence with original upsert | 14.33 | 17.62 | 0 / 9 | 3,429 |
| Shared fence with fresh-row debounce skip | 3.180 | 3.599 | 0 / 0 | 661 |

Reproduce only through the approved remote runner:

```sh
remote-test.sh "$WORKTREE" ./internal/authz -run '^$' \
  -bench '^BenchmarkKeyScopeLock$' -benchtime=1x -count=1 -v
```

## Event scan

The probe seeds 100,000 synthetic events with 256-byte payload padding. A busy
project identity has 40,000 events; the remaining events are distributed over
other identities. `delivery.step`, `delivery.item` and `delivery.incident` are
sparse among ordinary node, comment and heartbeat activity. It analyzes the
events table and runs `EXPLAIN (ANALYZE, BUFFERS)` for the exact
`internal/delivery/flow_stream.go:flowHints` query, with cursors 0 and 90,000 and
a 100-row limit. The query uses the same service visibility as the handler and
the restricted app role, without bypassing RLS through a superuser.

Both cursors use `events_subscription_type_cursor_idx` (the existing
`tenant_id,type,id` index from migration 1288), followed by a small quicksort.
There is no sequential events scan.

| Cursor | Returned rows | Matching index rows / rows filtered | Execution, ms | Planning, ms | Execution shared hits / reads |
| --- | ---: | ---: | ---: | ---: | ---: |
| 0 | 100 | 120 / 181 | 0.477 | 4.190 | 309 / 0 |
| 90,000 | 12 | 12 / 18 | 0.150 | 2.015 | 39 / 0 |

The old-cursor plan inspects 301 matching-type events across the dataset rather
than walking the busy project's 40,000-event history. The tail plan inspects
30. Existing `events_node_idx` also provides the tenant/node/id access path.
This fixture shows no clearly missing index, so migration 1362 is not added or
reserved and no shared migration/DSAR files change. These warm-cache execution
results do not reproduce or explain the isolated production I/O wait; another
index is not justified by that observation alone.

## Admission callers and ordering

| Caller | Mode and reason |
| --- | --- |
| `db.lockAgentScopeUse`, through `InTenant`, `InTenantWithBootstrap` and `InTenantReadSnapshot` | Shared for authenticating/declared use keys; exclusive for declared change targets. One sorted, deduplicated batch selects the strongest mode per canonical key. |
| `authz.LockKeyScopeUseTx` / `recordKeyScopeUseTx` / `RequireTx` | Shared re-entry; read the live key ceiling and record granted use. |
| `auth.proposeKeyTrim` / `WithKeyScopeUse` | Shared for target and authenticating keys; proposals leave scopes unchanged. |
| `nativeintegration.RootAuthority.Revalidate` | Shared for the declared delegated original key and authenticating key; authority is rechecked under final resource fences. |
| `nativesessions/store.Store.Claim` | Shared for the persisted original actor key and paired daemon key. |
| `nativesessions/receipts.Repository.Persist` | Shared for the final publication original actor key and daemon key. |
| `auth.decideKeyTrim` / `authz.LockKeyScopeChangeTx` | Exclusive for the target before the tenant fence; approval/restore changes scopes. Decline shares this decision path. |
| `auth.adoptAgentKey` / `WithKeyScopeChange` | Exclusive; adopting a person changes the original-creator ceiling. |
| `auth.agentKeyScopes` / `WithKeyScopeChange` | Exclusive; manual edits change scopes/full access/expiry, and management reads can prune retired scopes. Any authenticating workstation key joins the same batch. |
| `auth.handleOwnerWorkstation` / `WithKeyScopeChange` | Exclusive; designation changes authority and unmarking removes workstation scopes. |
| `auth.revokeAgentKey`, `auth.rotateAgentKeyWithScopes` / `WithKeyScopeChange` | Exclusive for the old key before tenant/resource locks; `revokeAgentKeyTx` reuses this entry fence. |
| `TestAttachedDaemonAuthorityRecheckedInEveryTransaction` | The explicit revocation/scope/role test mutation uses the exclusive change helper. |

The order remains **key batch → tenant → tree → resource rows → event counter
last** (including existing pairing/blob fences where applicable). No change
upgrades a shared key fence after taking resource locks. Context declarations
preserve earlier targets and case aliases cannot downgrade an exclusive fence.
An admitted use can re-enter its held shared lock despite a queued changer;
PostgreSQL excludes newly arriving uses until that changer completes.

## Security and regression evidence

The exclusive changer waits for all shared holders, so committed usage evidence
and the guarded final action finish before a trim examines scopes/recent use.
Later requests queue behind the changer and `RequireTx` re-reads the updated
scope ceiling. Authentication snapshots cannot retain a removed scope after
the change commits. Every scope mutation retains its final-transaction
authorization and audit writes.

Repeatable-read transactions establish their snapshot before an admission wait.
After the tenant guard, a temporary `FOR SHARE` probe rejects a key changed
since that snapshot with PostgreSQL `40001`. `FOR KEY SHARE` is insufficient
for a non-key scope update. Rolling back the probe's savepoint releases its row
lock before handler locks; the parent transaction retains its shared advisory
fence through commit. The operation fails closed rather than authorizing from
the stale snapshot.

Three new cases are ESSENTIAL in the canonical Go tier manifest:

- `TestKeyScopeUsesOverlapThroughAuthorization`: four independent PostgreSQL
  transactions must all reach a barrier after successful same-key authorization
  before any can commit, including shared-fence re-entry.
- `TestKeyScopeChangeDrainsUsesAndRejectsQueuedStaleScope`: database lock-state
  barriers prove the in-flight use, queued trim/manual edit and later use overlap;
  later requests remain excluded through commit and lose the removed scope.
  Both read-committed and repeatable-read transactions are covered.
- `TestKeyScopeBatchModesAndOrder`: exclusive probes verify the sorted batch
  prefix and actual per-key modes, including duplicate/case aliases and context
  declaration composition.

The existing trim grant/approval barriers remain intact. Opposite proposals now
prove sorted shared admission and completion of the second proposal while the
first is paused. Registration/move asserts the actual tenant-row wait after
shared admission. Workstation lock probes use independent contexts, retaining
their real NOWAIT tenant/computer/pairing assertions. Adoption's queued and
in-flight authority tests remain unchanged.

A temporary local negative control changed only regular admission back to the
original exclusive SQL. The overlap test failed for the expected reason:
`use completed before all grants overlapped: timeout: context deadline exceeded`.
The positive source was restored immediately; no negative-control commit was
created. This was one targeted package with `GOMAXPROCS=2`, `go test -p 2` and
the `just db-up` Postgres while the remote lane was occupied. The disposable
maintenance database was removed afterward.

Remote validation passed:

- Full `internal/auth`, `internal/authz`, `internal/db` and `internal/harness`
  suites at `23b254906`, including the new barrier tests and existing trim,
  adoption, scope, workstation and registration/move checks.
- Full `internal/agentpairing` and `internal/nodes` suites at `d522b5b53`.
- At `23b254906`, targeted global lock-order checks in `internal/questions`,
  `internal/inbox`, `internal/portal`, `internal/agentruns` and `internal/rules`,
  plus delegated native claim/final/revocation checks in
  `internal/nativeintegration`, `internal/nativesessions/store` and
  `internal/nativesessions/receipts`.
- Both 10-worker benchmark runs, with the explicit legacy reconstruction and
  intermediate shared-only arm.
- `ci-static-remote.sh` at `23b254906`, running
  `node scripts/ci-static.mjs --merge-main`: exit 0, 43 checks passed, zero
  optional skips. This includes Go vet, tier/shard manifests, audit ownership,
  migrations and contract/static checks.

No API, permission, version or DSAR schema changes are required. New sources
are covered by existing audit slice globs; only the three own ESSENTIAL rows
were added to the shared tier registry. No origin push, deployment or model
review gate runs as part of this worker brief.
