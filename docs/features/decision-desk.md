# Decision Desk

The Decision Desk UI (AEON-567) lives at `/decision-desk`. Open and answered
questions are paged separately; refresh retains up to ten loaded pages per state
(1,000 questions), with remaining results stated explicitly. Each memo freezes its
source for the round. New arrivals wait for the next round, including rounds opened
from Decided. Expired approvals appear in history. Approvals and tier requests
require an explicit choice; Enter on another focused control performs that control's
action. In a field, Enter finishes editing and the platform modifier plus Enter
submits. Access loss, expiry and changed sources block the write and retain drafts.

Tier requests are read only for sessions advertising AEON-436's `service_tier_v1`.
AEON-455 server availability selects request-bound phone verification on every
screen size; a server without that package keeps the existing approvals API.
Held replies address the original principal UUID and, when supplied, its exact
session. Related ticket records use permission-checked node relations; specialised
Always/doctrine publishing and its context projection remain upstream package
integration work. The unavailable stamps explain their restrictions in the memo.

Decision Desk question groundwork (AEON-562): `aeon ask --project KEY
--option '["Title","Description","Answer"]' "Question"` stores a bounded,
project-scoped question and returns immediately. Add `--ticket KEY`,
`--context-file PATH`, `--recommend 1 --why "Reason"`, `--meanwhile parked`,
`--meanwhile-text "Other work"`, `--keep once` or `--anyway "New evidence"`
as needed. Retain the printed `--request-id UUID` and exact input for retries;
`aeon ask status UUID` reads the durable question after the original session ends.
`--session UUID` is a verified public harness generation, separate from the
CLI's global attribution `--session-id`. Named project/ticket lookup uses the
existing node read permissions; UUID addressing needs only the question scopes.
The MCP `ask` tool accepts `{project, ticket?, input}` with the same typed HTTP
input and mandatory request ID; `ask_status` accepts `{question_id}`.

The additive API is documented in `api/openapi.yaml`: project question create/list,
question get/status/person decision and a permission-filtered `/decision-desk`
question projection. Answered lists accept `state=answered&order=desc` (AEON-611):
newest current answer first, with question ID as the tie-breaker. Follow the
bounded `next_cursor` using `cursor`, with the same tenant, principal and project;
permissions are checked again per page. Descending reads require offset zero.
The Decided view follows these cursors; “Load 100 more” resumes the saved cursor
with one page request. Refresh rechecks its loaded pages from the newest decision,
so a fresh answer stays available for correction. Continuation keeps questions
before protected requests, hides action requests projected into questions, and
preserves decisions recorded in the desk while that page is loading. Decisions
committed during pagination may require a refresh from the first page. Default and
Open reads keep oldest-first creation order. `questions.ask` and `questions.read`
are explicit agent key scopes; `questions.decide` is person-only. Owner/admin/member roles receive all
three, viewer receives read, guest/customer receive none. Existing keys gain no
new scopes. Agents read only questions they asked, with only their memberships.
Generic node/knowledge CRUD cannot modify question/decision authority.

Questions and immutable answer revisions use protected nodes plus tenant/project
projections. Each asker retains input, principal, exact original session, source
request, reply-root UUID and comment destination (ticket, or question node).
The dispatcher materializes each reserved reply root as a held counterpart for
its answering person. The original request is never released or executed.
Person answers use a database-clock ten-second grace window; edits restart it.
A bounded durable dispatcher serializes with edits and commits each asker's inbox
and comment effects independently. Revisions dispatched before a change remain
in the log; the new typed correction names its `replaces` answer ID. The inbox
outbox state `delivered` means dispatched, while `receipt_state` reports `queued`,
`handed_off`, or `failed`. CLI status distinguishes dispatch from receiver proof.

The question dispatcher (AEON-1111) wakes on committed answer, reused-answer and
session-ended receipt notifications. Reused answers create immediately due inbox
and comment effects, without another grace window. A single tenant-scoped read selects up to 32 due effects
and the earliest persisted delivery or retry deadline; the ten-second grace and
30-second retry windows remain unchanged. Startup, listener reconnect and a
30-second reconciliation recover missed notifications. Notifications only prompt
a durable read; each effect still checks current authority in its write transaction.

A signed-in addressed person with inbox management and question permissions can
use `tell --reply-to UUID --session-cookie-file PATH` on a held request. The
file contains only the `aeon_session` cookie value; the command uses the selected
instance URL (or `AEON_URL` / `PAIMOS_URL`), sends that cookie without an
Authorization header, and ignores ambient agent keys and sender sessions.
This records the answer/outbox and settles the request atomically, returning
`status: pending`, a question ID, revision and deadline. Other callers retain the hidden-parent refusal. Resolve/dismiss and
permission approvals keep their existing immediate semantics. Desk dispatch and
ordinary replies reserve the obligation, message, delivery and receipt rows
before appending events. Migration 1117 permits event references to be filled
in within that transaction and rejects incomplete reservations at commit.

The additive `harness-session/2.7` response contract includes optional `desk_answers`
references; registration and heartbeat request requirements remain unchanged.
Ended generations retain bounded durable answer references. Only a verified
same-project/principal continuation receives the latest answer; an absent
successor stays visibly `successor_pending`. Continuation briefs carry references
whose private text is resolved through authorized `ask status`, preventing
harness metadata readers from gaining question content. No old process is woken.
Outcome effects (AEON-565) apply after the finalized grace revision. Once keeps
an immutable Decided record. Always publishes a protected Knowledge entry of type
`decision`, using the existing knowledge list/filter/resolve/graph surfaces and
CLI taxonomy. A correction immediately withdraws and archives the earlier Always
answer, independently of grace or failure of its replacement; lineage remains.
Withdrawing an active Decision requires `knowledge.write` on its project,
including when correcting to Once.
Requirement appends one criterion with its exact answer marker, preserves the
other ticket fields, and compares the captured ticket revision before writing.
Concurrent edits leave a visible `ticket_revision_conflict`; decide again after
reviewing the ticket. Requirement is unavailable for list or structured criteria.
Corrections replace only the exact tracked criterion; edited or removed criteria
remain untouched and appear in `effect_data.review_required` on the new effect.
Missing, moved or otherwise non-editable prior tickets also require review;
their criteria stay untouched while the rest of the correction applies.
Carried reviews retain each distinct kind and reference. Refreshing a review's
reason on a later correction preserves the reason on superseded effect revisions.
Doctrine requires `doctrine: {source_id, path, rule_key, rule_sha256, tldr_en?,
tldr_de?}` on the question or person decision. It creates only a pending AEON-444
inbox draft; the existing person-only submit/dismiss and AEON-319 publication gates
remain authoritative. Dismissed and expired drafts are already retired. Published
or person-edited proposals remain untouched and appear in `review_required`; the
rest of the answer can apply, and the existing Doctrine path governs any manual
correction. This review provenance carries across later answers; a Doctrine
correction requiring that review creates no new draft; a person must handle
the correction through the existing Doctrine path.
CLI status reads the current proposal lifecycle, including dismissal and promotion.
No rule is described as changed merely by proposing.
Status exposes `outcomes` with availability and a why for disabled stamps, plus
per-revision `effect_data` and safe failure explanations. Availability reads load
permissions once and report a cheap Doctrine `mapping_present` hint; exact mapping
and quotation checks run when deciding and applying. Expensive inbox preparation
runs before the tenant mutation fence, then rechecks authority and cache versions
inside the write. Transient failures retry with current permissions, including
`stale_source` preparation races and an expired `public_main_unavailable` cache.
Each transient failure schedules its next attempt 30 seconds later. A stale
public-main cache keeps retrying until it is refreshed or the answer is replaced.
Permanent conflicts expose `retryable: false` and require a reviewed new decision; failed
outcome writes never appear applied. Encoded Doctrine inputs exceeding the
4096-byte persistence bound return 422 and are stored only for Doctrine.
`source_handover_id` remains unavailable pending its verified ask-source adapter.
