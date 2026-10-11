# Routine execution principals

`GET /api/recurrences/{recurrenceId}/execution-principals` supplies the identity
choices for a saved routine with an assignment. It requires an interactive
session of the canonical owner, with current `recurrences.manage` in the output
and definition scopes. Other people, agent credentials and bearer credentials
cannot use the picker. Personal definitions retain their owner privacy.

Each item contains only `id`, `kind` and `label`. Personal routines offer their
active canonical person. Project and workspace routines offer existing active,
unlinked, configured, keyless agents with exactly one restricted custom role in
the exact scope. Source permissions, role restrictions, active keys and pairing
computers use the same `executionIdentityTx` check as consent and final claims.
No role, credential, consent, process or execution grant is created.

Pagination is bounded to 100 checked principals per request, ordered by UUID.
Pass `next_cursor` as `after` until the cursor is null. A page can contain fewer
than 100 eligible items, including none, while still having a continuation
cursor. This bounds work even when many principals are ineligible; such pages
do not mean the list has ended. Invalid cursors return 400, invisible or retired
recurrences return 404, and ticket-only recurrences return 409.

Identity eligibility is independent of project qualification and launch
enablement, so choices remain available while those gates are closed. The
consent PUT still checks the displayed recurrence revision, current owner and
permissions, live qualification, and the chosen identity under the access
fence. An identity revoked after listing cannot be consented using the old
list. Consent and final admission remain separate requirements.
