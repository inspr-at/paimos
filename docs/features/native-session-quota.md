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

Fix round 6 inspected the failing jobs on PR head `217901899`.
`go-static` job `115149804389` again fails only at strict native test
collection, naming exactly the four unclassified quota cases above. Refreshed
`origin/main` at `fd8fae22f` still lacks those rows. PR #619 remains mergeable,
so no conflict-resolution merge is required. Its quota and C0 package sources
are unchanged since the passing fix-round-5 run on `38df044aa`.

`web` job `115156140924` fails because `web-shard (1)` job `115150348299` was
cancelled during `Run selected UI cases without retries`; its log ends with
`The operation was canceled.` without a reported failing assertion. Catalogue,
setup, all four unit shards and browser shard 2 passed. `tier-measurements`
job `115156166810` reports `coverage=incomplete`, `missing_case_artifact` and
missing evidence for `web-shard-1`, with no failed cases. These are incomplete
browser execution and measurement results requiring coordinator CI reruns,
not evidence for changing quota code or browser assertions. The trusted
review verdict remains coordinator-owned.

Fix round 6 preserves the P4b/R0 ownership boundary: R0 must register the
four existing ESSENTIAL cases in this same PR. No quota behavior, assertion
or shared inventory is changed, so there is no new behavior fix requiring
a regression against `217901899`. The delivery remains incomplete.

Fix round 7 inspected full job logs and the Go JSONL artifact for PR head
`e9a41a6d6`, Actions run `38356096419`. `go-static` job `115159401094` still
fails only at strict tier collection, naming exactly the four quota cases
above. Refreshed `origin/main` at `fd8fae22f` still lacks their rows; the central
source-ownership glob is present. PR #619 remains mergeable, so no
conflict-resolution merge is required. Quota and C0 package sources are
unchanged from the passing fix-round-6 package run.

Current CI independently proves the quota behavior: artifact
`go-test-2-tier-measurements` (`11793350744`) contains a passing result for
each of the four named quota tests and the package (0.133 s). The CI selector
runs these unclassified cases as NIGHTLY; that fallback does not satisfy the
required ESSENTIAL registration or strict manifest check.

`web` job `115166145652` fails because both `web-shard (1)` (`115159941621`)
and `web-shard (2)` (`115159941400`) were cancelled during `Run selected UI
cases without retries`. Their logs report `The operation was canceled.`
without a reported failing assertion. Catalogue, setup and all four web unit
shards passed. `tier-measurements` job `115166182078` reports
`coverage=incomplete`, `missing_case_artifact` and missing evidence for both
browser shards, with zero failed cases in every tier. Coordinator CI reruns
must complete that browser and measurement evidence; review approval remains
coordinator-owned.

Both guarded Go attempts and both guarded static attempts in fix round 7
exited 3 before execution because the global remote slots were full. No local
suite or browser fallback was used. The offline affected planner passed,
selecting 130 Go packages, 80 browser specs and 120 web unit files, but still
omits `internal/nativequota` pending R0 registration. This is an incomplete
handoff, not a green static or merge check. R0 must apply the four exact
ESSENTIAL rows in this same PR and rerun the required checks. No quota
behavior or assertions changed, so no new regression against `e9a41a6d` is
required. All fix-round-7 edits stay inside this feature document.

Fix round 8 inspected both cited jobs and their downstream evidence in Actions
run `38359304112`, bound to PR head `5625f69d3`. Its tested merge commit
`5e9cc6343` has identical quota and C0 package sources. `go-static` job
`115169609169` fails only on the same four unclassified quota tests listed
above. Refreshed `origin/main` at `fd8fae22f` still lacks those rows. Source
ownership, tier selector regressions and release checks passed; R0 must still
register the four ESSENTIAL rows in this same PR. PR #619 is mergeable, so no
conflict-resolution merge is required.

Current CI artifact `go-test-2-tier-measurements` (`11795025184`) proves all
four quota tests and the package passed (0.139 s); the C0 package also passed
(2.429 s). These prove all five P4b acceptance items: qualified stream/account
binding, independent readiness and allowance, unsupported idle probing,
private existing-ingress projections, and clock-driven freshness/isolation.
Their NIGHTLY fallback classification still leaves the strict registration
check red; passing execution is not a substitute for R0 registration.

`web` job `115177937820` fails because `web-shard (1)` (`115170284675`)
was cancelled. Catalogue, setup, all four unit shards and browser shard 2
passed. The cancelled job ran from 06:59:00 to 07:19:28 UTC; its log reports
`The operation was canceled.` at 07:19:16 without a failing assertion, during
case 171 of a 191-case group after a preceding 100-case group. Cancellation
is consistent with its 20-minute job limit in `.github/workflows/ci.yml`;
the coordinator must resolve browser execution/budget evidence outside P4b's
write set. `tier-measurements` (`115178131063`) reports `coverage=incomplete`,
`missing_case_artifact` and only `web-shard-1` missing, with zero failed cases
in each tier. No browser assertion or execution requirement is weakened.

Both guarded Go attempts in fix round 8 exited 3 for the global 2/2 remote
capacity limit. Both guarded static attempts exited 3 because the hold/host
level guard refused execution. No local or browser fallback was used. The
offline affected planner passed (130 Go packages, 80 browser specs, 120 unit
files), but still omits quota pending R0 registration. This round changes only
this handoff document; no behavior fix or regression against `5625f69d` is
needed. Delivery remains incomplete until R0 registration, completed browser
evidence, green required checks and the coordinator's review. Repeating a P4b
fix round cannot repair the R0 manifest or CI workflow ownership boundary.

Fix round 9 inspected both cited jobs and the current-head artifacts in Actions
run `38363598974`, bound to PR head `1881fd959`. Its tested merge commit
`325a689b7` has identical quota and C0 sources. `go-static` job `115184061909`
fails only at strict native collection, naming the four missing ESSENTIAL rows
above. Refreshed `origin/main` at `f571b7765` still contains none of them;
source ownership and release checks pass. PR #619 remains mergeable, so no
conflict-resolution merge is required. The normative plan still reserves
`scripts/ci/go-test-tiers.json` to R0, with no ownership transfer in fix round 9.

Current artifact `go-test-2-tier-measurements` (`11796073998`) proves all four
quota tests and the package passed (0.075 s), and C0 passed (1.611 s). Local
P4b acceptance evidence is:

- PASS: qualified Claude/Codex stream facts, exact binding and separate scopes
  in the qualification, freshness and sparse-denial cases.
- PASS: sign-in, binary pin, host/account slots, allowance and measurement stay
  independent; failed measurement does not invent logout or zero quota.
- PASS: idle Claude probing remains unsupported, even with stream qualification.
- PASS: detached existing-ingress projections exclude private fields and retain
  the exact enrollment binding; production I2 wiring remains a separate slice.
- PASS: injected-clock freshness and exact foreign-account/revision rejection
  retain successful identity facts.

`web` job `115192972759` fails because `web-shard (2)` (`115185141712`)
was cancelled during `Run selected UI cases without retries`. Catalogue,
setup, all four unit shards and browser shard 1 passed. Shard 2 ran from
07:41:44 to 08:02:16 UTC; cancellation at 08:01:59 is consistent with the
workflow's 20-minute job limit. Its completed release group reports 215
expected cases and zero unexpected results; the next group ends during case
84 of 100 without a failing assertion. `tier-measurements` (`115193007937`)
reports incomplete coverage, `missing_case_artifact` and only `web-shard-2`
missing, with zero failed cases in every tier. Coordinator-owned CI execution
and budget evidence must be resolved without weakening browser requirements.

Both fix-round-9 guarded Go attempts exited 3 for remote capacity refusal.
Both guarded static attempts exited 3 at the hold/host-level guard, before
execution; no local fallback was used. The offline affected planner passed
against `f571b7765` (130 Go packages, 80 browser specs, 120 unit files), but
omits quota until R0 registration. No quota behavior, assertion, shared
inventory or CI workflow changes are made, so no new behavior regression
against `1881fd959` is required. This handoff remains incomplete: R0 must apply
the exact four tier rows in this same PR, and the coordinator must complete
browser/static evidence and its separate review before merge. No push,
deployment, review gate or model CLI was run by the builder.

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
- PASS: fix-round-6 single permitted local package run on `217901899`,
  `GOMAXPROCS=2 go test -p 2 -count=1 -v ./internal/nativequota/...`; all four
  named cases and subcases passed (0.261 s). Both preceding guarded remote
  attempts exited 3 for the busy CI pool/global remote capacity limit. No
  vendor account or model call was made; no broader local suite was run.
- PASS: fix-round-7 current-head CI JSONL evidence on `e9a41a6d6`: all four
  quota cases passed, with package result 0.133 s. Stream qualification,
  independent readiness/allowance, unsupported idle probing, private ingress
  and injected-clock account isolation are covered by these named cases.
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
  Fix round 6 reproduced the same counts against `fd8fae22f`, exit 0, and
  again confirmed that `internal/nativequota` is absent from the selection.
  Fix round 7 selected 130 Go packages, 80 browser specs and 120 web unit
  files against `fd8fae22f`, exit 0; `internal/nativequota` remains absent.
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
- INCOMPLETE: both fix-round-6 `ci-static-locked.sh` attempts exited 3 before
  collection because the remote CI pool/global capacity limit was busy.
  Static checks were not run locally. Current PR CI independently identifies
  the same four R0-owned tier omissions; the earlier static pass lines do not
  establish a green check for this round.
- INCOMPLETE: both fix-round-7 `remote-test.sh` attempts and both
  `ci-static-locked.sh` attempts exited 3 for the full global remote slots.
  The current-head CI package pass above is separate evidence, not a green
  guarded static run. Strict Go-tier registration and cancelled browser
  execution/measurement evidence remain unresolved coordinator actions.
- Browser validation does not apply to this source-only slice. Production pin
  qualification, I2 integration, review and release remain coordinator-owned.
