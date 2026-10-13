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

Both raw Expert and structured Events require the current C0
`events_supported` capability. Withdrawal aborts capture, clears retained
frames and open diagnostics, and disables Watch live; late callbacks remain
discarded. Restoring the capability enables the explicit action without
starting a stream automatically.

Event rows use harness/tool names, plain status and lifecycle labels, and
Approval or Question. C0/native event names remain in the details header with
the sequence, alongside native envelopes, correlation IDs, redacted paths,
omitted byte counts and receipt states. Message deltas coalesce by message ID,
finals replace their text, and tool start/result pairs share a call-ID row.
Character counts, tool durations and quota percentages occupy the metadata slot
without truncation; narrow panes show them in details. Inferred reply
correlation stays labelled in details. Review emits the exact approval
for the existing Decision Desk; this component never approves a request.
Copy JSON is a deliberate authorized reader action, not automatic persistence.
Quota descriptions name their scope and explicitly show limited, stale,
unknown or unsupported readings. Only context quotas use “% of context”; other
scopes use “%” alongside their scope description. Raw quota envelopes stay intact.

The local tail holds at most 500 frames and 512 KiB. Eviction, missing sequence
numbers, truncated pages, cursor failures and transport gaps are visible.
Replay validates generation, binding, descending exclusive sequence order and
cursor progress. A generation change never attaches to a successor silently.
No raw frame or cursor is written to local storage or durable history. Type
filters use the existing per-person `agents:expert.types` server preference.

Controls sit above the scrolling list. One status line places the event count
and Load earlier beside Watch live / Stop watching. Notices use “events” and
plain words; raw reason codes remain in the JSON. Expanding a row or scrolling
up pauses Follow live. Jump to live appears only after new live events arrive
while the live end is out of view, with the number of new events. Replay pages
do not count as live arrivals. Earlier pages preserve the visible row's
viewport position. The type menu reuses `FloatingPanel` and 44 px rows: a
compact 240 px popover anchored below Types on desktop, a full-height sheet
only on phones. JSON wraps and scrolls inside
its code surface. Both themes use shared tokens and SVG icons.

The type sheet has a 44 px Done action that restores focus to Types. On phones,
the full-height sheet pins this action below the scrolling options and respects
both safe areas. Escape also restores focus; keyboard Tab stays inside the sheet.

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

Source ownership: the two components fall under
the existing S8 `web/src/components/**` audit coverage; the three library files
fall under S9 `web/src/**` (excluding components and views).
The CI fix-round brief assigns the missing registrations for
`web/tests/native-expert-stream.spec.ts`: all 12 cases are now registered in
the web tier manifest, and the browser source manifest assigns the spec once
to `stable-controls`. Identity, coalescing, bounded replay, earlier-page,
agent/revocation and terminal-slot cases are ESSENTIAL; the six viewport/theme
cases are NIGHTLY with the required new-case inventory. The shared metadata
change adds only this spec's rows and its scheduling note; existing tiers,
policies and assertions are retained. No routes, SQL, permissions, versions or
dependencies are added.
Tick owns the visual comparison with r2 + A1, and the coordinator owns the
cross-family gate and release. Browser fixtures use no model/account calls.

Initial worker validation: the guarded `remote-spec.sh` suite passed all 12 cases,
including control stability within ±0.5 px, viewport-preserving prepend and
explicit replay/stream barriers. The initial disabled-link regression failed
before the role fix and passed after it. The guarded static run returned 1:
37 checks passed, including TypeScript, lint, ownership coverage, tier tooling
and browser safety; web classification/shard checks failed because R0 has not
registered this spec. This is an incomplete release check, not a green static
result. The registrations are included in fix round 2; guarded static and
authoritative PR CI must be green before merge. The affected planner exited 0 with `FULL: complete browser-impact
map unavailable or stale for base tree`; authoritative full PR CI is required.

Fix round 2 reproduces the existing source-inventory regression on `b1bf2d0a`:
it fails specifically because this spec is missing from the source manifest.
The unchanged regression passes with the registrations. Targeted metadata
checks also verify the 12 exact case identities, essential selection on PRs
and merge groups, complete scheduled coverage, a single source owner,
canonical formatting and preservation of all previous registrations.
The guarded browser run on `a5d187573` passed all 12 cases in 6.9 seconds with
zero residual browser processes. The remote `ci-static --merge-main` run on
that commit exited 0: all 42 checks passed with no optional skips, including
the web shard/selector regressions, strict classification, audit, TypeScript
and lint. The final follow-up commit changes only this validation record.

Screenshot evidence uses `testInfo.outputPath()` and is copied back by the
guarded runner to `web/test-results/AEON-1270/`. Each theme/width case emits
`AEON-1270/zellij-{light,dark}-{390,1024,1440}.png` and
`AEON-1270/events-{light,dark}-{390,1024,1440}.png` inside its result directory.
The final worker status records the exact tested commit. Screenshots are
ignored and not committed. No Go package changed; P2d specifies no Go suite.

Fix round 3 extends the existing 12 registered cases. Its test-only negative
control `161186cc7` retains the product code from `1cd50600`: the raw-authority
case fails because capability withdrawal leaves the stream signal un-aborted,
and both 390 px touch cases fail because Done is absent. The other nine cases
pass. The temporary branch was removed; no negative-control commit is part of
the delivery branch.

The same spec covers structured capability withdrawal, discards late callbacks,
requires explicit restart after capability restoration, and measures Done and
selector positions with the shared stability guard. Phone taps and a 320 px
viewport-height check prove dismissal remains reachable while options scroll;
Done and Escape restore trigger focus. The two themes at each width also emit
`AEON-1270/types-{light,dark}-{390,1024,1440}.png`; wide-viewport type sheets use
the narrow-panel layout. Fix-round evidence is kept under
`web/test-results/AEON-1270-fix3/`. The worker status records the final tested
HEAD, browser/static results and any incomplete coordinator-owned checks.

Fix round 4 applies Tick's stream review to the same r2 + A1 design. The existing
12 registered cases cover the row words and metadata, singular event count,
plain gap/eviction/omission notices, Watch live / Stop watching copy, genuine
consent withdrawal from retained expanded diagnostics and an active stream,
viewport-aware new-event counting, and compact desktop Types placement. The
consent assertion runs before any capability withdrawal. Its capability case
then starts with newly retained diagnostics and a fresh active stream as well.

The viewport/theme cases use C0 fixtures for 15 mixed rows in generation 2,
including a tool start/result pair, context usage, Running/Idle/Needs you,
Started/Resumed/Interrupted and Approval/Question. No fixture is product data.
Each captures pre-watch, lock, mixed, jump, earlier and gap states at 390, 1024
and 1440 px in light and dark, alongside the existing details/Types/Zellij
shots. These ignored artifacts use `testInfo.outputPath()` and the guarded
remote runner's final output directory `web/test-results/AEON-1270-fix4/final/`.
The evidence set is `pre-watch`, `lock`, `mixed`, `jump`, `earlier`, `gap`,
`events`, `types` and `zellij`, each suffixed with theme and viewport width.
The complete mixed list is captured before resizing for the scroll checks;
scroll notifications and observed layout bounds establish the interleavings,
with no sleeps or weakened assertions. Deferred follow actions also re-check
the current Follow setting and component lifetime before moving the view.

The test-only negative control `e0fb5a175` changes only this spec over
`45cdcd5c`: nine cases fail for the reviewed row/metadata, event-language,
watch-copy, visible-tail Jump and desktop-popover defects. The three authority
and terminal cases pass, including the now non-vacuous consent checks. Soft
assertions collect independent regressions and still fail their case. The
scratch branch was deleted without putting its commit on the delivery branch.
The corrected source on `3975f1227` passes all 12 cases in the guarded remote
Chromium run, with zero residual browser processes. The final worker status
and ignored `validation.json` pin the exact final HEAD, matching screenshots,
browser/static results and coordinator-owned Linux/visual/release checks.

Fix round 5 changes only quota scope/state labels from the ap771 Medium.
The known context reading keeps Tick's “23 % of context” wording; account
usage uses a plain percentage, and limited/stale states stay explicit in both
the row and details. The existing coalescing case covers eight context/account
readings, including known, limited, stale, unknown and unsupported states.
The six existing viewport/theme cases check the real replay rows, unchanged
quota envelopes, readable metadata and control stability when opening details.
No test identity, tier, source inventory or assertion was removed or weakened.

The identical regression spec fails on the test-only detached control
`6d83c2ce6` over `eca481ff`; only the spec differs from that baseline, and this
control is absent from delivery history. On `5355738e6`, the guarded Chromium
suite passes all 12 cases in 12.9 seconds with no residual browser processes;
guarded static `--merge-main` exits 0 with 42 checks, zero skips, in 127.3 seconds.
P2d has no affected Go package or separate web unit suite; quota projection
assertions run through the real module in the browser regression case.

Final-commit evidence is refreshed under
`web/test-results/AEON-1270-fix5/final/`: `usage` and `usage-detail`, each at
390/1024/1440 in light/dark, supplement the 54 previously accepted shots.
The final worker status and ignored `validation.json` pin the evidence HEAD
and final checks. Linux browser CI, Tick comparison and all review/release
gates remain coordinator-owned. The existing policy-preview diagnostic
requires the coordinator's trusted verdict for the new HEAD.
