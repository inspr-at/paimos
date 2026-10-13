<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Native account admission

AEON-1303 (P4c) supplies admission helpers for the sessions program. I1/I2 own
server and agentd wiring. These helpers do not enable a provider, native tool
policy or automatic coordinator start. `G_ACCOUNTS`, authoritative PR CI and
coordinator release gates remain required.

The workspace owner's enrolled vendor account is pinned to the conversation.
`NativeAccountBinding` captures its account key, owner, harness, link revision
and existing identity fingerprints. `RequireNativeSessionTx` enters the final
tenant → tree → pairing → account-use fence, checks current paired-principal
permissions, compares the binding and reuses existing native
account/context/model/quota and AEON-1251 host gates. The owning service also
checks the original actor's immutable tool/settings/workspace grant in that
transaction. This helper alone cannot authorize an operation. Capacity counts
include sibling projects without exposing their account/session facts. Launch
and first-input admission precede marking a worker running; later input
authorization belongs to the owning runtime service.

The local `nativeadmission.Gate` uses C0's `ProfileResolver` and the existing
`agentsetup.SharedLedger`. Roots (`run_id = null`) and workers each consume one
local machine/login group. Workers retain their existing server run reservation;
no second server root or fake leaf is created. Only the pinned account is
considered. The mandatory `Authority` adapter performs fresh final server checks
and excludes only the request's already-held reservation; a cached route or
final-drain claim cannot authorize launch/input.

Integration supplies a dedicated private `agentsetup.Store`, current ledger
member/generation, enrollment-derived login fingerprint and the immutable
`NativeAccountBinding.Digest()` as `Request.AccountBindingDigest`. `Reserve`
pins the resolved local profile and acquires the shared ledger hold. `Attempt`
checks profile identity and server authority immediately before invoking the
owning lifecycle's context-bound launch callback. `Input` checks the pinned
profile revision/configuration/binary digest and current authority before its
transport callback. Existing controller, tool, sandbox and process proofs still
apply. No credentials, auth files or vendor binaries are accessed by this package.

The bounded private journal stores identifiers, digests, ledger coordinates and
process evidence, with no homes, environments, prompts, transcripts or finals.
Released tombstones preserve the account pin across daemon reconstruction.
Saved PIDs never authorize process adoption or signals. Under the existing
dispatch mutex, I2 must include `HeldGroups` alongside existing worker/lead
occupancy during ledger import. Unresolved old-generation groups refuse a
rebuild; old salted login fingerprints cannot become fresh identity proof.
The journal is capped at 4,096 entries; retention/recovery remains owner-led.

Idle, turn completion, takeover, detach, API outage and grant expiry do not free
capacity. Possible-fork evidence is durable before launch. Failed/unconfirmed
fork or exit stays held. `Abandon` releases only a never-attempted reservation;
`ConfirmExit` requires matching process identity, root exit and group absence.
If the ledger launch transition or final admission check fails before the launch
callback is invoked, `Attempt` records a released tombstone and releases the
shared ledger hold under the admission lock, including on cancellation. Cleanup
errors are returned alongside the refusal. A crash before that proof is recorded
still retains the possible-fork hold.
Unknown exit cannot become running through a state update. A taken writer needs
confirmed exit and a fresh admitted epoch before structured hand-back. The
owning lifecycle updates/releases the server reservation separately with the
same verified proof.

`ChangeProfile` requires explicit current person/OPS authority and reserves the
same shared login before a qualified login/logout callback. Live/uncertain local
users and peer login holds refuse it. Unconfirmed completion keeps the ledger
hold and private recovery record, even if the login identity changes. A completed
change requires a newly resolved profile revision; it cannot change a live
process's profile. No credential copying, shared subscription pool or global
account swap is provided.

Same-conversation alternate-account resume is explicitly unsupported, including
changed enrollment identity under the same account ID. A stopped, authorized
successor on the same account needs fresh runtime/harness generations and an
increasing binding revision. Another account requires a new conversation and
separate current authorization, with no quota-evading rotation.

Named behaviour tests use admission/profile-change barriers, injected clocks,
fake transports/resolvers and isolated temporary stores. They cover root/worker
overlap at `max_parallel=1`, revocation after routing, retained idle/PTY/unknown
fork holds, final pre-fork refusal freeing the slot without a launch, immutable
profile/account binding, and current server
identity/context/model/account/host checks. Builders make no subscription/model
calls. No migration, permission, dependency or public contract change is needed.

Validation of source snapshot `c4cf99282`: both assigned Go package suites
passed through the guarded remote runner, as did
`TestSharedFenceCallerInventory`. Strict Go/web manifests passed. Guarded
`ci-static --merge-main` exited 0 with 42 checks and no skips. A detached
negative control removing final authority revalidation made the root/worker
revocation test fail as expected; the delivery branch retains the check.

The only R0 additions are ESSENTIAL rows for
`TestNativeAdmissionOverlappingRootWorkerAndRevokedRoute`,
`TestNativeAdmissionPreForkRefusalReleasesReservation`,
`TestNativeProfileChangeSerializesWithLiveUsers`, and
`TestNativeSessionFinalAdmissionRechecksIdentityContextAndCapacity`, plus
the `agentpairing.Lock` caller `agentaccounts/native_session.go`. The existing
native-source audit glob covers the package. No push to origin, deployment,
model CLI or review gate was run by this worker. I1/I2 wiring and `G_ACCOUNTS`
remain coordinator-owned.
