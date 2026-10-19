# Compact chrome on phones and short screens

Phones and short screens give their height to the rows (AEON-1459, design
AEON-1279 round 2, parts D1, D2, D3, D4, D5, D6, D7, D11 and R2.1). The ticket
list's own toolbar and rows follow in AEON-1460.

## One rule

The compact chrome applies at `(max-width: 719px), (max-height: 500px)`: phones
in portrait, phones turned to landscape, and short desktop windows. Short
landscape is `(max-height: 500px) and (min-width: 720px)`. The rule is spelled
once in `web/src/lib/chrome.ts` (`COMPACT_QUERY`, `SHORT_QUERY`) and the same way
in the stylesheets.

## The header

- One opaque 44 px line (`--chrome`, the raised surface, no blur); nothing wraps.
- The places are 44 px icons; the active place is a raised disc inside its
  target. Business shows in landscape and stays in the app menu in portrait.
- Search is a 44 px icon that opens the command palette.
- The theme's quick toggle and the Decision Desk preview's chevron step aside;
  the avatar menu leads with both.
- Under 360 px, Settings and help moves into the avatar menu.
- In portrait the header stays at the top. In short landscape it is the page's
  first line and scrolls away with it, so only the list's toolbar sticks (44 px
  of chrome while reading).

## No footer bar

Compact screens have no footer bar, and the page's scroller never changes size
while scrolling.

- The screen's summary moves into the list toolbar's status slot (the Agents
  list now): the state mark and the count, with the words in its accessible
  name. Short landscape adds the word; under 380 px the slot steps aside.
- The running release moves into the avatar menu: "What's new · release ·
  version", with the count of new releases; it opens the release the page runs.
  The menu ends with the product's name.
- What must not go unseen still shows as one floating line over the page's
  foot: a save that failed (with Try again), and live updates that stopped
  arriving where no status slot says so.

## The project crumb

On a project's pages the header's crumb is the project's key (and its name in
short landscape). It opens the project's sheet: the name and description, the
progress and counts, the sections (Tickets, Knowledge, Delivery, Settings), the
lead, and on the ticket list whether the project head shows above it. A sheet
from the bottom on phones, anchored to the crumb in short landscape; Esc, a tap
outside or a swipe down closes it.

## The Agents page

- **Title row:** `Agents`, the state counts as icons and numbers (still filters,
  words in their names), then ⋯ and New. Wind down leads ⋯ ("Wind down… · Finish
  what runs, start nothing new"); while a wind-down runs its chip stays.
- **Capacity line:** the dial strip and the accounts chip fold into one line,
  "24 agents · 16 running · 4 accounts · 2 computers ›". It opens a sheet with
  the page's own dial control (its units and steppers) and the accounts chip,
  then "The full dial", which unfolds the dial's body on the page.
- **Short landscape:** the title, the counts, the capacity line and the actions
  share one 44 px line.
- **Sessions toolbar:** one opaque 44 px line, `Sessions N` · status · Search ·
  Filter (its icon and the number of filters on) · ⋯. Search opens over the
  line in the same box (a 44 px field with 16 px text) and stays while a query
  is set; Cancel clears it. ⋯ holds Display…, Collapse all or Expand all, Clear
  stale and History. There is no pills row and no column head; Clear all is in
  the Filter menu.
- **Rows:** 56 px in portrait; in short landscape the table keeps Session, Time,
  Execution (the model) and ⋯ in 48 px rows.
- **Held changes:** a mouse or keyboard on the list holds arriving rows (touch
  never does); on compact screens the note is a floating chip, "N changes wait ·
  Show now", and the toolbar keeps its one line.

The list toolbars are opaque at every width; the desktop's geometry is
unchanged.
