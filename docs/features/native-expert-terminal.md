# Read-only native terminal

AEON-1289 supplies the Terminal pane for a taken native session. This is a
bounded, transient view of the holder's PTY. Structured sessions continue to
use Events. A person needs current watch permission, an exact binding, an
approved terminal export capability and active consent from the session owner.
Agent principals never mount the terminal. The view grants no control authority.

## Integration owned by I2

P2d exports `SessionTerminal` from `web/src/lib/nativeExpert/views.ts`. Mount
`NativeTerminal.vue` in its `terminal` slot. Pass the slot's session, viewer,
consent and model name, plus the panel's active state and layout. Use the existing
panel's content edge: `--session-content-inset`, default 12 px on desktop and
8 px on phones. The root needs a bounded flex parent with `min-height: 0`.
The terminal fills the remaining pane and scrolls internally.

I2 alone adds and pins the maintained `@xterm/headless` dependency and supplies
`createXtermRenderer(Terminal)` from `web/src/lib/nativeTerminal/renderer.ts`.
This slice changes no dependency or lock file. The adapter uses xterm's public
buffer API with `allowProposedApi`; the pinned package must pass I2's real-parser
qualification. A missing renderer presents an unavailable state and opens no
stream. No CDN, alternate parser or unqualified fallback is loaded.

The separate `NativeTerminalTransport` port requests C0's `view: terminal`,
`mode: view`, with an immutable binding, generation, revision, principal and
owner-consent ID. `lastEventId` is always null: reconnect starts a fresh grid.
I2 provides authenticated attach/SSE and must preserve per-frame and keepalive
authorization. Abort detaches only this viewer. There is no input, paste,
terminal reply, resize, native resume or control-capability property in the port.

The control line uses I2's current `TerminalControl` display metadata and
`TerminalActionView`. Actions carry the expected binding, revision and controller
epoch. A click emits an immutable `TerminalActionRequest` only while these still
match the record on screen. I2 owns confirmations, current permissions and the
final mutation; the renderer never performs a control action.

Only an exact web-holder actor, client ID and controller epoch may emit a
`HolderMeasurement`. Measurement waits for `document.fonts.ready`, debounces
150 ms, suppresses unchanged grids, and pauses while hidden, inactive, at risk
or pinch-zoomed. Re-entry or a regained lease re-applies the measurement. Bounds
are 40 × 12 to 320 × 100; small holder panes use 11 px cells before internal
scrolling. Window, splitter, font, orientation, visual viewport and panel-layout
changes trigger measurement. P2e sends no PTY resize: I2/P2c must recheck the
current local holder before applying TIOCSWINSZ. Another tab of the same person
and a CLI holder remain viewers here.

I2 owns sidebar and Full screen controls and remembered layouts. On a phone in
Full screen, pass the header's control-slot element as `controlTarget`; the same
line moves there without creating another control. Phone holders have a pinned
key row with safe-area space. Its keys are disabled because browser input is
outside this read-only slice. The local holder's terminal stays the writer.
The panel's shortcut registry should consult `isHolderTerminalTarget` before
Esc, F or numbered view shortcuts. This view absorbs a holder's Esc without
forwarding it; other browser and OS shortcuts remain native.

## Export and lifecycle bounds

The adapter never opens xterm's interactive DOM, registers `onData`, `onBinary`
or `onResize`, installs addons, link providers or clipboard handlers, or paints
a caret. A streaming escape allowlist discards OSC (including 8 and 52), DCS,
APC, PM, SOS, device/status queries, window operations and input reporting modes.
Fragmented controls and UTF-8 are handled across chunks. Only render controls
reach xterm. Cells paint through `textContent`, never HTML or navigable links.
Terminal-specific light/dark palette values follow the r5 reference; the frame
and controls use existing PAIMOS tokens and SVG icons.

Encoded frame limits are checked before base64 allocation; the exported-frame
ceiling reserves envelope space. Grid dimensions are bounded at 320 × 100.
The parser retains the current screen only (`scrollback: 0`). Queued decoded
bytes stay at or below 512 KiB and 64 frames; the parser has a three-second
write deadline. At 512 KiB of consumed data its screen is discarded with an
explicit incomplete-view notice, bounding even combining-glyph accumulation.
This is not complete scrollback. No terminal text, screenshots, downloads or
history are stored by production code.

Binding, generation, person, owner, consent, permission, capability and epoch
changes dispose the parser and erase its DOM and pending data. Hidden views
also stop capture. Late frames and parser callbacks cannot revive discarded
text. A gap, invalid frame, lost sequence or connection failure clears the grid
and requires an explicit fresh watch; it never pretends to be complete history.
Retired terminal epochs cannot replace the current grid. Error bodies from the
transport or parser are never rendered. Owner consent is never created or
renewed by the component.

## r5 presentation and verification

The control line is 36 px, plain except for an at-risk lease, with a 28 px action
and shared left/right edges. Desktop gaps are 8 px; phones may drop the gap below
the control line. The grid sits at the bottom-left. Free space remains dotted,
with one 1 px hairline only on the grid sides facing dots (top and/or right).
Viewer-only Fit defaults to actual size (13 px, shrinking only as required down
to 10 px). Fit scales to the available width, capped at 16 px. It is remembered
through the existing person preference `agents:terminal.fit`, separately for
phone/short screens and larger devices. Save failures appear in the existing
preference failure state and the control tooltip.

`web/tests/native-expert-terminal.spec.ts` covers this new untrusted-output
boundary: malicious and fragmented escapes have no outbound side effects;
bounded backpressure and late callbacks erase rather than revive text; identity,
consent, binding, epoch and reconnect gaps stay fenced; holder measurements use
an injected clock and explicit samples. A separate NIGHTLY stability/evidence
case checks Fit, dotted-facing edges, long German action labels and phone controls
using the shared ±0.5 px guard. Screenshots contain test fixtures only and use
`testInfo.outputPath('AEON-1289/...')`.

Source ownership is already covered by S8 (`web/src/components/**`) and S9
(`web/src/**` excluding components, plus tests). The three authority/bounds
cases belong in ESSENTIAL; the viewport/evidence case belongs in NIGHTLY, with
one four-case row in the web shard manifest. No API, permission, database,
migration, release-version or model-account change is required.

Release stays behind `G_UI` (including Tick's visual check),
`G_TERMINAL_EXPORT`, I2's dependency/stream/panel wiring and authoritative PR CI.
This source slice does not enable a live terminal export, assert secret
redaction, or advertise qualified provider support.
