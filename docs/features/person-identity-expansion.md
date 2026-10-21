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
remain valid. Project authorization ignores node bindings, including roles
that otherwise hold write permissions. Per-computer permissions remain separate.

Transaction visibility is resolved by `aeon_enter_principal(tenant, principal,
key_creator, acting_person)`. Its text result and `aeon.visible_projects` keep
their existing contract: `*` for all projects, a UUID array for selected projects,
and empty/unset for none. Migration 1410 adds `aeon.visible_nodes`, a UUID array
of individually readable nodes (empty/unset means none), read by
`aeon_visible_nodes()`. The legacy three-argument entry delegates with no acting
person; both signatures keep the account-use guard and canonical-principal IDs.

Node bindings count only when their role reads nodes and both the principal
and its canonical principal are active. Each caller's readable set is its
workspace/project visibility plus its explicit node grants. The result is the
intersection of the principal, key creator (when present), and acting person
(when present). Mixed scopes intersect as records: an agent with project access
acting for someone granted one node sees that node, provided the key creator
also sees it. A missing, inactive or non-person acting identity sees nothing.
Re-entry replaces both settings, and explicit service/no-principal entries clear
the node setting.

The restrictive node visibility policy adds these IDs only to `USING`; its
workspace/project `WITH CHECK` stays unchanged. An empty grant list is a
non-indexable constant false in that expression, so an ordinary project list
keeps its child-index walk. Membership is the same once a grant exists.
Attachments, embeddings, embedding jobs, relations and events follow their
existing node-reference RLS policies.
Rule-resource restrictions and outcome/serves visibility still apply. Node
bindings never enter `readGrants`, so mutation authorization through `RequireTx`
still requires workspace/project permission. Endpoint permissions such as
`search.read` and `attachments.read` remain independently required. This is
groundwork: no grant creation API or UI is added; native stream viewer authority
remains the separate AEON-1291 scope.
