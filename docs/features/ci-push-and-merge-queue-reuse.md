# CI push and merge-queue reuse (AEON-423, OPS-257 L5)

AEON-1023 reduces browser shard discovery and launch overhead. Tier browser
jobs collect only the native Playwright catalogue. Unit classifications remain
planner metadata so that source-impact decisions retain their existing
semantics; strict web setup and unit jobs still discover the complete web
catalogue. Case selection, shard weights, required checks and retry limits are
unchanged. Compatible groups share a supervised Playwright execution and Vite
server: config, project, remaining flags and host restriction must match, and
environment values must not conflict. Screenshot variables are combined and
failure tracing is retained when any contributing group requests it. The tier
runner still forces one worker and disables automatic retries. Conflicting
policies and the separate performance config keep their own execution.

Each browser shard measurement includes `webShardTiming`: monotonic collection,
planning and run seconds, summed native case durations including explicit
retries, residual non-case seconds, and per-launch group membership, native
list and execution timing. Missing case duration evidence leaves the residual
unknown. `browserCases` records case/file/config/project identities and final
statuses for comparison across a full run; a missing result is `notRun`.
Existing case accounting and full-execution checks remain authoritative.

`node scripts/test-tiers/prove-web-shards.mjs` checks exact native old/new
selection identities across all twelve full browser shards; `--all` checks the
whole catalogue. It compares full versus browser-only discovery, resolves both
the old group lists and the combined lists through Playwright `--list`, and
writes `tmp/test-tiers/web-shard-set-equality.json`. This is a discovery proof;
it does not claim browser execution. The runner regression executes a full
twelve-shard layout with native-result fixtures and rejects missing/duplicate
identities. Actual browser execution and before/after non-case timing require
the approved browser lane or the coordinator's CI run. The AEON lead owns the
seven-day outcome: compare actual per-shard residuals, runner minutes and
first-attempt results with the pre-change run, and post on AEON-1023 after
deployment. The reference Arion W2 full run `37815054461` has web shards
4.8–12.8 minutes, 99.7 minutes in selected-UI steps and 71.7 summed case minutes.

Main-push reuse keeps its existing `CI_TREE_REUSE` semantics: unless set to
`off`, a successful full merge-group run at the **same commit SHA** can replace
heavy execution. The verifier checks repository, workflow name/path/ID, latest
attempt, complete job inventory and full-tier execution steps for every shard.
Main-only `cache-prime` still installs dependencies and browsers and warms Go
caches; its failure fails the required aggregates. A queue run that itself
reused PR evidence is **not** full execution evidence for main: main runs its
normal fallback suite. There is no transitive reuse or PR-to-main shortcut.

Merge-group reuse is separately opt-in: only `CI_MG_REUSE=on` enables lookup;
unset, `off` or any other value preserves the existing queue execution.
The queue may reuse one successful **full** same-repository `pull_request` run
whose actual checkout tree equals the queue commit's tree. The PR head tree
alone is insufficient: PR jobs check out `refs/pull/N/merge`. An optional inline
`ci-plan` step verifies `HEAD == GITHUB_SHA` and the exact two parents
`event.base.sha event.head.sha`, then records the run ID, attempt, PR head, base,
checkout commit and tree. The successful `tier-measurements` aggregate carries
those value-free identifiers in its API-visible job name, with a successful
`Confirm PR tree proof` step only after full-lane/full-tier execution and all
five required checks passed. Lookup cross-checks that proof against the run's
head SHA, workflow/repository identity, immutable Git commit/tree/parent API
facts, every full-suite job and every full-tier execution marker. The source
run is checked again after inspection. No mutable PR merge-ref lookup,
registry, artifact download or artifact lifetime supplies identity; deleted or
missing run/job/Git evidence falls back to normal CI.

PR attempts other than **1** are deliberately ineligible, even complete reruns:
carried-over jobs cannot become fresh execution evidence. Essential, spec-only,
docs-only, incomplete/cancelled suites, missing or malformed proof, fork heads,
tree mismatches, API errors, deadlines and absent trusted-base code all retain
full queue tier validation when enabled (including full fanout and case
selection). Unset/off retains the prior planner behavior. Discovery considers
at most 20 green PR runs and uses a ten-second total network deadline and bounded responses; an older match can
be missed safely. The heavy Go matrix, web setup/units/browser shards,
release-check execution and e2e execution are skipped only after verified proof.
`go-static`, `go-timing`, runner routing and planning still run; unconditional
`migration-compat` rechecks current release compatibility. Required check names
`go`, `web`, `release-check`, `e2e`, `migration-compat` stay unchanged. Each
accepting aggregate independently revalidates the same source run, requires
confirmed proof outputs, and fails when confirmation observes invalid proof
after lookup.
Passed checks are snapshots: a source rerun after the last confirmation does
not retroactively revoke a passed check. This is not a revocation service.
Measurements identify reused source evidence and count only the freshly run
timing guard cases; missing current timing evidence remains incomplete. They
never invent fresh passes for skipped shards.

Queue lookup and aggregate confirmation execute `ci-tree-reuse.mjs` from the
immutable merge-group **base SHA**, on hosted runners with only `contents: read`
and `actions: read`, no persisted checkout credentials and no write token.
An older base without this protocol cannot authorize reuse. Exact tree equality
also includes workflows, scripts, manifests and tests. This does not sandbox a
same-repository PR that changes its own workflow or fakes its own test commands:
today's full CI already executes that tree's workflow. Trusted-base verification
adds no stronger token or runner trust and cannot make an altered test suite
stronger than its declared checks. Every retained cache-capable guard disables
cache saving on reused queue runs; queue reuse never starts `cache-prime`.
Main-scoped cache priming remains unchanged for genuinely full queue evidence.

Roll out with the flag unset, review these offline regressions, then let the
coordinator enable `CI_MG_REUSE=on`. Only new full PR runs emit proof. Inspect a
hosted matching-tree run, a mismatch fallback, all required contexts and cache
behavior before relying on savings. Roll back by unsetting the variable or
setting it to `off`; an in-flight reused run whose confirmation observes the
switch change fails safely and needs a fresh normal queue run. Keep
`CI_TREE_REUSE` independent. Hosted runs must confirm job-name preservation,
run head semantics, Git-object availability, API timing and actual latency.

`node scripts/ci-mg-reuse-rate.mjs` measures an offline upper-bound proxy over
the last 80 first-parent `origin/main` PR merges. At implementation the final
merge tree equaled the second-parent PR head tree in **18/80 (22.5%)**; requiring
that head to contain the previous main tip also gave **18/80 (22.5%)**. This
proxy measures neither green full PR execution nor historical PR merge trees,
queue grouping or production hit rate. Head-tree equality is not a mathematical
upper bound for merge-checkout reuse: a PR can already have tested base-only
content in its merge tree. It cannot establish the 1–2 minute
latency target, especially while static/timing/migration checks remain active
and reused queue evidence forces normal main-push fallback.

Offline tests: `node --test --test-concurrency=1 scripts/ci-tree-reuse.test.mjs
scripts/ci-pr-plan.test.mjs scripts/test-tiers/test-tiers.test.mjs` and
`go test ./scripts/releaseworkflow ./scripts/ci-runner-guard -count=1`.
The API fixture is representative offline data, not a hosted receipt. Historical
workflow pins remain intact; tests normalize only these reviewed additions and
check the full queue inventory against the verifier.

The offline CI proof foundation (AEON-417 A) is in `internal/ciproof`, with
versioned obligation, plan and receipt contracts in `contracts/v1.schema.json`.
`go run ./scripts/ci-proof digest --mirror /absolute/controller-owned/mirror.git
--policy-commit <full-SHA>` computes a diagnostic policy digest for independent
review. `shadow` uses that commit plus the independently approved
`--policy-digest`, an `--environment-digest`, and a `--binding` JSON file with
repository ID, event/delivery/generation, immutable base/candidate/check-target
SHAs, PR source head/number or group ID/sorted constituent PRs. It emits a full
pending plan; `--ledger` optionally appends to a private controller-owned JSONL
file, and `--receipt` records an unsigned observation against an existing plan.
The mirror must be bare and its policy revision must precede the bound base.
Every trusted workflow job and shard row is retained, with base/candidate test
package discovery and complete path/mode/blob fingerprints. Both shard layouts
are inventory facets in shadow output, not two executor launches. No event
authentication, executor admission, reusable credit, signatures or check
publication exists in A; current workflows and runner routing are unchanged.
Future authority/executor and selection packages must establish those boundaries
before any omission. Run the foundation tests with `go test ./internal/ciproof`.

AEON-417 C adds `scripts/ci-go-impact`, a **shadow-only** Go selection and
full-result comparison tool. The complete foundation/authority plan remains
unchanged: every action is `run`, every required check and job remains present,
and timing/static run fresh. `ci-go-impact shadow --mirror <absolute-bare-mirror>
--git <absolute-reviewed-git> --plan <full-plan.json> --analysis-context
<installed-context.json> --base-metadata <base-go-list.json> --candidate-metadata
<candidate-go-list.json> --record <private-shadow.jsonl>` records a separate
selection hint. This command consumes bounded artifacts and immutable Git blobs;
it never launches candidate code or `go list` on the controller host. Missing,
invalid, incomplete or unsupported metadata produces a full selection. Without
metadata, omit the three analysis options to record that fallback explicitly.

The metadata recipe is `/opt/aeon/bin/go list -mod=readonly -deps -test
-json=<required-fields> ./...` in B's disposable offline Linux/amd64 guest. The
installed recipe fixes the complete required field list and excludes large
unused transitive-dependency lists to fit the artifact bound. Collect base and
candidate separately under the same pinned image/environment. The context has `goos`,
`goarch`, `tags` (empty), `toolchain_digest`, `dependency_digest` and
`environment_digest`. The decoder covers production/test/external-test imports,
test variants, ignored source files and production/test embeds; immutable source
imports independently widen the graph so a supplied artifact cannot remove
edges. Unsupported targets/tags, cgo/assembly, workspaces/replacements, missing
objects, malformed metadata and graph limits fall back to full work. A sealed B
metadata observation has a separate validation API; raw CLI artifacts remain
diagnostic. The reviewed analysis image and actual hosted boot proof still need
coordinator provisioning before any trusted collection claim.

The union of both graphs retains removed imports, files and packages. Changes to
testdata, embedded files and declared runtime fixtures select owners and reverse
dependants. Shared CI/harness/module/migration/OpenAPI/web-embed/version inputs
and unmapped paths select the full inventory. The compiled
`internal/ciproof/go-impact-policy.json` initially audits only `runkind`,
`scopecode` and `ticketbenefits`, with byte pins and explicit runtime inputs.
Changed or unknown runtime closures remain selected with whole-tree input
fingerprints; candidate policy edits force full selection. Every selected package
retains **all** ordinary shard rows
in both recorded layouts. A scheduling hint never permits omission without a
verified passing baseline receipt: `required_fresh_packages` always includes the
complete live package inventory while optimization is disabled.

Add `--results <full-run.json>` to compare the hint with actual full-run terminal
results. The diagnostic input schema `aeon.ci.go-full-run.v1` binds `plan_id`,
`candidate_commit`, `environment_digest`, `run_id`, `attempt` (1) and `layout`
(7 for PRs; 4 is allowed for full main runs). Its `results` array has
`obligation_id`, `package`, and `result` (`success`, `failure`, `skipped` or
`cancelled`) for every live row of that layout, including all split-package rows.
New packages without rows use their `go-package/<import-path>` obligation.
Missing/skipped/cancelled rows make the comparison incomplete and leave
`omitted_failure_count` null; a complete comparison must have zero omitted
failures. The CLI records the comparison before exiting nonzero for incomplete
coverage or omitted failures. These unsigned diagnostic records do not mint
receipts, baseline credit, certificates or success checks. No workflow, required
check, runner route, queue reuse, timing reuse or UI selection changes in C.

Project sections have their own URLs: `/p/KEY/tickets`, `/p/KEY/knowledge`, and `/p/KEY/settings`. A ticket uses
`/p/KEY/TICKET`; `?section=knowledge` retains its background section. Tickets is the default,
so its ticket links need no section query. Existing `?view=full` ticket links
still open the full-page ticket at the same address.

The Tickets project header defaults to Compact: project details, sections with
saved views and Hide/Display, then the toolbar. Comfortable gives the title and
description more reading room; Collapsed retains the toolbar. The centered
app-bar switch selects all three on desktop. Phones fold with the project badge
and chevron, and choose Comfortable/Compact in the Filters sheet's Display
section. Command+Shift+Period on macOS or Ctrl+Shift+Period elsewhere toggles
Collapsed and restores the last roomy choice, including while typing. It uses
the physical Period key; Escape and scrolling do not unfold or fold the header.
The choice stays on this device, separately for each person and workspace,
across projects; row height and saved views remain independent. Active filter
buttons show their values (counts on narrower desktops), with individual remove
actions and Clear all; their labels, including names arriving asynchronously,
update after the filter popover closes. The phone fold badge keeps its full
44 px touch height and an unclipped keyboard focus ring. Open, doing and done
counts filter by the server's work buckets; clicking again clears the choice.
Comfortable adds the eleven status counts in four groups (Open, Doing, Done,
Closed); phones retain the three group buttons. Status clicks include legacy
spellings. Selecting a status hidden by Hide temporarily shows closed work and
announces why; clearing it or choosing visible statuses restores Hide. A manual
Hide toggle takes precedence. These selections and automatic Hide restoration
survive URL links and saved views. The project summary carries bounded
`status_counts` and their buckets;
missing or truncated detail is labelled unavailable or partial, while group
totals remain exact. With partial detail, a single-status selection temporarily
turns Hide off because an omitted kind category may hide that status; clearing
the selection restores Hide, and manual toggles still take precedence. Group
selections retain their exact bucket policy. The additive `work_state` and
`work_bucket` list parameters share the summary's canonical spellings and
kind-aware buckets. Hide's ghost gear opens “What Hide hides”; phones offer the
same five choices inside Filters. All five remain the default. The three
finished choices read “Hide finished”; other subsets show Hide and their status
icons, with names available on hover and to screen readers. At least one choice
remains selected; Reset restores all five. Counts use the project summary and
show unavailable when its detail is missing or partial. Choices travel with the
URL and saved view even while Hide is off, and the gear and Display stay still.
The additive `hide_states` list selector applies only with `hide_closed=true`.
Each kind's bucket wins: Delivered/Accepted retain their names inside Done,
other Done-bucket statuses use the Done choice, and exit buckets use Cancelled
or Archived. List, Outline and Graph share this membership policy, including
the default and Reset. Graph retains closed topology until the list query
decides membership, so a kind's Open Accepted or Done QA follows its configured
bucket rather than the graph's spelling-based category.

Model estimates learn from completed-ticket outcomes, actual session profiles and
frozen work placements. Fully reported worker runs with measured active time feed
the newest 30 samples per model version, effort, kind and complexity bucket.
Token rates back off from the exact cell to the same line across versions, the
profile across kinds, then the existing harness/model/effort route and documented
5M/h planning fallback. Every calibration names its basis and sample count. Live
uncalibrated fallback tokens and costs are withheld in the UI and sort as missing.
Epics exclude those children and label the sum partial. Frozen fallback baselines
remain visible with an uncalibrated label so work-start comparisons survive.
Page planning reads live descendants from their selected parents; whole-list
model, token and cost sorts retain batch joins. The 6,000-node regression checks
request latency and descendant node visits under stale kind statistics.
This regression runs with the other wall-clock budgets in CI's isolated
`go-timing` job; both the four- and seven-shard layouts skip it.

Speed factors need five exact-cell samples with frozen positive size estimates:
model-adjusted hours are size × median(active hours / size), while `estimate_hours`
stays unchanged. Work-start snapshots freeze the adjusted estimate, its basis and
explicit speed factor: tokens = rounded size hours × unscaled token rate × speed;
`list_per_hour` is already speed-scaled. Snapshot history stays limited to its own
project. Learning filters target profiles/lines and placements before a
newest-12,000 candidate bound and expensive worker aggregation, then keeps 30
eligible samples per cell. Reaching the candidate or 4,096-profile bound leaves
model history truncated. The independent bounded legacy route or any-route
calibration remains usable, with the history issue appended to its basis; only
insufficient legacy evidence uses the documented uncalibrated default. Sorted
and unsorted lists share one learning read per request. SQL hint errors roll
back a savepoint and leave work starts usable with history unavailable, with
one diagnostic per shared planner containing SQLSTATE and request context,
never raw SQL or error text. Workers must belong to the outcome project: a
cross-project worker is excluded before the distinct-source aggregation guard.
The candidate bound precedes the per-cell newest-30 window, so a large history
can continue to truncate. A future window-first bound must push project
visibility into SQL first so hidden samples cannot crowd out visible history.

The read-only
`GET /api/usage/model-estimates?profile_id=…&kind=…&bucket=normal|complex`
endpoint requires `harness.read` and returns null hints below five samples.
`ChoicePicker` supports this history through each choice's `estimate`, with
`estimateKind` and `estimateBucket` selecting the context; its hint line reserves
space while history loads or is absent. This is groundwork without an editor
consumer or end-to-end picker release claim. The model preference editor is
delivered separately; its caller must discard cancelled or stale-context history
responses. The isolated picker geometry tests use the shared stability guard for
history arrivals, selection and absent hints; consumer response-race coverage
belongs with the editor integration.

Ticket lists refresh worker names, progress and ETA on session registration,
heartbeat, rebinding and stop events. The shared live feed also refreshes after
reconnecting and on lifecycle changes; heartbeats leave it on its 20-second poll.
The ticket list still refetches ETA and lead projections on heartbeat hints.
List and Outline reconnect and fully resync after visibility, pageshow, online,
or a clock gap longer than twice the 5-second check period. A 45-second silent
stream also reconnects; live-mode SSE sends observable `stream.ping` events at
the 15-second keepalive deadline without changing the durable event cursor.
The freshness dot shows Live, Reconnecting, or an amber last-update age. Until
both list and live-worker reads recover, workers and ETAs are dimmed and past
estimates show their last clock time without asserting current overdue work.
List projections use the additive `Aeon-Event-Position` response header to
reject older snapshots even
when the ticket's own `updated_at` has not changed. A node-list response names
the counter before its handler as a lower bound and runs once. A page covering
the hint that triggered its batch updates the row immediately; newer hints
remain queued for a follow-up read, so busy tenants do not need a quiet gap.

The INSPR Flow is retired (AEON-723). Journey stages, the Journey project view,
the footer Flow pill, stage handoffs, and the external-stage CLI are removed.
Old Journey bookmarks open the project's tickets. Stored Flow data is preserved;
deprecated Journey, requirements and handoff APIs return authenticated 410 errors
without executing stage actions. Existing reporter success schemas and contract
headers remain pinned for PHAROS/JANUS compatibility. Ordinary release creation
and membership use the native release APIs, independently of Flow gates.
No table or column is dropped in this release. Contract-phase cleanup needs a
separate follow-up, including migrating the legacy-named release ledger and
historical provenance before removing dormant Flow tables and compatibility routes.

Reserved, never-published versions are hidden in the release history by default.
**Show reserved versions** under **Settings → Developer**
(`/settings/developer#reserved-versions`) enables them for that person and
workspace, including comparison choices and previous/next navigation. Statistics
and result counts always include reservations; the footer's **N new** count
includes only visible versions. A direct link still opens a hidden reservation
with a quiet explanation of the setting.

Native intake drafts accept an optional Aithema `extensions` map and the
original review snapshot as `document_bytes` alongside the required native
projection fields. Aeon extracts extensions from the single confirmed,
unbound item and stores both fields as immutable text, preserving evidence and
JSON spelling; idempotent retries must keep the bytes unchanged. Aithema owns
registry/schema validation and its canonical 16 KiB instance / 64 KiB map
limits. Aeon's intake request cap remains 1 MiB, including JSON escaping and
projection overhead. Drafts, accepted briefs, requirements and their generated
tickets show each namespace/version in a neutral Extension data disclosure.
`GET /api/projects/{projectId}/intake?node_id=UUID` returns only the accepted
drafts for that node or the requirement that generated its ticket, with empty
sources/turns and the same `intake.read` permission. This additive native API
does not implement Aithema's snapshot-only plugin protocol or change Aithema's
separate `pending_op.payload` limit (AIT-89); the adapter integration remains
tracked by AEON-360.

Aithema token primitives live in `internal/aithema/tokens`. Hosts with an HTTPS
`AEON_PUBLIC_URL` expose public Ed25519 keys at `GET /api/aithema/jwks`; the exact
URL is the issuer and delegated audience, while session tokens use `aithema`.
The host-wide key set is owned by the bootstrap tenant under forced RLS and
encrypted with the existing session-key secret, a signing-specific derivation,
and the existing AES-GCM vault. Keep that host secret stable across restarts;
HTTPS development also requires a persistent `AEON_SESSION_KEY_FILE`.
Keys rotate daily on use; a next key is prepublished and retired public keys
remain for 960 seconds. Discovery caches for at most 60 seconds and supports
ETags; HTTP-only development returns noncacheable 503. Trusted integrations can
mint session/delegated claims and rotate through the package API. Both claim
sets are closed, enforce a 900-second maximum lifetime and preserve safe
integers before conversion; `nbf` is refused because the binding schema does
not permit it. Verification grants no route access: project, generation and
epoch freshness checks remain AEON-360's responsibility. No journal is required.

Aithema's host journal and ledger live in `internal/aithema/journal` under
`/api/aithema/{journal,ledger}/sessions/{sid}`. Every route requires its exact
delegated capability; person cookies and agent API keys do not grant access.
New writes fence generation and epoch atomically with the effect, and token expiry
is checked again after lock waits. Journal snapshots use revision CAS and persist
a nondecreasing `consumed_seq`; pending operation and `op.result` keys must begin
with the session id followed by a colon. Foreign keys return `400 invalid_request`
before allocating a sequence or committing a client event id.
Records use session-scoped client IDs. Exact retries preserve the original result;
changed bytes return `409 idempotency_conflict`. `records?ids=1,2,3` hydrates
cited sources, turns and design inputs; `format=stored` also returns the exact
submitted bytes. `cursor` returns the latest snapshot and replay position.

Journal and snapshot contracts support minor 2 while retaining legacy inline
pending operations. New `pending_op.content` records and reference snapshots
require `min_reader: 2`. Content is immutable and deduplicated by SHA-256 within
each tenant/session; every submitted event ID retains byte-exact conflict
checking through an alias wire digest, without copying canonical bytes. A reference
snapshot commits only after its content sequence, digest
and UTF-8 size match an acknowledged content record in the same session.

Large content and snapshots use the AIT-89 chunk protocol on the existing
routes: POST with `upload=<original-byte SHA-256>&ack=seq` and a JSON body
`{offset,total,chunk}` containing canonical base64 of up to 256 KiB. Intermediate
replies contain `{offset,total}`; only a complete, digest-verified submission
commits and returns `{seq}`. Offset zero restarts an interrupted upload. Staging
survives host restarts, permits at most 64 unfinished uploads per session, and
is discarded on takeover or purge. Staged totals are capped at 16 MiB before
buffering; every request retains the 1 MiB cap and every response the 4 MiB cap.
`ack=seq` also works for ordinary writes. `cursor?snapshot=seq` returns a bounded
snapshot reference; `records?digests=<sha256>` resolves content addresses to
sequences, and `records?ids=<seq>&offset=<byte offset>&length=262144` returns
`{seq,offset,total,chunk}` for lossless hydration. The adapter verifies and
hydrates reference content through these reads; snapshots retain their exact
reference envelopes. Records larger than 1 MiB require range reads backed by
durable 256 KiB rows, so Postgres never detoasts the entire large bytea for a
range. Inline ids/after/cursor reads return 413 too_large for large records or
a response exceeding 4 MiB; use digest lookup and snapshot=seq to find sequences.
Migration 1100 installs the replacement check NOT VALID and drops the old check
in one isolated statement, the only policy exception requiring coordinator review
before merge/release. Migration 1101 validates after the exclusive lock is released;
1102 builds the unique content-address index concurrently.

The ledger admits against session, principal/day and tenant/day caps, commits
one digest-bound claim per hold after a matching worker `budget.hold` journal
acknowledgement, and settles actual cost or an unknown maximum. Only the claim's
original generation and epoch can settle it, including after takeover or purge;
that late completion is charged at maximum. A successor uses recovery instead.
`holds?state=open` provides uncached keyset pagination. Current-generation
`recover` closes unclaimed holds as `void` and claimed holds as `unknown`;
settled holds retain their result across repeated recovery and lost replies.
Settlement arriving after recovery returns the recorded close without changing
its charge; a previously committed settlement still requires byte-exact retries.
Only host-qualified local lanes may reserve zero. `journal/.../authority`
returns generation, epoch, authorization, suspended state, tombstone and current
`issued_at` with `Cache-Control: no-store`. Session creation, takeover and
revocation are trusted in-process methods, with controls journaled before projection. Suspend
pauses new admissions, claims, worker journal writes and takeover while allowing
settlement, recovery and reads. Refused takeover returns `409 suspended` without
consuming a generation, so current claim owners can still settle actual cost.
Resume clears that pause; purge alone creates a tombstone.
AEON-361 provides the store and HTTP routes; lifecycle installation and the
AEON-360 intake adapter are wired by AEON-P04. The adapter uses
`LockAuthority` inside its intake transaction so takeover cannot race its write.

The compiled `aithema` plugin and `internal/aithema/host` provide that wiring.
An administrator enables the plugin with `intake.read` and `intake.write`, binds
its agent principal to the project, and writes typed settings through
`/api/plugins/aithema/settings`. The service credential is write-only:
`service_jwt` is masked on reads, omission preserves it, and an empty value
clears it. Its ciphertext uses a separate derivation of the stable host session
key and tenant-bound AES-GCM. Audit events record field names and rotation,
never values. Paid processing remains refused until the administrator has
approved host-qualified processor evidence (`evidence_verified`, default false).
Processing settings cannot change while sessions or callbacks are active;
preview public keys, picker hash, and service credentials may rotate.

A person with project `intake.write` creates a host session through
`POST /api/projects/{projectId}/aithema/sessions`; the host assigns the session,
tenant, project, epoch and settings digest. It queues service creation before
issuing browser tokens. `…/sessions/{sid}/tokens` refreshes for the current
generation only after creation succeeds. Browser responses contain only the
session token with audience `aithema`; host-audience delegated tokens remain
server-side and are supplied to the service at creation/resume delivery.
Intake delegation uses the same locked journal authority as its effect, checks both project bindings and the
installed plugin, and creates no persistent agent grant. Acceptance stays
person-only. Journal and ledger effects recheck host bindings as well; authority
polling and settlement of already committed claims retain their existing rules.

`…/sessions/{sid}/control` accepts `suspend`, `resume`, or `purge` with a UUID
idempotency key. Resume takes over the generation; purge writes the tombstone
before service delivery. State and callback enqueue commit together. Native
project events automatically enqueue value-free `host-event` notifications;
`…/host-event` also permits explicit notification of an existing project event.
Offboarding or plugin removal refuses new effects immediately; authority
polling exposes the live tombstone even before the worker persists it and queues
the revoke accelerator. `POST /api/aithema/deprovision` tombstones the exact
`(issuer, tenant, sub)` scope and prevents it creating new sessions. Exact
retries retain their original result. The durable outbox uses replica leases,
ten-second HTTP deadlines, no redirects, a stable `Idempotency-Key`, and at most
six attempts with delays capped at thirty seconds. Delivery state is visible at
`GET /api/aithema/callbacks/{callbackId}` to people with `plugins.manage`.

The service protocol uses `POST /v1/sessions`, `/v1/deprovision`, and
`/v1/sessions/{sid}/{host-event,suspend,resume,purge,revoke}`. Every callback
carries the configured service JWT in `Authorization`; creation and resume
carry freshly minted `X-Aithema-Session-Token` and
`X-Aithema-Delegated-Token` headers. These credentials never enter the outbox.
The service must deduplicate the immutable callback payload by
`Idempotency-Key`, independently of refreshed credential headers. Purge must
acknowledge `{sid, purged: true, host_artifacts: [...]}`; artifact refs must begin
with `sid:`. Only that bounded acknowledgement is retained; it is a list for
host-owned cleanup, never authority to delete arbitrary paths.

`/aithema/preview/{design_rev}?cap=…` proxies documents; `resource=base.css`
or `resource=tokens.css` proxies their stylesheets with the same capability.
The service-signed EdDSA JWT has exactly `iss`, `aud`, `tid`, `sid`,
`design_rev`, `iat`, and `exp`, uses a pinned `kid` and `typ: JWT`, and expires
within 300 seconds without expiry skew. The unsigned tenant claim selects
only public verification settings; signature and claims verification precede
any read or decryption of the service credential. Missing, foreign, invalid,
expired or revoked caps receive the same 404 before upstream access. Final host headers
permit only the configured picker script hash, same-origin CSS, data images
and fonts, and `frame-ancestors 'self'`; they set `SAMEORIGIN`, `nosniff`,
`no-referrer` and `private, no-store`. Native app documents allow
`microphone=(self)`, `frame-src 'self'`, and only the host's WebSocket origin;
this also covers later Vue navigation into Journey. Public portal documents
keep microphone denial. AEON-P05 owns the iframe's `sandbox="allow-scripts"`,
`referrerpolicy="no-referrer"` and empty `allow` attribute.

The origin-checked proxy is
`/api/projects/{projectId}/aithema/sessions/{sid}/proxy/{operation}`:
POST accepts only `input` and `ws-ticket`; GET accepts only an `inline`
WebSocket upgrade with a service-issued single-use `ticket`. The service owns
ticket consumption; the host checks live person authority before the handshake.
The host supplies service/session credentials and strips browser cookies and
forwarding headers. It never retries input or sockets, follows no redirects,
and suppresses upstream credential echoes, including WebSocket data and control
frames. Complete WebSocket messages (at most 1 MiB / 1024 fragments) are checked
before forwarding; compressed, extended or masked server frames are refused.
Service URL writes and every dial use one outbound policy: HTTPS with public
DNS answers by default, no redirects or ambient proxy, and only checked IP
literals are dialed. The deployment may explicitly configure an operator-local
service with `AEON_AITHEMA_OPERATOR_LOCAL_SERVICES`, a comma-separated list of
exact `host:port` entries (IPv6 uses `[address]:port`; no wildcards). Only
`location=operator` at one of those exact endpoints permits HTTP and
loopback/private IPs; link-local and other special networks stay denied.
Tenant settings never enable this exception, and a changed DNS answer is
checked again before any connection. No frontend assets changed in this host
package;
real renderer and native-view qualification remain AIT-P23 / AEON-P05.

Wide project headers can show an ambient ticket graph (Display → Graph in
project header). It uses a tilted 3D cloud with an optional elliptic force bias,
fits the densest 85% of nodes by height, and fades out inside the empty space
between the text block and counts. Reduced motion, narrow screens and sparse
graphs suppress it; hover exposes Open graph and Pause. Header framing and text
separation are covered by `web/tests/header-glimpse.spec.ts`.

Graph **Focus** fills the viewport and hides the app chrome. It prefers browser
fullscreen when permitted and keeps the in-app full-frame layout when the API
is absent, refused or ignored. Escape or **Exit** returns to the same graph,
filters and selection. Open `/p/AEON/tickets?view=graph&focus=1` directly for
in-app focus; knowledge graphs use the same parameters on their knowledge route.
Focus preserves the graph's reduced-motion preference.
