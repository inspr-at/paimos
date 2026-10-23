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

Assistant approval links use `/decision-desk?assistant=<principal-id>`. The
existing Desk applies this additional filter to its asking-principal identities
and removes it with Show all. Invalid identifiers fail closed and show an invalid
filter message; source failures remain visible.
This does not add any decision controls to the assistant panel.

Owned routines (AEON-1704) use the existing recurrence list and its new agent-owner
read. Discovery is limited to two 100-definition pages and six concurrent reads;
larger lists disclose truncation. People with current project routine-management
permission can pause/resume using the captured revision. Failed writes keep the
last confirmed state. Result links and Activity read the latest run of up to 20
owned routines, rechecking the run's frozen owner before attributing it to an
assistant. The bounded history window stays explicit. Production dispatch remains
closed under D's policy/budget guard; this UI does not enable it.

Assistant adapter/receipt reads (AEON-1702) and action requests (AEON-1703) have not
merged. Their capabilities stay closed instead of fabricating health, spend or
approvals. Assistant-wide pause/resume, approvers and reset weekday likewise stay
unavailable while their APIs are absent. No sample values appear in product code.
EN/DE strings are kept in the assistant catalog and follow the shared app language.

The section uses the Sessions card style, with its heading and the page layout
control inside the card header. That same control stays reachable in the existing
sticky dock when its card scrolls away. Missing row metrics use muted dashes with a
"Not available yet" tooltip; only the Status cell names the unavailable status.
Overview explains the pending spend, adapter and approval connections once.
Assistant-wide Pause is disabled in the actions menu, with a desktop tooltip and
an inline reason in the phone sheet. Policy editing labels the budget section
"Budget" and its amount "Per week". Routine activity shows the routine name and
relative age, with more desktop column space while the other metrics are pending.
