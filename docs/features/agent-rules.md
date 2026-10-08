# Agent rules (ADR-004, AEON-248 / AEON-249)

The dedicated `/api/rules` API stores layers, sets, rules and immutable version
snapshots as nodes. Generic node/event APIs cannot read or mutate these resources.
Single, batch and restored rule publications reject credential-shaped text in
snapshot fields and publication notes with a generic `422 credential_text`.
The detector is shared with knowledge learnings, whose existing explicit
false-positive confirmation policy is unchanged.
Git-backed doctrine rules (AEON-319) have **Propose change** when the server's
GitHub App is enabled for the workspace. The editor creates a proposal branch
and PR in the rule's owning public/private repo, changing its source and git
TL;DR sidecar together. Public proposals run the `inspr-modules` leak patterns
against only the changed rule, its TL;DR entry and PR explanation before any
GitHub call or token mint. Both public and private proposals also use the shared
credential detector (provider/cloud keys, private keys, password assignments,
Markdown labels, authorization values and URLs with passwords). It checks the
edited fields and complete outgoing blobs and paths before GitHub receives
anything; refusals never echo matched values. Raw and case-preserving Unicode
compatibility checks retain token shapes alongside the existing skeleton check.
Comparison removes every Unicode default-ignorable
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
`python3 internal/unicodeguard/unicodegen/generate.py`, then `gofmt` the output.
The generator verifies immutable input digests; builds and runtime are offline.
Credential-shaped text is refused for either repository, including private.
The shared publication detector checks raw text, compatibility forms without
format controls, and case-preserving confusable forms from the same pinned table.
Rule snapshots (single, batch and restore) use these checks too. Credential
range offsets for knowledge confirmation still refer to the original text.
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
Admins can set 2,000–500,000 bytes in the rules budget editor, with optional
layer caps from 500 bytes to the total. Older clients never block a save;
out-of-range totals return the stable `400 invalid_budget` error. Lowering a
budget below published rules still fails with 422. The editor estimates tokens
at full budget (bytes ÷ 4), adds nonblocking guidance above 64 KB and 128 KB,
and offers a collapsible English/German Tip for keeping the kernel small.
Budget fields and save/cancel controls precede expandable guidance, so warnings,
validation messages and client details grow downward without moving them.
Use ⌘↵ on macOS or Ctrl+↵ elsewhere to save from a field; Escape leaves the
field first, then cancels editing on the next press. Browser shortcuts stay native.

CLI and agentd send optional `max_session_file_bytes` and `rules_client_version`
on registration and every heartbeat. The transport ceiling is 512,000 bytes,
separate from the 500,000-byte workspace budget. General Codex reports remain
32,768 bytes, its default combined project-instruction limit; other harnesses
report 512,000. Fresh Aeon app-server launches supplied with rules set
`project_doc_max_bytes` to the greater of the delivered byte size and 32,768,
preserving the default allowance for the repository's `AGENTS.md` chain.
The delivered rules go separately into ephemeral developer instructions,
without changing account config or repository files. Manually launched Codex
sessions keep their honest default report. See the
[official configuration reference](https://developers.openai.com/codex/config-reference/).
Omission resets support to the legacy 12,000-byte limit after a downgrade.
These are request-only fields; PHAROS/JANUS session responses stay unchanged.

Rules requests report `X-Aeon-Max-Session-File-Bytes` (2,000–512,000; omission
means 12,000). Managed delivery also respects its registered capability.
Delivery fits within the lesser of the workspace budget and client limit,
retaining all locked rules before adding whole normal rules in this fixed
priority: company, project, person, agent role, named agent, task; identity
ascending within each priority. A compatibility note names any cut. Legacy
cuts also fit the old 512-KiB cache envelope, reserving 16 KiB for the wrapper.
The body digest, receipt and served manifest describe the actual cut. If
locked rules plus the note cannot fit, delivery fails closed with an upgrade
message; it never drops a locked rule. Whole rules may leave unused bytes.
The admin editor shows each recent client's delivery allowance
`min(budget, reported limit)` and marks smaller allowances as truncated. The
historical `blocking_clients` field now carries this inventory, grouped by
host/harness/version/limit over seven days, bounded to 50 plus a remainder
count, including stopped/archived generations. Readers without workspace
`settings.manage` never receive tenant-wide host/version details. The product
ceiling is always 500,000, including an empty inventory.
Upgraded CLI, managed delivery, Claude bridge and caches accept 512,000-byte
files. The cache envelope remains bounded at 32 MiB; publication store caps
remain 2,000 rules and 2 MiB. Roll out migration 1042 and the server before
upgraded reporting clients.
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
