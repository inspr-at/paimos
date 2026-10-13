# Native session quota facts

AEON-1273 / AEON-1259 P4b supplies a local quota collector for an already owned
native session. I2 connects it to agentd and the existing account ingress; this
slice does not enable a production runtime, introduce routes or perform probes.

Sign-in, executable qualification, host slots, account slots, vendor allowance
and the measurement are independent facts. Missing, expired or failed usage is
unknown, never zero usage, zero remaining allowance or evidence of logout.
Only a separate identity check can change the sign-in fact. A vendor denial
can remain known while its utilization is unknown.

## Stream input and qualification

`nativequota.New` receives an immutable local binding, an injected clock and
release-owned stream qualification. The binding includes the frozen C0
`nativecontract.Binding`, local process/connection identity, exact account ID,
the enrollment's `link_revision`, the local profile revision and executable/SDK
pins. Every observation, snapshot, health update and ingress projection checks
that same binding. Resume, restart or account rebind creates a new collector;
old observations never migrate to it.
Native turn and resume-cursor references may change on that same connection;
they are correlation data and do not retarget its account quota binding.

The integrator supplies `Qualification` only from its compiled, release-approved
registry: exact path/digest/version/protocol, exact Claude SDK pin, evidence
digest and qualified Codex bucket IDs. Matching strings and fake fixtures do
not constitute production qualification. No JSON/config qualification loader
or production pin registry is added here. Missing qualification produces
`unsupported` and rejects stream observations.

The `claude-rate-limit/1` codec accepts the existing rate-limit event shape:
`type=rate_limit_event`, `rate_limit_info.status`, `rateLimitType`, `utilization`,
`resetsAt` and allowlisted `unifiedWindows`. Five-hour and seven-day scopes,
including Opus/Sonnet/overage-included scopes, stay separate. Utilization is a
fraction, normalized to a percentage. Rejection without a named window is an
account stop, without an invented percentage.

The `codex-rate-limits/1` codec accepts owned app-server notifications
`account/rateLimits/updated` and `rateLimits/updated`: `rateLimits`,
`rateLimitsByLimitId`, `ordinaryUsageAllowed` and each window's `usedPercent`,
`windowDurationMins` and `resetsAt`. It accepts equal singular/by-ID aliases and
rejects conflicting ones. Arbitrary limit labels are rejected unless present
in compiled qualification. Token-consumption notifications, transcript records
and uncorrelated response objects are excluded.

An optional native session/thread reference must agree with the owned
connection. The owner's source sequence must increase; replay cannot refresh a
reading. Input is bounded before typed decoding to C0's 1 MiB / depth 16, 8,192
values and 256 keys per object. Duplicate keys and case aliases are rejected.
There are at most 32 remembered scopes. Extra vendor fields are discarded;
frames, account labels, plans, paths, headers, authentication and reasoning
never enter collector state or ingress bodies.

## Freshness and allowance

A complete reading requires utilization, a valid duration and a future reset
inside that duration. It stays fresh until the earlier of its reset and ten
minutes after observation. At that exact deadline its `NativeQuota` percentage
becomes null and its state becomes `unknown`; the local measurement says
`stale`. A clock moving behind the observation also makes usage unknown.
Reset passage does not infer replenishment.

Sparse patches never reconstruct a fresh reading from old numeric fields. A
sparse denial records vendor allowance separately and never substitutes 100%
for consumption. Named denials fence only that scope; account denials fence
every scope. Sparse allowance does not recover a retained denial: recovery
requires complete measured evidence and explicit allowance. Named denial
expiry follows its own reset; an unnamed stop remains conservative.

`Failed` records a bounded measurement cause and invalidates numeric readings
until a complete new reading restores its own scope. It preserves health/slot
facts and vendor denials. Binding refusals must not be reported as a failure
of the current account. A same-binding decoder/transport error should be
recorded with `Failed` by I2, with `protocol`/`timeout`/`transport` as appropriate.

`Snapshot.Quota` uses the frozen C0 `NativeQuota` DTO; the remaining snapshot
fields are local diagnostic facts. Numeric usage and allowance are not combined
into invented monetary balance, remaining-token counts or admission authority.
Existing account/host admission remains authoritative.

## I2 ingress and privacy handoff

1. Observe quota events from the existing owned reader under its immutable
   account/profile/runtime identity and source sequence. Update readiness only
   from the existing independent sign-in, pin and occupancy observations.
2. Send `Readings(binding)` to the existing
   `POST /api/agent-accounts/{accountId}/readings`. Its detached, bounded body
   contains `source=agentd`, no run/phase/plan, `usage_probe=true` and the exact
   **enrollment** binding revision. This selects existing current enrollment,
   registering-principal and owner-consent checks; local profile revision is
   not substituted for enrollment revision. Empty/stale/failed readings return
   an error, never a successful empty or zero snapshot.
3. Send `ProbeFailure(binding)` to the existing account `/probe` ingress when
   a measurement fails. Its `measurement_only=true` report preserves the
   ordinary health probe, uses the exact daemon/enrollment generation and
   supplies an honest readiness failure without fabricated facts. A completed
   fresh measurement prevents replay of an earlier check failure.
4. For a sparse denial with no numeric reading, map the snapshot's explicit
   allowance into existing `ReadinessFactWrite` under the current subscription
   readiness-resource membership: null utilization/reading time, `vendor_denied`
   and `named_reset` or `unnamed`. Preserve its original allowance observation
   time and known reset; never infer a resource or pooled account identity.
   I2 owns this resource mapping and existing server recovery behavior.
5. Keep the existing paired proof, `account.probe`, final transaction permission
   rechecks, consent and accountprivacy/native-session owner visibility. A local
   snapshot is not public authority. Do not broadcast account details to other
   readers or attach source frames to durable events. Check transport results;
   rejected or partial writes remain failures.

No statusline, transcript scrape, OAuth/private endpoint, auth-file parser,
credential copying or account pooling is introduced. Sessions use the
workspace owner's own enrolled vendor account. Idle Claude `get_usage` remains
`unsupported`, even with stream qualification. There is no process/network/
filesystem probe callback in this package. Enabling any future idle capability
requires separately compiled production pin and quota-neutral exchange evidence.

## Coordinator R0 registration

Audit slice `S2` (`agent-runtime`) already covers `internal/nativequota/**`
through the central `internal/native*/**` glob merged in PR #623 and included
in this branch at `38df044aa`. Before merge, in this same PR, R0 must add these
exact rows to `tests` in `scripts/ci/go-test-tiers.json`:

```json
[
  {"kind":"go","package":"internal/nativequota","name":"TestQuotaIngressKeepsPrivateFieldsOutAndCopiesFacts","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativequota","name":"TestQuotaRejectsUnqualifiedAndMalformedFrames","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativequota","name":"TestSparseQuotaDenialPreservesConsumptionAndScope","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativequota","name":"TestStreamQuotaFreshnessAndAccountIsolation","tier":"ESSENTIAL"}
]
```

The source files are `binding.go`, `collector.go`, `decode.go`, `ingress.go`,
`json.go` and `quota_test.go`, all inside `internal/nativequota`. There are no
browser/shard entries, API routes, permission changes, SQL/DSAR changes or new
tenant/tree-lock callers. Builders do not edit R0's shared inventories.

Fix round 3 verified these same omissions in PR #619 at `4a5e40209`:
`go-static` reports the four unclassified cases; `release-check-run` reports
the six unassigned files and the resulting audit CLI failure. Both repairs
require the coordinator's R0 lane under the normative plan's exclusive write
sets. No quota behavior or assertion change is needed for these failures.
This handoff remains incomplete until R0 applies both additions and the
required checks pass. The policy preview separately awaits the coordinator's
trusted review verdict.

Fix round 4 inspected the full failing-job logs for the same PR head,
`4ab38db03`. `go-static` still names exactly the four unclassified cases above;
`release-check-run` still names exactly the six unassigned source files. The
refreshed `origin/main` at `06929d1a4` contains neither repair. The central audit
repair is open PR #623 (`work/aeon-1259-r0-audit`), which changes only
`scripts/audit/slices.json`; it does not supply the four quota tier rows. R0
must apply both registrations before this PR can pass. PR #619 is mergeable,
so no conflict-resolution merge is required in this round.

The separate `web` aggregate failure is caused by cancelled `web-shard (1)`
and `web-shard (2)` executions, both during `Run selected UI cases without
retries`. Web catalogue, setup and all four unit shards passed. Cancelled
browser runs remain incomplete and need coordinator CI reruns; no browser
behavior failure is identified by the cited findings. `gate/policy-preview`
reports no trusted verdict for this head, which remains coordinator-owned.
`tier-measurements` reports `coverage=incomplete`, `missing_case_artifact` and
missing evidence for those same two cancelled browser shards, with no failed
cases in its report.
No quota implementation or assertion was changed in fix round 4, so there is
no new behavior fix requiring a negative-control regression.

Fix round 5 inspected the full failing `go-static` job `115140473377` for
PR head `38df044aa`. Only `Validate every Go tier against native collection`
failed: it names exactly the four unclassified quota cases above. The audit
repair is now included and `release-check-run` passed. Refreshed `origin/main`
at `bf4aa778f` still contains none of the four quota tier rows. PR #619 is
mergeable, so no conflict-resolution merge is required.

All current Go execution shards, web catalogue/setup/unit/browser checks,
release checks, e2e and tier measurements passed on that PR head. The `go`
aggregate fails because of `go-static`; `gate/policy-preview` separately
awaits the coordinator's trusted review verdict. No review gate was run by
the builder.

The remaining repair is entirely in R0's exclusive metadata write set. P4b
owns only `internal/nativequota/**` and this feature document, with no
ownership transfer for the tier manifest. The coordinator must apply the
four rows above; this round does not change quota behavior, test assertions
or shared inventories, and requires no new behavior regression. The handoff
remains incomplete until those registrations and the required checks pass.

Run the named package through the guarded remote Go runner and static checks
through `ci-static-locked.sh`; run `node scripts/test-tiers/affected.mjs --base
origin/main`. These clock-driven tests use synthetic pins and events, with no
vendor account/model call. Authoritative PR CI and coordinator qualification/
review remain required before integration or release.

## Builder validation (2026-10-13)

- PASS: initial guarded remote Go run of `./internal/nativequota/...` on
  `fce23905b`; all four named cases and their subcases passed (0.250 s).
- PASS: final-source guarded remote Go run on `05dbc1069`; all four named
  cases passed, including native turn/cursor changes (0.353 s).
- PASS: fix-round-3 guarded remote Go run of `./internal/nativequota/...` on
  `4a5e40209`; all existing quota cases passed (0.297 s).
- PASS: fix-round-4 guarded remote Go run of `./internal/nativequota/...` on
  `4ab38db03`; all existing quota cases passed (0.324 s).
- PASS: fix-round-5 guarded remote Go run of `./internal/nativequota/...` on
  `38df044aa` with `-count=1 -v`; all four named cases and subcases passed
  (0.246 s), without vendor account or model calls.
- PASS: negative control on a temporary branch from that final source, with
  the freshness deadline check disabled. The named clock test failed on its
  exact stale-reading assertion for both Claude and Codex (exit 1). After
  guarded remote refusal for load 18 > 12, this was one permitted local
  `GOMAXPROCS=2 go test -p 2` package run. The delivery branch was restored and
  both worker-created temporary refs removed; no mutant commit is in delivery
  history.
- PASS: affected planner against refreshed `origin/main`, exit 0,
  `mode=essential`, `layout=full`; selected 128 Go packages, 80 browser specs and
  120 web unit files from the current manifests. `nativequota` awaits R0 rows.
  Fix round 4 reproduced this selection against `06929d1a4` and confirmed that
  `internal/nativequota` is absent until its tier rows are registered.
  Fix round 5 reproduced the same counts against `bf4aa778f`, exit 0, and
  confirmed that `internal/nativequota` remains absent from the selection.
- INCOMPLETE: fix-round-3 guarded static `--merge-main` on `4a5e40209` exited 1.
  Its 39 other checks passed with no skips, including Go vet, test-tier tests,
  web-shard tests, web typecheck and web lint. Only Go-tier drift and source
  ownership/audit coverage failed for the four cases and six files listed
  above. R0 must apply those rows and rerun static in this same PR; this is
  not a green merge gate.
- INCOMPLETE: fix-round-4 guarded static `--merge-main` on `4ab38db03` exited 1.
  Again, 39 checks passed with no skips, including Go vet, test-tier tests,
  audit tooling's other cases, web-shard tests, web typecheck and web lint.
  Only `go-tiers`, `audit-tests` and `audit-coverage` failed, for the same four
  cases and six files. The pending central audit PR #623 was still open after
  this run. No shared-inventory, quota behavior or assertion edits were made.
- INCOMPLETE: fix-round-5 guarded static `--merge-main` on `38df044aa` exited 1.
  Its 41 other checks passed with no skips, including Go vet, tier-selector
  regressions, audit tests/coverage, web-shard tests, web typecheck and web
  lint. Only `go-tiers` failed, naming exactly the four missing quota rows
  above. The central source-ownership repair is verified; tier registration
  remains coordinator-owned. This is not a green merge gate.
- Browser validation does not apply to this source-only slice. Production pin
  qualification, I2 integration, review and release remain coordinator-owned.
