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
  trigger shows that sort and its direction.
- **Waiting on you** lists the person's live Decision Desk items from the same
  projection as the header badge: the count, at most three first lines
  (project key · question · goal, or the project when the question is not attached
  to a goal) and Open Desk. Nothing waiting is one quiet line.
- **Column header.** PROJECT · GOAL · MOVING · QUEUED · DONE · PROGRESS · LANDS. The
  labels are the sort controls; LANDS (ETA, soonest first) is the default and a second
  click turns any order around. The server sorts and filters.
- **A goal row** shows its project keys, kind (goal or release), name and "Next",
  one agent-badge column shared with its items (reserved when no one works), the three
  counts, progress with "N of M done" (a pen marks a hand-set value), the waits-on-you
  marker and LANDS, the ETA its agent set. A goal too large to expand shows those
  counts as unknown.
- **An opened goal** lists what's missing as ordered steps, then its items: Moving,
  Queued (five, then "N more queued · show"), Paused; Done folds into one line; the
  "Handed over" line closes the agents' part. A search that finds only items opens
  their goal and shows just those.
- **The ETA popover** (hover or keyboard focus) says who set the ETA and when, and
  what it was before, right-aligned to the ETA and flipping above when there is no
  room. A passed ETA (the server's bit) is gold: "passed · update pending". For a goal
  it adds where the recent pace lands it, "At Ø 0.8 %/h this lands ~19:40" (a date
  past today, in the list's words — "Sat 26 Sep", never "Sept" — and "beyond 30 days"
  past that); it is omitted at 0.05 %/h or less, with
  fewer than three values, or once the goal is done. LANDS stays the agent's ETA.
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
- **Footer.** "N projects with nothing moving: KEY KEY …" and the keyboard line.
- **Phones** show name, ETA and progress; the state chips, Hide done and Expand all are
  behind ⋯, with a 44 px Expand all row above the first goal. An opened item links
  each of its tickets, with a 44 px target, so an item without an agent still opens
  its work. The same ⋯ menu says when the counts omit oversized goals.

Without goals the view says so; it never shows examples. The Coming line of the design
has no data source in Aeon yet and is not shown.

## Reading

`web/src/lib/goals.ts` reads `GET /api/goals` (100 per page, "Show more goals" for the
rest), `/history` for the ETA popover and `/series` per goal as rows need it, three at a
time. A goal's creation time comes from `GET /api/nodes/{id}` once per visit. Every answer
is bound to the person and the query it was asked for, and an answer with an older
`(read_through, generation)` than the list on screen is dropped. The list is read again
every minute while the view is shown. The rate and projection rules live in
`web/src/lib/goalRate.ts` (unit tests: `web/tests/goal-rate.test.ts`); the browser spec is
`web/tests/goals-view.spec.ts`. The Goals keys are registered with the shortcut runtime
(`goals view` scope in `web/src/lib/shortcuts.ts`).
