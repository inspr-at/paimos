# The Decision Desk in the header

The header's places read **Projects · Agents · Business · Decision Desk**
(AEON-1214, package P6 of the Agents page design AEON-1196). Without Business
they read Projects · Agents · Decision Desk. The keys stay the same: g p, g a,
g b and g d.

## The split place

For people, the Decision Desk is a split place with two halves in one segment:

- **The name** goes to `/decision-desk`. It carries the open count from the
  canonical Desk projection, as before. Its tooltip names the count and g d.
- **The chevron** opens a preview of the Desk, on every page that shows the
  header. Its tooltip, Preview, hides while the preview is open.

Agents signed in to the web app keep the plain Desk place, because the Desk
projection belongs to people.

## The preview

The preview is the former Desk panel from the Agents page, moved to the header.
It reads the same projection as the header count. Opening it also reads the
project list if it is not loaded yet, and looks up the project of a rule change.

- The head says how many things wait for you, or that nothing does, or that the
  count is unavailable. On wide screens it also holds **Open the Decision
  Desk**.
- Rows waiting on you come first, then the ones the agents carry on without.
  Each row names its project (Workspace for workspace-wide items), its kind and
  its age. The whole row opens that item on the Desk, as before.
- Long requests wrap within their row and never run under its Open.
- The preview shows up to twelve rows. When the Desk holds more, it says how many
  of how many it shows. **Open the Decision Desk** leads to all of them.
- A failed read says so and offers Retry. Nothing is ever shown as zero when it
  could not be read.

The preview overlays the page and moves nothing: the places, search, gear and
avatar keep their boxes when it opens and closes. Its own controls stay put too:
while the Desk is read, fails, is read again or a refresh adds a row, only the
rows change. They grow downward below the head, or scroll. It opens from the
chevron under the header and stays inside the window. Keyboard focus moves to
its first row; Escape closes it and returns focus to the chevron, and the down
arrow on the chevron opens it.

## Tablets

The split place is wider than a plain place. On a project's tickets, where it
shares the row with Business and the density switch, the header still never
runs past the window: the breadcrumb gives way first, then the search pill. The
pill drops its keys, then its label, and keeps its magnifier. Just above phone
width the gaps between the header's controls tighten.

## Phones

On a phone each half is a 44 px target. The preview is a full-height sheet with
44 px rows that scroll between its head and its foot. **Open the Decision Desk**
is pinned at the foot, clear of the safe area. The phone header has room for a
limited number of 44 px buttons. When a ticket key would otherwise be cut, the
chevron steps aside first, then the moon. They come back in reverse order as
soon as the room holds them. This is measured, never guessed. While the chevron
is away, the Desk itself is one tap away on its place. The avatar sheet then
lists **Preview the Decision Desk** after Personal settings, a 44 px entry that
opens the same preview. Escape returns focus to the avatar. At 390 px the home
page shows the chevron; at 375 px the moon keeps its place and the chevron steps
aside.