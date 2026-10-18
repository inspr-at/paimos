# The Decision Desk count in the browser tab

When something waits on the Decision Desk, the browser tab says so (AEON-1473,
design decisions by Tick). The tab shows the same count as the header's Desk
badge: both read the one Desk projection store, never a second source.

## The title

Every page title leads with the count in parentheses, the browser convention:
**(3) Decision Desk · PAIMOS**. Above nine it reads **(9+)**; at zero there is
no prefix. While the store reads the Desk again, the tab keeps the count it had.
When a read fails, the tab shows no count rather than an old one.

## The icon

The tab's icon is drawn again on a canvas with a small amber dot at its
bottom-right corner, in the header badge's amber. A thin gap separates the dot
from the icon, and a hairline edge keeps it visible on light tab strips. On a
screen with 24 or more device pixels per tab icon (a Retina screen draws 32) the
dot carries the digit, or **9+**; on a plain screen (16 pixels) it is a plain
dot. Pinned and narrow tabs show only the icon, so the dot alone carries the
signal there. At zero the plain icon comes back unchanged.

## Motion, once

Nothing loops or blinks. When the count rises:

- In the visible tab, the dot pops once: it grows to 1.2 times its size and back
  in six frames, about 600 ms.
- In a background tab the title reads **New decision** for two seconds, then
  settles on the count. Bringing the tab forward settles it at once. Tick's
  decision 3 wrote a leading bullet; a tab title cannot hold an SVG icon, and
  the glyph guard rejects that bullet, so the notice is the words alone.

A first count, a falling count or the same count read again moves nothing. With
reduced motion only the number changes: no pop and no notice. The tab never
takes focus and never plays a sound.

## Who sees it

Only people. Signed out, or signed in as an agent principal, the tab has no
prefix and the plain icon.

## Keeping it current

The header reads the Desk every 30 seconds while the tab is visible. A
background tab, or a page that folds the header away, has no such read, so the
tab reads the same store again once its last answer, or its last try, is a
minute old. While the header's reads succeed, this adds no requests.
