# Delivery page

Each project has a **Delivery** section (`/p/KEY/delivery`, between Knowledge and
Settings) that shows how fast changes move from pull request to live, measured
against the Project Arion targets. The section is absent, not disabled, for people
without `delivery.read` on the project; such an address opens the project's tickets.

The page head carries **Numbers | Flow** (`?view=flow`), the window
**7 · 30 · 90 · 180 · 365 days** and **Simple | Expert**. Window and level are the
person's own: they are saved server-side as the preference `delivery:numbers`
(`{ "level": "simple" | "expert", "window": 7 | 30 | 90 | 180 | 365 }`) and default
to Simple and 7 days. Switching never moves a control: the controls sit in the
top-anchored head, the window switch keeps its slot (hidden) on Flow, and content
below grows downward.

**Numbers · Expert** reads `GET /api/projects/{projectId}/delivery/metrics` once and
refreshes it every minute while the page is visible. Ten tiles show, for the chosen
window, the value (p50 for durations, the mean for queue runs, the share for rates),
p50/p90 or the number's own detail, the count, the change against the same number of
days just before (steady within 10 %, or 3 points for shares; no comparison when that
window has no data), the Arion target and the source. Numbers 3 and 7 combine two
metrics (green on first try with flake-only failures; review time with the share of
"changes" verdicts); merge rounds show scripted of all rounds, the nightly run green
nights of nights with a run. A tile is **Partial** when its window is partial or not
fully covered, and names the coverage ("covers 4 of 7 days").

Ten trend charts use one point per day (7 and 30 days), per week (90 and 180) or per
month (365) from the contract's daily points and series. Days before a source covers
them are hatched "no data yet"; covered days without samples stay empty ("nothing
recorded"); partial buckets are tinted, with hollow points and dashed lines. When p90
would squash the chart, the band fades out at the top and the footer says how far p90
reaches. Hover or arrow keys (Home/End) move one selection; the readout line above
the plot names the bucket. The nightly run is a strip of squares: a tick for green, a
cross for red, an outline for a night without a run.

States replace content in place: skeletons while loading, a banner with **Retry** when
the read fails (no old numbers stay on screen), and a plain sentence when no repository
is linked yet or the linked repository has no data. Words follow the person's profile
language (English or German).

**Numbers · Simple** is the default level and reads the same answer. A summary card
says how many of the numbers with an Arion target are on target, names the closest one
and the biggest gap, counts the changes against the window before (better, worse,
steady, without a comparison) and says how to read the small charts. Below it the ten
numbers sit in three plain sections along the path: making a change ready (PR checks,
green on the first try, time until green, review), getting it merged (PR opened to
merged, one merge-queue run, queue tries per change, conflicts solved by script) and
shipping it (release to live, the full test run each night).

Each Simple tile has a plain name, the value in its own unit with "lower/higher is
better", a verdict against the target as shape and word (on target up to 1×, close up
to 1.5×, far off beyond; "no data yet" or "not loaded" instead of a verdict when there
is nothing to judge), a small chart with the target zone shaded green, an arrow marked
"better" and hatched days no source covers, the change against the window before, one
sentence with this window's numbers and Arion's wish, and the source or "Partial" with
its coverage. Merge rounds count the scripted share (target 80 %, the server's "≤ 20 %
by a model" turned around). **Learn** keeps the proper terms (p50, p90, wall time,
flake) and the Expert name: hover or keyboard focus shows it, a click or Enter pins it,
Esc closes it and leaves focus on the button. Below the sections the technical words
are explained simply. A failed preference save and a failed numbers read can stand
together: each alert owns one half of the status row, and Retry stays put.

**Flow** (A Lanes) shows who worked and who waited, one lane per actor (you, LEAD,
OPS, Reviewer, Builder, Checks). A wait sits in the lane of whoever is awaited, repeats
are rose, an incident is its own red band, and dotted connectors mark hand-overs. Rows
inside a lane are reserved once for the whole run, so zooming and panning never move a
lane. A step's label shortens from "run · text" to the text, then (waits and repeats)
to its duration, then to an ellipsis; a step cut by the window's start keeps
"· since HH:MM". Steps too narrow to see merge into a "+N" pill that zooms in on click.

The card keeps fixed heights: a control bar (zoom presets **Fit all · 15 · 30 · 1 h**
and **Follow**), a hint line that shows the selected step, a 64 px overview map of the
whole run with a brush (drag it to pan, drag its edges to resize, click beside it to
centre it), and 330 px of lanes. Only the playhead moves the time: drag its pill (or the
overview playhead), or press Shift+←/→ (a minute), Home or End. A click selects a step
(outline plus the hint line) or clears; it never moves the time. Dragging empty lane
space or a sideways wheel pans, ⌘/Ctrl+wheel zooms around the pointer, and a plain
vertical wheel scrolls the page. With the lanes focused, ←/→ pan by 10 %, ↑/↓ choose
a lane, Enter selects the step under the playhead, Esc clears and +/− zoom. Live opens
at 45 minutes (20 on a phone) with now at 65 % and follows now; moving the playhead
away shows "Viewing HH:MM" and **Back to now** returns.

Until the flow data package records runs, Flow says that no flow data is recorded yet
and shows release 126 of 8 Oct 2026 as an example. The moment panel and the Live,
Replay and Compare modes follow in a later package of AEON-994.
