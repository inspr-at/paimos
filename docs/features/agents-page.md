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
  Each claim follows its own reads: account readiness needs accounts, quota
  and computers; connectivity needs computers; the pacing summary needs the
  schedule. The chip combines all four sources. Loading uses a same-size
  skeleton; an unreadable source shows a warning and a retry for that part.
  Last-good values carry a clock and “as of” time. Forbidden reads show a lock
  with no values, including after a previously successful read. “No accounts
  yet” requires an authorized successful accounts read; admins can Connect.
  Chip and tile controls keep their size through these states. The inline
  pace edit uses the shared guarded writer and follows the default with null;
  a changed revision is re-read. At the limit follows Settings › Models.

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
