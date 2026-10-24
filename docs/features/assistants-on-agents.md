# Assistants on Agents

Hosted assistants appear between capacity and Sessions on the existing Agents page.
Opening an assistant uses the same right-hand dock as session details; phones use
a full-height sheet. In split view, Sessions scrolls in the space below the
Assistants card so selecting a row preserves its position. Assistants are excluded from Sessions when their principal
identity is known. Lists show five rows initially, with a browser-local expansion
preference scoped to the current person and workspace. Live reads preserve row
order; closing the panel or focusing the page permits a new status/name sort when
the pointer and keyboard focus are outside the section.

The first Assistants card appears with Sessions after the initial metadata and
permission reads settle. This keeps it from flashing above a still-loading
capacity row or moving when a linked session opens its dock.

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
Open requests come from the shared, permission-checked Desk projection and match
the asker's principal id. Waiting assistants sort first on initial admission;
later updates preserve the row order until the existing focus/close boundaries.
The approval count is its own keyboard-reachable link. Partial or stale Desk
coverage marks counts with `+`; an incomplete page with no known matching asks
links to Check approvals instead of claiming zero. The panel lists the known
requests and links to each exact Desk item. Recent decisions remain unavailable.

Owned routines (AEON-1704) use the existing recurrence list and its new agent-owner
read. Discovery is limited to two 100-definition pages and six concurrent reads;
larger lists disclose truncation. People with current project routine-management
permission can pause/resume using the captured revision. Failed writes keep the
last confirmed state. Result links and Activity read the latest run of up to 20
owned routines, rechecking the run's frozen owner before attributing it to an
assistant. The bounded history window stays explicit. Production dispatch uses
the server policy/budget guard; this UI grants no execution authority.
Discovery runs on initial admission, when an assistant panel opens, and after
batched assistant or recurrence events. The 20-second identity poll reuses those
routine facts instead of repeating owner and run-history reads.

Adapter/receipt metrics (AEON-1702) have no connected reader here. Outward action
requests (AEON-1703) use the existing Decision Desk and its project-scoped person
decision route. This section has no connected per-assistant decision-history
reader; health and measured spend likewise remain unknown. Assistant-wide
pause/resume remains unavailable. Approver and reset-weekday editing controls
remain unavailable here; configured values survive edits to other policy fields.
Policy evaluation events appear in row activity and the existing Policy/Activity
tabs, including the refusal filter. No sample values appear in product code.
EN/DE strings are kept in the assistant catalog and follow the shared app language.

The section uses the Sessions card style, with its heading and the page layout
control inside the card header. That same control stays reachable in the existing
sticky dock when its card scrolls away. Missing row metrics use muted dashes with a
"Not available yet" tooltip; only the Status cell names the unavailable status.
Overview explains the pending spend and adapter connections once.
Assistant-wide Pause is disabled in the actions menu, with a desktop tooltip and
an inline reason in the phone sheet. Policy editing labels the budget section
"Budget" and its amount "Per week". Routine activity shows the routine name and
relative age, with more desktop column space while the other metrics are pending.
