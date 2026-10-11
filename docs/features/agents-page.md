# The Agents page

The Agents page (`/agents`) is built from the approved design AEON-1196 in
packages (epic AEON-1208). This file describes what has shipped; each package
adds its part.

## Head, capacity row and the Sessions card head (AEON-1209, P1)

**Head.** One line: `Agents`, the state counts (working, waiting, problems;
throttled, pausing and paused only while there are any; each count filters
Sessions), then Wind down, More and New, with New last. The Decision Desk is a
place in the app header, so the Agents page no longer carries its own Desk panel
or a More item for it.

**Capacity row.** Under the head, scrolling with the page: what may run.

- The folded dial is one light control: the total and one unit per harness,
  bound by proximity (mark and number 4 px apart, a faint hairline between
  units), with no outline and no fill. Each unit's − and + keep their room and
  show on hover or focus, so nothing moves. In a row under 900 px wide only the
  units show; hover, focus or a tap lifts that unit onto a plate above its
  neighbours. At the edge of a line, where one end has no room, − and + both
  sit on the side that has it. When the row cannot hold every unit (many
  harnesses, a tablet), the units wrap onto further lines inside the control
  instead of being cut off. Over pace and today's limit show as a flag after
  the number. The unfolded dial opens as a card under the row (see
  [agents-daily-plan.md](agents-daily-plan.md)).
- On touch, −, the number and + are three separate 44 px targets that never
  overlap: − and + are 44 px wide, and the number's target takes in its mark.
  In a tight row a closed unit is one target, and a tap opens its plate.
- The accounts chip names the accounts and computers and flags what needs a
  person (sign-ins first). Its popover opens with three tiles into Settings ›
  Accounts, Computers and Capacity, each with its own state words; below them,
  where text may grow without moving a control, come Away with Back now, the
  first thing that blocks agents with its one fix command and Verify again
  where the person may verify, and the status line. The Needs-you list and the
  sign-ins table live in those Settings parts.

**Sessions card head.** The card's head carries the list's own tools: the
title with the count of sessions on show, Search sessions (it filters this list;
the app header searches everything), Filter (by state, the same filter as the
head counts) and Display (the sort and Default order), then Clear stale and
History. Applied filters show as pills under the head. The head, the pills and
the column heads stick inside the card under the app header; sticking adds a
hairline shadow only, so no box changes. `?q=` carries the search.

**Keys.** F or / focuses the search, Shift F opens the filter, Esc clears the
search and then leaves the field. Plain keys never fire inside a text field.
Enter, p and r act only on a session the list shows (or the one open in the
panel); when a search or filter hides the session under the cursor, the cursor
moves to the first match, or to none.
One control height applies to the head, the capacity row and the card head:
36 px, 44 px on touch and on phones.

## The split, the fixed preset dock and keys (AEON-1213, P5)

The divider sets the list's width continuously: 280 px minimum, compact rows
below 560 px, table rows from 560 px. Releasing with less than 380 px left for
details closes them and retains the last valid handle. The handle is stored in
the person's `agents:layout.handle` preference; other fields in that preference
are preserved, including when the first preference read is still pending. A
person change closes details and cancels the old person's drag
and queued preference write. A failed save says the layout lasts for this visit.

One sticky button stays at the work area's top-right corner. Both heads reserve
its space. It jumps through Focus (list 320 px), Overview (details 480 px) and
List only; its icon follows the current list form and its tooltip names the next
preset. Presets take 240 ms, or no animation with reduced motion. Selecting from
List only reopens the remembered handle, with Overview as the first default.
Escape and the close button return directly to List only.

A split fits at 1056 px of work area (560 + 16 + 480). Below that threshold,
details open over the list as a sheet and the preset button hides. Phones have a
full-height sheet with a 44 px Back control and safe-area padding. In a split,
the work area comes directly under the app header; the list and details have
viewport-high panes with separate scrolling. A new selection resets the details
to their top while the picked row stays in view.

F and / (including numpad slash) search this list; S jumps the presets when they
fit. These actions, their hints and the focused divider's Left/Right (16 px),
Home (list 280 px) and End (details 380 px) use the shared shortcut registry.
Fields and browser shortcuts keep their native behavior. The app header leaves
slash to `/agents` and its session routes alongside the header's Desk navigation.

## State in the avatar, compact rows and calm enter/leave (AEON-1211, P3)

**The avatar carries the state.** Each session row shows the person's live
indicator with its state badge and nothing else on it (the harness badge is
gone). The state word is the avatar's tooltip and part of its accessible name
("Lead AEON lead: Needs something"); a more precise word, such as "Stop
requested" or "Lost contact", follows it in the tooltip and opens line 2. The
State column is gone; Display › Sort still orders by state.

**Two lines per row.** Line 1 names the session (its result, then its own name,
and Lead); line 2 says what it works on, what it asks or how it ended. A state
change changes the badge, line 2 and the time, never the row's height.

**Time.** The Time column sits beside the name: running time while a session
runs ("1 h 12 min"), how long ago it ended ("12 min ago"), or since when it is
paused ("since 09:58"). Sorting by Time orders by what the column shows.

**The list's width decides its form.** From 880 px the full table; from 560 px
a table without Host and the account line, with the ticket key before the name
(the details name and rename the computer); under 560 px (phones) compact rows.
A compact row opens line 1 with a 12 px harness mark whose tooltip and name say
the model, effort, harness and account ("gpt-6.1-sol · xhigh · Codex · ChatGPT
Pro"), ends line 1 with the short time ("42m", "3m ago"), and keeps ⋯ beside
the two lines (an ended or silent row also keeps its one-tap bin): Pause,
Resume and Change tier are in its menu, and the details name and rename the
computer.

**Rows enter and leave calmly.** New sessions, rows leaving the list, rows
moving to another group or parent, rows that start or stop matching the filter
or search, and new orders wait while the pointer is on the list, a row has
keyboard focus or one of the list's menus is open; the last row stays even when
the live list empties. Badges, line 2, the counts and times keep updating
meanwhile. The card head then says "2 changes wait while you point here · Show
now"; otherwise, quietly, "Live · updated 4 s ago". The changes apply 1.5 s
after the pointer leaves, on Show now, or when the page loads. Your own
removals and moves apply at once, and so do a new sort order, filter or search.
A done or ended session keeps its place among its siblings and its end state
for 10 minutes (line 2 says when it moves to Ended), then moves to Ended at the
next allowed moment.
