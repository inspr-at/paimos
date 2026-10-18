# Attach a running session (AEON-352)

Computer pairing connects agentd to Aeon once. Linking a running session to a
ticket is a separate approval for that process. The attach CLI and review show
**Computer paired · This session not yet linked** until activation; pairing does
not grant permission to share conversation text. `aeon-agentd attach --language de`
shows the pairing/session distinction and next-step guidance in German;
the browser uses English or German for that guidance according to its language.

From a separate interactive terminal on a paired computer, run
`aeon-agentd attach --setup-root PATH --pid PID --harness codex --project-id UUID --ticket-id UUID --transcript PATH`.
**Watch the conversation** is the default; the transcript must be a resolvable
physical file, as in the AEON-258 mirror. Choose **Status only (no conversation
text)** at the local prompt, or pass `--status-only` (no transcript needed), to
report status without reading or sharing conversation text. Missing or unsafe
transcripts never silently select status-only.

After reviewing the process and sharing mode, press Enter or `y` in the separate
terminal for the local check. The CLI then opens the prefilled approval page on
macOS and always prints the link as a fallback. `--no-browser` disables opening;
other platforms use the printed link. Opening the page only fills in the code;
the person must still review and approve in Aeon, followed by Touch ID when the
pairing requires it. Keep the terminal open while linked; Ctrl-C detaches.

On macOS, normal Terminal and Ghostty tabs and SSH terminals work with a
root-owned `login` or `sshd-session` leader; tmux also remains supported.
Ancestry and session-leader checks use kernel PID, parent, UID, start time,
session and TTY metadata without reading ancestor paths. The helper must have
a TTY and belong to the target's user. Neither process may be an ancestor of
the other, and ancestor UIDs must be that user or root. The complete process
graph is rechecked for PID reuse and reparenting on confirmation and polls.
The target still requires its full physical folder and image validation.
On Linux, ancestry uses the same metadata-only read: `/proc/<pid>/stat` and
the uid of the `/proc/<pid>` directory. Root-owned `sshd`, `su` and `sudo`
ancestors are acceptable. The selected target still requires its executable
and working directory.
Those checks are defence in depth. A program running as the same user can open
another terminal and request the review. On a Mac whose browser-approved pairing
pinned a Secure Enclave public key, attach approval requires that key's Touch ID
signature by default until the person saves a choice, even when the daemon
reports that Touch ID cannot run. Linux and older pairings without a pinned key
keep approval in Aeon. To allow an upgraded Mac without a graphical login or
usable Touch ID, explicitly save **Approve in Aeon** in
**Settings → Personal → Security → Session watching**. Saving Mac confirmation
fails closed where Touch ID cannot run. SSH to a Mac that can show Touch ID
prompts on that Mac's screen.
Run `GOMAXPROCS=2 nix develop -c python3 scripts/check-attach-ancestry-mutations.py`
on macOS to verify that the negative ancestry regressions catch removed guards.

Claude and Codex are identified from the kernel-observed running image,
so an exec wrapper or a vendor auto-update does not require re-pairing. On macOS,
the daemon verifies the running PID against Apple's certificate chain, the
Developer ID Application markers, and the built-in vendor Team ID and CLI
signing identifier on preview, confirmation and every poll. A vendor file
renamed over a foreign running binary cannot confer that identity. A Claude
process is refused (`harness_identity_unsupported`) when any NUL-terminated
string after the executable path — an argument, an environment entry, or an
apple-vector string — is a non-blank `BUN_*` assignment other than
`BUN_INSTALL`. The signed executable can run other JavaScript from those
variables and still keep the vendor signature. Unset them and attach again.
`NODE_*` assignments stay allowed. When the kernel omits the environment, or
the string area cannot be parsed, Claude is refused or reported unavailable,
because a missing or unreadable environment is not evidence that every `BUN_*`
variable is unset. Codex is identified from its signature alone, and the daemon
leaves its procargs unread. This environment check is a best-effort deterrent.
`KERN_PROCARGS2` copies the process's own rewritable string area; only the
argument count comes from the kernel. Code running in the process can rewrite
that area before attach, including a NUL that ends the string list early.
Reading the environment as it was at exec needs Endpoint Security, which
requires root and an entitlement, and is out of scope. The person's approval
remains the real gate. Cursor
attach is refused on macOS until a signed cursor-agent CLI exists;
Cursor.app's signature is not a harness identity.
Legacy Claude and Codex wrapper pairings work after upgrading and
restarting agentd. Unsigned Claude and Codex images are refused on macOS,
including when run through Rosetta. Linux uses a local installation
root plus owner recorded at pairing or by `repin --harness claude`,
`add-harness --harness codex` or `add-harness --harness cursor`; restart agentd
after recording a fallback identity. Unknown layouts retain only the approved
exact-file pin. Pairing retains recorded roots after removal of an old version,
bound to the same pairing and account. A recorded owner and root must match even
when the running image still has the old exact path.
The daemon does not interpret or execute wrappers to discover an install root.
Every image and ancestor must satisfy the existing ownership and permission
rules, and confirmation and polls recheck the image. Local HTTP 409 diagnostics
include `harness_identity_mismatch`, `harness_executable_unsafe`,
`harness_image_changed`, `harness_identity_unavailable` or
`harness_identity_unsupported` with a fixed repair, retry or unsupported-harness
hint; the attach client displays them. Signature checks run outside the manager
lock. Startup drops only fallback identities that cannot be re-derived from
the approved installation; signed images and other harnesses remain available.
Refresh validates new fallback identities against their approved installations.

Identity regressions cover wrapper pairing upgrades, native exec
chains, vendor updates, Linux root fallback and its repair, unsigned Rosetta
image refusals, signature failures, writable installations, real
running-process rename-over attacks,
verification timeouts and concurrent detach, and local 409 diagnostics.
On macOS, run `GOMAXPROCS=2 nix develop -c python3 scripts/check-attach-identity-mutations.py`
to remove each guard temporarily and require a failing regression; the script
rejects build failures as evidence and restores each source file. Real codesign
checks also probe running installed vendor binaries; a harness with no running
process is reported as skipped, while fixture signature checks still run.

Review the kernel-observed process, physical folder and chosen mode, then press
Enter or `y` for the local check. The helper says where to approve: the page, the menu
entry, the nine-digit code and how long it lives, plus a link
(`/agents#attach=<code>`) that only fills the code in. **Agents** also lists the
waiting request (computer, harness, expiry) with a **Review** button, and says so
when it was approved, expired or cancelled. The approval screen shows the selected
mode. The computer owner approves the exact snapshot; the code expires in ten minutes.
Both modes require a consent digest and single-use approval (repeat approval
returns 409). Keep the terminal open: peer-checked polls renew a 60-second lease.
Revocation, identity changes and lease expiry require a fresh approval. A stopped
watch is detached; lost contact is unreachable; only a kernel check confirms exit.

Conversation watching shares only new turns after activation with people
explicitly granted `harness.watch` in the project. Status-only (`snapshot.mode=lease`)
opens no transcript, rejects conversation text and has no conversation viewer.
The project permission remains off for all built-in roles and is never implied
by `harness.read` or `nodes.read`. Both attach modes require protocol 2 and
`local_consent_proof_version=2`, negotiated at startup registration. A
protocol-less older daemon still registers and keeps serving work; its attach
requests receive HTTP 409 `update_agentd` and cannot create a watch or lease.
An AEON-460 protocol-2 daemon with an omitted or v1 proof version is refused at
registration with HTTP 409 `update_agentd` and “upgrade paimos-agentd” guidance,
before approval or Touch ID; ordinary work continues with attachment disabled.
An older terminal helper shows `local lifecycle request rejected`
(the daemon's local refusal is `paired instance refused attach`), rather than
the server's update message. Upgrade `paimos-agentd`, restart it and give fresh
approval. Existing pairing capabilities and Enclave keys remain valid; the proof
format upgrade does not require re-pairing. The updated daemon requires the
server's v2 acknowledgement and refuses missing or unknown proof versions.
A newer daemon connecting to an older server receives HTTP 400 on attach
registration because that server rejects an unknown `attach_protocol` or
`local_consent_proof_version` field.
The daemon logs the server's refusal, disables attach and keeps serving work
and local control. The updated helper shows version-repair guidance on the
existing authenticated, kernel-checked socket; unauthenticated callers only get
the generic auth refusal. After updating the server, restart agentd
to retry failed startup registration. Startup refusals still require this explicit
repair. Once registered, a running daemon can recover a registration lost during
a server restart, as described below.

Owner refusal guidance distinguishes incompatible attach versions, a pairing
that no longer authenticates, unavailable project/ticket access, draining or
removed harness enrollment, an expired code, lost daemon registration, transport
failures and the distinct computer, attempt, registration and approval limits.
Local startup failures have separate guidance: a computer-wide disconnect needs
settlement followed by fresh pairing, completed cleanup needs fresh pairing, and
an origin or workspace mismatch needs the approved configuration or fresh pairing
for the intended instance and workspace. Other local startup failures direct the
owner to `aeon-agentd status`, the agentd log and a restart when owned work permits;
they are not reported as connection failures. Admission and limit hints share the
same per-computer cap and attempt window.
Server errors preserve `code` and `error`
and add `attach_refusal` only after checking the computer proof and principal
(or the signed-in person owner), with the narrowly scoped `poll_key_unknown`
exception described below. Fixed English/German hints name the next action;
unknown causes direct the owner to daemon status and the administrator's server
logs. Arbitrary server text never becomes terminal output. Revoked HTTP
bearers stay unauthenticated; pairing repair is offered only by the locally
authenticated interactive helper. Poll refusals still detach and clear local
state, and no uncertain conversation submission is retried.

Recovery reads the long-lived computer lifecycle proof from the approved setup
store for each registration attempt; the transport retains the memory-only poll
key and pinned origin, without retaining that proof for the daemon lifetime.
Startup obtains the host and registration proof together in one read. Store or
Keychain stalls can leave at most one authority reader in flight per daemon;
cancellation or the total deadline releases the transport gate, and abandoned
results are discarded rather than used by a later registration.
Local cleanup, disconnect or configuration changes refuse recovery before a
registration is sent. Unknown-key diagnostics use a separate server-local budget
of 30 attempts per minute per authenticated computer principal before computer, scope or watch
lookups; they never consume the tenant attach request budget. The server holds
at most 4096 live budgets across tenants. As an accepted AEON-608 limitation,
new principals (including those in another tenant) are refused until a slot
expires when that shared cap is full; existing budgets retain their counters.
A capped call returns `attach_recovery_limited` with
`Retry-After` seconds, without authorizing registration. The daemon retains
the server refusal and performs no exchange or registration until that delay
expires (valid server delays are bounded to one day). If the explicit
`poll_key_unknown` refusal reaches the helper during cooldown, run attach again
in a few seconds and give fresh approval; a daemon restart is not required.

After a server restart, agentd automatically re-registers its memory-only poll key
only on the explicit `poll_key_unknown` refusal. The server issues that signal
only for the authenticated principal of a connected computer with valid attach
scope; incorrect keys, revoked pairing, draining/removed enrollments, ended watches
and foreign tenants remain refusals. Older servers without this signal still
require an agentd restart. This replaces the blanket no-in-process-retry policy:
a confirmed rejection before mutation is safe to replay, while a failed or
uncertain exchange is not. Each call attempts registration at most once and
replays once, with one exchange/recovery in flight, a 20-second total deadline,
and exponential jitter windows of 500 ms to 8 seconds (actual delay: half to the
full window). The same delay is retained as a cooldown after completion or failure;
calls during cooldown return the refusal and there is no background retry loop.
Registration still ends earlier approvals and watches. Run attach again and give
fresh approval for an ended watch; new requests work without restarting agentd.
Re-pairing is not required for lost registration. A local `harness claude draining`
report alone does not block attach: the attach manager does not use the supervisor's
launch fence. A server-side draining or removed enrollment does block a fresh
attach, including `--status-only` for an already running process. That operation
creates new session/consent authority; draining preserves previously owned work
for settlement, not new attachment authority. For a harness drain,
`aeon-agentd add-harness` can create a new enrollment while the old one drains.
For a computer drain, let owned work settle and pair the computer again.
Restarting agentd does not undo a server-side disconnect.
The paired computer's tenant-scoped workspace is the hard cwd allowlist; neither
`AEON_URL` nor local request fields can override the paired origin. Same-user
processes are not isolated by this feature.

Security regressions live in `internal/agentd/attach_lease_test.go` (injected
commands, PID/executable/cwd changes, explicit mode choice, status-only without
transcript I/O, expiry and offline teardown in both modes),
`internal/agentpairing/attach_lease_test.go` (atomic approval, isolated session
leases, expiry/revocation, text refusal and cross-tenant RLS/404), and the
platform-specific process tests (native macOS/Linux kernel identity and exit).
`cmd/aeon-agentd/paired_serve_test.go` verifies the paired-origin pin against an
alternate remote, `AEON_URL` and HTTP redirects before either mode
can attach. It also runs the real serve loop through registration refusals,
checks that work polling and local control continue with attach disabled, and
restarts against an updated server to recover attach. Server regressions verify
that protocol-less registrations keep daemon identity usable while every attach
operation requires an update, even if later requests claim protocol 2. Consent
proof regressions reject v1 and unknown versions before approval, reject a v1
signature, and accept v2 using the unchanged browser-pinned pairing key.
`TestAttachModesProtectionMatrix` checks consent, mode tampering and terminal
states in both modes, sending text on refused watch polls. Early text at pending,
discovery, local-confirmation and activation stages detaches without a session.
Darwin tests require ESRCH before a missing or mismatched PID counts as exited.
The status-only text regression backdates the poll clock and observes the relay directly, so rate
limiting cannot hide a missing content guard. The approval browser spec covers
both modes and consent policies at 1600/390 pixels in light and dark.
The reporter contract is `harness-session/2.7`, declared by the response-only
`Aeon-Contract` header. Existing reporters keep working without a Pharos or
Janus release; registration and heartbeat requests need no contract header:
existing state values stay intact; optional `watch.process_state` carries a
confirmed exit. The existing default-off permission and code-attempt-cap tests
remain in `internal/agentpairing/watch_test.go`.
