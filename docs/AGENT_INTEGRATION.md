# Agent integration

Aeon is the interface for people and for agents. People sign in with OIDC. Agents call the HTTP API with a scoped key, or they talk to a local `aeon-agentd` which holds that key and supervises a harness on the operator's machine.

## CLI and paimos mode

`paimos` is one binary. `paimos serve` runs the server; operator commands such as `aeon demo seed` and the agent CLI are dispatched by the same binary. The same binary behaves as `paimos` when `argv[0]` is `paimos` (the Nix package installs that name as a symlink). Help text uses the name you invoked.

Configuration lives in `~/.aeon/config.yaml`, or `~/.paimos/config.yaml` when the binary is `paimos`. The file names a default instance and a map of instances, each with a URL. API keys are not kept in that file. They are stored under the sibling `keys/` directory, mode `0600`. If a key is still in the YAML, the CLI moves it there on the next read.

A process can skip the file. `AEON_URL` together with `AEON_API_KEY` or `AEON_API_KEY_FILE` is the target. When the binary is `paimos` and `AEON_URL` is unset, `PAIMOS_URL` with `PAIMOS_API_KEY` or `PAIMOS_API_KEY_FILE` works the same way. If `AEON_URL` is set, it wins even for the `paimos` name. Do not set both the key variable and the key file.

`aeon auth login` records an instance. The key is read from `--key-file` or stdin, never echoed. `aeon whoami` calls `GET /api/me`.

Issue, project, knowledge, search, relation, tag, attachment, model resolve, and onboard commands call the configured Aeon server. `issue list` reads issue nodes beneath a project. Knowledge types on the CLI are `memory`, `runbook`, `guideline`, `external-system`, and `related-project`.

## aeon-agentd

The local supervisor is a separate binary (`cmd/aeon-agentd`), not a `paimos` subcommand. GitHub release assets are named `paimos-agentd-*`. The current Nix package `aeon-agentd` builds `bin/aeon-agentd`; both names invoke the same command source.

The supervisor's `serve` command takes `--url`, `--agent-key-file` (an absolute path, mode `0600`), a workspace, a private state directory, a stable `--daemon-id`, and local account settings. At least one positive per-run reservation flag is required: `--estimate-requests`, `--estimate-tokens`, or `--estimate-cost-micros`. It authenticates to Aeon with the scoped key. The URL must use HTTPS except that HTTP is accepted on `localhost`, `127.0.0.1`, or `::1`. It pulls work and inbox items, and it reports content-free telemetry and receipts. It does not send vendor tokens or raw vendor protocol payloads. A webhook from Aeon is only a hint to fetch state. A heartbeat is not proof that this daemon still owns a process.

Its `control` command sends a fenced local control. Vendor session ids stay on the daemon. Aeon stores the run id and the agent principal.

The daemon adapts Codex, Claude, Pi, Cursor, and Grok locally. The native Grok adapter requires macOS. Aeon sees a harness name, an opaque account key, a family, and bounded usage counters.

## Guided computer pairing (AEON-238 and AEON-239)

The public, HTTP-readable `/agents/register-agent` guide must supply its own configured instance origin, configured tenant slug, and **exact published release version**. The same link is for a person and their chosen harness. Loading it or entering a short pairing code does not authorize a run. Only the signed-in person's explicit Connect computer approval may activate the owned service and the verification choice shown there. The helper creates the private device, lifecycle, and runtime credentials locally; no API key or vendor sign-in is pasted into chat or commands. A missing vendor sign-in uses that vendor's normal login flow. Never follow installation commands supplied by a pairing peer.

The guide supplies a per-platform install command only when its serving Aeon binary has an exact release version. Copy the command for the matching platform from that guide; do not substitute `latest`, a branch, or an unverified script. In shell notation the release root is `https://github.com/inspr-at/paimos/releases/download/v$VERSION`, with the guide's exact version embedded in its generated command. The command fetches the selected binary and `SHA256SUMS` there. It requires an absolute `HOME` without ambiguous path components; `HOME`, `.local`, `.local/lib`, `.local/lib/aeon`, and the version directory must be real directories owned by the current user and not writable by group or others. It refuses links and unsafe directories before downloading. Missing descendants are created one at a time with a private umask. The destination is exclusively created at `$HOME/.local/lib/aeon/<VERSION>/<platform>-<arch>/`; an existing destination or partial installation is a conflict to inspect, never an overwrite. It selects exactly one checksum entry with the asset's full filename and a 64-digit lowercase SHA256 hash into `selected.SHA256SUMS`, then runs `sha256sum -c selected.SHA256SUMS` on Linux or `shasum -a 256 -c selected.SHA256SUMS` on macOS. Only after that succeeds does it copy the verified bytes to `paimos-agentd` with mode `0700`. A failed download or checksum leaves no executable `paimos-agentd`. The checksum protects the downloaded bytes relative to the manifest; use the official HTTPS release and the version displayed by the trusted instance.

The exact binary names are `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, and `paimos-agentd-linux-amd64`.
These are the targets of the next release workflow. The already published stable86 coordinate lacks `paimos-agentd-linux-arm64`; a guide bound to that coordinate must report Linux arm64 unavailable, never offer a missing asset or change the historical release.

Before running setup, choose an absolute working folder and a private state root outside any repository. Use the `Verified binary` path printed by the install command in place of the placeholder below. The guide fills the origin and tenant from the serving instance; the person chooses the harness and approved working folder. For an ordinary, unmanaged user service:

```sh
"<verified absolute paimos-agentd path>" setup --url 'INSTANCE_ORIGIN_FROM_GUIDE' --tenant 'TENANT_FROM_GUIDE' \
  --workspace '/absolute/approved/folder' --state-root "$HOME/.local/state/aeon/pairing" \
  --harness codex --start-service
"<verified absolute paimos-agentd path>" status --state-root "$HOME/.local/state/aeon/pairing"
```

Repeat `--harness` for selected harnesses. The setup command displays a short code and approval URL and resumes after interruptions with the same private state root. `--account-context` is an optional visible Codex account label when more than one sign-in is available. After approval, `status` reports connection and verification outcome. To add a harness to the existing computer, use `add-harness --state-root ROOT --harness NAME`; it still needs fresh person approval. To remove one enrollment use `disconnect --state-root ROOT --account-id UUID`; omit `--account-id` for the whole computer. A drain waits for owned work; offline revocation may leave local cleanup or run accounting unconfirmed. These commands never remove vendor login stores or project files.

Codex and Cursor remain **Connect only** for automatic verification (AEON-238, Knowledge MEM-1). Codex's read-only sandbox and `approvalPolicy: never` do not isolate inherited MCP tools or startup hooks; an empty working directory is not an execution boundary. Cursor's isolated config directory and ask mode do not enforce a global no-tools policy, including startup/team hooks and hosted effects. The server and daemon share `internal/agentverification` qualification and refuse these verification runs before account probing, reservations or vendor startup. This is a helper qualification limit, not a claim that future vendor versions cannot support isolation. Never copy vendor credentials, bypass managed policy or enable verification solely because fake protocol fixtures pass.

For a live check after a reviewed helper release qualifies a harness: use the guide's pinned helper, review the exact computer/account and select verification before **Connect computer**. Inspect helper `status` and the website for one completed verification per selected harness, one at a time, at most 60 seconds each; reconnect/status must not create another run or refill its allowance. If the guide still reports unavailable, stop at Connect only. No live vendor run is part of the fixture suite.

For `--harness claude`, the local bridge also needs Node.js and the **Claude Agent SDK** package, which is separate from the Claude CLI and the ordinary Anthropic API SDK. Install Node.js using its normal vendor or package manager instructions, then install [`@anthropic-ai/claude-agent-sdk`](https://github.com/anthropics/claude-agent-sdk-typescript) in a global npm prefix outside the approved working folder (for example, `npm install -g @anthropic-ai/claude-agent-sdk` in a trusted shell outside a project). The helper checks the existing Node executable and the SDK package's declared module entry using filesystem metadata; it never runs npm, package scripts, or Node module resolution during discovery and never installs anything. If discovery cannot find them, pass `--node-path /absolute/path/to/node --claude-sdk-path /absolute/path/to/@anthropic-ai/claude-agent-sdk/sdk.mjs` to `setup` or `add-harness`, using the actual entry declared in that installed package's `package.json`. Missing dependencies block setup before approval; they do not mean vendor sign-in is required. Saved dependency pins are validated and retained on resume and when adding another harness. A conflicting override is rejected without changing the enrollment.

The concrete validation matrix is macOS 15 arm64 (`macos-15`, launchd user), macOS 15 amd64 (`macos-15-intel`, launchd user), Ubuntu 24.04 amd64 (`ubuntu-24.04`, systemd user), and Ubuntu 24.04 arm64 (`ubuntu-24.04-arm`, systemd user). The four release assets are cross-built. The isolated fake-executable install/status/drain/remove fixture passed on all four actual runners in [GitHub Actions run 36346781623](https://github.com/inspr-at/paimos/actions/runs/36346781623) for source `3744ba8`. That is implementation qualification for those runner environments, not evidence of actual paid model integration, a final release, or final-source CI. No other macOS release or Linux distribution is claimed by that evidence.

For a Nix or Home Manager managed installation, use `packages.<system>.aeon-agentd` from a reviewed, exact Aeon flake pin. The current `flake.nix` builds `${pkgs.aeon-agentd}/bin/aeon-agentd`; the GitHub asset name is different. Keep the private pairing state root and runtime credential files in an owner-only directory outside the Nix store. The owning Home Manager `launchd.agents` (macOS) or `systemd.user.services` (Linux) definition points to that pinned executable with `serve --setup-root <private-state-root>` and an owner-only umask. It must be activated only after the person approves pairing. NIX-583 currently owns the real `at.inspr.aeon-agentd` daemon. A managed-plan or service-conflict result means no installation or connection claim: keep that service untouched, have its owner review the package pin and declarative service change, drain/stop only its owned work under that review, then activate the reviewed configuration. Do not edit a generated plist/unit or let a later Home Manager activation restore a revoked pairing. Disconnection requires a reviewed configuration removal or disablement as well as server revocation and confirmed local cleanup.

## Agent keys and scopes

An operator creates a key with `paimos agent-key create --tenant SLUG (--name AGENT | --principal-id UUID) --out-file PATH`. The token is written only to that file (mode `0600`, never overwritten) and is not printed. `paimos agent-key revoke` revokes by id.

Use `--workspace-role ROLEKEY` when the agent needs workspace access. This binds the agent principal as part of key creation; its effective permissions are the intersection of the key scopes and that role. The operator cannot grant Owner or workspace Guest. For an existing principal, use `aeon access bind --tenant SLUG --principal NAME_OR_UUID --workspace-role ROLEKEY`; remove the binding with `aeon access unbind --tenant SLUG --principal NAME_OR_UUID --workspace-role`. Repeating either action is safe. These commands use the same workspace binding rules and audit event as the Members API.

Scopes are an outer ceiling. An empty list grants nothing. Unknown names and permissions that are not agent-grantable are rejected. Typical scopes are registry keys such as `nodes.read`, `nodes.write`, `inbox.send`, `intake.write`, `approvals.request`, `work_orders.write`, `run.create`, `run.claim`, `run.telemetry`, `harness.write`, and `account.manage`. For HTTP key creation, the creating principal must hold every requested scope. The operator-only `paimos agent-key create` command runs on the host without a creating principal; it can create an agent principal and does not perform that creator-permission check. It still validates requested scopes against the registry.

The HTTP middleware applies that ceiling before module handlers run. Routes with no agent mapping answer 403. Person sessions are governed by role instead. An approval grant cannot exceed the key. Journey gate scopes such as `journey.build` are checked against this ceiling. They are not themselves keys in the permission registry, so `paimos agent-key create` will not accept them. A key that already holds a registry prefix covers dotted refinements of that prefix (`nodes.read` covers `nodes.read.fields`).

## Operator project access

On the AEON host, with `AEON_DATABASE_URL` set for the tenant database, an operator can create an agent key and grant project access together:

```sh
paimos agent-key create --tenant inspr --name pharos-worker --out-file /secure/path/pharos.key --scopes nodes:read --project PHAROS_PROJECT_KEY --project-role viewer
paimos agent-key create --tenant inspr --name janus-worker --out-file /secure/path/janus.key --scopes nodes:read --project JANUS_PROJECT_KEY=viewer
```

Repeat `--project KEY --project-role ROLEKEY` or use a comma-separated `KEY=ROLE` list for several projects. The key goes only to the specified new 0600 file. If binding fails after key creation, the command reports the key ID and file path so the operator can correct access or revoke the key.

For existing people or agents, use `aeon access bind --tenant SLUG --principal NAME_OR_UUID --project KEY --role ROLEKEY` and `aeon access unbind --tenant SLUG --principal NAME_OR_UUID --project KEY`. An ambiguous name returns candidate UUIDs. Owner and Customer are workspace roles and cannot be granted on a project. Repeating a bind to the same role or an unbind adds no event. These commands run locally with database access; they are not HTTP endpoints. Project mutations use the same store path as the members API and append `binding.set` or `binding.removed` events. Operator CLI events use the per-tenant Access operator principal as actor.

## Inbox

Messages are durable rows in one tenant. Each names a sender, a recipient, an idempotency key, and one sent event. The body is not copied into the tenant-wide event payload or into a webhook.

`aeon tell` sends. The target is a `harness:agent` address or a principal UUID, with `--project` and the message text. `aeon listen --project KEY` reads the caller's inbox (at most 10 rows). `--ack` acknowledges each message that was printed. Only the recipient can acknowledge, and only an acknowledgement removes the message from the pending view. Repeating an acknowledgement is safe. `aeon message deliveries --project KEY` shows redacted delivery state, not message bodies.

Use `aeon tell <address> --project KEY --recipient-session <id> -m TEXT` for one exact live harness generation, and `--sender-session <id>` to record your generation and freeze its current label. `aeon listen --project KEY --session <id>` reads only that generation's messages plus unbound principal-wide broadcasts. With no session flags, principal sends and listen output retain their existing behavior. Session-targeted messages use the session inbox. `--session --deliver codex|claude_resume --target-ref-file PATH` hands them to an operator-supplied exact local harness reference (a private, owned file); it never claims a principal-wide target. The adapter must match the selected session; this validation also requires `harness.read`. The file contains the vendor thread/session ID, not the Aeon UUID or heartbeat `session.ref`. Managed sessions continue to use agentd drain/complete-delivery. Stopped or archived recipients return 409, and their bound reply obligations close without a fabricated reply. Replies to a session-bound message must supply the matching sender/recipient session bindings. In the session panel, unbound history lives under “Other sessions”; exact duplicate posts within 60 seconds share a count, while durable messages and receipts remain separate.

`listen --follow` uses `wait_ms=25000` long-poll requests, with no two-second sleep after a response. Address-based listeners retain their existing project/address payload and use the inbox as a wake hint with a separate cursor. `--follow --deliver` remains resident on leased, foreign-worker, blocked, rerouted or temporarily unavailable adapter work, with exponential backoff capped at 30 seconds (initial delay: `--poll-interval`, default 2s). One-shot delivery retains exit codes 3 (empty), 4 (unavailable), and 5 (another worker). Ctrl-C cancels polling and backoff. Long poll holds at most 30 seconds. A webhook or a notify is a hint. The daemon then fetches authenticated state. The webhook body is ids only and is not authority.

### Operator-installed turn-boundary hooks (AEON-281)

`aeon hook claude <event>` and `aeon hook codex <event>` read hook JSON from stdin for `PostToolUse`, `UserPromptSubmit`, and `Stop`. These are synchronous harness-executed commands: no model-started watcher is needed. Inputs/outputs were checked against installed Claude Code **2.1.284**, Codex CLI **0.158.0**, the [Claude hook reference](https://code.claude.com/docs/en/hooks), and [OpenAI hook reference](https://developers.openai.com/codex/hooks). Codex has lifecycle hooks; its older `notify` command is unnecessary here.

Bind each launched harness to its **Aeon generation**, using exactly one of:

- `AEON_SESSION_ID`: the Aeon harness session UUID returned by registration.
- `AEON_SESSION_FILE`: an owned, regular, non-symlink file containing that UUID and an optional newline.
- `AEON_SESSION_STATE_DIR`: the existing `harness run-heartbeat --state-dir` directory; the hook reads only its `session.id`, never the lease. This lets a hook observe a newly registered generation without changing the environment.

The explicit ID wins over the file, and the explicit file wins over the state directory. Missing binding/file is a quiet no-op; invalid binding fails open with a content-free diagnostic. The vendor's hook `session_id` is **not** an Aeon UUID and is never used as one. Supply the binding in the harness launch environment, separately for each worker; do not set one global session ID for unrelated sessions. The normal Aeon CLI instance configuration and recipient credentials apply, with `inbox.read` and `inbox.send` scopes (the acknowledgement route currently uses `inbox.send`).

Install from the operator's shell with the released binary and the intended instance/configuration:

```sh
aeon --instance ppm hook install --harness claude --scope user --dry-run
aeon --instance ppm hook install --harness claude --scope user
```

Use `--scope project` from the project root to edit `.claude/settings.json`; user scope edits `~/.claude/settings.json` (or `CLAUDE_CONFIG_DIR/settings.json`). Review the changes in Claude's `/hooks` and restart the session as required by the harness. The installer prints only its owned hook additions/removals, preserves unrelated settings and hooks, shell-quotes the executable/configuration paths, writes atomically, and is idempotent. Invalid JSON, symlinks, and detected concurrent edits are refused. `--dry-run` writes nothing. Uninstall removes only Aeon's marked handlers:

```sh
aeon hook uninstall --harness claude --scope user --dry-run
aeon hook uninstall --harness claude --scope user
```

Codex uses the same commands with `--harness codex`; they merge `~/.codex/hooks.json` (`CODEX_HOME/hooks.json` when set), or `.codex/hooks.json` for project scope. Review and trust the new definitions in Codex `/hooks`; project scope also requires a trusted project. The installer does not override managed policy, enable disabled hooks, or bypass hook trust. Its additional-context handlers set `additionalContextLimit: 0` to avoid Codex replacing long messages with previews. Codex may still apply its own size handling to Stop continuation prompts; see the vendor's large-output documentation.

Each invocation pulls `/api/inbox/messages?session=<Aeon UUID>&wait_ms=0`, including principal-wide broadcasts. Messages are framed as untrusted data with sender, timestamp and message ID. Bodies are JSON-quoted in full, never shortened by Aeon. A batch contains at most ten messages; later hooks retrieve any remainder. `PostToolUse` and `UserPromptSubmit` emit `hookSpecificOutput.additionalContext`; `Stop` emits `decision: "block"` with a reason only for a nonempty batch. When `stop_hook_active` is true, it returns without fetching, emitting or acknowledging, preventing repeated Stop continuations even if an acknowledgement previously failed.

Only a successful write of the complete JSON output permits `POST /api/inbox/messages/{id}/ack`. That endpoint invokes session confirmation and advances the session delivery/receipt. A broken output pipe never acknowledges. A failed acknowledgement leaves the message pending and may cause replay: delivery is at least once, not exactly once. Emitting is a transport handoff, not proof the model acted. The command returns success on runtime failures, reports only content-free errors to stderr, and has a 2.5-second total budget; installed hook entries also have a three-second harness timeout. No pending messages means no output or Stop block. Hooks run only at boundaries; guaranteed idle/mid-turn injection remains the managed agentd path.

## Harness sessions

A harness session is a public generation of a local worker on one project. `POST /api/projects/{projectId}/harness-sessions` registers it. The body names the agent principal, the harness (`codex`, `claude`, `pi`, `cursor`, or `grok`), the host, managed or unmanaged mode, worker or coordinator role, advertised capabilities, a session ref, and a worker lease. A ticket and a work shape (`ship` or `scout`) are bound together. A run, when set, must belong to that agent and to the named work order.

The same session ref and lease register again without a second row. A different active registration for that ref conflicts. Worker calls (heartbeat, yield, drain, stop) require the agent, the lease, and a harness worker scope. People can interrupt or stop a session with a harness control scope.

`GET /api/harness-sessions` lists sessions across projects, with state, harness, agent, project, and ticket filters.

## Work orders and runs

A work order is a `work_order` node plus assignment, status, acceptance criteria, and optional cost and time ceilings. Cost is integer micros. Duration is whole seconds. Status moves through `draft`, `ready`, `running`, `blocked`, `done`, and `cancelled`. `done` requires every criterion checked, evidence attached, and no live run.

`POST /api/work-orders/{id}/runs` queues a run for an agent and a model profile. The order must be `ready` or `running`, and under its ceilings. The caller is the assigned agent or holds `run.create` as a person. Routing picks an agent account with a fresh probe and allowance, and reserves the estimate. Claim binds the daemon id and generation. Telemetry then reports monotonic counters and a status. A finished report uses a terminal status (`completed`, `failed`, `cancelled`, or `ownership_lost`) and sets `ended_at`. Exact replay of the same sequence is idempotent. A divergent replay conflicts. Vendor text and secrets are not stored in telemetry.

Model profiles are tenant pins (harness, family, model, effort, tier). The first read of an empty registry seeds the catalog and records that with an event. `aeon model resolve` asks the server which profile a role should use. It does not execute a command.

## Stage handoff launch

The coordinator mounts `stagehandoff.New` (an `httpapi.Module`) with a Pharos launch-check provider and registers the compiled Pharos manifest through `plugins.Builtin`. A routed Pharos agent sends a UUID `Idempotency-Key` on each admit and consume request. Retry the same key and JSON body after a lost response: Aeon returns the original admission or consumption receipt, including `consumed_at`, without repeating the change. A changed body or principal conflicts; a new key cannot consume an already spent admission. Exact replay remains available for 24 hours after a terminal handoff. `GET /api/stage-handoffs/{id}` includes safe admission state for reconciliation.

## MCP

`aeon mcp` (or `paimos mcp`) serves a stdio MCP server. `whoami` calls `GET /api/me` on the configured instance. The server also registers `issue_list`, `issue_get`, `issue_create`, `issue_update`, `issue_comment`, `knowledge_list`, `knowledge_get`, `knowledge_create`, `knowledge_update`, and `search`. Those tools still answer that they arrive in R1. Use the CLI verbs for those operations. They already call the Aeon API.

## Ticket benefits (AEON-256)

`issue create` and `issue update` accept `--pill-en`, `--pill-de`, `--benefit-en`,
`--benefit-de` and `--hide-from-release-notes true|false`. JSON issue reads return
these values. Creation reports missing-field warnings; `--status done` goes
through the same server gate as the ticket UI. Supply the four texts and done
state in one update when appropriate. A failed completion saves neither fields
nor state. Updates send the fetched node timestamp as `If-Unmodified-Since` to
protect concurrent edits; on conflict re-read the ticket before trying again.
The raw node API replaces the whole `fields` object, so retain unrelated fields
and send its precondition when clearing or editing a field.

Pills have 2–4 words; benefits have one or two positive plain-language sentences.
German is neutral without direct address. Hidden tickets still require benefits.
The proposal in `proposals/ticket-benefit-writing.json` is compatible with AR1's
rule draft shape and is not a published company rule. See `RELEASE.md` for the
exact membership source, snapshot capture and offline release-history behavior.
