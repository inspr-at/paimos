# Idle database work (AEON-1114, AEON-1115)

AITHEMA host projection writes its cursor only when the tenant event high water
advances. An unchanged high water also avoids the filtered event-tail query.
The existing authorization checks, transactional cursor/outbox coupling,
100-event pages, callback leases and due-work claims remain in place.

Background embedding configuration is cached per tenant for 30 seconds, with
at most 256 resident entries. Refresh checks the provider identity and revision;
an unchanged revision reuses configuration and encrypted credential bytes.
Successful settings saves invalidate the local entry after commit. Other
replicas observe changes at the refresh boundary. A refresh has a five-second
deadline and never serves an expired entry after an error. Decrypted credentials
are not retained in this cache or written to logs or artifacts. Request contexts
and retained transactions read configuration directly, preserving request
authorization guards and preventing uncommitted settings from entering the
worker cache. Chat and parent-benefit consent reads remain fresh.

Heartbeat warning counters increment only through the three-beat grace period
and reset only when needed. Repeated valid reports and a saturated counter do
not produce another counter write. Reporter projection keeps ETA freshness,
pause deadlines, lineage and batched coordinator progress; it omits calculation
of move rights that the reporter response already discards. Session lists retain
their existing move-rights batching. ETA settings are read only when an active
session has an estimate timestamp. Generation and lease checks, heartbeat
cadences, required response fields and the existing event policy are unchanged.

## Measured workload

On 2026-10-23, the existing
`TestNativeHostEventsCursorAndTenantScope/idle_minute` test executed the same
schedule against disposable remote test Postgres: one tenant, one idle AITHEMA
host, one idle unbound harness worker, enabled embeddings and empty queues.
After warming the session's grace counter and delivering initial callbacks,
the measurement ran 60 host passes, six reconciliations, 30 embedding passes
and four accepted heartbeats. An injected configuration clock advanced one
second per pass and exercised the 30-second refresh boundary.

This is a replay of one minute's scheduled work, not a wall-clock production
sample. A pgx tracer counts real client statements and affected rows without
recording arguments. Counts include tenant setup, transaction control, host and
embedding tenant discovery, and endpoint SQL. Fixture setup, warm-up, assertion
queries, outer authentication middleware and unrelated server workers are
excluded. Baseline commit `3fdbd8049` ran on agm2; fixed commit `034b9d91f` ran
on mbp2606 after agm2's capacity guard selected the approved fallback. These are
statement counts, not latency or cross-host performance comparisons.

| Per replayed minute | Before | After |
| --- | ---: | ---: |
| All SQL statements | 2,814 | 2,490 |
| SQL excluding transaction control | 2,286 | 2,018 |
| Host cursor UPDATE statements / rows | 60 / 60 | 0 / 0 |
| Provider configuration reads | 30 | 2 |
| Warning-counter UPDATE statements / rows | 4 / 4 | 0 / 0 |
| Due outbox claims | 60 | 60 |
| Due embedding claims | 30 | 30 |
| Accepted heartbeats | 4 | 4 |

The replay removes 324 statements (11.5%) and 64 unchanged row writes. The
remaining outbox and embedding claim checks are deliberately retained.

## Regression evidence

Existing registered Go tests were extended; no tier manifest changes or new
top-level test names are required. The old production code failed the counted
assertions: 30 provider reads instead of one during 30 unchanged resolutions;
seven counter writes instead of three during seven missing-progress reports;
two resets instead of one during repeated valid reports; seven unnecessary ETA
setting reads; and the replay's unchanged cursor and saturated counter writes.
The fixed code passes these assertions.

Injected-clock provider checks also cover local enable/disable invalidation,
credential and model rotation, another replica's exact expiry boundary,
revision-only refresh, tenant separation, refresh failure/recovery, request
revocation guards and transaction rollback. Existing generation, warning,
activity, progress, heartbeat-event and reporter-contract tests remain active.

The complete host, modelprovider, harness, embedding, dbtest and reportercontract
packages passed through `remote-test.sh` on agm2. The final clock-driven replay
also passed through that entry point on mbp2606. Static validation remains a
coordinator/hosted-CI handover: `ci-static-remote.sh` returned exit 3 with
`remote static check paused at night (night protocol: mbp2606 is shared with the CI pool); rely on the PR's hosted CI static job`.
No local heavy fallback or review gate was run.
