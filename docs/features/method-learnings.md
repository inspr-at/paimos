# Prepared method learnings

AEON-1330 narrows the server's method-learning inbox to incident tickets
(incident kind, tag or title), reopened work, review `changes` verdicts at or
past the workspace fix-round limit, and explicit `process-learning` comments
by people or agents. The sensitive-text guard remains active. Routine closed
work and untagged status/incident comments are no longer nominated.

Agents read all open candidates through `GET /api/knowledge/learnings` and
prepare a proposal through `PUT .../{learningId}/recommendation`. People see
only current, complete proposals with a lesson, `source_summary`, and a live
target. An entry target includes the exact dated `line` that accept appends.
For a rule target, `knowledge_id` is the set, `rule_layer_id` is its layer, and
`lesson`/`rule_text` is the exact text added by the existing draft action.
Drafting does not publish rules. Rule target reads respect the caller's rule
owner and scope; applying the draft checks write access again.

Older recommendation requests remain valid but stay agent-only until their
preparation fields are complete. Stale proposals and unavailable targets also
stay out of the person list. Filtering starts before each bounded source scan,
so a large unprepared window cannot hide an older prepared lesson.

Agents with `knowledge.write` may dismiss noise with a nonblank reason. Each
dismissal records `knowledge.learning_dismissed` with the actual agent actor.
People with the existing Undo and other-actor Undo permissions may undo it. Accept and draft remain person-only; older person dismiss
requests without a reason remain valid.

At the next server startup, each instance clears pre-release open sources
without any recommendation. Migration 1350 records a fixed cutoff for existing
tenants; startup records one for new tenants. Cleanup runs under each tenant's
System principal, in bounded batches and separate item transactions, logs counts
per project, and writes the normal undoable dismissal event with the reason:

> auto-dismissed: legacy title-only nomination without a lesson (Markus 2026-10-14)

The cleanup protects every recommendation, including stale and incomplete older
ones. It copies no source text into new events. Checkpoints resume interrupted
work, and the completion marker prevents a restart from dismissing a source
that a person has reopened by undo. This backend package does not add the
Decision Desk UI; that is the separate P2 delivery.
