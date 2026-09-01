# Backend test inventory

PAI-852 audited the backend corpus before changing CI selection. At the exact
audit commit `8f540071c27608b38f7d8cca6c4ac001a7dc1ad9`, the repository contained
1,794 top-level test or fuzz functions
in 268 `_test.go` files (267 contain test/fuzz functions and one is the shared
handlers helper): 1,789 `Test*` functions, five `Fuzz*` functions, and no
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

Every source-level test at that point-in-time PAI-852 audit commit is accounted
for by its package group below. Later additions are intentionally described in
the execution-policy updates rather than retroactively changing this ledger.

| Package group | Files | Tests/fuzz | Class | Evidence for the decision |
| --- | ---: | ---: | --- | --- |
| `backend/handlers` | 123 | 664 | Keep | HTTP, authorization, privacy, untrusted input, SQLite concurrency, removed-route, and customer contract coverage; the runtime hotspot is per-test migration setup. |
| `backend/cmd/paimos` | 45 | 428 | Keep | CLI parsing, attribution, durable inbox, runner control, and release-facing behavior; many tests are the only shell-to-API contract proof. |
| `backend/db` | 12 | 138 | Keep | Migrations, schema guards, rollback/precondition behavior, busy/lock behavior, and the explicit isolated test-DB path guard. |
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
| **Total** | **268** | **1,794** | **Keep: 1,794** | **Fold: 0; drop: 0.** |

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

- Pull requests run independent required lanes for `go vet ./...`, two affected
  normal-package shards, four DB shards, five handler shards, the unchanged Agent
  Mode five-second performance contract, and directly changed race targets.
  Selection for normal tests is the directly changed package plus its bounded
  transitive reverse test-dependency closure. The affected lane excludes only
  the DB/handler shards and isolated performance duplicate; it still runs the
  parent stream test's other subtests.
- DB, handler, managed-harness, and other directly changed race targets use
  separate required runners. Generic affected race packages are distributed
  exactly once across four matrix runners. The original two M147 SQLite
  arbitration tests retain 32 simultaneous application goroutines and 32 open
  connections in the normal DB plan. Companion variants exercise the same
  exactly-one-winner oracles under `-race`, each in its own process, with the
  production ten-connection pool and five-second busy timeout. The 664 handler
  normal tests and 19 handler concurrency contracts
  each use five independently provisioned matrix runners, after hosted timing
  showed four left too little margin. The PR lanes reject an unindexed local
  multi-process invocation.
  Managed-harness PR race instrumentation is likewise limited to its four true
  concurrent, stop-versus-control, and crash/replay contracts because all 17
  package tests independently rebuild the full SQLite migration chain. Each
  oracle owns one independently provisioned PR matrix runner. A new package
  concurrency/recovery oracle must update the semantic selector and shard
  topology in the same change; the gate pins the currently selected names
  exactly once. Normal PR selection and the exhaustive full-serial workflow
  still run all 17; the broad main, nightly, and manually authorized race
  workflow retains the same complete set of four package-local concurrency and
  recovery oracles in four sequential foreground processes.
  E2E and security scanning remain separate required contexts.
- The protected `test` context is an aggregator over every vet, normal, race,
  performance, backend quality, and frontend quality lane, so GitHub still
  fails closed while those lanes run concurrently.
- Full serial `go test -p 1 ./...` and a broad sequential sweep of package-local
  control-plane concurrency contracts (including sequential handler shards) run
  as parallel jobs in a dedicated workflow on `main`, nightly, and manual
  dispatch, outside the ordinary PR merge path. Main and tag image paths gate
  on a compact backend/platform job, including executable unsupported-platform
  denial contracts, and require a successful `backend-full.yml` run for their
  exact head before the stable `test` context can permit publication. A tag can
  reuse the already-green exhaustive result from the identical protected-main
  head instead of starting the serial suite again. For hosted pre-merge timing
  evidence, an operator can apply the explicit `backend-full-evidence` PR label;
  other PR events and labels cannot authorize the exhaustive jobs.
- A release tag is created from the exact protected-main merge only after that
  head has a successful exhaustive workflow result. The tag's `ci` workflow
  reuses the already-green exact-head result; no duplicate exhaustive tag run is
  triggered. `release.sh` then waits only for the `ci` and `release`
  evidence workflows. Release artifacts, SBOMs, signatures, and attestations
  remain tag-gated.
- When a resumed release PR's required checks are already green for the pinned
  head, `release.sh` records that fact and waits only for GitHub to perform the
  protected merge. It does not claim that the remaining merge-state wait is gone.
- Baseline PR #171, exact head `fa8d23b45a53f481e9631dfa1c859ae16d1dd64c`:
  backend vet/full test 26m32s, broad race 7m06s, required `test` job 38m03s.
- Pre-change local default-parallel run at `ec235d7cd03a13d06a02727cf55bad7c29bd89c7`:
  green in 6m50s; `handlers` 409.791s, `db` 96.892s, `cmd/paimos`
  59.046s, `supervision` 59.694s. Concurrent SQLite-using packages passed.
- Final local handler evidence under simultaneous load: all 664 tests passed
  exactly once across five isolated shards, with wall times 88.24s–93.45s; all
  19 handler concurrency contracts passed exactly once across five race shards
  with `GOMAXPROCS=2`, with wall times 67.08s–88.10s.
- The first hosted candidate correctly disproved the original combined lane:
  vet took about 53s, `db` took 284.400s under package contention, and the
  unchanged Agent Mode five-second SLO measured 6.510s; the job failed in
  6m42s. Its combined DB race also exposed `SQLITE_BUSY` in both preserved M147
  32-writer proofs on the two-core runner.
- The corrected local lanes passed concurrently: the 14 non-special reverse
  dependents in 71.37s, all 136 DB tests in four shards in 39.39s, and all 664
  handler tests in four shards in 139.44s. The unchanged Agent Mode performance
  contract passed alone in 2.37s. The exact pre-fix hosted topology
  (`GOMAXPROCS=2`, 32 writers, `-race`) reproduced `SQLITE_BUSY` locally in
  46.98s; retaining 32 writers while restoring the production pool bound made
  the same mutation proof pass in 57.85s. Split DB and handler race lanes then
  passed under simultaneous local load in 85.85s and 115.20s respectively.
- The second hosted candidate proved every non-handler backend lane below five
  minutes: affected reverse dependents 4m17s, DB race 3m42s, DB normal 1m48s,
  security invariants 1m56s, vet 1m02s, and isolated performance 28s. It also
  disproved four handler processes sharing one two-core runner: normal took
  6m40s and race failed at 8m59s after resource contention produced a 500 and
  `SQLITE_BUSY`. The final matrix gives each unchanged shard its own runner;
  both failed-under-contention race tests passed alone with `GOMAXPROCS=2` in
  24.60s and 22.81s.
- The third hosted candidate was fully green and reduced normal handler shards
  to 3m26s–4m45s and race shards to 3m54s–5m01s. The affected lane took 4m26s,
  DB normal 2m08s, and DB race 3m37s. Because the slowest four-way race shard
  still missed the hard target by one second, the final race partition uses
  five independently provisioned runners while preserving an exact-once
  accounting guard over the same 19 contracts.
- The fourth hosted candidate, exact head `703293a423e8ad398243c197d031c9b3a2e8c6a5`,
  measured `quality` at 3m46s and `frontend-quality` at 2m14s. Every individual
  backend lane was below five minutes, but the stable required `test` context
  completed about 5m16s after workflow start because the 4m45s affected job
  started later. The replacement therefore divides affected packages across two
  required runners; final exact-head hosted timing remains the acceptance proof.
- The final full serial suite passed in 722.08s, including unsharded `handlers`
  in 413.893s, `db` in 92.098s, and `supervision` in 55.184s. This is
  the exact assurance retained on main, nightly, and manual dispatch before a
  release tag can be created.
- The final broad package-local concurrency sweep passed in 687.32s. Its five
  handler shards ran foreground-only in 80.149s, 75.453s, 61.048s, 61.892s, and
  58.120s; the three isolated DB race processes passed in 17.378s, 19.584s, and
  36.064s. Agent Mode's five-second overflow performance budget remains in
  normal/full serial; its non-performance stream concurrency subtests passed
  under race instead.

The inventory count is reproducible with:

```sh
rg -n '^func (Test|Benchmark|Fuzz)' backend --glob '*_test.go'
```
