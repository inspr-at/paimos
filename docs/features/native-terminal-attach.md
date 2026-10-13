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
generation failures close local authority. Planned five-minute stream rotation
and the client rotation deadline resume the same viewer from the last accepted
`generation:seq` event ID, retaining the gapless sequence guard and controller
lease. The stream never automatically reconnects across a generation; gaps,
other end reasons and unexpected disconnects report lost live continuity.
The server must separately recheck access on every frame/keepalive.

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

Source ownership and the slice's Go test registrations are included in this
PR. The rotation regression is registered as `ESSENTIAL`. No routes,
permissions, migrations, DSAR fields, web shards or tenant/tree lock callers
are added by this slice.

Run the committed source through the approved remote runner for
`./internal/nativeattach/... ./internal/cli/...`; then guarded static
`--merge-main` and the affected-set planner. Browser checks do not apply.
The coordinator owns cross-family gates, push, release and live verification.
