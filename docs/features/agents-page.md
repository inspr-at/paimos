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
  [agents-daily-plan.md](agents-daily-plan.md)). A page opened on a phone or a
  short landscape screen starts with the dial folded, so the sessions come
  first; its toggle unfolds it for the visit, and the saved fold, shared across
  devices, belongs to wider screens (AEON-1286).
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
hairline shadow only, so no box changes. `?q=` carries the search. On short
landscape screens the tools stay on the title's line as icons and the column
heads are left out, as on phones, so the rows get the height (AEON-1286).

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

Interrupt, Pause (Resume when paused) and Stop live in the panel's ⋯ menu with
their labels and reasons (AEON-1286). The tabs are the panel's view switch
(AEON-1453, [View switches](view-switch.md)): a lead panel's default tab is set
by right-clicking, holding or pressing Shift+F10 on Lead, Details or Chat, and
carries the small pin in the slot those tabs reserve. On a phone, ⋯ also offers
it as a labelled choice for the tab on screen, such as "Open on Chat by
default", which sets the same default. On Events or Terminal it names the
current default; clearing it there keeps the item in place until ⋯ closes, so it
can be ticked again. Lead start, resume and cooperative handover use the caller-specific
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
Compact rows keep the name and time together; opening a worker shows the
remaining details. Model names come from the matching harness registry, with
an explicit unavailable label when display metadata cannot be read.

Before every start displays the last admission report, with its timestamp and
one row per reported gate. It never promises current availability. An absent
report says No start yet, unreadable history offers Try again without check
marks, and reports older than the session or 24 hours are labelled stale.

## The split, the fixed preset dock and keys (AEON-1213, P5)

The divider sets the list's width continuously: 280 px minimum, compact rows
below 560 px, table rows from 560 px. Releasing with less than 380 px left for
details closes them and retains the last valid handle. The handle is stored in
the person's `agents:layout.handle` preference; other fields in that preference
are preserved, including when the first preference read is still pending. A
failed read keeps the chosen handle local; the next change retries the read
and saves only after shared fields can be merged. A person change closes
details and cancels the old person's drag
and queued preference write. A failed save says the layout lasts for this visit.

One sticky button stays at the work area's top-right corner. Both heads reserve
its space. It jumps through Focus (list 320 px), Overview (details 480 px) and
List only; its icon follows the current list form and its tooltip names the next
preset. Presets take 240 ms, or no animation with reduced motion. Selecting from
List only reopens the remembered handle, with Overview as the first default.
Escape and the close button return directly to List only.

A split fits at 1056 px of work area (560 + 16 + 480). Below that threshold,
details open over the list as a sheet and the preset button hides. Phones have a
full-height sheet: Back, the avatar, the name over its state and model, and ⋯,
then the menu row (AEON-1286). The state is said once, on that line. ⋯ opens as a
sheet from the bottom edge in groups: control, the session's process, how the
panel opens, requested changes, layout. Short landscape screens get the whole
screen too, with head and menu on one line; a phone turned sideways keeps its
head and puts the menu on that line, as icons when the title needs the room. In Chat the composer pads the home
indicator itself, so no band is left under it. Ticket keys and code in chat text
sit in the line like words, so a line holding one is as tall as any other, and
a key's link is tapped on its own line only. In a split,
the work area comes directly under the app header; the list and details have
viewport-high panes with separate scrolling. A new selection resets the details
to their top while the picked row stays in view. Opening a sheet, including when
a smaller window replaces the split, also starts its body at the top.

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

**Two lines per row.** Line 1 is the name only: the ticket key opens it
("PHAROS-12 · Restore the backups"), and the project's claimed lead adds Lead. The session's own
label is in the name's tooltip; its computer has its own column, never line 1.
Line 2 says what it works on, what it asks or how it ended. A state change
changes the badge, line 2 and the time, never the row's height.

**The columns.** Session · Time · Execution · Computer · Beat, as the design
has them. Execution names the model the way Settings › Models does, with its
level ("Opus 5.5 · xhigh"), and the account under it ("Claude Max · markus");
a model the registry does not list still reads as words, never as its raw id.
Search sessions finds a row by that name, and still by the raw id.
The ticket has no column (its key opens the name; Display › Sort still orders
by it). The tier, the estimate and whether the session listens or has an inbox
live in the details, in words; the tier there keeps its one-step Left/Right
arrow keys.

**Time.** The Time column sits beside the name: running time while a session
runs ("1 h 12 min"), how long ago it ended ("12 min ago"), or since when it is
paused ("since 09:58"). Sorting by Time orders by what the column shows.

**Columns.** Session, Time, Execution, Computer and Beat. The ticket key is
part of the session name in every form; Display still offers sorting by Ticket.
Service tier and its keyboard controls live in the details panel, with Change
tier also available through the row menu. Ticket estimates are shown in the
details.

**The list's width decides its form.** From 880 px the full table; from 560 px
a table without Computer and the account line, with the ticket key before the name
(the details name and rename the computer); under 560 px (phones) compact rows.
A compact row opens line 1 with a 12 px harness mark whose tooltip and name say
the model, level, harness and account ("GPT-6.1 Sol · xhigh · Codex · ChatGPT
Pro"), ends line 1 with the short time ("42m", "3m ago"), and reserves one slot
for ⋯ beside the two lines. Its trigger appears on hover or focus and stays visible
on touch. Remove stale session, Pause, Resume and Change tier are in its menu;
table rows retain the one-tap bin for ended or silent sessions. The lead's second
line summarizes its workers; the table omits the extra sub-agent count. The
details name and rename the computer; a failed service tier read explains what
failed and offers Try again.

**Rows enter and leave calmly.** New sessions, rows leaving the list, rows
moving to another group or parent, rows that start or stop matching the filter
or search (a model renamed in Settings › Models included), and new orders wait while the pointer is on the list, a row has
keyboard focus or one of the list's menus is open; the last row stays even when
the live list empties. Badges, line 2, the counts and times keep updating
meanwhile. The card head then says "2 changes wait while you point here · Show
now"; otherwise, quietly, "Live · updated 4 s ago". The changes apply 1.5 s
after the pointer leaves, on Show now, or when the page loads. Your own
removals and moves apply at once, and so do a new sort order, filter or search.
Only a mouse or a pen points: on a touch screen the changes wait while a
finger is down and its scroll still runs, momentum included, and apply 1.5 s
after the scroll settles, without Show now. Whenever changes apply, the first
rows on screen keep their place, on phones too (Safari does not keep them in
place by itself). On phones and short landscape screens the footer folds away
over the page while reading down; the page itself never changes size while it
scrolls.
A done or ended session keeps its place among its siblings and its end state
for 10 minutes, then moves to Ended at the next allowed moment. Line 2 says how
and when it ended and when it moves: "Finished 11:57 · moves to Ended at 12:07"
for Done only; any other end names its cause ("Cancelled", "Budget exhausted",
"Lost contact"), or "Stopped" when the reason is withheld.

## How sessions ended, the claimed lead, the live account and retries (AEON-1285)

**How it ended, in words.** No row and no details show a reported stop code. A
known reason reads as a label ("Process failed", "Cancelled", "Lost contact")
and, on line 2, as a short sentence with what to do ("The worker process failed
· Retry or check the ticket"). Any other reason reads "Stopped"; a failure among
them reads "The session reported a failure · Retry or check the ticket", without
repeating what the reporter wrote.

**Lead means the lead claim.** Only the session the project's lead names (the
harness lead claim) shows Lead on its row and in its spoken name. A coordinator
without the claim shows no badge; its details call it a coordinator.

**The account in use.** The heartbeat reports the account the harness is signed
in to now, so a session whose harness switched accounts shows the current one.
For Claude Code the heartbeat asks `claude auth status --json` at most every
10 minutes and sends only the login's email, never a credential. Other
harnesses, API-key logins and failed probes keep the label given at launch;
`AEON_ACCOUNT_PROBE=off` always keeps it.

**Retried attempts fold away.** A failed session with a newer attempt in its
run family (the same ticket, owner and role, and the same label but for its
trailing round: "-r2" and "-r3", "-fix4" and "-fix5", "-2" and "-3") reads
Ended, says "Retried as … · Process failed" and moves to Ended like any ended
row, after its 10 minutes. The list works this out when it reads the sessions,
so failures from before this release fold too; nothing is written. Its details
keep its history and link to the retry. A failure nobody retried stays a
problem, with its bin.
