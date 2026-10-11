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

## Sessions grouped by project (AEON-1210, P2)

**Groups.** Each project is one group, parted from the next by a hairline. The
groups keep a fixed order: the person's own order of projects (their Custom
sort on Projects), then the rest by key; activity never moves a group. The
lead row heads its group, its workers nested below in start order, then the
project's other sessions. Only the session the lead currently names counts
as the lead: an ended generation keeps its place under the current lead and
never hides what the lead does now. A lead without a running session on the
list (starting, waiting for room, paused, can't start) still heads its group
from the project's lead state; its line says why it waits, and only a full
dial says the dial is full. A project with queued work and no lead is one slim line
with Start a lead, or the reason it cannot start. "No project" and "Ended"
come at the end. An ended family keeps its place and end state for 10
minutes, then moves to Ended.

**Queue chip.** A lead with queued work shows how much on its title line
(on the state line in compact rows); the chip opens the lead panel at Next
up. One chip per project, on the current lead. The lead row's ⋯ also offers
the lead panel and Next up.

**Folds.** A lead's fold is the person's own, on every device (the
`agents:layout` preference, `folded_projects`). Folded, the lead's line sums
up what it holds: "5 workers · 1 problem · 1 question". Collapse all /
Expand all beside the title writes the same preference; Left and Right
arrows on a lead row fold and unfold it.

**Filter and Display.** Filter offers State (the head counts' filter),
Project, Harness and Computer; each applied filter is a pill under the card
head that reopens its choices, × removes it, Clear all ends them all.
Display groups by Project (the default), Lead, Harness, State, Computer or
None, above the sort. `?group=`, `?project=`, `?harness=` and `?computer=`
carry them; the values are bounded.

**One projection.** The tree, the New menu and the lead band and panel read
one shared projection (`stores/agentProjection.ts`): each project's lead, the
queued work, the open questions and the session tree. Workers belong to a
lead by their real parent session; a question belongs to the exact session
that asked, so another generation's ask stays on the Decision Desk. Reads
are bounded and say when they failed or were cut short: a queue that can't
be read leaves a line under the list instead of silently dropping chips and
no-lead lines, a queue read in part shows its counts as lower bounds ("4+"),
and when not every open question could be read the list points to the
Decision Desk. The lead panel and band show a worker count read from a
session list cut short as "3+". The lead, decision, queue, question and
reparent events wake a batched reread of what changed; a reconnect and the
30-second reread refresh everything, including the Next up list of an open
lead panel or band.

## One lead panel (AEON-1212, P4)

Leads have Lead, Session and Chat in one panel. An explicit tab link wins;
otherwise a question that can be decided, a held start, an ended session or a
worker problem opens the relevant tab, ahead of the person's pinned default
and last-used tab. The opening reason stays visible. Workers have Overview
and Chat. Defaults and last-used tabs are person preferences.

Interrupt, Pause (Resume when paused) and Stop remain in the panel head on
every tab. Lead start, resume and cooperative handover use the caller-specific
lead action projection, including its blocked reason; a missing projection
leaves those actions unavailable. Session controls retain their own ownership
and permission checks. A stopped lead's Start a lead is in Right now.

The queue chip and lead menu link to `/agents/:sessionId?tab=lead#next-up`.
Only the panel body scrolls to Next up. The Lead tab's Answer in Chat opens
the exact question in Chat. Its real server options use the Decision Desk's
revision-checked decision adapter and an idempotent request id. Results replace
the option row in place: pending, recorded, failed with Retry, or answered on
the Desk. More than four options show the first three and a Desk link; without
decision permission the card explains who may decide. A plain chat message
never decides or closes a question. Selection and identity changes discard
late results.

The panel's own worker rows use Session, Time, Execution, Computer and Beat.
Compact rows keep the name and time together; the Session tab carries the
remaining details. Model names come from the matching harness registry, with
an explicit unavailable label when display metadata cannot be read.

Before every start displays the last admission report, with its timestamp and
one row per reported gate. It never promises current availability. An absent
report says No start yet, unreadable history offers Try again without check
marks, and reports older than the session or 24 hours are labelled stale.
