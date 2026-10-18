# Ticket worker scopes

The ticket-worker set is `nodes.read`, `nodes.write`, `comments.read`, `comments.write`, `search.read`, and `events.read`. Propose it with this public code:

```text
aeon-scopes:v1:comments.read+write,events.read,nodes.read+write,search.read:5a072a8d
```

Paste a code into **New key**, **Change scopes** (the Edit scopes dialog), or **Rotate**. It replaces the selection, leaves unknown or ungrantable scopes unticked, and explains each skipped scope. Review before confirming; a code is not a credential and grants nothing. Pasting never extends an agent's role. Rotation keeps the old scopes unless you explicitly select a different set, and saves the replacement and old-key revocation together. Every rotation rechecks the actor's permissions and the agent's existing role ceiling, even when preserving scopes. Creation, editing and rotation use the same role ceiling: workspace-role permissions plus project-grantable and self-service permissions (`kinds.read`, `profile.read`, `profile.write`, `authz.read`) held through project roles, intersected with the agent-grantable registry. A scope from a project role still requires that project's binding on each request; it grants no workspace access. Rotation never creates or extends a role, restores a removed workspace binding, or adds default permissions; project-only agents keep their existing project access.

Dialogs, sheets and modals share one size scale (AEON-730): S (440px) for confirmations, M (560px) for forms and results, L (840px) for editors and multi-column pickers (`--dialog-s/m/l` in `tokens.css`). Each shrinks with the window and never stretches beyond its step on wide screens. Actions sit under the title on desktop, primary first and only as wide as their labels; a label that changes (Create → Creating…) reserves its widest form, so neighbouring buttons stay put. Phones get a full-height sheet with the action bar pinned to the bottom. New and edited key scopes use one column below 600px of available body width and two from 600px. The frame is decided when the dialog opens; filtering or expanding a preview cannot change it. `web/tests/dialog-scale.spec.ts` checks each dialog's width against its step and every action's width against its label at 2560px, and the phone sheets at 390px. Confirmation actions fit their labels, wrap long words, and stack when the row cannot hold both. `web/tests/dialog-widths.spec.ts` checks label fit, column counts and control stability at 390, 1024, 1440 and 1920px in both themes, and saves the dialog screenshots under `web/test-results/`.

Role choices use a bounded, wider frame with one scrolling body and visible actions; phones use the full screen. Rows and the “What changes” area keep their size when a role is picked, and pointer selection does not scroll the clicked row. Paired runtime roles show computer identity names from their existing workspace bindings on the second line; other roles show their descriptions. A runtime role without a bound computer or description still needs a data follow-up, since the role payload has no host or creation date. Other floating choice lists scroll with their panel instead of a second small scroll box. Headings and role details use two lines; clipped names and details are readable on hover, keyboard focus or a touch hold through the shared clip-tip, and the selected role’s full decision context remains in the scrolling preview. Closing choice menus keep a separate **Read full name** action: tap, Enter or Space opens its full text; Escape dismisses that disclosure while leaving the choice menu open. Very long names use a bounded scrollable disclosure: Tab enters it, Shift+Tab returns to the read action, and forward Tab exits the menu. Short role actions retain the full person/project sentence as their accessible name. Enter on Cancel cancels without writing. The computer-name editor uses **Use registered name** rather than putting the hostname in its button. `web/tests/role-picker-layout.spec.ts` covers every role choice, both actions inside the panel, native keyboard cancellation, full-text access, constrained heights and stability in all eight floating list callers, with screenshots of all ten views at 1440, 1024 and 390px in both themes.

For the exact calls a CLI command makes, run `aeon scopes needed issue get/create/update/comment` or `aeon scopes needed "issue get" "issue search"`; `--json` returns the code and group/label/id list. With no commands, it covers get/create/update/comment/search. This works offline, derives permission names from the authorization route map, and tests the command table against actual HTTP calls. `issue get` reads the activity feed, so it needs `events.read` (**See history**) as well as `nodes.read`. The broader ticket-worker set above also includes `comments.read`; the current CLI reads comments through activity rather than a separate comment-read call. Project moves need `nodes.move` in addition to ordinary update scopes. Unsupported command forms are refused rather than guessed.

An authenticated scope-only 403 and `doctor` name the missing scope and its label and show a code to propose it. That single-scope code replaces the dialog selection too; tick any existing permissions you still need before saving. Anonymous, role, and project denials never expose a scope proposal. Codes use explicit sorted identifiers and a v1 CRC32 checksum, so registry additions do not reinterpret an older code. The checksum detects copying mistakes, not trust or approval.

Claude accounts offer **Show Aeon in your Claude status line** in Settings → Accounts.
Only that explicit opt-in lets the enrolled daemon install `aeon statusline` in
its private Claude home; an existing user status line is preserved. The command
prints one plan line and sends only the two supported quota windows to the local
owner-only agentd socket. The registering agent reports at most once per minute,
including across daemon restarts. The toggle requires `account.manage`. Disabling
removes only the matching owned entry, even after the executable leaves PATH or
its installation path changes or contains spaces.

Structured vendor limit hits settle managed runs as `vendor_limit`; sessions show
Throttled with the vendor's reset when known. Codex model-specific bucket signals
stop only the matching model; account-wide denials still apply. Known denied
windows are reported at 100%; missing bounds remain unknown. Accounts publish
`reading_support` and a tenant-keyed HMAC of a verified vendor account ID, never an
email, token or local path. Missing verified IDs leave the fingerprint empty.
Doors explicitly confirmed together share allowance windows, outstanding reservations
and vendor denials within the tenant; group membership and schedules stay on each
door. Reservations and launch validation recheck project fences and ticket pins.
Settings displays `aeon use <harness> <account-id>` so labels containing spaces or
shell metacharacters remain plain display text. The CLI still accepts labels;
only the local owner-only agentd resolves the selected account's environment.
Claude idle `get_usage` and Grok billing captures remain disabled until the coordinator verifies a
quota-neutral exchange and adds an exact-version, exact-binary capability; this
fixture implementation does not approve a production vendor capture.

Managed `aeon-agentd` Codex runs report fresh app-server thread usage to
`POST /api/projects/{projectId}/harness-sessions/{sessionId}/usage` with the
registered session's worker lease (`harness.worker`). Input includes cached
input; missing cache remains unknown. Thread totals become per-model cumulative
snapshots, with exact receipt retries and a five-second final flush. A model
change needs explicit usage-model or `model/rerouted` evidence and a matching
last-usage interval. Reroutes bind to the same turn; subsequent usage needs fresh
model evidence because reroutes apply to individual requests;
ambiguous or malformed captures remain provisional. Final usage requires the
acknowledged turn ID, one matching completed terminal, all counters known, and
owned child stop plus stdout EOF within a two-second drain deadline. One bounded
terminal candidate can wait for the start acknowledgement; duplicate terminals
or usage after a terminal invalidate completion. Failed start, stream loss or
drain timeout cannot publish final receipts. An archived generation (410) detaches
the reporter without signalling the process; existing run settlement continues.
Managed adapter completion and failed starts release their owned stdout reader
even when another writer keeps the pipe open. Cleanup suppresses further stream
callbacks and bounds the wait for an in-flight callback; local closure never proves EOF
for a final Codex receipt. `Stop` keeps its process-only role so an observer can
call it without waiting on itself.
Protocol writes, including startup and control frames, have a 20-second maximum
and honor shorter caller deadlines while queued or blocked in the pipe. A canceled
partial write closes the transport and cleans up its owned process group. Account
probes also clean up their own descendants and bound inherited-pipe waits. Vendor
launches bound stderr draining to two seconds after leader exit, including failed
ownership checks; cleanup signals the launch group before reaping its leader.
Codex quota capture applies its three-second budget to every protocol write. The
native Grok proxy rejects headers beyond 4096 bytes before buffering more input.
The local `run-agent` runner shares the accepted run deadline across the agent
and `--test-exec`; Stop cancels both phases and waits for owned process cleanup.
The reporter retains bounded normalized state in memory, never raw output or
worker leases on disk. Restart/crash recovery and external worker capture remain
separate work; an unavailable endpoint can leave the last snapshot provisional.
Cursor's managed ACP usage currently supplies cost only to run settlement, so
its session tokens remain unreported. No second CLI or transcript backfill is
used. Pricing stays in the API: `GET`/`HEAD /api/model-prices` requires
`harness.read`; price creation remains person-only. Reporters neither fetch
prices nor infer subscription/account coverage. Aggregate readers must not add
session usage to the overlapping managed-run token telemetry.

`paimos harness provenance` records hashes and version identifiers for explicitly
provided `--instruction` files (`AGENTS.md`, `CLAUDE.md`, or a skill's `SKILL.md`),
never file contents or full paths. Files must be regular, at most 1 MiB, and outside
private stores. On Linux and Darwin, supply a physical path: symlinks in any path
component are rejected. Darwin's root `/var` and `/tmp` aliases to `/private/var`
and `/private/tmp` are supported through a checked descriptor walk. A custom
symlinked checkout must be named by its physical path. Other platforms refuse
file hashing; `--show` and explicit prompt-template versions/digests do not read instruction files.
