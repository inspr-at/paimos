# Native LEAD qualification and handover (AEON-1287 / P1c)

This package is offline qualification groundwork. It does not enable or start
LEAD, grant tools/history access, change settings, adopt a PID, or release a
reservation. I1 owns the integration; the coordinator owns `G_TERMS`,
`G_LEAD_GRANT` and the 48-hour `G_P1_LIVE` soak. Hide this groundwork from release
notes until the actual pilot is live and verified.

## Qualification boundary

`internal/nativequalification` checks a current, immutable C0 actor/policy,
physical workspace and resource lease, account/conversation directories, exact
fixture binary/SDK hashes and bounded deadlines. Native local history requires
its own current `RequireHistory` approval; `vendor_native` or a profile label
alone is insufficient. Confined/routine history and tools retain their existing
ceilings. The workspace owner uses their own enrolled vendor account, without
shared subscription capacity, token pooling, a credential proxy or modifications
to the vendor binary.

`SessionOverlay` contains no inherited setting sources, plugins, additional
directories or ambient tools. It explicitly clears session-start, prompt,
pre/post-tool, stop and subagent hook buckets and records the disabled
`attached-inbox`, `cswap` and duplicate subagent-registration hooks. Apply only
this session overlay; never write personal/global/workspace settings. Keep the
runtime PreToolUse and current bound-tool checks enabled. Do not use
`disableAllHooks` to suppress the required tool fence. P1a currently excludes
legacy hooks by `settingSources: []`; I1 must deliberately apply the explicit
overlay where the pinned vendor settings interface accepts it.

`CheckTool` is a conformance observation, never a write authorization or an OS
sandbox. The real broker must re-check the exact actor/grant, tool, resource
lease, size/time bounds and physical descriptor paths with `RequireTx` under
the final write fence. An SDK callback cannot qualify a native TUI on its own.
The child receives only an expiring run-bound MCP proof. Daemon, server and
vendor credentials do not enter its environment, prompts or settings.

The pinned offline SDK executes through the actual P1a Node bridge and proves
its current settings/tool/environment boundary, explicit result handling,
revoked-input refusal and observed owned exit. Its separate metadata trace
exercises the C0 observer: a task remains pinned after the foreground final and
through compaction; the exact conversation stays unchanged while thread/cursor
references advance; only a consumed input/native turn may receive an explicit
final. Invalid identity, correlation, sequence or authority does not advance
evidence. Raw prompt/final/tool bodies are not stored by the recorder. Observer
quiescence never proves exit or frees account/host occupancy.

These fixtures do not prove days-long SDK fidelity, actual vendor terms, a
production subscription, production OS isolation or durable crash delivery.
Their fixture version/protocol is not a production SDK compatibility pin.
The live pin/account/authority decision remains a separate coordinator gate.

## Self-handover: approved happy path

1. Before scheduling exit, record current official vendor/account terms and
   exact binary/SDK/protocol pins under `G_TERMS`. Record `G_LEAD_GRANT` with the
   owner, PPM instance/tenant/project, account identity, physical workspace,
   resource lease, allowed tools, settings digest, separate native-history
   approval, model/effort, concurrency and deadlines. Do not infer an automatic
   routine flip. Missing or revoked approval refuses launch.
2. Freeze agentd upgrades on the owning host for the whole pilot. The tooling
   owner records the deployed daemon generation/pin and the rollback route;
   neither a builder nor LEAD changes launchd, upgrades the daemon or restarts a
   live writer. I1 must wire native-root occupancy into every existing account/
   host admission path before live enablement. Retain idle/taken/uncertain holds.
3. Capture the source from the current authenticated LEAD generation: exact
   native conversation UUID, account/profile, physical cwd, old kernel process
   start identity and image. Keep raw native references local. Never search
   another account's history, scrape credentials or adopt a saved PID. Bind the
   source digest and request ID to one server intent and local writer lock.
4. Prepare the successor **inside Zellij** using the owning OPS recipe
   `~/.local/share/ops-qa/zellij/coord-session.sh LEAD` with web sharing enabled
   and an attached client. The recipe/launcher integration belongs to its
   owner; this package does not edit or execute it. Queue the native start with
   `--profile native --role coordinator --project AEON --harness claude`, the
   exact approved `--cwd`, `--resume` and `--after-exit` source PID through the
   approved CLI/owner socket. The daemon waiter, not Markus, observes the source
   exit. The old LEAD finishes its last explicit handover final and exits.
5. While holding the one-writer fence, prove the old pinned root and process
   group are absent. `CheckSuccessor` rejects missing/group-unknown exit,
   mismatched process digest, future/preceding evidence, a different conversation
   or a reused harness/runtime generation. P1a additionally pins kernel start,
   executable, cwd and owner before waiting. Raw PID reuse is not exit evidence.
   Revalidate current grants, history approval, account/profile, resource lease,
   admission and deadline after the wait and immediately before possible fork.
6. Claim/register a fresh harness/runtime epoch, retain the root reservation,
   then open exactly one structured successor against the same native
   conversation/account/workspace. The approved happy path needs no manual
   Markus command after scheduling; person approval is a prerequisite rather
   than a step hidden in the transition. If writer exit is unknown, publish
   recovery-needed and hold occupancy; do not start or clean up a successor.
7. Publish only the credential-free `terminal_viewer_url` through P1b registration:
   `https://<approved tailnet host>:8082/LEAD`. Authorized person readers may see
   the link; agent principals must not receive it, including owner-workstation
   agents. The Zellij read-only token stays in the person's browser. PAIMOS
   never receives/stores it. No URL query, fragment or userinfo is permitted.
8. Exercise direct streamed input/consumption/final receipts, visible web
   conversation, interrupt, graceful stop with owned exit, and viewer detach
   while retaining process/account occupancy. Start the 48-hour soak only after
   these checks pass. Record zero lost acknowledged inputs/finals and real root
   occupancy. Separately qualify deliberate crash/restart recovery after P3/I2;
   do not force a restart during the frozen pilot.

## Rollback and failure handling

Disable only the new native LEAD route, preserving the old attached serving
paths and authorized durable history/receipts. Stop the owned native runtime
through the current bound authority; prove exact root/group exit before an
official native CLI resume on the same approved profile/conversation. Never
foreground-resume while agentd still owns a writer. A lost acknowledgement or
possible write is uncertain and must not be automatically injected again.
Unknown exit retains occupancy and requires coordinator recovery; it does not
permit force, PID adoption, account hopping or deletion of vendor history.
Retained explicit finals use P3's exact final-drain grant, with no input/control
authority and no revival of revoked consent. Lift the upgrade freeze only when
the owning coordinator has recorded the final ownership and rollback evidence.

## Integration blockers and R0 handoff

- **Z1 viewer port (AEON-1293):** resolved. The default `ViewerHosts` port
  allowlist is 443 and 8082, beside the existing host allowlist.
  `TestNativeLeadPersonOnlyViewerAndZ1Compatibility` requires the approved
  `:8082/LEAD` URL on an allowlisted host. Credential material, an unlisted
  port, an empty port suffix and a foreign host still fail with `invalid_input`
  and the error does not echo the URL. The host allowlist is unchanged.
- **Native metadata/final wiring (I1, AEON-1290):** P1a's bridge currently does not forward task
  or compaction metadata to C0, and `chat_final` does not carry the consumed
  input correlation ID. I1 must map a pinned reader into the C0 observer and
  correlated final path before claiming these behaviors for a live session.
  The offline trace proves observer behavior only, independently of that bridge
  boundary. Native terminal-link display remains P2d/I1-owned.
- No API routes, permissions, migrations, DSAR columns, version changes or new
  tenant/tree lock callers are introduced. Existing `internal/native*/**`
  source ownership covers this package. The browser file's source ownership
  remains with the existing web-test glob.
- The four Go tests in `internal/nativequalification` are registered as ESSENTIAL:
  `TestNativeLeadPinnedSDKPolicyBoundary`,
  `TestNativeLeadBackgroundCompactionAndConsumedFinal`,
  `TestNativeLeadFencesDeadlinesAndRevocationBarrier`,
  `TestNativeLeadPersonOnlyViewerAndZ1Compatibility`.
- `web/tests/native-lead-pilot.spec.ts` is registered as ESSENTIAL browser
  coverage with six cases, `AEON-1287 LEAD conversation <light|dark> <390|1024|1440>`,
  and a matching row in the existing `stable-controls` web-shard group. The fix
  adds only P1c's new tier/shard rows; existing classifications, shard policies
  and source ownership remain unchanged.

## Acceptance and validation evidence

Validation results and any incomplete checks are recorded here before handoff.
No fixture result authorizes push, merge, deploy, model review or live cutover.

The coordinator's finishing brief records all four Go qualification behaviors
as remotely passed at `9e975e2d2`. This is prior-run evidence, rather than a new
full package run in the finishing round:

| Acceptance item | Offline evidence and remaining boundary |
| --- | --- |
| Pinned SDK, tools, settings, workspace/resource fences, deadlines and child environment | `TestNativeLeadPinnedSDKPolicyBoundary` and `TestNativeLeadFencesDeadlinesAndRevocationBarrier` passed remotely. Actual vendor terms, production pins and the live grant remain coordinator gates. |
| Explicit exclusion of attached-inbox, cswap and duplicate registration hooks | The policy-boundary test passed remotely; the overlay clears all seven legacy hook buckets. I1 must apply the overlay through the approved vendor settings interface. |
| Separate native-history approval; existing confined/routine ceilings | The policy and fence tests passed remotely, including missing/refused history consent and routine rejection. No approval is issued by these fixtures. |
| Exact-conversation self-handover, after-exit proof and rollback | The runbook above records the approved path. The background/compaction test passed remotely with missing-exit and wrong-process refusals. Production one-writer wiring and upgrade freeze remain I1/coordinator work. |
| Background pinning, compaction references, consumed-input finals and revocation | The background/compaction and revocation-barrier tests passed remotely. The corrected replay/correlation scenario also passed locally at `135d00d70` in this finishing round. Live metadata/final wiring remains AEON-1290. |
| Z1 person-only terminal projection and required port | The earlier remote run proved person-only projection and the then-current `:8082` refusal. AEON-1293 closes that boundary: the same test now requires the default allowlist to admit the approved `:8082/LEAD` URL and still refuse credential, unlisted-port and foreign-host URLs. |

The finishing-round negative control compiled the preceding text-only replay
digest from `135d00d70^` through a temporary Go source overlay, against the
current test. It failed at the exact intended assertion:

```text
qualification_test.go:261: changed input replay accepted <nil>
--- FAIL: TestNativeLeadBackgroundCompactionAndConsumedFinal (0.04s)
```

The unmodified current source then passed the same targeted test with
`GOMAXPROCS=2 go test -p 2 -count=1 -timeout 45s -run
'^TestNativeLeadBackgroundCompactionAndConsumedFinal$' -v
./internal/nativequalification`:

```text
--- PASS: TestNativeLeadBackgroundCompactionAndConsumedFinal (0.48s)
PASS
ok  github.com/inspr-at/paimos/internal/nativequalification  0.713s
```

The temporary overlay was trashed. No production source was changed, no new
test was added and no negative-control commit was left on the work branch.

Exactly one finishing-round browser attempt through `remote-spec.sh` and one
static attempt through `ci-static-locked.sh` both exited **3**: the two remote
slots were occupied (`2/2`, CI pool `1/4`). Neither check ran to completion;
both are **incomplete locally, awaiting PR CI**. There was no retry or local
browser/static fallback. The first-round affected selection was `FULL`.
At that handoff, authoritative PR CI and the tier/shard rows above were still
required before merge. AEON-1293 and AEON-1290 were the documented integration
dependencies then; AEON-1293's port allowlist has since landed. No live
qualification or release gate was run by that worker.

## Fix round 3: CI registration drift

CI on `16862178c` rejected the four unclassified Go qualification tests, the six
unclassified browser cases and the missing `native-lead-pilot.spec.ts` shard row.
The three existing web selector/inventory regressions failed on that same head.
The aggregate Go/web failures and missing `web-unit-1` measurement evidence
followed these failures; the original qualification assertions were unchanged.

`58ae26413` adds the four Go and six browser cases as ESSENTIAL and assigns the
spec once to the existing `stable-controls` group. Its 30-second shard weight is
explicitly an estimate for six unmeasured cases, not a fabricated measurement.
No production behavior, fixture assertion, exclusion or existing row changed.

Focused manifest checks loaded the old manifests directly from `16862178c`,
without changing the checkout. Strict validation rejected exactly the four Go
and six browser identities; source inventory rejected exactly the missing spec.
The current manifests passed those checks, covered all 316 browser specs and
retained every existing classification and shard setting. These checks did not
perform native test collection or launch a browser. Existing CI regressions
provide the negative control; no throwaway commit was added to this branch.

The guarded `ci-static-locked.sh` retry ran the underlying
`node scripts/ci-static.mjs --merge-main` on the approved test host at `58ae26413` and exited
**0**:

```text
PASSED test-tiers
PASSED go-tiers
PASSED audit-tests
PASSED web-shard-spec-tests
PASSED web-shard-tests
PASSED web-tiers
PASSED web-spec-inventory
PASSED web-classification
Static pre-filter: 149.15s; 42 passed, 0 optional skipped.
```

This includes the three required tier/shard/audit self-checks, strict native
registration collection and the existing web regressions that failed in CI.
Canonical `manifests --check` and `git diff --check` also exited 0. The affected
selection is `FULL`, so authoritative PR CI still runs the full lane.

Three attempts each through `remote-test.sh` for the qualification package and
`remote-spec.sh` for the six browser cases were capacity-refused with exit **3**;
the last attempts reported CI pool `3/4`. Fresh Go behavior and browser runs are
**incomplete, awaiting PR CI**. The earlier four-test remote Go evidence above
remains valid for the unchanged qualification source; it is not a fresh run of
this commit. No local Go/browser fallback, actual model call, review gate, push
to origin, deploy or live qualification was performed.
