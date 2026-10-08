# Models card (Settings › Models)

One default model for all work, a different one only where it matters, reviews that pick themselves, and one line on what runs next. This is the web half of AEON-999 "Models minimal" v3 (build-map package 2); the server half is `models-minimal-server.md`. English only; AEON-998 owns German.

## What people see

- **Default · all work** is a raised tile and always first. Rows under **Except for** are exceptions: a kind of work with its own model. **+ Different model for…** lists existing kinds only (from Settings › Kinds of work, plus Concepts) and starts a row with the default's model and level and the picker open. Enter keeps it; Esc or a click elsewhere takes the row away again. Nothing is stored until a pick is made.
- **One combined picker per row.** Every model in the registry, grouped by harness, each with its own thinking levels in the model's native names (`low · medium · high · xhigh`, `off`, `default`, …). ↑↓ choose the model, ←→ its level, Enter picks both, typing filters, clicking a level picks both. Nothing is filtered out by what a model can do: a quiet line names what it cannot do here, and the row says what runs instead.
- **For everyone · Just me** (admins; they open on Just me). Members only have their own choices. A person's own row reads "yours · reset"; a row only that person has shows ×.
- **Lock.** In For everyone, the picker's "Members can't change this" switch pins the pick to the top of its column as a workspace rule with the generated reason "Locked in Settings › Models by {name}". Members and admins on Just me see a locked pick as plain text with a lock; hover or focus says who, when, and the reason where a person wrote one. Changing a locked pick for everyone moves the pin with it.
- **Reviews** read "Automatic · always another family" with a link to Policies. A new model shows as one line, "X is new · Use it for…", with Not now. The last line says what runs next for the first queued ticket; **Why?** opens a numbered trace with every skip and its reason (`?why=1` opens it).
- **Saved · Undo** for ten seconds. A refused or half-finished write says so and is never shown as saved.

## Data

The page reads `GET /model-preferences/simple?for=me|default` (picks, locks, can't-run lines, new models, next, revisions) beside `GET /model-preferences/board` (the stored order behind each pick and what a column cannot do), `GET /models` (picker entries) and, for people who may read the member list, `GET /members` (lock authors).

- A pick writes the column's first-build order with the pick first and the rest of the stored order unchanged (`PUT /model-preferences/orders/{column}/first`), then its level (`…/first/thinking` with `effort`) when the order did not carry it. Personal writes carry `If-Prefs-Person`. × and reset delete the order (`DELETE …?revision=`), so the kind follows the default again.
- The lock is `PUT /model-rules/workspace/{column}` with the rules revision the page read; not now is `POST /model-preferences/tray/{line}/dismiss`.
- Every write is bound to the person, scope and revision on screen. A person or scope that changes mid-way ends the change; a 409 refreshes and says the change was not saved; Undo only runs against the revisions its change ended on.

Stored ranked orders beyond the first pick, situations, templates, usage, providers, residency and other rule kinds stay in the database untouched and keep driving the automatic fallback; the page does not show them (expert mode, parked).

## Removed from the live page

The AEON-878/879 board UI (view switch, templates, Thinking and Usage switches, setup assistant and welcome banner, next-run menus, side panel, Show/Project/Providers menus, situations, drag and move, other rule kinds, hide column, new tray, coverage, proof and run evidence, the full-screen board route). `/settings/models/board` redirects to the page; the query layer, situation, project, mode, kind and ticket are ignored. Endpoints and stored data are unchanged.

The catalog freshness settings stay one fold away (**Model catalog**, opened by `#model-refresh`) until the model registry card (package 3) replaces them.

## Verification

`web/tests/models-simple.unit.test.ts` (entries, native levels, rows, locks, next and Why?, the writes with Undo and refusals), `web/tests/models-simple.spec.ts` (default and exceptions, picker by harness, keyboard, For everyone and Just me, lock, member, can't run, new model, failures, accessibility, and the AEON-541 stability guard at ±0.5 px on desktop and phone) and `web/tests/models-simple-shots.spec.ts` (1440 and 400 px, light and dark).
