# Authenticated review verdicts

The dispatcher creates a reviewer run for one ticket, repository, PR, target
branch and exact `base_sha..head_sha` scope. The base is the reviewed merge base
with the target. The current delivery observation must match, including its
target branch. Older delivery observations without a target remain unknown
until refreshed. No run credential is returned to the dispatcher.

The author and reviewer must have recorded worker sessions on that ticket in
the same project, with different agent principals. Review runs require known,
different model families even when the generic review policy is `off`. Harness,
model and reasoning effort are captured from the reviewer session when the run
is created and cannot be supplied in the verdict. Runs expire after 30 minutes.

The assigned reviewer uses its own agent key to claim the run credential once.
The creator and any principal with coordinator authority cannot claim it. The
credential is bound to that key and run, stored only as a digest, and consumed
by one submission. Submission accepts `X-Aeon-Review-Credential` without a
session cookie or ordinary bearer key. The final transaction rechecks the
reviewer session, key, project permissions, range and target. Responses carrying
credentials use `Cache-Control: no-store`. Paired daemon principals retain their
existing runtime ceiling and cannot use this separate reviewer credential path.

For example, the dispatcher runs:

```sh
aeon review run create --ticket AEON-1391 --repo inspr-at/paimos --pr 123 \
  --target main --base "$BASE" --head "$HEAD" \
  --author-session "$AUTHOR_SESSION" --reviewer-session "$REVIEWER_SESSION" --json
```

The assigned reviewer runs with its own configured key:

```sh
aeon review run submit --run "$REVIEW_RUN" --ticket AEON-1391 \
  --repo inspr-at/paimos --pr 123 --target main --base "$BASE" --head "$HEAD" \
  --verdict ok --visual-impact no --reason 'No presentation changes in the reviewed scope.'
```

The CLI claims the credential in memory and submits without its regular key.
It never prints the credential or follows credential-bearing redirects.
An already claimed credential can instead be passed through
`--credential-file PATH` or `--credential-file -` for stdin. A lost credential
or changed head, target or scope requires a fresh run.

Visual runs use `--kind visual` when created and require the separate
`reviews.visual_approve` permission (Tick). This permission is absent from all
built-in roles, including Owner and Admin. A workspace owner can explicitly
delegate it through a custom role. The reviewer key needs the scope, and its
person creator needs an explicit role grant too, as with other agent authority
intersections. Revocation before submission is effective inside the final
transaction. Visual runs do not substitute for cross-family review runs.

Existing `coordinator_review.recorded` and visual evidence events remain in
the append-only audit log. `GET /api/nodes/{nodeId}/review-evidence` flags
records without authenticated run provenance; entry-check refusal reasons name
them `legacy_unverified`. They cannot satisfy Done or admission. The old
entry-evidence route continues to handle person-admin design attachments and
refuses review and visual verdicts. A later negative verdict revokes a passing
observation for that kind. The authenticated verdict includes `visual_impact`
(`yes`, `no` or `uncertain`) and a reason for the downstream admission evaluator.

These endpoints record evidence only. Enqueue, merge authority, holds and the
conservative visual-impact classifier belong to the admission packages.
