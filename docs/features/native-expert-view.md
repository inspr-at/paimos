# Native Expert view

AEON-1270 adds the independent Expert view from the approved AEON-1264 r2
attachment and A1 live-terminal amendment. I2 owns its mounting in the shared
session panel and the session-head menu. It is never an automatic opening tab.

`SessionExpert` accepts the C0 `NativeSessionView`, current `ExpertViewer`,
existing server-approved `ExpertConsent`, name, host, optional registry model
profile, active-tab flag and a `NativeExpertTransport`. Model names use
`modelsSimple.fullName`; I2 must use that same function for Recent runs.

The transport in `web/src/lib/nativeExpert/ports.ts` is an integration port,
not a second native runtime. I2 supplies authenticated C0 attach/fetch-SSE and
events calls. Attach uses `mode: view` and the exact binding and consent; SSE
uses `X-Aeon-Viewer-ID`, the exact expected runtime/harness/binding revision,
and `Last-Event-ID` from the request. No capability or token goes in a URL.
`events` requests exclusive `before`, current generation and a limit of 200;
the transport must bound bytes before JSON decoding and honor abort, current
permissions, consent, lease expiry and detach. Aborting releases a viewer,
never stops the session. Both methods return only the server-filtered C0
projection. C0 routes remain disabled until qualified integration.

Watch live is explicit. Raw Expert frames require a person with current read
and watch permissions and matching active owner consent for the exact native
binding. Readers with structured permission can see Events without raw Expert
or PTY frames. Agents have no Terminal segment or viewer link. Identity,
binding, generation or consent changes abort capture and clear the RAM tail;
returning to a tab or visible page requires Watch live again. Late replay and
stream callbacks cannot populate another view. Server revocation blocks
restart until the current authority projection changes.

Events retain C0 names, native envelopes, correlation IDs, redacted paths,
omitted byte counts and receipt states. Message deltas coalesce by message ID,
finals replace their text, and tool start/result pairs share a call-ID row.
Inferred reply correlation stays labelled. Review emits the exact approval
for the existing Decision Desk; this component never approves a request.
Copy JSON is a deliberate person action, not automatic persistence.

The local tail holds at most 500 frames and 512 KiB. Eviction, missing sequence
numbers, truncated pages, cursor failures and transport gaps are visible.
Replay validates generation, binding, descending exclusive sequence order and
cursor progress. A generation change never attaches to a successor silently.
No raw frame or cursor is written to local storage or durable history. Type
filters use the existing per-person `agents:expert.types` server preference.

Controls sit above the scrolling list; expanding a row or scrolling up pauses
Follow live. Earlier pages preserve the visible row's viewport position. The
type menu reuses `FloatingPanel` and 44 px rows. JSON wraps and scrolls inside
its code surface. Both themes use shared tokens and SVG icons.

Terminal availability is fixed on panel opening. A person with a viewer URL
gets the A1 plain Zellij block with Open live terminal, a new-tab link with
`noopener noreferrer`, and the first-visit hint. Losing the URL disables that
same link; both hint strings reserve the same height. The URL must be HTTPS
without embedded credentials, query or fragment. No iframe, token handling or
vendor login intermediating exists here. When a taken PTY and viewer URL
coexist, the terminal slot holds P2e's read-only renderer; the viewer link
belongs only in the existing head menu. I2 uses the exported `liveTerminalLink`
descriptor, whose label is Open live terminal, with terminal and external-link
SVGs. The slot is withheld without current person/watch/consent authority.

Coordinator handoff (R0, same PR before merge): assign the two component files
and `web/src/lib/nativeExpert/{ports,buffer,rows}.ts` to the Agents UI audit
slice; register `web/tests/native-expert-stream.spec.ts` in the web tier and
Agents shard manifests. Those shared metadata files are outside P2d's exclusive
write set. No routes, SQL, permissions, versions or dependencies are added.
Tick owns the visual comparison with r2 + A1, and the coordinator owns the
cross-family gate and release. Browser fixtures use no model/account calls.
