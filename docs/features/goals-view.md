<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Projects › Goals

AEON-1457 adds **Goals**, the third Projects view (List · Cards · Goals), built on
the Goals API (`docs/features/goals.md`, AEON-1456) to Tick's approved design
(AEON-1452, decisions 1–10, the r5 progress-over-time addendum and its amendment).
It is the same `ViewSwitch` as List and Cards: the view used last, an explicit
default, and `?view=goals` once.

## What it shows

- **Toolbar.** The filter keeps one width in all three views (from 1420 px it may
  give way to 160 px for the Goals controls, and List and Cards follow, so a switch
  never moves the group chips). After the group chips come the state chips
  All · Moving · Queued · Paused · Done (keys 1–5), Hide done (H) and
  Expand all / Collapse all (E). The chips count items over every visible goal and
  add up; Hide done and the search change the list, never the counts. When the
  server marks the totals partial, the chips and a line on the list say the counts
  omit oversized goals. Below 1420 px
  these controls take the toolbar's second row. Display sorts by ETA, name,
  progress, project, moving, queued or done. Below 1200 px the Moving, Queued
  and Done headers hide, and while one of those sorts the list, Display's
  trigger shows that sort and its direction. Phones hide every header, so there
  the trigger always shows the sort and its direction. The trigger keeps one size
  through every sort, and below 1200 px the same size in List, Cards and Goals,
  so neither a sort nor a view switch moves a control.
- **The workspace status note** (AEON-1494) is one paragraph an agent writes for
  the workspace, between the toolbar and Waiting on you: no box, no tint, its author
  and age on the right ("LEAD · 2 h ago"). At every width it shows one line; a longer
  note ends in "…" and "more", which expands it in place ("less" folds it back to its
  line). Exactly one line is reserved, so a new or shorter note never moves the list;
  only the person's "more" pushes what follows down, never the toolbar. A new note
  starts on one line again. Without a note nothing renders. Only people and keys that
  read the whole workspace see it. Phones keep the byline for the expanded note.
- **Waiting on you** lists the person's live Decision Desk items from the same
  projection as the header badge: the count, at most three first lines
  (key · question · asked by · Open) and Open Desk. Nothing waiting is one quiet line.
  Coming names the person's next upcoming step. For the workspace owner the band also
  shows the questions waiting on sibling instances (AEON-1495, see
  `goals-waiting-instances.md`).
- **Column header.** PROJECT · GOAL · MOVING · QUEUED · DONE · PROGRESS · LANDS. The
  labels are the sort controls; LANDS (ETA, soonest first) is the default and a second
  click turns any order around. The server sorts and filters.
- **A goal row** shows its project keys, kind (goal or release), name (wrapping to two
  lines, the rest in its tip) and "Next" (one line),
  one agent-badge column shared with its items (reserved when no one works), the three
  counts, progress with "N of M done" (the percent is the rollup over the goal's own
  items; a hand-set value always shows a pen and the tip "Progress set by hand"), the waits-on-you
  marker and LANDS, the ETA its agent set. Under the ETA a muted line says the open
  estimate hours, "~40 h left" (the unfinished work leaves of unfinished items, each
  once, rounded to whole hours); the line is reserved while unknown, so nothing moves
  when it arrives, and phones drop it. A goal too large to expand shows those counts
  as unknown.
- **An opened goal** lists what's missing as ordered steps, then its items: Moving,
  Queued (five, then "N more queued · show"), Paused; Done folds into one line, which
  says how many of them finished today in the viewer's time zone ("8 done · 7 today ·
  show", from the `merged_at` the merge records); the "Handed over" line closes the
  agents' part. Items nest under the goal: the state icon sits at the steps' icon x,
  then the ticket key ("+1" when there are more, named in its tip), then the label;
  the project-key column belongs to goal rows, and the fold lines line up the same
  way. An item's live line is its agent's note: a speech icon, one line closed by the
  author and time ("· AEON-LEAD 18:52"), which stay readable; a clipped note or label
  shows in full as a tip (on hover and on keyboard focus). When the counts show and
  the goal column gives the key and label at least 280 px, each item is one line. An
  item with no live line and no agent runs its label on to the ETA column, so it is
  cut only when it does not fit there. With a live line or an agent, the label keeps
  the goal and agent columns (at least 320 px from 1440 px), the agent's badge sits at
  the end of the agent column, and the live line takes the span from Moving to the ETA
  column. When the counts are hidden or that space is under 280 px, the label runs
  from the key to the ETA in at most two lines, and the live line and the agent's
  face share one line under it. The choice is made when the
  goals arrive or the window resizes, never while someone works in the list. A search
  that finds only items opens their goal and shows just those.
- **The ETA popover** (hover or keyboard focus) says who set the ETA and when, and
  what it was before, right-aligned to the ETA and flipping above when there is no
  room. A passed ETA (the server's bit) is gold: "passed · update pending". For a goal
  it adds where the recent pace lands it, "At Ø 0.8 %/h this lands ~19:40" (a date
  past today, in the list's words — "Sat 26 Sep", never "Sept" — and "beyond 30 days"
  past that); it is omitted at 0.05 %/h or less, with
  fewer than three values, or once the goal is done. LANDS stays the agent's ETA. With
  open estimate hours it ends "About 40 agent hours of work left (estimates)", and
  names the open tickets that have no estimate.
- **Progress over time.** The bar is a button: it swaps for a stepped chart in place
  (Enter, click or Esc swap back; focus returns to the bar). The range 24 h · 7 d · 30 d
  is one global setting, the one used last, kept on the server (`goals` preference).
  The y-domain is fitted; arrow keys, hover or a touch drag read a point
  ("13:00 · 20 % (+7) · AEON-1445 changed"). A goal younger than the window draws from
  its creation; with fewer than three values it says "History since 16:40".
- **The signal row** under the bar shows the average pace over the available history,
  "Ø 0.8 %/h · 24 h" (per day for 7 d and 30 d). Under 0.05 %/h, when the stall rule
  does not apply, it says "no change · 24 h" (or "· 7 d" / "· 30 d") in the same muted
  ink. In gold, "no progress for N h" when the server flags a stall (moving items, no
  change for four hours or more); that line wins. Its slot reserves the longest form.
- **Footer.** "N projects with nothing moving: KEY KEY …", then the freshness stamp:
  "Live · ETAs last set 18:41" (the latest ETA an agent set over every goal the
  project/group filters match) while the event stream is up, "Reconnecting · last
  update 19:03" in warn ink once it dropped. Both labels share one fixed slot, so a
  reconnect moves nothing. On phones it is the list's last line. The keyboard line
  follows.
- **Phones** show name, ETA and progress; the state chips, Hide done and Expand all are
  behind ⋯, with a 44 px Expand all row above the first goal. An opened item stacks
  its label, ETA and note (up to two lines) and links
  each of its tickets, with a 44 px target, so an item without an agent still opens
  its work. The same ⋯ menu says when the counts omit oversized goals.

Without goals the view says so; it never shows examples. An item's PR state has no data source yet either, so the agent note is its
only live line.

## Reading

`web/src/lib/goals.ts` reads `GET /api/goals` (100 per page, "Show more goals" for the
rest), `/history` for the ETA popover and `/series` per goal as rows need it, three at a
time. A goal's creation time comes from `GET /api/nodes/{id}` once per visit. Every answer
is bound to the person and the query it was asked for, and an answer with an older
`(read_through, generation)` than the list on screen is dropped. The list is read again
every minute while the view is shown, and at once (one read per burst) when the event
stream (`/api/events/stream?after=latest`, `web/src/lib/goalsLive.ts`) reports a
`goal.*` event, including `goal.status_note_changed`; after a gap the stream cannot
bridge, the list is read again too. The same list carries the status note
(`status_note`) and `eta_last_set_at`. The rate and projection rules live in
`web/src/lib/goalRate.ts` (unit tests: `web/tests/goal-rate.test.ts`); the browser spec is
`web/tests/goals-view.spec.ts`. The AEON-1494 pieces are covered by
`web/tests/goals-gaps.unit.test.ts` and, at 1440, 1024 and 390, `web/tests/goals-gaps.spec.ts`. The Goals keys are registered with the shortcut runtime
(`goals view` scope in `web/src/lib/shortcuts.ts`).

## How lane agents keep Goals current

Every In flight area is a goal in its own project; its lane agent writes progress,
ETAs and notes through the goals CLI (`aeon goal …`), with a stable
`--idempotency-key` per change (reuse it, with the same text, only to retry a lost
response):

- **An ETA** for a goal: `aeon goal update REL-136 --eta "~21:00" --eta-at 2026-10-18T19:00:00Z --idempotency-key lane-r136-eta-1840`.
  For an item, send its ref in a file: `{"items":{"upsert":[{"ref":"gate","eta":{"text":"~21:00"}}]}}`
  with `aeon goal update REL-136 --file - --idempotency-key …`. LANDS and "ETAs last set" follow.
- **An item note**, the item's live line: `aeon goal note REL-136 --item gate "Hand-check of 5 review runs, then the gate goes live" --idempotency-key lane-r136-note-1852`;
  `--clear` removes it. The server records the author and time; repeating the same
  text changes nothing. Notes hold at most 200 characters.
- **The workspace status note** (the lead, with a workspace key):
  `aeon goal note --workspace "Since this morning: 9 items merged …" --idempotency-key lead-status-1700`;
  `aeon goal note --workspace` reads it, `--clear` removes it. At most 2000 characters;
  the next note replaces it.
- **Progress** comes from the work itself; `aeon goal update KEY --progress N` sets it by
  hand (shown with a pen), `--progress clear` returns to the computed value.

`--dry-run` validates and prints the request without writing. Item refs come from
`aeon goal get KEY`.

## New goal and Link work (AEON-1618)

Tick spec v1, decided by Markus on 2026-10-21. For people who may create a node in a
project (or in the workspace), **New goal** ends the Goals toolbar; on phones it is
the first row behind ⋯. Without that right the button is absent.

- **The sheet** (the New project pattern): Title ("What should be true when this is
  done?"), Where (a project the person may write, or Whole workspace for people who
  may write the workspace; fixed while editing) and an optional Lands day, read as
  the end of that day. Create goal closes the sheet. Search and the state filter
  clear so the new goal is on screen, and a goal past the first page is fetched
  and shown, before it opens with **Link work** focused. The toast says "Goal created".
- **Link work** sits at the bottom of an opened goal and in its ⋯. It opens the
  ticket picker in its multi-select mode: search tickets and epics (a project goal
  searches its project), mark several, "Link {n}" (⌘↵ or Ctrl+↵). A ticket already
  on the goal stays marked; choosing it says so and does not change Link {n}.
  On a desktop the button stays above the results and a refusal. On a phone the
  results scroll and the button stays pinned. The toast says "{n} linked" and Undo
  removes only the ids this call added; a link that changes nothing has no Undo.
  Linked items show as the goal's own items; progress,
  Moving · Queued · Done and Lands roll up as for every item.
- **Unlink from goal** is in a linked item's ⋯, with Undo. **Edit goal** reopens
  the sheet filled in; **Delete goal** offers Undo ("Goal deleted") and never touches
  the linked tickets. Releases and epics offer none of these.
- The word "outcome" never reaches people.

### The goal's ⋯ (Tick spec v1.1, part a)

The ⋯ groups what a person can do with a goal: change the goal (**Rename**, **Edit
goal**), work (**Link work…**), for me (Expand or Collapse, progress over time, Copy
link and, for people, Hide for me or Unhide, AEON-1614) and state (**Delete goal**, in
danger ink). People who may not edit the goal see only the "for me" group. On phones the ⋯ sits under the goal's Lands and opens as the
shared bottom sheet.

- **Rename** (F2 on a focused goal row): the name becomes a text field over the same
  box, so neither the row nor its controls move. Enter saves ("Goal renamed", with
  Undo), Esc cancels, an empty name is refused in the field ("A goal needs a name").
  Leaving the field keeps a changed name.
- **Link work…** (L on a focused goal row) opens the ticket picker as above.
- **Delete goal** (Del, or the Mac delete key, on a focused goal row) asks nothing:
  the toast's Undo restores the goal with its links, name and Lands; focus moves on
  to the next goal. Linked tickets are never touched.
- The keys do nothing for people who may not edit the goal, and never while typing.
