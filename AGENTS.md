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
- UI stability (AEON-541): **The control stays put; content grows away from controls or moves inside a scroll area.** Selecting, hovering, typing or toggling never changes a control's size or position, or its neighbours in the same group. Feedback appears in place, never by pushing controls. Rule 7 governs the choice of layout; fixed heights are not the goal.
  1. For long content, Accept, Decide & next, Skip and Cancel live in a fixed footer; content above uses a scrolling body. Longer content means scrolling, never a moved button.
  2. One control layout and position for a whole series (1 of N). After next/previous, action buttons stay identical and focus stays on the same button. Top-anchored frames may grow downward; a series with an already scrolling body retains the same dialog size.
  3. Selectors and option lists keep fixed row heights. Selection explanations and effects occupy a reserved slot below the list, never between options.
  4. Anything that must grow grows away from the control: below the last control or inside the scroll area. Use rule 7 to choose the layout, then apply the control and selector requirements above.
  5. Phones use a full-height sheet with the action bar pinned at the bottom, including the safe area. No free-floating buttons.
  6. Important dialogs must use the reusable Playwright stability guard: measure named actions, selectors, selector groups and the clicked row before/after every option interaction and series step (±0.5 px). Assert frame height only for pattern B (phone sheets or an already scrolling body); pattern A frames may grow downward. Require at least one interaction and positive-size samples; user scrolling is measured in scroll-container coordinates, and horizontal overflow fails.
  7. Refinement (Markus, 2026-10-02): controls never moving is the goal, not fixed heights. Prefer a dialog anchored at a fixed top position with navigation, choices and actions above content that grows downward. Short content stays short; never pad it to a fixed height just to hold a button still. Use the pinned footer with a scrolling body for long content and phone sheets. Use hairlines, whitespace, typography and a fitting metaphor; do not surround every element with grey rounded rectangles or forced pills.
- Keyboard convention (AEON-541): single-letter shortcuts work only outside text fields. Submit from a field with ⌘↵ on macOS or Ctrl+↵ elsewhere; detect the platform and match the labels. Esc leaves a field, then closes on the next press. Preserve browser and OS shortcuts (⌘S/⌘R/⌘D/⌘P/⌘A and Ctrl+A stay native). Show keycaps on the buttons they trigger.
- Licence: AGPL-3.0-only. SPDX header in new source files.
