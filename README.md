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

The repository will be renamed to `inspr-at/paimos` at cutover. GitHub redirects the old name, so this flake reference keeps resolving.

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

Preview output includes exact body hash/version/size and an AEON-219 provenance
payload. `--rules-record-received SESSION_UUID --rules-worker-lease-file PATH`
explicitly posts it to an existing owned harness generation. This records receipt,
not execution; `execution_verified` remains false. It does not mint a registered
harness session. Stub installation, automatic registration, rollout comparison,
template import/export and UI are separate coordinator-owned work.

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

People with `harness.recover` permission can open **Recover** in a session’s details and archive its registration after confirming the exact session and host. Archive preserves ticket links, outcomes and audit history, revokes the old worker generation, and records process state as unknown. It never signals a process. Late heartbeats, control completions and registration replays cannot reopen an archived generation; a new session needs a new reference and lease. The recovery dialog refreshes on stale observations. Recovery-aware daemons detach their harness registration without stopping the run. Active older managed daemons must stop normally before archive is available, because they cannot detach safely. Archive waits for any already-authorized force request to finish or expire.

Managed daemons report a per-launch process identity and generation. **Force stop** additionally requires human `harness.force_stop` permission, a fresh ownership report, and exact session/host/process-group confirmation. The daemon rejects a changed identity, restart, expired request or lost ownership; deadline and cancellation are rechecked under the final signal lock. Local transport and inbox credentials cannot authorize force stop. Expiry uses server and daemon wall clocks, which need normal host clock synchronization; archive cannot retract a signal that was already authorized or delivered, and always retains unknown process state. It signals only the owned process group (including children in that group), and reports root exit separately from queue acceptance; escaped descendants are outside its scope. Linux and macOS keep the group leader unreaped while signaling, preventing PID reuse. Unsupported adapters, legacy unmanaged sessions and offline ownership cannot be force stopped. Normal user **Stop** sends TERM and reports timeout without escalating; daemon cleanup retains its bounded force cleanup.
