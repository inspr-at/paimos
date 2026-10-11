# Harness-sourced model catalog probes

The daemon health poll services model discovery even when no run is queued.
`POST /api/models/harness-probes/claim` schedules due enrolled accounts using the
existing catalog `interval_minutes`. `POST /api/models/refresh` (Check now)
queues approved accounts immediately, sharing the existing five-minute manual
cooldown. Provider refresh and harness collection complete independently:
`harness_probes_queued` and `harness_probes_truncated` report queueing;
`GET /api/models/refresh` exposes `harness_sources` and its truncation flag with
pending, probing, fresh, limited, unsupported, stale or timeout states. Reporting
can be disabled through the existing `agent_reports_enabled` setting.

Claims use bounded account keyset pages, one-minute retry leases, exact daemon
generations and account binding revisions. Reports recheck the exact live agent
key and `models.report` permission under tenant, tree, pairing, catalog and
account fences. Revoked, archived, draining, rebound or unapproved accounts
cannot publish. Same-request identical replay appends no second event; changed
evidence or a superseded request returns 409. Account health, grants, existing
immutable pins and routing orders are unchanged. Explicit native levels record
at most 200 advertised tuples per publication. The existing auto-add setting
allows new inactive profiles; otherwise these remain proposals. Identifier-only
models acquire no guessed default pin. The report response exposes `added`,
`proposed` and `profiles_truncated`; an identical replay adds nothing.

Each fact retains its exact native field path and an account-specific harness
source. Route-specific evidence outranks provider/aggregator facts only for that
route. A current missing field stays unknown instead of retaining an old field
as apparently fresh metadata. A complete list records route membership through
`listed`; partial or failed lists do not establish disappearance. Failures retain
last good evidence. Native efforts have their original names and no guessed
numeric scale. Identifier-only Cursor output keeps exact launch IDs and does
not derive efforts from suffixes. Cursor output examples are parser regressions;
they do not qualify a particular live binary's format or per-effort semantics.
OpenCode variant keys are not native levels.

Codex reads all bounded `model/list` pages with hidden entries, native levels,
default effort, upgrade and retirement metadata. Unix retirement seconds are
normalized to UTC while retaining `upgradeInfo.retirementAt` provenance. An
already-owned Codex process can supply this without launching another process.
Idle Codex launch retains the existing exact-binary startup/config/termination
qualification: there is currently no qualified production entry, so idle
Codex discovery reports unsupported. Claude uses `supportedModels()` on an
already-owned SDK Query; without an active supported Query it reports
unsupported. Catalog reads never submit a completion prompt or modify that
session's model. No idle Claude session is started.

Cursor uses its prompt-free `models` command with the paired private profile.
OpenCode uses `models --verbose` and preserves exact provider routes. pi runs an
isolated prompt-free RPC process with extensions, tools, skills and project
resources disabled; each model is selected and confirmed in that isolated
process before `get_available_thinking_levels` is read. An active pi session
is left alone and the catalog result is unsupported. Unrecognized outputs are
stale rather than guessed. Children use the existing owned-process lifetime;
unconfirmed cleanup retains the durable capture fence across daemon restarts.
The daemon refuses redirects and bounds commands, pages, model counts, bytes
and the whole capture deadline. No vendor output or credentials enter events.

Migration 1334 adds one tenant-RLS request row per account and is classified in
the DSAR inventory. Recorded protocol examples and injected RPCs/clocks cover
metadata, missing fields, precedence, incomplete lists, idle safety, scheduling,
Check now, replay and final authorization. Tests require no live vendor request
or harness login. The fixture README links the checked source formats.
