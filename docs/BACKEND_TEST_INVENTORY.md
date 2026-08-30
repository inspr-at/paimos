# Backend test inventory

PAI-852 audited the backend corpus before changing CI selection. At the
candidate head, the repository contains 1,792 top-level test or fuzz functions
in 268 `_test.go` files (267 contain test/fuzz functions and one is the shared
handlers helper): 1,787 `Test*` functions, five `Fuzz*` functions, and no
benchmarks. Subtests are intentionally counted under their owning function.

The audit found no evidence-based deletion. The only duplicate top-level name,
`TestProviderSharedContract`, applies the same contract to two different CRM
providers and therefore is not duplicate coverage. Historical migration,
compatibility, removed-route, identity, untrusted-message, secret-handling,
SQLite busy/lock, and unsupported-platform tests remain live assurance. The
cost evidence also points away from deletion: the default-parallel baseline
completed in 6m50s while `handlers` alone took 6m49.8s because its tests create
fresh databases and replay the migration chain. Serializing packages magnified
that cost; redundant assertions were not the long pole.

## Keep / fold / drop ledger

Every source-level test is accounted for by its package group below.

| Package group | Files | Tests/fuzz | Class | Evidence for the decision |
| --- | ---: | ---: | --- | --- |
| `backend/handlers` | 123 | 664 | Keep | HTTP, authorization, privacy, untrusted input, SQLite concurrency, removed-route, and customer contract coverage; the runtime hotspot is per-test migration setup. |
| `backend/cmd/paimos` | 45 | 428 | Keep | CLI parsing, attribution, durable inbox, runner control, and release-facing behavior; many tests are the only shell-to-API contract proof. |
| `backend/db` | 12 | 136 | Keep | Migrations, schema guards, rollback/precondition behavior, busy/lock behavior, and the explicit isolated test-DB path guard. |
| `backend/auth` | 14 | 74 | Keep | Identity, scopes, API-key timing, CSRF, OIDC, dev-login separation, and fail-closed authorization. |
| `backend/deliverytrust` | 4 | 50 | Keep | Trust evaluation, history, revision binding, and fuzzed hostile input. |
| `backend/agentd` | 8 | 44 | Keep | Owned-process lifecycle, adapter privacy, restart/reap ordering, locking, and unsupported-platform denial. |
| `backend/agentmessage` | 3 | 39 | Keep | Durable message ledger, sender/target identity, webhook secrets, fallback truth, and concurrent delivery. |
| `backend/cmd/paimos/adapters` | 5 | 36 | Keep | Harness discovery, adapter conformance, fixtures, and external command boundaries. |
| `backend/agentmode` | 8 | 34 | Keep | Snapshot/cursor privacy, filtering, streaming, frontier consistency, and numeric bounds. |
| `backend/supervision` | 3 | 34 | Keep | Lease/grant/input races, replay resistance, SQLite conflict mapping, and authoritative terminal convergence. |
| `backend/delivery` | 2 | 31 | Keep | Delivery snapshot/correction state, legacy visibility, event ordering, and mutation truth. |
| `backend/cmd/paimos/sync` | 2 | 23 | Keep | Resource sync, knowledge rendering, drift, and watch behavior. |
| `backend/externalstage` | 2 | 22 | Keep | Cross-product stage contract, pinned receipts, idempotency, and failure closure. |
| `backend` | 2 | 19 | Keep | Root schema/OpenAPI coverage and production wiring assertions. |
| `backend/secretvault` | 2 | 18 | Keep | Encryption, key sources, rotation rollback, and SQLite transaction safety. |
| `backend/agentmessage/harness` | 5 | 17 | Keep | Codex/agentd delivery adapters, Unix ownership, and stub boundary behavior. |
| `backend/ai` | 2 | 15 | Keep | Prompt constraints and provider request/error contracts. |
| `backend/managedharness` | 1 | 14 | Keep | Managed registration, identity, control routing, and isolated persistence. |
| `backend/contracts` | 4 | 10 | Keep | JSON schema and immutable cross-product fixture compatibility. |
| `backend/devseed` | 1 | 10 | Keep | `dev_login`-tagged synthetic identities and assets; excluded from production builds and checked separately. |
| `backend/handlers/crm/hubspot` | 1 | 9 | Keep | Provider-specific authentication, mapping, paging, and shared CRM contract. |
| `backend/handlers/crm/http` | 2 | 8 | Keep | Generic HTTP provider/signature behavior and independent shared CRM contract. |
| `backend/secretinput` | 1 | 8 | Keep | Owner-only secret-file validation, symlink rejection, and bounded reads. |
| `backend/sse` | 1 | 7 | Keep | Broker ordering, cancellation, backpressure, and subscriber lifecycle. |
| `backend/handlers/knowledge` | 1 | 7 | Keep | Knowledge module boundary, scope, and rendering behavior. |
| `backend/cmd/paimos/adapters/claudecode` | 1 | 7 | Keep | Claude adapter invocation, output, and failure contract. |
| `backend/cmd/paimos-mcp` | 3 | 6 | Keep | MCP tool coverage registry, fixture shape, and bounded tool surface. |
| `backend/httpcontract` | 1 | 6 | Keep | Closed error and route response shapes shared across handlers. |
| `backend/localjournal` | 1 | 3 | Keep | Durable bounded journal recovery and corruption handling. |
| `backend/cmd/paimos-agentd` | 1 | 3 | Keep | Daemon CLI configuration and startup boundary. |
| `backend/cmd/dev-fixture-sql` | 1 | 3 | Keep | Development fixture SQL uses the initialized production schema safely. |
| `backend/ownedprocess` | 2 | 2 | Keep | Unix process-group ownership and unsupported-platform fail-closed behavior. |
| `backend/controlcontract` | 1 | 2 | Keep | Canonical control enum compatibility. |
| `backend/pharoslink` | 1 | 1 | Keep | PHAROS request identifier validation. |
| `backend/models` | 1 | 1 | Keep | Entity graph model invariants. |
| `backend/agentdwire` | 1 | 1 | Keep | Unsupported-platform client behavior. |
| **Total** | **268** | **1,792** | **Keep: 1,792** | **Fold: 0; drop: 0.** |

### Fold: zero today

No group has a second test that asserts the same risk with a strictly sharper
oracle. Compatibility and migration fixtures may look similar, but each plants
a different prior schema or protocol state. Provider contract tests intentionally
repeat one contract against distinct implementations. Folding any of these now
would remove a failure mode rather than only remove duplication.

### Drop: zero today

No backend test was shown to claim an abandoned product shape without a live
replacement. Negative tests for removed endpoints prove that the endpoint stays
removed; legacy-row tests protect upgrades from databases that can still exist;
and human-facing workflow tests exercise HTTP/CLI contracts rather than retired
UI rendering. A future drop must record all three facts in this ledger: the old
claim, evidence that the claim is false now, and the exact remaining test that
covers the original risk.

## Execution policy and timing evidence

- Pull requests run `go vet ./...`, normal tests for directly changed packages
  plus their bounded transitive reverse test-dependency closure, security
  regression/authz fuzz tests, and race tests for directly changed packages.
  The 664-test handlers package uses four isolated normal shards; its actual
  concurrency contracts use four race shards. E2E and security scanning remain
  separate required contexts.
- The protected `test` context is an aggregator over normal, race, backend
  quality, and frontend quality lanes, so GitHub still fails closed while
  those lanes run concurrently.
- Full serial `go test -p 1 ./...` and a broad sequential sweep of package-local
  control-plane concurrency contracts run in a dedicated workflow on `main`,
  nightly, release tags, and manual dispatch, outside the ordinary PR merge path.
- A release tag is created from the exact protected-main merge. Its independent
  exhaustive sweep does not block `release.sh`, which waits only for the `ci`
  and `release` evidence workflows; release artifacts, SBOMs, signatures, and
  attestations remain tag-gated.
- When a resumed release PR's required checks are already green for the pinned
  head, `release.sh` records that fact and waits only for GitHub to perform the
  protected merge. It does not claim that the remaining merge-state wait is gone.
- Baseline PR #171, exact head `fa8d23b45a53f481e9631dfa1c859ae16d1dd64c`:
  backend vet/full test 26m32s, broad race 7m06s, required `test` job 38m03s.
- Pre-change local default-parallel run at `ec235d7cd03a13d06a02727cf55bad7c29bd89c7`:
  green in 6m50s; `handlers` 409.791s, `db` 96.892s, `cmd/paimos`
  59.046s, `supervision` 59.694s. Concurrent SQLite-using packages passed.
- Post-change local handlers evidence: all 664 tests passed across four isolated
  shards in 129.35s (slowest shard 127.868s); 19 handler concurrency contracts
  passed across four race shards in 101.13s (slowest shard 99.519s).
- The exact candidate's affected normal lane (`go vet` plus the DB change's
  16-package reverse closure, including all handler shards) passed in 219.23s;
  its non-handler long poles were `db` 94.947s and `supervision` 60.510s.
- The final broad package-local concurrency sweep passed in 446.89s. Agent
  Mode's five-second overflow performance budget remains in normal/full serial;
  its non-performance stream concurrency subtests passed under race instead.

The inventory count is reproducible with:

```sh
rg -n '^func (Test|Benchmark|Fuzz)' backend --glob '*_test.go'
```
