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
refreshes it every minute while the page is visible. Eighteen tiles (the ten Arion
numbers, then the eight v5 readings described below) show, for the chosen
window, the value (p50 for durations, the mean for queue runs, the share for rates),
p50/p90 or the number's own detail, the count, the change against the same number of
days just before (steady within 10 %, or 3 points for shares; no comparison when that
window has no data), the Arion target and the source. Numbers 3 and 7 combine two
metrics (green on first try with flake-only failures; review time with the share of
"changes" verdicts); merge rounds show scripted of all rounds, the nightly run green
nights of nights with a run. A tile is **Partial** when its window is partial or not
fully covered, and names the coverage ("covers 4 of 7 days").

A trend chart per tile uses one point per day (7 and 30 days), per week (90 and 180) or per
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
numbers sit in four plain sections along the path: making a change ready (PR checks, green on the
first try, time until green per branch and per commit, review, runner wait, runs that
failed and then passed, pre-checks), getting it merged (PR opened to merged, one
merge-queue run, queue tries per change, conflicts solved by script, changes thrown out
of the queue, extra queue runs of unknown cause), shipping it (release to live, the full
test run each night) and keeping it safe (review-audit findings, defects that reached
production). A section whose numbers have no target shows no "0 of 0 on target".

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
are explained simply.

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

**Flow modes** (`?mode=live|replay|compare`, `?run=`) sit left of the legend. Flow reads
the recorded runs (`GET /api/projects/{projectId}/delivery/flow` and
`/delivery/flow/runs/{itemId}`, see [Delivery Flow data](delivery-flow-data.md)) and
follows the server-sent hints `delivery.step`, `delivery.item` and `delivery.incident`
by reading again; Live also reads every minute. New data for the same view keeps the
person's window, time and selected step; a failed read shows an error with **Retry**,
never an old answer. Steps carry facts only, so their labels are built from the step
key, kind, round, outcome and wait reason. Without any recorded run, all three modes
show one plain empty state: "No recorded runs yet. Runs appear here once work goes
through the PAIMOS work queue." Flow never shows sample data (AEON-1135); the first
recorded run replaces the empty state without a reload.

- **Live** shows the runs active in the last hours (and those that ended in the last
  45 minutes): releases first, then the soonest estimate. An open step runs on to its
  usual length (a dependency to the awaited run's estimate), drawn as expected past
  now. Below, the runs in flight: Simple says what happens now, who is on it, what it
  waits for, how much is done and when it is expected, with a verdict against the
  target (on target, close, far off: shape and word); Expert shows step, actor, step ETA
  and run ETA with p90.
- **Replay** opens at the start of the latest release (or the latest run) and plays it
  once per browser session: 60 s for the whole run at 1×, 30 s at 2×. Playing follows
  the playhead; a pan or zoom stops following, a playhead drag pauses. With reduced
  motion nothing plays: the run stands still, Play is off, and the handle and
  Shift+←/→ still move the time.
- **Compare** races a release from its step a against the Arion target (Project Arion
  v5 §4b: 60.6 min now, 53.9 after Phases 2 and 4) on one relative axis. The wait for
  a person (W) and a conditional catalogue overrun sit beside that path and are not
  in the total. Two lane sets (Reviewer, OPS, Checks, and the person on the target),
  one overview, one playhead, finish lines.
- Replay and Compare list **where the time went**: working, waiting, doing it again and
  incident + recovery along the critical path, hand-overs, and the biggest waits and
  repeats.

The headline above the card says the state in one Simple sentence or one Expert line.
The **moment panel** (196 px, 300 px stacked on phones) says "At HH:MM" with one
sentence, one line per active lane (step, minutes so far of its length, usual p50,
Arion target) and one idle line; on the right the selected step (start to end to the
second, took, usual, target, outcome, round, waits for, source) or the incident. The
avatar, the PAIMOS Orbit agent cube carrying the run's parcel, stands at the playhead on
the main run's current step, with a clock badge while waiting and a red one during an
incident.

**Phone, dark and keyboard.** On a phone the project sections show only the selected
section's word; the others keep their icon, with the name read aloud and shown as a
tooltip. Every Delivery control is at least 44 px there, including the time handle and
the overview's brush edges and playhead. Every text reaches 4.5:1 against what is painted
behind it in light and dark. Each switch group is one tab stop with arrows, Home and End
inside; Learn opens on focus, Enter pins it and Esc closes it. In Flow a refused save of
the window or level takes the headline's place, so the mode switch and the card stay put.

## Project Arion v5 readings (AEON-1016)

The page is steered by Project Arion v5 (WP1.1). Every definition below is the one
`measure-v2.py` uses, so the tiles equal it on the same window: a run is counted on the
day it was created, a pull request on the day it merged, durations are rounded to 0.01
minutes before the percentile. `TestDeliveryNumbersEqualMeasureV2` holds the Go
computation against the plan's own records (`internal/delivery/testdata/arion_w2.json.gz`,
built by `arion_w2_convert.py` from the value-free records `measure-v2.py` wrote) on its
W2 window, 5 Oct 00:00 to 9 Oct 04:00 UTC: 427 first attempts, 62.3 % green, 65.8 % with
the required checks only, 267 commits and 130 branches that went green with 192 and 19
that never did, 90 inferred ejections and 24 extra queue runs for 151 merged pull
requests, runner wait p90 6.2 minutes. A run that completes after the window's end is not
a fact yet, so a window ending in the past equals the plan's figures.

**Targets** follow v5 section 4: PR CI wall and merge-queue run 10 minutes, then 7
(conditional), with no "3 on a reuse hit"; first-attempt green 70 %, then 80 %; time to
first green per branch p50 30, then 20; PR opened to merged p50 80, then 60; queue runs
per PR 1.25, then 1.1; review 10, then 8 minutes with changes at most 40 %, then 25 %;
release about 61 minutes plus W (the wait for a person), then about 54 plus W; runner wait
p90 3 minutes, then 1; confirmed flaky runs at most 2 % of runs; inferred ejections 25,
then 10 per 100 merged PRs. Readings the plan sets no target for (time to first green per
commit, unclassified queue runs, preflight, audits, defects) show "No target yet", never a
verdict.

**Readings.**

- *Time to first green* per branch (first run created to first green on any commit) and per
  commit (to the first green attempt of that commit). Branches and commits that never went
  green are counted beside the time, never inside it; for commits, how many a later commit
  of the branch superseded. A branch that began before the window is not in it, even when it
  went green inside.
- *Required checks green* beside the workflow's green: only the project's required checks
  (delivery settings, default `go`, `web`, `release-check`, `e2e`, `migration-compat`) decide,
  skipped counts as passing. On a complete job map a required name that is absent is red
  (the job never ran). On a legacy map that is not complete, an absent name stays unknown
  and is not guessed. The Numbers and Simple views show this companion's own sample size,
  and its coverage and missing-facts reason whenever that window is not whole.
- *Inferred ejections* and *extra queue runs, cause unclassified*, each per 100 merged pull
  requests that had a queue run: a queue run with a red required check is an inferred
  ejection; an earlier run of a merged pull request that was cancelled or required-green is
  counted as unclassified (predecessor failure, reordering or manual removal cannot be told
  apart before the queue events are joined, WP1.6).
- *Runner wait*: per first attempt the longest wait of any job that ran (skipped jobs have
  none); the big number is p90, with p50 beside it.
- *Confirmed flaky runs*, *workflow rescues* and *suspects*. A confirmed flake is the same
  case, the same commit, the same workflow revision and the same runner class, red then
  green (Project Arion v5 §3a). This release does not store case, revision or runner class,
  so the confirmed share is withheld: no percentage, and not 0%. A first attempt that failed
  and passed on a later attempt of the same run is a workflow rescue, counted beside the
  share, never inside it. measure-v2 labels that same-run rescue as confirmed; v5 §3a does
  not, and this page follows §3a. A suspect is a run that failed on a commit whose next
  commit passed on the very same tree (the commit's tree id is stored with the run). That is
  a subset of the plan's definition (no change in the failing owner package, spec or
  fixtures); the wider form needs the complete impact map of WP1.4 and is not counted. Suspects
  never enter the share.
- *Preflight red rate*: first attempts of `ci-preflight.yml` runs that ended red, on their own
  line; never counted as CI green.
- *Review audits* and *escaped defects* are reported facts (`POST .../delivery/metrics/facts`):
  `review_audit` (key, `pull_request`, outcome `clean|low|medium|high`) and `escaped_defect`
  (key = the bug or incident ticket, `pull_request` and/or `release` that caused it, outcome
  `low|medium|high`). The same key corrects the earlier report. Rollout incidents of releases
  (Flow, `down` high, `degraded` medium) count as escaped defects too. Coverage starts with the
  first report; inside it, nothing recorded is shown as "0 recorded", before it as "no data yet".

**Where the facts come from.** The check-suite webhook stores each run attempt with the tree of
its head commit. For the first attempt of a pull-request or merge-queue run it also reads the
jobs and keeps every job conclusion that fits the page bound, as a complete map, plus the worst
wait for a runner. A name or conclusion that does not fit is refused and the run stays without
job facts. A legacy row that stored only some check names is read once more. A jobs read that
fails never loses the run: the run stays without a complete map, and the windows that hold it
are partial and name how many runs lack job facts. The backfill lists the CI workflow, the
nightly workflow when it differs, and `ci-preflight.yml` on a fresh backfill. It ends with a
jobs pass (`phase: jobs`) that reads the jobs of first attempts whose map is not complete, at
most 40 GitHub reads a step, newest first. A run whose jobs GitHub could not return is not
eligible again until an hour after the failure. While any such retry is still outstanding the
step stays `state: running`, `phase: jobs`, and names `retry_at`. A caller stops only when
`state` is `done`. When nothing is unfinished the pass is done and does not read GitHub again.
Nothing readable at all answers 502. A deployment whose backfill finished before preflight was
collected runs one catch-up of `ci-preflight.yml` and returns to the jobs pass. That catch-up
does not read pull requests again. Preflight coverage is its own span: full only after that
workflow was backfilled, otherwise only the webhook samples.

Not part of this release: the machine-readable job-summary marker for strata lane, runner class
and reuse result (it needs a producer in `ci.yml`, which OPS owns), and a native "caused by"
link between tickets (a new relation type changes the shared relation contract; escaped defects
are reported with their link instead).
