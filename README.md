# PAIMOS AEON

The next generation of Paimos: an agents-first, voice-first, multi-tenant work platform in the INSPR family. Hybrid human work stays first-class: a fully dynamic work tree, list, search and a Markdown sidebar viewer.

Status: under construction (release R0). Live at <https://aeon.barta.cm> once R0 ships. Decisions: PPM project AEON, ADR-001 (foundation) and ADR-002 (stack: Go, Postgres 18 + pgvector, Vue 3).

## Develop

```sh
just db-up        # Postgres 18 + pgvector on :55432 (Docker)
just test         # Go tests
just web-check    # web typecheck and build
just dev          # run the server (API on :8080); `cd web && npm run dev` for the UI
```

Project sections have their own URLs: `/p/KEY/tickets`, `/p/KEY/journey`, and
`/p/KEY/knowledge`. A ticket uses `/p/KEY/TICKET`; `?section=journey` or
`?section=knowledge` retains its background section. Tickets is the default,
so its ticket links need no section query. Existing `?view=full` ticket links
still open the full-page ticket at the same address.

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

Wide project headers can show an ambient ticket graph (Display → Graph in
project header). It uses a tilted 3D cloud with an optional elliptic force bias,
fits the densest 85% of nodes by height, and fades out inside the empty space
between the text block and counts. Reduced motion, narrow screens and sparse
graphs suppress it; hover exposes Open graph and Pause. Header framing and text
separation are covered by `web/tests/header-glimpse.spec.ts`.

## Command line

`paimos` is the agent command line. `paimos serve` still runs the server. Existing doctrine commands keep their shape.

```sh
paimos auth login --url https://aeon.example --name default --key-file ./agent.key
paimos whoami
paimos issue list --project AEON
paimos mcp
```

Named instances and the default live in `~/.aeon/config.yaml`. The agent API key is read from `--key-file` or stdin, never echoed, and stored under `~/.aeon/keys/` mode 0600. `AEON_URL` together with `AEON_API_KEY` (or `AEON_API_KEY_FILE`) is a process-only target. When the binary is `paimos`, `PAIMOS_URL` and `PAIMOS_API_KEY` work the same way.

`whoami` calls `GET /api/me`. Issue, knowledge, search and onboard exit 3 with `arrives in R1` until those endpoints exist. `model resolve` exits 3 with a not-yet message. `aeon mcp` serves those tools over stdio.

Versioning: INSPR Calendar Versioning v2 (`inspr-calendar-v2`, `YYMMDDhhmmss.0.0`); the version display uses the pinned INSPR presentation bundle, checked by `just release-check`.

## Install the CLI

Nix installs `bin/aeon` and a `bin/paimos` symlink:

```sh
nix profile install github:inspr-at/aeon#aeon
```

GitHub release assets, next to `paimos-agentd` for the same four OS/architecture pairs and listed in the same `SHA256SUMS`: `aeon-cli-darwin-amd64`, `aeon-cli-darwin-arm64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64`. Put the CLI file on `PATH` as `aeon`; a symlink named `paimos` selects its compatibility mode. For checksum-verified computer pairing, see [Agent integration](docs/AGENT_INTEGRATION.md).

Invoking the binary as `paimos` gives the paimos-compatible CLI. `PAIMOS_URL` (with `PAIMOS_API_KEY` or `PAIMOS_API_KEY_FILE`) is the process-only target.

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

## Agent rules (ADR-004, AEON-248 / AEON-249)

The dedicated `/api/rules` API stores layers, sets, rules and immutable version
snapshots as nodes. Generic node/event APIs cannot read or mutate these resources.
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
checked on every request. The complete rendered body must fit 12,000 UTF-8 bytes;
an oversized result returns 422 with the measured size instead of truncating.
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

## UI shell (P0.5 / AEON-10)

The Vue shell includes an authenticated workspace, sign-in, a 404, an account
menu, and light/dark themes. The theme follows the operating system until the
user toggles it; that choice lasts for the current page session and writes no
browser storage. Assets and fonts are served locally. The supplied mark is
preserved at `web/src/assets/brand/aeon-mark.svg` for its later replacement.

The auth adapter is isolated in `web/src/lib/api.ts`. Pending P0.3 contract
confirmation, it expects `/api/me` to return
`{ principal: { id, name, email? }, tenant: { id, name }, dev_mode?: boolean }`.
A 401 clears identity and routes to sign-in. Development email sign-in is
hidden unless the server explicitly returns `dev_mode: true` (including on its
401 response). It never relies on Vite's development mode. Login navigates to
`/api/auth/login`; development login posts `{ email }` to `/api/auth/dev-login`;
sign-out posts to `/api/auth/logout` before routing to `/signin`. API calls use
same-origin credentials, a ten-second timeout, and no browser response cache.
The backend owns authentication cookies and the INSPR authentication redirect.
No analytics, third-party runtime assets, or optional device storage are added.

Both version surfaces use the unchanged, verified calendar bundle in Pretty
mode with brand gold. The shared helper provides reveal and copy interactions;
`dev` remains plain text. Every production web build verifies the bundle pin.

```sh
cd web
npm run test:unit
npx playwright install chromium
npm test
```

The Playwright suite starts Vite on port 5175, intercepts all `/api/*` calls,
and covers sign-in, auth errors, logout, theme switching, version interactions,
44 px targets, and viewport overflow. It writes home, sign-in, development
sign-in, and 404 screenshots in both themes at 1280×720 and 390×844 to
`/tmp/aeon-p05-shots/`. Screenshots and browser test output are not committed.

Licence: AGPL-3.0-only.

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

Managed daemons report a per-launch process identity and generation. **Force stop** additionally requires human `harness.force_stop` permission, an ownership report no more than 45 seconds old, and exact session/host/process-group confirmation. Ownership recording and freshness checks use the Postgres clock, so API-host clock skew does not reject a fresh report; future-dated observations still fail closed. The daemon rejects a changed identity, restart, expired request or lost ownership; deadline and cancellation are rechecked under the final signal lock. Local transport and inbox credentials cannot authorize force stop. Expiry uses server and daemon wall clocks, which need normal host clock synchronization; archive cannot retract a signal that was already authorized or delivered, and always retains unknown process state. It signals only the owned process group (including children in that group), and reports root exit separately from queue acceptance; escaped descendants are outside its scope. Linux and macOS keep the group leader unreaped while signaling, preventing PID reuse. Unsupported adapters, legacy unmanaged sessions and offline ownership cannot be force stopped. Legacy normal user **Stop** sends TERM and reports timeout without escalating; the qualified Claude managed control uses native close as described above. Daemon cleanup retains its existing bounded force cleanup.

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
