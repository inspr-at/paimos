# Agent integration

How AI agents participate in PAIMOS — authentication, workflows,
and best practices for treating an agent as a first-class user.

---

## Introduction

PAIMOS is a self-hosted project management system for engineering teams
and solo developers. Epics → tickets → tasks, sprints, time tracking,
attachments, search — all behind a single Go binary and a JSON API.

Agent integration matters because it is the reason PAIMOS exists in its
current shape. Agents can do everything humans can — create and read
issues, leave comments, update status, log time, search, manage sprints.
They do not interact through a second-class "automation" surface.
They use the same REST API as the web SPA, authenticated the same way,
gated by the same role and project-membership rules.

If you are building an agent that ships software, PAIMOS is the place
it logs its work.

---

## Authentication

Agents authenticate with API keys. A key is bound to a user account;
the agent inherits that user's role and project memberships.

- Keys are created via `POST /api/auth/api-keys` and returned **once**
  in the response. They are never retrievable again.
- Keys are prefixed (default `paimos_`) and stored only as a sha256
  hash. Losing the key means creating a new one.
- Every authenticated request sends the key as a bearer token:
  `Authorization: Bearer <key>`.

### Creating a key

The `/api/auth/api-keys` endpoint itself requires an authenticated
session. Log in once from a browser or with `POST /api/auth/login`,
then create a key for the agent to use going forward.

```bash
# 1. Log in (captures a session cookie)
curl -s -c cookies.txt -H "Content-Type: application/json" \
  -X POST https://paimos.example.com/api/auth/login \
  -d '{"username":"ci-bot","password":"<password>"}'

# 2. Mint an API key
curl -s -b cookies.txt -H "Content-Type: application/json" \
  -X POST https://paimos.example.com/api/auth/api-keys \
  -d '{"name":"build-agent"}'
# → { "id": 7, "name": "build-agent", "key_prefix": "paimos_1a2b3c4",
#     "key": "paimos_<64-hex-chars>"  ← store this now, you can't get it later
#   }

# 3. Use the key on every subsequent request
export KEY='paimos_<64-hex-chars>'
curl -s -H "Authorization: Bearer $KEY" https://paimos.example.com/api/auth/me
```

Revoke a key with `DELETE /api/auth/api-keys/{id}`.

### Response headers worth watching

Every authenticated response (key or cookie) carries:

- `X-Permissions-Epoch` — per-user counter (PAI-320). Bumped on
  role / membership / status change. Track the value seen at first
  request; if it changes, capability decisions cached client-side
  are stale and should be re-derived.

Cookie-authenticated responses (i.e. browser SPA, not API keys)
additionally carry `X-Session-Expires-At` (RFC3339) for the unified
expiry-modal flow (PAI-322).

### Agent attribution headers (PAI-324 / PAI-325 / PAI-354)

Two request headers tag every mutation with the agent and session that
caused it. Both are optional — missing headers persist as NULL — but
strongly recommended for any non-human caller:

| Header | Persists to | Notes |
|---|---|---|
| `X-Paimos-Agent-Name` | `issue_history.agent_name`, `mutation_log.agent_name` | Free-text label (≤ 64 chars). Convention: kebab-case role name (`ops`, `dev`, `sec-review`). |
| `X-Paimos-Session-Id` | `issue_history.session_id`, `mutation_log.session_id` | UUIDv7 minted by `paimos session start`. Shared across every call within one "session" so the undo / activity feeds group correctly. |

The `paimos` CLI forwards both headers automatically when
`PAIMOS_AGENT_NAME` / `PAIMOS_SESSION_ID` env vars are set — see
`AGENT_INTERFACE.md` §4a for the `paimos session start` flow. Hand-
rolled HTTP clients should set them explicitly.

---

## Core workflows for agents

### 1. Reading project state

```bash
# List all projects the agent's user account can see
curl -s -H "Authorization: Bearer $KEY" https://paimos.example.com/api/projects

# Get a project's issues with filters
curl -s -H "Authorization: Bearer $KEY" \
  "https://paimos.example.com/api/projects/2/issues?status=backlog&priority=high"

# Get the full hierarchy (epics → tickets → tasks) for a project
curl -s -H "Authorization: Bearer $KEY" \
  https://paimos.example.com/api/projects/2/issues/tree

# Place a ticket under an epic. Hierarchy (epic⊃ticket, ticket⊃task) is the
# `parent` relation edge — the single source of truth (one parent per child).
# Equivalent to setting parent_id on issue create/update.
curl -s -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"target_id": 123, "type": "parent"}' \
  https://paimos.example.com/api/issues/100/relations   # 100=epic, 123=ticket

# Search across all accessible projects
curl -s -H "Authorization: Bearer $KEY" \
  "https://paimos.example.com/api/search?q=authentication+bug"
```

Search also accepts issue keys — `?q=PAI-42` will find that specific
issue, and partial keys (`PAI-4`) prefix-match.

### 1a. Reading project context for coding agents

PAIMOS now has a dedicated project-context layer for code-aware agents.
Use it when an issue needs repository locations, canonical commands, or
structured environment facts instead of just prose.

```bash
# List linked repos for a project
curl -s -H "Authorization: Bearer $KEY" \
  https://paimos.example.com/api/projects/2/repos

# Read unified project knowledge
curl -s -H "Authorization: Bearer $KEY" \
  https://paimos.example.com/api/projects/2/knowledge

# Fetch the canonical agent artifact
curl -s -H "Authorization: Bearer $KEY" \
  https://paimos.example.com/api/projects/2/agents/codex.json

# Retrieve mixed context hits for a question
curl -s -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  https://paimos.example.com/api/projects/2/retrieve \
  -d '{"q":"where is the auth middleware and how do I run tests?","k":8}'

# Inspect anchors for one issue
curl -s -H "Authorization: Bearer $KEY" \
  https://paimos.example.com/api/issues/PAI-42/anchors
```

Project context is no longer a manifest blob (PAI-358). Use the
first-class surfaces instead:

- `repos` — linked repos or subtrees relevant to the project
- `knowledge` — memories, runbooks, guidelines, external systems, related projects
- `agents/{name}.json` — canonical agent artifact including commands, rules, and inventories
- `anchors` — issue-to-file anchors uploaded by repo-side tooling
- `graph` / `graph/blast-radius` — typed relationships and impact views
- `retrieve` — mixed context search across issues, anchors, knowledge, graph neighbours, and repo context

Anchors are uploaded to `/api/projects/:id/anchors` by a repo-side tool
that maps issue keys to file/line locations. Each anchor carries repo
revision and schema metadata so deep links and provenance stay explicit.

`/api/projects/:id/retrieve` now fuses project-scoped lexical hits from
issue text plus a dedicated context index for anchors and derived
symbols, then blends in local semantic vector matches and appends
graph-neighbor expansion. Vector indexing is asynchronous: retrieve
queues a project refresh and uses already-indexed vectors, so cold
projects can return lexical-only on the first call. The response includes
retrieval metadata so clients can see the fusion strategy, stage counts,
`embedding_indexing`, `embedding_model`, `embedding_provider`,
`vector_index`, and `freshness`. In v3.10.3 the local embedding model is
`local-semantic-v2`; vectors are ranked through SQLite via
`sqlite-scalar-cosine`.

For agents running inside a checked-out repo, `paimos serve` adds a local
read-only context broker:

```bash
paimos serve --project PAI --repo-root . --addr 127.0.0.1:8765
```

It combines the authenticated project context surface with bounded local
repo search/read/symbol tools:

- `GET /context/repo` — branch, HEAD, dirty counts, `AGENTS.md`, anchor index
- `POST /context/search` — fixed-string ripgrep search with bounded hits
- `POST /context/read` — bounded line-range file read with path escape checks and redaction
- `POST /context/symbols` — regex fallback for common declarations (`lsp_available: false`)
- `POST /context/retrieve` — remote `/retrieve` plus local search/symbol hits
- `POST /context/pack` — issue/query context bundle with an approximate token budget

MCP clients can launch the same broker over stdio:

```bash
paimos serve --project PAI --repo-root . --mcp-stdio
```

The broker does not accept project or repository writes. HTTP mode is
loopback-only unless the operator explicitly passes `--unsafe-allow-remote`.
For Claude Code, `--mcp-stdio --channel-as <harness>:<agent>` opts into the
experimental `claude/channel` server capability and polls that attributed
inbox. It emits `notifications/claude/channel` with framed message content and
safe string metadata, then advances only the durable message read cursor after
the JSON-RPC notification is written successfully. Without `--channel-as`, the
capability is absent and the broker remains wholly read-only.

Blast-radius queries are available at
`GET /api/projects/:id/graph/blast-radius?issue=PAI-79&depth=3` for the
"what else is affected if I change this?" agent flow.

### 2. Creating and updating issues

```bash
# Create a ticket in project 2
curl -s -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  https://paimos.example.com/api/projects/2/issues \
  -d '{"title":"Fix login timeout","type":"ticket","status":"backlog","priority":"high",
       "description":"Session expires after 5 minutes instead of 24 hours",
       "acceptance_criteria":"- [ ] Session lasts 24h\n- [ ] No regression on TOTP flow"}'

# Update issue status (PUT is partial — only send what changes)
curl -s -X PUT -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  https://paimos.example.com/api/issues/42 \
  -d '{"status":"done"}'

# Look up an issue by key
curl -s -H "Authorization: Bearer $KEY" \
  "https://paimos.example.com/api/search?q=PAI-42"
```

### 3. Comments and collaboration

Comments are the natural place for agents to post build reports,
review notes, and anything a human teammate would drop into a ticket.

```bash
# Markdown is rendered in the web UI
curl -s -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  https://paimos.example.com/api/issues/42/comments \
  -d '{"body":"## Build Report\n\nAll tests pass\n- Backend: 42 tests, 0 failures\n- Frontend: typecheck clean"}'
```

For retry-safe internal notes, add `visibility:"internal"` and a bounded
`client_request_id` (`A-Z`, `a-z`, digits, `.`, `_`, `:`, `-`; at most 128
bytes). The identity is scoped to the authenticated author. An exact retry
returns the original comment; reuse for another issue or body returns `409`.
Keyed notes bypass the generic response cache, so it never duplicates the
confirmed note response in `idempotency_keys`. A keyed comment can never be
external.

### 4. Time tracking

Agents should log time against the issues they work on so humans can
see the cost and cadence of agent-driven work alongside their own.

```bash
# Log time spent on an issue
curl -s -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  https://paimos.example.com/api/issues/42/time-entries \
  -d '{"minutes":30,"description":"Implemented fix and wrote tests"}'
```

### 5. Sprint management

```bash
# List active sprints (add ?include_archived=true for historical)
curl -s -H "Authorization: Bearer $KEY" https://paimos.example.com/api/sprints
```

---

## Access model

An agent does not have a separate identity class. It authenticates
**as a user account** via an API key, and whatever that user can see
and do, the agent can see and do.

PAIMOS uses two orthogonal layers:

1. **Role** (`admin` / `member` / `external`) — gates admin-only
   actions (project CRUD, user CRUD, some delete paths).
2. **Per-project access level** (`none` / `viewer` / `editor`) — gates
   read and write on individual projects and their issues.

- **Admin** agents bypass per-project checks — effectively editor on
  every project, plus admin-only surface.
- **Member** agents default to `editor` on every non-deleted project
  (seeded at user creation); individual projects can be downgraded to
  `viewer` or `none`.
- **External** agents default to `none` and must be granted `viewer`
  or `editor` per project explicitly; portal endpoints still apply.

404 is returned for projects/issues the agent can't view (no
existence oracle); 403 when a viewer tries to edit.

**Recommendation**: create a dedicated user account for each agent
(e.g. `ci-bot`, `triage-agent`, `release-agent`) with the minimum role
it needs. Do not share keys across agents — revoking a compromised
key should never disrupt an unrelated workflow.

---

## "Implement this" — UI-triggered local runs and drafts (PAI-605)

PAIMOS can hand a ticket to a coding agent **on a developer's own workstation**.
A button in the web UI creates a *run*; the developer's local runner picks it up
over the existing SSE channel, executes (Claude Code by default), and reports the
result back onto the ticket. This is a separate **execution** surface from the
render-only adapter protocol (`paimos skill render`) — the adapter turns a
canonical agent artifact into a harness file; the runner *executes* work.

PAIMOS can also create **draft runs** through a configured model provider. Draft
runs use the same run record and provenance fields, but they do not claim a
local runner, do not edit a checkout, do not run tests, and cannot deploy. Their
output is posted as an internal issue comment with clear draft provenance.

**Default posture: opt-in, repo-scoped, confirm-gated, and report-back only — no
auto-deploy.** Deploy stays a manual step until the deploy-gating phase.

### Run lifecycle

```
queued → running → completed
                 → tests_passed → deployed | failed
                 → tests_failed → failed
                 → deployed | failed | cancelled | drafted
```

The runner itself sets `running` / `completed` / `tests_passed` / `tests_failed`
/ `failed` / `deployed`;
`cancelled` is the decline-the-prompt off-ramp before `running`, and
`tests_failed` is set only when the configured `--test-exec` actually runs and
exits non-zero.
Draft providers move their server-created run from `running` to `drafted` after
posting the draft comment. Terminal statuses (`completed` / `drafted` /
`deployed` / `failed` / `cancelled`) are enforced
server-side — a run can't be moved back out of one.

### Endpoints

```bash
# Create a run (the "Implement this" / provider action button). Project-editor gated.
# Optional body:
#   {
#     "action_key": "claude_cli.implement",
#     "agent_name": "codex",
#     "device_id": "<target runner>",
#     "deploy_target": "ppm",
#     "source_draft_run_id": 41,
#     "options": {
#       "profile_id": "balanced",
#       "effort": "standard",
#       "prompt_preset_ref": "default",
#       "context_pack": "issue"
#     }
#   }
curl -X POST -H "Authorization: Bearer $KEY" \
  "$BASE/api/issues/PAI-265/implement"

# List a ticket's runs (issue-access gated) — the UI's run-status card.
curl -H "Authorization: Bearer $KEY" "$BASE/api/issues/PAI-265/runs"

# Fetch / update a single run (requester or admin). The runner PATCHes the
# status transitions and the structured report.
curl -H "Authorization: Bearer $KEY" "$BASE/api/runs/42"
curl -X PATCH -H "Authorization: Bearer $KEY" \
  -d '{"status":"deployed","version":"4.6.0","deploy_target":"ppm",
       "tests_summary":"42 passed",
       "repo_url":"https://github.com/example/app","branch_name":"pai-702",
       "commit_base_sha":"1111111111111111111111111111111111111111",
       "commit_sha":"2222222222222222222222222222222222222222"}' \
  "$BASE/api/runs/42"

# Online, implement-capable runners for a project (the device picker).
curl -H "Authorization: Bearer $KEY" "$BASE/api/projects/2/runners"

# Project-level run history (used by the Project Agents launchpad).
curl -H "Authorization: Bearer $KEY" "$BASE/api/projects/2/runs"
```

`action_key` is the requested provider/action. Omit it for backward
compatibility; the server records the legacy local Claude action
(`claude_cli.implement`). Current local CLI actions are:

- `claude_cli.implement` — provider label `Claude Code`, run mode `edit`.
- `codex_cli.implement` — provider label `Codex CLI`, run mode `edit`.

Draft actions are available from `/api/ai/execution-options` when their backing
provider is configured:

- `openrouter_draft.implement` — provider label `OpenRouter Draft`, run mode
  `draft`; uses enabled OpenRouter AI settings with a model and API key.
- `local_model_draft.implement` — provider label `Local Model Draft`, run mode
  `draft`; uses an enabled OpenAI-compatible local endpoint and model.

Draft actions reject `device_id` and `deploy_target`. They accept the same
`options` object as AI actions: `profile_id`, `effort`, `prompt_preset_ref`, and
`context_pack`. The run stores the resolved model/profile/effort/prompt/context
metadata, token counts when reported, and safe context-source provenance.

Local runners also report declared Git evidence: `repo_url`, `branch_name`,
`commit_base_sha`, and `commit_sha`. The produced range is `(base, head]`, so
multiple commits remain representable; equal base/head values explicitly mean
that the run produced no commit. PAIMOS validates URL and object-ID shape but
does not fetch or validate the references against a forge. That boundary is
deliberate: the local runner observes the checkout, while the backend performs
no Git operations and holds no push credentials. Pull-request references and
automatic anchor creation are not part of this contract.

A reviewed draft can be handed to a trusted local runner by creating a follow-up
run with a local CLI `action_key` and `source_draft_run_id`. The source run must
belong to the same issue, be terminal `drafted`, have `run_mode: "draft"`, and
not already have a `followup_run_id`. Draft actions are rejected as follow-ups,
so the second step is always an edit-capable local runner. Activity rows expose
only the linked run IDs (`source_draft_run_id` / `followup_run_id`), not draft
prompt bodies.

`agent_name` is optional. When present, it must be the lowercase project-agent
name declared on the issue's project (`GET /api/projects/:id/agents`). The
server stores it on the queued run so UI history and runner handoff can keep the
project-agent choice separate from the CLI provider/action.

Run records and issue `ai_work_status` include `action_key`, `provider_kind`,
`provider_id`, `provider_label`, optional `model`, `run_mode`, profile, effort,
prompt preset, context pack, and token metadata so list badges, comments, and
audit views can distinguish Claude, Codex, local-model, and OpenRouter requests.

`POST …/implement` publishes an **`implement_requested`** SSE event on the
project's `…/agents/events` stream — the run id rides in the event's `rev`, the
issue key in `name`.

The Project Agents tab combines `GET /api/projects/:id/agents`,
`/agents/:name.json`, `/runners`, and `/runs` into a launchpad: each declared
agent shows artifact links, compatible online adapters, runner availability,
commands for skill/session/runner setup, and recent agent-attributed runs.

### The runner

```bash
# On the developer's workstation, in the repo checkout:
paimos run-agent watch --project PAI --repo-root .
#   subscribes advertising implement-capability (?implement=1), and on an
#   implement_requested event: claims matching-action runs, generates an
#   issue-context prompt, spawns Claude Code (override with --exec) in --repo-root,
#   optionally runs --test-exec, then reports completed (no test command),
#   tests_passed / tests_failed (test command ran), or failed.
#   It reconnects on a dropped stream, processes one job at a time, and
#   periodically catches up on queued runs it missed; prompts before each run
#   unless --yes. Two runners never double-execute the same run (atomic claim).
```

The runner advertises one local CLI action on connect. The default
`--exec "claude"` advertises `claude_cli.implement`; an executable whose basename
contains `codex` advertises `codex_cli.implement`. Use `--action-key` to override
the inference when a wrapper command name would be ambiguous:

```bash
paimos run-agent watch --project PAI --repo-root . --exec "codex exec" --action-key codex_cli.implement
```

The default `--exec "claude"` is normalized to Claude Code print mode with
`--output-format stream-json`, a non-interactive permission mode, and an
allowlist of repository read/edit tools (`Read,Glob,Grep,Edit,Write`). The
validated argv passes that list to `--tools` as the hard built-in availability
boundary and to `--allowedTools` as the additional approval policy. It also
uses `--safe-mode`, disabling inherited CLAUDE.md, skills, plugins, hooks, MCP,
custom commands/agents, and other user/project customizations. It does not
enable Bash, browser, or permission bypass by default. Operators can
explicitly set `--claude-permission-mode` and `--claude-allowed-tools`; both are
validated before launch. The supported installed modes are exactly
`acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, and `plan`.
Selecting `bypassPermissions` is rejected unless the separate
`--unsafe-allow-bypass-permissions` flag is also present, and the generated argv
then includes Claude's `--allow-dangerously-skip-permissions` acknowledgement;
merely typing the mode never opts into bypass. A custom `--exec` remains the
deliberate raw-command escape hatch.
The generated issue prompt
is fed on stdin, so a queued run never opens an interactive TUI. The spawned
command sees `PAIMOS_RUN_ID`, `PAIMOS_PROJECT_ID`, `PAIMOS_ISSUE_KEY`,
`PAIMOS_ISSUE_TITLE`, `PAIMOS_CONTEXT_PACK`, `PAIMOS_CONTEXT_PACK_LABEL`, and
`PAIMOS_PROMPT_FILE`. The supervisor also sets `PAIMOS_RUN_CORRELATION_ID`,
`PAIMOS_RUN_PROVIDER`, and `PAIMOS_RUN_ADAPTER` to its exact telemetry identity;
custom provider commands can read the prompt file themselves.

When the run carries `agent_name`, the watcher fetches the canonical
`/api/projects/:id/agents/:name.json` artifact before spawning the command. The
prompt includes a bounded, redacted project-agent summary, and the child process
also receives:

- `PAIMOS_AGENT_NAME` — the selected project agent.
- `PAIMOS_AGENT_ARTIFACT_FILE` — a temporary redacted copy of the canonical
  artifact for harnesses that prefer structured input.

`--exec` is the explicit provider-neutral/raw fallback. It runs through a shell
(`sh -c`), so quoting, pipes, and chaining work for custom commands, e.g.
`--exec 'codex exec "$(cat "$PAIMOS_PROMPT_FILE")"'`. Raw output is not parsed
or persisted as telemetry. Telemetry identity follows the actual execution
mode, not the queue action: exact built-in Claude is `anthropic/claude-code`, a
direct Codex CLI command is `openai/codex-cli`, and raw Aider, OpenCode,
wrappers, or shell pipelines are the neutral `paimos/custom-runner`.

The supervisor parses only bounded Claude JSON-line envelopes. It translates
an allowlist of event type/tool-name classes into the wire phases `starting`,
`planning`, `implementing`, `testing`, `waiting`, and `reviewing`; prompt text, assistant text, tool
arguments, command output, source text, environment values, and provider error
bodies never enter telemetry. Oversized events, aggregate stream floods, and
malformed/unknown events fail closed. Terminal outcomes distinguish spawn
failure, malformed stream, silent child, execution timeout, cancellation,
provider failure, runner disappearance, report failure, and normal exit.

While a child is alive, the supervisor emits its own liveness heartbeat; this
does not depend on model callbacks. `--execution-timeout` (default `2h`),
`--heartbeat-timeout` (default `5m` of child stdout/stderr silence), and
`--heartbeat-interval` (default `15s`) are configurable. Timeout/cancellation
terminate the process group owned by the runner before any terminal report is
chosen, preventing a late successful exit from overwriting the failure.
On Unix-family platforms this kills the complete owned process group and is
covered by a descendant-death fixture. On other Go targets the portable fallback
can kill only the direct child; wrappers on those platforms must not detach
descendants, and this limitation is explicit rather than claiming tree-level
enforcement.

The supervisor sends these facts directly to `POST /api/runs/:id/telemetry`.
It owns one stable correlation id and strictly increasing sequence per run.
The correlation is a cryptographically random UUID created once after the
claim and reused in every fact and child environment value. Heartbeat and
semantic callbacks are serialized per run. Ambiguous network/5xx results retry
the exact request body and sequence; allocation advances only after append 201
or authoritative duplicate 200 acceptance. A 409 triggers a run refetch so
cancellation, another terminal/reaped status, and disappearance stay distinct.
Heartbeat reports use `kind=heartbeat`, `heartbeat=true`, and no activity text;
semantic phase/needs-input/blocker reports use `heartbeat=false`. Therefore a
stream of model activity cannot keep the supervisor watchdog alive. Claiming a
run atomically persists `expects_supervisor_telemetry=true` before launch.
Ordinary high-volume phase chatter may be coalesced when its bounded channel is
full, but `needs_input` is a priority safety fact: it evicts queued ordinary
progress, is delivered exactly once, and stops the one-shot runner. A burst of
provider output therefore cannot hide a human or permission blocker.

`--test-exec` is the only runner-owned way to prove tests in the run record. It runs
after the agent command through the same process-group, heartbeat, silence, and
execution watchdog as the provider and records a bounded result summary in
`tests_summary` (never command text or output). The server accepts any non-empty
bounded test evidence rather than one runner-specific sentence, and reports
`tests_failed` if the command exits non-zero, and skips deploy on test failure:

```bash
paimos run-agent watch --project PAI --repo-root . --yes \
  --test-exec "npm test"
```

When the provider intentionally leaves an uncommitted worktree, the runner
captures a bounded source-free snapshot before execution, at the state given to
`--test-exec`, and again after successful tests. If the covered source state is
stable and differs from the pre-run state, it sends only a domain-separated
`implementation_result_digest`; source bytes, paths, filenames, diffs, test
commands/output, environment, and remote credentials never leave the
workstation. The configured test command is represented inside that digest only
by a one-way hash.

The covered source surface is every actual node discovered from the frozen HEAD
tree or stage-0 index, plus non-ignored untracked nodes and every repository
`.gitignore` policy file (including self-ignored policies). Raw regular-file
bytes, symlink targets, executable state, real deletions, and recursively nested
Git worktrees are hashed through no-follow descriptors. Index modes and object
IDs are discovery/stability inputs rather than hashed source; every discovered
actual node that enters the covered set is raw-hashed. Mutable attributes,
filters, diff drivers, replace refs, inherited `GIT_*`, local/global excludes,
or Git presentation config cannot change the result. The runner resolves and
freezes one trusted absolute Git executable before starting the provider. It
also resolves `--repo-root` once to its physical directory and uses that frozen
path for provider, test, and deploy execution; later retargeting of an input
symlink cannot redirect execution away from the repository being evidenced.
Here, trusted deliberately means that neither the executable nor any ancestor
directory is mutable by the runner identity. A same-user package-manager Git
(or a root-owned Git when the runner itself is root) is therefore rejected;
select a system- or administrator-owned immutable Git through `PATH`.

The boundary is deliberately repository-source evidence, not a hermetic build
attestation. Payloads excluded by repository `.gitignore`, external dependencies,
the compiler/toolchain, and other process environment state are not hashed.
Snapshots prove the covered source state at both sides of the configured test;
they cannot prove that an arbitrary test program did not temporarily mutate and
restore state while it ran, or that a passing test implements the issue. Use a
trusted, preferably hermetic `--test-exec`; use a real reviewed commit for the
strongest provenance.

The explicit snapshot ceilings—more than 10,000 covered nodes, more than 64 MiB
of covered raw content, a path or Git-output record above its bound, or nested
repository depth above eight—disable the source-free/no-commit binding. A
changed-commit lane remains available only when a separate streaming proof
establishes both that the base and tested commit tree IDs differ and that the
index plus every covered raw worktree node exactly matches the tested HEAD
before and after the configured test. That proof computes Git blob identities
directly from no-follow raw nodes, rejects non-ignored untracked source and
untracked ignore-policy files, recurses through initialized submodules, and is
bounded by a 1,000,000-path ceiling, repository-depth ceiling, and 30-second
deadline. It therefore supports large committed source without trusting Git
filters, attributes, diff/status presentation, or a merely changed commit ID.
Whenever the richer pre-test snapshot fits, its exact post-test recheck remains
mandatory too. The no-commit lane requires all three bounded snapshots and a
covered change before tests followed by exact stability after them.

An empty or metadata-only changed commit; a dirty worktree that differs from the
reported commit; an exceptional, conflicted, or hidden index entry; an untrusted or mutated Git
executable; repository-root/topology/identity drift; an unsafe filesystem node;
a hashing or integrity failure; timeout or cancellation; no covered source
change; or differing available snapshots always hard-fails and never enters the
changed-commit fallback. Ignored payloads, external dependencies/toolchains, and
transient mutations restored within the test window remain outside the stated
boundary. Opt-in same-issue attachments remain a separate fallback.

`--attach-logs` (OFF by default) captures the job's combined output and attaches
it to the ticket as a log, stamping `log_attachment_id`. It is opt-in because
agent output can contain secrets, and a ticket attachment is visible to every
project member — only enable it for repos/tickets where that's acceptable. The
capture is capped; the cap does not turn raw output into telemetry.

A normal provider exit without `--test-exec` reports `completed`, never
`tests_passed`; its issue comment explicitly says tests were not run. Provider
exit itself emits `reviewing`, never telemetry `completed`, because tests and
deploy have not yet finished. Configured deploy commands use the same local
process-group, timeout, silence, and output watchdog. Once `tests_passed` has
closed the run's telemetry/control stream, the deploy phase emits no later
telemetry; its authoritative outcome is the following lifecycle PATCH.
Deployment and smoke verification remain separate facts; the
runner reports neither unless the corresponding configured command actually
ran successfully. Before any configured deploy starts, the runner first commits
the tested commit or source-free digest through `tests_passed`; the later
`deployed`/`failed` transition stays pinned to that tested Git state even if the
deploy command mutates the checkout. If deploy fails or is cancelled after tests passed, the final
failed record retains the bounded test summary, version, and attempted
`deploy_target`; a downstream failure never erases earlier verification facts.

The run lifecycle is enforced server-side: status changes must follow a legal
edge (e.g. a run can't jump straight to `deployed`), and a terminal run
(`completed`/`drafted`/`deployed`/`failed`/`cancelled`) is immutable. For non-requester project-editor
claims, the server requires the caller's user, device, and requested `action_key`
to match a live implement-capable runner connection; after the first
`queued -> running` claim, later writes are limited to the requester, admin, or
the stamped `claimed_by` executor.

Before a supervised success crosses out of `running`, the local control pump
must terminate without an internal renewal, identity, or journal failure; all
locally claimed cancellations are resolved; and the runner must revoke the
exact server cancellation lease. Lease revocation atomically refuses to cross
any cancellation already pending confirmation or accepted. The lifecycle
compare-and-swap also rejects supervised success while an unrevoked lease or
pending/accepted running cancellation remains, so a cancellation racing the
boundary wins.

### Single-run telemetry (PAI-799)

Run lifecycle status and run telemetry are separate contracts. Lifecycle
`PATCH /api/runs/{id}` remains the authority for state transitions. Telemetry
adds append-only facts about that one run:

```http
POST /api/runs/799/telemetry
GET  /api/runs/799/telemetry?after_sequence=0&limit=100
GET  /api/runs/799/telemetry/latest
```

The POST is limited to the run requester, stamped claimer, or an admin. Missing
and unauthorized runs both return 404. Reads use normal run visibility. A
terminal run (`completed`, `tests_passed`, `tests_failed`, `drafted`, `deployed`,
`failed`, or `cancelled`) rejects every new fact and conflicting replay with
409. An exact already-persisted same-sequence replay remains a read-only 200
duplicate acknowledgement and does not mutate history or the latest projection.
For telemetry, `tests_passed` and `tests_failed` close the fact stream even
though the lifecycle still permits their explicit deployment/failure edges;
those later outcomes are authoritative run PATCHes, not post-result telemetry.

Example report:

```json
{
  "sequence": 3,
  "correlation_id": "claude-session-abc",
  "provider": "anthropic",
  "adapter": "claude-code",
  "agent_reported_at": "2026-08-20T10:00:00Z",
  "kind": "progress",
  "heartbeat": true,
  "phase": "testing",
  "activity": "Running the documented backend test gate",
  "needs_input": false,
  "blocker_state": "none",
  "estimate_revision": 2,
  "progress_percent": 75,
  "eta_seconds": 300,
  "eta_min_seconds": 180,
  "eta_max_seconds": 480,
  "estimate_source": "adapter",
  "estimate_confidence": 0.8,
  "estimate_basis": "Three of four named verification checkpoints completed"
}
```

`sequence` must increase; exact same-sequence/same-body replay returns 200 with
`duplicate: true`, including after the run becomes terminal, while conflicting
duplicates, out-of-order reports, and every new post-terminal fact return 409;
a newly appended fact returns 201. `correlation_id`, `provider`, and `adapter` become immutable after the
first accepted report. Delayed reports are accepted when their sequence is
newer. `agent_reported_at` is retained as agent evidence, but freshness,
clock-skew detection use `server_received_at`. Active liveness uses only the
server-received timestamp of a report explicitly marked `heartbeat=true`;
latest-event freshness remains separate. Thus a bad workstation clock or a
stream of non-heartbeat semantic events cannot reset the supervisor watchdog.

Progress and ETA are optional. When supplied they require a monotonic
`estimate_revision`, `estimate_source`, confidence from 0 through 1, and a
short evidence basis; ETA always requires a complete, ordered range. PAIMOS never
creates a percentage from elapsed wall-clock time. A run with no reports is
returned by `/latest` as `instrumented: false`, `liveness: "unknown"`, and
`latest: null`—never as 0%. The indexed snapshot exposes `latest_event`,
`latest_heartbeat`, `latest_semantic`, and `latest_estimate` plus separate
freshness ages. A later heartbeat-only report does not erase activity,
needs-input/blocker, progress, or ETA evidence. History remains the append-only
authority. The projection is fully reconstructible from ordered history; the
M143 upgrade performs that rebuild rather than trusting existing pointer rows,
and an equivalence regression compares rebuilt output with incremental writes.
SSE publishes only `{type: "run_telemetry", name:
run_id, rev: sequence}` as an invalidation hint, and consumers refetch REST.

The body uses a strict field allowlist and small one-line limits. Never send raw
prompts, tool arguments, command output, environment values, secrets, source
contents, or arbitrary provider payloads in `activity` or `estimate_basis`.
Those data classes have no telemetry field and unknown JSON keys are rejected.
The 280-byte activity and 240-byte estimate-basis limits are UTF-8 byte limits,
not character counts. HTTP, CLI, MCP, adapters, and SQLite enforce the same
boundary; MCP validates bytes locally because JSON Schema `maxLength` counts
code points.
The server also rejects obvious bearer tokens, credential assignments, cloud
keys, and private-key headers in `activity` or `estimate_basis`; adapters remain
responsible for never constructing those fields from raw provider data.

The provider-neutral CLI seam is:

```bash
paimos run report "$PAIMOS_RUN_ID" \
  --sequence 3 --correlation-id claude-session-abc \
  --provider anthropic --adapter claude-code \
  --kind progress --heartbeat --phase testing \
  --activity "Running the documented backend test gate" \
  --estimate-revision 2 --progress-percent 75 \
  --eta-seconds 300 --eta-min-seconds 180 --eta-max-seconds 480 \
  --estimate-source adapter --confidence 0.8 \
  --basis "Three of four named verification checkpoints completed"
```

`PAIMOS_RUN_ID`, `PAIMOS_PROJECT_ID`, `PAIMOS_RUN_CORRELATION_ID`, `PAIMOS_RUN_PROVIDER`, and
`PAIMOS_RUN_ADAPTER` can supply the stable runner context. `--dry-run` prints
the exact request. MCP clients use `paimos_report_progress`, which maps to the
same POST and forwards only the allowlisted fields.

The built-in supervisor serializes heartbeat and structured callbacks through
that same POST. It owns delivery order and estimate revisions; an adapter may
provide progress/ETA only with source, confidence, and a bounded evidence
basis. Without that evidence, progress and ETA stay absent rather than becoming
a fabricated percentage.

Claude Code is a proof adapter, not a server special case:

| Claude Code signal | Provider-neutral report |
|---|---|
| session/run start | `kind=heartbeat`, `phase=starting`, stable correlation id |
| bounded adapter phase change | `kind=phase`, one of the phase enum values |
| permission or human question | `kind=needs_input`, `needs_input=true`, `blocker_state=input` |
| named checkpoint completion | `kind=progress`; optional evidence-backed estimate revision |
| quiet active session | `kind=heartbeat`; no fabricated percentage |
| normal exit | `kind=phase`, `phase=reviewing`; lifecycle verification remains pending |

Codex CLI uses the same contract explicitly:

| Codex signal | Provider-neutral report |
|---|---|
| process start / supervisor tick | `provider=openai`, `adapter=codex-cli`, `kind=heartbeat` |
| repository analysis | `kind=phase`, `phase=planning` |
| patch application | `kind=phase`, `phase=implementing` |
| test command class | `kind=phase`, `phase=testing` |
| normal exit | `kind=phase`, `phase=reviewing`; lifecycle later records `completed` or a test-evidenced status |

Aider is the third-harness mapping for an explicit Aider adapter that opts into
this endpoint. Merely using raw `--exec aider` stays `paimos/custom-runner`:

| Aider signal | Provider-neutral report |
|---|---|
| adapter-owned tick | `provider=<selected-model-provider>`, `adapter=aider`, `kind=heartbeat` |
| repository map/read phase | `kind=phase`, `phase=planning` |
| edit commit/application | `kind=phase`, `phase=implementing` |
| operator question | `kind=needs_input`, `phase=waiting`, `blocker_state=input` |
| adapter failure | `kind=blocker`, `phase=unknown`; no raw error body |
| normal exit | `kind=phase`, `phase=reviewing`; never pre-claims test/deploy completion |

The adapter summarizes only the allowlisted state. Claude hook payloads, tool
inputs/results, prompts, and transcript text are never copied. Provider adapters
map to the same fields without changes to server or domain code.

The server watchdog runs at boot and on a configurable interval. It separately
fails an unclaimed queued run, a supervised run that never delivered its first
heartbeat, and a supervised run whose latest heartbeat is stale. A durable
claim marker preserves the longer legacy fallback for old uninstrumented
runners. Every failure update repeats its status/freshness predicate, so a
heartbeat or terminal write that wins the database race suppresses the stale
write; late success after timeout/cancellation receives 409.

Enabling deploy is **triple-gated** and off by default — it runs only when all
three hold: `--allow-deploy` AND `--deploy-exec "<cmd>"` AND the run carries a
`deploy_target`. Even then it asks for a separate deploy confirmation unless
`--yes-deploy` is also passed:

```bash
paimos run-agent watch --project PAI --yes \
  --test-exec "npm test" \
  --allow-deploy --deploy-exec "just deploy-ppm" --yes-deploy
#   after a successful run with a deploy_target, commits `tests_passed` with
#   its tested artifact binding, runs the deploy command, captures the version
#   from ./VERSION, and then marks the run `deployed`.
```

For the local non-production PAI-625 demo harness and exact workstation
commands, see [`PAI_625_DEMO.md`](PAI_625_DEMO.md).

For the PAI-629/PAI-630 path from one generic action to explicit Claude, Codex,
local-model, and OpenRouter actions, see
[`IMPLEMENT_THIS_PROVIDERS.md`](IMPLEMENT_THIS_PROVIDERS.md).

The spawned command can read the generated prompt or selected-agent artifact
and report bounded progress through `paimos run report`. `needs_input` is a safe
blocker telemetry signal only: this one-shot runner stops the child and cannot
advertise pause/resume, accept an answer, or resume a session. On any transition
into a terminal status the
server auto-posts a summary comment on the ticket — attributed to the reporting
user — so the human-readable trail always matches the structured run record.

Post-M144 runs are also linked into the issue-rooted delivery audit model in the
same transaction as creation. An authenticated Implement/follow-up click records
approval of the server-canonical issue-spec digest, then opens an implementation
execution with explicit predecessor lineage. Draft mode opens a specification
execution only; `drafted` is an incomplete `draft_ready` fact and never approves
the specification. A run is linked to exactly one stage execution.

Accepted terminal PATCH transitions for delivery-instrumented version-1 runs are
normalized atomically into that linked execution; legacy version-0 runs retain
their pre-delivery behavior and are never backfilled. `completed` and `deployed`
can satisfy only the implementation stage
and only with an allowlisted commit, source-free implementation-result digest,
or same-issue attachment. For a current visible run whose QA is required,
`tests_passed` requires one of those bounded implementation bindings and
atomically satisfies that same implementation execution, starts QA,
and records QA success with a domain-separated digest binding the run, the one
selected implementation reference (commit, else digest, else attachment), and
the exact persisted bytes of bounded `tests_summary`; lower-priority conflicting
transport fields are not part of the canonical tuple. It never implies
deployment or verification. Superseded or hidden closures remain history-only,
and QA-not-applicable policy retains its existing non-projection behavior. A
terminal QA execution is retried with an exact
compare-and-swap; an active human or external QA execution is never stolen. A
later `deployed` or `failed` run-lifecycle status records a later operational
outcome and does not erase valid immutable implementation/QA evidence. Only a
new upstream execution or attempt can make that evidence lineage-ineligible.
Failures/cancellation record bounded semantic outcomes without copying provider
errors. Telemetry remains in the PAI-799 tables: M144 appends only a safe
delivery invalidation identity in the same transaction, and freshness continues
to use the separate server-received heartbeat pointer.
Every terminal status, including `cancelled`, receives an immutable
`finished_at`. If a retry, specification edit, handoff, or project move already
superseded the linked execution, the closure is retained as one exact-once
`run_lifecycle_observed` attempt-history event while current delivery truth stays
unchanged.

### Agent Mode delivery snapshots and invalidations (schema v1)

The internal Agent Mode read surface projects delivery truth without exposing
reporter IDs, run IDs, provider/adapter metadata, evidence references, prompts,
or output. It is available to authenticated internal users at:

- `GET /api/agent-mode/deliveries`
- `GET /api/agent-mode/projects/{projectID}/deliveries`
- `GET /api/agent-mode/deliveries/{deliveryKey}`
- `GET /api/agent-mode/deliveries/events`

Snapshot responses are `private, no-store` schema-v1 JSON. The top-level
`cursor` is opaque and bound to the authenticated user, permission epoch,
route audience, and result filters. Project query filtering is not an
authorization boundary; the project path is. A detail path is a selection
lookup and deliberately shares the global event scope. `selected_delivery` is
not cursor-bound and the response has only one outside-selection shape:
`selected_outside: {reason, row}`. Rows, root/project/lane count sets, and the
maximum-12 attention list all come from the same pinned database snapshot and
calculation instant.

The events endpoint is an SSE invalidation stream, not a second source of row
truth. Resume with `Last-Event-ID` (authoritative when present) or `cursor=`.
`refetch` and `checkpoint` events carry their replacement opaque cursor in the
SSE `id`; refetch data contains only currently authorized delivery hints, while
checkpoint data has no hints. Any invalid, expired, revoked, wrong-scope,
below-retention, or ahead-of-tail resume returns one identity-free `reset` with
`{"schema_version":1,"reason":"resync_required"}` and closes. Clients then
reload a snapshot. SSE responses use `private, no-store, no-transform`; malformed
non-cursor filters remain canonical problem+json `400`, and missing versus
inaccessible resources remain indistinguishable canonical `404`s.

The complete field and enum contract is the served OpenAPI document at
`GET /api/openapi.json`. Frontend transport adaptation is intentionally a
separate integration step; generic change-stream envelopes are not aliases for
these Agent Mode events.

Agent Mode voice uses the same internal-only, canonical-404, CSRF, and
`private, no-store` boundary:

- `POST /api/agent-mode/voice/transcribe?language=en|de` accepts one raw,
  allowlisted browser audio/video blob (12 MiB maximum) and returns exactly
  `{utterance_id,text,final:true}`. Audio and transcript are ephemeral.
- `POST /api/agent-mode/voice/speak` accepts exactly
  `{template,delivery_id,delivery_revision,candidate_ids,locale}`. Templates
  are `status`, `note_ready`, or `clarification`; there is no caller-text
  field. The server authorizes the primary and up to three active candidates
  in one coherent snapshot, rechecks the revision/capability, and narrates
  only closed structured facts.

Voice audit rows contain action/surface/provider/model/unit/outcome metadata
only. Intake and Agent Mode share the same per-user STT and TTS admission and
daily-budget pools.

### External delivery-stage reports (PAI-810)

External-stage reporting is a separate machine-to-machine boundary from local
run telemetry. A handoff is bound to one delivery/issue, immutable attempt plan,
stage execution and predecessor lineage, authority epoch, reporter
registration, credential epoch, expiry, schema major, fixture digest, and safe
context digest. Handoff lifecycle is
`issued → accepted → active|waiting|blocked → succeeded|failed`; it does not
widen the canonical delivery-stage enums.

Every external pull, accept, or report requires the exact registered Bearer API
key plus a separate credential in the inbound-only
`X-PAIMOS-Handoff-Secret` header. Class, role, dependency key, and evidence
ceiling are resolved from the current registration and key binding inside the
transaction, including for wildcard API keys. Missing, wrong, rotated,
revoked, expired, unauthorized, or stale-authority credentials share canonical
concealment.

Operators provision that server-owned state through the authenticated Agent
Mode admin control plane, outside the frozen adapter routes. The CLI exposes
`external-stage registrations list|create|revoke` and `prerequisites seal`;
the list contains current non-revoked safe IDs only, and every mutation is
idempotent and mandatorily audited. Handoff creation consumes the exact returned
`registration_id`. Guessed IDs and direct-SQL provisioning are unsupported.
Each sealed prerequisite explicitly carries `requirement: required|optional`;
the CLI spelling is `required|optional:dependency=registration-id`, with no
default. A sealed empty set and required-only, optional-only, or mixed sets up
to 16 rows are valid. Only required rows gate owner success; optional rows do
not transfer authority or complete canonical state.

Owner and dependency streams use independent exact-next sequences and latest
projections. A Janus dependency report may satisfy or block only its declared
prerequisite and cannot update owner latest or complete a canonical stage.
Pharos deployment success remains `deployed_unverified`. Verification is a
separate verification-stage handoff for the same delivery and attempt: exact
environment plus artifact version/digest/commit must match the deployment,
while the allowlisted deploy and verification workflow symbols may differ.
Both verification observation and server receipt must be strictly after the
deployment server receipt.

One semantic report atomically commits its sequence, owner/dependency fact,
typed evidence or blockers, latest projection, mandatory safe audit, and
durable Agent Mode change hint; wake occurs only after commit. An exact replay
is read-only. Conflicting replay, a gap/regression, late new report, or any
audit/hint/evidence failure produces no partial mutation.

Use `paimos external-stage`; it keeps the raw 32-byte handoff credential out of
argv, environment variables, URLs, JSON, cookies, output, errors, logs, audits,
and fixtures. Mint/rotate stream to a new durable `0600` file only; pull,
accept, and report read from that protected file or stdin. Full route, media,
fixture, digest, pin, and loss-recovery details are in
[`EXTERNAL_STAGE_CONTRACT.md`](EXTERNAL_STAGE_CONTRACT.md).

---

## Best practices for agent implementors

1. **Search before creating.** Run `GET /api/search?q=...` first so
   your agent does not create duplicate issues for the same symptom.
2. **Reference issue keys in comments.** Write `PAI-42` style keys in
   prose so cross-linking from another issue picks them up. The web UI
   autolinks them.
3. **Follow the status lifecycle.**
   Typical flow: `new → backlog → in-progress → qa → done → delivered
   → accepted → invoiced`. `cancelled` is a terminal off-ramp at any
   point. Avoid jumping straight to `done` (skips QA) or setting
   `accepted`/`invoiced` programmatically — those are usually human
   decisions. See `docs/DATA_MODEL.md` for the full enum.
4. **Partial updates.** `PUT /api/issues/{id}` is partial. Send only
   the fields you want to change; everything else is preserved.
5. **Be reasonable about rate.** There is no hard rate limit on API
   key traffic, but stay under ~10 req/s. Batch work where you can,
   and respect 5xx with exponential backoff.
6. **Handle errors.**
   - `401` — missing or invalid key
   - `403` — authenticated but not authorised (e.g. member trying to
     delete, or an edit on a view-only portal issue)
   - `404` — issue/project does not exist **or** your user has no
     access to it (the two are deliberately indistinguishable)
   - `422` — validation error (bad enum, missing required field,
     invalid parent for the hierarchy)

---

## Full API reference

- **Compact API reference**: [`api-minimal.md`](api-minimal.md) — every
  route the web SPA uses, in one page.
- **Permissions and role matrix**: see the *Access model* section above
  and the per-project `project_members` / `access_audit` model in
  [`DATA_MODEL.md`](DATA_MODEL.md) and
  [`DEVELOPER_GUIDE.md`](DEVELOPER_GUIDE.md) §4a. Admin-gated routes
  are marked with `auth.RequireAdmin`; per-project view/edit gates
  live in `backend/auth/middleware_project.go`.

---

## Example: wiring PAIMOS into an agent skill

Drop something like this into your agent's tool manifest or skill
description so it knows how to reach a PAIMOS instance:

```markdown
## Tool: PAIMOS

- Base URL: https://your-paimos-instance.example.com/api
- Auth: `Authorization: Bearer <api-key>` (key minted by a human)
- Create issue:  `POST /projects/{id}/issues`
- Update issue:  `PUT  /issues/{id}`  (partial)
- Comment:       `POST /issues/{id}/comments`
- Log time:      `POST /issues/{id}/time-entries`
- Search:        `GET  /search?q=...`
- Project ctx:   `GET  /projects/{id}/repos`
- Knowledge:     `GET  /projects/{id}/knowledge`
- Retrieve:      `POST /projects/{id}/retrieve`
- Anchors:       `GET  /issues/{id}/anchors`

Before creating a new issue, always search for the title first.
Post a build report as a comment on the issue you just finished.
Use markdown freely — the web UI renders it.
```

That is the whole integration surface. An agent that can `curl` can
collaborate.
