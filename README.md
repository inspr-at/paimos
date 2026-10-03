# PAIMOS AEON

PAIMOS AEON is an open-source, self-hosted work platform for people and AI agents in the INSPR family. It brings projects, tickets and knowledge into a fully dynamic work tree, with list and outline views, search and live updates.

Agents-first and voice-first, Aeon gives people a web workspace and agents a CLI and API, with tenant isolation and scoped permissions. The stack is Go, Postgres 18 + pgvector and Vue 3, built around nodes, relations and an append-only event log.

Find published builds in [GitHub Releases](https://github.com/inspr-at/paimos/releases). PAIMOS AEON is licensed under [AGPL-3.0-only](LICENSE); third-party notices are in [NOTICE](NOTICE). See [SECURITY.md](SECURITY.md) to report a vulnerability privately.

Run Aeon on your own server with the [self-hosting guide](docs/SELF-HOSTING.md)
and [reference Docker Compose stack](deploy/compose/compose.yaml). Published
images use explicit release versions; there is no `latest` tag.

## Code health audits

AEON-571 defines the ongoing code-health workflow. Each run belongs to a child
ticket of that epic and produces ticket attachments, rather than committed
findings. Recurring work and queue dispatch belong to PAIMOS (AEON-573); until
that integration ships, the coordinator starts these runs manually.

| Run | Trigger | Scope |
| --- | --- | --- |
| Delta audit | Each published release | Files changed since the last audited release, by slice |
| Tool sweep | Weekly | Static analysis compared with the accepted tool baseline |
| Rolling deep read | Weekly | Two of nine slices in rotation; a complete rotation takes about five weeks |
| Full audit | Quarterly or on demand | All nine slices plus the tool sweep |

Readers use [the shared rubric](scripts/audit/rubric.md), record each assigned
file and findings in [the slice schema](scripts/audit/finding.schema.json),
and obtain independent verification from another model family. Check current
tickets before reporting known problems. Deduplicate against open findings by
the emitted `fingerprint` (files + theme + title, ignoring line-number churn,
title case and whitespace); link existing tickets in `related_tickets`.
Critical findings get an immediate ticket and operator notice; high findings
go to their theme's queued ticket; medium/low findings are batched by theme
with a weekly summary of new themes. This toolkit does not send notices,
create tickets, run models, schedule work or publish pages.

The tools in `scripts/audit/` use Python 3.10+ and the standard library, Git,
and Node for the optional renderer smoke check. They run offline, without
installing packages or invoking analysis tools. Supply locally collected,
sanitized diagnostics in the shared `T` manifest; never include credentials
or raw secret scanner output. Tool installation, advisory downloads and
analysis run separately on the approved build/test runner.

Start with an immutable snapshot and inspect `assigned` in the coverage
report to dispatch readers. The slice map uses repo-relative, case-sensitive
globs: `*` stays within one path component and `**` crosses directories.
`exclude` removes deliberate overlap (S9 excludes S8's views/components).
Unknown code extensions can be added to `code_extensions`/`code_names`;
new packages need explicit ownership. Ambiguous ownership and unassigned
code fail with exit 1, malformed inputs with exit 2. The map covers deploy
Compose, root/web embeds, web configuration and end-to-end tests as well as
the original nine areas. Assigned context files also require manifest entries;
use `read`, `generated`, `vendored` or `not-code` with a nonnegative line count.

```bash
AUDIT_DIR=tmp/code-health
AUDIT_SHA=$(git rev-parse HEAD)
mkdir -p "$AUDIT_DIR"
python3 -B scripts/audit/covcheck.py --sha "$AUDIT_SHA" --out "$AUDIT_DIR/assignment.json"
```

An assignment-only check proves ownership, not reading. Each reader writes
`S1.json` through `S9.json` with `slice`, `sha`, `coverage`, `findings` and
`good`. The coordinator then validates the actual manifests. For a rolling
read, pass `--slice S1 --slice S2` and just those two `--manifest` arguments.
For a delta audit, pass `--since <last-audited-commit>`; only changed files
still present at the new snapshot require coverage. Whole-tree ownership is
still enforced. Deleted paths do not need reading at the new snapshot.

```bash
# Full audit; all nine manifests must already exist.
coverage_args=()
merge_args=()
for manifest_file in "$AUDIT_DIR"/S[1-9].json; do
  coverage_args+=(--manifest "$manifest_file")
  merge_args+=(--manifest "$manifest_file")
done
python3 -B scripts/audit/covcheck.py --sha "$AUDIT_SHA" "${coverage_args[@]}" --out "$AUDIT_DIR/coverage.json"

# Weekly sweep: compare first. The checked-in baseline is an empty seed.
python3 -B scripts/audit/baseline.py compare --input "$AUDIT_DIR/T.json" \
  --baseline scripts/audit/tool-baseline.json --out "$AUDIT_DIR/T-new.json"

python3 -B scripts/audit/merge.py "${merge_args[@]}" --manifest "$AUDIT_DIR/T-new.json" \
  --reviews "$AUDIT_DIR/reviews.json" --release 'audited release label' --out "$AUDIT_DIR/merged.json"
python3 -B scripts/audit/groups.py --audit "$AUDIT_DIR/merged.json" --out "$AUDIT_DIR/grouped.json"
python3 -B scripts/audit/build.py --audit "$AUDIT_DIR/grouped.json" --out "$AUDIT_DIR/audit.html"
node scripts/audit/run.cjs "$AUDIT_DIR/audit.html"
just audit-check
```

Reviews are a JSON array of objects with `id`, `verdict`, nonempty `evidence`,
and optional `severity`, `note` and `pass`. Verdicts are `confirmed`, `partly`,
`refuted` or `duplicate:<finding-id>`; the prototype's `BEGIN-VERIFY`/`END-VERIFY`
JSONL envelope is also accepted. Review IDs must match candidates from the
same run, or use `T:<theme>` to verify a tool theme by sampling as in the
prototype. Individual tool verdicts take precedence over a theme verdict;
the tool table reports candidate counts and mixed verdicts per theme.
Missing reviews remain `unverified` in `dropped`; only confirmed or
partly confirmed findings appear on the page. Original severity and verification
evidence stay with each finding. Fingerprint duplicates retain merged locations,
related tickets and aliases, with each original candidate in `dropped`.
Mixed snapshots, conflicting reviews, duplicate IDs and invalid duplicate
targets fail rather than silently overwriting evidence.

`groups.py` derives themes from the retained findings. Optional
`--descriptions PATH` supplies a JSON object keyed by theme, with `title`,
`summary` and `tickets` for editorial copy; no fixed finding IDs are bundled.
The page keeps the prototype's search, severity filters, expandable themes,
coverage, tools, strengths and method. It calculates totals from input, escapes
audit text and uses local fonts. Identical inputs produce identical JSON/HTML;
there are no wall-clock timestamps or embedded claims about a previous audit.

Accept a baseline explicitly **after reviewing** the tool candidates:
`python3 -B scripts/audit/baseline.py accept --input tmp/code-health/T.json
--baseline scripts/audit/tool-baseline.json --out tmp/code-health/tool-baseline.json`.
It stores sorted SHA-256 fingerprints only, including the optional `tool`
producer identity (defaults to the theme), and retains known hashes across
clean sweeps. Later runs use the latest accepted attachment as `--baseline`;
acceptance is never implicit during comparison. Findings, reviews, reports
and accepted baseline attachments stay under ignored `tmp/` or outside the
checkout, and are attached to the run ticket. The stable living-page link,
trends and slice-coverage age are coordinator publication concerns.

## Recurring work

`aeon recur create|list|get|update|pause|resume|run-now|preview` manages
server-owned schedules. Create reads a JSON definition from stdin or
`--body-file`; update takes the recurrence UUID and a full definition with
`expected_revision`. Pause/resume accept `--revision`, or read the current
revision before writing. Run now requires `--idempotency-key` so retries return
the original occurrence receipt. Named instances and `--json` work as usual.
Create and update also accept repeatable `--tag NAME|UUID`, resolved in the
target project or workspace and added to the JSON template's tags.
`--description-file FILE` replaces the template description;
`--criteria-file FILE` replaces its criteria with one nonblank criterion per
line. Other template fields remain as supplied in JSON. Only one input can
read stdin (`-`). For example:

```sh
aeon recur create --body-file audit.json --tag website-audit \
  --description-file playbook.txt --criteria-file checklist.txt
```

A definition names a project and an immutable parent, a ticket/task template,
and a trigger. Time triggers accept `FREQ=DAILY`, `FREQ=WEEKLY;BYDAY=MO,FR`, or
`FREQ=MONTHLY;BYMONTHDAY=1,-1`, plus `time_of_day` (`HH:MM`), an IANA `timezone`,
and an optional `start_date` (defaults to today's local date). Omitted weekday
or month-day comes from that start date. Monthly rules also allow `INTERVAL=1`,
`2`, `3` or `6` (default `1`), stepping from the month of `start_date`; daily
and weekly rules reject `INTERVAL`. For example,
`FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1` runs on the last day every three months.
Other RRULE parts are rejected. Invalid
month dates and spring gaps are skipped; a repeated fall time uses its first
instant. Preview uses the same calendar and returns UTC timestamps without
writing anything, including while paused. Event triggers use
`{"kind":"event","event":"release.published"}`; future publication dates
cannot be previewed.

For example, a weekly tool sweep under the code-health epic can be created with
this body (replace project/parent UUIDs with the intended existing nodes):

```json
{
  "project_id": "<project UUID>",
  "parent_id": "<code-health epic UUID>",
  "template": {
    "title": "Weekly tool sweep {{date}} (#{{occurrence}})",
    "description": "Run lint, gosec, govulncheck, semgrep and npm audit; compare the stored baseline and verify new findings.",
    "acceptance_criteria": ["Compare the baseline", "Verify and deduplicate new findings"],
    "estimate_hours": 0.34,
    "priority": "high",
    "type": "ticket"
  },
  "trigger": {"kind":"time","rrule":"FREQ=WEEKLY;BYDAY=MO","time_of_day":"09:00","timezone":"Europe/Vienna"},
  "queue_each": true,
  "overlap_policy": "skip",
  "catch_up_policy": "one"
}
```

The rolling deep read uses the same weekly trigger with its own template and
3-hour estimate. A per-release delta audit uses the event trigger and a template
such as `Delta audit {{release_name}} ({{release_version}})`, with a 1-hour
estimate. These are configuration examples; installing a release creates no
production audit schedules automatically. Template variables also work in
descriptions and criteria. `tags` contains existing tag-node UUIDs; occurrence
creation resolves their current names into ordinary ticket `fields.tags`.

The server uses the database clock and tenant/tree claims, with durable unique
occurrence keys. Each pass consumes at most the latest missed time or release
per recurrence, avoiding a downtime burst. `overlap_policy=skip` suppresses work
while any earlier live occurrence ticket remains nonterminal; `create` allows
concurrent occurrences. Created and skipped attempts both increment the
occurrence number. Resume discards work accumulated while paused; manual run
now is allowed while paused and does not move the schedule. Queueing reuses
queue readiness, work orders and the inert queue holder, awaiting coordinator
routing. It requires a positive estimate and acceptance criteria.
Each tenant pass attempts at most 50 due rows. A failing row rolls back,
records a `recurrence.failed` audit event when its claim is available, and is
excluded for the rest of that pass so later rows can run. The pass reports
the accumulated failures. Its original cursor remains available for retry
on the next pass; failed attempts do not increment the occurrence number.

Journey publications and the configured product's immutable release history
produce tenant/project-scoped `release.published` events; the same project and
version are deduplicated across sources. Newly configured schedules ignore
publications from before their creation. Occurrence tickets and their node,
queue and recurrence audit events use the keyless system actor **Recurring
work**. Occurrence receipts link each ticket back to its recurrence. Management
requires `recurrences.manage`; agents need an explicit custom-role grant plus
a key scope, even when bound to Owner/Admin. Saving a definition also requires
`nodes.write`; `queue_each` adds `run.create` and `work_orders.write`.

The recurrence tests cover calendar/DST edges, preview, replica and foreground
claim barriers, restart recovery, overlap/catch-up, publication deduplication,
queue/provenance, transaction rollback, tenant/project RLS and permission
changes during a write. They use an injected database clock and channels rather
than sleeps. Regression coverage includes monthly intervals across short months,
gaps and folds, tree-writer lock barriers, CLI template files/project tags, and
persistent schedule/publication/queue failures followed by valid due work.
The full Go suite validates the shared authorization, work-order,
queue, server and OpenAPI integration alongside this package.

AEON-573 fix-round evidence: the full Go suite passed at `2f00b466` on the
approved offload host with `GOMAXPROCS=6 go test -p 4 -timeout 30m ./...`.
Overlaying only the new test files on `a2bd6890` reproduces both tree-writer
deadlocks (`40P01`), rejects the tenant FK key-share fence (`NOWAIT`), rejects
monthly intervals and both CLI template writes, and starves the later valid
row for each persistent cursor, publication and queue failure. The fixed
regressions pass, including denial after the barrier-controlled member
demotion. Source transfer used Git archives over SSH, without pushing a ref.

DSAR integration note (AEON-490): the inventory is absent from this branch and
its `origin/main` baseline. Migration 1115 introduces `recurrences` and
`recurrence_occurrences`, both tenant-scoped with forced RLS and project
visibility. Classify `recurrences.template` and `created_by_principal_id` as
personal; other definition, schedule and cursor columns as metadata. Locate a
recurrence by `(tenant_id,id)`. Classify the user-supplied occurrence key as
personal and other occurrence receipt columns as metadata,
located by `(tenant_id,recurrence_id,occurrence_key)`; linked ticket contents and
actor identity remain personal data in the existing nodes/principals/events
inventory. The coordinator must add these entries when merging onto the DSAR
inventory. No new secret storage is introduced.

## HTML attachment previews

HTML uploaded with `aeon attach` or the attachment picker remains an original
download on the app origin, with a script-denying CSP. HTML fragments named
`.html`/`.htm` and UTF-8 HTML documents also receive a **text thumbnail**: a
320×200 PNG of inert text, explicitly not a browser screenshot. The bounded
rendition reads at most 64 KiB/4096 tokens with a 250 ms deadline; it has no
network, browser, profile or process access and never executes CSS or scripts.
Original blobs and PNGs stay in the existing tenant/digest-namespaced file store.

Live previews are off by default. Set `AEON_PUBLIC_URL` and
`AEON_HTML_SANDBOX_ORIGIN` to exact HTTPS origins without paths or ports, on
**different registrable domains**. Missing/unsafe configuration or HTML larger
than 2 MiB leaves the download available and disables execution. The sandbox
host must be dedicated, freshly provisioned without existing service workers,
cookies, authentication, redirects or other application routes. Route both
hosts to the same Aeon instance, preserving the exact Host header; when enabled,
other hosts receive 421. Provision TLS and ingress in the owning deployment
repository. Disable access/request-body/URL logging, analytics, CDN caching,
authentication injection and response-header rewriting for the sandbox host.
Aeon refuses sandbox requests carrying cookies, Authorization or Service-Worker
headers and never runs its app/auth middleware there. Do not enable the setting
until the hostname/TLS/cookie/logging boundary has been verified by the operator.

An authenticated person requests a preview with
`POST /api/attachments/{id}/preview`. The no-store response carries availability
and, when enabled, a one-minute view-only capability. Never log, persist or share
that URL. It is bound to the login session, tenant, project, node, attachment,
digest and attachment revision. Each use rechecks live session, permissions,
visibility and immutable bytes. Logout, expiry, deletion, moves, edits or a
process restart invalidate it; load balancing to another replica fails closed.
At most 32 live grants per person and 4096 per process are retained. No new table,
API key, long-lived signing secret, browser storage or background browser worker
is introduced. The lightbox drops its iframe at expiry and when closed; already
delivered bytes in a separately opened tab cannot be recalled.

The lightbox uses exactly `sandbox="allow-scripts"` and
`referrerpolicy="no-referrer"`; the response independently enforces the sandbox
for a direct/new-tab view. Only inline scripts/styles and data images/fonts are
supported. Fetch/WebSocket, remote resources, workers, child frames, forms,
popups, downloads and same-origin access are denied. No frame message is acted
on by the parent, and frame content cannot resize the lightbox controls.
**Residual risk:** HTML can navigate its own browsing context (especially a
direct tab), potentially sending document content or its short-lived capability
in a destination URL. CSP/sandbox is not a complete network firewall. Use only
self-contained fragments, and never describe this feature as preventing all
exfiltration. See the [iframe sandbox flags](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe#sandbox)
and [CSP sandbox](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/sandbox).

## Ticket work queue

`aeon queue list`, `add <ticket>`, `remove <ticket>`, `move <ticket> <position>`,
`reset` and `next` manage ticket work on the existing agent run queue. People
need `run.create` in the ticket's project; coordinator agents need their live
coordinator role and scoped key. Plain workers cannot manage the queue.

New and Backlog become Open when queued; Blocked retains its place until
unblocked. Tickets need an estimate, acceptance criteria and a named blocker
when blocked. `aeon queue readiness <ticket>` returns missing fields and an
estimate suggestion. `POST /api/queue/{nodeId}/estimate` explicitly applies a
missing estimate; acceptance criteria and blockers use the existing ticket
edit/relation APIs. No acceptance evidence is invented.

Priority then FIFO is the default. Manual moves persist until Reset; arrivals
append during manual order. `add <ticket> --agent UUID --profile UUID` is the
advanced Start now path, first in that agent's separate line. Optional
`--account UUID` pins an account and never overrides its allowances. `next`
routes ready work; existing daemon reservation and fenced claim checks perform
pickup atomically, move the ticket to In progress and remove its queue marker.
Ticket API and CLI JSON include `queued: {position, by, at, …}` while waiting.
Capacity hours are advice, not a reservation or a hard cap.

The project queue and ticket drawer move work using the server's shared
positions, including Move to top of the displayed project queue. Readiness
accepts live blocker relations. A failed queue read exposes an error and Retry
even before any queue snapshot exists. Suggested releases prefer the server's
expected start; otherwise they use earlier visible work and parallel capacity.
The hover labels this local estimate: other projects, current runs and blocker
delays are not included. Unmeasured capacity or an unknown unblock time keeps
the suggestion empty.

## Model preferences

`aeon model resolve --ticket AEON-123` resolves the ticket's work kind,
complexity and role through Default → You → Project preferences and prints a
why line. `aeon model prefs [--project KEY]` reads the effective matrix. Both
commands are read-only; role-only `model resolve build` retains its existing
advisory response.

The API exposes `/api/model-preferences`, level and row PUT/DELETE routes,
`/api/work-kinds` and profile retirement at `/api/models/{id}/retire`.
Use a level's returned `revision` for edits and the DELETE `revision` query
parameter (0 for an absent level). People edit their canonical You slice;
Default and Project changes require `model_prefs.manage`, and agents only read.
Provider locks permit lower choices and flag a loosening in the view and trace.
Tightening stamps active runs without stopping turns; `running_outside` identifies
starting/running turns outside the new setting. Stamps never loosen. EU/local
routes require valid account evidence; without it, work waits with `residency`.
Work-kind lists use `limit`/`cursor` pagination; editor writes reject oversized
matrices or atomic re-stamp scopes. See `api/openapi.yaml` for the contract.

## Agent start plan

`aeon agents plan` shows the person's planned total, per-harness limits and
running counts; `--json` returns the same snapshot as `GET /api/agents/plan`.
The person reads their own plan. An agent key reads only its live person
creator's plan and needs the explicit `agents.plan.read` scope and live role
permission. There is no person or tenant selector. Existing coordinator keys
must receive this scope through the person's normal key-scope controls.
Plan reads, writes and running counts use the canonical person, including when
the caller or key creator is a linked alias. The snapshot returns that canonical
`principal_id`. With no canonical preference, a saved alias preference remains
readable; conflicting effective plans across linked rows fail closed. An
explicit person save updates the canonical row and reconciles existing alias
copies. Other preferences remain private to the calling principal.

The person saves the plan through `PUT /api/preferences/agents.working`:
`{"value":{"total":5,"limits":{"codex":4,"cursor":"off","claude":"no_limit"}},"expected_updated_at":null}`.
`expected_updated_at` is the exact timestamp from the last plan snapshot;
null requires an unset plan. The check and save are atomic across linked aliases.
A stale revision returns 409 without writing; re-read before another change.
Legacy clients may omit this optional precondition. The dial always supplies it,
discards queued stale edits on conflict, and shows the re-read values for review.
Total and numeric limits range from 0 to 30. Missing limits mean No limit;
numeric zero means at most zero, while `"off"` explicitly disables a harness.
Limits may add up to more than the total. Legacy `cap` (1–12) maps to total;
legacy area/model reservations are ignored, because reservations cannot become
maximum limits. Legacy values keep their original shape for existing UI
clients. No stored total defaults to 15, the existing launcher ceiling.

Running counts include unended, unarchived sessions owned by the person across
projects, including starting, waiting, idle, throttled and stopping sessions.
Silence alone does not release a slot. Ownerless legacy sessions count only
when their agent's key creators identify one person unambiguously. The API
returns counts without session or project details. Malformed saved plans fail
closed. `agentplan.CanStart` checks total and harness limits without changing
running work; launchers must serialize starts and check account room separately.
On **Agents**, the control reads “Run up to … agents at once.” The total and
harness limits save to the same canonical plan used by coordinators. The folded
line keeps compact − / + controls and a harness mark that cycles No limit →
At most → Off; a muted number means no own limit, capped by the total and measured
account room. Expanded numeric controls stop at 1; folded − from 1 selects Off.
A stored API/CLI zero stays visible and only + is enabled. Details grow below the
dial with each harness's controls and the
read-only Now, Accounts, Waiting and Checks. Folding is remembered separately
for the signed-in viewer in `agents.working.display`; it never changes the plan.
Arrow keys step a focused − / + or move between harness modes, preserving
browser and OS modifier shortcuts. Failed saves stay visible across successful
polls until another deliberate change. Repeated selections and arrows at a
boundary do not write. The live line announces total changes once.
Unknown account or queue readings stay explicit. The visible queue's ready work
is not reported as starting until the launcher picks it up. Lowering the total
or turning a harness off never pauses or stops existing work. Launcher
integration remains a separate AEON-540 part. The approved start-check copy
requires part D to read `/api/agents/plan` before every start and enforce the
total, harness limit and account-room checks before this UI is released; keep
AEON-540's pill and benefit out of release notes until enforcement is live.

## Local models for in-app AI

Workspace AI is off by default. A person with `settings.manage` can open
**Settings → Workspace → In-app AI**, enter an OpenAI-compatible API base URL
and chat model, and select the features allowed to send content to that server.
For [Ollama on the Aeon server](https://docs.ollama.com/api/openai-compatibility),
use `http://localhost:11434/v1` and the name of
an installed chat model. Save, then use **Test connection**: it sends only a
small synthetic prompt, including while AI is off. Saving never contacts a model.
The endpoint is resolved from the Aeon server; in Docker, `localhost` means the
Aeon container. Use an address reachable from that container for a separate
model service. Public, loopback and LAN endpoints are supported; redirects,
link-local/metadata addresses, CGNAT, NAT64 and other special-use ranges are
refused.

The first connected feature is **Rewrite customer notes with AI**. Also enable
the `business_crm` plugin with its `tools.invoke` permission. A person with
`crm.manage` can request a rewrite; the selected server receives the customer
name and notes. The result is a revision-bound draft: a person reviews and
applies it separately. A changed customer or provider configuration refuses the
stale generation. Delegated agent runs and Aithema intake retain their existing
harness, plugin and approval controls; this setting does not select their models.

API keys are optional and encrypted through the existing tenant-bound AES-GCM
vault, separate from JSON settings and event data. Reads reveal only whether a
key is set. A blank replacement clears the key; omitting it preserves it.
Changing the base URL clears a retained key unless a replacement is supplied.
Keep the existing `AEON_SESSION_KEY_FILE` stable across restarts; development
hosts that store a provider key also need this persistent host secret.

Semantic search and background indexing can use the same provider and key with
a separate embedding model. Aeon's existing vector storage requires **1536
dimensions**. Enabling embeddings or changing their endpoint/model queues a
fresh index; vector identities include both, so search never mixes vector spaces.
Without a configured, enabled workspace provider and feature, CRM generation
makes no model request, indexing leaves its queue untouched, and search stays
lexical. The server now uses these workspace settings for embeddings; migrate
older `AEON_EMBEDDING_*` server configuration here.

## Morning briefing

People can open **Morning briefing** on Projects or `/briefing`. The daily
in-app reminder uses a chosen time in the device's local timezone (08:00 by
default). It reads existing completion and release outcomes, delivered-state
and recorded ticket merge evidence, Status autopilot deliveries and skipped
human checks, failed reviews/CI, current approvals, held human
requests and available journey decisions. Each fact links to its item and source.
The suggested next step is one of those person actions or recorded findings.

The first visit covers 24 hours; later visits start at that person's saved
briefing visit. The database statement start establishes the window alongside the first
event page; only complete successful log reads advance the saved server cutoff.
Denied logs, failed logs and log pagination limits retain the earlier cutoff.
Pending approvals, held requests, journey actions and usage are separate current
snapshots: their page limits or failures do not freeze completed log windows.
Very old visits are bounded to 366 days. Preferences use the existing tenant/person-scoped store;
there is no new activity tracking or generated narrative.

Usage requires `harness.read`. Its API list value is approximate, includes
lifetime usage of sessions **started** in the window, and is not interval spend
or an invoice. Recorded ticket totals reuse the planning columns' measured and
estimated figures and Paid semantics; account budget windows describe current
reported usage. Permitted windows remain visible when account budget coverage is
partial. The briefing explains privacy omissions and the independent window/account
truncation limit; shared windows are counted once across project reads.
Unknown usage stays unknown. Headline usage sums only projects with
`harness.read`, using the workspace dashboard only for a workspace grant; source
links appear only after successful reads. Merge facts use changes to a ticket’s
recorded `fields.merge_commit`, available under project visibility; PR URLs alone
do not count as merges. The visit-bounded autopilot log must also load completely
before the cutoff advances. Journey next actions use one batch snapshot query.
The additive `GET /api/decision-desk/projection` supplies the future Decision
Desk's canonical order and exact counts. Held approvals sort by expiry, then
other held work by age, then ordinary open items by age, with source ID ties.
Questions with several askers count once; a question replaces its linked action
request. Sign-ins remain separate chores. Counts cover currently readable sources
independently of page size; coverage above 1000 projects returns an explicit 422.
The current Agents badge continues to count the approvals and held requests its
panel can display. Agents does not poll the unused desk projection before the
P6/P8 cutover, so failures in that future source do not affect its refresh state.
Morning briefing retains actionable approvals with their scope labels/rationale
and held requests with their project/body and app Source links.
P6/P8 must switch desk, badge and briefing together when the complete desk UI and
question deep links are available; ordinary questions currently remain in the
server projection, without generating pushes.

AEON-568's notification adapter (`internal/decisiondesk`) exposes bounded
`NoticesTx`, final `ClaimTx` admission and `CurrentTx` reauthorization for the
existing AEON-455 phone scheduler. Eligible work has a real unfinished work
link or a correlated unresolved held request; a parked label alone is insufficient.
The proposed approval expiry-warning window is 15 minutes, subject to the phone scheduler's quiet hours and escalation.
Notice scans filter project/workspace decision authority before their limit.
The scheduler must admit every candidate through ClaimTx before its transport
check: an approval lacking its native scope authority gets a terminal skipped
claim and returns false, allowing later scans to advance. Final claims take the tree fence,
tenant access fence and source row lock, matching project-access mutations, then recheck only that source's current project,
revision, state and decision authority. Recipient indexes compare native UUIDs.
Claims persist once per source/revision/recipient across devices and replicas,
including action requests later represented by their canonical question.
An ambiguous transport failure retains its claim and records failure rather
than repeating a push. Payloads contain only source pointers. Answer commits,
grace edits, corrections and expiry never emit success notifications; doctrine
keeps its existing per-person toast claim and has no competing desk push.
Phone scheduler activation remains dependent on AEON-455 landing on main;
this package adds no scheduler or competing subscription store. Full P7 acceptance
remains open: AEON-455 owns runtime delivery, quiet-hour/escalation/subscription
tests, agreement on the warning window and integrated doctrine toast suppression.
The current adapter treats approval requests against unfinished ticket/task work
as held; the coordinator must confirm that policy before activation. Durable
claims retain retry evidence rather than being pruned while a source can recur.

The in-app briefing is available; push, e-mail and spoken delivery remain future work.

## Link a vendor account to yourself

On a paired computer, select your enrolled login with `paimos use` as usual.
`paimos harness run` matches that private login home to the existing registry and
shows one line for an unlinked account: `Link this account to you: <origin>/link
· code 482 913`. Open `/link`, enter that code and confirm the named account,
computer and person once. The terminal then says `Linked to Markus` once;
future launches do not repeat it. Capacity, groups and limits need no form.

For a standalone terminal, run `aeon-agentd link-account --harness claude`
(or `codex`, `grok`, `cursor`, `pi`). Multiple local logins require
`--account-id UUID`; `--setup-root PATH` or `--socket PATH` selects private
pairing state. `harness run` accepts the same optional selectors. The daemon
verifies the enrolled vendor login through its existing adapter before offering
the code. Codes last ten minutes and are single-use. An expired offer remains
expired until `link-account --renew` explicitly requests a new code.

**Your linked accounts** on `/link` and Settings → Accounts offers **Unlink**
as one action, only for the owner. Ownership is distinct from machine pairing,
ongoing-use approval and spending authority; none of those controls change when
a person links or unlinks. Other people on the computer link their own enrolled
logins using the same flow. The person must be signed in; agent keys cannot
confirm or unlink. Tenant-scoped, persistent rate limits and revision-bound
confirmation prevent code guessing, reuse and stale-session approval. Audit
records carry account and person IDs, never codes or installation proofs.

## Develop

```sh
just db-up        # Postgres 18 + pgvector on :55432 (Docker)
just test         # Go tests
just web-check    # web typecheck and build
just dev          # run the server (API on :8080); `cd web && npm run dev` for the UI
```

The ordinary activity tests check exact pagination through 240 same-ticket
imported history snapshots and 30 Markdown comments alongside 27,422 unrelated
imported events, plus the node index definition. They impose no latency budget.
For a controlled performance check, use an idle dedicated test runner with local
Postgres and run without the race detector:

```sh
AEON_TEST_DATABASE_URL="postgres://aeon:aeon@127.0.0.1:55432/aeon?sslmode=disable" \
AEON_ACTIVITY_IMPORT_SCALE_PROBE=1 \
go test -count=1 -run '^TestTimelineAtImportScaleLatency$' -v ./internal/activity
```

This opt-in probe uses the same synthetic fixture, excludes setup, warms up five
first-page requests, and samples 30 sequential requests with a page size of 20.
It measures the handler and JSON decoding; the nearest-rank p95 must be below
100 ms. Median and maximum are logged for diagnosis. It does not measure full
history traversal or guarantee production latency. Shared or loaded runners
are unsuitable for interpreting this budget.

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

Project sections have their own URLs: `/p/KEY/tickets`, `/p/KEY/journey`, and
`/p/KEY/knowledge`. A ticket uses `/p/KEY/TICKET`; `?section=journey` or
`?section=knowledge` retains its background section. Tickets is the default,
so its ticket links need no section query. Existing `?view=full` ticket links
still open the full-page ticket at the same address.

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

The flow UI is hidden by default. People working on Paimos itself can enable
**Show the flow controls (not yet tested end to end)** under **Settings →
Developer** (`/settings/developer#flow-controls`). This per-person, per-workspace
preference reveals the footer flow pill, Journey tab, stages and release walker;
it does not grant action permissions. Journey bookmarks explain the opt-in while
it is off. Turning it off removes the flow UI again.

Reserved, never-published versions are hidden in the release history by default.
**Show reserved versions** under **Settings → Developer**
(`/settings/developer#reserved-versions`) enables them for that person and
workspace, including comparison choices and previous/next navigation. Statistics
and result counts always include reservations; the footer's **N new** count
includes only visible versions. A direct link still opens a hidden reservation
with a quiet explanation of the setting.

If a standing candidate or deployment gate expires or is revoked before
deployment finishes, Journey offers renewal on the Deploy stage. An agent
requests a fresh release-bound approval; its person decider applies it with
`renew_candidate` or `renew_deploy` through the journey actions API, including
the current `release_id` and `expected_revision`. Candidate renewal precedes
deployment renewal and preserves enterprise reviewer independence. Each renewal
appends gate history and advances the journey revision without changing the
release identity. Existing handoffs lose authority; terminal evidence from
before either renewal is historical, so rerun preparation after both renewals,
then request a new deployment. Journey contract `journey/1.2` adds the optional
`next_action.renewal_action`; clients must use it when present. Existing action
keys and reporter major versions remain unchanged.

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

## Command line

`paimos` is the agent command line. `paimos serve` still runs the server. Existing doctrine commands keep their shape.

CLI issue and knowledge updates, estimate plans, project-tag attachment and
declarative plan updates send the revision of the node they read. A concurrent
edit returns a conflict instead of replacing newer fields; read the current
node before retrying. If a tag was created but could not be attached, the error
names that retained tag. A failed plan leaves earlier successful writes applied.

```sh
paimos auth login --url https://aeon.example --name default --key-file ./agent.key
paimos whoami
paimos issue list --project AEON
paimos mcp
```

`aeon status help --json` (also `paimos status help --json`) reads
`GET /api/status/help`: the ordered status definitions, hints, Queued explanation
and effective workspace rules. `--project KEY` resolves that project's
Inherit/On/Off override. The help sheet and agents use the same definitions;
the API reads live Status autopilot limits when its settings tables are present,
otherwise it explicitly reports the defaults. Queued means Open in the work
queue (AEON-522), rather than another stored status.

Tickets and tasks can carry `human_check`, nullable text describing what only a
person can confirm. Create or patch it through the nodes API, and filter lists
with `human_check=pending` or `none` (prefix `!` to exclude); request
`facets=human_check` for counts. A person marks it checked with
`PATCH /api/nodes/{id}` and `{"human_check":null}`. The server records the original
text, person ID and UTC time in `fields.human_check_completed`; replacing fields
preserves that provenance and cannot forge it. Only a person can set another
pending check when it clears a stored completion. Agents may add or edit pending
checks that have no stored completion. Check and Undo in the ticket use the existing revision
preconditions. Part B's automation skips pending checks when moving tickets to
Delivered or Accepted.

Status autopilot starts new workspaces with automatic rules enabled. Workspaces
present when migration 1108 runs enter Suggest mode for 24 hours, including
release publication hooks. Settings → Workspace → Status autopilot → Changes
lists proposed status moves and attention flags with Apply / Dismiss. Proposals
leave tickets untouched; Apply checks the current ticket and rule, and Dismiss
prevents that status episode from returning. An owner can explicitly select
**Enable automatic changes** to end the upgrade review period early; ordinary
settings saves and project On do not end it. The worker rescans when the review
period expires or an owner confirms, including within the same UTC day.

Operators can set `AEON_STATUS_AUTOPILOT=off|suggest|on` (default `on`) before
starting the server. `off` pauses all unattended status rules, including release
hooks and attention flags; `suggest` keeps proposals waiting for review without
an expiry. These server modes cap every workspace and project setting. Explicit
Apply is available in Suggest mode and forbidden in Off. Invalid values refuse
startup. An explicitly authorized daemon work claim still starts its ticket;
it does not become an unattended status proposal.

`aeon capacity next codex` shows the server's next eligible account and parallel
capacity; `--json` returns the ordered advice. It never reserves quota. The
Accounts plan uses that same order: soonest weekly/monthly reset, then larger
available cap, then account ID. Recent own use moves an account behind other
eligible accounts, without changing its budget. Short windows still constrain
every admission.
Workspace readers can request advice across their accounts; paired agents and
keys with only `account.probe` see only accounts registered by that agent.

Matching login fingerprints are hints. In Settings → Accounts, open an account
and choose **Pool with…**, then confirm the named accounts use the same vendor
login. Only those confirmed accounts share readings, holds and parallel slots;
confirmation replaces the pool with exactly the named accounts, leaving unnamed
previous members separate. Later enrollments require their own confirmation.
The dialog names every current member plus the account being added, so adding
a third account keeps the existing pair. **Remove … from pool** and **Stop sharing
quota** explicitly name every remaining member; the last pair returns to separate
readings and limits. A changed fingerprint clears that account's confirmation.
Existing accounts
start unconfirmed after migration 1054; prior holds can still settle or release.
The person-only API is `PUT /api/agent-accounts/quota-pool` (`account.manage`).
Ticket pins require edit permission in the ticket's visible project. If a queued
account leaves its routing group, routing releases its old hold and slot before
choosing a current member or returning a visible wait. Claims also release obsolete
quota or group holds before returning a conflict; a daemon that has already
journaled the route observes the unreserved queued run and routes again. Group
edits replace only visible project memberships and preserve hidden project fences.

Capacity learning uses tenant-local readings and usage only (AEON-388,
migration 1020). Three matching run samples enable a decaying p75 hold; five
observed work days enable an Auto reserve normalized to the window's usable
work hours. Managed overlap is excluded from own-use learning. Blind accounts
show consumption until a vendor stop and a known or observed cycle support an
estimate. Estimates carry uncertainty and evidence, never replace a fresh
measurement, and never lift a vendor denial. Aging readings include observed
burn; off-day pacing uses learned throughput to spend what would otherwise
expire. The Accounts disclosure shows evidence and hours/Away/sleep suggestions;
only an explicit save changes a schedule. Learning state is bounded and
contains no local paths or credentials.

The learning regression fixtures cover twelve-run p75 holds, five-day Auto
reserves, blind-limit calibration and replay, exact-model token conversion,
fresh-measurement precedence, tenant isolation, and weekend throughput. Browser
coverage exercises the shared Agents/Usage evidence and explicit hours action
at 1600/390 pixels in light and dark themes.

`aeon capacity next codex --env` prints a shell-quoted config-home export only
when the selected account belongs to the authenticated local agentd. Use
`--shell fish` for fish, `--socket` for an explicit daemon socket, or
`--setup-root` for non-default pairing state. Paths stay on that computer and
never enter the server response. The command advises outside launchers; it
does not enforce their consumption.

A terminal vendor-limit failure waits on its account when the reset is within
20 minutes. Longer stops create one linked retry on the next eligible account
at the next daemon poll, preserving the work order and any person-selected
account fence and run-level group target through consecutive handoffs. The
original session records the handoff. The local daemon
requires proof that the previous process stopped and the same recorded workspace
and branch before continuing. No eligible account means a visible vendor wait;
Run now once is never inherited by an automatic retry. Account holds remain
1% until learned run costs are available (AEON-292 T6).

Named instances and the default live in `~/.aeon/config.yaml`. The agent API key is read from `--key-file` or stdin, never echoed, and stored under `~/.aeon/keys/` mode 0600. `AEON_URL` together with `AEON_API_KEY` (or `AEON_API_KEY_FILE`) is a process-only target. When the binary is `paimos`, `PAIMOS_URL` and `PAIMOS_API_KEY` work the same way.

Classic colleagues without a sign-in identity can join through **Settings → Access → People → Imported from classic → Invite this person**. The invite starts with their email and imported access; the administrator confirms roles they may grant. On acceptance, one active, unlinked imported account with exactly the same verified email (case-insensitive) in this workspace becomes an alias of the new person, with an audited reason. Classic identities and history remain intact. Multiple unlinked matches require manual **Link to person**, including inactive records or records that already have aliases; already linked, deactivated or existing link-target accounts are never automatically linked. A token alone cannot link an account. Invited access stays authoritative even for project-only invites; sign-in and later imports never restore workspace access from the alias's classic roles.

Create a CLI/script identity at **Settings → Access → Agents → New agent**: give it a name, a description, a role ceiling, and workspace or selected-project access. The **Ticket worker** purpose selects project-only access and suggests the smallest role you may grant that covers `nodes.read`, `nodes.write`, `comments.read`, `comments.write`, `events.read` (ticket activity and history), and `search.read`; Admin is never suggested automatically. Role options explain their effect for agent keys. The description is prefilled from the purpose and project keys, stays editable, and may be cleared after confirming **Create without a description**. A person with `keys.manage` can create it, within their own permissions; agent and role changes are audited together. The next sheet offers one-click **Full access**, **Ticket worker** and **Coordinator** presets and previews their scopes before applying only those permitted by the registry, creator and agent role. Scope rows show their label, id, registry explanation and risk; search matches names, ids, groups and descriptions. Built-in Owner, Admin and Member roles exclude `recurrences.manage` for agents; recurrence automation needs an explicit custom-role grant and a key scope. The same previews and search are available when editing scopes; rotation preserves original scopes only within the live ceiling. The first key is shown once, with a copy button (manual selection if clipboard access is blocked) and this instance's `paimos --instance <name> auth login --name <name> --url <origin>` command. Paste the key at the hidden terminal prompt. Computer pairing uses agentd and needs no API key; its page links directly to this alternative.

Manual rotation rechecks live permissions inside the replacement transaction. Shared roles combine workspace and project grants; generated private roles also cap replacement scopes at their configured workspace permissions, even when preserving the old scopes. A project binding cannot restore a scope removed from that private role. The coordinator’s derived project-only `rules.read` remains available. A rejected rotation leaves the old key unchanged and creates no replacement or audit event.

People with `keys.manage` can edit an active key in **Access → Agents → Edit scopes** without replacing its secret. **Full access** selects every agent-grantable scope within the agent’s role, editor and original creator ceilings. Per-group **All / None** changes only visible scopes in that group; presets and **All** never extend the agent’s role. A quiet note names the person-only actions: managing members, roles, keys and settings, reading keys, the access audit log, approval decisions, rule publishing, conversation watching, harness force-stop and recovery, ownership transfer and the customer portal. **Expires after** starts at **Keep current**, or can set 30, 90 or 365 days from saving, or **Never**, without rotation. The API accepts optional `expires_at`: omission preserves expiry, `null` means Never, and a timestamp must be in the future. Scopes and expiry apply atomically, retaining the same ID and secret. The sheet names the agent role when it limits a scope. A person who also has `roles.manage` may tick a permission they hold and confirm adding it to the agent's custom workspace role and key together. Shared-role impact is shown before confirmation; built-in roles remain fixed. The server rechecks the editor, original creator and live role under the tenant/key lock, then writes both changes in one transaction with one audit entry. Generated agent roles include `models.read`; migration `1061_agent_roles_models_read.sql` adds it to existing custom agent roles without changing key scopes. Unknown stored scopes are pruned and audited when the sheet loads or an edit succeeds. Changes apply on the next request and appear in the access audit. Removing every scope disables the key's access; revoked or expired keys cannot be edited.

The same change is available as `aeon keys scopes <key-id> --add harness.worker --remove nodes.write --session-file <private-cookie-file>` (repeatable/comma-separated scopes). The file contains an existing signed-in person's `aeon_session` cookie value; `-` reads it from stdin without echo. Use `--url` or the configured instance URL. This command neither stores nor prints the cookie; agent credentials cannot manage scopes. Permission denials can include `reason_code` (`missing_role_permission`, `missing_project_access`, or `missing_key_scope`); only a missing key scope after role authority passes includes `scope`. Agent session registration also requires `harness.worker`, preventing generations that cannot heartbeat or stop.

`whoami` and doctor's auth check use the same `GET /api/me` client call. A valid session or agent key can read its own identity without a workspace role or extra key scope, including project-only and empty-scope keys. Doctor probes public health and version information anonymously; schema and rules checks retain their own permissions. This grants no access to other workspace data or profile routes. Issue, knowledge, search and onboard commands use the current Aeon APIs. Commands whose API resource is unavailable exit 3. `aeon mcp` exposes a stdio interface; currently only `whoami` is implemented there.

`paimos model resolve review-gate --author-family codex` resolves a reviewer outside the author's family. `--author-family` accepts `openai`, `anthropic`, `xai` and `cursor`, plus harness aliases `codex` → `openai`, `claude` → `anthropic` and `grok` → `xai`. `pi` is ambiguous: pass the model's family explicitly. The API response and CLI JSON echo the normalised `author_family`; omitting it for other roles returns an empty string.

Release metadata lives in `version.json`. The version display uses the pinned INSPR presentation bundle, checked by `just release-check`; historical release metadata remains unchanged.

`node scripts/release-timing.mjs --rollout PATH --release LABEL --json` reports
cut → live, PR → merge, gate → live and rollback timings. `PATH` is a rollout
JSON file or directory; `--fixture scripts/testdata/release-timing/section1.json`
reproduces the recorded release baseline without GitHub access. GitHub calls
use fixed read-only templates. Lists keep a constant page width, de-duplicate
stable `id` / `databaseId` / `number` values and name capped, id-less or
incomplete search results in `collection.truncated`. Only deduplicated stable
IDs count toward `total_count`; duplicate pages cannot hide an unread tail.
The script validates timestamps with explicit zones, ordered intervals, PR
lifetime bounds and numbered rerun coverage (`1..runAttempt`) before computing
metrics. JSON `evidence` records carry `complete` or `partial` with reasons; partial metrics
are null, with observed sample counts reported separately. Forward rollout
records require `direction: "forward"`; `outcome`, release labels and sequence
numbers cannot establish direction. Use `direction: "rollback"` for rollback
records; explicit rollback timestamps also identify legacy rollback evidence.
A forward record may embed a provably later rollback. Complete forward
intervals take precedence over incomplete siblings; conflicting complete
records remain unknown with an ambiguity reason. A rollback never replaces
forward rollout timing, and the newest unfinished rollback remains unknown.
Missing or invalid ordering timestamps leave candidate selection ambiguous;
tied candidates with conflicting content also stay unknown. Identical rollback
intervals can still report their duration. An ambiguous release run cannot
supply a SHA or fall back to PR titles for PR, CI or status-gate timings;
independent rollout gate timestamps remain usable.
Unknown timing retains consistent release metadata for filtering. CI stalls
outside the rollout window cannot produce adjusted timings. The offline
baseline uses clearly marked fixture IDs and explicit forward directions;
its recorded timestamps are preserved.

Session **Messages** shows both directions of that session's conversation, newest
messages at the bottom. `aeon tell PERSON_UUID --project AEON -m 'Reply text'`
automatically uses `AEON_SESSION_ID`, `AEON_SESSION_FILE` (or
`AEON_SESSION_STATE_DIR/session.id`), then the registered harness binding when no
explicit source is configured. Ambient sessions are attached only when active in
the target project; an ended or unavailable session is omitted with a stderr note.
`--sender-session` overrides these sources and strictly requires your active
session in the target project.
Use `--reply-to MESSAGE_UUID` to link an answer to the person's question; linked
answers appear in the same thread even when sent without a session binding.
“Read” means the session acknowledged receipt; “Answered” means an accepted
counterpart reply exists in the loaded conversation. Unrelated principal history
and shared-inbox obligations stay outside the session thread.

### Agent work estimates

Setting a ticket/task estimate also fills missing `route_role`, `area` and `complexity`. The server uses title and string labels/tags, then the nearest ancestor's classification or title hints, then `build` / `backend`. Explicit values win. Every server suggestion carries `<field>_source: "suggested"`, the acting principal's `<field>_by`, UTC `<field>_at`, and `<field>_confirmed: false`. A person can confirm a suggestion or an unconfirmed agent classification by resubmitting the same value without source/by/at, or with `aeon issue update KEY --role build --area backend --complexity S`. Agent values remain unconfirmed; client-supplied stamps cannot confer confirmation. Existing ticket rows are not backfilled.

Complexity defaults to **S** for estimates up to 2 hours, **M** above 2 and up to 8 hours, and **L** above 8 hours. `build-hard` promotes one bucket (S → M, M → L, L → L). This is a deterministic suggestion, not a measured model-performance claim. Unconfirmed server-derived complexity follows estimate or role changes; explicit complexity retains its provenance. `aeon issue update KEY --estimate 2 --role build --area backend --complexity S` supplies all three, and issue JSON returns their stamps and confirmation flags.

Harness sessions return `model_profile_id` for a unique tenant registry match by harness, model and effort. Recognized trailing effort suffixes (`low`, `medium`, `high`, `xhigh`, `max`, `ultra`) move into `reasoning_effort`; `model_raw` retains the cleaned original string for audit. Unknown models, absent effort, conflicting explicit effort, and ambiguous profile versions stay unresolved. There is no inferred model alias or cross-tenant match. Model/effort heartbeats refresh the identity. An effort-only heartbeat preserves the stored original model string when a legacy session has no `model_raw`. The additive migration 1063 backfills only uniquely matched sessions, under tenant and project RLS, without changing registration replay digests. `SELECT aeon_backfill_session_model_profiles()` can repeat inside an authorized tenant transaction; it returns the updated-row count and leaves resolved and unknown rows unchanged.

Estimates are expected **agent hours until ready for review**, separate from a live ETA.
Set them with `aeon issue create ... --estimate-hours 2`, `aeon issue update AEON-317 --estimate-hours 1.5`, or `aeon issue estimate AEON-317 --hours 1.5 --source agent`. `--estimate 90m` remains an alias. Decimal hours and minutes are accepted; values must be greater than zero and at most 200 hours. The optional source asserts the authenticated principal kind; the server stamps the principal and time. Creating a ticket or task without an estimate prints a non-blocking warning. API callers can PATCH `/api/nodes/{id}` with `{"estimate_hours":2}` to change just the hours, or null to clear them; other fields survive. Do not combine this property with a replacement `fields` document.

Settings → Workspace → **Agent activity** applies to every project: **Off** collects and shows no activity, **Tool activity** uses sanitized tool observations without summary tokens, and **Agent summary** (default) prefers a public phrase for ten minutes, then uses tool activity. Heartbeat and attached-session replies include `agent_activity_mode`; agents should spend summary tokens only when it is `agent_summary`. `harness heartbeat --doing "Running Go tests"` reports a phrase of at most 60 characters. The heartbeat helper reads optional `doing` from its existing status file, anchoring freshness to the file's modification time, so an unchanged file never makes an old phrase fresh. Managed workers can query and report through `aeon_activity`. Hook observations live only in the current generation's existing private state directory. Approved conversation tails classify new tool envelopes; status-only attachment never opens a transcript. Automatic payloads contain only fixed phrases or safe source-file basenames, never raw commands, arguments, environment values or file contents. The agents table shows current activity below the name; details keep up to twenty changes and coordinators summarize their active workers. The existing `activity` busy/idle field remains unchanged; the additive phrase is `current_activity: {text, source, at}`.

Agent summaries, tool basenames and legacy heartbeat notes share one credential filter on the server and browser. It rejects credential words, known key prefixes, opaque tokens, credential URLs, environment assignments, invisible Unicode format characters and combining/enclosing marks. Status-file notes are screened before truncation, incoming notes before storage, and stored activity and histories before display; an unsafe summary falls back to valid tool activity. Legacy notes retain their 120-character limit and control stripping. Activity history follows the session's tenant and project visibility for both reads and writes.

`aeon harness run-heartbeat --status-file .agent-status.json` reads `pct`, `remaining_min` and `note` on every beat. Bound workers send the percent and a ready ETA anchored to the file's modification time plus the remaining minutes, including zero. An unchanged file's ETA can become overdue; after 30 minutes without an update, the CLI warns `stale_progress`. Minutes must be finite numbers from 0 to 524160 (364 days); ETAs outside the server's allowed window are omitted. Workers with `--worktree` default to that worktree's `.agent-status.json`; coordinators read a file only with an explicit `--status-file` and retain their separate live-ETA reporting. Missing, malformed or refused status files never stop the loop. If the server rejects status-file estimates, the CLI retries the heartbeat without those fields. Status reads retain the credential, symlink and hard-link fence.

Heartbeat responses carry non-blocking `warnings`: a working worker gets `missing_progress` after three accepted beats without a fresh percent, a working session with a bound `ticket_node_id` gets `missing_eta` for its role's absent ETA, and a visible bound ticket/task without valid hours gets `ticket_without_estimate`. Unbound workers and coordinators have no missing-ETA warning or UI hint. Warning-query failures are logged and return an empty array without rolling back the heartbeat. Both heartbeat commands print each code to stderr at most once per ten minutes, with receipts retained across reporter restarts. Run-heartbeat uses its private state directory; one-shot heartbeats use private 0700 directories under `~/.aeon`, including when the lease comes from stdin.

Coordinators report **ticket live**, including review, merge and deployment, with
`aeon harness heartbeat ... --eta-live +20m`. Their ETA does not mean the
coordinator process has finished. Coordinator beats reject `--progress` and
`--eta-ready` with an explanation: percent done is derived from their direct
worker children on the same bound ticket. Active workers and cleanly finished
workers count equally; unreported progress counts as zero. Failed, removed and
other-ticket children are excluded; no eligible children leaves progress absent.
The mean is rounded to the nearest integer and includes children outside the
current list page. This is a read projection; it never marks a coordinator Done.

Every coordinator can register AI, media and terminal children using the same
worker lease and parent/ticket/ETA/progress protocol. AI agents keep their
existing harness (`codex`, `claude`, `pi`, `cursor`, `grok`) and model flags.
Media uses `--harness media --generator higgsfield/kling3_0`; terminal uses
`--harness terminal --command ffmpeg`. Public labels are at most 120 ASCII
characters: letters, digits, `.`, `_`, `:`, `/`, `+`, `-` (terminal also permits
spaces). Start with a letter or digit; omit surrounding whitespace. These are
labels, never secrets or executable command arguments. Media/terminal require
`--role worker`, `--parent-session`, a ticket and a work shape; they neither
inherit `AEON_MODEL`/`AEON_EFFORT` nor look up the model registry.

For example, while a media job is running, start its reporter:

```sh
aeon harness run-heartbeat --owner-pid "$job_pid" --state-dir "$job_state_dir" \
  --project AEON --agent worker --harness media --generator higgsfield/kling3_0 \
  --parent-session "$coordinator_session" --ticket AEON-501 --work-shape ship \
  --status-file "$job_status_file" --host "$(hostname -s)"
```

The terminal equivalent uses `--harness terminal --command ffmpeg`; AI workers
use their usual harness and model. `harness register` accepts these same family
and label flags with its usual private registration files. Follow-up one-shot
heartbeats use the returned session id and lease with `--eta-ready +10m
--progress 40`, or keep `pct`, `remaining_min` and `note` current in the reporter's
status file. Each concurrent job needs its own private state directory.

The Sessions table retains its existing design, adds a separate **Host** column,
and calls the intended result column **Name**. Host identity is the exact
registered `--host`; `run-heartbeat` defaults to the short OS hostname (the part
before its first dot), without querying macOS ComputerName. People can click the
badge or its floating pencil to set **Your name for this computer**. Save applies
to all rows of that host for that person; **Use '<registered host>'** resets it.
Labels are stored server-side under tenant/person/host with person-only RLS.
`GET /api/me/host-labels` reads only your overrides; `PUT` with `{host, label}`
saves one and a null label resets it. Both require `harness.read`; a project-only
role with that permission suffices. Agents cannot read or write overrides.

Harness status and heartbeat return the response-only header `Aeon-Contract: harness-session/2.6`; no request header is required. Existing reporters keep working without a Pharos or Janus release. The warnings field is optional in the shared session schema and is returned as an array on heartbeats; existing response fields and request requirements are unchanged. 1.8 included `finished`, a required response boolean that is always present, false included, in every session, live and event payload (derived in SQL from a reported 100% and a recorded clean exit); readers that ignore it are unaffected, and no screen derives Done from `progress_pct` or `stop_reason`. It also adds the optional `row_version` (AEON-449): the session row's own revision, raised by the database inside every statement that changes the row, so the larger of two copies is the newer. Reporters may ignore it. 1.9 adds optional nullable `model_raw` and `model_profile_id` for auditable model identity; request requirements stay unchanged. 2.0 declares the expanded harness enum, including Gemini CLI and OpenCode (AEON-452), and adds the optional `generator` and `command` response labels and media/terminal families; existing fields remain intact. 2.1 adds optional `current_activity`, `current_activity_history` and `agent_activity_mode` for current activity reporting; request requirements stay unchanged. 2.5 adds optional `service_tier`, `service_tier_revision` and per-model `service_tier_reports`; 2.6 adds optional nullable `service_tier_request` for pending agent requests on list rows. Existing reporters and request requirements stay unchanged. The pin checker records expansion of an existing enum as a major schema change; existing harness values still register and heartbeat unchanged.

Pause levels (AEON-524 part A2) add `--level stop_now|pause_quickly|pause|wrap_up`
and an optional `--note` to pause one or all sessions. Omitted levels use the
person's workspace default, initially Pause; read or change it with
`aeon harness pause-default [--level pause_quickly]`. Pause quickly interrupts
where the harness advertises support, commits WIP as it is and requests a short
handover within three minutes. Pause chooses the next safe point within ten
minutes. Wrap up finishes only when the estimate is below ten minutes and fits
the deadline; otherwise it hands over. The API exposes `supported_pause_levels`
and `pause_can_interrupt`; workers without a cooperative inbox support only
Stop now. Pause all skips those workers unless Stop now is selected.

`aeon harness leaving-at --at 2026-10-02T17:00:00+02:00 [--note "Continue tomorrow"]`
sets one durable deadline for all of your running work in authorized projects.
Fresh finish estimates select Wrap up first, including near the deadline when
finishing fits with one minute to spare. Otherwise Pause starts at T minus ten
minutes; fresh handover and command timing choose Pause quickly when a full
handover cannot fit. Without that timing, escalation starts at T minus two
minutes. A current uninterruptible command keeps cooperative Pause, without an
interrupt signal. Stop now is requested at each level’s deadline. Pause and
Wrap up end at the earlier of the leave time and ten minutes after their actual
start; Pause quickly has a three-minute cap, with a two-minute escalation lead.
Workers without an inbox keep working until T. `aeon harness leaving-at` reads the deadline and
per-agent plans; `--off` withdraws pending requests and leaves paused handovers
resumable. A claimed stop cannot be recalled and is reported as in flight.
Setting the same deadline and selection again is idempotent; changing either
replaces pending plans. A deadline covers at most 200 owned running sessions
atomically, and rejects a larger selection before changing any plans.

Workers can include optional `step`, `next_point`, `next_point_in_min`,
`finish_in_min`, `finish_outcome`, `interrupt`, `command` and `command_left_min`
in a heartbeat. These form the optional `pause_progress` response snapshot,
with a database-clock `reported_at`; the current operation's `command` does not
change a terminal session's registered command label. Sending any planning
field replaces the snapshot (omitted fields are unknown, null clears); omitting
all of them preserves its original timestamp. Minutes count down from that
instant and expire after two ETA intervals. Once a planning snapshot exists,
an absent or stale finish prediction never falls back to an older ready ETA.
Legacy reporters without a snapshot retain their fresh ready-ETA behavior.
Text is limited to public labels without credentials, paths or command arguments.

The leaving API also accepts `{hosts: "all" | [host identities], agents: [session
UUIDs]}` alongside `deadline_at`. `agents`, when provided, selects the exact
owned running generations independently of `hosts`; ended and unauthorized
sessions are ignored. Omitted `agents` selects running work on the named hosts,
or all owned work when `hosts` is omitted. An empty agent array selects no work.
GET returns the effective selection and the stored host scope. Host scope is
persisted for the approved wind-down UI and later launcher admission integration;
this backend does not yet enforce refusal of new starts on those hosts.

Stop now uses the existing control queue. Agentd executes it only for its exact
live generation and owned process identity, with the existing monotonic signal
fence. A database-clock wake hint schedules the next heartbeat at each transition.
`harness run` advertises `owned_stop_v1` and may stop only the child it launched; a PID-only
`run-heartbeat` observer receives no signal authority. Requests, scheduling,
escalation, cancellation, defaults and confirmed outcomes are audited. A stop
request is not a process exit receipt: offline or older launchers can leave an
unconfirmed request, which remains visible in the deadline report. The server
never claims such a process has stopped. Existing part-A records without a
level retain their original cancel-on-expiry behavior. The response contract
adds optional fields at `harness-session/2.4`; request headers stay unchanged.

Pause/resume (AEON-524 part A) adds optional `pause` and `continuation` fields
in contract 2.2. `pause.state` distinguishes requested, planned, paused,
resume_requested, resumed and cancelled; the established phase values remain
unchanged. `state=paused` selects resumable closed generations, including those
older than 24 hours in `view=current`; existing stopped filters remain compatible.

```sh
aeon harness pause --project AEON --session SESSION_UUID --reason "Reboot"
aeon harness pause --all --except KEEP_RUNNING_SESSION_UUID
aeon harness resume --project AEON --all
```

A person needs `harness.control` and owns the registration (project owners/admins
may control other registrations). An agent coordinator uses `--project`,
`--coordinator-session` and its `--worker-lease-file` to control direct children.
Global `--all` spans the person's visible authorized projects; `--project` narrows
it. Batches return up to 200 items and `more`; repeat while `more` is true.
Requests and the default ten-minute handover deadline persist in Postgres.
The pause control uses the existing `stop` kind with `request_payload.pause=true`; managed
yield holds it aside so it cannot accidentally trigger an immediate signal.
Heartbeat delivers the durable request. `run-heartbeat --print-controls` emits
an `aeon.harness-pause.v1` JSON record; otherwise it prints guidance on stderr.
Agentd forwards a heartbeat pause through its existing generation-fenced inbox
when the adapter supports input, with the ordinary durable input receipt.
Managed sessions must advertise `inbox`; otherwise an individual pause returns
409 without creating a request. Project and person-wide pause-all leave these
sessions running, continue pausing capable sessions, and report the skipped
session's `id`, `project_id`, optional `display_label` and reason
`inbox_delivery_unavailable` in `skipped`. Reports are capped at 200 per call;
`skipped_more` indicates omitted reports. Skips do not consume the 200-pause
limit or set `more`, so repeating a batch can reach later capable sessions.
Cursor and Grok daemon runs currently lack that input path. Unmanaged CLI
heartbeat sessions advertise cooperative pause only with `--print-controls`;
one-shot `harness run` jobs advertise Stop now only.

An overdue requested or planned pause becomes `cancelled` on the database clock.
Heartbeat, session/control reads and the periodic sweep complete its cooperative
control with reason `pause_deadline_expired`. Level-aware requests also queue
Stop now; historical requests without a level retain cancel-only expiry. Before
the deadline, a
`heartbeat_lost` close preserves the pending or claimed pause control so the
same worker can heartbeat, plan and stop with its handover after revival.
An unarchived paused or resume-requested generation continues to occupy its
ticket, suppressing the status autopilot's stale-progress rule until it is
resumed or archived.

The worker must plan a safe stopping point, finish or roll back the current
step, commit WIP on its own branch, then submit a handover and exit cleanly:

```sh
aeon harness pause-plan --project AEON --session SESSION_UUID --agent AGENT_NAME --worker-lease-file LEASE_PATH --control-id CONTROL_UUID --handover-point "After the current commit"
aeon harness mark-stopped --project AEON --session SESSION_UUID --agent AGENT_NAME --worker-lease-file LEASE_PATH --reason paused --handover-file HANDOVER_JSON_PATH
```

The handover JSON contains `state`, a nonempty `next_steps` array,
`open_questions` (an empty array is valid), and `worktree_state` (`committed`,
`clean` or `rolled_back`). `committed` requires `commit_sha`. Session storage,
the bound-ticket comment, control completion and stopped generation are one
transaction; a retry cannot duplicate the handover. Notes are public project
content: never include credentials or private registration proofs.

`resume` persists a launch recipe and full continuation brief. For one session,
`--registration-file` may supply a fresh private reference and lease and create
the successor atomically; ordinary `harness register --succeeds SESSION_UUID`
and `run-heartbeat --succeeds` also consume that recipe. The successor keeps
the principal, harness/model, generator/command labels, branch/worktree and
ticket, and receives the full handover in `continuation.brief` rather than truncating it to the short
metadata label. Old run and process ownership are never reused. Resume paused
coordinators before their paused children; the new coordinator adopts those
children for continuation. Generation registration does not itself launch a
vendor executable. Agentd automatic launch after login and a hard-stop deadline executor
are separate runtime integration: any hard stop must use the existing exact
owned-process controls, and these HTTP endpoints never signal a process.

Reporter pins identify response schemas by their `METHOD /path status` labels.
`RequiredBump` treats a new required response property as a minor addition:
the server supplies it, and existing harness clients ignore extra response fields.
Components also referenced by any request body, including through nested schema
or reusable request-body references, retain request rules throughout their
expanded subtree. Pins store this provenance separately from the response hash.
New required properties on these shared components still require a major bump.
Changed existing properties, newly requiring an existing optional property, and adding a required
property to a previously closed response schema also remain major changes.

Agent drafts show `est.` until a person or a working agent bound to the ticket confirms or changes them. Resubmitting the hours through the estimate command confirms them; provenance is recorded again. The ticket's Estimate control also edits or clears the value. Epics show the sum of direct, visible, open ticket/task children, with estimated-child coverage in the tooltip; nested tasks are not counted twice. The Estimate sort keeps empty values last in either direction. Imported points remain visible as points, not converted to hours.

For a backfill, an agent drafts a JSON plan such as `[{"key":"AEON-317","hours":2},{"key":"AEON-318","hours":0.5}]`, then runs:

```sh
aeon issue estimate --missing --project AEON --from-file plan.json --dry-run
aeon issue estimate --missing --project AEON --from-file plan.json --apply
```

Both modes validate every plan entry and project membership before any write. Apply uses the agent identity, skips work already estimated and checks each node's revision. A concurrent change stops the plan; earlier successful writes remain applied and a rerun skips them. There are no server-side model calls.

## Independent commit reviews

The ticket panel's **Cross-family review** section requests a review of a repository and two full commit hashes. A completed managed builder run also requests one automatically when its daemon reports a produced commit range and its key already permits `work_orders.write` and `run.create`. The reviewer receives the ticket snapshot, acceptance criteria and exact bounded diff through the existing managed-run queue. This slice never merges.

Review work orders have immutable author, reviewer, repository and range bindings. The registry's `review-gate` profiles provide model/version pins; eligible strong or frontier profiles require `xhigh`, exclude the author family, and follow Codex → Grok → Claude. Only qualified adapters with tools, hooks, plugins and inherited settings disabled can launch a review. Today that means the Claude bridge or a qualified native Grok enrollment on arm64 macOS; unsupported adapters, unavailable accounts, exhausted capacity and missing approvals remain visible fallback reasons. Profile families must match their fixed harness provider or a supported explicit provider/model binding. Unknown providers cannot establish review independence. Inconsistent legacy pins remain immutable history but are excluded from routing, evidence and approval; replace them with a correctly labelled profile. Legacy daemons cannot receive or claim review runs. The daemon uses scratch space outside the repository and supplies a bounded text diff without credential paths or known credential forms. Binary or oversized changes require another review path; they cannot pass this gate.

Review context is built in a private, temporary bare Git repository. Only the
sealed base and head objects are fetched from the workspace through a transport
view with helper-owned metadata; workspace configuration, hooks, attributes,
grafts and replacement refs are excluded. Ancestry follows raw commit parent
headers, and the diff compares the two sealed trees (`base..head`) with renames,
external diff commands and text conversion disabled. Submodules show gitlink
hashes only; sensitive paths are refused before reading the patch. Missing or
invalid objects, incomplete ancestry, ambiguous duplicate packed HEAD records,
a walk beyond 4096 commits or a context build beyond 30 seconds fail closed.
Git output is capped at 192 KiB while the subprocess output is copied; an
oversized patch fails before it can grow the daemon's buffer past that cap.
Scratch repositories are removed on success and error.

Each finding uses `FINDING: <critical|high|medium|low> <relative-file>:<line> <message>`. The final line must be exactly `VERDICT: ok` or `VERDICT: changes`. Only a completed run with vendor-reported model evidence and a valid, independent `ok` opens the gate. Missing output, malformed verdicts, cancellation and unavailable routes leave it closed. Findings, elapsed time and vendor-reported cost appear on the ticket; unreported cost is omitted. Repeated request IDs safely replay the same binding; a changed range requires a new request.

Optional GitHub status reporting is host-owned. Configure all of `AEON_REVIEW_APP_ID`, `AEON_REVIEW_INSTALLATION_ID`, `AEON_REVIEW_APP_KEY_FILE` (absolute physical path to a private RSA file, mode 0600), `AEON_REVIEW_APP_TENANT_ID` and `AEON_REVIEW_APP_REPOSITORY` (`owner/repo`). The installation token is narrowed to that single repository, with `statuses:write` and `pull_requests:read` only. Specify the PR number when requesting a review. Reporting pages through all latest publishable repository/head reviews; reviews without a PR cannot hide the status owner. Independent publication and refresh passes check the exact PR, repository, base and head before posting `aeon/review`. Successful bindings are checked again without duplicate success posts. A changed binding revokes success on the originally reviewed SHA and marks the review stale; restoring the base does not revive it, so request a new review. Failed revocations remain closed locally and retry until GitHub accepts the error status. Temporary tokens are revoked. A moved PR cannot receive an approval for its new head from an old review. The repository owner must separately make `aeon/review` a required branch-protection status and restrict its source to this App; Aeon does not change protection rules. Without configuration, local reviews continue normally. See [GitHub commit statuses](https://docs.github.com/en/rest/commits/statuses) for repository-side enforcement.

For immediate base-change revocation, provision `AEON_REVIEW_WEBHOOK_SECRET_FILE` (absolute physical path, private mode, 32–4096 bytes) and configure the same secret on the GitHub App webhook at `/api/reviews/github`, subscribed to pull requests. Signed `edited`, `synchronize` and `reopened` deliveries are checked against the host-owned installation and repository. A changed base closes the matching review before posting an error, independently of unrelated dirty publications; a failed post returns 502 and remains retryable. Replayed deliveries never approve or reopen a review. The separate refresh scan remains the backup for missed deliveries and base-branch advances. Without the webhook secret, immediate delivery is disabled and the scan still runs.

## Lead handover and short review/fix jobs

A coordinator registered with the same principal, harness and native session
reference automatically takes over its stopped or heartbeat-lost predecessor's
live children. The transaction records one `harness.adopted` event per child and
`harness.handed_over` on the old lead; stopped children remain historical.
A healthy lead is never replaced. `harness run-heartbeat --role coordinator
--source-session NATIVE_UUID` uses that stable native reference; use a fresh
private state directory for the new process generation. After an unclean
restart, the helper retries an active-generation registration conflict at the
heartbeat interval while its owner process lives, logging each retry. Once the
predecessor's heartbeat expires, registration and child adoption proceed
automatically. For a different native session, pass `--succeeds OLD_SESSION_UUID`
to `harness register` or `harness run-heartbeat`. The predecessor must belong to
the same principal and project. A handed-over generation cannot revive through
a late heartbeat.

A Claude coordinator can register Claude children with the same authenticated
`--agent`, `--parent-session LEAD_UUID` and a distinct private session reference
and worker lease for each child. Child registration ignores the launcher's
ambient `CLAUDE_CODE_SESSION_ID`, which belongs to the parent. For a child's own
native binding, give `harness run-heartbeat` that child's `--source-session UUID`;
manual registration can use the child's native ID as its private session ref.
Explicit vendor references remain unique among active generations per tenant and
agent across all harnesses. Registration conflicts name the conflicting field
without returning private values. Vendor binding conflicts also append
`(sha256:<16 hex characters>)`, a fingerprint of the supplied reference using
the first eight bytes of `SHA-256("aeon.harness.ref\0" + reference)`. The same
reference has the same fingerprint on insertion and replay; the reference
itself and worker lease remain private. Concurrent children use separate
`--harness-session-file` and `--worker-lease-file` files.

On **Agents**, the old lead links to its successor and adopted workers link back
to the old lead. Drag a live worker to a live lead, or choose **Move to lead…**
from its menu. A person must own both registrations or be an owner/admin in the
same project and hold `harness.write`; sessions whose legacy owner is unknown
require an admin. The server checks revisions and rejects cycles. The toast's
**Undo** rechecks rights and hierarchy changes; an ordinary heartbeat does not
invalidate it. Moves preserve ticket bindings and estimates.

For flywheel gates and fixers, wrap the existing command without changing its
own review, sandbox or permission arguments. Retain the launcher's environment
sanitization (including the three `CLAUDE_CODE_*` messaging variables):

```sh
aeon harness run --project AEON --ticket AEON-322 --label "Handover review" \
  --role reviewer --harness claude --parent-session "$LEAD_SESSION" \
  -- claude -p "Review the prepared AEON-322 diff read-only"
aeon harness run --project AEON --ticket AEON-322 --label "Handover fixes" \
  --role fixer --harness claude --parent-session "$LEAD_SESSION" \
  -- claude -p "Apply only the accepted AEON-322 fixes"
```

The helper registers before launching, heartbeats while the command runs and
marks the generation stopped on success, nonzero exit, launch failure or
SIGTERM. It preserves stdout, stderr and the command's exit code; SIGTERM exits
143 after settling the session. It signals only the process group it launched,
using the existing ownership fence, with a five-second TERM grace period.
`reviewer` and `fixer` are worker jobs with a public activity note; reviewers
default to `scout`, fixers to `ship`. Project defaults to the ticket prefix,
agent to the authenticated caller, and harness to a recognized command name.
No command text or arguments are sent to the server. The default private state
directory is retained for recovery; `--state-dir` selects an explicit new one.
A failed stop prints that directory and retains the existing heartbeat retry
intent. Reuse `run-heartbeat` to settle it; `run` refuses to launch a second job
from an already-used directory. These helpers confer no additional permissions
and do not replace the ticket's worker marker or release review gates.

## Install the CLI

Nix installs `bin/aeon` and a `bin/paimos` symlink:

```sh
nix profile install github:inspr-at/paimos#aeon
```

GitHub release assets, next to `paimos-agentd` for the same four OS/architecture pairs and listed in the same `SHA256SUMS`: `aeon-cli-darwin-amd64`, `aeon-cli-darwin-arm64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64`. Put the CLI file on `PATH` as `aeon`; a symlink named `paimos` selects its compatibility mode. For checksum-verified computer pairing, see [Agent integration](docs/AGENT_INTEGRATION.md).

With the reviewed Nix package, run `env "$HOME/.nix-profile/bin/aeon-agentd" pair --url 'INSTANCE_ORIGIN_FROM_GUIDE'` from your working folder (bare `aeon-agentd pair` asks for the origin or resumes the saved instance). Confirm the folder, select detected signed-in harnesses, then enter the 9-digit code in the browser and approve as a person. Pairing creates its own private state; Nix/Home Manager retains service ownership.

**Connect your machine** offers the checksum-verified direct download pinned to this server’s version first; add `~/.local/bin` to PATH for that installation. On a Mac without Nix, Homebrew is always offered as `brew install inspr-at/tap/aeon-agentd`, and the pair line runs `env "$(brew --prefix)/bin/aeon-agentd" pair --url '…'`. Homebrew installs the latest INSPR release; `aeon-agentd status` tells you if this server needs a different version by checking its declared compatibility window. Neither the server nor the browser reads the tap formula. After browser approval, pairing installs the user LaunchAgent on macOS or systemd user unit on Linux. It retains Homebrew's stable `bin/aeon-agentd` link or `~/.local/bin/aeon-agentd`, so a package upgrade does not leave the service pointing at a removed version. The stable link must resolve to the binary doing the pairing; an unrelated service is never adopted or overwritten.

Owner workstation step-up (AEON-580/581): the shared CLI/MCP HTTP client handles
`428 step_up_required` by sending only the challenge ID to the local paired daemon.
The daemon fetches the challenge from its paired origin with its own pairing proof,
shows the server's summary verbatim in Touch ID, and signs with the pairing-pinned
Secure Enclave key. Cancellation, expiry, unavailable Touch ID and a refused proof
fail the action; the client retries the identical request at most once, without
redirecting its proof. Server-side marking, action/tenant binding, single-use
consumption and governance exclusions are owned by AEON-580.

`aeon-agentd status` reports whether the pairing has a pinned confirmation key and,
for older Mac pairings, prints the exact `aeon-agentd pair --url … --state-root …
--workspace …` command using a new private state folder. The old pairing remains
until the new one is verified; service configuration changes stay with its existing
owner. Access → computers shows the pin readiness separately from connectivity.
This prerequisite does not promise that the installed daemon can currently use
Touch ID (a signed enclave-enabled build and the owner's graphical session are
still required). For a nondefault pairing, set `agentd_state_root` to its absolute
path in that instance's CLI configuration. The client checks the daemon's origin
before prompting, keeping separate instances separate. Direct Go HTTP consumers
set `Client.ConfirmStepUp` to their authenticated local-daemon callback.

To remove a pairing, run `aeon-agentd disconnect` and wait for `disconnected` before uninstalling. This freezes new work, requests server revocation, waits for owned processes to drain, then stops and removes its own service. `--once` reports one resumable step; interruption or lost connectivity leaves cleanup pending, and the same command resumes it. Vendor sign-ins and project files are preserved. Homebrew users then run `brew uninstall aeon-agentd`; checksum-installer users remove the `~/.local/bin/aeon-agentd` link and downloaded versions under `~/.local/lib/aeon`. Nix users disable/remove the service and package through their owning configuration's review path. Retain private pairing state until cleanup and any accounting recovery are complete; `--state-root` selects a nondefault pairing.

Claude dependency pins preserve stable Node and SDK links, including Home Manager links. Each probe and start checks the full link chain, ownership, directory permissions, workspace exclusion and the SDK's declared package entry, then launches the resolved physical paths. Existing physical pins remain supported. To replace old pins after an update, run `aeon-agentd repin --harness claude --node-path /absolute/stable/bin/node --claude-sdk-path /absolute/stable/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs` (add `--state-root` for a nondefault pairing). Omit the dependency flags to discover the current global installation. The command shows old and new paths and versions; `--yes` confirms without prompting, including with `--json` in Home Manager activation. Missing old versions show as unavailable.

On macOS, these checks accept user- or root-owned directories writable by the `admin` group (gid 80), including Homebrew's `bin` and `Cellar`; group-writable files, other writable groups and non-sticky world-writable directories remain unsafe. Linux retains its group-write refusal. The known Homebrew repositories `/opt/homebrew`, `/usr/local/Homebrew` and `/home/linuxbrew/.linuxbrew` qualify as installed package prefixes for executable and dependency pins only when both the prefix and `.git` have trusted ownership and permissions. Private pairing state (`--state-root`) must remain outside every repository, including these Homebrew prefixes. The configured workspace and other repository ancestors remain excluded. Unsafe dependency errors name the offending component and explain how to fix it.

Codex discovery accepts `login status` on stderr and labels a confirmed ChatGPT account without an email as **ChatGPT login**. An explicit `--account-context` still requires a matching reported identity. If a shell guard around npm Codex cannot find Node on the service PATH, discovery retries that same guard with a validated, pinned Node interpreter; it preserves the guard and the workspace/permission checks. Discovery uses the account RPC and does not read vendor credential files.

Repin records local `claude-repin-<id>.json` and `claude-repin-<id>-applied.json` events in private pairing state. The running daemon replaces its Claude adapter after its current Claude processes exit. Historical ownership records and pending accounting remain intact and continue reconciling; they do not prevent adapter replacement. A successful `repinned` result requires the daemon's acknowledgement. After 20 seconds without it, the command returns nonzero with `restart remains pending`; the daemon retries automatically, and a daemon starting with the saved pins acknowledges them directly. An offline daemon must be started through its existing service owner. Repin preserves enrollment, credentials, fences and service definitions; it neither re-pairs nor takes ownership of a Nix/Home Manager service.

A pending or failed repin, or a broken Claude dependency, holds only fresh Claude work. Other harnesses and claim recovery keep polling. Local status keeps `ready: true` when any enrolled, unfenced account is ready; setup and Add harness can report connected as soon as one harness passes its probe, while another still shows starting or needs sign-in.

One status model covers the computer: it is ready while at least one harness can work. A failed start, a hold or a pin block is a detail of that harness or account, never `setup_failed` for the whole computer; a computer that only reports starting harnesses stays provisioning.

`harness_statuses` remains the legacy state map; the additive `harness_details` map carries `{state, reason, fix}`, and the daemon's per-account `blocked_accounts` carry `{account_id, harness, reason, fix}`. Both use one reason vocabulary and one fix form, `fix: {kind, command}` with kinds `repin`, `add_harness`, `login` and `restart`. Reasons are `repin_pending`, `dependency_invalid`, `pin_missing`, `pin_partial`, `pin_drifted`, `pin_invalid`, `pin_unsafe`, `login_required`, `starting`, `harness_failed`, `cli_unavailable` and `profile_permissions`: the `pin_*` codes come from the static pin check before launch, `dependency_invalid` from the runtime check of the same dependencies. Claude pin and dependency problems are fixed with `aeon-agentd repin --harness claude`, the same problems on other harnesses with `aeon-agentd add-harness --harness NAME`, a failed start, unavailable approved executable or pi profile permissions by restoring it and resuming with `aeon-agentd setup`. `/agents` shows the command on the affected harness row; a pending repin says “Waiting for repin” and retries automatically without a command. A ready harness may list `attention_accounts` for a proper subset of its enrollments so a healthy sibling cannot erase a block, and `/agents` shows “1 of 2 accounts needs attention” (need when more than one) from `attention_count` plus the derived fix on that row while each named enrollment keeps its own status. An account omitted from a truncated list is not shown as ready. `aeon-agentd status` prints the same reason and command per harness and per blocked account. Raw `harness_errors` stay local.

Unknown reason or state tokens from newer daemons remain visible as “Needs attention” with the raw code, without a guessed command; a newer state appears only in `harness_details`, never in the closed legacy map. Unenrolled, revoked, malformed-code and mismatched harness reports are dropped without interrupting fence sync or cleanup; one value-free warning is logged per server instance. Advisory detail extensions and legacy string fixes are accepted, and commands are always rebuilt from known reasons. Malformed JSON shapes still fail validation. Missing reports stay absent, offline reports are labelled as last reported, and a legacy daemon clears prior reports when it omits them. The CLI retains its existing approved physical-executable policy, including Homebrew installations; the stricter ownership and directory checks apply to Node and SDK pins.

Invoking the binary as `paimos` gives the paimos-compatible CLI. `PAIMOS_URL` (with `PAIMOS_API_KEY` or `PAIMOS_API_KEY_FILE`) is the process-only target.

### Ticket worker scopes

The ticket-worker set is `nodes.read`, `nodes.write`, `comments.read`, `comments.write`, `search.read`, and `events.read`. Propose it with this public code:

```text
aeon-scopes:v1:comments.read+write,events.read,nodes.read+write,search.read:5a072a8d
```

Paste a code into **New key**, **Change scopes** (the Edit scopes dialog), or **Rotate**. It replaces the selection, leaves unknown or ungrantable scopes unticked, and explains each skipped scope. Review before confirming; a code is not a credential and grants nothing. Pasting never extends an agent's role. Rotation keeps the old scopes unless you explicitly select a different set, and saves the replacement and old-key revocation together. Every rotation rechecks the actor's permissions and the agent's existing role ceiling, even when preserving scopes. Creation, editing and rotation use the same role ceiling: workspace-role permissions plus project-grantable and self-service permissions (`kinds.read`, `profile.read`, `profile.write`, `authz.read`) held through project roles, intersected with the agent-grantable registry. A scope from a project role still requires that project's binding on each request; it grants no workspace access. Rotation never creates or extends a role, restores a removed workspace binding, or adds default permissions; project-only agents keep their existing project access.

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

## Git-backed doctrine (AEON-318)

`GET /api/rules/doctrine` accepts people and agent keys with `rules.read`.
Source management remains person-only (`settings.manage`). GitHub reads use
only `https://api.github.com`; redirects are refused, including repository
renames, so configure the current repository name.

The operator provisions private read-only tokens under the absolute directory
`AEON_DOCTRINE_CREDENTIALS_DIR`. A reference such as `doctrine-private-read`
requires both the token file of that name and `doctrine-private-read.allowlist.json`:

```json
{"grants":[{"tenant_id":"<workspace UUID>","repository":"owner/repository"}]}
```

Each grant authorizes exactly one tenant/repository pair (names are compared
case-insensitively); repeat pairs to share a credential deliberately. The
server reads this policy before resolving a pin, fetching an index, or serving
cached doctrine. Missing, invalid, empty or nonmatching policies fail closed
with `credential unavailable`. Existing token files need this policy before
use. Removing a grant immediately prevents subsequent requests from reading
its cached doctrine or fetching again. Aeon has no API to write these grants:
the operator owns the directory and files, and the service has read access
only. Tenant owners may name references but cannot authorize them. Never put
token values in tenant configuration, API requests, logs or this repository.

## Agent rules (ADR-004, AEON-248 / AEON-249)

The dedicated `/api/rules` API stores layers, sets, rules and immutable version
snapshots as nodes. Generic node/event APIs cannot read or mutate these resources.
Single, batch and restored rule publications reject credential-shaped text in
snapshot fields and publication notes with a generic `422 credential_text`.
The detector is shared with knowledge learnings, whose existing explicit
false-positive confirmation policy is unchanged.
Git-backed doctrine rules (AEON-319) have **Propose change** when the server's
GitHub App is enabled for the workspace. The editor creates a proposal branch
and PR in the rule's owning public/private repo, changing its source and git
TL;DR sidecar together. Public proposals run the `inspr-modules` leak patterns
against only the changed rule, its TL;DR entry and PR explanation before any
GitHub call or token mint. Both public and private proposals also use the shared
credential detector (provider/cloud keys, private keys, password assignments,
Markdown labels, authorization values and URLs with passwords). It checks the
edited fields and complete outgoing blobs and paths before GitHub receives
anything; refusals never echo matched values. Raw and case-preserving Unicode
compatibility checks retain token shapes alongside the existing skeleton check.
Comparison removes every Unicode default-ignorable
character, applies NFKD, drops combining marks, folds case and uses a vendored
Unicode 17.0.0 UTS #39 skeleton. Named Latin small capitals are generated from
UnicodeData as well; ambiguous compatibility/visual forms fail closed. The
private corpus, public text and identity literals use the same normalizer;
proposed git bytes stay unchanged. Six-word runs, whole entries of five or more
words, and substantial four-word shingle overlap are refused.
Public proposals require a successfully indexed private source with a valid
credential grant. The quotation guard is an HMAC keyed by
`AEON_DOCTRINE_GUARD_KEY_FILE` (at least 32 characters; dev without the file uses
an ephemeral key). Its saved metadata binds the normalization code/table and
binary policy versions. Startup rebuilds missing, old or rotated guards;
public proposals fail closed until a compatible guard exists. An unset production
key or a configured file that does not exist disables PR proposal creation without
blocking startup or doctrine reads/indexing. Workspace administrators see the
reason in the Doctrine panel; malformed or unreadable keys remain configuration
errors. No key or host path is returned in that reason.

Every private-tree file must be UTF-8 text, without control codes other than
tab, LF and CR. No binary extension, signature or readable-text heuristic skips
files. A non-text file blocks the guard and the index error names only its path.
The optional host config `AEON_DOCTRINE_BINARY_ALLOWLIST` is an explicit reviewed
JSON object of exact repository-relative paths to lowercase SHA-256 digests of
complete blobs (empty by default). It cannot be set through proposals or source
configuration. Changing or removing an exception invalidates cached guards.

Public source indexing separately caches the resolved main commit and tree.
Only matching cached blobs can exempt a private match, for at most five minutes.
A missing, invalid or stale cache returns `422 public_main_unavailable` without
fetching; reindex the public source to refresh it. A proposal with no private
match needs no exemption cache and adds no guard GitHub reads. Normal PR
preparation still verifies main; if it changed since an exemption was checked,
reindex and retry. A proposal branch or unchecked pin never grants an exception.
Public letters must be Latin, including German umlauts and ß; the compatibility
form admits superscripts, fractions and the information symbol while other
scripts/digits stay refused. Unchanged file content is excluded.

Regenerate the pinned Unicode table with
`python3 internal/unicodeguard/unicodegen/generate.py`, then `gofmt` the output.
The generator verifies immutable input digests; builds and runtime are offline.
Credential-shaped text is refused for either repository, including private.
The shared publication detector checks raw text, compatibility forms without
format controls, and case-preserving confusable forms from the same pinned table.
Rule snapshots (single, batch and restore) use these checks too. Credential
range offsets for knowledge confirmation still refer to the original text.
Git stays authoritative; the database holds request digests, PR references and
audit metadata, never draft prose or credentials. Keep the same request UUID
and input when retrying a lost response. Short transactions authorize and reserve
a bounded proposal lease, then apply GitHub results with a version compare-and-set;
no database locks span network calls. Refresh reconciles an uncertain merge and
emits an event only when state changes. Externally observed merges are recorded
as observations; without prior Aeon approval, request release in the repository.

Host provisioning (no App exists yet): set `AEON_DOCTRINE_APP_ID`,
`AEON_DOCTRINE_INSTALLATION_ID`, `AEON_DOCTRINE_APP_KEY_REF`,
`AEON_DOCTRINE_APP_TENANT_ID` and `AEON_DOCTRINE_GATE_LOGIN`. An operator must
also set `AEON_DOCTRINE_DCO_ACKNOWLEDGED=true` after approving the App's
contribution/sign-off policy; without it proposals stay disabled. The App's
bot login and public noreply identity are resolved from GitHub, and commits
carry that bot's matching DCO trailer (required by the public repo). No
person's identity or address is copied into public commit metadata. The key reference
names a PEM RSA key under `AEON_DOCTRINE_CREDENTIALS_DIR`, read only at call time.
The operator must also provision `<key-ref>.allowlist.json` using the same
`grants` format as read credentials above, with an explicit tenant/repository
pair for each writable doctrine repo. Missing, invalid or revoked grants return
`credential unavailable` before key use or any GitHub request. Proposal,
refresh and approval operations recheck the grant. App configuration and tenant
settings cannot grant access to a key by themselves. Every GitHub request,
including PR writes, is pinned to `https://api.github.com` and refuses redirects.
The App installation must select exactly `inspr-at/inspr-modules` and
`inspr-at/inspr-doctrine-private`, with contents + pull requests write (and
implicit metadata read). Each minted token is narrowed to the proposal
repository alone and checked against that exact scope and live repository
visibility. Installation tokens are revoked after use (including validation
failures), with bounded cancellation-independent cleanup via GitHub’s
[revocation endpoint](https://docs.github.com/en/rest/apps/installations#revoke-an-installation-access-token).
A private repo becoming public blocks publication. The configured tenant is the
sole proposal writer; tenant settings
cannot grant another workspace access to the central doctrine. The App must
**not bypass branch protections**. Main must require CI and the independent
cross-family gate so both remain enforced during a merge race. No direct-main
write route exists.

The independent repository gate account named by `AEON_DOCTRINE_GATE_LOGIN`
must post an APPROVED PR review on the exact head, with its complete body:
`aeon-doctrine-gate: {"verdict":"ok","head_sha":"<head>","checks_green":true,"author_family":"openai","reviewer_family":"anthropic"}`.
It must independently verify required CI and the explicit cross-family review;
Aeon cannot supply that evidence itself. Families must differ. Missing, stale,
dismissed, same-family or negative evidence blocks approval. A person with
workspace `rules.publish` approves that head; Aeon checks protected-main
mergeability again and supplies the SHA to GitHub's merge endpoint. Branch
protections remain the race-safe authority. This uses PR reviews because
GitHub's checks/status APIs require extra App permissions beyond this ticket's
scope ([GitHub permissions](https://docs.github.com/en/rest/commits/statuses#get-the-combined-status-for-a-specific-reference)).

After a confirmed merge, Aeon sends `repository_dispatch` type
`doctrine-release` with `proposal_id`, `merge_commit` and
`requested_scheme: CalVer3`. The owning repo must install a receiver that
deduplicates on proposal ID, performs its approved reservation/release flow,
and publishes a release body line:
`aeon-doctrine-release: {"proposal_id":"<id>","merge_commit":"<sha>","version_scheme":"CalVer3"}`.
Aeon never reserves versions, changes consumer pins, or labels dispatch as a
release. Refresh verifies release provenance and that its tag resolves to the
merge or a descendant. Until the App, independent gate and release receiver
are provisioned by the coordinator, the workflow remains disabled/pending.
No foreign repository workflow was changed for this package.

The state reads proposed → in review → merged → released. A person with
`settings.manage` can report a verified machine pin through
`POST /api/rules/doctrine/proposals/{id}/pins` using a SHA-256 machine identity
and the observed release commit. The UI labels these as **reported** machines;
this is operator evidence, not automatic fleet discovery, and never changes
nixcfg, PHAROS or JANUS pins. New observations replace that machine's old pin
for the repository. Review, merge, release and pin evidence is tenant-isolated.

`rules.read` and `rules.write` are agent-grantable; `rules.publish` is high risk and
person-only. Company edits require a person with workspace publish authority;
project edits require that authority in the target project (or in an agent key's
creator). Person and named-agent layers require their exact owner. A person's
access to a named agent requires an active unexpired key they created. No grants
are created by this module.

Start with `POST /api/rules/layers` (a `RuleScope`), then
`POST /api/rules/sets` (`layer_id`, `name`). Replace the whole mutable draft with
`PUT /api/rules/sets/{setId}/draft` (`expected_revision`, `name`, `rules`). Rules
have a stable `identity`, bounded one-line `text` and `why`, optional on-demand
`details`, explicit `enabled`, `normal|locked` strength, source lineage, and
optional role/harness selectors and expiry. Locked rules must be enabled and
non-expiring; locked company rules are unconditional. Removing or changing one
requires a new publication by the authorized person. Nothing here changes the
company's current effective rules automatically.

Publish with `POST /api/rules/sets/{setId}/publish` and an explicit reserved
`YYMMDDhhmmss.0.0` version plus `expected_revision`. The same version and identical
snapshot replay; changed bytes require a later version. History is available at
`.../versions` and `.../versions/{version}`. Restore via `POST .../restore` with
`version`, `new_version` and `expected_revision`; this creates a new publication.
Every mutation appends a `rules.*` event in its tenant transaction. Set/version
reads include details; the merged response excludes details.

`GET /api/rules/merged` requires exact `project_id`, `person_id`, `role` and
`harness`; agents also supply their own `agent_id`; `task_id` is optional. Tenant
comes from authentication, and person must be the caller or its key creator.
Role/harness are explicit selection context, not permission grants or proof of
which process executed the result. Precedence is company, project, person, agent
role, named agent, task. The highest matching identity wins; identical-rank
ambiguity fails closed. No natural-language conflict guesses are made. Expiry is
checked on every request. The complete rendered body must fit the workspace
budget (12,000 UTF-8 bytes by default); publication over that budget returns 422.
Admins can set 2,000–500,000 bytes in the rules budget editor, with optional
layer caps from 500 bytes to the total. Older clients never block a save;
out-of-range totals return the stable `400 invalid_budget` error. Lowering a
budget below published rules still fails with 422. The editor estimates tokens
at full budget (bytes ÷ 4), adds nonblocking guidance above 64 KB and 128 KB,
and offers a collapsible English/German Tip for keeping the kernel small.
Budget fields and save/cancel controls precede expandable guidance, so warnings,
validation messages and client details grow downward without moving them.
Use ⌘↵ on macOS or Ctrl+↵ elsewhere to save from a field; Escape leaves the
field first, then cancels editing on the next press. Browser shortcuts stay native.

CLI and agentd send optional `max_session_file_bytes` and `rules_client_version`
on registration and every heartbeat. The transport ceiling is 512,000 bytes,
separate from the 500,000-byte workspace budget. General Codex reports remain
32,768 bytes, its default combined project-instruction limit; other harnesses
report 512,000. Fresh Aeon app-server launches supplied with rules set
`project_doc_max_bytes` to the greater of the delivered byte size and 32,768,
preserving the default allowance for the repository's `AGENTS.md` chain.
The delivered rules go separately into ephemeral developer instructions,
without changing account config or repository files. Manually launched Codex
sessions keep their honest default report. See the
[official configuration reference](https://developers.openai.com/codex/config-reference/).
Omission resets support to the legacy 12,000-byte limit after a downgrade.
These are request-only fields; PHAROS/JANUS session responses stay unchanged.

Rules requests report `X-Aeon-Max-Session-File-Bytes` (2,000–512,000; omission
means 12,000). Managed delivery also respects its registered capability.
Delivery fits within the lesser of the workspace budget and client limit,
retaining all locked rules before adding whole normal rules in this fixed
priority: company, project, person, agent role, named agent, task; identity
ascending within each priority. A compatibility note names any cut. Legacy
cuts also fit the old 512-KiB cache envelope, reserving 16 KiB for the wrapper.
The body digest, receipt and served manifest describe the actual cut. If
locked rules plus the note cannot fit, delivery fails closed with an upgrade
message; it never drops a locked rule. Whole rules may leave unused bytes.
The admin editor shows each recent client's delivery allowance
`min(budget, reported limit)` and marks smaller allowances as truncated. The
historical `blocking_clients` field now carries this inventory, grouped by
host/harness/version/limit over seven days, bounded to 50 plus a remainder
count, including stopped/archived generations. Readers without workspace
`settings.manage` never receive tenant-wide host/version details. The product
ceiling is always 500,000, including an empty inventory.
Upgraded CLI, managed delivery, Claude bridge and caches accept 512,000-byte
files. The cache envelope remains bounded at 32 MiB; publication store caps
remain 2,000 rules and 2 MiB. Roll out migration 1042 and the server before
upgraded reporting clients.
An applicable published locked company floor is required.

`paimos session start --rules-preview` is an opt-in JSON preview. Use `--project`
with its UUID, plus `--agent`, `--rules-tenant`, `--rules-person`, `--rules-agent`
(for agent callers), `--rules-role` and `--rules-harness`. Supply an explicit
`--rules-cache path.json`, a separately retained `--rules-floor floor.txt`, and
its trusted `--rules-floor-sha256`. These files require mode 0600 and physical
paths in an existing directory outside credential/harness stores. A reviewed
merged response's `floor` is the source for that retained file; provisioning and
trusting its digest is an operator step. Fetching never changes that pin.

`--rules-out new-preview.txt` creates a new file atomically and refuses overwrite.
No active `AGENTS.md` or `CLAUDE.md` is installed or replaced. The reusable Go
`rules.Stub` function provides AR6 a bounded stub preview with the verified floor.
On network unavailability (including HTTP 502/503/504), the exact instance/context
cache is marked stale. Wrong context, corruption, or any crossed expiry boundary
retains only the independent floor and reports the gap. HTTP authorization and
semantic failures never use the cache. Digests detect local corruption; the cache
is not a signed authority against a local user able to replace both cache and pin.

Preview output includes the exact body hash/version/size as
`proposed_received_payload`. It does not install an instruction file or update
an existing session's provenance; `provenance_recorded` and
`execution_verified` remain false. Preview never submits a receipt.

`paimos session start --rules-receive` is a separate, explicit receiving mode.
Use the same exact rules selectors, cache and independently pinned floor as
preview, plus `--session` with an **already registered public generation UUID**,
`--worker-lease-file`, a lowercase `--rules-request-id UUID`, an explicit
`--rules-expected-revision N` (receipt revision, initially `0`), a new
`--rules-state attempt.json` and a new `--rules-out received.txt`. Register first
with the existing `paimos harness register` flow; receiving never mints or switches
identities. Online receiving also uses the existing `account.manage` (`/api/me`),
`rules.read`, `harness.read` and `harness.worker` permissions; it grants none.
The lease stays in the trusted client and is never printed or saved in state.

Receiving writes the renderer's exact bytes as a private `.txt`, then explicitly
POSTs only context/hash/version/size/source and request/CAS metadata to the distinct
receipt endpoint. It never modifies active instructions or instruction provenance,
and proves neither model loading, execution, obedience nor authority. Online
floor changes (including additions), expiry, context/hash mismatches and HTTP
401/403/404 fail closed. Only network unavailability permits the existing exact
cache/independent-floor fallback: JSON reports `source: cache` or `floor-only`,
`stale: true`, a `gap`, `receipt_recorded: false`, and `complete: false`. This local
fallback exits successfully to make the retained bytes available; it is not a
completed online receipt and cannot be replayed as one later.

The immutable private checkpoint is saved before output/POST. After a partial
failure or lost response, rerun **the identical command with `--rules-retry`**.
Retry verifies the checkpoint, original context/instance/session/request/revision,
current online read authorization and floor, original expiry, and exact existing
output. It can create a missing output but never replace an existing one. It
resubmits the original online metadata; the server returns the original receipt
for an identical request ID, even if newer receipts/publications exist. No queue,
background replay, automatic CAS adjustment or registration is performed.
Keep the checkpoint and output until the result is resolved; local digests detect
corruption and are not signatures against an attacker able to rewrite local state.

The API verifies the lease only at POST, so rejection can leave bytes on disk.
Receipt failure exits nonzero with `receipt_status: rejected` (HTTP 4xx) or
`unconfirmed` (lost/invalid reply), `receipt_recorded: false`, and `complete: false`.
An unconfirmed result may already exist on the server. A successful explicit retry
confirms it without rewriting output. A 409 with a genuinely competing request,
changed floor, expired original rules, revoked access, or stopped/archived generation
requires operator inspection of receipt history; this CLI never changes the saved
CAS or switches generations to make it pass. A new attempt requires fresh request
ID/state/output and the explicitly inspected current receipt revision. Provisioning
a reviewed floor and registering the public generation remain operator prerequisites.

An explicit `POST /api/projects/{projectId}/harness-sessions/{sessionId}/rules-receipts`
records a separate worker report; `GET` on that path reads its append-only history.
Writes require `harness.worker`, the owning session agent and exact generation
lease. Reads use `harness.read` and project visibility. Supply a UUID `request_id`,
`expected_revision` (zero initially; otherwise the latest receipt revision),
`context` (tenant/project/person/agent/role/harness and optional task UUID),
`body_sha256`, calendar `version`, `byte_size` (1..12000), and `source`
(`online`, `cache`, or `floor-only`; the latter requires version `floor-only`).
Use the exact metadata of bytes the caller reports receiving. No bodies or paths
are accepted. The server checks tenant/project/agent, canonical key creator,
registered harness and optional current ticket binding. Role, digest, version
and source remain worker-reported; publication, model load and execution are
unverified, and no authority is granted. This does not establish that the reported
bytes came from a published merge. Existing instruction provenance stays intact.

Identical request-ID/normalized-metadata retries return the original receipt even
after later appends; divergent retries and competing stale revisions return 409.
A new request ID with the current revision appends even for identical bytes. Current
authorization, context binding and generation checks still precede replay.
Stopped/archived generations and expired/revoked credentials cannot record.
Harness generation leases have no time-based expiry; heartbeat age is not proof
of closure. History remains readable after closure, newest 32 first, with
`before_revision` pagination. Recording requires a live API connection; cache
and floor-only reports remain stale fallback evidence, never fresh authority.
There is no automatic submission or offline queue; the receiving mode above is explicit.
Migration 0897 uses a distinct immutable table because event visibility can hide
harness history from project readers; the audit event is transactional, not the
CAS source of truth. No company rules are seeded or published.

Stub installation, automatic registration,
template import/export and the rules editor are separate coordinator-owned work.
`aeon rules compare` is the one-time comparison of loaded harness files with the merged rules.

`aeon rules-compare` (the same verb on `paimos`) is a one-time offline check.
Pass explicit instruction files with `--file` and `--context`. Optional
`--merged` supplies AR1 merge metadata; `--provenance` supplies AEON-219
revision or page JSON and needs `--session` to bind an expected session.
Both JSON files are read as private `.json` files. The report compares exact
raw file hashes and supplied merge lineage. Canonical, session-bound provenance
is labelled supplied worker-reported metadata; bare arrays and preview
`provenance_items` remain unverified comparisons. Offline JSON cannot prove
API observation, snapshot publication, a trusted floor, runtime execution,
model load or obedience. Expired or malformed merge metadata remains useful
for historical differences only. Rollout stays unauthorized; the command
never waits or replaces `AGENTS.md` or `CLAUDE.md`.

`aeon rules compare` reads the `CLAUDE.md` or `AGENTS.md` chain one harness
loads for `--repo` and diffs it against `GET /api/rules/merged` for that
project, person, role and harness. `--harness` is `claude`, `claude-code` or
`codex`. It runs once, with no waiting period. The report lists statuses,
identities and hashes. `--upload` stores that summary for the project.
Instruction text is not uploaded. The command does not replace `AGENTS.md`
or `CLAUDE.md`, and rollout stays unauthorized.

`aeon doctor` also checks the two rule delivery channels. It discovers command
hooks marked `# aeon-rules-hook-v1` in the user and current project settings,
reads their literal `--rules-out` targets, and compares each harness file with
the pinned doctrine index. Inbox hooks alone do not establish rules delivery.
For a manually delivered file or a hook with a dynamic output path, use
`aeon doctor --rules-harness claude|codex --rules-out /absolute/received.txt`.
This explicitly checks that file and its harness file. Doctor never executes
hooks or expands shell expressions. Missing, empty, unreadable or unverified
files and unready/failed doctrine sources cannot earn “no rule served twice”.
Deleted rules are detected even when their markers were deleted too.
Doctor normalizes whitespace and follows literal Markdown `@path` imports,
relative to each importing file (`~/` resolves to the home directory). Reads
stay within the home and harness directories, refuse secret paths and symlinks,
and stop at 8 import levels, 64 files, 256 KiB per file or 1 MiB total. Unresolved
imports report “unverified” instead of drift; neither result certifies delivery.

Session merges resolve precedence before omitting doctrine copies. A locked
company rule delivered by doctrine retains its floor obligation as a reference
to the doctrine identity and immutable commit, without repeating its text in
the session file, cache or bootstrap. Existing independently retained floor
pins still require explicit review when changing from text to that reference.
Catalog reads for publication, delivery and channel reports recheck the same
host-provisioned credential grants as the doctrine API; an inaccessible source
fails the operation with `503 doctrine_unavailable` before cached text or
matching identities are inspected, including managed-session delivery. A batch
loads the catalog once for its request; later requests recheck the grants.
Channel reports include duplicates only from sets whose exact scope the caller
may read, including the project permission and scoped ownership checks.

## Web workspace

The Vue shell includes an authenticated workspace, sign-in, a 404, an account
menu, and light/dark themes. The theme follows the operating system until the
user toggles it; that choice lasts for the current page session and writes no
browser storage. Assets and fonts are served locally. The supplied mark is
preserved at `web/src/assets/brand/aeon-mark.svg` for its later replacement.

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
# Prepared mbp2606 entry point (refuses until OPS-247 bootstrap is approved):
AEON_REMOTE_CONTROL_DIR=/path/to/coordinator/aeon npm run test:remote
# Locally, only one targeted file, one worker, when there is a technical reason:
npm test -- tests/authz.spec.ts --workers=1
```

`just ui-remote` is the same remote entry point from the repository root. It
currently refuses with exit 3 before SSH or dependency installation: OPS-247
owns the approved browser bootstrap and shared heavy-job launcher. Use hosted
draft PR CI while that work is pending. No environment flag enables the lane;
the coordinator must confirm the launcher contract and review a follow-up change
to enable it. No browsers or Playwright were installed on mbp2606 for AEON-508.

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

### Pairing readiness and diagnostics

`aeon-agentd status` reads the last atomic setup snapshot and live daemon status
without taking `setup.lock`, reconciling enrollment, or performing cleanup.
Use `pair`/`setup` to resume setup and `disconnect` to resume cleanup. Approved
but unbound harnesses name the required `add-harness` command. Account checks
report a 60-second bound from daemon start for each initial account, or from
that account being added or unblocked by repin; refreshing unchanged accounts
does not extend the wait. Capacity capture reports a 10-second bound.
Unsupported or incomplete verification fails with `verification_unavailable`
and a bounded cause, independently of account probing. Connect-only approval
creates no verification, and a later approval cancels older queued verification
on that same computer without interrupting claimed work.

New pairing-owned macOS launchd services write diagnostics to
`~/Library/Logs/aeon-agentd/{stdout,stderr}.log` in a private `0700` directory.
Existing receipt-bound service definitions remain owned and unchanged; logging
is added when a new service is installed. Managed Nix/Home Manager services
retain their configuration ownership.

The paired daemon writes bounded `agentd polling diagnostic` lines to stderr
when the set of causes changes, then at most one reminder per cause every
15 minutes while the set persists. A healthy poll clears the set, so a recurring
cause is logged immediately. Multiple accounts sharing a cause produce one line:
`reason=probe_failed` records an account readiness failure (a failed probe/report
or a retained ownership/reporting block);
`reason=probe_timeout` confirms an account exhausted its own pending probe wait;
`reason=queue_unavailable` confirms `Queued()` returned an error before probing;
`reason=dispatch_not_allowed` confirms a dispatch fence, fence-read failure or
daemon shutdown prevented polling or an account probe. Correlate these lines
with read-only `aeon-agentd status --json` for the affected account. Queue errors
can therefore explain a subsequent probe timeout; the timeout alone does not
identify the underlying cause. Raw errors and private bindings are not logged.

### Paired daemon socket paths

Paired mode uses `<setup-root>/daemon/agentd.sock`. If that exceeds the
platform's socket path budget (100 bytes on macOS, 104 on Linux, allowing for
`sun_path` overhead), it uses `~/.aeon/run/<state-hash>.sock`, with 16 hex
characters identifying the daemon state directory. Both fallback directories
are owned by the user and mode 0700; symlinks and unsafe existing directories
are rejected. The selected socket is recorded privately in `daemon/control.json`.
Attach, setup/status and capacity clients read that reference; local control
can do the same with `control --setup-root PATH` (exclusive with `--socket`).
Existing generation-specific socket references remain readable while their
listener exists. No pairing data migration is required.

`HOME` is needed only for the fallback, so short and existing legacy socket
paths still resolve when it is unset. If the service and shell resolve different
fallbacks, the client reports its resolved home, setup root and socket alongside
the recorded socket; paths under the client's home are shortened to `~`.

`pair`, `setup` and paired `serve` reject an unrepresentable path before
creating pairing state or contacting the instance. Choose a shorter setup
root (`--state-root` for pair/setup, `--setup-root` for serve). Every local
listener takes an exclusive non-blocking lock on `<socket>.lock` before touching
the socket or token and retains it through shutdown cleanup. The lock file is
an owned mode-0600 regular file opened without following symlinks; it is retained
across restarts and is never deleted or renamed by startup, recovery or shutdown.
After acquiring the lock, startup compares the descriptor's device and inode
with the file named relative to the pinned private directory handle and retries
a bounded number of times if they differ.
Interrupted flock syscalls retry; only contention maps a flock error to busy.
The descriptor is close-on-exec, so harness children cannot keep the lock alive.
On macOS, a short directory flock serializes only the lock-file open: concurrent
`O_CREAT|O_NOFOLLOW` opens can otherwise return `ENOENT` during creation. It is
released before taking the lifetime file lock; open errors remain errors.
A second start reports "agentd is already running for this state
root" and leaves the active listener and token untouched, even when its accept
queue is full. Clients never take the lock.

The kernel releases the lock after a crash, including SIGKILL. The next owner
validates all stale socket artifacts through the private directory handle before
removing any: the socket, token (even without a socket), obsolete owner record,
and legacy `.s` plus eight hex digit quarantine names. Each must be owned by the
current uid, mode 0600 and single-linked, with the expected socket or regular-file
type. Symlinks, hardlinks, foreign owners and unsafe modes refuse startup.
The verified lifetime lock is the only cleanup authority. Recovery and shutdown
never probe the socket: on macOS even a live listener can refuse connections
when its accept queue is full. All cleanup checks and removals are relative to
the same pinned private directory handle. Lock identity is checked again before
each removal. Shutdown closes the listener and removes only its recorded
socket/token inodes while still holding that lock, then releases it. Losing lock
identity leaves residue for a verified owner to recover. Interruption likewise
leaves stale artifacts for the next owner; no quarantine is needed.
This advisory protocol serializes cooperating daemons. A same-uid process that
deletes or replaces the lock file or directory can disrupt a running daemon;
this is outside the protection boundary, since it can already signal or kill
the daemon. The no-symlink, owner, mode and link checks protect against accidental
or foreign-uid artifacts, not hostile same-uid path mutation.
Unrelated directory entries are preserved.
The private token and local control authorization rules remain unchanged.

### Harness interpreter pins

Guided setup pins Node's physical path and version privately for npm-launched
Codex, Cursor, Claude and pi. `--node-path` selects an installed Node outside the
workspace, including for shell wrappers. Setup checks the launcher using the
service PATH (`/usr/bin:/bin:/usr/sbin:/sbin`) with the pinned Node directory first;
interactive shell paths and Node injection variables cannot mask missing runtime
dependencies. The same pin is used for account probes and run launches. A missing,
partial, drifted, invalid, or unsafe pin blocks only that account: the paired
daemon and its other accounts keep running, and that harness does not launch
until the pin is fixed. Status names the account, a reason code (`pin_missing`,
`pin_partial`, `pin_drifted`, `pin_invalid`, `pin_unsafe`), and the fix as the
same `{kind, command}` object used by harness details. Claude launches with the
shared Node/SDK pins, so every Claude pin problem, a missing pin included, is
fixed with `aeon-agentd repin --harness claude`. For Codex, Cursor and pi,
`aeon-agentd add-harness --harness NAME` renews only the blocked pin of the
already connected account: the signed-in identity and launcher must match, no
request or approval is created, and a healthy pin is never replaced. If the
identity or launcher changed, remove the enrollment and add the harness again.
Repin and pin renewal never renew authority. Native launchers and saved pi
bindings remain supported. Paths and interpreter versions stay local.

### Pi guided setup

`paimos-agentd setup --harness pi` and `add-harness --harness pi` use the same
person-approved pairing flow as the other harnesses. Install pi normally and use
its `/login` and `/model` commands first. Setup pins the executable and reads its
version. For npm's `#!/usr/bin/env node` entrypoint it also resolves Node (or
uses `--node-path`), pins its physical path and version privately, and prepends
its directory to the probe and runtime PATH. Node must be installed outside the
workspace. A missing or changed interpreter pin blocks only that account.
Setup then checks
the selected provider/model against pi's public RPC
`get_available_models` response. `--account-context anthropic` selects a specific
configured provider instead. For existing native sign-ins the local profile is `~/.pi/agent`; Aeon never
opens its credential files, inherits provider keys for this check, or sends a
prompt. This establishes configured authentication, not remote credential
validity or a person's identity. The approval label names the provider and local
profile; the server selects an enabled pi model profile for that provider, and
only the computer keeps the profile path. The profile directory must be private
(no group or other access), as required by the daemon. Managed installations stay
with their owning Nix/Home Manager configuration.

The paired daemon rechecks that provider at most once a minute during polling
and afresh before each run, and refuses a model profile from a different
provider. Probe startup failures report a harness startup problem; only missing
provider configuration requests vendor login. Pi verification is explicitly
unavailable: its managed adapter has no qualified no-tools boundary. Connect without verification, then
set request limits for ongoing work. Pi retains its existing SVG harness icon and
account allowance pipeline; missing vendor token, cost or capacity readings
remain unreported, never inferred from a provider name or a successful probe.

For OpenRouter, run `aeon-agentd add-harness --harness pi --provider openrouter`
on an already paired computer (or append `--harness pi --provider openrouter`
to `aeon-agentd pair` for a new one). The hidden terminal prompt checks the key
with `GET https://openrouter.ai/api/v1/key`, without spending tokens, and stores
it in a new 0700 per-account pi profile with a 0600 `auth.json`. Headless setup
accepts `--openrouter-env-file /absolute/private/path`: only a literal
`OPENROUTER_API_KEY` assignment, never shell commands. No key flag, inherited
provider key, keychain dump, server upload, or key in child arguments/environment.
Normal native provider setup also accepts `--provider PROVIDER`.

Settings → Accounts lets a person with `account.manage` choose a pi model;
OpenRouter uses `vendor/model[:variant]`. The server checks its public models
list with a four-second timeout and a fifteen-minute cache (failed lookups cache
for thirty seconds). Unconfirmed slugs save as **unknown**. The initial
OpenRouter option is `stealth/space-bunny-alpha`; edits create immutable model
profiles for the account catalog/planning selector. Active runs keep their pin;
previously queued stale choices must be selected again. OpenRouter's underlying
model family is not assumed, so unknown families cannot qualify for review gates.

Stealth and free models show the provider data note. Local probes report only
numeric dollar usage, key limit and remaining credits with their reading time;
missing values stay unknown. Credit amounts are not a renewable allowance window:
normal request limits and the blind-provider pacing policy still apply. No
completion call is used to validate a key or a model.

### Session metadata

`scripts/session-metadata.py --codex-index` requires an explicit `session_index.jsonl`
and canonical source session UUID, and supplies names only. Linux and Darwin open
each path component relative to its parent descriptor without following symlinks;
Darwin's exact root `/var` and `/tmp` system aliases are supported. Custom symlinks,
private-store components and unsupported platforms are refused. The opened file
must be regular and at most 64 KiB; content reads remain bounded if it grows.
Fixture mode retains its stricter private-directory exclusions. Unmanaged capture
remains unavailable until the exact owning source is bound; live model and effort
capture remain unimplemented.

### Session service tiers (AEON-436)

`aeon agents tier show --project KEY --session UUID` shows Default / Fast / Fastest,
per-model adapter provenance, published price multipliers and pending decisions.
`set --tier fast` uses a person key with the existing project `harness.control`
permission. It queues a change bound to the current daemon/process generation;
the active tier changes only after the daemon acknowledges the native mechanism
at an idle boundary. Codex resumes its owned thread with `service_tier` for the
next run; Claude applies its `fastMode` setting for the next turn. A rejected or
uncertain native acknowledgement leaves the active tier unchanged. A session
running on its own and an ended session are read-only.

`ask --tier fast --reason "QA waits"` records the session agent's request without
exposing its daemon lease or switching the tier. Persons can use `approve` or
`decline --decision-request UUID --tier fast`; the tier API exposes requests for
the Decision Desk. Undo uses `set` with the previous tier: it cancels an unclaimed
change or queues a reversing change after confirmation. A claimed change must
settle before a reversing change can be queued.

The session-bound `/tier/report` endpoint carries the adapter's facts for each
model: price, speed (unknown where unpublished), mechanism, harness/adapter
version and checked-at. Offered tiers and all reported multipliers must match the
pinned vendor catalog; reports cannot supply their own price or usage factors.
The currently named Codex and Claude models have no cited tier price pins, so
Fast and Fastest are not offered and their multipliers remain null. A paid tier
requires a cited vendor price and mechanism before it can be enabled. Price and
subscription capacity consumption are distinct; the fragment's illustrative factors are not
pricing defaults. Vendor facts are pinned in `internal/servicetier`; account or
vendor rejection remains authoritative. Usage snapshots retain tier segments and
their multipliers, and run telemetry records the active tier. Earlier tokens are
never repriced when a tier changes; vendor billed costs remain vendor costs.
On Agents, the Tier column uses one small chevron per offered tier. Clicking the
glyph, or Enter/Space, opens Change tier; plain Left/Right and the hover minus/plus
step among offered tiers. The eight-second Undo toast cancels an unclaimed change
or reverses the confirmed change, and refuses an intervening revision or process
replacement. The session menu and Service tier block open the same picker;
phones use a full-height sheet with pinned actions. Agent requests can be approved
or declined in the session panel; Decision Desk integration remains AEON-536.
Unavailable tiers show their adapter reason, including “not offered: no published
price”. Unknown prices and model-time baselines remain explicit rather than
producing last-run estimates from the design fragment's sample numbers.

The tier API also returns `last_change`, including the completed control's outcome
and reason. A rejected, expired or superseded change reports an error and clears
its Undo receipt. Cancelling an unclaimed change is neutral: its successful Undo
receipt uses `tier_cancelled_pending`; a cancelled control uses `tier_cancelled`.
Neither shows a rejection alert. Other viewers following the change get a neutral
cancellation message. A real rejection can be dismissed for the current viewer;
starting the next change also retires it, and a later rejection still appears.
Overlapping tier reads share one result. Confirmation reads
back off from 1.2 seconds to 30 seconds and stop after two minutes; they stop
while the tab is hidden or Agents is closed. Live tier revisions and control
completion events still reconcile the result after that limit, and Check result
allows an explicit read. Heartbeats alone do not reload the tier panel.
Pending agent requests are carried by the session list row, so its “asks for”
hint does not depend on opening the panel. Host controls remain visible in the
compact layout alongside the tier control. The session list uses cards below
760 px container width, including a 1280 px viewport with the panel open;
1366 px and 1440 px panel-open viewports retain the table. Frozen run-cost labels,
tier history
and last-run comparisons are tracked in AEON-609 and must land before paid tiers
are offered.

Regression coverage includes native acknowledgement and Undo, pending replay,
tenant/project isolation, revoked authority, vendor cooldown and stored costs
across tier changes. Requests that leave the active tier unchanged also retain
their idempotency receipt; reusing their ID with a different body returns 409.

### Session recovery

Managed sandbox controls (AEON-260) use the additive
`POST /api/projects/{projectId}/harness-sessions/{sessionId}/managed-controls`
route. A person needs `harness.control`, the daemon must advertise
`managed_control_v1`, and the request carries a stable UUID `request_id`,
`kind` (`steer`, `interrupt`, `stop`) and exact `expected_ownership`.
A steer also carries at most 8192 UTF-8 bytes of `text`. Controls expire after
45 seconds, with at most 16 outstanding/recent requests per session.
The existing control GET returns durable metadata; yield claims at most once.
Steer text lives only in a bounded expiring memory relay, never in control rows,
events, heartbeat or the daemon journal. Server restart rejects lost text;
ambiguous delivery is reported as unconfirmed, never silently reinjected.
Identical request IDs replay their receipt; divergent retries are refused.
Pending authorization is rechecked when the daemon claims the control.
Yield includes `expires_in_ms`, a remaining lifetime calculated on the Postgres
clock and rounded down. The daemon deducts the full request round trip and keeps
one local monotonic deadline through queueing, adapter writes, setting waits and
the final force-stop signal lock. It rejects missing, zero or invalid lifetimes;
new daemons therefore require a server that supplies this additive field for
managed controls and force stop. `expires_at` remains audit metadata. Settings
completion uses the database clock, and transient steer retention uses a local
monotonic budget derived from it. Process start timestamps remain opaque identity
fields; the database observation stamp establishes ownership freshness.

The worker-only `POST .../managed-context` route returns the ADR-004 merge for
that session's tenant, project, key creator, agent and work order. Callers cannot
select another context. This is an execution-scoped read under the worker
lease, not a general rules permission or a repository file installation.

The qualifying adapter is currently the restricted Claude SDK bridge on macOS:
explicit daemon-owned MCP tools, no native Bash or file tools, strict MCP inventory
and a bounded sandbox terminal. Read/Edit/Write/Glob/Grep use descriptor-relative
`openat` with `O_NOFOLLOW` on every component; reads and writes validate regular
files with a single link using `fstat` before accessing contents. Search walks
opened directory descriptors. Symlinks (including internal ones), hard links,
special files and oversized searches fail closed. Write creates missing parent
directories relative to opened directory descriptors. The bridge receives only its pinned account HOME/config directory,
a tool-specific PATH and fixed locale, never the daemon environment. The terminal
allows exact CPU/page-size/memory/uname and ARM-feature sysctls for Go/Node;
process arguments and other-process inspection are explicitly denied (the macOS
default denial alone does not block numeric procargs queries). Self inspection
is allowed for the macOS runtime. The terminal qualifies toolchain libraries
through the Nix package closure or recursive macOS
`otool` inspection, resolving loader paths and Homebrew symlinks to exact library
files or their containing Cellar kegs with read/map access only. The per-run cache
rechecks executable and library mtimes and symlink targets; it never grants the
Homebrew installation prefix. Homebrew OpenSSL's host configuration remains
unreadable and is reported absent so Node can start with its built-in defaults.
Terminal completion
kills remaining group members before reaping its leader, including children that
close or inherit stdout. Codex and Cursor cannot yet enforce the
required inherited-tool ceiling; they, Pi and Grok do not advertise this new
capability. Unsupported platforms and missing bound tools/rules fail closed.
The boundary does not isolate another process running as the same OS user.
Claude steer queues the next input then interrupts the current turn. Interrupt
requires the SDK receipt; stop requires native Query.close and observed exit.
The managed panel replaces its legacy inbox composer with these exact-session
controls, leaving older sessions on their existing interface.

The same panel offers **Session settings** for qualified managed runs (AEON-224).
Name, model and effort use typed, person-only, ownership-fenced controls with
idempotent request IDs and pending/applied/rejected receipts. Model and effort
choices come from the run's exact enrolled account catalog; unsupported pairs
and unqualified harnesses are refused. The Claude bridge checks its live
`supportedModels()` catalog, calls `setModel()` or
`applyFlagSettings({ effortLevel })`, and reports metadata only after the SDK
acknowledges. Effort applies on the next turn. The daemon publishes the applied
settings by heartbeat before completing the control. Rename updates Aeon's
public `display_label` and metadata history after the daemon verifies ownership;
it does not enable vendor transcript persistence or rename files. A missing SDK
setter rejects the request. Codex remains unqualified. Tests use fake SDKs only.

Qualified runs default to 100,000 reported input/output tokens (including cache
usage) and 16 completed input turns, configured by agentd Config.MaxTokens and
Config.MaxTurns. The SDK also receives maxTurns for its agent loop. Counters
never reset on steering. Both the bridge and daemon close on exhaustion; the
bridge closes even if remote telemetry stalls. Stop reasons and any unconfirmed
exit are retained, tools close, and no force-stop fallback is implied. Token
usage arrives at vendor reporting boundaries and may exceed the threshold;
this is not a provider billing cap. Existing wall-clock deadlines remain.

People with `harness.recover` permission can open **Recover** in a session’s details and archive its registration after confirming the exact session and host. Archive preserves ticket links, outcomes and audit history, revokes the old worker generation, and records process state as unknown. It never signals a process. Late heartbeats, control completions and registration replays cannot reopen an archived generation; a new session needs a new reference and lease. The recovery dialog refreshes on stale observations. Recovery-aware daemons detach their harness registration without stopping the run. Active older managed daemons must stop normally before archive is available, because they cannot detach safely. Archive waits for any already-authorized force request to finish or expire.

Managed daemons report a per-launch process identity and generation. **Force stop** additionally requires human `harness.force_stop` permission, an ownership report no more than 45 seconds old, and exact session/host/process-group confirmation. Ownership recording and freshness checks use the Postgres clock, so API-host clock skew does not reject a fresh report; future-dated observations still fail closed. The daemon rejects a changed identity, restart, expired request or lost ownership; deadline and cancellation are rechecked under the final signal lock. Local transport and inbox credentials cannot authorize force stop. Expiry uses the database-derived monotonic budget above; archive cannot retract a signal that was already authorized or delivered, and always retains unknown process state. It signals only the owned process group (including children in that group), and reports root exit separately from queue acceptance; escaped descendants are outside its scope. Linux and macOS keep the group leader unreaped while signaling, preventing PID reuse. Unsupported adapters, legacy unmanaged sessions and offline ownership cannot be force stopped. Legacy normal user **Stop** sends TERM and reports timeout without escalating; the qualified Claude managed control uses native close as described above. Daemon cleanup retains its existing bounded force cleanup.

### Removing ghost sessions (AEON-265)

People with `harness.read` access to a project can remove a session from its
row's overflow menu (**Remove from Agents…**) or with **Remove** in the session
panel, then confirm once. The record immediately leaves
active views and counts; the **Removed** filter retains its ticket links and
history. `POST /projects/{projectId}/harness-sessions/{sessionId}/remove` takes
`{"reason":"..."}` (1–240 characters), uses the current locked record without a
client revision, and is idempotent even after another tab removed it. It accepts
unmanaged, legacy managed, offline, stopped, and pending-force registrations.

Removal revokes the generation: late heartbeat, mark-stopped, receipt and control
completion writes return 410. It releases outstanding deliveries and rejects
pending controls, recording `harness.removed` with actor, session and reason.
It never signals a process or asserts remote exit. A force signal already delivered
cannot be recalled; legacy daemons may independently react to the revoked lease.
The existing verified force-stop action in **Recover** keeps its identity and
confirmation checks; it cannot target an already revoked generation.

**Clear stale** (shown in the Sessions header only when stale sessions exist)
confirms once with the count, then calls the project-scoped
`POST /projects/{projectId}/harness-sessions/remove-stale` with a reason. The server
rechecks eligibility under row locks: the last accepted heartbeat must be older
than 15 minutes, falling back to creation time for sessions with no heartbeat.
A recently stopped record with a recent heartbeat is retained. One request removes
at most 200 records; the response lists them with the server cutoff and `more`,
and the UI repeats while `more` is true. Every removal event of one request carries
the same server-generated `batch_id` and the cutoff; retries produce no duplicate
removal audit.

Deploy approvals can carry optional `target` metadata: `hosts` or `environment`,
`service`, `change`, and optionally `image`. Targets appear on the deployment
card, Needs you, and approval history; missing targets read “Target not
named” and keep existing approval behavior. `target_digest_sha256` identifies the
recorded metadata and does not add an authority check (enforcement is AEON-287).
The additive contracts are `approvals/1.1` and `journey/1.3`. Stage handoff
responses remain byte-compatible `stage-handoffs/1.0` for strict PHAROS readers;
targets stay in storage and audit events, and the web reads them from the journey
deploy stage or approval, labelled “named by the agent”.
Managed agents can pass `target` and an optional `release_node_id` to
`aeon_request_approval`; `paimos external-stage request --operation deploy`
accepts an optional `--target-file JSON`. Verify handoffs retain the preceding
deploy handoff's recorded target internally when present; neither handoff body
emits target fields.

### Attach a running session (AEON-352)

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
another terminal and request the review. On a Mac that can use Touch ID, attach
approval asks for it by default until the person saves a choice. People without
Touch ID, Linux, and a Mac with no graphical login keep approval in Aeon.
Saving Mac confirmation turns watches off where Touch ID cannot run. SSH to a
Mac that can show Touch ID prompts on that Mac's screen.
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
to retry attach registration; there is no in-process registration retry.

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
(or the signed-in person owner). Fixed English/German hints name the next action;
unknown causes direct the owner to daemon status and the administrator's server
logs. Arbitrary server text never becomes terminal output. Revoked HTTP
bearers stay unauthenticated; pairing repair is offered only by the locally
authenticated interactive helper. Poll refusals still detach and clear local
state, and no uncertain conversation submission is retried.

If attach fails after a server restart, the daemon's memory-only poll registration
was lost: restart agentd when owned work permits, then request fresh approval.
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
The reporter contract is `harness-session/2.6`, declared by the response-only
`Aeon-Contract` header. Existing reporters keep working without a Pharos or
Janus release; registration and heartbeat requests need no contract header:
existing state values stay intact; optional `watch.process_state` carries a
confirmed exit. The existing default-off permission and code-attempt-cap tests
remain in `internal/agentpairing/watch_test.go`.

## Server outbound calls (AEON-493)

With default optional configuration and no opted-in tenant integrations, the active external-service call list is **empty**. Startup, scheduled default workers, health and installation-guide rendering do not contact GitHub, the tap, an update feed or a telemetry service. Postgres is the required operator-configured database dependency (`AEON_DATABASE_URL`), not an external-service integration; use a local socket or local address when the installation must have no network dependency. DNS resolution for the optional destinations below occurs only when their activation requires it.

This is the canonical inventory of server egress; there is no global switch that silently disables a configured integration. Disabling an integration means removing its activation, and an existing database may retain previously opted-in targets.

| Call and destination | Default | Activation / switch |
| --- | --- | --- |
| OIDC discovery, signing keys and token exchange at the configured identity provider | No issuer/client configured | `AEON_OIDC_ISSUER` and `AEON_OIDC_CLIENT_ID`; requests follow sign-in/token verification. Clear the issuer/client to disable. |
| Zitadel user lookup, creation and invitations | No provisioner | `AEON_IDENTITY_PROVISIONER=zitadel` plus `AEON_ZITADEL_URL`, tenant/org and token-file configuration; authorized invite operations. `none` or unset disables it. |
| Workspace model chat and embedding requests to the configured OpenAI-compatible endpoint | Off by default; lexical search only | A person with `settings.manage` enables the workspace provider and selects each feature. Disabling the provider or deselecting a feature stops its model calls. **Test connection** is the only model call while the provider is disabled; it requires an explicit person request and sends a synthetic prompt. Saving never contacts the model. |
| Inbox webhook delivery, including target DNS checks | No registered webhook receivers | An authorized receiver registers its webhook target; unregister/replace it to disable. Delivery workers run by default but have no external destination until configured. |
| Messaging routine webhook wakes | No registered routine webhook targets; messaging off in production without a key file | `AEON_MESSAGING_KEY_FILE` enables messaging (development uses an ephemeral key); an authorized routine receiver selects a webhook target. Remove that target to disable wakes. |
| GitHub doctrine metadata/tree/blob reads | No registered remote sources or requested sync | Authorized doctrine source registration and explicit sync/proposal operations select the repository and immutable pin. Private reads additionally require `AEON_DOCTRINE_CREDENTIALS_DIR` and a tenant/repository allowlist. Remove the source to disable future reads. |
| GitHub doctrine proposal token, branch/content and PR operations | App unconfigured | Complete `AEON_DOCTRINE_APP_*`, installation, tenant, gate-login and DCO configuration plus an authorized proposal/gate action. Remove the App configuration to disable. |
| GitHub cross-review token/status publication | App unconfigured | Complete `AEON_REVIEW_APP_ID`, `AEON_REVIEW_INSTALLATION_ID`, `AEON_REVIEW_APP_KEY_FILE`, tenant and repository configuration plus recorded review work. Remove the App configuration to disable. |
| OpenRouter public model catalog | No OpenRouter account/model selection | Authorized model changes on an enrolled `pi` account with provider `openrouter` perform an advisory catalog lookup. Changing/removing the provider account stops these lookups; no scheduled catalog polling. |
| Aithema service preview documents and host callbacks | Tenant service unconfigured | Authorized tenant host settings select `service_url` and qualified service evidence; remove those settings to disable. `AEON_AITHEMA_OPERATOR_LOCAL_SERVICES` only permits exact operator-local destinations and does not activate a service. |
| CRM provider search/fetch | Compiled server uses no provider adapters | A custom host must explicitly wire `PluginWithProviders` / `NewWithProviders`, grant integration access and configure a provider binding/secret reference. Remove that binding/adapter to disable. |

Separate processes have their own explicit destinations: `aeon-agentd` contacts its paired instance and selected model providers; user-run checksum installers and Homebrew fetch release/package assets; build/release/history tooling contacts the forge, registries and configured historical import sources. Those are not server startup workers. Classic migration readers remain historical CLI-only paths; they do not run in the server and Classic Paimos stays retired.

`TestServeShutdownAndBootstrap` observes and denies outbound HTTP/DNS while exercising startup and running handlers. On Linux amd64/arm64, `TestDefaultServerHasNoOutboundNetwork` additionally runs the full server against a fixture database through a Unix socket with a process-wide seccomp filter: any Internet socket attempt traps, including DNS and custom transports. Connect/DNS negative controls must trap before the test accepts the server run. The test exercises startup and two seconds of running workers/requests; it does not claim to simulate every optional integration or arbitrary elapsed time.

AEON-417 B adds an external **shadow authority** in `scripts/ci-authority` and a
disposable Linux guest init in `scripts/ci-executor`. Install reviewed binaries
outside all candidate workspaces; do not run this controller from a PR checkout.
The authority authenticates the original webhook body with HMAC-SHA256, reads
the numeric repository identity and current main/PR/queue state from GitHub,
and reconstructs the complete plan from its dedicated bare mirror. PR plans
bind the source head and the API-resolved merge commit separately. Queue plans
enumerate every constituent through Git parents and recheck the active queue
ref. Checks are revalidated after reconciliation. Its output includes each
existing required context, every extra inventory context, and `ci/trusted`, all
with **pending** status. There is no check writer or activation flag.

`ci-authority shadow --config /absolute/controller/config.json --event
pull_request --delivery <delivery-id> --signature <X-Hub-Signature-256> --webhook
/absolute/controller/event.json --generation <positive-generation>` reads only
Git objects and GitHub API state. Configuration has schema
`aeon.ci.authority-config.v1`, `mirror`, a `git` object with absolute `path` and
raw SHA-256 `digest`, and `authority`
containing `repository_id`, `repository`, the reviewed `policy` pin,
`environment_digest`, and `verifier_app_id` (zero until provisioned). Supply
`AEON_CI_WEBHOOK_SECRET` and a read-only `AEON_CI_READ_TOKEN` to the controller
process through approved credential storage; neither is forwarded to execution
or output. The installed authority requires a separate Linux host and verifies
root ownership, non-writable parents and Git bytes before opening its mirror.
Mirror refresh is a separate trusted ingress responsibility. The
current replay/generation guard lasts for one controller process; production
needs durable serialized ingress before any authority activation.

`observe` additionally needs a `profile`, `admission_public_key`, signed
`--admission`, `--obligation`, `--run-id`, and `--job-id`. The profile fixes each
obligation's absolute `/opt/aeon/` command, stage, reporter and expected manifest,
plus raw SHA-256 pins for QEMU, firmware, kernel, initrd and a read-only ext4 root
image. It also binds harness/toolchain/environment digests, security epoch,
separate QEMU UID/GID, CPU/memory limits and timeout. The supervisor deep-copies
these settings and requires `infrastructure: hosted-disposable`; the provider
must independently attest that placement. This backend supports Linux/amd64
only and never routes candidates to the trusted main pool or production hosts.
Each metadata/build/test stage boots a new Linux/KVM VM with
read-only framed task/source disks, no network, no host mount and no monitor or
control socket. The root guest init uses a read-only, `nosuid` executor image and
an unprivileged candidate process in private tmpfs storage with a fixed offline
environment. Source export reads every verified Git blob directly, ignores
candidate export attributes, and refuses symlinks/submodules and unsafe paths.
Only bounded content-addressed opaque artifacts can cross the result interface.
Candidate stdout is never interpreted as a host receipt or GitHub check.

Admission is independently Ed25519-signed over the plan, exact candidate commit,
complete tree-manifest digest, policy, executor, approved harness, environment,
epoch, review target/record and a maximum 24-hour validity window. This admits
the complete executable closure, including package initializers, JS helpers,
dependency/config and lifecycle code. It cannot establish honest assertions in
unreviewed code. Supervisor observations have an in-process private seal;
serializing one loses that provenance. Trusted ingress can revoke an admission;
expiry and revocation are rechecked after execution and during reconciliation.
Durable signatures/revocation, source
run/job verification, full receipts and reuse remain E's responsibility. Partial
reruns are refused. The browser/application VM connection and approved browser
reporter remain D/H work and currently fail closed.

This is an unactivated implementation: independently reviewed VM images,
Linux/KVM hosted provisioning and a real boot/tamper probe remain required.
The tests cover protocol, admission and supervisor ownership, including attack
classes from all five AEON-421 reviews; they do not certify a live VM boundary.
App provisioning, expected-App per-context ruleset probes, durable ingress and
review revocation are later coordinator/OPS steps. No workflow, runner route,
required check, version or execution selection changes here. Keep full existing
CI and both optimization switches off until those prerequisites are proven.
Build the guest init with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`; the approved
kernel needs built-in devtmpfs, virtio block/PCI and ext4 support. The pinned
rootfs needs `/workspace`, `/tmp`, `/proc`, `/dev` mountpoints and all approved
tools/dependencies under `/opt/aeon`. No image is produced or provisioned by
this worker, and missing images, recipes or admission refuse execution.

The Decision Desk UI (AEON-567) lives at `/decision-desk`. Open and answered
questions are paged separately; refresh retains up to ten loaded pages per state
(1,000 questions), with remaining results stated explicitly. Each memo freezes its
source for the round. New arrivals wait for the next round, including rounds opened
from Decided. Expired approvals appear in history. Approvals and tier requests
require an explicit choice; Enter on another focused control performs that control's
action. In a field, Enter finishes editing and the platform modifier plus Enter
submits. Access loss, expiry and changed sources block the write and retain drafts.

Tier requests are read only for sessions advertising AEON-436's `service_tier_v1`.
AEON-455 server availability selects request-bound phone verification on every
screen size; a server without that package keeps the existing approvals API.
Held replies address the original principal UUID and, when supplied, its exact
session. Related ticket records use permission-checked node relations; specialised
Always/doctrine publishing and its context projection remain upstream package
integration work. The unavailable stamps explain their restrictions in the memo.

Decision Desk question groundwork (AEON-562): `aeon ask --project KEY
--option '["Title","Description","Answer"]' "Question"` stores a bounded,
project-scoped question and returns immediately. Add `--ticket KEY`,
`--context-file PATH`, `--recommend 1 --why "Reason"`, `--meanwhile parked`,
`--meanwhile-text "Other work"`, `--keep once` or `--anyway "New evidence"`
as needed. Retain the printed `--request-id UUID` and exact input for retries;
`aeon ask status UUID` reads the durable question after the original session ends.
`--session UUID` is a verified public harness generation, separate from the
CLI's global attribution `--session-id`. Named project/ticket lookup uses the
existing node read permissions; UUID addressing needs only the question scopes.
The MCP `ask` tool accepts `{project, ticket?, input}` with the same typed HTTP
input and mandatory request ID; `ask_status` accepts `{question_id}`.

The additive API is documented in `api/openapi.yaml`: project question create/list,
question get/status/person decision and a permission-filtered `/decision-desk`
question projection. `questions.ask` and `questions.read` are explicit agent key
scopes; `questions.decide` is person-only. Owner/admin/member roles receive all
three, viewer receives read, guest/customer receive none. Existing keys gain no
new scopes. Agents read only questions they asked, with only their memberships.
Generic node/knowledge CRUD cannot modify question/decision authority.

Questions and immutable answer revisions use protected nodes plus tenant/project
projections. Each asker retains input, principal, exact original session, source
request, reply-root UUID and comment destination (ticket, or question node).
The reply root is a reserved correlation identity, not yet a public inbox message:
P3 must bind the answering person and materialize the inbox counterpart before
using `tell --reply-to`. No synthetic recipient or newer generation is guessed.
P1 person answers support Once and record revision-bound pending inbox/comment/
outcome effects with a database-clock ten-second deadline. They do not dispatch
messages or claim successful delivery. Always/Requirement/Doctrine publication,
verified handover sources and post-dispatch corrections require the later adapters;
unsupported requests fail explicitly. Suggestions for all four outcomes are stored.
