# PAIMOS AEON · rules for every worker

You are one of several agents building PAIMOS AEON in parallel. A coordinator plans releases, dispatches work packages, merges and deploys. Read this file fully before you start.

## What Aeon is

The next generation of Paimos: agents first, voice first, multi-tenant, hybrid human work first-class. Decisions are fixed in two accepted ADRs (PPM project AEON, knowledge `adr-001-foundation` and `adr-002-stack`):

- Core model: **nodes** (one type in a fully dynamic tree, kinds and labels are tenant configuration), **relations** (typed links), **events** (one append-only log per tenant for audit, history, undo, live updates), plus **principals** (people and agents) and **files**.
- Every row carries `tenant_id`; Postgres row-level security enforces isolation.
- Stack: Go (standard library HTTP, `pgx`, `sqlc`-style typed SQL), Postgres 18 + pgvector, Vue 3 + TypeScript + Vite, one OpenAPI 3.1 contract (`api/openapi.yaml`), server-sent events for live updates, OIDC through Zitadel for people, scoped API keys for agents.

## Layout

```
cmd/aeon/            single binary: `paimos serve` and later the CLI
internal/            Go packages (config, db, migrate, tenant, auth, httpapi, events, …)
internal/db/migrations/  numbered SQL migrations, embedded
api/openapi.yaml     the contract; change it first, then code
web/                 Vue 3 + TypeScript app (built into the binary)
scripts/             release checks
```

## How you work

1. **Your package is your scope.** Only change files your package needs. If you must touch a shared file (`api/openapi.yaml`, `go.mod`, `web/package.json`, a migration number), keep the change minimal and additive.
2. **Contract first.** Endpoints are added to `api/openapi.yaml` in the same change as their handler.
3. **Migrations** are numbered `NNNN_name.sql` in `internal/db/migrations/`; take the next free number within your package's range (P0.2: 0001–0019, P0.3: 0020–0039).
4. **Tests are part of done.** `just test` must pass (Go tests use the Postgres from `just db-up`; web: `just web-check`). Add tests for what you build.
5. **Progress file.** After every meaningful step overwrite `.agent-status.json` in your worktree root (it is git-ignored):
   `{"package":"P0.2","worker":"grok-1","pct":40,"remaining_min":25,"note":"migrations and RLS done"}`
6. **Commits** on your branch only, message `P0.x: what changed` plus the ticket key (e.g. `AEON-7`). Never commit secrets, `.env` files or generated build output. Do not push; the coordinator merges.
7. **Never** read or print secrets, never touch other worktrees, never run destructive git (`reset --hard`, `clean -f`, `push --force`).
8. When done: `pct` 100, a one-paragraph summary in `note`, and a final commit.
9. **No reviews by other models.** Do not run cross-family or any other review gates per package, and never call other model CLIs (claude, codex, grok, cursor-agent) yourself. QA is consolidated per release by the coordinator (Markus, 2026-09-23).
10. **Classic Paimos is retired.** The cutover is done (2026-09-26, AEON-43); its removal is tracked in AEON-261. Never build on classic or bring it back; old pm.barta.cm links resolve through `/from-classic`. pm.augmentoring.com (business trust context) follows each live-verified Aeon release via the documented PMA bump flow only (agm-nixcfg pin, backup first, tickets in the pma tracker, never PPM); no other changes there (Markus, 2026-09-30).
11. **No colored edge accents in the UI.** Never mark selection, emphasis or state with a colored bar or thick border on the left or top edge of a row, card, callout, toast or panel; it is the classic AI-generated UI tell. Use a subtle full tint, a hairline outline or ring, elevation, or type weight instead (Markus, 2026-09-24).

## Style

- Go: standard library first, small packages, explicit errors, context everywhere, no global state except in `main`.
- SQL: every table has `tenant_id` (except `tenants`), RLS policy on `current_setting('aeon.tenant_id')`.
- Web: Vue 3 `<script setup lang="ts">`, design tokens from `web/src/styles/tokens.css`, SVG icons only (never text glyphs), icons centred in their controls.
- Licence: AGPL-3.0-only. SPDX header in new source files.
