<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Structured terminal attach

AEON-1269 / AEON-1259 P2b provides the owner-socket client and canonical command
node for `aeon session attach OWNED_ID --project KEY`. This source slice is
groundwork: I1 must register the node and wire a qualified native host before
the command is available in the installed CLI. It does not enable a provider,
start a process, migrate LEAD, or change any vendor account or binary.

Viewing is read-only by default. `--control` explicitly requests the single
controller lease; a conflict is reported without force-taking or silent
downgrade. `--input-mode now|after_turn` chooses input timing. The server remains
the authority for current grants, permissions, controller ownership and the
five-second heartbeat / thirty-second grace. The client subtracts request time
from `expires_in_ms`; wall-clock `expires_at` is never authority.

The client resolves the authorized project/session projection before opening
the protected Unix socket. Each request carries C0's protocol header and each
mutation its strict envelope. Viewer IDs and control capabilities are headers,
never URL parameters. Socket ownership and the existing private token store
are reused; no native vendor credentials are accessed or copied.

The structured tail renders message deltas by message ID, explicit finals,
paired tool status and input receipts. Accepted or queued input is displayed
as that state, not delivery. Uncertain input is never automatically resent.
Rendering strips terminal controls and bidi formatting. The tail retains at
most 64 rows and 256 KiB in RAM; frames, composer input, response bodies and
queues are bounded. This is a transient tail, not durable transcript history.
Text omitted at the per-message display bound is marked explicitly.
`Session.Receipts` exposes bounded pages, their next cursor and truncation.

Every action and stream frame uses the original full binding and generation.
A successor requires a new explicit attach. Protocol, correlation, lease and
generation failures close local authority. The stream never automatically
reconnects across a generation, and a gap or disconnect reports lost live
continuity. The server must separately recheck access on every frame/keepalive.

On an interactive terminal, `a` enters the composer. Enter adds a newline.
Submit uses `⌘↵` on macOS and `Ctrl+↵` elsewhere when the terminal supports
CSI-u or modifyOtherKeys. The portable alternative is Esc followed by `s`.
Esc first leaves input, then detaches on a second press. `i` is an explicit
interrupt action outside input, fenced to a known active turn. Single letters
remain text inside input, paste cannot trigger actions, and other modified
keyboard shortcuts do not trigger native control. Ctrl+C, Ctrl+D, EOF and
termination signals detach the viewer. Detach never stops the owned process
or releases its runtime/account occupancy.

The local renderer redraws after a terminal resize without sending PTY resize
or input. Raw mode, bracketed paste and keyboard reporting are restored before
bounded viewer detach, including error and cancellation paths. A piped stdin
is supported for read-only output; control requires an interactive terminal.

## Integration handoff

I1 owns `internal/cli/compat_cmds.go` after the core handoff. Append
`rt.cmdSessionAttach()` to `cmdSession().subs` and update that command's help.
This slice deliberately leaves the reserved shared file untouched. Use the
same node's handler/options for any later additive
`aeon attach --session OWNED_ID --project KEY` alias in I1/I2; keep
`aeon attach ISSUE FILE` and `attach list|get|rm` unchanged. There is no
ambiguous positional session alias. P2a supplies the qualified local routes
and I1 connects the host; fake-socket evidence is not live qualification.

R0 must apply the remaining test registrations to this branch in the same PR
before final CI/merge. Its metadata files are outside P2b's exclusive write set.

- Source ownership is covered by S2's `internal/native*/**` from central
  [R0 PR #623](https://github.com/inspr-at/paimos/pull/623), now merged into
  this branch. Both `internal/cli/native_attach.go` and its test match
  S3's `internal/cli/**` coverage. No further audit row is required.
- Go tier `ESSENTIAL`: `internal/nativeattach:TestFakeSocketDetachViewerAndStaleBinding`,
  `internal/nativeattach:TestAttachLeaseUsesElapsedServerBudget`,
  `internal/nativeattach:TestTerminalRestoredOnEveryExit`,
  `internal/nativeattach:TestComposerReceiptsAndEscapeLeaveSessionRunning`,
  `internal/nativeattach:TestStreamRejectsSuccessorBeforeRendering`,
  `internal/nativeattach:TestComposerPreservesShortcutsAndBoundsTerminalData`,
  `internal/cli:TestNativeAttachCommandPreservesAttachmentMeaning`,
  `internal/cli:TestNativeAttachCommandValidation`, and
  `internal/cli:TestNativeAttachRejectsMismatchedProjection`.
- No new routes, permissions, migrations, DSAR fields, web shards or
  tenant/tree lock callers. C0's API and local route inventories stay intact.

Run the committed source through the approved remote runner for
`./internal/nativeattach/... ./internal/cli/...`; then guarded static
`--merge-main` and the affected-set planner. Browser checks do not apply.
The coordinator owns R0, cross-family gates, push, release and live verification.

## Builder acceptance and validation

2026-10-13, source slice only:

- PASS: canonical command node, structured tail, honest input receipts and
  explicit exclusive control lease are implemented. Command-root registration
  and qualified host wiring remain I1-owned, as specified above.
- PASS: the additive `attach --session` alias is handed to I1/I2; the existing
  upload command and attachment subcommands retain their meaning.
- PASS: EOF, signal cancellation, raw-entry/output/input/resize errors and
  lease refusal restore the terminal before detaching. The fake host stays
  running; no process stop or PTY resize is sent.
- PASS: bare and CSI-u Esc leave input before detaching; quick double Esc is
  handled. Only deliberate interrupt targets a known turn. Submit labels and
  modified-Enter handling match the platform; paste cannot trigger actions.
- PASS: fake Unix-socket scenarios prove read-only clients send no input,
  multiple viewers coexist with one controller, controller expiry preserves
  viewers, and stale input/stream bindings never target a successor.

The complete `internal/nativeattach` package passes in the permitted one-package
local fallback after remote refusal. The new CLI tests plus the existing
`TestAttachmentGetCLIEndToEnd` regression pass in a separate small CLI run.
The negative control uses an unreferenced stash object: removing the viewer
fence makes the exact read-only assertion fail with `grant_missing`. Original
source and HEAD were restored; no throwaway branch commit or stored stash was
left behind. Tests use fake sockets, injected clocks and barriers, without
native vendor/model calls. `git diff --check` passes.

INCOMPLETE: repeated `remote-test.sh` and `ci-static-locked.sh` attempts exit 3
because both guarded remote slots are occupied. Neither full Go validation nor
static `--merge-main` is green. The affected planner exits 0 and selects FULL
Go: 3,219 reverse-dependency cases exceed its 300-case threshold. R0 must apply
the listed source/tier rows in this same PR, then the coordinator must obtain
authoritative full Go/static CI before merge. Browser checks do not apply to
this slice. No push, deployment, review gate or live cutover was performed.

## Fix round 2: CI metadata ownership blocker

The CI report for PR #618 at `a54ddd2b` contains metadata failures rather
than a native attach behaviour finding:

- `go-static` job 115096247775 fails strict collection because all nine tests
  listed in the integration handoff are absent from
  `scripts/ci/go-test-tiers.json`. R0 must register them as `ESSENTIAL`.
- `release-check-run` job 115096247804 fails
  `test_current_snapshot_has_no_unassigned_code` for all nine tracked files
  in `internal/nativeattach/`. R0 must add `internal/nativeattach/**` to S2's
  paths in `scripts/audit/slices.json`. The delta CLI test also fails because
  `covcheck.py` intentionally requires whole-tree assignment even for an empty
  `--since HEAD` delta; it has the same root cause.

P2b's normative plan reserves both metadata files exclusively to R0 and
requires an explicitly assigned metadata integrator. The fix-round brief does
not transfer that ownership. This builder therefore stops before editing
those files, as required by the original brief. The exact ownership glob and
nine test identities above are ready for the coordinator's same-PR integration.
The CI failures remain open; no behaviour, test assertion, permission, contract,
version, or other slice's file was changed in this round.

Validation of the unchanged source at `a54ddd2b` through the approved remote
runner: `./internal/nativeattach/...` passes. The initial combined package run
exits 1 in `internal/cli`; its failure detail is unavailable in the truncated
output. An immediate full `./internal/cli/...` rerun with Go JSON diagnostics
passes (38.111 seconds), without a source or fixture change. This is reported
as a failed first run followed by a passing rerun, not as a diagnosed fix.
Browser/web checks do not apply to this non-browser slice. The existing CI
strict-tier and audit tests already fail at the requested negative-control
revision, so neither assertion needs to be weakened or replaced.

The guarded `ci-static-locked.sh` run completes with exit 1: `go-tiers`,
`audit-tests`, and `audit-coverage` reproduce exactly the missing registrations
above; the other 39 checks pass, with zero optional skips. This includes passing
test-tier and web-shard self-checks, Go vet, web typecheck/lint, and migration
checks. Static validation is failed, not incomplete or green. R0 must apply the
two metadata changes in this PR and rerun the strict checks before merge. The
non-required `gate/policy-preview` CI failure reports a missing trusted review
verdict; that verdict remains coordinator-owned. No push, deployment, model
call, or review gate was performed by this builder.

## Fix round 3: R0 integration still required

The new CI report `review-ci618-0500.out` reviews `dc50cb3c`; direct inspection
of that revision's job logs confirms the same two unresolved metadata changes:

- [Go static job 115102655677](https://github.com/inspr-at/paimos/actions/runs/38337981184/job/115102655677)
  fails strict tier collection on exactly the nine P2b test identities listed
  above. The test-tier self-check itself reports zero failures. R0 must add
  those nine `ESSENTIAL` rows to `scripts/ci/go-test-tiers.json`.
- [Release check job 115102655792](https://github.com/inspr-at/paimos/actions/runs/38337981184/job/115102655792)
  fails both existing audit assertions on the nine tracked files under
  `internal/nativeattach/`. R0 must append only `internal/nativeattach/**` to
  S2's `paths` in `scripts/audit/slices.json`. CLI ownership is already covered.

These assertions already fail at the required negative-control revision
`dc50cb3c`. There is no new behaviour finding or behaviour change in this round;
the existing assertions remain intact. All seven Go test shards, four web unit
shards, twelve web browser shards, migration compatibility and E2E checks are
green in that CI run. This does not make strict static or release checks green,
and the unregistered P2b tests still need explicit remote validation.

The current ticket and normative plan still reserve both metadata files to R0;
fix round 3 contains no ownership transfer. The original brief requires
"Write ONLY inside your exclusive_write_set" and "Never touch another slice's
files." The coordinator must apply the exact two metadata changes above to
this branch in the same PR, then obtain green strict static/release checks.
No metadata or source file outside P2b's write set was changed. PR #618 reports
`MERGEABLE`, so the conditional main-merge instruction does not apply.

This round's first combined remote Go run and guarded static check both refused
with exit 3 because both remote slots were occupied. The combined Go retry also
refused with exit 3, this time for load (13 above the limit of 12). The permitted
single-package local fallback then passed: `GOMAXPROCS=2 go test -p 2
./internal/nativeattach/...` exits 0 (0.540 seconds). Full CLI and guarded static
validation remain incomplete; no full local fallback was run. `git diff
--check` passes. Web/browser checks have no affected P2b cases. The separate
non-required policy preview still reports a missing trusted review verdict;
the coordinator owns that gate after builder exit. No push, deployment, model
CLI, review gate or live cutover was performed.

## Fix round 4: prepared R0 registration patch

At the requested negative-control revision `facb16772`,
[Go static job 115109204348](https://github.com/inspr-at/paimos/actions/runs/38340153694/job/115109204348)
still rejects exactly the nine test identities above, and
[release check job 115109204383](https://github.com/inspr-at/paimos/actions/runs/38340153694/job/115109204383)
still fails the two existing audit assertions on the same nine nativeattach
files. No assertion or behaviour change is needed. PR #618 is `MERGEABLE`.

The normative plan still reserves both files to R0, whose owner is the
"Coordinator or one explicitly assigned metadata integrator". This round's
brief does not assign P2b that role, and the original brief requires stopping
before editing another slice's files. The following additive patch is prepared
for R0 to apply in this PR. `git apply --check` passes against `facb16772`;
the proposed JSON parses and preserves every existing test row. The actual
metadata files remain unchanged, so the CI blockers remain open.

```diff
--- a/scripts/audit/slices.json
+++ b/scripts/audit/slices.json
@@ -113,6 +113,7 @@
       "title": "Agent runtime, harnesses and runs",
       "paths": [
         "internal/agentd/**",
+        "internal/nativeattach/**",
         "internal/nativecontract/**",
         "internal/nativesessions/**",
         "internal/agentd/routine_budget.go",
--- a/scripts/ci/go-test-tiers.json
+++ b/scripts/ci/go-test-tiers.json
@@ -4906,6 +4906,12 @@
     ,
     {"kind":"go","package":"internal/cli","name":"TestModelTicketResolveAndReadOnlyPrefs","tier":"GATED-FULL"}
     ,
+    {"kind":"go","package":"internal/cli","name":"TestNativeAttachCommandPreservesAttachmentMeaning","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/cli","name":"TestNativeAttachCommandValidation","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/cli","name":"TestNativeAttachRejectsMismatchedProjection","tier":"ESSENTIAL"}
+    ,
     {"kind":"go","package":"internal/cli","name":"TestOnboardCompatTranscript","tags":["delete-candidate"],"tier":"GATED-FULL"}
     ,
     {"kind":"go","package":"internal/cli","name":"TestOpenNoFollowReadsRegularFileAndRejectsSymlinkParent","tier":"GATED-FULL"}
@@ -7769,6 +7775,18 @@
     {"kind":"go","package":"internal/modelregistry","name":"TestWorkLatestPinAndAutomaticResidencyReselection","tier":"GATED-FULL"}
     ,
     {"kind":"go","package":"internal/modelregistry","name":"TestWorkingEvidenceCannotUseAnotherModelsUsage","tier":"GATED-FULL"}
+    ,
+    {"kind":"go","package":"internal/nativeattach","name":"TestAttachLeaseUsesElapsedServerBudget","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/nativeattach","name":"TestComposerPreservesShortcutsAndBoundsTerminalData","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/nativeattach","name":"TestComposerReceiptsAndEscapeLeaveSessionRunning","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/nativeattach","name":"TestFakeSocketDetachViewerAndStaleBinding","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/nativeattach","name":"TestStreamRejectsSuccessorBeforeRendering","tier":"ESSENTIAL"}
+    ,
+    {"kind":"go","package":"internal/nativeattach","name":"TestTerminalRestoredOnEveryExit","tier":"ESSENTIAL"}
     ,
     {"kind":"go","package":"internal/nativecontract","name":"TestNativeContractAreaParity","tier":"ESSENTIAL"}
     ,
```

The approved remote Go run at `facb16772` passes both complete affected
packages, `./internal/nativeattach/... ./internal/cli/...`, with exit 0; CLI
reports 36.784 seconds. There are no affected web or browser cases. The approved
guarded static `--merge-main` run exits 1: `go-tiers`, `audit-tests` and
`audit-coverage` reproduce the two missing registrations; the other 39 checks
pass, with zero optional skips. This also proves the existing negative controls
fail at `facb16772`. Static validation is failed, not incomplete or green. R0
must apply the patch and obtain green strict static/release checks before merge.
The non-required policy preview awaits the coordinator's trusted review verdict.
No push, deployment, review gate, model CLI or live cutover was performed.

## Fix round 5: central R0 audit integration pending

The report `review-ci618-0618.out` targets `320d8362d`. Its complete job logs
confirm the same two metadata blockers, without a behaviour finding:

- [Go static job 115122561642](https://github.com/inspr-at/paimos/actions/runs/38344438107/job/115122561642)
  rejects exactly the nine unclassified test identities in the integration
  handoff. The tier self-check itself has zero failures. R0 must register all
  nine as `ESSENTIAL`; the additive tier patch in fix round 4 remains applicable.
- [Release check job 115122561162](https://github.com/inspr-at/paimos/actions/runs/38344438107/job/115122561162)
  fails the existing whole-tree coverage and delta CLI assertions on exactly
  the nine `internal/nativeattach/` files. Both failures share the missing
  source-ownership root cause; neither assertion should be changed.

These existing CI assertions fail at the required negative-control revision
`320d8362d`; no new behaviour fix or regression test is needed. The normative
P2b write set still excludes both metadata files, and the updated worker-common
rule explicitly says not to edit `scripts/audit/slices.json` in builder branches.

At 2026-10-13 04:23 UTC, latest `origin/main` is still `06929d1a4` and
[central R0 PR #623](https://github.com/inspr-at/paimos/pull/623) is open. It
adds `internal/native*/**` to S2, which will cover this package once merged;
it does not add the nine test-tier rows. The coordinator must integrate that
released main change and the missing tiers before final checks. PR #618 is
`MERGEABLE`, so no conflict merge or unapproved R0-branch merge was performed.

Both complete affected Go packages pass through the approved remote runner at
`320d8362d`: `./internal/nativeattach/... ./internal/cli/...`, exit 0
(nativeattach 0.351 seconds, CLI 37.483 seconds). The offline affected planner
also exits 0 and selects FULL Go because 3,233 reverse-dependency cases exceed
its 300-case limit. That selection is not evidence of a full-suite run. There
are no affected web or browser cases in this non-browser slice.

The approved guarded static `--merge-main` run exits 1 at `320d8362d`:
`go-tiers`, `audit-tests` and `audit-coverage` reproduce exactly those R0
blockers. The other 39 checks pass with zero optional skips, including Go vet,
test-tier/web-shard self-checks, web typecheck/lint and migration checks. Static
validation is failed, not incomplete or green. Both metadata changes and green
strict static/release checks remain required before merge.

The separate non-required policy preview reports no trusted `gate/verdict`;
the coordinator owns that review after builder exit. All R0 metadata and test
assertions remain unchanged. No push to origin, deployment, model CLI, review
gate or live cutover was performed.

## Fix round 6: remaining Go tier registrations

The report `review-ci618-0708.out` targets `1775fb4f6`. The complete
[Go static job 115134826316](https://github.com/inspr-at/paimos/actions/runs/38347961181/job/115134826316)
rejects exactly the nine unclassified tests listed in the integration handoff.
Go vet and the tier selector regressions pass. All seven Go test shards, web,
e2e, migration compatibility and the release check pass on this PR revision.
The aggregate Go failure follows the failed static job; it is not a separate
behaviour failure. No assertion or behaviour change is needed.

Latest `origin/main` is `3507e3319`, already included in `1775fb4f6` by the
coordinator. It contains central R0 PR #623, so the previous audit blocker is
resolved. PR #618 is `MERGEABLE`; no further conflict merge is required.
`scripts/ci/go-test-tiers.json` still lacks all nine rows. P2b's normative
exclusive write set and this fix brief do not transfer R0 ownership.

R0 must apply only the Go tier portion of the fix-round-4 patch above, adding
the nine cases as `ESSENTIAL`. Its audit portion is already superseded by
main. The tier-only patch passes `git apply --check` against `1775fb4f6`.
An in-memory candidate parses, contains exactly nine additional rows and
preserves every existing row. The unchanged manifest at the required
negative-control revision `1775fb4f6` lacks all nine registrations, matching
the exact CI failure. No new regression test is needed for this metadata
failure, and no R0 file or test assertion was changed.

Both complete affected Go packages pass through `remote-test.sh` at
`1775fb4f6`: `./internal/nativeattach/... ./internal/cli/...`, exit 0
(nativeattach 0.368 seconds, CLI 34.861 seconds). The offline affected planner
also exits 0 and selects FULL Go because 3,233 reverse-dependency cases exceed
its 300-case limit. That selection is not a full-suite result. This slice has
no affected web unit tests or browser specs.

Guarded static `--merge-main` exits 1 at `1775fb4f6`: only `go-tiers` fails
on those exact nine registrations. The other 41 checks pass, with zero skips,
including audit tests/coverage, Go vet, tier/web-shard self-checks, web
typecheck/lint and migration checks. Static validation is failed, not green;
R0 must apply the tier-only patch before final checks and merge. The separate
PR policy preview still requires the coordinator's trusted review verdict.
`git diff --check` and the P2b write-set check pass. This round changes only
this feature handoff and the ignored progress file; implementation, test
assertions and R0 metadata remain unchanged. No origin push, deployment,
model CLI, review gate or live cutover was performed.
