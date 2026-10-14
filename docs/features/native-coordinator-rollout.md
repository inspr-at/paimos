# Tick, OPS and AIT native coordinator recipes (AEON-1306 / P5d)

This is rollout groundwork behind the integration and owner gates. No role has
been migrated by this change. `G_P1_LIVE` (the verified 48-hour LEAD pilot),
`G_ROLES`, authoritative PR CI and the coordinator's release gate remain
prerequisites. Hide this groundwork from release notes until the particular
role is live and verified. Fixtures do not qualify a vendor account, platform,
production host, subscription, native TUI or operator effect.

`internal/nativeroles.Lookup` provides three exact, independent recipes. They
contain requirements only. They have no default account, inherited grant,
executable shell command or launch effect. `LEAD`, `coordinator`, arbitrary
display names and differently cased identifiers cannot select a role recipe.
The frozen C0 wire role remains `coordinator`; the trusted role resolver binds
the separate Tick/OPS/AIT identity to its actual grant. No contract enum changes
or new permissions are needed.

| Role | Workspace and tools to approve explicitly | Owner execution boundary |
| --- | --- | --- |
| Tick | The approved design/UI worktree and exact resource leases; explicit brokered design, read, edit and test tools under Tick's actor and tool/settings policy. | Tick's owner applies its launcher change and records its actual-host cutover. Design acceptance, merge and release decisions require their own authority. |
| OPS | The approved OPS tooling worktree and exact operator resource leases; explicit brokered OPS tools. Workstation evidence does not qualify production hosts. | Production, launchd, Zellij and external OPS tooling changes are performed only by their separately authorized operator. An OPS name is not operator approval. |
| AIT | The approved AIT worktree and exact resource leases; explicit brokered AIT tools and its own actor/policy. | AIT's owner applies its launcher change and records AIT's independent cutover and rollback. No Tick, OPS or LEAD grant is borrowed. |

Every role uses its workspace owner's **own** locally enrolled vendor account.
The instance, tenant, project, principal, owner, role, computer, platform,
account ID/key, physical workspace, model profile, policy ID/revision,
tool/settings digests and native-history consent are explicit. No account
fallback, token pooling, shared subscription capacity, copied credential store
or cross-instance account/history transfer exists. Vendor binaries remain
unmodified. The initial recipes support the qualified Claude coordinator route
from P1c; another provider requires its own pin/history qualification.

## Source and authority fence

`Switcher.Switch` accepts authenticated `Scope` separately from the caller's
expected predecessor and one C0 `LocalStart` or `ResumeIntent`. Role names and
CLI recipe output never construct `Scope`, `Grant`, `Current` or live gate
evidence. `Grant` and `Current` refuse JSON serialization/deserialization; native
conversation and approval-history metadata remain local authority records.

The I2-owned `Authority.WithCurrent` adapter must hold the existing **local
conversation writer fence**, capture the current grant and immutable predecessor
assignment, and supply a route bound to both. It calls the callback exactly
once on success. This is not a database transaction around native/network I/O.
The route uses the existing C0 start/resume intent/claim path. Final server
writes still re-check permissions with `RequireTx` under the access-change
locks, and the daemon rechecks current admission, owner/process provenance and
the same writer fence before possible fork. This package allocates no second
ledger and never stops, signals, enumerates, adopts or resumes unrelated
sessions. Without an authenticated adapter, cutover is unavailable.

The checked source record includes the original role/principal/account/workspace
assignment, exact owned/harness/runtime/daemon binding, current revision,
native conversation reference and approval-history digest. A changed tenant,
principal, workspace, account, grant, policy, model, history or predecessor
generation refuses the operation. Local starts require the exact approved
workspace root; the owning launcher must also prove its physical descriptor
identity, OS resource fences and pinned binaries immediately before launch.

Tools are explicitly selected in the role grant, with P1c's session overlay
validation rejecting ambient built-ins and unknown tools. The native broker
retains its mandatory tool checks; production resources need exact operator
authorization. Native persistence requires separate current history consent.
Existing restricted/confined/routine ceilings and personal/global settings are
unchanged. Resuming history preserves context, not predecessor approval powers:
pending decisions must still match their current generation, native request,
effect, expiry and person/step-up authority through the existing broker.

## Per-role cutover

Perform the following independently for Tick, OPS and AIT. The builder does not
execute these operator steps or modify the external launcher.

1. Record the role grant and separate native-history consent, current official
   vendor terms/pins, actual host/platform, restricted and native capability
   runs, approved concurrency, `G_P1_LIVE`, `G_ROLES` and rollback. Bind evidence
   to this role's instance/tenant/principal/computer/account/workspace and policy
   digests. Production execution has its own operator approval record. Terminal
   export and local take require separate qualified evidence; no capabilities
   are inferred from the LEAD pilot or another Mac's OS.
2. Pair agentd **locally on each owning host** with its existing owner-only
   pairing flow. The protected Unix socket, token and kernel peer checks remain
   local. The host uses outbound authenticated server relay. Remote viewing
   uses the approved session API; approved SSH may invoke
   `aeon session attach OWNED_ID` **on the owning host**. Never expose or forward
   a raw Unix socket over the network, transfer its token, or start a vendor
   writer to attach a viewer. Detach does not release occupancy or stop a writer.
3. The tooling owner prepares the role inside Zellij with its existing
   `~/.local/share/ops-qa/zellij/coord-session.sh Tick`, `OPS` or `AIT` recipe and
   web sharing, analogous to P1c. Freeze agentd upgrades on that owning host.
   Publish only the validated `https://<approved host>:8082/<role>` path through
   the existing registration projection. Person readers alone receive this
   link; the browser's Zellij token stays in the browser. PAIMOS stores/sends no
   token or URL userinfo/query/fragment. External tooling is owner/operator work.
4. Capture the current authenticated source generation and submit the owned
   native start/resume route with `--profile native --role coordinator`, the
   approved project/harness/cwd/account/policy, the **exact same** native
   conversation, and a pinned `--after-exit` barrier. The C0 `LocalStart` includes
   the same owned session, native resume reference, PID, process start time,
   identity digest and bounded deadline (at most 300 seconds and within the
   grant). Keep one stable request ID for reconciliation; never retry with a
   fresh ID after a possible effect. The old role emits its handover and exits.
5. The existing owning daemon proves the old root **and group** absent and
   revalidates the role grant, history consent, account/workspace, admission and
   deadlines before opening exactly one successor with fresh harness/runtime
   identity. Existing owned recovery uses C0 `ResumeIntent` with the exact
   binding/revision/policy only after confirmed exit; missing, wrong-process,
   future or partial exit evidence fails closed. Verify the same conversation
   and approval history, correlated input/final receipts, controls, viewer
   detach and honest account/host occupancy before recording cutover complete.

`accepted` and `queued` mean an intent is pending; they do not prove process
start, native consumption, successful migration or free capacity. Returned
receipts must match the exact request/operation/session and qualified capability
ceiling. Completed cutover has a fresh harness/runtime and higher binding
revision with the exact predecessor. Rejected, expired, uncertain, mismatched
and lost responses are surfaced; this layer never automatically retries.
Idle, detach, completed turns and uncertain exit retain the owning ledger hold.
Agent callers receive no terminal URL or person-only terminal/take/export
capabilities.

## Per-role rollback

| Role | Disable | Resume only after owned exit |
| --- | --- | --- |
| Tick | Only Tick's newly enabled native route/launcher selection. | Tick's same approved local account, workspace and native conversation; retain its design and handover history. |
| OPS | Only OPS's newly enabled native route; its external tooling rollback is executed by the authorized operator. | OPS's same approved local account/workspace/conversation, after root/group proof on the actual owning host. Production rollback needs its own operator execution. |
| AIT | Only AIT's newly enabled native route/launcher selection. | AIT's same approved local account, workspace and native conversation; retain its approved receipts/history. |

Preserve legacy serving paths and authorized durable receipts. Stop the owned
native runtime through current bound authority and prove exact root/group exit
before an official native CLI resume. Never foreground-resume while agentd owns
a live or uncertain writer. Unknown exit or a lost input acknowledgement means
recovery-needed with occupancy retained; no successor, automatic reinjection,
PID adoption, account hopping, deletion of native history or broad cleanup.
Lift the upgrade freeze only after the owner records final ownership and
rollback evidence. No actual rollback or live role cutover was run here.

## I2 integration and evidence handoff

I2 mounts `runtime.nativeRoleRecipe()` as `aeon session role-recipe Tick|OPS|AIT`
under its existing `cmdSession` ownership. The command shows requirements only
and needs no account/socket/model call. `switchNativeRole` is the local helper
for the authenticated fenced adapter; it is unavailable without that adapter.
P3b recovery remains a soft dependency closed by the coordinator's merge-main
and I2 round. No shared CLI root, agentd, server, API, dependency or release
version is modified by P5d.

| Acceptance item | Package evidence / remaining owner boundary |
| --- | --- |
| Separate explicit Tick/OPS/AIT role, workspace, account and tool grants; no name-derived LEAD authority | `TestNativeRoleSwitchFencesAndSingleWriter` rejects identity/assignment crossover for all three roles; `TestNativeRoleRecipesRequireSeparateAuthority` rejects LEAD/arbitrary names and a missing adapter. Actual grants are owner-issued. |
| Owned start/resume + pinned after-exit; exact conversation and approval history | Fake exit barriers show no possible successor before old exit and frozen request pointers. Resume evidence rejects unknown/group-partial/wrong-process exit; receipt matching rejects stale successors/escalated capabilities. Production writer/admission and history fidelity remain I2/P3b/role-owner qualification. |
| Local per-host pairing, outbound relay, approved API or SSH-local attach | Grant checks reject missing local pairing/outbound relay and borrowed host/platform evidence. Recipes above require approved API or SSH invoking local attach; no network socket transport is added. Owner pairs and qualifies actual hosts. |
| Per-role cutover/rollback and restricted/native capability evidence | Independent recipe/matrix above; missing restricted/native run, role approval, LEAD soak, terminal export or production operator approval refuses cutover. Actual run/approval records remain pending for each role. |
| Fake switches reject crossover/stale predecessors and leave unrelated live sessions alone | The named behavior test asserts the exact refusal reason, zero native calls for rejected preflights, one successor after the exit barrier and an unrelated live-session fixture throughout. A separate revocation barrier proves final-route refusal without retry/fork. |

Three own Go tests are classified ESSENTIAL in the R0-granted
`scripts/ci/go-test-tiers.json` rows:

- `internal/nativeroles:TestNativeRoleSwitchFencesAndSingleWriter`
- `internal/nativeroles:TestNativeRoleResumeExitAndReceiptEvidence`
- `internal/cli:TestNativeRoleRecipesRequireSeparateAuthority`

Existing native-package and CLI audit globs cover the new source. No new routes,
permissions, migrations, DSAR classifications, tenant/tree lock callers, web
tests or shard rows are introduced. Validation results are recorded at handoff;
no fake or test result opens a live, review or release gate.
