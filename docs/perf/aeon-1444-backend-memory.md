# AEON-1444 · database backend memory

The release 135 Decision Desk person-step projection enlarged an already large
RLS-expanded query plan. On the approved PostgreSQL 18.6 lane, its planner used
41.6 MiB versus 27.9 MiB in release 134. The sampled process retained substantially
more physical memory after planning, although the largest Desk sort used only
28 KiB and its largest hash used 8 KiB. This reproduces the gap between live
PostgreSQL contexts and process memory reported by OPS. The measured cause is
transient planning allocation and retained allocator memory, rather than a large
Desk sort/hash or an unbounded prepared-statement cache.

The fix selects at most 1,001 current-owner candidate identities separately,
rejects coverage above 1,000, then passes the bounded identities into the Desk
projection. The existing HTTP repeatable-read transaction covers both queries.
Each identity includes its project; the final query rechecks project identity,
deletion, active steps and current owner. Tenant RLS, source permissions, result
counts, cursors and the explicit coverage error are retained. No migration,
permission or API change is required.

## Reproduction and measurements

All workloads ran through LEAD's `remote-test.sh` on mbp2606, Darwin arm64,
PostgreSQL 18.6. The restricted app role owns the schema with FORCE RLS and
NOSUPERUSER/NOBYPASSRLS. Fixtures contain a project with 1,500 generated work
leaves, 1 KiB bodies and three active person checks. Each of two pinned app
backends receives 16 requests each for the scoped ticket list and facets,
unscoped nodes, a node detail, kinds, inbox long-poll and Desk. Release 135 also
exercises a populated admission read. Other pool slots remain acquired, so cold
unused backends cannot dilute the samples. JIT is off as in production.

The release 134 comparison transplants only the diagnostic test and its generic
test helpers onto `v261015121011.0.0`
(`fa2a090541222c4ccbd16fe050d00dfb15d83b84`). It runs that release's actual
handlers and embedded migrations, ending at 1354. Migration 1353 was added after
134 shipped; simply applying today's numeric prefix through 1354 would include
the new person-step function and would be an incorrect baseline. Release 135's
product source matches `v261016060843.0.0`, ending at migration 1359.

MiB means 1,048,576 bytes. These are per-process measurements after Desk reads:

| Measurement | 134 backend 0 | 134 backend 1 | 135 original backend 0 | Fixed backend 0 | Fixed backend 1 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Darwin physical footprint, MiB | 126.98 | 134.17 | 167.69 | 129.94 | 126.70 |
| Allocated live contexts, MiB | 25.06 | 24.25 | 27.83 | 30.19 | 29.62 |
| Used live contexts, MiB | 16.64 | 16.04 | 19.57 | 19.66 | 18.98 |
| Cached plan subtrees, MiB | 16.93 | 16.59 | 19.52 | 21.88 | 21.53 |
| Catalog subtrees excluding plans, MiB | 5.72 | 5.56 | 5.88 | 5.88 | 5.72 |
| CacheMemoryContext itself, MiB | 4.12 | 4.06 | 4.12 | 4.12 | 4.06 |
| Prepared statements | 50 | 42 | 51 | 52 | 44 |
| Largest Desk planner memory, KiB | 28,551 | 28,494 | 42,591 | 28,958 | 28,931 |

Fixed physical footprint is within 10% of 134 on both sampled backends; backend
0 is 2.3% above its 134 measurement and 22.5% below original 135. The new candidate
query separately uses 13,212 KiB of planner memory. The extra bounded cached plan
increases live context allocation while reducing process footprint: allocated
context totals alone would miss this regression. After the additional admission
read, fixed footprints are 130.98 and 127.80 MiB; that path adds under 1.1 MiB.
The six new RLS policies change the policy count from 485 to 491, without the
large catalog growth that would explain the process delta.

The scoped list's planner uses approximately 5,628 KiB, its largest sort 107 KiB
and largest hash 2,084 KiB. Kinds' largest planner uses 203 KiB. These paths are
similar across releases and do not reproduce Desk's new allocation jump. The
fixture seeds run the node and admission triggers before opening sampled pools;
this is not a claim that every possible trigger/write workload was profiled.

Memory views run through a disposable database-local SECURITY DEFINER helper
that exposes numeric accounting only. It grants no monitoring role to the app
and does not change the workload's RLS. Snapshots and process probes do not
prepare instrumentation statements. EXPLAIN (ANALYZE, BUFFERS, MEMORY) runs on a
separate diagnostic connection so diagnostics cannot grow the measured backend.
Instrumented execution snapshots wrap the actual list/Desk result and retain
its row while sampling contexts; the wrapper and helper add their own overhead.
For example the fixed diagnostic connection records 14.88–16.97 MiB during the
scoped list, 28.09 MiB during Desk, and a 32 KiB relcache map. The recorded plan
summaries retain numbers, relation names and SQL fingerprints; they omit SQL
predicates, arguments, credentials and response contents.

Darwin physical footprint is not Linux RssAnon or cgroup memory.current. The
production observation was about 64.3 MiB RssAnon per idle client backend, versus
roughly 16 MiB of live contexts. This experiment establishes the query cause and
the controlled release comparison; production RSS/swap recovery after rollout
remains an OPS measurement. Latencies are diagnostic observations, not a timing
gate. No production database or data was accessed.

## Pool retention and sizing

The application already defaults to 16 pooled backends, maximum connection age
15 minutes plus up to 3 minutes of jitter, and 3 minutes idle time. pgx retires
expired connections on release/acquisition and checks idle connections through
its health checker; an acquired transaction is allowed to finish. URL, keyword
and service overrides remain authoritative. These controls were already present
in release 134; adding another identical recycle policy would not fix this
regression. No DISCARD is issued against sessions holding advisory locks or
LISTEN state.

OPS observed a deployment override of 30 pooled connections, with 35 total
backends. At the observed 64.3 MiB/client, 30 equally warm pooled sessions alone
would require about 1,929 MiB; 16 require about 1,029 MiB, returning roughly
900 MiB of headroom. OPS's separately owned nixcfg #999 removes that override.
Capacity planning must reserve shared buffers, maintenance operations and up
to five dedicated listener/admission sessions separately. The app retains the
16-connection default and its two foreground reserve slots. This change does
not alter deployment configuration or claim #999 has been deployed.

## Regression check and validation

`TestBackendMemoryBudget` is registered ESSENTIAL. It requires populated
50-row list pages and three active Desk person steps, samples two backends,
asserts positive live memory within 48 MiB, and requires an actual Desk EXPLAIN
sample with planner memory within 32 MiB. The memory assertion is numeric;
request timeouts only guard hangs. The benchmark adds process accounting and
numeric execution/plan diagnostics.

```sh
/Users/markus/.aeon/lead/aeon/remote-test.sh <worktree> ./internal/cli -run '^TestBackendMemoryBudget$' -count=1
/Users/markus/.aeon/lead/aeon/remote-test.sh <worktree> ./internal/cli -run '^$' -bench '^BenchmarkBackendMemoryRelease$' -benchtime=1x -count=1 -v
```

The fixed benchmark passed on `cfc35e17c6` in 17.861 seconds. The release 134
benchmark passed on the disposable diagnostic branch `7a24a4ba0d`. Final behavior,
negative-control and static-check results are recorded below when available.
