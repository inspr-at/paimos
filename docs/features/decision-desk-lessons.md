# Lessons on the Decision Desk

Prepared lessons appear as **Lesson to adopt** on the Decision Desk. Each memo
shows the lesson sentence, the linked source when available, the agent’s source
summary, and the exact line that Accept adds to an entry’s Changelog or a rule
draft. Dismiss changes no entry and needs no reason; the decision remains audited.
Both decisions support the existing event Undo. The last lesson of a round ends it
like every other Desk kind: "All decided" with Undo for 1.2 seconds, then the
confirmation toast with Undo for the rest of a ten-second grace counted from the
decision. Undo on "All decided" restores the lesson open in the memo; Undo in the
toast puts it back on the Desk. A lesson decided earlier in a round keeps Undo in its
memo. Rule drafts still require a separate publication approval.

The Knowledge tab replaces the Method learnings inbox with one quiet link to the
project’s lessons on the Desk, shown only when lessons wait. Its count uses
canonical project lesson totals, including lessons beyond the loaded Desk page. Unprepared, stale,
decided, inaccessible or missing-target candidates never enter the projection.
Lesson IDs are stable UUIDs derived from the native learning ID, and their revision
is the recommendation event ID. A changed target is named in the memo; Accept
writes against its current version. A replaced recommendation is refused under
the native learning mutation lock through the optional quoted `If-Match` revision.
Older API clients can continue using the existing accept, dismiss and draft calls.

Projection reads share the prepared-person list validation, enforce tenant and
project visibility, and bound candidate rows/pages to 1,000. Coverage failures are
explicit rather than reported as exact counts. Lessons remain quiet notification
sources. Memo drafts, asynchronous decisions and Undo stay bound to the record
and signed-in person; source changes require a deliberate reopen.
