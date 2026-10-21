# Owned session panel

## AEON-1291 core wiring: partial build

The command tree now includes `aeon session attach OWNED_ID --project KEY`,
`aeon session take OWNED_ID --project KEY` and the identical
`aeon attach --session OWNED_ID --project KEY` alias. Take's existing
`--hand-back` option is registered too. Attachment uploads with
`aeon attach ISSUE FILE`, attachment subcommands and attribution
`aeon session start` retain their existing meanings. Registration does not
qualify the daemon's still-unconnected native attach/take transport.

With the existing default-off `serve --native-sessions` option, the harness
adapter wraps AEON-1593's production store and relay. It adds public read-only
attach, renewal, detach and current viewer authentication. Control attaches
remain unavailable until the private capability transport and final command
write fence can be connected. Native input retains the server's chat-disabled
gate. No launcher, provider enrollment, tool ceiling or host setting changes.

Viewer delivery holds the existing tenant/tree/pairing fence while checking
the original person's current native consent, exact runtime binding, enrolled
computer/account/key, composition digest, actor-bound viewer and bounded lease.
Both replay and stream admission enforce the current audience. Terminal and
Expert are limited to the run's person and workspace Owner/Admin; a project
Admin alone does not confer export access. Separate watch permission and a
qualified explicit owner export consent are also required. The released
composition supplies no such export-consent adapter, so those exports remain
unavailable. The policy tests use an explicit fake qualified export.

The take-authority predicate permits the run's person and project Admins,
including workspace Owner/Admin, and refuses agents and API keys. This build
uses that predicate for existing controller authority checks; it does not
implement a successful take or hand-back, and emits no fictitious audit entry.
The panel uses the server's capabilities when offering Terminal. A `taken`
runtime status by itself cannot make that tab appear.

Shared live snapshots omit terminal URLs and account quota, and live relay
pages/streams omit account-quota frames. The separately authorized session
read retains owner-only quota; workspace administration alone does not expose
it. Replay writes and stream writes have explicit deadlines. No progress/ETA
source exists in the owned native process/receipt/heartbeat seam: the existing
daemon heartbeat carries activity/doing, but has no owned-session progress or
ETA value to forward. No new estimate or progress mechanism is introduced.

**Incomplete acceptance:** an integrated input → take → terminal → confirmed
hand-back → fresh epoch → recovery scenario cannot yet run. The production
store's command selector currently supports only input, interrupt and stop;
its ready receipt pins one runtime digest. It has no successful successor
epoch commit for take/return/resume. The current consent authority explicitly
admits only the person-invoked coordinator with a null run ID. Completing
those paths needs coordinated ownership of
`internal/nativesessions/store/{commands,production,claim}.go` and
`internal/nativesessions/consent/session.go`, which this brief excludes.
The daemon's native attach/take/read/recovery methods and local person-proof
binding also remain to be connected after those server seams exist. Existing
AEON-1593 account/profile/quota/start-to-exit composition is retained.

R0 additions in this build are the ESSENTIAL Go rows
`internal/harness::TestNativeSessionCommandAliasesPreserveLegacyHelp`,
`internal/harness::TestNativeSessionWiringKeepsChatDisabled` and
`internal/nativesessions/stream::TestCurrentViewerAuthorityScopesEveryDelivery`,
plus the `agentpairing.Lock` caller entries `harness/module.go` and
`nativesessions/stream/current.go`. The existing `internal/native*/**` audit
ownership covers the two added stream files. No migration, permission,
contract-schema or browser-test-identity row is added. The existing browser
case gains the withheld-capability regression and six viewport/theme captures;
it keeps its name, tier and timeout. This is wiring of the existing panel;
the later takeover/Expert UI still needs its approved Opus design and Tick check.

Validation so far: targeted command/authority/fence checks passed remotely.
The affected-package run on `30c451bfa` passed nativeintegration,
agentaccounts, reportercontract, harness and stream. Agentd and CLI failed
on the agm2 fallback: `/bin/true` and `/bin/sleep` are absent, with additional
probe/version/provenance fixture failures. These tests were not changed or
skipped. Browser and static runs returned exit 3: mbp2606 was not GREEN.
Final follow-up validation is recorded in `.agent-status.json`; this partial
build must not be treated as completed acceptance or a release qualification.

## Existing panel behavior

The session panel has one menu: Details, Chat, Events and, when advertised at
opening, Terminal. A project's bound lead has Lead first. Existing links and
saved choices using `session`, `overview` or `messages` remain readable.
Events and Terminal never become an automatic opening choice. Live updates do
not change the opening menu or pull the reader to another pane.

The condensed header contains the session identity, the menu, Sessions list,
Full screen, More session actions and Close. The identity's tooltip includes
the complete ticket title, state, control status and registered model name.
Narrow panels and phones put layout actions in More. Phones keep More and
Close in the first row, with the menu below. In phone full screen the menu is
available from More.

Sessions list shown, list hidden and full screen apply to every pane. Layout
preferences belong to the signed-in person and are separate for phones/short
screens and larger screens. A failed save leaves a visible explanation that
the layout currently lasts for this visit. Selecting Terminal on a short
screen enters full screen without saving that automatic choice.

Arrow keys navigate the menu. Number keys 1–5 follow its visible order.
Shift+E enters or leaves full screen; F and / retain session search. Escape
leaves a field before closing, and leaves full screen before closing the
panel. These keys never override a focused holder terminal or browser/OS
modifier shortcuts. Close shows Esc in its tooltip.

All session actions live in More. Existing managed Name, Model, Effort and
Steer editors retain their ownership, catalog and receipt checks. Model names
in Details and Recent runs use the same full names as the Models registry.
Live changes above a scrolled Details reader preserve the visible content's
position, including browsers without native scroll anchoring.

Events mounts the existing bounded, privacy-filtered native event view. A
structured viewer requests only `mode: view`; it never requests a controller
capability. Replay includes the current generation, and stream attachment
uses the authenticated viewer header. Missing or disabled native endpoints
produce an unavailable/incomplete state. After a session has been identified
as owned, a failed context refresh cannot enable legacy mutation controls.

Terminal mounts the existing read-only native terminal pane or the advertised
Zellij link. Zellij opens in a new tab with `noopener noreferrer`.
This UI slice does not grant watch consent, provide a terminal parser, enable
browser input/resize, or implement the native control/runtime routes. Those
paths remain unavailable with a reason until their separately qualified
integration is supplied. Take, Hand back, End control, owned-session Chat,
settings, recovery and removal cannot claim success through a legacy route.

No provider/account qualification, release gate or live cutover is performed
by this panel change. Core wiring and Tick's visual check remain owned by the
coordinator.

## Response validation and compatibility

The native-context lookup is a fixed operation in `agentRows.ts`, alongside
the other session sub-resources. The panel bounds response bytes before
decoding JSON as unknown, checks the context and read-only viewer grant, and
validates replay pages and stream envelopes against the current binding and
generation. Malformed responses leave an explicit incomplete view; a known
owned session never regains legacy write controls after a failed read. Stream
failure detaches only the read-only viewer.

The lead-panel regression checks follow the approved A3/A4/A5/r5 design:
Session and Overview selectors target Details; the visible opening-reason
line is replaced by the accessible explanation and the actual question or
focused Next up content. Header stability measures Panel actions and More
session actions. Pause and the default-Lead choice are checked inside the
overflow, including persistence and removal of the saved choice. The model
check reads the identity tooltip's full name. The historical test names stay
stable, and no case is skipped or given a longer timeout.

Core wiring, authoritative CI and Tick's visual check remain coordinator
release responsibilities.

## Fix round 10: opening width and readable header

With no saved list width, opening a session uses Focus: a 320 px list and the
remaining width for the panel. A saved divider width takes precedence, including
after reopening. Overview remains available through the existing preset cycle.

The header measures its complete menu, glyph, actions, spacing and padding on
opening and resizing. `PANEL_HEAD_TITLE_MIN` reserves 200 px for the heading;
the resulting header breakpoint is stored in `--panel-head-labels-at`. This
accounts for the actual tab count and whether the sidebar action is present,
instead of hiding labels below a fixed 1000 px container width. Selection,
hover and unread changes do not choose another menu form. The key stays whole,
followed by a literal ` · ` and the title; the title gives up space first.

Owned actions remain disabled. Their menu reason reads “Only via aeon on the computer that runs it”;
Pause reads “Pause and resume aren't here yet”. Existing permission, consent
and ownership checks still determine the available actions. Reasons paint on
one line from 360 px; at 320 px they may use up to two lines inside the existing
menu width, without moving its controls.

The existing `native-session-program-1440.spec.ts` cases add the unsaved Focus
width, a real persisted divider drag and full-key checks. The existing
`native-session-program-1024.spec.ts` cases add visible labels and measurements
of the title and action geometry across the measured breakpoint. The existing
`native-session-program-390.spec.ts` cases add the full key with a clipped long
German title. All three files add exact disabled-menu copy and menu-opening
stability through shared assertions in `native-session-program-fixtures.ts`,
in light and dark. The phone cases retain the one-line assertion at 360 px and
check the two-line limit and stable menu width at 320 px. Earlier assertions,
test names, tiers and timeouts remain.

## Fix round 4: approved test requalification

The lead's AEON-1309 grant replaces retired panel expectations with the
approved A3/A4/A5/r5 surfaces. Existing test identities, tiers and timeouts
stay unchanged. No assertion is removed without checking its replacement.

| Granted file | Old expectation → approved replacement |
| --- | --- |
| `web/scripts/ci-web-shard.test.mjs` | A single uncharged panel spec in the historical budget → six independently packed files, all nine declared cases and their explicit added weight; the historical `< 300` assertion and twelve shards remain. |
| `web/tests/agent-recovery.unit.test.ts` | An unbalanced substring fixture and standalone action buttons → whole parsed header/control/evidence elements, real Details/Chat/Events/Terminal tabs and overflow actions/settings, all preceding changing diagnosis in both ownership modes. |
| `web/tests/no-shift-agents.spec.ts` | Visible Interrupt/Stop/Steer buttons → those actions in More; measure the persistent header actions, More, Close and tabs through receipts and confirmation, plus the same phone sheet footer. |
| `web/tests/agent-pause.spec.ts` | Standalone panel Stop → overflow Stop; retain the exact pause request, row state, no-exit claim and unchanged row/header geometry checks. |
| `web/tests/lead-flow.spec.ts` | Standalone lead Pause and its phone target → overflow Pause and the phone More target; preserve revision/generation, confirmation, pinned sheet actions and accessibility checks. |
| `web/tests/session-chat.spec.ts` | Overview and its hidden zero-count badge, standalone Interrupt and Session actions → Details, absence of a zero badge with Chat's accessible name, overflow Interrupt and Panel actions; retain watermark persistence, private drafts, exact receipts and geometry. |
| `web/tests/aeon-1071-chat-live.spec.ts` | Standalone Interrupt state/click/geometry and Overview → explicitly open More to check/click the actual Interrupt item, measure its persistent header trigger, and select Details; retain all streaming and refusal assertions. |
| `web/tests/lead-adoption.spec.ts` | Standalone Pause measured during history scrolling → verify overflow Pause is disabled while adoption is pending, Resume is absent, and Pause is enabled after adoption; then measure More and Close through the same scrolling. Pending cancellation and identity checks remain. |
| `web/tests/agent-recovery.spec.ts` | Overview, the inline managed-control frame, and absent recovery buttons → Details and Panel actions remain stable; check actual Restart/Reconnect menu items are absent when unauthorized/offline. Exact requests and outcomes remain. |
| `web/tests/lead-start.spec.ts` | Disabled standalone Resume with a tooltip → disabled overflow Resume with its visible reason; measure Resume, More and Close and prove the disabled click sends no write. |
| `web/tests/managed-controls.spec.ts` | Inline Steer/settings, a second More menu and inline feedback → the one header More menu, anchored setting/steer editors or phone sheets, and header receipts. Check real action items remain disabled while pending/unauthorized; retain ownership payloads, drafts, retry IDs, request counts, confirmation and reported-value assertions. |
| `web/tests/agents.spec.ts` | Session actions, a session-name heading and raw model ID → Panel actions, the exact ticket-key/title heading and the registered full model name; retain the exact session URL, ticket link/state, hover/focus and geometry assertions. |
| `web/tests/aeon-1213-agents-split.spec.ts` | A floating dock throughout an open split → the header More action while open and the original launcher when closed; retain registered preset keys, divider bounds, person isolation, shared preferences and geometry through layouts. Sheets expose More while the old dock stays hidden. |
| `web/tests/attach-watch.spec.ts` | No More button for a watched session → More is available for view/layout choices, and its actual menu contains no mutation/settings actions, including Recover, Remove, Restart and Reconnect; no textbox or automatic watch is introduced. |

This preserves the watched-session read-only boundary in the product too:
the panel's managed-settings, recovery and removal predicates reject watched
sessions, and the Restart/Reconnect item uses the same watch guard. Moving
all actions into More must not expose mutations that the retired header hid.
Managed retry controls sit above their feedback in the header. Feedback grows
downward in normal flow, so an error cannot cover Retry or move the header's
existing actions. The retained lost-response test clicks the real Retry
button and proves that its request ID and payload are reused.

An open desktop split or wide pane also clears the app main scroller's
96-pixel page-anchor padding. Its toolbar already starts below the app
header; applying that clearance again made scroll-into-view move More into
the middle of the screen and hide the composer's newest lines. The same
clearance displaced the history reader during Earlier messages. The scoped
CSS correction passes the retained viewport assertions and the unchanged
0.5-pixel history-anchor check through 231 messages. No chat-engine change,
placement observer or deferred-placement retry is added.

The nine new panel cases move, unchanged, into `native-session-program.spec.ts`
(mode/identity/menu), `native-session-program-{390,1024,1440}.spec.ts` (two
theme variants each), `native-session-program-validation.spec.ts` (malformed
responses) and `native-session-program-reader.spec.ts` (the negative control).
The shared fixture contains no test registration. Existing audit ownership
already covers `web/tests/**`; no audit registry change is needed. Shared
registry edits are restricted to this slice's nine moved tier identities,
two explicit NIGHTLY `postGateCases` entries, six shard source rows and six
nightly timing rows, written with the canonical manifest writer.

The initial split kept all nine cases ESSENTIAL and apportioned the existing
60-second conservative estimate: 13.333333 seconds per viewport file and
6.666667 seconds per single-case file. The budget test still failed, at a
304.841982-second packed peak for the prior comparison including this slice.
Only the malformed-response and reader negative-control cases therefore move
to NIGHTLY under the explicit grant; seven open/menu/Escape/no-shift cases
remain ESSENTIAL, with 46.666667 seconds total declared scheduling weight.
No pre-existing case changes tier. The source catalogue retains all nine
cases and all 60 seconds for full/nightly scheduling.

The historical prior gate excluding this slice remains 299.838842 seconds
(3598.033863 seconds total across twelve shards). These are calculated
scheduling weights, not measured browser runtimes. The budget test charges
this slice separately, following the existing accounting for other additions,
and still asserts the historical bound below 300 seconds. Even the seven
core cases plus that historical set total 3644.700530 seconds, so none of
their positive weights can fit in the 3600-second historical aggregate.

Before remote execution, `node --test --test-concurrency=1
scripts/test-tiers/*.test.mjs web/scripts/ci-web-shard.test.mjs` passed all
502 checks with zero skips. The corrected recovery unit passed all six cases
with one Vitest worker; the ownership audit passed all 33 checks. TypeScript
syntax checks passed for all twenty changed test/fixture files. The nine
split test bodies match the original bodies, and every foreign tier row is
unchanged. The canonical manifest check and `git diff --check` passed.

Final runtime/test implementation: `8a252cbf4`. The handoff commit changes
documentation only.

| Validation | Result |
| --- | --- |
| Full tier/shard suite on final source | 502 passed, zero skipped; 162.777 seconds. |
| Recovery unit on final source | All six passed with one worker. |
| Ownership audit on final source | All 33 passed. |
| Targeted source ESLint, canonical manifests and diff checks | Passed. |
| Final remote batch B | All 215 cases passed on `8a252cbf4`; 206212 ms supervisor wall. Covers agent pause, lead flow, session chat, live chat, Agents and attach/watch. |
| Focused final scroll proof | Four streaming cases and all nine AEON-976 cases passed, including the unchanged history-anchor assertion. These are subsets of batch B. |
| Earlier complete remote batch A | All 95 passed on `920f5cedd`; 157348 ms supervisor wall. Covers managed controls, no-shift, recovery, lead start/adoption and split. This precedes the final scroll-padding change. |
| Earlier split panel proof | All nine passed on `5b12c8084`; 12103 ms supervisor wall, with 26 viewport/theme screenshots. This precedes the source follow-ups. |

**BLOCKED-REMOTE-CAPACITY:** the final batch A replay was refused by the
browser supervisor because another live suite owns its machine-wide lock.
The lock and its owner were left untouched. Per the brief, no further browser
attempt was made. Therefore batch A on the final source, a fresh replay of
the nine split panel cases/screenshots, and the prior approved 56-case
`aeon-1212-lead-panel.spec.ts` remain unproven in this round. Every completed
guarded run reports zero remaining owned browser processes.

The guarded static wrapper also returned exit 3: remote static checks are
paused by the night protocol while the remote host is shared with the CI pool. This
is incomplete, not a passing static result. PR CI is authoritative. Worker
progress remains 95%; the coordinator retains static CI, the cross-family
gate and Tick's visual check. No origin push, merge, deployment, model CLI
or review gate was run by this worker.
