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

GitHub release assets, next to `aeon-agentd` and listed in the same `SHA256SUMS`: `aeon-cli-darwin-amd64`, `aeon-cli-darwin-arm64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64`. Put the file on `PATH` as `paimos` and symlink `paimos` to it.

Invoking the binary as `paimos` gives the paimos-compatible CLI. `PAIMOS_URL` (with `PAIMOS_API_KEY` or `PAIMOS_API_KEY_FILE`) is the process-only target.

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
