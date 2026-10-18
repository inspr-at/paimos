# Settings › Computers

Settings › Computers answers one question: where do agents run? It is one table
with a row per paired computer and a column per account (AEON-1174, design
AEON-1149). It replaces the earlier sign-ins grid and computer list.

## The table

- **Computer column.** The computer's name, then how many agents run there
  ("4 of 8 agents running") and why new starts wait, if they do. An offline
  computer says when it was last seen and is drawn dimmer. The row opens the
  computer's panel.
- **Account columns.** The vendor and a short name; an address is shortened to
  the part before the @ and the whole address is in the tooltip. The column head
  opens the account's panel.
- **Cells.** One word for the account's sign-in on that computer: **Ready**,
  **Expired** (verification ran out), **Offline**, **Draining** or **Blocked**
  (the computer was removed, the sign-in was signed out, or it cannot start
  agents until someone acts). A check that is still running says **Checking**
  and a sign-in the computer has not reported says **Not reported**; neither is
  ever called Ready. The tooltip carries the full status, and a cell opens the
  computer's panel at that sign-in.
- **Past six accounts**, and whenever the table is wider than the window, the
  computer column stays in view while the table scrolls sideways.
- **On a phone** each computer is a stack of rows: the computer, one row per
  sign-in with its account named, then New starts. Nothing scrolls sideways.
  From 1200 px the docked panel narrows the table instead of covering it; the
  table never restacks when a panel opens.

## New starts

New starts decides when a computer takes on another agent. Only new starts
wait; running agents are never stopped.

- The **chip** on each row shows the mode: Smart, Fixed · load N or Off. It
  opens three choices, each saying what it does with that computer's own
  options. Choosing one saves at once and the toast offers Undo.
- The chip uses the panel's write, `PUT /api/agent-pairing/computers/{id}/capacity`,
  with the full policy as last reported and only the mode changed, plus the
  reviewed `expected_revision`. Maximum agents, maximum load and the Smart
  options stay as they were. Without a reported policy the chip only reads.
- **Undo** writes the earlier policy back at the revision the save produced. If
  the computer changed in the meantime the server refuses (409) and nothing is
  overwritten; the toast says so and the chip shows the stored setting.
- Only the computer's owner with permission to manage accounts can change it,
  and only on a connected computer that reports; everyone else sees the words.
- The computer panel's section is called **New starts** (formerly "Capacity and
  load") and is open by default. It holds every option: maximum agents, the
  load limit and the Smart conditions.
