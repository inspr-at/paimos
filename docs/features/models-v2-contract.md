# Models v2 page contract

`GET /api/model-preferences/simple` returns each row's pick, native effort,
ordered `chain`, continuation `end` (`wait` or `default`), and attributed
`never` rules. `for=default` also returns explicit workspace `situations`,
keyed by column and `small`, `fix`, `stuck`, or `review`. Missing situations
follow the row; missing Review cells use automatic cross-family review.

Order writes accept `rank: [pick, ...chain]`, `efforts: {line: nativeEffort}`,
`end`, and the current profile `revision`. Existing `not` input remains
supported and can be omitted. Omitted efforts/end preserve saved values;
an empty efforts object restores automatic levels. A member can save a
workspace column with `for=me` and `If-Prefs-Person`; resetting removes the
personal order. Workspace top locks return 423 for personal writes. Review
orders must meet the strong/frontier floor at the highest native level.
Native efforts are validated across the registered harnesses of the line;
legacy harness-derived line ids remain accepted. Stored effort maps are
bounded to 32 KiB.

Workspace administrators can set or lift a row exclusion with
`PUT`/`DELETE /api/model-preferences/orders/{column}/first/never/{line}`.
PUT requires `why` and the current workspace `rules_revision`; DELETE uses
the `revision` query parameter. These writes preserve other rules and record
the current person, timestamp, and explanation.

People with `models.manage` can pause registered native levels of a harness/model
route with `PUT /api/models/lines/{harness}/{model}/pause`, using `until`
(a future timestamp or null) and `reason`. DELETE resumes the route. Active
pauses appear on model profiles and dispatch reads the same pause storage.
Cursor's effort suffixes are handled together for the same line and version;
the separate fast route and other versions retain their own pause state.
Authorization is rechecked in the final transaction under the tenant fence;
successful writes append their audit event after the mutation.

The page includes Quick jobs' resolved pick, harness and trace. Each
`unavailable` entry represents one distinct cause for that row:
`daily_cap`, `paused`, `no_tools`, `no_account`, `retired`, or `residency`,
with the skipped route, visible reset/expiry time, replacement, and `waits`.
The compatibility `reason` remains deprecated. Private account evidence
retains the resolver's generic explanation and exposes no reset metrics.

The `outdated` summary groups non-retired older numeric versions across
routes and levels and names representative immutable profile ids and their
newer active successors. `uses` counts visible explicit pins in workspace
and canonical caller settings. Detailed provider lifecycle facts and the
retirement/undo endpoints belong to AEON-1145.

`GET /api/settings/review-policy` explains automatic reviewer `family_order`,
the strong/frontier `floor` with `min_effort_level: 4`, and
`security_excludes: [anthropic]`. These dispatch rules are read-only. A
full-access agent key can read `/simple` under its live workspace grants;
it does not gain person-only write authority.
