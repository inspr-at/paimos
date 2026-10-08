# PAIMOS AEON · rules for every worker

You are one of several agents building PAIMOS AEON in parallel. A coordinator plans releases, dispatches work packages, merges and deploys. Read this file fully before you start.

## What Aeon is

The next generation of Paimos: agents first, voice first, multi-tenant, hybrid human work first-class. Decisions are fixed in two accepted ADRs (PPM project AEON, knowledge `adr-001-foundation` and `adr-002-stack`):

- Work creation: **create a work item; nesting decides its name**. Agents work on leaves only; parent statuses follow their children and are never set by lead status scripts.
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

1. **Your ticket is your scope.** Change only the paths in your brief's write set. If you must touch a shared file (`api/openapi.yaml`, `go.mod`, `web/package.json`, a migration, `AGENTS.md`), keep the change minimal and additive and say so in your summary.
2. **Contract first.** Endpoints are added to `api/openapi.yaml` in the same change as their handler. Keep contract changes additive and backward compatible (PHAROS and JANUS parse strictly; never rename or remove fields or routes). Quote OpenAPI descriptions and run `go test ./internal/reportercontract/`. For new routes, update the strict route tables in tests deliberately, and bump the Aeon-Contract version when a pinned response schema changes.
3. **Migrations** are `NNNN_name.sql` in `internal/db/migrations/` and **expand-only**: add tables, nullable columns or indexes. NOT NULL, CHECK or FK constraints that older writers could violate, DROP, RENAME and destructive UPDATEs need `-- aeon:contract-phase …` after a released expansion. Use the number your brief names (reserved by the coordinator, above the latest released maximum); on a collision with main, stop and report, never renumber yourself. Run `node scripts/check-migrations.mjs`. Classify new tables and columns in `internal/dsar/inventory.json` in the same change.
4. **Tests are part of done** and follow the agent test policy (PAIMOS Knowledge AEON `agent-test-policy`): name the risk; usually one behaviour test per ticket; barriers and injected clocks instead of sleeps; never loosen or skip a test without a person-approved note on the ticket. Register new tests in the tier manifests (`node scripts/test-tiers/cli.mjs`). Tests never write to absolute local paths (use Playwright `testInfo.outputPath()`). Before hand-over, `node scripts/ci-static.mjs --merge-main` exits 0 (exit 3 means incomplete, not green).
5. **Progress file.** After every meaningful step overwrite `.agent-status.json` in your worktree root (it is git-ignored):
   `{"package":"AEON-123","worker":"<model>","pct":40,"remaining_min":25,"note":"migrations and RLS done"}`
6. **Commits** on your branch only: `AEON-NNN: what changed`. Never commit secrets, `.env` files or generated build output. Do not push unless your brief allows it; the coordinator pushes, opens the PR and merges.
7. **Never** read or print secrets, never touch other worktrees, never run destructive git (`reset --hard`, `clean -f`, `push --force`).
8. When done: `pct` 100, a one-paragraph summary in `note`, and a final commit.
9. **No model calls of your own.** Never call other model CLIs (claude, codex, grok, cursor-agent, pi) and never run review gates yourself. The coordinator runs a read-only gate from another model family on every PR; only High and Medium findings block, and they come back to you as a fix-round brief (Markus, 2026-10-06).
10. **Classic Paimos is retired.** The cutover is done (2026-09-26, AEON-43); its removal is tracked in AEON-261. Never build on classic or bring it back; old pm.barta.cm links resolve through `/from-classic`. pm.augmentoring.com is the business-context instance: only the PMA bump flow run by OPS touches it; never carry credentials or tickets across (Markus, 2026-09-30).
11. **No colored edge accents in the UI.** Never mark selection, emphasis or state with a colored bar or thick border on the left or top edge of a row, card, callout, toast or panel; it is the classic AI-generated UI tell. Use a subtle full tint, a hairline outline or ring, elevation, or type weight instead (Markus, 2026-09-24).
12. **INSPR Flow / Journey is retired** (Markus, 2026-10-05, AEON-723): don't build on, extend or fix journey or stage features; route such tickets to retirement.
13. **Shared files.** New source files get a slice in `scripts/audit/slices.json`. When merging main, the tier manifests merge through the merge driver; on a real conflict take main's version and re-apply your rows with `node scripts/test-tiers/cli.mjs manifests --write`; never commit conflict markers. A change to this file updates `existing_agents_sha256` in `scripts/rules-bootstrap/rollout.json` in the same commit.
14. **Design and release copy.** The design source is the HTML attached to the ticket. Release-note copy (pills, benefits) must be true for the code that ships; use marketing release names; German in impersonal form.
15. **Feature docs.** A new feature gets its own file in `docs/features/`; README.md carries no feature prose.

## Code health (AEON-574)

Rules 1–6 prevent recurring AEON-545 audit findings; rule 7 is the companion layout rule from Markus's refined AEON-541 report. They govern this repo; shared doctrine changes remain proposals until approved and published.

1. **Authorize inside the write.** Re-check the current target's permissions with `RequireTx` inside the final mutation transaction, under the lock that serialises access changes. An earlier transaction or a check before a network read is insufficient. Example: attachment uploads could commit after `attachments.write` was revoked (S1-005, `internal/attachments/module.go`).
2. **One global lock order.** Acquire tenant row → tree → node/record rows → blob locks → event counter last. Take all blob locks in one batch sorted by tenant + hash; never append an event and then acquire another lock in that transaction. Prefer `FOR NO KEY UPDATE` for fences that must allow FK share locks. Example: message sends and webhook workers took target and event-counter locks in opposite orders (S3-001, `internal/inbox/message.go`, `target.go`, `wake.go`).
3. **Bound inputs before work.** Give every request body, list, page, decode, diff and external response explicit size and time bounds before expensive work; list endpoints use bounded keyset pagination. Example: compressed avatar byte limits still allowed 100 million decoded pixels and gigabyte-scale allocation (S1-006, `internal/profile/avatar.go`).
4. **Bind web actions to the record on screen.** Capture the record id and revision when the user acts. Async results, Undo, drafts and confirmations re-check that identity and discard stale responses; reset per-person state on identity changes. Example: confirming deletion of ticket A could delete newly selected ticket B (S8-007, `TicketWorkspace.vue`, `useTicket.ts`).
5. **Report honest results.** A failed or partial write, search, sync or delivery must not report success; expose truncation and partial results. Example: workspace timing controls displayed new values even when their PUT failed (S8-011, `WorkspaceSection.vue`).
6. **Tests prove their claim.** Establish interleavings with barriers and time with injected clocks, rather than sleeps or elapsed-time thresholds; timeouts only guard against hangs. Fixtures retain the data being asserted, and assertions reject the wrong failure reason. Example: event-counter and long-poll tests could pass without their competing operations overlapping (S3-021, `internal/events/events_test.go`, `internal/inbox/inbox_test.go`).
7. **Controls never move.** Prefer a top-anchored frame with controls above content that grows downward; short content stays short, without padded fixed heights. Use a pinned footer and scrolling body for long content or full-height phone sheets. Selectors keep fixed row heights, with details in a reserved slot below; verify control bounding boxes across options and series items. Example: the AEON-541 Decision Desk “keep as” selector and “Decide & next” action shifted as content changed (Markus's layout report; not a numbered AEON-545 finding).

### Coordinator review checklist

Use these checks in every PR gate; workers do not run model reviews themselves (rule 9).

- [ ] Authorization is re-checked with `RequireTx` against the current target under the access-change lock in the final write transaction.
- [ ] Locks follow tenant → tree → rows → sorted blob batch → event counter last, with no later lock acquisition and FK-compatible fences.
- [ ] Size and time bounds precede body/list/page/decode/diff/external-response work; list endpoints use bounded keyset pagination.
- [ ] Actions capture record id/revision; async results, Undo, drafts and confirmations reject stale identity; person changes reset owned state.
- [ ] Failed and partial writes, searches, syncs and deliveries surface errors; truncation and partial results are explicit.
- [ ] Barriers/clocks prove the claimed interleaving/time; fixtures preserve asserted data; assertions cannot pass for a different failure.
- [ ] Bounding-box checks keep actions and selectors still through options/series; downward growth keeps short frames short, without padded fixed heights.
- [ ] UI follows `docs/ui-conventions.md`: shared tokens/components, designed light and dark themes, no coloured edge accents, decorative side frames/glows, gradient blobs or grey-pill soup; controls measured stable (±0.5 px).

## Style

- Go: standard library first, small packages, explicit errors, context everywhere, no global state except in `main`.
- SQL: every table has `tenant_id` (except `tenants`), RLS policy on `current_setting('aeon.tenant_id')`.
- Web: Vue 3 `<script setup lang="ts">`, shared tokens and styles from `web/src/styles/tokens.css` and `web/src/styles/base.css`, SVG icons only (never text glyphs), icons centred in their controls. Reuse existing PAIMOS components before creating new ones; design both light and dark themes. Use hairlines, full tints, whitespace and type weight; no coloured edge accents, decorative side frames/glows, gradient blobs or grey-pill soup.
  Implement the approved Opus design named in the brief; without one, build only the specified UI with existing components and note "needs Opus design" in the summary.
  UI work must also satisfy docs/ui-conventions.md
- UI stability (AEON-541): **The control stays put; content grows away from controls or moves inside a scroll area.** Selecting, hovering, typing or toggling never changes a control's size or position, or its neighbours in the same group. Feedback appears in place, never by pushing controls. Rule 7 governs the choice of layout; fixed heights are not the goal.
  1. For long content, Accept, Decide & next, Skip and Cancel live in a fixed footer; content above uses a scrolling body. Longer content means scrolling, never a moved button.
  2. One control layout and position for a whole series (1 of N). After next/previous, action buttons stay identical and focus stays on the same button. Top-anchored frames may grow downward; a series with an already scrolling body retains the same dialog size.
  3. Selectors and option lists keep fixed row heights. Selection explanations and effects occupy a reserved slot below the list, never between options.
  4. Anything that must grow grows away from the control: below the last control or inside the scroll area. Use rule 7 to choose the layout, then apply the control and selector requirements above.
  5. Phones use a full-height sheet with the action bar pinned at the bottom, including the safe area. No free-floating buttons.
  6. Important dialogs must use the reusable Playwright stability guard: measure named actions, selectors, selector groups and the clicked row before/after every option interaction and series step (±0.5 px). Assert frame height only for pattern B (phone sheets or an already scrolling body); pattern A frames may grow downward. Require at least one interaction and positive-size samples; user scrolling is measured in scroll-container coordinates, and horizontal overflow fails.
  7. Refinement (Markus, 2026-10-02): controls never moving is the goal, not fixed heights. Prefer a dialog anchored at a fixed top position with navigation, choices and actions above content that grows downward. Short content stays short; never pad it to a fixed height just to hold a button still. Use the pinned footer with a scrolling body for long content and phone sheets. Use hairlines, whitespace, typography and a fitting metaphor; do not surround every element with grey rounded rectangles or forced pills.
- Keyboard convention (AEON-541): single-letter shortcuts work only outside text fields. Submit from a field with ⌘↵ on macOS or Ctrl+↵ elsewhere; detect the platform and match the labels. Esc leaves a field, then closes on the next press. Preserve browser and OS shortcuts (⌘S/⌘R/⌘D/⌘P/⌘A and Ctrl+A stay native). Show keycaps on the buttons they trigger.
- Space and text: no fixed px widths for variable text; buttons fit long German labels and stack when needed; no `…` without a way to read the rest; rows are clickable as a whole; phone sheets respect safe areas; touch targets ≥ 44 px.
- Licence: AGPL-3.0-only. SPDX header in new source files.
