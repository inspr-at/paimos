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

Simple (summary, plain sections and small charts) and Flow (Live, Replay, Compare)
follow in later packages of AEON-994; until then Simple shows the Expert numbers and
Flow says that no flow data is recorded yet.
