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
key cannot. The receiving browser creates a state and an independent PKCE
verifier, keeps both in its person-bound pending record, and sends only the
S256 challenge to the confirmation page. Confirmation is a same-origin POST.
The home instance stores only a SHA-256 code hash, bound state hash, challenge,
origin and a 60-second expiry. The fixed receiving address is
`/instances/connected`; its fragment carries a single-use code and state.
The browser removes that fragment before authentication/return-path processing
or rendering. It checks its pending person's identity and configured sibling,
then exchanges code, state and verifier in a CORS POST with credentials omitted.
Only the no-store response body carries the 90-day read-only bearer. Successful
exchange atomically consumes the code and stores only the new bearer hash.

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
origins receive CORS for GET and DELETE with Authorization and POST exchange
with Content-Type, including preflight, without
Access-Control-Allow-Credentials. A credential is also bound to its receiving origin.

A stolen raw grant can read that person's current summary until expiry or
revocation, including by a non-browser client that supplies the bound Origin.
Origin binding is not protection against theft of a bearer credential. It cannot
sign in, read full decision context, change anything, issue another grant or
exercise another person's authority. It may revoke only its own grant.
Reads are limited to 12 per minute per token hash within each server process,
with 429 and Retry-After when exceeded; the browser keeps that connection as
unreachable, retaining its dated last answer. Usage timestamps update only
when older than five minutes. Normal polls do not take the tenant writer
fence; due usage writes reauthorize under that fence. The fresh post-read
snapshot still rejects exact revocation, expiry, deactivation and access loss.
SHA-256 protects the high-entropy grant at rest. Personal settings on the home instance list issued grants without hashes
or raw values and revoke them immediately. Removing a connection on the receiving
browser first revokes it on the home instance and then deletes its local
credential. Failure keeps the credential so Disconnect can be retried and
never reports success. Replacement revokes the previous grant before exchanging
and adopting the new one. Explicit sign-out clears that person’s stored grants,
notification claims, settings and pending verifiers, leaving other people’s
storage alone. Home-instance revocation ends the grant for every copy.
Permission loss and inactive accounts also end reads.

Unknown discovery or an unread connected grant prevents a global all-clear
statement. Unconnected and ended connections contribute no items. Unreachable
instances retain only this tab's last successful summary, explicitly dated;
expired/revoked connections discard cached items. Consolidated badges include
known sibling totals, and their tooltip shows each instance and stale/unknown
coverage. Goals expands sibling rows only on click and remembers its row slots.
Local items, including Coming, precede the instance groups. Goals shows a
home-Desk link only when more than five sibling items wait; shorter lists
already show every item. Row actions share the block's right edge. On phones,
each sibling title has its own full-width text line with a clip-tip, followed by
project and age. The confirmation account shows both name and email.
When a sibling is unreachable, the local empty line says only “Nothing waits
here”; the sibling section header carries its unavailable state and dated cache.

Browser notices use the existing device opt-in. The initial read establishes a
baseline; only items created after the previous successful source read can
notify. Items merely entering the top five never notify. Claims and the source
watermark survive reload, while a new grant also excludes its initial backlog.
Eligible item identities are claimed once in browser storage,
with a browser lock for multiple tabs. Notices open the exact home item. Where
page notifications are unavailable, the existing service worker uses a local
handoff with the home URL in a fragment; the app rechecks the local person and
configured origin before navigating. Sibling polling/notification discovery runs
while the app is open; no server polls other instances in the background.

The Goals line, confirmation and personal settings follow Tick spec v1. Tick
v2 supplies consolidated count breakdowns, remote Desk/preview groups, the
Decided-tab home links, notification copy and per-instance notification switches.
The shared sync line retains the local read status and includes each sibling’s
read status and age in its tooltip, including cached or ended connections.

A stolen callback code, including the entire value recovered from browser
history or history sync, cannot grant a summary read. It cannot be exchanged
without the independent PKCE verifier retained in the receiving browser's
person-bound pending record. It expires in about 60 seconds and is unusable
after successful exchange. Code plus state plus the matching verifier can
redeem the still-unused code during that window; origin headers alone cannot
prove possession. A stolen bearer still permits the bounded summary reads
above until revocation, expiry or access loss. No code or bearer is an
interactive browser session. A same-origin XSS can still steal browser storage;
non-extractable request proofs, home-instance Disconnect all, and clearing old
peek cookies on discovery remain follow-up work.
