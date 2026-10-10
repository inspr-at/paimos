# Web workspace

Quote presence coalesces selection changes made during an outstanding request and sends the latest state after completion, preserving throttling, rejoin backoff and disposal. In Hours, an incomplete start time keeps the selected cost unit and its options; completing the time rechecks rate eligibility against that start's UTC date. Incomplete times cannot be logged.

**Settings → Access → Access log** reads the newest 2,000 access changes and says when older changes are omitted. The API keeps its existing ascending `after` pagination; `GET /api/audit?category=access&order=desc` returns newest first, with `next_before` for older pages through `before`. Each page is bounded to 50 events.

The Vue shell includes an authenticated workspace, sign-in, a 404, an account
menu, and light/dark themes. The theme follows the operating system until the
user toggles it; that choice lasts for the current page session and writes no
browser storage. Assets and fonts are served locally. The supplied mark is
preserved at `web/src/assets/brand/aeon-mark.svg` for its later replacement.

Ticket drafts belong to a record and are cleared when Discard is accepted;
opening that same ticket as a full page keeps its comment draft. Knowledge,
customer, quote-version and session-chat reads ignore results from a previous
record. Hours approval displays entries and digest from one stable read, then
uses that displayed revision and digest after confirmation. Account changes
cancel pending preference reads, timers and writes and clear earlier toast
actions. Changing person or workspace, or starting sign-out, cancels open
confirmation dialogs so their captured actions cannot proceed in the new session.
CRM and quote mutation responses carry `X-Aeon-Event-Ids`, listing the
exact accepted events in undo order. Undo uses only this receipt; missing
receipts and intervening changes refuse Undo without a latest-event lookup.

Clipped names using the shared clip-tip reveal their full text on mouse hover
or keyboard focus. On touch, tapping performs the row or link's action; holding
for 500 ms reveals the text without selecting or navigating. Moving more than
8 px, scrolling or cancelling the pointer before that threshold cancels the
hold. A clipped-name hold takes precedence over phone row selection; a hold
elsewhere on the row can still start selection. The next tap keeps its normal action.
The revealed text remains until an outside tap or Escape. Phone list and
picker names use two lines; picker rows retain their fixed height.
Project links also bind each pointer gesture to its starting record and name;
a record change before release cancels navigation.

Avatar uploads accept PNG, JPEG or WebP up to 8 MiB, 4,194,304 pixels and
4096 pixels per side, with a square crop up to 2048 pixels. Attachment images
accept up to 16,777,216 pixels and 8192 pixels per side, in addition to the
configured file byte limit (50 MiB by default). Dimensions and avatar crops
are checked before pixel decoding. Both paths share a 256 MiB budget for
estimated live image work across tenants; queued work observes request
cancellation. This is an image-processing budget, not a cap on total server
memory. Avatar orientation and cropping use source views, and generated PNGs
are stored without decoding them again.

Settings → Workspace → Brand accepts static SVG logos and serves only the
sanitized drawing. Non-drawing attributes (`role`, `aria-*`, `data-*`, `class`,
`focusable`, `xml:space`, `enable-background`) and known editor metadata are
removed. Upload feedback counts and names removed attributes in English or
German using the person's profile language. Scripts, handlers, references,
external paint URLs, animation and style elements still refuse the upload,
including active features inside discarded metadata; internal CSS conversion
is not supported.

The ticket list and Outline share the causal row store and live stream. Field
changes patch in place; moves, additions, removals and held edits wait behind
**N updates · Show** (shortcut **U**), or apply after two idle seconds when no
selection, editor, menu, dialog or drag is active. There is no countdown. The
Outline retains expansion and tree placement while updates wait, refreshes
filtered ancestors, and moves successful bulk results immediately. Stream loss
invalidates outstanding reads before reconnect; older pages cannot overwrite
locally changed child counts.
On phones, List and Outline share a bottom-centred updates chip above the safe
area, footer and selection sheet, with scroll clearance for the last row; the
desktop action stays in the table header. Lazy pages retain the server's order.

The project page's toolbar no longer repeats the ticket count; the footer says
it. With the project header collapsed, **Display** sits beside **New** in the
toolbar, on every window width, and its menu carries what the collapsed header
hides. At the top it lists the project's sections and **Needs attention**; then
the saved views (the open one has its options and, for an unsaved list, **Save
view**) and **Hide closed** with the gear for what Hide hides; then the usual
display options. Anything that needs a popover of its own opens under the
Display button once the menu has closed. The menu never scrolls: when it is
taller than the room under the button it flows into two or three columns,
counted when it opens and when the window resizes, and in windows up to 900 px
it is a full-height sheet with its title and **Done** pinned. List, Outline and
Graph switch and the filters stay where they are. Comfortable and Compact
headers keep Display in the header bar with its menu as it was; the toolbar
holds that place with an inert twin of the button, so folding or unfolding the
header moves nothing in the toolbar.

The filter strip shows **Filter** first and then only the filters that are
applied, one pill each in the order they were applied, followed by **Clear
all**; there are no empty Status, Priority or Assignee buttons. Filter never
moves and keeps its size; when pills overflow, only the pills scroll. **Type**
filters by the names set under Settings › Workspace: the leaf name (default
Ticket) and each parent level (default Epic, Story, Level 3 …), with counts
from the list API's `level` facet. A row is its leaf name when it has no work
children, else the name of its depth; levels that share a name are one option.
The list API takes `level=leaf,1,!2` alongside `kind`, `shape` and `depth`,
which keep working for the CLI. Links and saved views that used the legacy
type (`type=epic`, `ticket`, `task`) or Parents / Leaves open as Type; a Parents
/ Leaves or Depth filter that Type cannot express stays as its own pill and is
offered in Filter only while it is applied.

At widths up to 900 px, Filters includes Display controls for sorting, row
height, columns and model display. Saved column visibility and order also apply
to phone cards: optional values appear below Key and Title; Automatic restores
the compact phone layout. Attachment Compare reserves its opacity control slot
across Side by side, Slider and Onion skin so the mode buttons stay in place.
Phone Compare and column reorder buttons have separate 44 px touch targets.
Selection scroll clearance follows the measured bulk toolbar height, including
Queue and Release, so the final ticket can scroll fully above the toolbar.

The auth adapter is isolated in `web/src/lib/api.ts`. It expects `/api/me` to return
`{ principal: { id, name, email? }, tenant: { id, name }, dev_mode?: boolean, oidc_display_name?: string }`.
A 401 clears identity and routes to sign-in. Development email sign-in is
hidden unless the server explicitly returns `dev_mode: true` (including on its
401 response). It never relies on Vite's development mode. Set
`AEON_OIDC_DISPLAY_NAME` to the public name of your identity provider (for example,
`Acme SSO`); the sign-in button, redirect hint and provider-specific errors use
that name. Configuration strips control and bidirectional formatting characters,
collapses whitespace, and caps the name at 48 Unicode characters. Empty names
after sanitisation show neutral sign-in copy. The name is exposed on both the
authenticated and unauthenticated `/api/me` responses.
Login navigates to
`/api/auth/login`; development login posts `{ email }` to `/api/auth/dev-login`;
sign-out posts to `/api/auth/logout` before routing to `/signin`. API calls use
same-origin credentials, a ten-second timeout, and no browser response cache.
The backend owns authentication cookies and the configured OIDC authentication redirect.
OIDC discovery has a five-second deadline and shares one in-flight attempt without
holding the sign-in mutex over network I/O. Canceled callers can leave immediately;
failed discovery remains retryable. Discovery and signing-key responses are capped
at 1 MiB before decoding.
Email comparisons fold only ASCII A-Z; Unicode characters remain distinct in
bootstrap admin checks, development sign-in, invitation provisioning, imported
profile matching and link suggestions. Classic principal backfills take the
same tenant lock as invitation acceptance before repairing emails.
Email-based OIDC bootstrap enrollment requires `email_verified: true`, just
like invite enrollment, and can run only once. The first principal binding's
append-only audit event closes bootstrap enrollment; subsequent claims to the
same mailbox cannot create another admin, even when verified. Existing and
operator-pinned issuer/subject memberships continue to sign in.
Invite acceptance holds the tenant access lock through its binding inserts;
linking takes that lock before the alias lock (seed 532) and principal rows.
These access-write fences use `FOR NO KEY UPDATE` so they serialize with each
other and membership changes while allowing event writers' tenant FK checks.
The last-owner trigger uses the same fence during binding removal/deactivation.
Project-role assignment and attachment writes lock the tree (seed 0), then the
tenant, before reading current grants and resource rows. Attachment request
bodies are read before these locks; the final transaction checks permission in
the node's current project and commits metadata and audit events together.
Attachment writes use a shared tenant lock: access edits are fenced while
other resource writers can finish their tenant foreign-key checks. Retained
principal import/backfill paths take tree, tenant, then alias locks in that order.
Pairing, readiness and residency mutations acquire pairing, tree, then the tenant
access fence before account/resource rows, retaining final-transaction permission
checks. Their tenant fence uses `FOR NO KEY UPDATE`; access-only writers may omit
pairing/tree but must never acquire them after tenant. Event counters remain last.
No analytics, third-party runtime assets, or optional device storage are added.

Both version surfaces use the unchanged, verified calendar bundle in Pretty
mode with brand gold. The shared helper provides reveal and copy interactions;
`dev` remains plain text. Every production web build verifies the bundle pin.

The release history's Highlights eyebrow reads “PAIMOS AEON · Release” (or the
configured wordmark); generation and release counts stay in Details. The live
codename leads in light display type with the heading's spacing, above one
glass dock holding live status and the Pretty version. Its separators use the
theme's muted ink; resting hours and minutes use primary ink at the renderer's
80% weight for AA contrast in both themes.
Hover or keyboard focus crossfades the renderer's characters to the full canonical version over
one second; reduced motion switches instantly. Click or Enter copies the exact
canonical value, including `.0.0`, and announces “Version copied”. This character
crossfade is an Aeon presentation layer over the pinned renderer and its shared
timing and opacity helpers; the vendor bundle remains unchanged.

Release list rows keep the codename and its badges visible while a separate
Pretty version sits at the right of the heading. That version uses the same
crossfade and canonical copy feedback as the dock; copying keeps the current
selection and address. When the heading is too narrow, the version wraps below
the codename and stays right-aligned. The history uses interactive grid rows so
the copy button is available to assistive technology; j/k and arrow keys retain
the selected-row navigation.

The connect screen keeps Connect available when a selection mixes verifiable
and unverifiable harnesses. Clicking it offers **Connect without verification**
for the whole selection or **Leave them out** to keep only the verifiable
accounts for review before connecting. Approval currently records one verification
mode for the selection; no harness is silently excluded or treated as verified.
When Connect is disabled, its reason appears beside the button.

```sh
cd web
npm run test:unit
npm run test:browser-safety # tiny Node process fixtures; no browser locally
# Full UI suites: prefer CI; sharded UI jobs are tracked in AEON-410 (PR #29).
# Prepared build-6 entry point (refuses until OPS-247 bootstrap is approved):
AEON_REMOTE_CONTROL_DIR=/path/to/coordinator/aeon npm run test:remote
# Locally, only one targeted file, one worker, when there is a technical reason:
npm test -- tests/authz.spec.ts --workers=1
```

`just ui-remote` is the same remote entry point from the repository root. It
currently refuses with exit 3 before SSH or dependency installation: OPS-247
owns the approved browser bootstrap and shared heavy-job launcher. Use hosted
draft PR CI while that work is pending. No environment flag enables the lane;
the coordinator must confirm the launcher contract and review a follow-up change
to enable it. No browsers or Playwright were installed on build-6 for AEON-508.

The prepared runner accepts extra arguments to select files or reporters. Set
`AEON_REMOTE_CONTROL_DIR` to the existing
coordinator directory containing `remote-test.sh` and its OPS hold controls. The
runner respects holds and capture reservations, refuses an active builder pool,
Mailina's console session, a non-ci console idle less than ten minutes, unknown
presence/load, load above 18, or any existing heavy-run reservation. It reserves
one remote browser lane before setup and checks presence/capacity again. Refusal
or unreachability returns exit 3 and never starts a local suite. It streams only
committed HEAD through `git archive` (no extra Git push), runs at one worker, and
copies logs and test artifacts to `web/test-results/remote/<run>/`. The remote
checkout and artifacts remain for inspection; no other worker's state is cleaned.
After the gate is enabled, a missing pinned headless shell still refuses the run
rather than installing one. When the approved shared lane launcher is available,
`AEON_HEAVY_JOB_LANE=/absolute/launcher` wraps the job using `browser -- COMMAND ARGS`;
the coordinator must confirm that adapter contract before enabling it.

All three Playwright configs default to one worker locally; `PW_WORKERS` is an
explicit positive-integer override. Local CLI `--workers` / `-j` values are
ignored unless `PW_WORKERS` is set. Only the UI config opts into test-level
parallelism in CI; smoke and performance keep their serial test behavior.
CI's worker/shard budget remains owned by CI (AEON-410). Local UI runs use
one project and Playwright's bundled [Chromium headless shell](https://playwright.dev/docs/browsers#chromium-headless-shell)
with GPU disabled. Smoke and performance runs use the same browser policy.

Use `npm test`, `npm run e2e`, or `npm run audit:ui` to keep the shared per-user
host lock and process supervisor active, including across worktrees. Direct local
`npx playwright test` is refused by global setup before browsers start. A second
suite prints the lock path and owner PID and refuses to start. An interrupted or
failed run terminates only its own process groups, checks that they are empty, and
then releases the lock. `AEON_PW_PROCESSES` logs before/peak/after browser counts
and wall time. A Node preload records detached browser groups when they spawn,
preserving Playwright's normal browser shutdown behavior. The lock descriptor
stays open throughout the run. Root and detached launches retry transient process-table
misses; a live launch whose identity cannot be verified has its process group killed
without appending an incomplete record. The root preload verifies its identity before
executing suite code; an already-exited root keeps its original exit code.
After forced supervisor termination (SIGKILL),
the next run recovers a dead owner's lock under an exclusive recovery claim:
it signals only journalled groups with matching process start identities,
verifies that they have exited, and removes that owner's journal and lock before
starting. Live owners and missing identities/journals refuse recovery. Reused
group PIDs are never signalled and do not retain the lock after verified siblings
are reaped. Unverified orphan groups and malformed journal rows retain the lock,
but do not prevent verified sibling groups from being reaped.
Metrics use stderr so JSON reporter stdout remains parseable. Never kill other
workers' or desktop browsers.
Recovery handles SIGINT, SIGTERM and SIGHUP before acquiring its claim, finishes
verified cleanup and exits without starting a new suite. If the reaper is killed
with SIGKILL, the next starter reclaims its `.guard` only after proving that the
reaper PID is gone or its start identity has changed. A matching live reaper or
an unknown identity still refuses recovery and prints the exact guard path.
Claims are atomically published as nonempty directories containing a unique
owner record, so competing reapers cannot remove a new owner's claim. Stale
file-based claims from earlier versions are also recognized. Invalid claims and
journals remain for operator inspection.

When AEON-410's runner from PR #29 is integrated, use `npm run test:ui-shards --
--shard=1/8` (or `node scripts/playwright-ui-shards-safe.mjs --shard=1/8` from
the root) in place of calling `playwright-ui-shards.mjs` directly. This wrapper
supervises the whole existing planner, JSON listing, selection verification and
shard execution, without duplicating its sharding or compiled graph. It sets
`AEON_PW_SHARD=1` and `PW_WORKERS=1`; keep `workers: 1` and
`fullyParallel: false` **after** the policy spread in the merged UI config.
The wrapper refuses before acquiring a lock when that runner is absent.

The Playwright UI suite starts Vite on a stable port derived from its worktree
path; `PLAYWRIGHT_PORT` overrides it. Set `PLAYWRIGHT_REUSE=1` only for a dev server
already running from this same worktree. It intercepts all `/api/*` calls,
and covers sign-in, auth errors, logout, theme switching, version interactions,
44 px targets, and viewport overflow. It writes home, sign-in, development
sign-in, and 404 screenshots in both themes at 1280×720 and 390×844 to
`/tmp/aeon-p05-shots/`. Screenshots and browser test output are not committed.

**Full UI QA** runs the complete `playwright.ui.config.ts` inventory in five
hosted shards with one browser worker each and zero retries. It runs nightly on
`main` at 02:37 UTC. To request a full QA, dispatch `full-ui-qa.yml` with an
optional `ref` (branch, tag or commit; blank defaults to `main`), or add the
`full-qa` label to a PR. Updates to a labeled PR run the suite again; unrelated
label additions do not. PR runs test the merge commit. Every shard checks out
the same resolved commit, including when a manual branch moves during the run.
The `full-ui-qa` check and Actions summary report the batch result; shard JSON
reports and failed traces are retained as artifacts for seven days. This is an
optional QA check; existing required CI checks and PR test selection are unchanged.
Scheduled/manual triggers become available after the workflow reaches `main`.

Licence: AGPL-3.0-only.
