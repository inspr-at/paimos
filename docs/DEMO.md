# Demo seed

`aeon demo seed --tenant <slug>` fills an existing tenant with fictional data for product screenshots. It refuses to run unless `AEON_ENV` is exactly `dev`. An unset variable is not dev. Run it again on the same tenant and it does not add a second copy.

The tenant must already exist (`paimos tenant create`). The command does not create a tenant and it does not sign anyone in with a real identity provider. It binds three fictional people through the tenant bootstrap helper, using the issuer `https://demo.aeon.invalid`:

- Demo Operator, admin
- Ivo Quill, member
- Nia Frost, member

What the first successful run writes through the application modules and operator key store:

- Three projects: Lumen Archive (`LUMEN`), Harbor Ledger (`HARBOR`), and North Glass (`NGLASS`), each with a description.
- About 40 tickets under epics, with mixed states, assignees, comments, and relations. One Lumen ticket has a long fictional brief. Attachments are not seeded.
- Eight knowledge entries covering runbook, guideline, memory, external system, and related project, linked by `[[slug]]` mentions.
- Three agents: Lumen Scribe (Codex), Harbor Clerk (Claude), and Glass Scout (Grok). Each has an ended harness session and a completed run on a fictional work order in its project. Clerk keeps its ticket approval request pending.
- Three fictional accounts on Demo workstation: Lumen desk, Harbor desk, and North Glass desk. Each has a demo allowance and an explicit grant to an enabled registry profile for its harness. StartAgentDialog reads that host, the three harness choices, accounts, models and thinking levels from `/api/agent-accounts/catalog`.
- Profile selection matches the harness. If no enabled Grok harness profile exists, the seed creates a demo Grok profile using an enabled Cursor/xAI registry pin's model, effort, tier and version. Existing profiles and role routes are preserved; no model ID is invented. Missing enabled profiles make the seed fail and roll back.
- People and agent comments on `LT-1`, including Scribe's work marker and a link to the completed session. Its detail projects the completed run's state evidence. Its provenance contains the content hash and byte size of the authored fictional `lantern-review/SKILL.md` instructions. No workstation file is read.
- A few hour entries on a fictional cost unit, priced in exact EUR decimals.

The seed commits all its changes in one database transaction. An interrupted run rolls back its nodes, keys, bindings, and events; a retry starts cleanly. The completion marker is `fields.demo_seed` = `complete` on the Lumen Archive project node. A later run reads that marker and returns without new events or refreshed account probes, windows, gates, or provenance. Already completed demo tenants are not backfilled: create a fresh demo tenant to capture these additions.

The seed no longer creates stages, gate grants, or handoffs from the historical
INSPR Flow / Journey, retired by AEON-723 in
[Direct Dome](https://github.com/inspr-at/paimos/releases/tag/v261007035250.0.0).
Existing work, harness sessions, runs and ordinary approval evidence remain.
[Flow 2 (AEON-821) is planned](features/web-workspace.md#planned-work);
the seed does not demonstrate it.

Money in the seed is an exact decimal rate (`80.00` internal, `140.00` bill, EUR per hour). Durations are whole seconds. Nothing is stored as a binary float.

`demo.Seed(ctx, pool, slug)` is the same work without flag parsing, for tests. Both refuse a non-dev `AEON_ENV`. The seed is a CLI command only; it has no HTTP route.

## Dev instance for captures

Use a dedicated local database and one stable origin, for example `http://127.0.0.1:8080`. The capture pipeline belongs to inspr-at; point its browser at this origin. Keep the tenant slug, origin and commit in the capture manifest there. Do not use production data or the UI-audit fixtures.

After `just db-up`, create a separate local database with `docker exec aeon-dev-db psql -U aeon -d postgres -c 'CREATE DATABASE aeon_demo_capture'`. If that name already exists, inspect the owned demo database or choose a fresh name; do not drop it. Use the development binary built from the reviewed checkout (`go build -o bin/paimos ./cmd/aeon`) and the built frontend (`just web-check`). In a dedicated terminal, set these non-secret development values:

```sh
export AEON_ENV=dev
export AEON_DATABASE_URL='postgres://aeon:aeon@127.0.0.1:55432/aeon_demo_capture?sslmode=disable'
export AEON_ADDR=127.0.0.1:8080
export AEON_PUBLIC_URL=http://127.0.0.1:8080
export AEON_BOOTSTRAP_TENANT_SLUG=lumen-demo
export AEON_BOOTSTRAP_TENANT_NAME='Lumen Demo'
export AEON_BOOTSTRAP_ADMIN_EMAIL=demo.operator@demo.aeon.invalid
export AEON_WEB_DIR=web/dist
bin/paimos serve
```

Server startup opens and migrates the database and creates the configured bootstrap tenant. Wait for its `aeon listening` log, then in another terminal with the same explicit dev database/environment values run `bin/paimos demo seed --tenant lumen-demo`. A later `bin/paimos demo seed --tenant lumen-demo` must report `"already": true` and leave the data unchanged.

Use the dev sign-in at that origin with `demo.operator@demo.aeon.invalid` and tenant `lumen-demo`. The dev login creates a separate fictional capture operator; the seed's Demo Operator remains the author of its historical decisions. A headless capture context can POST `{"email":"demo.operator@demo.aeon.invalid","tenant":"lumen-demo"}` to `/api/auth/dev-login` and keep the response cookie in that context. Do not print, copy into a manifest, or commit the cookie. This route requires dev mode; it does not demonstrate IdP sign-in.

Capture targets are the Start agent dialog on `LT-1`, Harbor Clerk's pending ticket approval in Needs you, and `LT-1` Activity filtered to People and agents. Follow the agent comment's session link for state evidence and instruction provenance. The session is ended and its run is completed, so it does not turn into a fabricated live delivery or a stale-heartbeat failure line.

Account probes and the two-hour allowance windows are fictional seed snapshots. They expire normally and replay does not refresh them. Use a fresh tenant for a new set of seed snapshots, or an actually paired account for live availability. Harness and model controls retain their registered names. The promo-owner decision for AEON-497 (2026-10-01) is to show a mix of Codex, Claude and Grok in the picker and session list so no single vendor is featured; promo framing must not ring or caption a vendor name. Local-provider configuration and IdP sign-in need their respective real setup; the seed supplies neither a provider service nor IdP credentials.

## Pair a real local runner

The seed never sends a harness heartbeat or a runner presence report. Its completed run, account probe and session history do not mean a runner is online. For a running-delivery shot, pair a real local `aeon-agentd` to this dev instance after the runner support is available in both binaries.

Use a reviewed helper from the same commit as the dev server (`go build -o bin/aeon-agentd ./cmd/aeon-agentd`) or the exact compatible release from the instance's `/agents/register-agent` guide. From the intended local demo working folder, run:

```sh
/absolute/path/to/checkout/bin/aeon-agentd pair \
  --url http://127.0.0.1:8080 --tenant lumen-demo \
  --state-root "$HOME/Library/Application Support/aeon/demo-capture"
```

That state-root example is for macOS; on Linux use a dedicated folder under `${XDG_STATE_HOME:-$HOME/.local/state}/aeon/demo-capture`. It must stay outside repositories and the working folder. Use a new private root for this dev pairing; preserve any existing production pairing and service. Confirm the working folder and select an installed, signed-in harness locally. Approve the displayed pairing code as the fictional operator at this dev origin. This is a person action: automation must not approve it or borrow a production account's authority.

On an unmanaged installation, pairing starts its owned user service after approval. A managed installation leaves activation to the configuration owner; the command waits for that service. Do not replace an existing service to run a demo. See [AGENT_INTEGRATION.md](AGENT_INTEGRATION.md#guided-computer-pairing-aeon-238-and-aeon-239) for service ownership, dependency checks, and safe disconnection.

Run `/absolute/path/to/checkout/bin/aeon-agentd status --state-root <the-private-demo-root>` and verify that the helper reports connected to the dev instance. In Aeon, choose the newly paired host/account in Start agent and queue a separate fictional ticket. Record a running-delivery shot only when that real daemon is online and the new run has actually been claimed and is running. A queued run, the seeded completed run, or an available account alone is insufficient. If the helper or owned service is unavailable, skip the shot and record the blocker; never insert a heartbeat. When finished, disconnect this demo pairing through the helper's normal lifecycle, retaining private state until cleanup is confirmed.
