<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Native Codex sessions

AEON-1274 supplies the P5a native Codex codec behind C0's `Launcher` and
`NativeTransport`. It is an integration component with no default process
opener or grant source. It is not enabled by this change. `G_TERMS` and
`G_PROVIDER_PINS` remain coordinator gates before any live provider use.

The intended deployment uses the workspace owner's own enrolled Codex CLI
account. Each child receives its own `CODEX_HOME`; no account credential file
is read, no token is passed through PAIMOS, and no managed OAuth provider or
shared subscription pool is created. Login and profile enrollment remain
person/OPS operations. The official vendor binary stays unchanged.

## Integration and qualification

I2 implements `WireOpener`/`Wire` around agentd's existing `launchWire` and
owned-process lifecycle, rather than adding another supervisor. There is one
app-server process per logical session. The opener must verify the physical
binary and workspace/profile paths, exact binary digest and version, existing
server/local admission, exclusive owned-writer reservation and policy lifetime
before possible fork. RPC result dispatch must be independent of bounded
notification consumption. Overflow or pipe loss is lost evidence, never proof
that the process exited.

The codec fixture is identified by `codex-app-server/0.157.1`, matching the
version referenced by the existing Codex qualification boundary. It freezes
the tested request vocabulary; it is not live qualification of that binary.
The coordinator must compare the exact official binary-generated schemas and
tool/settings behavior to this fixture at `G_PROVIDER_PINS`. A different schema
or binary needs its own qualification; there is no version fallback.

`GrantSource` resolves an existing supervised native worker grant, bound to the
full C0 binding, account/profile revision, binary and tool/settings digests.
It must qualify inherited hooks and MCP/settings, enforce existing authority
and budgets, and serialize profile login/rebind against live use. A read-only
sandbox alone does not prove this boundary. Routine, confined, verification and
bounded-preapproved paths stay unavailable. Run-bound MCP tool injection is
also unavailable in this codec until independently qualified.

The launch environment is explicit and excludes ambient credentials/provider
overrides. Native account identity is checked through `account/read` before
thread creation, every input/control write, quota projection and final
publication. Account change notifications invalidate the writer's evidence;
the adapter never logs in or rotates accounts to recover it. The grant is
rechecked after the identity read under the adapter's native-I/O lock.

The Host/server integration still owns command actor/controller verification,
`write_started` journaling before native I/O, retention/output consent and
receipt persistence. These responsibilities cannot be inferred from a successful
native RPC. `ApprovalAuthority` must recheck the exact command actor, effect and
person step-up before replying to a native approval request. `FinalGrants`
supplies only a server-issued drain grant for the same binding and opaque
conversation reference. It grants no launch or control authority.

## Protocol and evidence

The handshake is `initialize`, `initialized`, `account/read`, then
`thread/start` or exact-ID `thread/resume`. Thread requests use `sandbox`;
turn requests use `sandboxPolicy`. The codec checks the resolved model,
provider, cwd, sandbox and approval policy before input. Resume response history
is ignored as evidence for the new writer. Experimental APIs are not enabled.

`turn/start` acknowledges a write. A correlated `turn/started` supplies consumed
input evidence. `turn/steer` contains only the thread, text input and exact
`expectedTurnId`, and its reply must match that turn. Steering acknowledges
injection; it does not claim which steered input a final answered. Final
correlation retains the original turn-start input. `after_turn` returns busy
while a turn is active, leaving queueing/revalidation to the Host. Interrupt
uses the exact opaque current-turn reference and native `turn/interrupt`; the
RPC acknowledgement never means process exit.

An explicit completed `agentMessage` is a final candidate. Only a successful
`turn/completed` for this writer/turn, after current account/grant checks, can
produce `FinalPublish`. Commentary, token assembly, failed/interrupted turns,
historical items and duplicate/foreign terminal evidence never publish finals.
Without publication authority, only a live final reference is emitted. Public
thread/turn/item/final references are domain-separated by tenant, session and
runtime. Native correlation stays local.

Only command/file approvals are mapped, with generic safe summaries and a
digest of the complete native effect. Replies are `accept` for `allow_once`
or `decline` for `deny`, once per request; session-wide/policy amendments and
permission/OAuth requests are unavailable. Callbacks expire at turn end or
grant expiry and do not transfer to another runtime.

Token counters are bounded and scoped to the current native thread/turn.
Rate-limit windows carry the exact enrolled account and profile revision,
measurement time and a one-minute freshness bound. Live text, tool statuses,
safe token counters and quota facts use C0 projections. Raw commands, paths,
tool arguments/results, private reasoning and unknown native fields are omitted.
Native JSON is bounded to 1 MiB and depth 16 before typed decoding; duplicate
keys and trailing JSON are rejected. C0 payload and exported-frame limits are
checked before publication. There are 32 buffered provider events and at most
32 pending approvals. Overflow loses evidence visibly instead of truncating a
successful final or automatically replaying an uncertain write.

Close/Wait require matching owned root/group exit evidence. Completed turns,
idle state, frame EOF and cancelled watchers never release occupancy.

Protocol reference: [official Codex app-server documentation](https://learn.chatgpt.com/docs/app-server).

## Validation and R0 handoff

Named risk: app-server/schema drift and native-versus-exec tool-policy parity
can invalidate provider capabilities. All qualification tests use an injected
clock and fake app-server connection; no model, login or subscription calls.
The pre-ack notification case uses explicit request/release barriers.

Coordinator-owned same-PR registrations:

- Source ownership: add `internal/nativeproviders/codex/**` to S2 (agent runtime).
- Go test tiers: `internal/nativeproviders/codex:TestNativeCodexTranscriptQualification`,
  `internal/nativeproviders/codex:TestNativeCodexRejectsUnqualifiedLaunch` and
  `internal/nativeproviders/codex:TestNativeCodexRejectsUnboundedOrAmbiguousFrames`;
  proposed tier `ESSENTIAL` for all three (fake protocol safety tests).
- No web/browser shard, route/contract pin, permission registry, database/DSAR
  or shared-fence registration changes are needed by this source-only slice.

Builders do not edit R0's shared inventories. These rows must be applied in
this PR before final authoritative checks and merge. Run the targeted package
through `remote-test.sh`, the affected planner against `origin/main`, and the
guarded static check. A refused/incomplete check is not green and remains an
explicit coordinator/PR-CI handoff.

## Builder handoff evidence

The final implementation was validated at code commit `918a33bd8`:

- PASS acceptance 1: C0 `Launcher`/`NativeTransport` implementation with injected
  `launchWire` adaptation ports and no process supervisor.
- PASS acceptance 2: pinned handshake, start/resume, exact-turn start/steer/
  interrupt, explicit final, approval and usage mappings; fake single-child
  launch and resume transcript assertions.
- PASS acceptance 3: current enrolled account checked before input and final
  evidence; per-child `CODEX_HOME`, immutable grant/profile/policy, and no
  credential/OAuth handling.
- PASS acceptance 4: supervised native policy matches the existing grant and
  resolved sandbox; routine/confined/bounded-preapproved profiles refuse before
  possible fork. Live native tool qualification remains `G_PROVIDER_PINS`.
- PASS acceptance 5: fake transcripts reject stale turns, changed grants,
  mismatched steering acknowledgements and wrong-account input/final evidence.
  Removing the account-match fence fails both wrong-account assertions for
  their intended reasons; exact source was restored with no control commit.

The targeted suite passed through the guarded remote test runner (0.293 s).
The permitted small local package fallback also passed with `-race` (1.726 s)
after remote capacity refusals. The three named tests include barriers, injected
clock approval expiry, observed-exit failure and C0 payload validation.

`affected.mjs --base origin/main --json` exited 0: essential/full selection,
128 Go packages, 80 browser specs and 120 web unit files. The new Codex package
is absent from that manifest-derived selection until R0 applies the three tier
rows, so the targeted package was run explicitly. This is not evidence that the
broader selected suites passed. Browser validation is not applicable to P5a.

R0 registrations and authoritative PR CI remain required in this same PR.
The final `ci-static-locked.sh` run completed with exit 1: 39 checks passed,
none were skipped, and only the R0-controlled registrations failed. `go-tiers`
reports the three unclassified tests above; `audit-tests`/`audit-coverage`
report the six new package source/fixture files without slice ownership.
Earlier guarded static attempts refused the remote 2/2 slot cap with exit 3.
The builder cannot fix either registry outside its exclusive write set. Apply
the S2 ownership glob and the three tier rows, then rerun guarded static and
authoritative selected-suite CI before merge. This handoff is not static-green.

No shared inventory, contract, permission, migration or release-version file was
changed. No origin push, deployment, live vendor session, model CLI or review
gate was run.

## CI fix round 5

The full failed-step logs at PR head `77520cb0` confirm two outstanding R0
registrations:

- `go-static` job `115120755491` failed `check go --strict` on the three
  unclassified Codex tests listed below. Its full failed-step log contains no
  provider compilation or behavior failure.
- `release-check-run` job `115120755358` failed
  `test_current_snapshot_has_no_unassigned_code` and
  `test_delta_cli_and_malformed_input_exit_codes`. Both failures come from
  missing slice ownership for `codec.go`, `launch.go`, `notifications.go`,
  `qualification_test.go`, `testdata/app-server.json` and `transport.go` under
  `internal/nativeproviders/codex/`. Even the empty `--since HEAD` delta rejects
  an invalid ownership map before it reports the delta; its assertion must not
  be weakened.

R0's central [PR #623](https://github.com/inspr-at/paimos/pull/623) adds
`internal/native*/**` to S2 in `scripts/audit/slices.json`, which covers the
Codex package. It is still open and the glob is absent from fetched
`origin/main` (`06929d1a4`). The worker rules added on 2026-10-13 explicitly say
"Do NOT edit scripts/audit/slices.json in your branch" and require merging main
once that central fix lands. No merge is needed for conflicts: PR #617 is
mergeable. R0 also needs these three rows in `scripts/ci/go-test-tiers.json`,
retaining canonical ordering; PR #623 contains only the audit-map change:

```json
[
  {"kind":"go","package":"internal/nativeproviders/codex","name":"TestNativeCodexRejectsUnboundedOrAmbiguousFrames","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativeproviders/codex","name":"TestNativeCodexRejectsUnqualifiedLaunch","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativeproviders/codex","name":"TestNativeCodexTranscriptQualification","tier":"ESSENTIAL"}
]
```

The strict Go-tier check and manifest-based selection must include this
package. The normative P5a plan explicitly reserves both inventory files for
R0: "Builders send exact row/registration intents; they never independently
rewrite these shared inventories." P5a owns only
`internal/nativeproviders/codex/**` and this feature document. Fix round 5
requires preserving ownership and records no transfer of either registry.
The required registrations therefore remain with R0 before this PR can merge.

At this PR head, all seven Go shards, Go timing, all four web unit shards, all
twelve browser shards and e2e passed in hosted CI. Those results do not replace
the missing registrations or establish that the Codex package was selected.
No provider behavior change is needed for these findings; no behavior, assertion
or existing test tier was changed. The ownership checker reproduced exactly
the six unassigned files against immutable `77520cb0`. Adding only the proposed
S2 path in memory made full-tree ownership pass with zero errors; the on-disk
registry stayed unchanged. The assigned Codex package passed through the
guarded remote runner with `-count=1` (0.296 s). Web and browser reruns are not
applicable to this documentation-only handoff.

The fix round 5 `ci-static-locked.sh` run at `77520cb0` completed remotely with
exit 1: 39 checks passed and none were skipped. Only `go-tiers`, `audit-tests`
and `audit-coverage` failed, for exactly the registrations above. The required
tier-toolkit and web-shard self-checks passed; the audit self-check reproduced
the two ownership failures. Public names, Go vet, web typecheck/lint and strict
web tiers passed. This is not static-green, and no heavy local fallback was
run.

The non-required `gate/policy-preview` diagnostic reports a missing trusted
verdict. That decision remains with the coordinator's cross-family gate.
After applying the two R0 registrations in this PR, rerun guarded static and
authoritative CI, including the tier/web-shard/audit self-checks. This round
remains incomplete until that work is green. The remaining blocker is R0
metadata work; no provider behavior fix was found, so no new behavior test or
assertion change is warranted. The PR was mergeable after fetching current
`origin/main`; no merge was needed. No origin push, deployment, model CLI or
review gate was run.

## CI fix round 6

The full failed-step logs at reviewed head `4836ccb72` confirm the same two
metadata causes: the three unclassified Codex tests in `go-static` job
`115126936584` and the two ownership assertions in `release-check-run` job
`115126936672`. No provider compilation or behavior failure was reported.

R0's central PR #623 merged as `3507e3319`. Fix round 6 merged that current
`origin/main` cleanly into this branch as `90446fb2c`, importing its
`internal/native*/**` S2 ownership glob. The ownership checker reproduces all
six unassigned Codex files with the reviewed head's configuration and assigns
all six without errors with the imported configuration. No ownership assertion
was loosened and the builder did not edit the shared registry.

The guarded remote Codex package run at `90446fb2c` passed with `-count=1`
(0.342 s). The affected planner against `origin/main` exited 0 and selected
128 Go packages, 120 web unit files and 80 browser specs. The Codex package
still has zero registered tier rows and is absent from that selection, so the
targeted remote run remains necessary. No web or browser source changed;
their reruns are not applicable to P5a.

The three exact `ESSENTIAL` rows in the fix round 5 handoff remain required in
`scripts/ci/go-test-tiers.json`. The normative P5a/R0 plan continues to reserve
that file for the coordinator metadata lane; this fix brief preserves ownership
and does not transfer it. CI cannot be called green until R0 applies those
rows in this PR and the strict tier check and authoritative CI pass. No provider
behavior fix or new behavior test is warranted for these metadata findings.

The guarded `ci-static-locked.sh` run at `90446fb2c` completed remotely with
exit 1: 41 checks passed and none were skipped. `audit-tests` and
`audit-coverage` now pass, confirming that both reported ownership failures
are resolved. The tier-toolkit and web-shard self-checks also pass. The sole
remaining failure is `go-tiers`, naming exactly the three missing Codex rows
above; this is an incomplete handoff, not static-green.

Fix round 6 changes only this feature document and imports the coordinator's
shared ownership fix through the main merge. No provider behavior, assertion,
test-tier registry, contract, permission, migration or release version was
edited. No origin push, deployment, live vendor session, model CLI or review
gate was run. R0's three tier registrations and green authoritative CI remain
the coordinator's next required work before merge.

## CI fix round 7

The complete failed-step log for `go-static` job `115133739874` at reviewed
head `6d57938f6` identifies exactly the three unclassified Codex tests listed
in the fix round 5 handoff. `go vet`, bootstrap drift and tier-selector
regressions passed; `Validate every Go tier against native collection` failed
on those missing rows. There is no provider compilation or behavior finding.
The source-ownership fix remains effective: `release-check-run` passed in this
CI round, as did all seven Go shards, four web unit shards, twelve browser
shards and e2e. These shard passes do not establish selection of the
unregistered Codex tests.

Fetched `origin/main` is still `3507e3319`, already an ancestor of this branch,
and PR #617 is mergeable. Neither reviewed head nor current main contains any
Codex tier row. No merge is needed for this round. The remaining fix is to add
the three exact `ESSENTIAL` rows above to `scripts/ci/go-test-tiers.json` in
this PR, then rerun strict native Go-tier validation and authoritative CI.

That manifest remains outside P5a's exclusive write set. The original brief
requires "Write ONLY inside your exclusive_write_set" and the normative plan's
R0 rule says "Builders send exact row/registration intents; they never
independently rewrite these shared inventories." Fix round 7 preserves
ownership and supplies no metadata-lane transfer. The builder therefore leaves
the shared manifest with R0 and reports the outstanding failure explicitly.

A bounded manifest-only check against immutable `6d57938f6` rejects exactly
those three missing identities. Adding only the proposed `ESSENTIAL` rows in
memory resolves that classification failure. The shared registry stayed
unchanged; this diagnostic does not replace native test enumeration.

The unchanged Codex package passed through `remote-test.sh` with `-count=1`
at `6d57938f6` (0.250 s). The affected planner against `origin/main` exited 0
with essential/full selection; the Codex package remains absent because it
has no tier registrations. No web or browser source changed, so their scoped
reruns are not applicable to this non-browser slice. No behavior fix was made;
no regression test or assertion change is warranted for this metadata failure.

The guarded `ci-static-locked.sh` run at `6d57938f6` completed remotely with
exit 1: 41 checks passed, none were skipped, and only `go-tiers` failed,
naming exactly the three missing Codex rows. The tier-toolkit, web-shard and
audit self-checks passed, along with ownership coverage, Go vet and web
typecheck/lint. This confirms the remaining R0 metadata blocker; this handoff
is not static-green.

This round changes only this feature document. R0 must apply the three tier
rows before this PR can be static-green or merge. No shared inventory,
contract, permission, migration, release version or provider behavior was
edited. No origin push, deployment, live vendor session, model CLI or review
gate was run.
