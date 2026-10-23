# Assistants on Agents

Hosted assistants appear between capacity and Sessions on the existing Agents page.
Opening an assistant uses the same right-hand dock as session details; phones use
a full-height sheet. Assistants are excluded from Sessions when their principal
identity is known. Lists show five rows initially, with a browser-local expansion
preference scoped to the current person and workspace. Live reads preserve row
order; closing the panel or focusing the page permits a new status/name sort when
the pointer and keyboard focus are outside the section.

The identity and policy API supplies names, vendor, project/scope ceilings and an
exact weekly budget. Empty project allowlists mean **no projects**. Policy editing
requires a person who owns the assistant and currently holds both `agents.create`
and `keys.manage`, matching the server's Policy authority. Edits capture the
original revision, use a pinned draft Save bar and retain drafts on failure or
conflict. Outward-action approval and the prohibition on asking people for secrets
or manual steps are locked. The server records successful changes in its event
log, shown in Policy and Activity when `events.read` is available. History is a
bounded, explicitly partial view of the last 90 days.

The connect action reuses `/agents/register-agent?kind=assistant`, also available
from New. It creates an identity with no keys, no allowed projects and zero spend
authority. It does not claim to establish a working vendor adapter. Disconnect
uses the existing atomic identity-disable/key-revocation endpoint. Agents can
read their own policy through the self-policy endpoint; they cannot edit it or
list other assistants.

At this revision, assistant adapter/receipt reads (AEON-1702), action requests
(AEON-1703) and owned routines (AEON-1704) have not merged. Their capabilities are
closed: the five tabs show explicit unavailable states instead of fabricated
health, activity, spend, approvals or routine counts. Pause/resume, approvers and
reset weekday likewise remain unavailable because the current policy contract
does not expose them. No sample values appear in product code. EN/DE strings are
kept in the assistant catalog and follow the app's shared display language.
