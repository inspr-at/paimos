# START project retention (AEON-1478)

Projects created through the START project-draft route receive a durable
`start` creation-source receipt in the same transaction as their records.
The short-lived handoff draft is not the receipt. Older imports are recovered
in bounded batches from their server-authored START creation events. Titles,
project keys and editable fields never establish import provenance.

Retention applies at 12 calendar months after project creation. Any subsequent
person-authored change to the project or its work, knowledge, files or comments
keeps it, including person-authored events recorded before this feature shipped.
The initial import and later agent-only activity do not count as engagement.
A person with `nodes.write` on the project can also permanently engage it with
`POST /api/projects/{projectId}/import-retention/engage`. Repeating this request
has no additional effect. Projects without positive START provenance are excluded.

Workspace settings reuse the existing Workspace settings cards. People with
`settings.manage` can enable retention and select dry run. The configured START
tenant and tenants with imported projects default to enabled, with dry run on.
Other tenants default to disabled. `GET/PUT /api/settings/import-retention`
exposes the policy with revision checking; stale writes return 409. The period
is fixed at 12 months and the action is deletion. There is no archive option.
`GET /api/settings/import-retention/candidates?limit=20&after=<uuid>` previews
candidates without modifying them, whether the policy is enabled or disabled.
The maximum page size is 100; `next_cursor` identifies the next page.

The existing handoff expiry loop processes at most 16 tenants and eight projects
per tenant per pass. It rotates checked candidates, including dry runs and
failed deletes, so one protected record cannot starve later candidates. Each
project has its own transaction. Settings, provenance and person engagement
are checked again under the tenant/tree access-change fence before deletion.
Transaction and record budgets bound the work. Repeated runs are idempotent.

Live mode hard-deletes the project tree, including work, knowledge and ordinary
relations, attachments, aliases and search records, with one atomic
`import_retention.deleted` audit event and a minimal source/deletion receipt.
Existing append-only audit history remains intact. Transcript bytes are released
from their attachment/history ownership when the node is permanently removed;
the existing file garbage collector reclaims unreferenced bytes after its normal
grace period. Shared blobs remain while another owner needs them.

Nested projects remain independent ownership boundaries. Existing foreign-key,
process, financial and immutability protections are never bypassed. If protected
linked data prevents deletion, or a project exceeds the 1,000-node/attachment
budgets, the entire project deletion rolls back, no deletion event is committed,
and the scheduled pass reports an error. Such projects require operator follow-up;
a failed or partial pass is never reported as a successful deletion.

The settings UI reuses existing components; needs Opus design.
