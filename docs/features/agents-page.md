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
  neighbours. Over pace and today's limit show as a flag after the number. The
  unfolded dial opens as a card under the row (see
  [agents-daily-plan.md](agents-daily-plan.md)).
- The accounts chip names the accounts and computers and flags what needs a
  person (sign-ins first). Its popover keeps the status line, Away with Back
  now, and the first thing that blocks agents with its one fix command and
  Verify again where the person may verify. Three tiles open Settings ›
  Accounts, Computers and Capacity, each with its own state words. The Needs-you
  list and the sign-ins table live in those Settings parts.

**Sessions card head.** The card's head carries the list's own tools: the
title with the count of sessions on show, Search sessions (it filters this list;
the app header searches everything), Filter (by state, the same filter as the
head counts) and Display (the sort and Default order), then Clear stale and
History. Applied filters show as pills under the head. The head, the pills and
the column heads stick inside the card under the app header; sticking adds a
hairline shadow only, so no box changes. `?q=` carries the search.

**Keys.** F or / focuses the search, Shift F opens the filter, Esc clears the
search and then leaves the field. Plain keys never fire inside a text field.
One control height applies to the head, the capacity row and the card head:
36 px, 44 px on touch and on phones.
