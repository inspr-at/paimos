# Person identity expansion and paired computer inventory

API-key responses include nullable `person_id`; authenticated key principals
carry that explicit value as `KeyPersonID`. Run responses include nullable
`acting_person_id`. Existing keys and runs remain unbound. Issuer, pairing
approver, requester and model-preference stamps never fill these fields.

Server admission writers can call `agentruns.SetActingPersonTx` inside their
authorized transaction, under the tenant access fence and before event writes.
It accepts an active canonical person in the same tenant and preserves an
existing binding, including on retries. This expansion wires no start path and
does not enforce the later person ceiling or old-key cut-over.

`GET /api/agent-pairing/computers/inventory` requires a signed-in person with
workspace `account.manage`. `aeon computers inventory --session-file PATH`
uses the same endpoint; `--json` emits its allowlisted evidence projection.
`--url URL` selects an instance for the supplied person session. No credential
is included in the report.

Each computer reports its pairing approver, live marked-workstation key person
and issuer evidence, and enrollment account owners and approvers. Conflicting
person identifiers produce `owner: ambiguous`; consistent or missing evidence
remains `unconfirmed`. This report assigns no owner and grants no permissions.
Archived and revoked computers remain visible for inventory. Pages default to
50 computers, at most 100; pass `next_cursor` as `--cursor` to continue. At most
100 enrollments are returned per computer, with `enrollments_truncated` when
evidence is partial. Requests have a five-second deadline.

Role bindings now admit computer and node scopes with separate shape checks
and partial unique indexes. Workspace/project shapes and their conflict targets
remain valid. Current project authorization ignores the expanded scopes;
per-computer permissions and individual-node visibility are separate increments.
