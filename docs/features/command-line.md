# Command line

`paimos` is the agent command line. `paimos serve` still runs the server. Existing doctrine commands keep their shape.

CLI issue and knowledge updates, estimate plans, project-tag attachment and
declarative plan updates send the revision of the node they read. A concurrent
edit returns a conflict instead of replacing newer fields; read the current
node before retrying. If a tag was created but could not be attached, the error
names that retained tag. A failed plan leaves earlier successful writes applied.

```sh
paimos auth login --url https://aeon.example --name default --key-file ./agent.key
paimos whoami
paimos issue list --project AEON
paimos mcp
```

`aeon status help --json` (also `paimos status help --json`) reads
`GET /api/status/help`: the ordered status definitions, hints, Queued explanation
and effective workspace rules. `--project KEY` resolves that project's
Inherit/On/Off override. The help sheet and agents use the same definitions.
The endpoint is read-only tenant metadata available to authenticated agents,
including keys without scopes or role bindings. People retain the `nodes.read`
permission check, which denies customer sessions without that permission.
Project names and overrides still require project visibility; inaccessible projects return 404.
Customer portal restrictions and pairing lifecycle checks remain in place.
This read grants no ticket or settings write permission.
The API reads live Status autopilot limits when its settings tables are present,
otherwise it explicitly reports the defaults. Queued means Open or Blocked in
the work queue (AEON-522), rather than another stored status.

Work **merged into another ticket** gets `cancelled`: it will not be done
separately. Record a reason naming the destination ticket. `done` means the
completed code was merged, or a non-code result exists and passed review;
`merged` and `superseded` are not separate status values.

Tickets and tasks can carry `human_check`, nullable text describing what only a
person can confirm. Create or patch it through the nodes API, and filter lists
with `human_check=pending` or `none` (prefix `!` to exclude); request
`facets=human_check` for counts. A person marks it checked with
`PATCH /api/nodes/{id}` and `{"human_check":null}`. The server records the original
text, person ID and UTC time in `fields.human_check_completed`; replacing fields
preserves that provenance and cannot forge it. Only a person can set another
pending check when it clears a stored completion. Agents may add or edit pending
checks that have no stored completion. Check and Undo in the ticket use the existing revision
preconditions. Part B's automation skips pending checks when moving tickets to
Delivered or Accepted.

Status autopilot starts new workspaces with automatic rules enabled. Workspaces
present when migration 1108 runs enter Suggest mode for 24 hours, including
release publication hooks. Settings → Workspace → Status autopilot → Changes
lists proposed status moves and attention flags with Apply / Dismiss. Proposals
leave tickets untouched; Apply checks the current ticket and rule, and Dismiss
prevents that status episode from returning. An owner can explicitly select
**Enable automatic changes** to end the upgrade review period early; ordinary
settings saves and project On do not end it. The worker rescans when the review
period expires or an owner confirms, including within the same UTC day.

Operators can set `AEON_STATUS_AUTOPILOT=off|suggest|on` (default `on`) before
starting the server. `off` pauses all unattended status rules, including release
hooks and attention flags; `suggest` keeps proposals waiting for review without
an expiry. These server modes cap every workspace and project setting. Explicit
Apply is available in Suggest mode and forbidden in Off. Invalid values refuse
startup. An explicitly authorized daemon work claim still starts its ticket;
it does not become an unattended status proposal.

`aeon capacity next codex` shows the server's next eligible account and parallel
capacity; `--json` returns the ordered advice. It never reserves quota. The
Accounts plan uses that same order: soonest weekly/monthly reset, then larger
available cap, then account ID. Recent own use moves an account behind other
eligible accounts, without changing its budget. Short windows still constrain
every admission.
Workspace readers can request advice across their accounts; paired agents and
keys with only `account.probe` see only accounts registered by that agent.

Usage lists configured accounts even before a reading or successful verification,
with their current computer/account readiness and any reported repair. A failed
computer lookup retains the last account inventory and marks it incomplete. Cursor,
Grok and Pi do not expose a supported limit reading; their rows say so instead
of promising a reading after the first run. Release-qualified Codex idle capture
uses the enrolled Node interpreter, checks the account identity in that same
process, and reads `account/rateLimits/read` without starting a prompt. The first
idle capture is eligible after a successful probe; subsequent attempts are
spaced at least five minutes apart. No production Codex idle capability is yet
qualified; its native readings currently come from a run. Revoked local accounts with a dispatch fence
are excluded from `aeon-agentd capacity`; an enrolled but unbound account stays
visible as blocked and cannot prevent a healthy sibling account from working.

Workspace **Low-quota warnings** settings default to an early notice at 10%
remaining and an urgent notice at 3%. Both are whole percentages from 1–50,
with urgent below early (`GET/PUT /api/settings/quota-warnings`,
`settings.manage`; writes require a person). Only measured readings no more
than ten minutes old, before their reset, qualify; a balance without a
percentage denominator cannot produce a percentage warning. Receipts persist
per resource or confirmed login pool, window, reset and threshold across
computers and recovery. A simultaneous crossing produces only the urgent
notice. A newer measured recovery clears the notice; clock passage alone
withholds expired details without claiming recovery. A durable quota/window
observation watermark includes healthy readings and reset transitions, so a
delayed reading from another computer cannot undo newer recovery evidence.
Current availability is separate from notice deduplication: urgent → recovery
→ early still shows limited availability without sending another notice in
the same reset. `/agents` names affected
active sessions with a recorded run/account binding. Their parent lead receives
a durable project-specific inbox summary without quota figures, accepted with
a queued delivery receipt and deadline. Unread summaries expire through the
inbox sweeper or fail when their recipient session ends. Exact figures
on `/agents` follow current owner sharing, including every pooled sibling.
The internal notice step works for reporting daemons without project access;
it sends only to leads with current access and restores the reporter's scope.
A restarted daemon's committed heartbeat still evaluates accepted fresh
measurements for notices while rejecting the previous generation's check facts.

Matching login fingerprints are hints. In Settings → Accounts, open an account
and choose **Pool with…**, then confirm the named accounts use the same vendor
login. Only those confirmed accounts share readings, holds and parallel slots;
confirmation replaces the pool with exactly the named accounts, leaving unnamed
previous members separate. Later enrollments require their own confirmation.
The dialog names every current member plus the account being added, so adding
a third account keeps the existing pair. **Remove … from pool** and **Stop sharing
quota** explicitly name every remaining member; the last pair returns to separate
readings and limits. A changed fingerprint clears that account's confirmation.
Existing accounts
start unconfirmed after migration 1054; prior holds can still settle or release.
The person-only API is `PUT /api/agent-accounts/quota-pool` (`account.manage`).
Ticket pins require edit permission in the ticket's visible project. If a queued
account leaves its routing group, routing releases its old hold and slot before
choosing a current member or returning a visible wait. Claims also release obsolete
quota or group holds before returning a conflict; a daemon that has already
journaled the route observes the unreserved queued run and routes again. Group
edits replace only visible project memberships and preserve hidden project fences.

Capacity learning uses tenant-local readings and usage only (AEON-388,
migration 1020). Three matching run samples enable a decaying p75 hold; five
observed work days enable an Auto reserve normalized to the window's usable
work hours. Managed overlap is excluded from own-use learning. Blind accounts
show consumption until a vendor stop and a known or observed cycle support an
estimate. Estimates carry uncertainty and evidence, never replace a fresh
measurement, and never lift a vendor denial. Aging readings include observed
burn; off-day pacing uses learned throughput to spend what would otherwise
expire. The Accounts disclosure shows evidence and hours/Away/sleep suggestions;
only an explicit save changes a schedule. Learning state is bounded and
contains no local paths or credentials.

The learning regression fixtures cover twelve-run p75 holds, five-day Auto
reserves, blind-limit calibration and replay, exact-model token conversion,
fresh-measurement precedence, tenant isolation, and weekend throughput. Browser
coverage exercises the shared Agents/Usage evidence and explicit hours action
at 1600/390 pixels in light and dark themes.

`aeon capacity next codex --env` prints a shell-quoted config-home export only
when the selected account belongs to the authenticated local agentd. Use
`--shell fish` for fish, `--socket` for an explicit daemon socket, or
`--setup-root` for non-default pairing state. Paths stay on that computer and
never enter the server response. The command advises outside launchers; it
does not enforce their consumption.

A terminal vendor-limit failure waits on its account when the reset is within
20 minutes. Longer stops create one linked retry on the next eligible account
at the next daemon poll, preserving the work order and any person-selected
account fence and run-level group target through consecutive handoffs. The
original session records the handoff. The local daemon
requires proof that the previous process stopped and the same recorded workspace
and branch before continuing. No eligible account means a visible vendor wait;
Run now once is never inherited by an automatic retry. Account holds remain
1% until learned run costs are available (AEON-292 T6).

Named instances and the default live in `~/.aeon/config.yaml`. The agent API key is read from `--key-file` or stdin, never echoed, and stored under `~/.aeon/keys/` mode 0600. `AEON_URL` together with `AEON_API_KEY` (or `AEON_API_KEY_FILE`) is a process-only target. When the binary is `paimos`, `PAIMOS_URL` and `PAIMOS_API_KEY` work the same way.

Classic colleagues without a sign-in identity can join through **Settings → Access → People → Imported from classic → Invite this person**. The invite starts with their email and imported access; the administrator confirms roles they may grant. On acceptance, one active, unlinked imported account with exactly the same verified email (case-insensitive) in this workspace becomes an alias of the new person, with an audited reason. Classic identities and history remain intact. Multiple unlinked matches require manual **Link to person**, including inactive records or records that already have aliases; already linked, deactivated or existing link-target accounts are never automatically linked. A token alone cannot link an account. Invited access stays authoritative even for project-only invites; sign-in and later imports never restore workspace access from the alias's classic roles.

Create a CLI/script identity at **Settings → Access → Agents → New agent**: give it a name, a description, a role ceiling, and workspace or selected-project access. The **Ticket worker** purpose selects project-only access and suggests the smallest role you may grant that covers `nodes.read`, `nodes.write`, `comments.read`, `comments.write`, `events.read` (ticket activity and history), and `search.read`; Admin is never suggested automatically. Role options explain their effect for agent keys. The description is prefilled from the purpose and project keys, stays editable, and may be cleared after confirming **Create without a description**. A person with `keys.manage` can create it, within their own permissions; agent and role changes are audited together. The next sheet offers one-click **Full access**, **Ticket worker** and **Coordinator** presets and previews their scopes before applying only those permitted by the registry, creator and agent role. Scope rows show their label, id, registry explanation and risk; search matches names, ids, groups and descriptions. Built-in Owner, Admin and Member roles exclude `recurrences.manage` for agents; recurrence automation needs an explicit custom-role grant and a key scope. The same previews and search are available when editing scopes; rotation preserves original scopes only within the live ceiling. The first key is shown once, with a copy button (manual selection if clipboard access is blocked) and the `paimos auth login --name <workspace> --url <origin>` command (named after the workspace's slug, else the host). Paste the key at the hidden terminal prompt. Computer pairing uses agentd and needs no API key; its page links directly to this alternative.

Manual rotation rechecks live permissions inside the replacement transaction. Shared roles combine workspace and project grants; generated private roles also cap replacement scopes at their configured workspace permissions, even when preserving the old scopes. A project binding cannot restore a scope removed from that private role. The coordinator’s derived project-only `rules.read` remains available. A rejected rotation leaves the old key unchanged and creates no replacement or audit event.

People with `keys.manage` can edit an active key in **Access → Agents → Edit scopes** without replacing its secret. **Full access** selects every agent-grantable scope within the agent’s role, editor and original creator ceilings. Per-group **All / None** changes only visible scopes in that group; presets and **All** never extend the agent’s role. A quiet note names the person-only actions: managing members, roles, keys and settings, reading keys, the access audit log, approval decisions, rule publishing, conversation watching, harness force-stop and recovery, ownership transfer and the customer portal. **Expires after** starts at **Keep current**, or can set 30, 90 or 365 days from saving, or **Never**, without rotation. The API accepts optional `expires_at`: omission preserves expiry, `null` means Never, and a timestamp must be in the future. Scopes and expiry apply atomically, retaining the same ID and secret. The sheet names the agent role when it limits a scope. A person who also has `roles.manage` may tick a permission they hold and confirm adding it to the agent's custom workspace role and key together. Shared-role impact is shown before confirmation; built-in roles remain fixed. The server rechecks the editor, original creator and live role under the tenant/key lock, then writes both changes in one transaction with one audit entry. Generated agent roles include `models.read`, `models.report` and `models.refresh`; migration `1061_agent_roles_models_read.sql` adds only `models.read` to existing custom agent roles without changing key scopes. Unknown stored scopes are pruned and audited when the sheet loads or an edit succeeds. Changes apply on the next request and appear in the access audit. Removing every scope disables the key's access; revoked or expired keys cannot be edited.

Agent key reductions can also be proposed as protected approval memos on the **Decision Desk** (AEON-615). `POST /api/agent-keys/{id}/trim-proposals` requires `approvals.request` and accepts an idempotent `request_id`, the analysed `expected_scopes`, a strict-subset `candidate_scopes`, expiry (up to seven days), and evidence with an observation date and a consequence for each dropped scope. It creates a proposal and leaves the key unchanged. Confirm the ops candidate with OPS before proposing it. A person with `keys.manage` reads `/api/key-trim-proposals` and uses **Approve** or **Decline** in one click. Approval rechecks the live scope digest, recent use and editor/agent/original-creator ceilings under the tenant and key usage fences; scope changes audit `agent_key.scopes_changed` without credential material. The applied memo offers **Restore** for thirty days. Restore compares against the applied candidate digest and rechecks live grant ceilings, so it cannot overwrite another scope edit or restore permissions the key may no longer hold. Replayed decisions never apply or audit twice. Both Open and Decided have **Next key trims** and **First key trims** controls when paginated; each displays at most 100 key trims, with continuation to any later proposal.

Keyed-agent transactions acquire the authenticating-key usage fence before tenant, tree or resource locks, including harness registration and node moves. Different keys remain independent. Proposals declare their complete target/authenticating-key batch before transaction entry and acquire it in sorted order; person-owned decisions acquire the target-key fence before the tenant access fence. Later `RequireTx` checks re-enter the held fence and re-read the live scope ceiling. Callers must carry the authenticating principal in the `InTenant` context. Usage recording and scope reductions remain atomic, with the event counter acquired last.

Successful authorization records a tenant-isolated timestamp per authenticating key and scope, with at most one persisted write per minute and no request payload, route or address. A trim refuses to drop any scope used in the last 24 hours, with a conservative one-minute margin for debounce. Reads recheck that guard and expose a blocked memo if use or scopes changed after analysis. Missing timestamps mean unknown use; they do not establish that a scope was never used. Protected memo responses and their audit events contain no key secret, prefix or credential hash. Existing key creation, rotation and manual scope editing retain their contracts.

The same change is available as `aeon keys scopes <key-id> --add harness.worker --remove nodes.write --session-file <private-cookie-file>` (repeatable/comma-separated scopes). The file contains an existing signed-in person's `aeon_session` cookie value; `-` reads it from stdin without echo. Use `--url` or the configured instance URL. This command neither stores nor prints the cookie; agent credentials cannot manage scopes. Permission denials can include `reason_code` (`missing_role_permission`, `missing_project_access`, or `missing_key_scope`); only a missing key scope after role authority passes includes `scope`. Agent session registration also requires `harness.worker`, preventing generations that cannot heartbeat or stop.

`whoami` and doctor's auth check use the same `GET /api/me` client call. A valid session or agent key can read its own identity without a workspace role or extra key scope, including project-only and empty-scope keys. Doctor probes public health and version information anonymously; schema and rules checks retain their own permissions. This grants no access to other workspace data or profile routes. Issue, knowledge, search and onboard commands use the current Aeon APIs. Commands whose API resource is unavailable exit 3. `aeon mcp` exposes `whoami`, `ask`, `ask_status`, issue list/get/create/update/comment, knowledge list/get/create/update, and search over stdio. Work tools reuse the CLI application operations and scoped HTTP APIs; updates preserve revision checks and kind changes remain person-only. JSON field writes preserve exact numeric tokens. Node walks fail explicitly if a cursor repeats or remains after fifty pages. Project-scoped issue search resolves ancestor membership and continues search pages up to the requested limit within the server’s ranked window of 200 matches. An ancestor returning not-found excludes that path from the visible descendant set; other lookup errors and traversal limits still fail the search. Onboarding `--check` compares the complete rendered bundle, options and managed header, normalizing only its generation timestamp.

Model catalog v3 upgrades existing tenants on server startup (or first registry access). It adds the current Codex and Grok CLI pins, retires known-invalid models from resolution, and upgrades only role ladders that still exactly match the v2 defaults. Customized routes and active availability overrides are preserved. An explicitly saved ordinary review order remains unchanged even when it matches the old default. `review-gate-security` uses Grok CLI → Cursor Grok → Codex, stays read-only and excludes the author's family; the normal review gate retains Claude.

Migration 1126 is an expansion: security-review routes live in a separate tenant-isolated table, leaving the published role table and its constraint unchanged. Current catalog reads and route replacement include both tables; previous release binaries continue using the original routes. The refresh tables use explicit forced RLS policies.

**Workspace → Models** shows refresh settings, recent observations, pending profiles and the last scheduled run. Agent reports and automatic profile recording are on by default. Discovered profiles stay disabled until a person accepts them, including for accounts that allow every enabled profile. Reports never reorder ladders. Separate observations preserve immutable profile/run history: two distinct invalid-model failures at least five minutes apart suppress a profile for 24 hours; a successful run clears that pause. Every agent observation requires an available account owned by the reporter on that harness; self-registering a session cannot substitute for enrollment. Health evidence must also match the exact stored model and effort of an active caller-owned session or its managed run pin. Positive usage for a different model remains accounting data and cannot clear suppression; stopped sessions and unavailable or missing accounts also contribute usage without changing model health. Stable report IDs make retries idempotent for seven days; each principal may retain at most 1000 receipts and submit at most 200 new receipts per hour. Existing workers can report their own harness via register/heartbeat or `/model-reports` with their worker lease; standalone `aeon model report --file observations.json` needs `models.report`. Standalone model commands require both the workspace role permission and key scope; existing keys retain their scopes.

`aeon model refresh` needs the agent-grantable `models.refresh` permission and accepts no profile or route writes. Refresh defaults to 1440 minutes (configurable from 60 to 43200), shared by agent triggers and scheduled runs. Vendor calls run after the reservation transaction commits, leaving catalog reads and routing available; all vendor requests share a 30-second deadline, with at most five pages and 500 identifiers per vendor and 200 observations/profile additions per run. Optional vendor API discovery is **off by default**. A person with `models.manage` may save a model-list key for an enrolled vendor account and enable it. Keys are encrypted with a separate derivation of the server's stable session key, bound to the tenant/account/vendor, and never returned or audited. Server-key rotation requires replacing these discovery keys. The job only lists models at fixed OpenAI, xAI, Anthropic or OpenRouter endpoints, refuses redirects, bounds responses and handles Anthropic pagination. API sightings do not establish CLI success; outages keep the last good catalog and mark the source stale. Cursor discovery uses agent reports. Sonnet xhigh remains absent because it has not been verified. Every completed refresh records `model.catalog_refreshed`; automatic additions do not change tenant routing policy.

Refresh regression coverage includes startup upgrades, custom route and active override preservation, agent role/key ceilings, worker leases and harness binding, retry idempotency, suppression expiry, encrypted tenant/account credential isolation, and catalog preservation during vendor outages. The targeted settings browser test checks private defaults, interval changes and proposal acceptance without route writes.

`paimos model resolve review-gate --author-family codex` resolves a reviewer outside the author's family. `--author-family` accepts `openai`, `anthropic`, `xai` and `cursor`, plus harness aliases `codex` → `openai`, `claude` → `anthropic` and `grok` → `xai`. `pi` is ambiguous: pass the model's family explicitly. The API response and CLI JSON echo the normalised `author_family`; omitting it for other roles returns an empty string.

Release metadata lives in `version.json`. The version display uses the pinned INSPR presentation bundle, checked by `just release-check`; historical release metadata remains unchanged.

`node scripts/release-timing.mjs --rollout PATH --release LABEL --json` reports
cut → live, PR → merge, gate → live and rollback timings. `PATH` is a rollout
JSON file or directory; `--fixture scripts/testdata/release-timing/section1.json`
reproduces the recorded release baseline without GitHub access. GitHub calls
use fixed read-only templates. Lists keep a constant page width, de-duplicate
stable `id` / `databaseId` / `number` values and name capped, id-less or
incomplete search results in `collection.truncated`. Only deduplicated stable
IDs count toward `total_count`; duplicate pages cannot hide an unread tail.
The script validates timestamps with explicit zones, ordered intervals, PR
lifetime bounds and numbered rerun coverage (`1..runAttempt`) before computing
metrics. JSON `evidence` records carry `complete` or `partial` with reasons; partial metrics
are null, with observed sample counts reported separately. Forward rollout
records require `direction: "forward"`; `outcome`, release labels and sequence
numbers cannot establish direction. Use `direction: "rollback"` for rollback
records; explicit rollback timestamps also identify legacy rollback evidence.
A forward record may embed a provably later rollback. Complete forward
intervals take precedence over incomplete siblings; conflicting complete
records remain unknown with an ambiguity reason. A rollback never replaces
forward rollout timing, and the newest unfinished rollback remains unknown.
Missing or invalid ordering timestamps leave candidate selection ambiguous;
tied candidates with conflicting content also stay unknown. Identical rollback
intervals can still report their duration. An ambiguous release run cannot
supply a SHA or fall back to PR titles for PR, CI or status-gate timings;
independent rollout gate timestamps remain usable.
Unknown timing retains consistent release metadata for filtering. CI stalls
outside the rollout window cannot produce adjusted timings. The offline
baseline uses clearly marked fixture IDs and explicit forward directions;
its recorded timestamps are preserved.

Session **Messages** shows both directions of that session's conversation, newest
messages at the bottom. `aeon tell PERSON_UUID --project AEON -m 'Reply text'`
automatically uses `AEON_SESSION_ID`, `AEON_SESSION_FILE` (or
`AEON_SESSION_STATE_DIR/session.id`), then the registered harness binding when no
explicit source is configured. Ambient sessions are attached only when active in
the target project; an ended or unavailable session is omitted with a stderr note.
`--sender-session` overrides these sources and strictly requires your active
session in the target project.
Use `--reply-to MESSAGE_UUID` to link an answer to the person's question; linked
answers appear in the same thread even when sent without a session binding.
“Read” means the session acknowledged receipt; “Answered” means an accepted
counterpart reply exists in the loaded conversation. Unrelated principal history
and shared-inbox obligations stay outside the session thread.
