# PAIMOS AEON

The next generation of Paimos: an agents-first, voice-first, multi-tenant work platform in the INSPR family. Hybrid human work stays first-class: a fully dynamic work tree, list, search and a Markdown sidebar viewer.

Status: under construction (release R0). Live at <https://aeon.barta.cm> once R0 ships. Decisions: PPM project AEON, ADR-001 (foundation) and ADR-002 (stack: Go, Postgres 18 + pgvector, Vue 3).

## Develop

```sh
just db-up        # Postgres 18 + pgvector on :55432 (Docker)
just test         # Go tests
just web-check    # web typecheck and build
just dev          # run the server (API on :8080); `cd web && npm run dev` for the UI
```

Project sections have their own URLs: `/p/KEY/tickets`, `/p/KEY/journey`, and
`/p/KEY/knowledge`. A ticket uses `/p/KEY/TICKET`; `?section=journey` or
`?section=knowledge` retains its background section. Tickets is the default,
so its ticket links need no section query. Existing `?view=full` ticket links
still open the full-page ticket at the same address.

If a standing candidate or deployment gate expires or is revoked before
deployment finishes, Journey offers renewal on the Deploy stage. An agent
requests a fresh release-bound approval; its person decider applies it with
`renew_candidate` or `renew_deploy` through the journey actions API, including
the current `release_id` and `expected_revision`. Candidate renewal precedes
deployment renewal and preserves enterprise reviewer independence. Each renewal
appends gate history and advances the journey revision without changing the
release identity. Existing handoffs lose authority; terminal evidence from
before either renewal is historical, so rerun preparation after both renewals,
then request a new deployment. Journey contract `journey/1.2` adds the optional
`next_action.renewal_action`; clients must use it when present. Existing action
keys and reporter major versions remain unchanged.

Wide project headers can show an ambient ticket graph (Display → Graph in
project header). It uses a tilted 3D cloud with an optional elliptic force bias,
fits the densest 85% of nodes by height, and fades out inside the empty space
between the text block and counts. Reduced motion, narrow screens and sparse
graphs suppress it; hover exposes Open graph and Pause. Header framing and text
separation are covered by `web/tests/header-glimpse.spec.ts`.

## Command line

`paimos` is the agent command line. `paimos serve` still runs the server. Existing doctrine commands keep their shape.

```sh
paimos auth login --url https://aeon.example --name default --key-file ./agent.key
paimos whoami
paimos issue list --project AEON
paimos mcp
```

`aeon capacity next codex` shows the server's next eligible account and parallel
capacity; `--json` returns the ordered advice. It never reserves quota. The
Accounts plan uses that same order: soonest weekly/monthly reset, then larger
available cap, then account ID. Recent own use moves an account behind other
eligible accounts, without changing its budget. Short windows still constrain
every admission.
Workspace readers can request advice across their accounts; paired agents and
keys with only `account.probe` see only accounts registered by that agent.

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
account fence. The original session records the handoff. The local daemon
requires proof that the previous process stopped and the same recorded workspace
and branch before continuing. No eligible account means a visible vendor wait;
Run now once is never inherited by an automatic retry. Account holds remain
1% until learned run costs are available (AEON-292 T6).

Named instances and the default live in `~/.aeon/config.yaml`. The agent API key is read from `--key-file` or stdin, never echoed, and stored under `~/.aeon/keys/` mode 0600. `AEON_URL` together with `AEON_API_KEY` (or `AEON_API_KEY_FILE`) is a process-only target. When the binary is `paimos`, `PAIMOS_URL` and `PAIMOS_API_KEY` work the same way.

Create a CLI/script identity at **Settings → Access → Agents → New agent**: give it a name, an optional description, a role ceiling, and workspace or selected-project access. A person with `keys.manage` can create it, within their own permissions; agent and role changes are audited together. The next sheet creates its first scoped key and shows it once, with a copy button (manual selection if clipboard access is blocked) and this instance's `paimos --instance <name> auth login --name <name> --url <origin>` command. Paste the key at the hidden terminal prompt. Computer pairing uses agentd and needs no API key; its page links directly to this alternative.

People with `keys.manage` can edit an active key in **Access → Agents → Edit scopes** without replacing its secret. The saved scopes are limited to the agent's existing role, the editor's permissions and the original creator's live ceiling. Changes apply on the next request and appear in the access audit. Removing every scope disables the key's access; revoked or expired keys cannot be edited.

The same change is available as `aeon keys scopes <key-id> --add harness.worker --remove nodes.write --session-file <private-cookie-file>` (repeatable/comma-separated scopes). The file contains an existing signed-in person's `aeon_session` cookie value; `-` reads it from stdin without echo. Use `--url` or the configured instance URL. This command neither stores nor prints the cookie; agent credentials cannot manage scopes. Permission denials can include `reason_code` (`missing_role_permission`, `missing_project_access`, or `missing_key_scope`); only a missing key scope after role authority passes includes `scope`. Agent session registration also requires `harness.worker`, preventing generations that cannot heartbeat or stop.

`whoami` calls `GET /api/me`. Issue, knowledge, search and onboard exit 3 with `arrives in R1` until those endpoints exist. `model resolve` exits 3 with a not-yet message. `aeon mcp` serves those tools over stdio.

Versioning: INSPR Calendar Versioning, INSPR-CalVer3 (`inspr-calver-3`, `YYMMDDhhmmss.0.0`); releases up to 260929113854.0.0 stay INSPR-CalVer2 history. The version display uses the pinned INSPR presentation bundle, checked by `just release-check`.

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

### Agent work estimates

Estimates are expected **agent hours until ready for review**, separate from a live ETA.
Set them with `aeon issue create ... --estimate-hours 2`, `aeon issue update AEON-317 --estimate-hours 1.5`, or `aeon issue estimate AEON-317 --hours 1.5 --source agent`. `--estimate 90m` remains an alias. Decimal hours and minutes are accepted; values must be greater than zero and at most 200 hours. The optional source asserts the authenticated principal kind; the server stamps the principal and time. Creating a ticket or task without an estimate prints a non-blocking warning. API callers can PATCH `/api/nodes/{id}` with `{"estimate_hours":2}` to change just the hours, or null to clear them; other fields survive. Do not combine this property with a replacement `fields` document.

`aeon harness run-heartbeat --status-file .agent-status.json` reads `pct`, `remaining_min` and `note` on every beat. Bound workers send the percent and a ready ETA anchored to the file's modification time plus the remaining minutes, including zero. An unchanged file's ETA can become overdue; after 30 minutes without an update, the CLI warns `stale_progress`. Minutes must be finite numbers from 0 to 524160 (364 days); ETAs outside the server's allowed window are omitted. Workers with `--worktree` default to that worktree's `.agent-status.json`; coordinators read a file only with an explicit `--status-file` and retain their separate live-ETA reporting. Missing, malformed or refused status files never stop the loop. If the server rejects status-file estimates, the CLI retries the heartbeat without those fields. Status reads retain the credential, symlink and hard-link fence.

Heartbeat responses carry non-blocking `warnings`: a working worker gets `missing_progress` after three accepted beats without a fresh percent, a working session with a bound `ticket_node_id` gets `missing_eta` for its role's absent ETA, and a visible bound ticket/task without valid hours gets `ticket_without_estimate`. Unbound workers and coordinators have no missing-ETA warning or UI hint. Warning-query failures are logged and return an empty array without rolling back the heartbeat. Both heartbeat commands print each code to stderr at most once per ten minutes, with receipts retained across reporter restarts. Run-heartbeat uses its private state directory; one-shot heartbeats use private 0700 directories under `~/.aeon`, including when the lease comes from stdin.

Harness status and heartbeat declare `Aeon-Contract: harness-session/1.6`. The warnings field is optional in the shared session schema and is returned as an array on heartbeats; existing fields and required payloads are unchanged.

Agent drafts show `est.` until a person or a working agent bound to the ticket confirms or changes them. Resubmitting the hours through the estimate command confirms them; provenance is recorded again. The ticket's Estimate control also edits or clears the value. Epics show the sum of direct, visible, open ticket/task children, with estimated-child coverage in the tooltip; nested tasks are not counted twice. The Estimate sort keeps empty values last in either direction. Imported points remain visible as points, not converted to hours.

For a backfill, an agent drafts a JSON plan such as `[{"key":"AEON-317","hours":2},{"key":"AEON-318","hours":0.5}]`, then runs:

```sh
aeon issue estimate --missing --project AEON --from-file plan.json --dry-run
aeon issue estimate --missing --project AEON --from-file plan.json --apply
```

Both modes validate every plan entry and project membership before any write. Apply uses the agent identity, skips work already estimated and checks each node's revision. A concurrent change stops the plan; earlier successful writes remain applied and a rerun skips them. There are no server-side model calls.

## Lead handover and short review/fix jobs

A coordinator registered with the same principal, harness and native session
reference automatically takes over its stopped or heartbeat-lost predecessor's
live children. The transaction records one `harness.adopted` event per child and
`harness.handed_over` on the old lead; stopped children remain historical.
A healthy lead is never replaced. `harness run-heartbeat --role coordinator
--source-session NATIVE_UUID` uses that stable native reference; use a fresh
private state directory for the new process generation. After an unclean
restart, the helper retries an active-generation registration conflict at the
heartbeat interval while its owner process lives, logging each retry. Once the
predecessor's heartbeat expires, registration and child adoption proceed
automatically. For a different native session, pass `--succeeds OLD_SESSION_UUID`
to `harness register` or `harness run-heartbeat`. The predecessor must belong to
the same principal and project. A handed-over generation cannot revive through
a late heartbeat.

On **Agents**, the old lead links to its successor and adopted workers link back
to the old lead. Drag a live worker to a live lead, or choose **Move to lead…**
from its menu. A person must own both registrations or be an owner/admin in the
same project and hold `harness.write`; sessions whose legacy owner is unknown
require an admin. The server checks revisions and rejects cycles. The toast's
**Undo** rechecks rights and hierarchy changes; an ordinary heartbeat does not
invalidate it. Moves preserve ticket bindings and estimates.

For flywheel gates and fixers, wrap the existing command without changing its
own review, sandbox or permission arguments. Retain the launcher's environment
sanitization (including the three `CLAUDE_CODE_*` messaging variables):

```sh
aeon harness run --project AEON --ticket AEON-322 --label "Handover review" \
  --role reviewer --harness claude --parent-session "$LEAD_SESSION" \
  -- claude -p "Review the prepared AEON-322 diff read-only"
aeon harness run --project AEON --ticket AEON-322 --label "Handover fixes" \
  --role fixer --harness claude --parent-session "$LEAD_SESSION" \
  -- claude -p "Apply only the accepted AEON-322 fixes"
```

The helper registers before launching, heartbeats while the command runs and
marks the generation stopped on success, nonzero exit, launch failure or
SIGTERM. It preserves stdout, stderr and the command's exit code; SIGTERM exits
143 after settling the session. It signals only the process group it launched,
using the existing ownership fence, with a five-second TERM grace period.
`reviewer` and `fixer` are worker jobs with a public activity note; reviewers
default to `scout`, fixers to `ship`. Project defaults to the ticket prefix,
agent to the authenticated caller, and harness to a recognized command name.
No command text or arguments are sent to the server. The default private state
directory is retained for recovery; `--state-dir` selects an explicit new one.
A failed stop prints that directory and retains the existing heartbeat retry
intent. Reuse `run-heartbeat` to settle it; `run` refuses to launch a second job
from an already-used directory. These helpers confer no additional permissions
and do not replace the ticket's worker marker or release review gates.

## Install the CLI

Nix installs `bin/aeon` and a `bin/paimos` symlink:

```sh
nix profile install github:inspr-at/aeon#aeon
```

GitHub release assets, next to `paimos-agentd` for the same four OS/architecture pairs and listed in the same `SHA256SUMS`: `aeon-cli-darwin-amd64`, `aeon-cli-darwin-arm64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64`. Put the CLI file on `PATH` as `aeon`; a symlink named `paimos` selects its compatibility mode. For checksum-verified computer pairing, see [Agent integration](docs/AGENT_INTEGRATION.md).

With the reviewed Nix package, run `env "$HOME/.nix-profile/bin/aeon-agentd" pair --url 'INSTANCE_ORIGIN_FROM_GUIDE'` from your working folder (bare `aeon-agentd pair` asks for the origin or resumes the saved instance). Confirm the folder, select detected signed-in harnesses, then enter the 9-digit code in the browser and approve as a person. Pairing creates its own private state; Nix/Home Manager retains service ownership.

On a Mac without Nix, **Connect your machine** shows `brew install inspr-at/tap/aeon-agentd` only when the tap formula matches this release, and the pair line runs `env "$(brew --prefix)/bin/aeon-agentd" pair --url '…'`. Otherwise use that page’s direct download and add `~/.local/bin` to PATH. After browser approval, pairing installs the user LaunchAgent on macOS or systemd user unit on Linux. It retains Homebrew's stable `bin/aeon-agentd` link or `~/.local/bin/aeon-agentd`, so a package upgrade does not leave the service pointing at a removed version. The stable link must resolve to the binary doing the pairing; an unrelated service is never adopted or overwritten.

To remove a pairing, run `aeon-agentd disconnect` and wait for `disconnected` before uninstalling. This freezes new work, requests server revocation, waits for owned processes to drain, then stops and removes its own service. `--once` reports one resumable step; interruption or lost connectivity leaves cleanup pending, and the same command resumes it. Vendor sign-ins and project files are preserved. Homebrew users then run `brew uninstall aeon-agentd`; checksum-installer users remove the `~/.local/bin/aeon-agentd` link and downloaded versions under `~/.local/lib/aeon`. Nix users disable/remove the service and package through their owning configuration's review path. Retain private pairing state until cleanup and any accounting recovery are complete; `--state-root` selects a nondefault pairing.

Claude dependency pins preserve stable Node and SDK links, including Home Manager links. Each probe and start checks the full link chain, ownership, directory permissions, workspace exclusion and the SDK's declared package entry, then launches the resolved physical paths. Existing physical pins remain supported. To replace old pins after an update, run `aeon-agentd repin --harness claude --node-path /absolute/stable/bin/node --claude-sdk-path /absolute/stable/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs` (add `--state-root` for a nondefault pairing). Omit the dependency flags to discover the current global installation. The command shows old and new paths and versions; `--yes` confirms without prompting, including with `--json` in Home Manager activation. Missing old versions show as unavailable.

On macOS, these checks accept user- or root-owned directories writable by the `admin` group (gid 80), including Homebrew's `bin` and `Cellar`; group-writable files, other writable groups and non-sticky world-writable directories remain unsafe. Linux retains its group-write refusal. The known Homebrew repositories `/opt/homebrew`, `/usr/local/Homebrew` and `/home/linuxbrew/.linuxbrew` qualify as installed package prefixes for executable and dependency pins only when both the prefix and `.git` have trusted ownership and permissions. Private pairing state (`--state-root`) must remain outside every repository, including these Homebrew prefixes. The configured workspace and other repository ancestors remain excluded. Unsafe dependency errors name the offending component and explain how to fix it.

Codex discovery accepts `login status` on stderr and labels a confirmed ChatGPT account without an email as **ChatGPT login**. An explicit `--account-context` still requires a matching reported identity. If a shell guard around npm Codex cannot find Node on the service PATH, discovery retries that same guard with a validated, pinned Node interpreter; it preserves the guard and the workspace/permission checks. Discovery uses the account RPC and does not read vendor credential files.

Repin records local `claude-repin-<id>.json` and `claude-repin-<id>-applied.json` events in private pairing state. The running daemon replaces its Claude adapter after its current Claude processes exit. Historical ownership records and pending accounting remain intact and continue reconciling; they do not prevent adapter replacement. A successful `repinned` result requires the daemon's acknowledgement. After 20 seconds without it, the command returns nonzero with `restart remains pending`; the daemon retries automatically, and a daemon starting with the saved pins acknowledges them directly. An offline daemon must be started through its existing service owner. Repin preserves enrollment, credentials, fences and service definitions; it neither re-pairs nor takes ownership of a Nix/Home Manager service.

A pending or failed repin, or a broken Claude dependency, holds only fresh Claude work. Other harnesses and claim recovery keep polling. Local status keeps `ready: true` when any enrolled, unfenced account is ready; setup and Add harness can report connected as soon as one harness passes its probe, while another still shows starting or needs sign-in.

One status model covers the computer: it is ready while at least one harness can work. A failed start, a hold or a pin block is a detail of that harness or account, never `setup_failed` for the whole computer; a computer that only reports starting harnesses stays provisioning.

`harness_statuses` remains the legacy state map; the additive `harness_details` map carries `{state, reason, fix}`, and the daemon's per-account `blocked_accounts` carry `{account_id, harness, reason, fix}`. Both use one reason vocabulary and one fix form, `fix: {kind, command}` with kinds `repin`, `add_harness`, `login` and `restart`. Reasons are `repin_pending`, `dependency_invalid`, `pin_missing`, `pin_partial`, `pin_drifted`, `pin_invalid`, `pin_unsafe`, `login_required`, `starting`, `harness_failed`, `cli_unavailable` and `profile_permissions`: the `pin_*` codes come from the static pin check before launch, `dependency_invalid` from the runtime check of the same dependencies. Claude pin and dependency problems are fixed with `aeon-agentd repin --harness claude`, the same problems on other harnesses with `aeon-agentd add-harness --harness NAME`, a failed start, unavailable approved executable or pi profile permissions by restoring it and resuming with `aeon-agentd setup`. `/agents` shows the command on the affected harness row; a pending repin says “Waiting for repin” and retries automatically without a command. A ready harness may list `attention_accounts` for a proper subset of its enrollments so a healthy sibling cannot erase a block, and `/agents` shows “1 of 2 accounts needs attention” (need when more than one) from `attention_count` plus the derived fix on that row while each named enrollment keeps its own status. An account omitted from a truncated list is not shown as ready. `aeon-agentd status` prints the same reason and command per harness and per blocked account. Raw `harness_errors` stay local.

Unknown reason or state tokens from newer daemons remain visible as “Needs attention” with the raw code, without a guessed command; a newer state appears only in `harness_details`, never in the closed legacy map. Unenrolled, revoked, malformed-code and mismatched harness reports are dropped without interrupting fence sync or cleanup; one value-free warning is logged per server instance. Advisory detail extensions and legacy string fixes are accepted, and commands are always rebuilt from known reasons. Malformed JSON shapes still fail validation. Missing reports stay absent, offline reports are labelled as last reported, and a legacy daemon clears prior reports when it omits them. The CLI retains its existing approved physical-executable policy, including Homebrew installations; the stricter ownership and directory checks apply to Node and SDK pins.

Invoking the binary as `paimos` gives the paimos-compatible CLI. `PAIMOS_URL` (with `PAIMOS_API_KEY` or `PAIMOS_API_KEY_FILE`) is the process-only target.

Claude accounts offer **Show Aeon in your Claude status line** in Settings → Accounts.
Only that explicit opt-in lets the enrolled daemon install `aeon statusline` in
its private Claude home; an existing user status line is preserved. The command
prints one plan line and sends only the two supported quota windows to the local
owner-only agentd socket. The registering agent reports at most once per minute,
including across daemon restarts. The toggle requires `account.manage`. Disabling
removes only the matching owned entry, even after the executable leaves PATH or
its installation path changes or contains spaces.

Structured vendor limit hits settle managed runs as `vendor_limit`; sessions show
Throttled with the vendor's reset when known. Codex model-specific bucket signals
stop only the matching model; account-wide denials still apply. Known denied
windows are reported at 100%; missing bounds remain unknown. Accounts publish
`reading_support` and a tenant-keyed HMAC of a verified vendor account ID, never an
email, token or local path. Missing verified IDs leave the fingerprint empty.
Doors with the same fingerprint share allowance windows, outstanding reservations
and vendor denials within the tenant; group membership and schedules stay on each
door. Reservations and launch validation recheck project fences and ticket pins.
Settings displays `aeon use <harness> <account-id>` so labels containing spaces or
shell metacharacters remain plain display text. The CLI still accepts labels;
only the local owner-only agentd resolves the selected account's environment.
Claude idle `get_usage` and Grok billing captures remain disabled until the coordinator verifies a
quota-neutral exchange and adds an exact-version, exact-binary capability; this
fixture implementation does not approve a production vendor capture.

Managed `aeon-agentd` Codex runs report fresh app-server thread usage to
`POST /api/projects/{projectId}/harness-sessions/{sessionId}/usage` with the
registered session's worker lease (`harness.worker`). Input includes cached
input; missing cache remains unknown. Thread totals become per-model cumulative
snapshots, with exact receipt retries and a five-second final flush. A model
change needs explicit usage-model or `model/rerouted` evidence and a matching
last-usage interval. Reroutes bind to the same turn; subsequent usage needs fresh
model evidence because reroutes apply to individual requests;
ambiguous or malformed captures remain provisional. Final usage requires the
acknowledged turn ID, one matching completed terminal, all counters known, and
owned child stop plus stdout EOF within a two-second drain deadline. One bounded
terminal candidate can wait for the start acknowledgement; duplicate terminals
or usage after a terminal invalidate completion. Failed start, stream loss or
drain timeout cannot publish final receipts. An archived generation (410) detaches
the reporter without signalling the process; existing run settlement continues.
Managed adapter completion and failed starts release their owned stdout reader
even when another writer keeps the pipe open. Cleanup suppresses further stream
callbacks and bounds the wait for an in-flight callback; local closure never proves EOF
for a final Codex receipt. `Stop` keeps its process-only role so an observer can
call it without waiting on itself.
The reporter retains bounded normalized state in memory, never raw output or
worker leases on disk. Restart/crash recovery and external worker capture remain
separate work; an unavailable endpoint can leave the last snapshot provisional.
Cursor's managed ACP usage currently supplies cost only to run settlement, so
its session tokens remain unreported. No second CLI or transcript backfill is
used. Pricing stays in the API: `GET`/`HEAD /api/model-prices` requires
`harness.read`; price creation remains person-only. Reporters neither fetch
prices nor infer subscription/account coverage. Aggregate readers must not add
session usage to the overlapping managed-run token telemetry.

`paimos harness provenance` records hashes and version identifiers for explicitly
provided `--instruction` files (`AGENTS.md`, `CLAUDE.md`, or a skill's `SKILL.md`),
never file contents or full paths. Files must be regular, at most 1 MiB, and outside
private stores. On Linux and Darwin, supply a physical path: symlinks in any path
component are rejected. Darwin's root `/var` and `/tmp` aliases to `/private/var`
and `/private/tmp` are supported through a checked descriptor walk. A custom
symlinked checkout must be named by its physical path. Other platforms refuse
file hashing; `--show` and explicit prompt-template versions/digests do not read instruction files.

## Git-backed doctrine (AEON-318)

`GET /api/rules/doctrine` accepts people and agent keys with `rules.read`.
Source management remains person-only (`settings.manage`). GitHub reads use
only `https://api.github.com`; redirects are refused, including repository
renames, so configure the current repository name.

The operator provisions private read-only tokens under the absolute directory
`AEON_DOCTRINE_CREDENTIALS_DIR`. A reference such as `doctrine-private-read`
requires both the token file of that name and `doctrine-private-read.allowlist.json`:

```json
{"grants":[{"tenant_id":"<workspace UUID>","repository":"owner/repository"}]}
```

Each grant authorizes exactly one tenant/repository pair (names are compared
case-insensitively); repeat pairs to share a credential deliberately. The
server reads this policy before resolving a pin, fetching an index, or serving
cached doctrine. Missing, invalid, empty or nonmatching policies fail closed
with `credential unavailable`. Existing token files need this policy before
use. Removing a grant immediately prevents subsequent requests from reading
its cached doctrine or fetching again. Aeon has no API to write these grants:
the operator owns the directory and files, and the service has read access
only. Tenant owners may name references but cannot authorize them. Never put
token values in tenant configuration, API requests, logs or this repository.

## Agent rules (ADR-004, AEON-248 / AEON-249)

The dedicated `/api/rules` API stores layers, sets, rules and immutable version
snapshots as nodes. Generic node/event APIs cannot read or mutate these resources.
Git-backed doctrine rules (AEON-319) have **Propose change** when the server's
GitHub App is enabled for the workspace. The editor creates a proposal branch
and PR in the rule's owning public/private repo, changing its source and git
TL;DR sidecar together. Public proposals run the `inspr-modules` leak patterns
against only the changed rule, its TL;DR entry and PR explanation before any
GitHub call or token mint. Comparison removes every Unicode default-ignorable
character, applies NFKD, drops combining marks, folds case and uses a vendored
Unicode 17.0.0 UTS #39 skeleton. Named Latin small capitals are generated from
UnicodeData as well; ambiguous compatibility/visual forms fail closed. The
private corpus, public text and identity literals use the same normalizer;
proposed git bytes stay unchanged. Six-word runs, whole entries of five or more
words, and substantial four-word shingle overlap are refused.
Public proposals require a successfully indexed private source with a valid
credential grant. The quotation guard is an HMAC keyed by
`AEON_DOCTRINE_GUARD_KEY_FILE` (at least 32 characters; dev without the file uses
an ephemeral key). Its saved metadata binds the normalization code/table and
binary policy versions. Startup rebuilds missing, old or rotated guards;
public proposals fail closed until a compatible guard exists. An unset production
key or a configured file that does not exist disables PR proposal creation without
blocking startup or doctrine reads/indexing. Workspace administrators see the
reason in the Doctrine panel; malformed or unreadable keys remain configuration
errors. No key or host path is returned in that reason.

Every private-tree file must be UTF-8 text, without control codes other than
tab, LF and CR. No binary extension, signature or readable-text heuristic skips
files. A non-text file blocks the guard and the index error names only its path.
The optional host config `AEON_DOCTRINE_BINARY_ALLOWLIST` is an explicit reviewed
JSON object of exact repository-relative paths to lowercase SHA-256 digests of
complete blobs (empty by default). It cannot be set through proposals or source
configuration. Changing or removing an exception invalidates cached guards.

Public source indexing separately caches the resolved main commit and tree.
Only matching cached blobs can exempt a private match, for at most five minutes.
A missing, invalid or stale cache returns `422 public_main_unavailable` without
fetching; reindex the public source to refresh it. A proposal with no private
match needs no exemption cache and adds no guard GitHub reads. Normal PR
preparation still verifies main; if it changed since an exemption was checked,
reindex and retry. A proposal branch or unchecked pin never grants an exception.
Public letters must be Latin, including German umlauts and ß; the compatibility
form admits superscripts, fractions and the information symbol while other
scripts/digits stay refused. Unchanged file content is excluded.

Regenerate the pinned Unicode table with
`python3 internal/rules/doctrine/unicodegen/generate.py`, then `gofmt` the output.
The generator verifies immutable input digests; builds and runtime are offline.
Credential-shaped text is refused for either repository, including private.
Git stays authoritative; the database holds request digests, PR references and
audit metadata, never draft prose or credentials. Keep the same request UUID
and input when retrying a lost response. Short transactions authorize and reserve
a bounded proposal lease, then apply GitHub results with a version compare-and-set;
no database locks span network calls. Refresh reconciles an uncertain merge and
emits an event only when state changes. Externally observed merges are recorded
as observations; without prior Aeon approval, request release in the repository.

Host provisioning (no App exists yet): set `AEON_DOCTRINE_APP_ID`,
`AEON_DOCTRINE_INSTALLATION_ID`, `AEON_DOCTRINE_APP_KEY_REF`,
`AEON_DOCTRINE_APP_TENANT_ID` and `AEON_DOCTRINE_GATE_LOGIN`. An operator must
also set `AEON_DOCTRINE_DCO_ACKNOWLEDGED=true` after approving the App's
contribution/sign-off policy; without it proposals stay disabled. The App's
bot login and public noreply identity are resolved from GitHub, and commits
carry that bot's matching DCO trailer (required by the public repo). No
person's identity or address is copied into public commit metadata. The key reference
names a PEM RSA key under `AEON_DOCTRINE_CREDENTIALS_DIR`, read only at call time.
The operator must also provision `<key-ref>.allowlist.json` using the same
`grants` format as read credentials above, with an explicit tenant/repository
pair for each writable doctrine repo. Missing, invalid or revoked grants return
`credential unavailable` before key use or any GitHub request. Proposal,
refresh and approval operations recheck the grant. App configuration and tenant
settings cannot grant access to a key by themselves. Every GitHub request,
including PR writes, is pinned to `https://api.github.com` and refuses redirects.
The App installation must select exactly `inspr-at/inspr-modules` and
`inspr-at/inspr-doctrine-private`, with contents + pull requests write (and
implicit metadata read). Each minted token is narrowed to the proposal
repository alone and checked against that exact scope and live repository
visibility. Installation tokens are revoked after use (including validation
failures), with bounded cancellation-independent cleanup via GitHub’s
[revocation endpoint](https://docs.github.com/en/rest/apps/installations#revoke-an-installation-access-token).
A private repo becoming public blocks publication. The configured tenant is the
sole proposal writer; tenant settings
cannot grant another workspace access to the central doctrine. The App must
**not bypass branch protections**. Main must require CI and the independent
cross-family gate so both remain enforced during a merge race. No direct-main
write route exists.

The independent repository gate account named by `AEON_DOCTRINE_GATE_LOGIN`
must post an APPROVED PR review on the exact head, with its complete body:
`aeon-doctrine-gate: {"verdict":"ok","head_sha":"<head>","checks_green":true,"author_family":"openai","reviewer_family":"anthropic"}`.
It must independently verify required CI and the explicit cross-family review;
Aeon cannot supply that evidence itself. Families must differ. Missing, stale,
dismissed, same-family or negative evidence blocks approval. A person with
workspace `rules.publish` approves that head; Aeon checks protected-main
mergeability again and supplies the SHA to GitHub's merge endpoint. Branch
protections remain the race-safe authority. This uses PR reviews because
GitHub's checks/status APIs require extra App permissions beyond this ticket's
scope ([GitHub permissions](https://docs.github.com/en/rest/commits/statuses#get-the-combined-status-for-a-specific-reference)).

After a confirmed merge, Aeon sends `repository_dispatch` type
`doctrine-release` with `proposal_id`, `merge_commit` and
`requested_scheme: CalVer3`. The owning repo must install a receiver that
deduplicates on proposal ID, performs its approved reservation/release flow,
and publishes a release body line:
`aeon-doctrine-release: {"proposal_id":"<id>","merge_commit":"<sha>","version_scheme":"CalVer3"}`.
Aeon never reserves versions, changes consumer pins, or labels dispatch as a
release. Refresh verifies release provenance and that its tag resolves to the
merge or a descendant. Until the App, independent gate and release receiver
are provisioned by the coordinator, the workflow remains disabled/pending.
No foreign repository workflow was changed for this package.

The state reads proposed → in review → merged → released. A person with
`settings.manage` can report a verified machine pin through
`POST /api/rules/doctrine/proposals/{id}/pins` using a SHA-256 machine identity
and the observed release commit. The UI labels these as **reported** machines;
this is operator evidence, not automatic fleet discovery, and never changes
nixcfg, PHAROS or JANUS pins. New observations replace that machine's old pin
for the repository. Review, merge, release and pin evidence is tenant-isolated.

`rules.read` and `rules.write` are agent-grantable; `rules.publish` is high risk and
person-only. Company edits require a person with workspace publish authority;
project edits require that authority in the target project (or in an agent key's
creator). Person and named-agent layers require their exact owner. A person's
access to a named agent requires an active unexpired key they created. No grants
are created by this module.

Start with `POST /api/rules/layers` (a `RuleScope`), then
`POST /api/rules/sets` (`layer_id`, `name`). Replace the whole mutable draft with
`PUT /api/rules/sets/{setId}/draft` (`expected_revision`, `name`, `rules`). Rules
have a stable `identity`, bounded one-line `text` and `why`, optional on-demand
`details`, explicit `enabled`, `normal|locked` strength, source lineage, and
optional role/harness selectors and expiry. Locked rules must be enabled and
non-expiring; locked company rules are unconditional. Removing or changing one
requires a new publication by the authorized person. Nothing here changes the
company's current effective rules automatically.

Publish with `POST /api/rules/sets/{setId}/publish` and an explicit reserved
`YYMMDDhhmmss.0.0` version plus `expected_revision`. The same version and identical
snapshot replay; changed bytes require a later version. History is available at
`.../versions` and `.../versions/{version}`. Restore via `POST .../restore` with
`version`, `new_version` and `expected_revision`; this creates a new publication.
Every mutation appends a `rules.*` event in its tenant transaction. Set/version
reads include details; the merged response excludes details.

`GET /api/rules/merged` requires exact `project_id`, `person_id`, `role` and
`harness`; agents also supply their own `agent_id`; `task_id` is optional. Tenant
comes from authentication, and person must be the caller or its key creator.
Role/harness are explicit selection context, not permission grants or proof of
which process executed the result. Precedence is company, project, person, agent
role, named agent, task. The highest matching identity wins; identical-rank
ambiguity fails closed. No natural-language conflict guesses are made. Expiry is
checked on every request. The complete rendered body must fit the workspace
budget (12,000 UTF-8 bytes by default); publication over that budget returns 422.
Admins can set up to 64,000 bytes in the rules budget editor once all clients
active in the tenant over the last seven days report support. Missing reports,
reports under 12,000, stopped/archived older generations and an empty inventory
retain the 12,000-byte ceiling. The editor lists up to 50 blocking hosts,
harnesses and reported versions, and counts any further clients.
CLI and agentd send optional `max_session_file_bytes` and `rules_client_version`
on registration and every heartbeat. The number is that harness's default read
limit: Codex stops at `project_doc_max_bytes` (32,768 bytes of combined
`AGENTS.md`); the other harnesses report 64,000. Omission resets support to the
legacy limit, so rolling a client back closes the gate. These are request-only fields;
PHAROS/JANUS reporter response contracts and pins are unchanged.

Rules requests report `X-Aeon-Max-Session-File-Bytes` (2,000–64,000; omission
means 12,000). Managed delivery also respects its registered capability.
When a valid publication is larger than that limit, delivery retains all locked
rules, then adds whole rules in precedence/identity order as space allows, with
an explicit compatibility note in the file. Legacy cuts also fit the old 512-KiB
cache envelope, with 16 KiB reserved for its wrapper. The body digest, receipt
and served manifest describe the actual cut. If locked rules plus the note cannot fit,
delivery fails closed with an upgrade message; it never drops a locked rule.
Upgraded CLI, managed delivery, Claude bridge and caches accept 64,000-byte
files. The bounded cache envelope allows 32 MiB for repeated text, metadata and
worst-case JSON escaping. Roll out the server before upgraded reporting clients;
raise the workspace budget only after the editor's gate clears.
An applicable published locked company floor is required.

`paimos session start --rules-preview` is an opt-in JSON preview. Use `--project`
with its UUID, plus `--agent`, `--rules-tenant`, `--rules-person`, `--rules-agent`
(for agent callers), `--rules-role` and `--rules-harness`. Supply an explicit
`--rules-cache path.json`, a separately retained `--rules-floor floor.txt`, and
its trusted `--rules-floor-sha256`. These files require mode 0600 and physical
paths in an existing directory outside credential/harness stores. A reviewed
merged response's `floor` is the source for that retained file; provisioning and
trusting its digest is an operator step. Fetching never changes that pin.

`--rules-out new-preview.txt` creates a new file atomically and refuses overwrite.
No active `AGENTS.md` or `CLAUDE.md` is installed or replaced. The reusable Go
`rules.Stub` function provides AR6 a bounded stub preview with the verified floor.
On network unavailability (including HTTP 502/503/504), the exact instance/context
cache is marked stale. Wrong context, corruption, or any crossed expiry boundary
retains only the independent floor and reports the gap. HTTP authorization and
semantic failures never use the cache. Digests detect local corruption; the cache
is not a signed authority against a local user able to replace both cache and pin.

Preview output includes the exact body hash/version/size as
`proposed_received_payload`. It does not install an instruction file or update
an existing session's provenance; `provenance_recorded` and
`execution_verified` remain false. Preview never submits a receipt.

`paimos session start --rules-receive` is a separate, explicit receiving mode.
Use the same exact rules selectors, cache and independently pinned floor as
preview, plus `--session` with an **already registered public generation UUID**,
`--worker-lease-file`, a lowercase `--rules-request-id UUID`, an explicit
`--rules-expected-revision N` (receipt revision, initially `0`), a new
`--rules-state attempt.json` and a new `--rules-out received.txt`. Register first
with the existing `paimos harness register` flow; receiving never mints or switches
identities. Online receiving also uses the existing `account.manage` (`/api/me`),
`rules.read`, `harness.read` and `harness.worker` permissions; it grants none.
The lease stays in the trusted client and is never printed or saved in state.

Receiving writes the renderer's exact bytes as a private `.txt`, then explicitly
POSTs only context/hash/version/size/source and request/CAS metadata to the distinct
receipt endpoint. It never modifies active instructions or instruction provenance,
and proves neither model loading, execution, obedience nor authority. Online
floor changes (including additions), expiry, context/hash mismatches and HTTP
401/403/404 fail closed. Only network unavailability permits the existing exact
cache/independent-floor fallback: JSON reports `source: cache` or `floor-only`,
`stale: true`, a `gap`, `receipt_recorded: false`, and `complete: false`. This local
fallback exits successfully to make the retained bytes available; it is not a
completed online receipt and cannot be replayed as one later.

The immutable private checkpoint is saved before output/POST. After a partial
failure or lost response, rerun **the identical command with `--rules-retry`**.
Retry verifies the checkpoint, original context/instance/session/request/revision,
current online read authorization and floor, original expiry, and exact existing
output. It can create a missing output but never replace an existing one. It
resubmits the original online metadata; the server returns the original receipt
for an identical request ID, even if newer receipts/publications exist. No queue,
background replay, automatic CAS adjustment or registration is performed.
Keep the checkpoint and output until the result is resolved; local digests detect
corruption and are not signatures against an attacker able to rewrite local state.

The API verifies the lease only at POST, so rejection can leave bytes on disk.
Receipt failure exits nonzero with `receipt_status: rejected` (HTTP 4xx) or
`unconfirmed` (lost/invalid reply), `receipt_recorded: false`, and `complete: false`.
An unconfirmed result may already exist on the server. A successful explicit retry
confirms it without rewriting output. A 409 with a genuinely competing request,
changed floor, expired original rules, revoked access, or stopped/archived generation
requires operator inspection of receipt history; this CLI never changes the saved
CAS or switches generations to make it pass. A new attempt requires fresh request
ID/state/output and the explicitly inspected current receipt revision. Provisioning
a reviewed floor and registering the public generation remain operator prerequisites.

An explicit `POST /api/projects/{projectId}/harness-sessions/{sessionId}/rules-receipts`
records a separate worker report; `GET` on that path reads its append-only history.
Writes require `harness.worker`, the owning session agent and exact generation
lease. Reads use `harness.read` and project visibility. Supply a UUID `request_id`,
`expected_revision` (zero initially; otherwise the latest receipt revision),
`context` (tenant/project/person/agent/role/harness and optional task UUID),
`body_sha256`, calendar `version`, `byte_size` (1..12000), and `source`
(`online`, `cache`, or `floor-only`; the latter requires version `floor-only`).
Use the exact metadata of bytes the caller reports receiving. No bodies or paths
are accepted. The server checks tenant/project/agent, canonical key creator,
registered harness and optional current ticket binding. Role, digest, version
and source remain worker-reported; publication, model load and execution are
unverified, and no authority is granted. This does not establish that the reported
bytes came from a published merge. Existing instruction provenance stays intact.

Identical request-ID/normalized-metadata retries return the original receipt even
after later appends; divergent retries and competing stale revisions return 409.
A new request ID with the current revision appends even for identical bytes. Current
authorization, context binding and generation checks still precede replay.
Stopped/archived generations and expired/revoked credentials cannot record.
Harness generation leases have no time-based expiry; heartbeat age is not proof
of closure. History remains readable after closure, newest 32 first, with
`before_revision` pagination. Recording requires a live API connection; cache
and floor-only reports remain stale fallback evidence, never fresh authority.
There is no automatic submission or offline queue; the receiving mode above is explicit.
Migration 0897 uses a distinct immutable table because event visibility can hide
harness history from project readers; the audit event is transactional, not the
CAS source of truth. No company rules are seeded or published.

Stub installation, automatic registration,
template import/export and the rules editor are separate coordinator-owned work.
`aeon rules compare` is the one-time comparison of loaded harness files with the merged rules.

`aeon rules-compare` (the same verb on `paimos`) is a one-time offline check.
Pass explicit instruction files with `--file` and `--context`. Optional
`--merged` supplies AR1 merge metadata; `--provenance` supplies AEON-219
revision or page JSON and needs `--session` to bind an expected session.
Both JSON files are read as private `.json` files. The report compares exact
raw file hashes and supplied merge lineage. Canonical, session-bound provenance
is labelled supplied worker-reported metadata; bare arrays and preview
`provenance_items` remain unverified comparisons. Offline JSON cannot prove
API observation, snapshot publication, a trusted floor, runtime execution,
model load or obedience. Expired or malformed merge metadata remains useful
for historical differences only. Rollout stays unauthorized; the command
never waits or replaces `AGENTS.md` or `CLAUDE.md`.

`aeon rules compare` reads the `CLAUDE.md` or `AGENTS.md` chain one harness
loads for `--repo` and diffs it against `GET /api/rules/merged` for that
project, person, role and harness. `--harness` is `claude`, `claude-code` or
`codex`. It runs once, with no waiting period. The report lists statuses,
identities and hashes. `--upload` stores that summary for the project.
Instruction text is not uploaded. The command does not replace `AGENTS.md`
or `CLAUDE.md`, and rollout stays unauthorized.

`aeon doctor` also checks the two rule delivery channels. It discovers command
hooks marked `# aeon-rules-hook-v1` in the user and current project settings,
reads their literal `--rules-out` targets, and compares each harness file with
the pinned doctrine index. Inbox hooks alone do not establish rules delivery.
For a manually delivered file or a hook with a dynamic output path, use
`aeon doctor --rules-harness claude|codex --rules-out /absolute/received.txt`.
This explicitly checks that file and its harness file. Doctor never executes
hooks or expands shell expressions. Missing, empty, unreadable or unverified
files and unready/failed doctrine sources cannot earn “no rule served twice”.
Deleted rules are detected even when their markers were deleted too.
Doctor normalizes whitespace and follows literal Markdown `@path` imports,
relative to each importing file (`~/` resolves to the home directory). Reads
stay within the home and harness directories, refuse secret paths and symlinks,
and stop at 8 import levels, 64 files, 256 KiB per file or 1 MiB total. Unresolved
imports report “unverified” instead of drift; neither result certifies delivery.

Session merges resolve precedence before omitting doctrine copies. A locked
company rule delivered by doctrine retains its floor obligation as a reference
to the doctrine identity and immutable commit, without repeating its text in
the session file, cache or bootstrap. Existing independently retained floor
pins still require explicit review when changing from text to that reference.
Catalog reads for publication, delivery and channel reports recheck the same
host-provisioned credential grants as the doctrine API; an inaccessible source
fails the operation with `503 doctrine_unavailable` before cached text or
matching identities are inspected, including managed-session delivery. A batch
loads the catalog once for its request; later requests recheck the grants.
Channel reports include duplicates only from sets whose exact scope the caller
may read, including the project permission and scoped ownership checks.

## UI shell (P0.5 / AEON-10)

The Vue shell includes an authenticated workspace, sign-in, a 404, an account
menu, and light/dark themes. The theme follows the operating system until the
user toggles it; that choice lasts for the current page session and writes no
browser storage. Assets and fonts are served locally. The supplied mark is
preserved at `web/src/assets/brand/aeon-mark.svg` for its later replacement.

The ticket list and Outline share the causal row store and live stream. Field
changes patch in place; moves, additions, removals and held edits wait behind
**N updates · Show** (shortcut **U**), or apply after two idle seconds when no
selection, editor, menu, dialog or drag is active. There is no countdown. The
Outline retains expansion and tree placement while updates wait, refreshes
filtered ancestors, and moves successful bulk results immediately. Stream loss
invalidates outstanding reads before reconnect; older pages cannot overwrite
locally changed child counts.
On phones, List and Outline share a bottom-centred updates chip above the safe
area, footer and selection sheet, with scroll clearance for the last row; the
desktop action stays in the table header. Lazy pages retain the server's order.

The auth adapter is isolated in `web/src/lib/api.ts`. Pending P0.3 contract
confirmation, it expects `/api/me` to return
`{ principal: { id, name, email? }, tenant: { id, name }, dev_mode?: boolean }`.
A 401 clears identity and routes to sign-in. Development email sign-in is
hidden unless the server explicitly returns `dev_mode: true` (including on its
401 response). It never relies on Vite's development mode. Login navigates to
`/api/auth/login`; development login posts `{ email }` to `/api/auth/dev-login`;
sign-out posts to `/api/auth/logout` before routing to `/signin`. API calls use
same-origin credentials, a ten-second timeout, and no browser response cache.
The backend owns authentication cookies and the INSPR authentication redirect.
No analytics, third-party runtime assets, or optional device storage are added.

Both version surfaces use the unchanged, verified calendar bundle in Pretty
mode with brand gold. The shared helper provides reveal and copy interactions;
`dev` remains plain text. Every production web build verifies the bundle pin.

The connect screen keeps Connect available when a selection mixes verifiable
and unverifiable harnesses. Clicking it offers **Connect without verification**
for the whole selection or **Leave them out** to keep only the verifiable
accounts for review before connecting. Approval currently records one verification
mode for the selection; no harness is silently excluded or treated as verified.
When Connect is disabled, its reason appears beside the button.

```sh
cd web
npm run test:unit
npx playwright install chromium
npm test
```

The Playwright suite starts Vite on port 5175, intercepts all `/api/*` calls,
and covers sign-in, auth errors, logout, theme switching, version interactions,
44 px targets, and viewport overflow. It writes home, sign-in, development
sign-in, and 404 screenshots in both themes at 1280×720 and 390×844 to
`/tmp/aeon-p05-shots/`. Screenshots and browser test output are not committed.

Licence: AGPL-3.0-only.

### Pairing readiness and diagnostics

`aeon-agentd status` reads the last atomic setup snapshot and live daemon status
without taking `setup.lock`, reconciling enrollment, or performing cleanup.
Use `pair`/`setup` to resume setup and `disconnect` to resume cleanup. Approved
but unbound harnesses name the required `add-harness` command. Account checks
report a 60-second bound from daemon start for each initial account, or from
that account being added or unblocked by repin; refreshing unchanged accounts
does not extend the wait. Capacity capture reports a 10-second bound.
Unsupported or incomplete verification fails with `verification_unavailable`
and a bounded cause, independently of account probing. Connect-only approval
creates no verification, and a later approval cancels older queued verification
on that same computer without interrupting claimed work.

New pairing-owned macOS launchd services write diagnostics to
`~/Library/Logs/aeon-agentd/{stdout,stderr}.log` in a private `0700` directory.
Existing receipt-bound service definitions remain owned and unchanged; logging
is added when a new service is installed. Managed Nix/Home Manager services
retain their configuration ownership.

The paired daemon writes bounded `agentd polling diagnostic` lines to stderr
when the set of causes changes, then at most one reminder per cause every
15 minutes while the set persists. A healthy poll clears the set, so a recurring
cause is logged immediately. Multiple accounts sharing a cause produce one line:
`reason=probe_failed` records an account readiness failure (a failed probe/report
or a retained ownership/reporting block);
`reason=probe_timeout` confirms an account exhausted its own pending probe wait;
`reason=queue_unavailable` confirms `Queued()` returned an error before probing;
`reason=dispatch_not_allowed` confirms a dispatch fence, fence-read failure or
daemon shutdown prevented polling or an account probe. Correlate these lines
with read-only `aeon-agentd status --json` for the affected account. Queue errors
can therefore explain a subsequent probe timeout; the timeout alone does not
identify the underlying cause. Raw errors and private bindings are not logged.

### Paired daemon socket paths

Paired mode uses `<setup-root>/daemon/agentd.sock`. If that exceeds the
platform's socket path budget (100 bytes on macOS, 104 on Linux, allowing for
`sun_path` overhead), it uses `~/.aeon/run/<state-hash>.sock`, with 16 hex
characters identifying the daemon state directory. Both fallback directories
are owned by the user and mode 0700; symlinks and unsafe existing directories
are rejected. The selected socket is recorded privately in `daemon/control.json`.
Attach, setup/status and capacity clients read that reference; local control
can do the same with `control --setup-root PATH` (exclusive with `--socket`).
Existing generation-specific socket references remain readable while their
listener exists. No pairing data migration is required.

`HOME` is needed only for the fallback, so short and existing legacy socket
paths still resolve when it is unset. If the service and shell resolve different
fallbacks, the client reports its resolved home, setup root and socket alongside
the recorded socket; paths under the client's home are shortened to `~`.

`pair`, `setup` and paired `serve` reject an unrepresentable path before
creating pairing state or contacting the instance. Choose a shorter setup
root (`--state-root` for pair/setup, `--setup-root` for serve). Every local
listener takes an exclusive non-blocking lock on `<socket>.lock` before touching
the socket or token and retains it through shutdown cleanup. The lock file is
an owned mode-0600 regular file opened without following symlinks; it is retained
across restarts and is never deleted or renamed by startup, recovery or shutdown.
After acquiring the lock, startup compares the descriptor's device and inode
with the file named relative to the pinned private directory handle and retries
a bounded number of times if they differ.
Interrupted flock syscalls retry; only contention maps a flock error to busy.
The descriptor is close-on-exec, so harness children cannot keep the lock alive.
On macOS, a short directory flock serializes only the lock-file open: concurrent
`O_CREAT|O_NOFOLLOW` opens can otherwise return `ENOENT` during creation. It is
released before taking the lifetime file lock; open errors remain errors.
A second start reports "agentd is already running for this state
root" and leaves the active listener and token untouched, even when its accept
queue is full. Clients never take the lock.

The kernel releases the lock after a crash, including SIGKILL. The next owner
validates all stale socket artifacts through the private directory handle before
removing any: the socket, token (even without a socket), obsolete owner record,
and legacy `.s` plus eight hex digit quarantine names. Each must be owned by the
current uid, mode 0600 and single-linked, with the expected socket or regular-file
type. Symlinks, hardlinks, foreign owners and unsafe modes refuse startup.
The verified lifetime lock is the only cleanup authority. Recovery and shutdown
never probe the socket: on macOS even a live listener can refuse connections
when its accept queue is full. All cleanup checks and removals are relative to
the same pinned private directory handle. Lock identity is checked again before
each removal. Shutdown closes the listener and removes only its recorded
socket/token inodes while still holding that lock, then releases it. Losing lock
identity leaves residue for a verified owner to recover. Interruption likewise
leaves stale artifacts for the next owner; no quarantine is needed.
This advisory protocol serializes cooperating daemons. A same-uid process that
deletes or replaces the lock file or directory can disrupt a running daemon;
this is outside the protection boundary, since it can already signal or kill
the daemon. The no-symlink, owner, mode and link checks protect against accidental
or foreign-uid artifacts, not hostile same-uid path mutation.
Unrelated directory entries are preserved.
The private token and local control authorization rules remain unchanged.

### Harness interpreter pins

Guided setup pins Node's physical path and version privately for npm-launched
Codex, Cursor, Claude and pi. `--node-path` selects an installed Node outside the
workspace, including for shell wrappers. Setup checks the launcher using the
service PATH (`/usr/bin:/bin:/usr/sbin:/sbin`) with the pinned Node directory first;
interactive shell paths and Node injection variables cannot mask missing runtime
dependencies. The same pin is used for account probes and run launches. A missing,
partial, drifted, invalid, or unsafe pin blocks only that account: the paired
daemon and its other accounts keep running, and that harness does not launch
until the pin is fixed. Status names the account, a reason code (`pin_missing`,
`pin_partial`, `pin_drifted`, `pin_invalid`, `pin_unsafe`), and the fix as the
same `{kind, command}` object used by harness details. Claude launches with the
shared Node/SDK pins, so every Claude pin problem, a missing pin included, is
fixed with `aeon-agentd repin --harness claude`. For Codex, Cursor and pi,
`aeon-agentd add-harness --harness NAME` renews only the blocked pin of the
already connected account: the signed-in identity and launcher must match, no
request or approval is created, and a healthy pin is never replaced. If the
identity or launcher changed, remove the enrollment and add the harness again.
Repin and pin renewal never renew authority. Native launchers and saved pi
bindings remain supported. Paths and interpreter versions stay local.

### Pi guided setup

`paimos-agentd setup --harness pi` and `add-harness --harness pi` use the same
person-approved pairing flow as the other harnesses. Install pi normally and use
its `/login` and `/model` commands first. Setup pins the executable and reads its
version. For npm's `#!/usr/bin/env node` entrypoint it also resolves Node (or
uses `--node-path`), pins its physical path and version privately, and prepends
its directory to the probe and runtime PATH. Node must be installed outside the
workspace. A missing or changed interpreter pin blocks only that account.
Setup then checks
the selected provider/model against pi's public RPC
`get_available_models` response. `--account-context anthropic` selects a specific
configured provider instead. For existing native sign-ins the local profile is `~/.pi/agent`; Aeon never
opens its credential files, inherits provider keys for this check, or sends a
prompt. This establishes configured authentication, not remote credential
validity or a person's identity. The approval label names the provider and local
profile; the server selects an enabled pi model profile for that provider, and
only the computer keeps the profile path. The profile directory must be private
(no group or other access), as required by the daemon. Managed installations stay
with their owning Nix/Home Manager configuration.

The paired daemon rechecks that provider at most once a minute during polling
and afresh before each run, and refuses a model profile from a different
provider. Probe startup failures report a harness startup problem; only missing
provider configuration requests vendor login. Pi verification is explicitly
unavailable: its managed adapter has no qualified no-tools boundary. Connect without verification, then
set request limits for ongoing work. Pi retains its existing SVG harness icon and
account allowance pipeline; missing vendor token, cost or capacity readings
remain unreported, never inferred from a provider name or a successful probe.

For OpenRouter, run `aeon-agentd add-harness --harness pi --provider openrouter`
on an already paired computer (or append `--harness pi --provider openrouter`
to `aeon-agentd pair` for a new one). The hidden terminal prompt checks the key
with `GET https://openrouter.ai/api/v1/key`, without spending tokens, and stores
it in a new 0700 per-account pi profile with a 0600 `auth.json`. Headless setup
accepts `--openrouter-env-file /absolute/private/path`: only a literal
`OPENROUTER_API_KEY` assignment, never shell commands. No key flag, inherited
provider key, keychain dump, server upload, or key in child arguments/environment.
Normal native provider setup also accepts `--provider PROVIDER`.

Settings → Accounts lets a person with `account.manage` choose a pi model;
OpenRouter uses `vendor/model[:variant]`. The server checks its public models
list with a four-second timeout and a fifteen-minute cache (failed lookups cache
for thirty seconds). Unconfirmed slugs save as **unknown**. The initial
OpenRouter option is `stealth/space-bunny-alpha`; edits create immutable model
profiles for the account catalog/planning selector. Active runs keep their pin;
previously queued stale choices must be selected again. OpenRouter's underlying
model family is not assumed, so unknown families cannot qualify for review gates.

Stealth and free models show the provider data note. Local probes report only
numeric dollar usage, key limit and remaining credits with their reading time;
missing values stay unknown. Credit amounts are not a renewable allowance window:
normal request limits and the blind-provider pacing policy still apply. No
completion call is used to validate a key or a model.

### Session metadata

`scripts/session-metadata.py --codex-index` requires an explicit `session_index.jsonl`
and canonical source session UUID, and supplies names only. Linux and Darwin open
each path component relative to its parent descriptor without following symlinks;
Darwin's exact root `/var` and `/tmp` system aliases are supported. Custom symlinks,
private-store components and unsupported platforms are refused. The opened file
must be regular and at most 64 KiB; content reads remain bounded if it grows.
Fixture mode retains its stricter private-directory exclusions. Unmanaged capture
remains unavailable until the exact owning source is bound; live model and effort
capture remain unimplemented.

### Session recovery

Managed sandbox controls (AEON-260) use the additive
`POST /api/projects/{projectId}/harness-sessions/{sessionId}/managed-controls`
route. A person needs `harness.control`, the daemon must advertise
`managed_control_v1`, and the request carries a stable UUID `request_id`,
`kind` (`steer`, `interrupt`, `stop`) and exact `expected_ownership`.
A steer also carries at most 8192 UTF-8 bytes of `text`. Controls expire after
45 seconds, with at most 16 outstanding/recent requests per session.
The existing control GET returns durable metadata; yield claims at most once.
Steer text lives only in a bounded expiring memory relay, never in control rows,
events, heartbeat or the daemon journal. Server restart rejects lost text;
ambiguous delivery is reported as unconfirmed, never silently reinjected.
Identical request IDs replay their receipt; divergent retries are refused.
Pending authorization is rechecked when the daemon claims the control.
Yield includes `expires_in_ms`, a remaining lifetime calculated on the Postgres
clock and rounded down. The daemon deducts the full request round trip and keeps
one local monotonic deadline through queueing, adapter writes, setting waits and
the final force-stop signal lock. It rejects missing, zero or invalid lifetimes;
new daemons therefore require a server that supplies this additive field for
managed controls and force stop. `expires_at` remains audit metadata. Settings
completion uses the database clock, and transient steer retention uses a local
monotonic budget derived from it. Process start timestamps remain opaque identity
fields; the database observation stamp establishes ownership freshness.

The worker-only `POST .../managed-context` route returns the ADR-004 merge for
that session's tenant, project, key creator, agent and work order. Callers cannot
select another context. This is an execution-scoped read under the worker
lease, not a general rules permission or a repository file installation.

The qualifying adapter is currently the restricted Claude SDK bridge on macOS:
explicit daemon-owned MCP tools, no native Bash or file tools, strict MCP inventory
and a bounded sandbox terminal. Read/Edit/Write/Glob/Grep use descriptor-relative
`openat` with `O_NOFOLLOW` on every component; reads and writes validate regular
files with a single link using `fstat` before accessing contents. Search walks
opened directory descriptors. Symlinks (including internal ones), hard links,
special files and oversized searches fail closed. Write creates missing parent
directories relative to opened directory descriptors. The bridge receives only its pinned account HOME/config directory,
a tool-specific PATH and fixed locale, never the daemon environment. The terminal
allows exact CPU/page-size/memory/uname and ARM-feature sysctls for Go/Node;
process arguments and other-process inspection are explicitly denied (the macOS
default denial alone does not block numeric procargs queries). Self inspection
is allowed for the macOS runtime. The terminal qualifies toolchain libraries
through the Nix package closure or recursive macOS
`otool` inspection, resolving loader paths and Homebrew symlinks to exact library
files or their containing Cellar kegs with read/map access only. The per-run cache
rechecks executable and library mtimes and symlink targets; it never grants the
Homebrew installation prefix. Homebrew OpenSSL's host configuration remains
unreadable and is reported absent so Node can start with its built-in defaults.
Terminal completion
kills remaining group members before reaping its leader, including children that
close or inherit stdout. Codex and Cursor cannot yet enforce the
required inherited-tool ceiling; they, Pi and Grok do not advertise this new
capability. Unsupported platforms and missing bound tools/rules fail closed.
The boundary does not isolate another process running as the same OS user.
Claude steer queues the next input then interrupts the current turn. Interrupt
requires the SDK receipt; stop requires native Query.close and observed exit.
The managed panel replaces its legacy inbox composer with these exact-session
controls, leaving older sessions on their existing interface.

The same panel offers **Session settings** for qualified managed runs (AEON-224).
Name, model and effort use typed, person-only, ownership-fenced controls with
idempotent request IDs and pending/applied/rejected receipts. Model and effort
choices come from the run's exact enrolled account catalog; unsupported pairs
and unqualified harnesses are refused. The Claude bridge checks its live
`supportedModels()` catalog, calls `setModel()` or
`applyFlagSettings({ effortLevel })`, and reports metadata only after the SDK
acknowledges. Effort applies on the next turn. The daemon publishes the applied
settings by heartbeat before completing the control. Rename updates Aeon's
public `display_label` and metadata history after the daemon verifies ownership;
it does not enable vendor transcript persistence or rename files. A missing SDK
setter rejects the request. Codex remains unqualified. Tests use fake SDKs only.

Qualified runs default to 100,000 reported input/output tokens (including cache
usage) and 16 completed input turns, configured by agentd Config.MaxTokens and
Config.MaxTurns. The SDK also receives maxTurns for its agent loop. Counters
never reset on steering. Both the bridge and daemon close on exhaustion; the
bridge closes even if remote telemetry stalls. Stop reasons and any unconfirmed
exit are retained, tools close, and no force-stop fallback is implied. Token
usage arrives at vendor reporting boundaries and may exceed the threshold;
this is not a provider billing cap. Existing wall-clock deadlines remain.

People with `harness.recover` permission can open **Recover** in a session’s details and archive its registration after confirming the exact session and host. Archive preserves ticket links, outcomes and audit history, revokes the old worker generation, and records process state as unknown. It never signals a process. Late heartbeats, control completions and registration replays cannot reopen an archived generation; a new session needs a new reference and lease. The recovery dialog refreshes on stale observations. Recovery-aware daemons detach their harness registration without stopping the run. Active older managed daemons must stop normally before archive is available, because they cannot detach safely. Archive waits for any already-authorized force request to finish or expire.

Managed daemons report a per-launch process identity and generation. **Force stop** additionally requires human `harness.force_stop` permission, an ownership report no more than 45 seconds old, and exact session/host/process-group confirmation. Ownership recording and freshness checks use the Postgres clock, so API-host clock skew does not reject a fresh report; future-dated observations still fail closed. The daemon rejects a changed identity, restart, expired request or lost ownership; deadline and cancellation are rechecked under the final signal lock. Local transport and inbox credentials cannot authorize force stop. Expiry uses the database-derived monotonic budget above; archive cannot retract a signal that was already authorized or delivered, and always retains unknown process state. It signals only the owned process group (including children in that group), and reports root exit separately from queue acceptance; escaped descendants are outside its scope. Linux and macOS keep the group leader unreaped while signaling, preventing PID reuse. Unsupported adapters, legacy unmanaged sessions and offline ownership cannot be force stopped. Legacy normal user **Stop** sends TERM and reports timeout without escalating; the qualified Claude managed control uses native close as described above. Daemon cleanup retains its existing bounded force cleanup.

### Removing ghost sessions (AEON-265)

People with `harness.read` access to a project can remove a session from its
row's overflow menu (**Remove from Agents…**) or with **Remove** in the session
panel, then confirm once. The record immediately leaves
active views and counts; the **Removed** filter retains its ticket links and
history. `POST /projects/{projectId}/harness-sessions/{sessionId}/remove` takes
`{"reason":"..."}` (1–240 characters), uses the current locked record without a
client revision, and is idempotent even after another tab removed it. It accepts
unmanaged, legacy managed, offline, stopped, and pending-force registrations.

Removal revokes the generation: late heartbeat, mark-stopped, receipt and control
completion writes return 410. It releases outstanding deliveries and rejects
pending controls, recording `harness.removed` with actor, session and reason.
It never signals a process or asserts remote exit. A force signal already delivered
cannot be recalled; legacy daemons may independently react to the revoked lease.
The existing verified force-stop action in **Recover** keeps its identity and
confirmation checks; it cannot target an already revoked generation.

**Clear stale** (shown in the Sessions header only when stale sessions exist)
confirms once with the count, then calls the project-scoped
`POST /projects/{projectId}/harness-sessions/remove-stale` with a reason. The server
rechecks eligibility under row locks: the last accepted heartbeat must be older
than 15 minutes, falling back to creation time for sessions with no heartbeat.
A recently stopped record with a recent heartbeat is retained. One request removes
at most 200 records; the response lists them with the server cutoff and `more`,
and the UI repeats while `more` is true. Every removal event of one request carries
the same server-generated `batch_id` and the cutoff; retries produce no duplicate
removal audit.

Deploy approvals can carry optional `target` metadata: `hosts` or `environment`,
`service`, `change`, and optionally `image`. Targets appear on the deployment
card, Needs you, and approval history; missing targets read “Target not
named” and keep existing approval behavior. `target_digest_sha256` identifies the
recorded metadata and does not add an authority check (enforcement is AEON-287).
The additive contracts are `approvals/1.1` and `journey/1.3`. Stage handoff
responses remain byte-compatible `stage-handoffs/1.0` for strict PHAROS readers;
targets stay in storage and audit events, and the web reads them from the journey
deploy stage or approval, labelled “named by the agent”.
Managed agents can pass `target` and an optional `release_node_id` to
`aeon_request_approval`; `paimos external-stage request --operation deploy`
accepts an optional `--target-file JSON`. Verify handoffs retain the preceding
deploy handoff's recorded target internally when present; neither handoff body
emits target fields.

### Attach a running session (AEON-352)

From a separate interactive terminal on a paired computer, run
`aeon-agentd attach --setup-root PATH --pid PID --harness codex --project-id UUID --ticket-id UUID --transcript PATH`.
**Watch the conversation** is the default; the transcript must be a resolvable
physical file, as in the AEON-258 mirror. Choose **Status only (no conversation
text)** at the local prompt, or pass `--status-only` (no transcript needed), to
report status without reading or sharing conversation text. Missing or unsafe
transcripts never silently select status-only.

On macOS, normal Terminal and Ghostty tabs and SSH terminals work with a
root-owned `login` or `sshd-session` leader; tmux also remains supported.
Ancestry and session-leader checks use kernel PID, parent, UID, start time,
session and TTY metadata without reading ancestor paths. The helper must have
a TTY and belong to the target's user. Neither process may be an ancestor of
the other, and ancestor UIDs must be that user or root. The complete process
graph is rechecked for PID reuse and reparenting on confirmation and polls.
The target still requires its full physical folder and image validation.
Run `GOMAXPROCS=2 nix develop -c python3 scripts/check-attach-ancestry-mutations.py`
on macOS to verify that the negative ancestry regressions catch removed guards.

Claude and Codex are identified from the kernel-observed running image,
so an exec wrapper or a vendor auto-update does not require re-pairing. On macOS,
the daemon verifies the running PID against Apple's certificate chain and the
built-in vendor Team ID and CLI signing identifier on preview, confirmation and
every poll. A vendor file renamed over a foreign running binary cannot confer
that identity. Cursor attach is refused on macOS until a signed cursor-agent
CLI exists; Cursor.app's signature is not a harness identity.
Legacy Claude and Codex wrapper pairings work after upgrading and
restarting agentd. Unsigned Claude and Codex images are refused on macOS,
including when run through Rosetta. Linux uses a local installation
root plus owner recorded at pairing or by `repin --harness claude`,
`add-harness --harness codex` or `add-harness --harness cursor`; restart agentd
after recording a fallback identity. Unknown layouts retain only the approved
exact-file pin. Pairing retains recorded roots after removal of an old version,
bound to the same pairing and account. A recorded owner and root must match even
when the running image still has the old exact path.
The daemon does not interpret or execute wrappers to discover an install root.
Every image and ancestor must satisfy the existing ownership and permission
rules, and confirmation and polls recheck the image. Local HTTP 409 diagnostics
include `harness_identity_mismatch`, `harness_executable_unsafe`,
`harness_image_changed`, `harness_identity_unavailable` or
`harness_identity_unsupported` with a fixed repair, retry or unsupported-harness
hint; the attach client displays them. Signature checks run outside the manager
lock. Startup drops only fallback identities that cannot be re-derived from
the approved installation; signed images and other harnesses remain available.
Refresh validates new fallback identities against their approved installations.

Identity regressions cover release-13 wrapper pairing upgrades, native exec
chains, vendor updates, Linux root fallback and its repair, unsigned Rosetta
image refusals, signature failures, writable installations, real
running-process rename-over attacks,
verification timeouts and concurrent detach, and local 409 diagnostics.
On macOS, run `GOMAXPROCS=2 nix develop -c python3 scripts/check-attach-identity-mutations.py`
to remove each guard temporarily and require a failing regression; the script
rejects build failures as evidence and restores each source file. Real codesign
checks also probe running installed vendor binaries; a harness with no running
process is reported as skipped, while fixture signature checks still run.

Review the kernel-observed process, physical folder and chosen mode, type `WATCH`
or `ATTACH` as shown, then enter its nine-digit code under **Agents → Attach
session** on the paired instance. The approval screen shows the selected mode.
The computer owner approves the exact snapshot; the code expires in ten minutes.
Both modes require a consent digest and single-use approval (repeat approval
returns 409). Keep the terminal open: peer-checked polls renew a 60-second lease.
Revocation, identity changes and lease expiry require a fresh approval. A stopped
watch is detached; lost contact is unreachable; only a kernel check confirms exit.

Conversation watching shares only new turns after activation with people
explicitly granted `harness.watch` in the project. Status-only (`snapshot.mode=lease`)
opens no transcript, rejects conversation text and has no conversation viewer.
The project permission remains off for all built-in roles and is never implied
by `harness.read` or `nodes.read`. Both attach modes require protocol 2. An older
daemon connecting to a newer server still registers and keeps serving work; its
attach requests receive HTTP 409 `update_agentd` and cannot create a watch or
lease. The released older terminal helper shows `local lifecycle request rejected`
(the daemon's local refusal is `paired instance refused attach`), rather than
the server's update message. Update agentd, restart it and give fresh approval.
A newer daemon connecting to an older server receives HTTP 400 on attach
registration because that server rejects the unknown `attach_protocol` field.
The daemon logs the server's refusal, disables attach and keeps serving work
and local control. An attach attempt through that daemon shows
`local lifecycle request rejected`. After updating the server, restart agentd
to retry attach registration; there is no in-process registration retry.
The paired computer's tenant-scoped workspace is the hard cwd allowlist; neither
`AEON_URL` nor local request fields can override the paired origin. Same-user
processes are not isolated by this feature.

Security regressions live in `internal/agentd/attach_lease_test.go` (injected
commands, PID/executable/cwd changes, explicit mode choice, status-only without
transcript I/O, expiry and offline teardown in both modes),
`internal/agentpairing/attach_lease_test.go` (atomic approval, isolated session
leases, expiry/revocation, text refusal and cross-tenant RLS/404), and the
platform-specific process tests (native macOS/Linux kernel identity and exit).
`cmd/aeon-agentd/paired_serve_test.go` verifies the paired-origin pin against an
alternate remote, `AEON_URL` and HTTP redirects before either mode
can attach. It also runs the real serve loop through registration refusals,
checks that work polling and local control continue with attach disabled, and
restarts against an updated server to recover attach. Server regressions verify
that protocol-less registrations keep daemon identity usable while every attach
operation requires an update, even if later requests claim protocol 2.
`TestAttachModesProtectionMatrix` checks consent, mode tampering and terminal
states in both modes, sending text on refused watch polls. Early text at pending,
discovery, local-confirmation and activation stages detaches without a session.
Darwin tests require ESRCH before a missing or mismatched PID counts as exited.
The status-only text regression backdates the poll clock and observes the relay directly, so rate
limiting cannot hide a missing content guard. The approval browser spec covers
both modes and consent policies at 1600/390 pixels in light and dark.
The reporter contract is `harness-session/1.6`:
existing state values stay intact; optional `watch.process_state` carries a
confirmed exit. The existing default-off permission and code-attempt-cap tests
remain in `internal/agentpairing/watch_test.go`.
