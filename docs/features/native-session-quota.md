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

Before merge, in this same PR, add `internal/nativequota/**` to audit slice
`S2` (`agent-runtime`) and register these Go cases in
`scripts/ci/go-test-tiers.json` with `package=internal/nativequota`,
`kind=go`, `tier=ESSENTIAL`:

- `TestStreamQuotaFreshnessAndAccountIsolation`
- `TestSparseQuotaDenialPreservesConsumptionAndScope`
- `TestQuotaIngressKeepsPrivateFieldsOutAndCopiesFacts`
- `TestQuotaRejectsUnqualifiedAndMalformedFrames`

The source files are `binding.go`, `collector.go`, `decode.go`, `ingress.go`,
`json.go` and `quota_test.go`, all inside `internal/nativequota`. There are no
browser/shard entries, API routes, permission changes, SQL/DSAR changes or new
tenant/tree-lock callers. Builders do not edit R0's shared inventories.

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
- INCOMPLETE: guarded static `--merge-main` exited 1. Its 39 other checks passed
  with no skips, including Go vet. Only Go-tier drift and source ownership/audit
  coverage failed for the four cases and six files listed above. R0 must apply
  those rows and rerun static in this same PR; this is not a green merge gate.
- Browser validation does not apply to this source-only slice. Production pin
  qualification, I2 integration, review and release remain coordinator-owned.
