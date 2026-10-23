# Hosted assistant identity and policy

AEON-1701 adds the `assistant` class of agent principal alongside builders.
The principal kind stays `agent`, retaining existing authentication, inbox,
keys, tenant isolation and audit. Nullable `principals.agent_class` is additive;
existing messaging-only principals are recognized as assistants without changing
their IDs, keys or owners. Their default policy allows the three existing inbox
scopes, no projects and zero USD weekly spend. Vendor is `unknown` until configured.

`/api/assistants` lists bounded pages, creates identities or links existing
messaging assistants. Each identity has a display name, vendor (`grok` or
`unknown` for this first slice), person owner, principal status and policy.
Linking accepts only an existing messaging-only identity with the same person
owner. Owner transfer and reactivation are excluded from these routes.
New identities retain the messaging-only restriction and receive no key or role.
The existing `agents assistant` credential lifecycle remains available.

Person writes require both live workspace `agents.create` and `keys.manage`,
rechecked under the tenant/tree access fences inside the mutation transaction.
Writes preserve the existing owner boundary: only the canonical person owner
may configure or disable an assistant, including when another person has both permissions.
Person reads require `members.read`. An agent with live `inbox.read` may read
only its own `/api/assistants/{principalId}/policy`; existing routes retain their
authorization. Disabling deactivates the principal and revokes its keys atomically.

Policy contains explicit project and scope allowlists, an exact decimal weekly
budget plus unit (`USD`, `EUR`, `tokens` or `credits`), `outward_action_rule:
approval_required` and `never_ask_person_for_secrets_or_manual_steps: true`.
Empty project allowlists permit no project work under the policy. A policy is
an additional ceiling, never a grant of key, role or project authority.
Metadata and policy edits require `expected_revision`; legacy defaults start at
revision zero. Conflicts return HTTP 409. All responses are uncached.

The CLI uses the same API:

```sh
aeon assistant list --limit 50 --session-cookie-file SESSION
aeon assistant show PRINCIPAL-ID --session-cookie-file SESSION
aeon assistant create --name Amy --vendor grok --owner PERSON-ID \
  --principal EXISTING-MESSAGING-ID --policy-file policy.json \
  --session-cookie-file SESSION
aeon assistant policy show PRINCIPAL-ID
aeon assistant policy set PRINCIPAL-ID --expected-revision 1 \
  --policy-file policy.json --session-cookie-file SESSION
aeon assistant update PRINCIPAL-ID --expected-revision 2 --name Amy \
  --session-cookie-file SESSION
aeon assistant disable PRINCIPAL-ID --session-cookie-file SESSION
```

`--policy-file -` reads one bounded JSON policy from stdin. Person operations
use an already available session via `--session-cookie-file`; no new credential
is requested. Agent self-policy reads use the existing scoped key. List output
includes `next_cursor` when another page is available; pass it as `--after`.

`assistants.PolicyReader.ReadPolicyTx(ctx, tx, tenantID, principalID)` returns
policy together with principal ID, owner, status and revision in the caller's
existing tenant transaction. Mutation callers hold the tenant fence so policy
edits cannot race their final authorization. `Module.ReadPolicy` also provides
an independent bounded snapshot for read-only callers. Callers must reject
inactive principals and retain existing authorization. Adapters,
outward-action approval enforcement, budget accounting,
Routine ownership and lead delegation are separate AEON-754 slices; this change
persists their policy inputs without claiming enforcement in those paths.
