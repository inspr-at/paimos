# Recorded deployments

After OPS has checked a deployment live, its confirmation can deliver merged
work without publishing a PAIMOS release:

```sh
aeon deploy record --project AIT --env production --sha FULL_COMMIT_SHA --version VERSION
```

`--version` is optional. `--sha` is the full 40-character Git commit SHA.
`--env` names the deployment destination. The project must have its repository
linked through the existing Delivery source settings. The API is
`POST /api/projects/{projectId}/deploys`, with `environment`, `sha` and optional
`version`. It uses the existing project-scoped `releases.deploy` permission.
The authenticated person or agent is recorded as the live confirmer, with the
server's timestamp. Recording is an explicit confirmation; unattended Status
autopilot modes do not disable this authorized action.

The server verifies the deployed commit in that repository and checks whether
each ticket's newest recorded merged delivery is an ancestor of that commit
(including equality). Merge evidence comes from delivery completion events or
the merge audit, never editable ticket fields or commit dates. Done work leaves
then use the release publication transition, including its audit events,
comments, human-check safeguards and Undo support. The existing ticket state
display updates through those events. Delivered and Accepted stay unchanged;
parents, active work, tickets without merged delivery evidence and work marked
`no_release_needed` are preserved. A pending human check is reported as skipped.

The append-only `deployment.recorded` project event is the immutable deploy
record and contains the repository, environment, SHA, version label, confirmer,
time and per-ticket results. Its tenant-local event ID is returned by the API
and CLI. Ticket changes and the deploy record commit in one transaction.
Retries with the same project, environment and SHA return the original record,
including its original version label and confirmer, without repeating ticket
changes. Recording an older SHA never reverses delivery. Recording the same
SHA in another environment creates a separate confirmation.

An unknown deployed SHA returns 422. Missing repository setup or changed
repository/merge evidence returns 409. Repository read failures return 502,
including a private repository without matching configured GitHub App access.
The existing App authority stays bound to its tenant and repository; other
public repositories use anonymous GitHub reads. Requests have a two-minute
deadline, each upstream response is limited to 4 MiB, and the candidate set is
limited to 1000 tickets and distinct commits. Exceeding the bound or failing to
verify the complete candidate set records nothing. CLI output includes
delivered, unchanged and skipped counts; `--json` returns the full record.

Historical AIT backfill remains the separate AIT-180 operation.
