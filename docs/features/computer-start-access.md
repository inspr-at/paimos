# Computer start access

`run.start_on_computer` is a person permission at workspace or computer scope.
Built-in Owner and Admin roles receive it through their Go templates on every
computer; ordinary Member roles do not. It cannot be issued to agent or owner
workstation keys and is absent from pairing's runtime permissions. Pairing
approval, computer ownership evidence and account-use approvals grant no such
permission.

Computer grants use existing `role_bindings` with `scope_type='computer'`,
`computer_id` set and `scope_id` null. They confer no project permissions, even
when their role contains project permissions. Group subjects have a reader seam;
group writes are not available yet.

The additive person-only API is:

- `GET /api/agent-pairing/computers/{computerId}/access` with `account.read`.
  Returns `admins: true` and the other active people who can start, with a bounded
  `people` page and `next_cursor`. `limit` is 1–100 (default 50); `cursor` is the
  last principal UUID. Workspace Owner/Admin people are represented by Admins.
- `PUT /api/agent-pairing/computers/{computerId}/access/{principal_id}` with
  `{ "role_id": "…" }`. Uses the same `members.manage` guard as project access,
  at workspace scope, plus the actor's current start authority on this computer.
  Roles must contain the computer start permission; Owner is workspace-only.
- `DELETE` on that same person path removes only the computer binding and needs
  workspace `members.manage`. Workspace grants remain effective.

Writes require a same-origin signed-in person, recheck authorization under the
shared tenant/tree/pairing fences, and append
`agent_pairing.computer_access_granted` or
`agent_pairing.computer_access_revoked` audit events. Repeating an unchanged
grant creates no duplicate event. The existing role editor continues to refuse
deleting or reassigning a role still bound at computer scope.

Person run creation checks the selected agent principal's paired computer.
Final claim checks resolve the authenticated claiming daemon's computer and
the run's stored `acting_person_id`, including ordinary agent-run claims,
lead-runtime claims/replays, recovery claims and native worker claims after
local observation. They retain the existing account-use rules. The dedicated
authorization reader is `internal/authz/computer_access.go`.

This is dual-read groundwork: NULL acting people keep the previous claim
behavior until S1-07. This feature does not fill `acting_person_id` or infer it
from an approver, account owner or computer owner; S1-03 supplies that binding.
There is no UI change in this package.
