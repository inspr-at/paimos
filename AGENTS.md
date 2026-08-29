# PAIMOS Agent Quickstart

Start here when opening this repo cold.

## Core commands

- `cd backend && go test ./...`
- `cd backend && go test -race ./agentd ./localjournal ./ownedprocess`
- `cd frontend && npm test -- --run`
- `cd frontend && npm run typecheck && npm run build`

## High-signal docs

- [`docs/DEVELOPER_GUIDE.md`](docs/DEVELOPER_GUIDE.md)
- [`docs/AGENT_INTERFACE.md`](docs/AGENT_INTERFACE.md)
- [`docs/AGENT_INTEGRATION.md`](docs/AGENT_INTEGRATION.md)
- [`docs/ANCHORS.md`](docs/ANCHORS.md)

## Project-context surface

- `GET /api/projects/{id}/repos`
- `GET /api/projects/{id}/knowledge` — unified knowledge plane (PAI-338); replaces the removed `/manifest` endpoint
- `POST /api/projects/{id}/anchors`
- `GET /api/projects/{id}/graph`
- `GET /api/projects/{id}/graph/blast-radius`
- `POST /api/projects/{id}/retrieve`
- `GET /api/issues/{id}/anchors`
- `GET /api/projects/{id}/agents/{name}.json` — canonical agent artifact (PAI-329)
- `GET /api/projects/{id}/messages/listen` · `POST /api/projects/{id}/messages/ack` — attributed durable agent inbox (PAI-816)
- `POST /api/projects/{id}/messages` · `GET|POST /api/projects/{id}/message-targets` · `GET /api/projects/{id}/message-deliveries` — durable send with `delivery_level`, encrypted receiver targets, and redacted delivery state (PAI-826; see [`docs/api-minimal.md`](docs/api-minimal.md))
- `GET|POST /api/projects/{id}/harness-sessions` · `POST /api/projects/{id}/harness-sessions/{sessionID}/{heartbeat|yield|drain|complete-delivery|stop}` · `POST .../{sessionID}/controls/{kind}` — durable managed/unmanaged harness control plane with attributed full-FIFO inbox delivery and typed owned controls (PAI-848; see [`docs/AGENT_INTEGRATION.md`](docs/AGENT_INTEGRATION.md))
- `POST /api/issues/{id}/implement` · `GET /api/issues/{id}/runs` · `GET|PATCH /api/runs/{id}` · `GET /api/projects/{id}/runners` — "Implement this" run lifecycle (PAI-605; see [`docs/AGENT_INTEGRATION.md`](docs/AGENT_INTEGRATION.md))

## Repo-side tooling

- `paimos anchors scan --output .paimos/anchors.json`
- `paimos anchors verify --index .paimos/anchors.json`
- `paimos onboard --project PAI [--agent <name>]` — single-shot project briefing (PAI-340)
- `paimos skill render <agent>` — render an agent artifact through a harness adapter (PAI-329 / PAI-332)
- `paimos run-agent watch --project PAI --repo-root .` — local "Implement this" runner; spawns Claude Code on a UI-triggered run, report-back only (PAI-608)
- `paimos-agentd serve --instance <ppm-instance>` — operator-local owner for managed Codex children; private per-instance Unix socket, with Claude control supplied separately by PAI-850
- `paimos tell <harness>:<agent> --project PAI --level simple|steer -m <text>` — durable message with receiver interruption intent; `--action-request` holds it for humans (PAI-826)
- `paimos message target set --project PAI --address <harness>:<agent> --adapter codex|agentd_codex|grok_bot_routine|claude_resume|claude_channel --kind codex_thread|agentd_session|https_webhook|claude_session …` — register an encrypted receiver target; `grok_bot_routine` additionally requires `--target-key-file <file|->` (the routine sender key, sent as `Authorization: Bearer` on every wake; never an argument); `message target list` / `message deliveries` show redacted state (PAI-826 / PAI-827 / PAI-828 / PAI-849)
- `paimos listen --as <harness>:<agent> --project PAI [--follow] [--ack] [--deliver codex|agentd_codex|claude|grok]` — durable native inbox delivery using the receiver-owned target (`--deliver-target` only for pre-bus rows); Grok Build additionally requires `--enable-grok-build-delivery` (PAI-816 / PAI-826 / PAI-849)
- `paimos harness register|list|status|heartbeat|yield|drain|complete-delivery|interrupt|stop|complete-control|mark-stopped ...` — durable harness-session lifecycle, with steer-named drain/completion compatibility aliases; distinct from attribution-only `paimos session start` (PAI-848)

## Notes

- The committed `.paimos/anchors.json` is dogfood for the anchor tooling.
- Skill files rendered by `paimos skill render` carry a paimos-managed header so `paimos sync check` can detect drift; the legacy `paimos manifest pull` flow was removed in PAI-358 (replaced by the knowledge plane).
