# Attached-note integration slice (AEON-660, disabled)

This worker delivers the brief's first coherent slice: S2-3's one-attempt
broker and S2-4's credential-free local hook, with the corrected AEON-391/392
trust and consent prerequisites. Forward migrations 1251–1255 add metadata
and fences without altering published migrations or the release version.

A claim commits one immutable attempt before taking the volatile body; lost
responses, rollback, restart, revocation and expired offers cannot recreate it.
The paired exchange binds the attachment, grant, generation, daemon epoch and
hook epoch. `message_validate` freshly checks that exact attempt and current
consent without returning content or renewing the watch lease. Receipts report
shown/completed or uncertain evidence; they do not assert model reading.

The hook authenticates daemon executable bytes against its own compiled exact
release ceiling before sending request bytes. Public `hook-peer.json` only
narrows the kernel-observed process identity and cannot approve a substitute.
Each socket invocation exchanges a fresh challenge with the current generation
and epoch before its single offer. After fetching, the hook revalidates remote
consent and then rechecks local registry, kernel peer and deadline at disclosure;
local revocation is serialized with the final write. Reply bridges cannot
intercept explicitly attached sends before the volatile-message policy.

Both production hook/daemon release ceilings are empty. Feature switches stay
off, and ordinary pairing does not opt a computer into messaging. Native
Darwin/Linux signing and effective-settings/launch-chain qualification, harness
version coverage, AEON-395 UI (including F6 person-change draft clearing), and
AEON-404 independent helper messaging approval remain for the coordinator.
The concrete source adapter and protocol are tested; end-to-end helper consent
wiring and production activation are not delivered by this slice. No new
credentials are required or introduced. This disabled groundwork is not a
user-visible release benefit.

Validation on current release-123 main (2026-10-05): the approved remote runner
refused execution because Colima was stopped. Bounded, touched-package tests
were therefore run locally one package at a time; their results are recorded
in this worktree's ignored `tmp/aeon-660-validation` directory. The migration
allowlist, test-tier checks, web shard registry checks and source ownership
audit passed (34 tier checks, 22 shard checks, 33 ownership tests). Local checks
passed for the affected attachment/consent broker, hook, source adapter, CLI,
daemon command, installer/runtime authentication, managed-drain fence, inbox,
server shutdown/bootstrap, OpenAPI contract and signing workflow. Controlled
barriers prove 12 overlapping claims release one body and consent withdrawal
before disclosure releases none.

Fix round 2 rechecks the daemon's live key, scope and `harness.worker` permission
inside every message-operation transaction, under the existing fences,
including the separate body-release transaction. Server acceptance and native
hook output share one renderer: the 8000-character context and 10000-character
complete JSON limits include escaped content, owner notice and maximum-width
server metadata for all three supported events. Oversized output is refused
before a message, rate charge or delivery attempt is created.

The new revocation and output-boundary regressions fail against the reviewed
`44fdf894` production files via Go's source overlay; all 21 daemon revocation
interleavings fail on that baseline, including key/scope withdrawal between
claim commit and release. Fix-round validation remains local and serial, as
required by its brief; no remote test host, push or deployment is used. Fixed-tree
checks pass for the attached-message broker and hook packages, all attached-note
pairing cases, focused CLI/source-adapter tests and OpenAPI parsing. The updated
registries pass 45 tier checks, 24 shard checks and 33 ownership tests.
Full hosted CI, native qualification and consolidated release QA
remain coordinator gates; this worker neither pushes nor deploys.
