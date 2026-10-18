# Ticket work measurement (AEON-503)

Terminal managed runs expose `waiting_ms` alongside `active_ms`: waiting spans
are clipped to the run lifetime and closed at the terminal timestamp, including
finished reports without a status. More than 10,000 status readings leave both
measurements unknown without rejecting completion or usage. Existing rows keep
their recorded active time and have null waiting time; no history is rebuilt.
New work-start estimate snapshots freeze area, role and complexity as well as
size and rate. Later estimate edits cannot change that baseline.

`GET /api/outcomes/measurement?ticket_node_id=AEON-503` requires `outcome.read`
and `harness.read`. It returns lifetime recorded completion, review-verdict and
fix-round counts, and distinct retained commit diff totals from current session
bindings. It is a live evidence view, not an episode receipt or proof of complete
contributor coverage. Project restrictions and bounded/truncated evidence are
reported in `gaps`; missing diff totals are null, never zero. File changes sum
per commit, not unique paths. Heartbeats collect content-free numstat counts
with byte, file and time bounds; binary/failed reads remain unknown. Session
responses add optional diff fields under `harness-session/2.8`.

AEON-502e remains the only model/kind/complexity estimator. Its existing learning
query now requires paired measured active/waiting time, final token evidence,
a frozen source-project baseline and a single baseline per ticket. Legacy,
missing timing, provisional tokens, moved sources and reopened/multiple-baseline
work do not train this path. Restricted node visibility also excludes samples
so an RLS subset cannot certify whole-ticket effort. Episode boundaries, retained
contributor ownership, completion receipts and interval rebinding remain a
separate concept decision.
