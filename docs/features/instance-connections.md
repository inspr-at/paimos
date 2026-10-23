# Personal instance connections

AEON-1709 consolidates Waiting on you in the Decision Desk, its header badge
and preview, browser tab title, Goals, and opted-in browser notifications.
Decisions open in one click on their home instance. This instance never makes
a decision on a sibling, fetches a sibling, or receives its credential.

Deployments name themselves with `AEON_INSTANCE_TAG` and list exact trusted
sibling web origins in `AEON_SIBLING_INSTANCES`. Both deployments must configure
each other. Existing content-security-policy support admits only those origins.
No account role or credential belongs in deployment configuration.

An active signed-in person chooses Connect. A top-level page on the sibling
shows their account and asks for confirmation. Any person can connect; an agent
key cannot. Confirmation is a same-origin POST with a random state. The home
instance consumes its hash once and issues a 90-day read-only summary grant.
It stores only the grant hash, person, receiving origin, timestamps and state
hash. The fixed receiving address is `/instances/connected`; the credential
travels only in its fragment. The receiving browser verifies the pending state,
its ten-minute lifetime, local person and configured sibling, consumes it once,
and removes the fragment before authentication/return-path processing or rendering.

Credentials stay in browser storage, partitioned by local tenant and person.
They are never saved through preference APIs, displayed, or sent to the other
instance's server. One private store makes sibling reads with Authorization and
credentials omitted, sharing the local Desk's polling cadence and focus refresh.
The retired peek cookie is never accepted or issued; login/logout clear old copies.

The summary rechecks the active person's current authority and each native
source's visibility, using the same Desk projection as the home page. It exposes
five first lines by default (title, project key, asker, creation time and exact
app link), expanding on request to at most 100 first lines with explicit partial
coverage and a home-Desk link for larger sets, total open/held counts, the first upcoming step and an as-of timestamp.
There is no context, answer, option, decision or write authority. Exact configured
origins receive CORS for GET with Authorization, including preflight, without
Access-Control-Allow-Credentials. A credential is also bound to its receiving origin.

A stolen raw grant can read that person's current summary until expiry or
revocation, including by a non-browser client that supplies the bound Origin.
Origin binding is not protection against theft of a bearer credential. It cannot
sign in, read full decision context, change anything, issue another grant or
exercise another person's authority. SHA-256 protects the high-entropy grant at
rest. Personal settings on the home instance list issued grants without hashes
or raw values and revoke them immediately. Removing a connection on the receiving
browser deletes its local credential and offers Undo; home-instance revocation
ends the grant for every copy. Permission loss and inactive accounts also end reads.

Unknown/disconnected instances prevent a global all-clear statement. Unreachable
instances retain only this tab's last successful summary, explicitly dated;
expired/revoked connections discard cached items. Consolidated badges include
known sibling totals, and their tooltip shows each instance and stale/unknown
coverage. Goals expands sibling rows only on click and remembers its row slots.

Browser notices use the existing device opt-in. The initial read establishes a
baseline; subsequent new item identities are claimed once in browser storage,
with a browser lock for multiple tabs. Notices open the exact home item. Where
page notifications are unavailable, the existing service worker uses a local
handoff with the home URL in a fragment; the app rechecks the local person and
configured origin before navigating. Sibling polling/notification discovery runs
while the app is open; no server polls other instances in the background.

The Goals line, confirmation and personal settings follow Tick spec v1. Tick
v2 supplies consolidated count breakdowns, remote Desk/preview groups, the
Decided-tab home links, notification copy and per-instance notification switches.
The current local refresh tooltip includes each sibling; the separate AEON-1700
sync-line slice must preserve that tooltip when combined by the coordinator.
