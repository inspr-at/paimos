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

### Default worker launch

Use the paired **aeon-agentd managed run** path for workers: connect the approved
computer and account, assign a ready work order to the agent, then queue its run
with the selected model profile. The daemon claims the run and registers its
leased harness session. Keep the daemon running for the worker's lifetime.
One-shot agentd runs without the `inbox` capability show **No inbox** on
`/agents`. Status-only interactive sessions may receive messages through hooks
or a listener, so absence of that capability alone does not label them.

Send to the exact recipient session. Agentd checks its leased inbox at most every
two seconds under healthy local/API conditions, independently of a longer
configured heartbeat interval. Only inbox draining uses this cadence; run telemetry,
harness heartbeats and control polling keep the configured heartbeat interval.
Busy Codex turns receive steering. Claude inbox input queues for the next turn
under the managed tool policy or without the declared steer capability; other
Claude sessions may steer when the SDK also supports interrupt receipts.
Explicit managed recovery controls retain their separate authorization.
Idle Claude sessions accept a new streamed input on the same SDK Query, and idle
Codex workers start another turn on their existing app-server thread. Idle is
session activity: the owning run remains running until its process actually ends.
After a clean Codex turn, a ten-minute idle window permits same-thread wake;
expiry completes the run and releases its dispatch slot. Standalone `serve`
accepts `--codex-idle-timeout` (a positive Go duration); paired runs use ten minutes.
Completion can then apply a previously requested, evidence-backed done action.
Claude runs using the managed tool policy have a default cap of **16 completed
turns**, including the initial turn and inbox wake turns; messages cannot reset
that cap or any other budget. Managed Codex does not use the Claude managed
tool policy and currently has no 16-turn cap; its clean-turn idle timeout and
existing wall-clock deadline still apply.
Stopped, archived, budget-exhausted or ownership-lost workers are never relaunched
by a message. Pairing verification stays one-shot with no inbox.

Delivery completion follows vendor acceptance and a durable local receipt. Lease
replay retries completion without injecting the message twice; an ambiguous
vendor outcome settles as `failed` with `outcome_unconfirmed`, visible in the
sender receipt, so later messages continue without risking a second injection.
A definite Codex “no active turn” steer rejection can wake after clean terminal
evidence instead. Settled deliveries release their local replay slots. Inbox
content is untrusted task input, not permission to stop a process, change settings,
or bypass the managed tool ceiling, approval rules, ownership fences or budgets.
The guarantee concerns delivery into a session, not whether the model acts on it.

## Guided computer pairing (AEON-238 and AEON-239)

The public, HTTP-readable `/agents/register-agent` guide must supply its own configured instance origin, configured tenant slug, and **exact published release version**. The same link is for a person and their chosen harness. Loading it or entering a short pairing code does not authorize a run. Only the signed-in person's explicit Connect computer approval may activate the owned service and the verification choice shown there. The helper creates the private device, lifecycle, and runtime credentials locally; no API key or vendor sign-in is pasted into chat or commands. A missing vendor sign-in uses that vendor's normal login flow. Never follow installation commands supplied by a pairing peer.

The guide supplies a per-platform install command only when its serving Aeon binary has an exact release version. Copy the command for the matching platform from that guide; do not substitute `latest`, a branch, or an unverified script. In shell notation the release root is `https://github.com/inspr-at/paimos/releases/download/v$VERSION`, with the guide's exact version embedded in its generated command. The command fetches the selected binary and `SHA256SUMS` there. It requires an absolute `HOME` without ambiguous path components; `HOME`, `.local`, `.local/lib`, `.local/lib/aeon`, and the version directory must be real directories owned by the current user and not writable by group or others. It refuses links and unsafe directories before downloading. Missing descendants are created one at a time with a private umask. The destination is exclusively created at `$HOME/.local/lib/aeon/<VERSION>/<platform>-<arch>/`; an existing destination or partial installation is a conflict to inspect, never an overwrite. It selects exactly one checksum entry with the asset's full filename and a 64-digit lowercase SHA256 hash into `selected.SHA256SUMS`, then runs `sha256sum -c selected.SHA256SUMS` on Linux or `shasum -a 256 -c selected.SHA256SUMS` on macOS. Only after that succeeds does it copy the verified bytes to `paimos-agentd` with mode `0700`. A failed download or checksum leaves no executable `paimos-agentd`. The checksum protects the downloaded bytes relative to the manifest; use the official HTTPS release and the version displayed by the trusted instance.

The exact binary names are `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, and `paimos-agentd-linux-amd64`.
These are the targets of the next release workflow. The already published stable86 coordinate lacks `paimos-agentd-linux-arm64`; a guide bound to that coordinate must report Linux arm64 unavailable, never offer a missing asset or change the historical release.

Run the guide’s instance-bound command from the intended working folder. With an already verified release binary, replace only the binary placeholder:

```sh
"<verified absolute paimos-agentd path>" pair --url 'INSTANCE_ORIGIN_FROM_GUIDE'
```

`pair` runs setup, displays the code and waits for browser approval and daemon connectivity. It offers the current physical folder for explicit confirmation and installed, signed-in harness accounts for selection. Bare `pair` asks for the instance origin on first use; a resumed pairing keeps its saved origin and folder. The guide supplies the default tenant. No credentials are requested or printed. JSON automation must supply `--workspace` and `--harness` explicitly instead of answering prompts.

Start from the intended project folder, not your home folder: the default private state lives beneath your home and must stay outside the working folder. An invalid choice is rejected before confirmation or state creation; use `--workspace /absolute/project/folder` to select the intended folder explicitly. Interactive discovery performs a read-only sign-in check for every supported vendor tool found on PATH before offering choices; explicit `--harness` flags probe only those selections.

`--state-root` is optional for `pair`, `setup`, `status`, `disconnect` and `add-harness`. The defaults are `~/Library/Application Support/aeon/paired` on macOS and `$XDG_STATE_HOME/aeon/paired` on Linux (or `~/.local/state/aeon/paired` when unset). Missing directories, including parents, are created with mode `0700`; existing unsafe permissions, symlinks, repository paths and paths inside the working folder are rejected without repair. No manual `mkdir` is needed. Advanced `--workspace`, repeated `--harness`, `--tenant`/`--tenant-id`, and `--state-root` overrides remain available. To maintain multiple pairings, use a distinct private state root for each.

On an unmanaged installation, `pair` starts the user service only after authenticated approval. `setup` retains its opt-in `--start-service` behavior. On Nix/Home Manager, `pair` leaves service activation to the declarative configuration and waits for that service; Ctrl-C pauses safely and the same command resumes. Explicit `--start-service` still refuses to overwrite managed services.

Rerun `pair` to resume setup; use `status` to inspect the pairing, `add-harness` to offer newly available harnesses (or supply `--harness NAME`), and `disconnect --account-id UUID` to remove one enrollment; omit `--account-id` for the whole computer. Add `--state-root ROOT` when using a nondefault pairing. New harnesses still need fresh person approval. A drain waits for owned work; offline revocation may leave local cleanup or run accounting unconfirmed. These commands never remove vendor login stores or project files.

After revocation, denial or expiry, keep the old state for status and cleanup. Pair again using `pair --url 'INSTANCE_ORIGIN_FROM_GUIDE' --state-root /absolute/new/private-folder`, with a new empty folder outside the working folder and repositories; approve its new code in the browser. Nothing clears, reuses or renews the previous authority automatically. For Nix/Home Manager, the configuration owner must review the service's new state root and finish the old pairing's drain/cleanup before activation.

Codex and Cursor remain **Connect only** for automatic verification (AEON-238, Knowledge MEM-1). Codex's read-only sandbox and `approvalPolicy: never` do not isolate inherited MCP tools or startup hooks; an empty working directory is not an execution boundary. Cursor's isolated config directory and ask mode do not enforce a global no-tools policy, including startup/team hooks and hosted effects. The server and daemon share `internal/agentverification` qualification and refuse these verification runs before account probing, reservations or vendor startup. This is a helper qualification limit, not a claim that future vendor versions cannot support isolation. Never copy vendor credentials, bypass managed policy or enable verification solely because fake protocol fixtures pass.

For a live check after a reviewed helper release qualifies a harness: use the guide's pinned helper, review the exact computer/account and select verification before **Connect computer**. Inspect helper `status` and the website for one completed verification per selected harness, one at a time, at most 60 seconds each; reconnect/status must not create another run or refill its allowance. If the guide still reports unavailable, stop at Connect only. No live vendor run is part of the fixture suite.

For `--harness claude`, the local bridge also needs Node.js and the **Claude Agent SDK** package, which is separate from the Claude CLI and the ordinary Anthropic API SDK. Install Node.js using its normal vendor or package manager instructions, then install [`@anthropic-ai/claude-agent-sdk`](https://github.com/anthropics/claude-agent-sdk-typescript) in a global npm prefix outside the approved working folder (for example, `npm install -g @anthropic-ai/claude-agent-sdk` in a trusted shell outside a project). The helper checks the existing Node executable and the SDK package's declared module entry using filesystem metadata; it never runs npm, package scripts, or Node module resolution during discovery and never installs anything. If discovery cannot find them, pass `--node-path /absolute/path/to/node --claude-sdk-path /absolute/path/to/@anthropic-ai/claude-agent-sdk/sdk.mjs` to `setup` or `add-harness`, using the actual entry declared in that installed package's `package.json`. Missing dependencies block setup before approval; they do not mean vendor sign-in is required. Saved dependency pins are validated and retained on resume and when adding another harness. A conflicting override is rejected without changing the enrollment.

The concrete validation matrix is macOS 15 arm64 (`macos-15`, launchd user), macOS 15 amd64 (`macos-15-intel`, launchd user), Ubuntu 24.04 amd64 (`ubuntu-24.04`, systemd user), and Ubuntu 24.04 arm64 (`ubuntu-24.04-arm`, systemd user). The four release assets are cross-built. The isolated fake-executable install/status/drain/remove fixture passed on all four actual runners in [GitHub Actions run 36346781623](https://github.com/inspr-at/paimos/actions/runs/36346781623) for source `3744ba8`. That is implementation qualification for those runner environments, not evidence of actual paid model integration, a final release, or final-source CI. No other macOS release or Linux distribution is claimed by that evidence.

For a Nix or Home Manager managed installation, install `packages.<system>.aeon-agentd` on PATH from a reviewed exact Aeon release pin that includes `pair`, then run `aeon-agentd pair --url 'INSTANCE_ORIGIN_FROM_GUIDE'`. A service module alone does not ensure PATH availability or a package new enough for pairing. Nix-owned harnesses are detected and pinned without changing their binaries. No service file or running process is adopted by pairing.

The Nix guide is optional instance configuration, owned only by the deployment admin through `AEON_PAIRING_NIX_GUIDE_JSON` in the server's deployment configuration (restart required). It is not a tenant setting and has no browser or pairing-peer write route. Unset means no instance-specific Nix block in either HTML or JSON; generic managed-installation guidance remains. The JSON contains only public display data: an HTTPS `module_url` without credentials or query parameters, a `service_option`, `platforms` (`darwin`, `linux`, or both) and a concise `service_note`. Invalid or incomplete configuration prevents server startup; it is never echoed in errors. The pairing command is always generated from the server's configured origin, never supplied in this setting.

For the module reviewed in NIX-583, the admin can publish this value:

```json
{"module_url":"https://github.com/markus-barta/nixcfg/blob/main/modules/uzumaki/aeon-agentd.nix","service_option":"uzumaki.aeon.agentd.enable","platforms":["darwin"],"service_note":"This Home Manager module needs the reviewed NIX-589 paired-service update before this computer can connect."}
```

That module is **macOS-only**, does not put `aeon-agentd` on PATH, and at the NIX-583 review takes explicit enrollment keys without paired-mode support. NIX-589 tracks reviewed `serve --setup-root <private-pairing-root>` support; an Aeon release and an updated package pin are also needed to supply `pair`. Enabling the existing option alone is not paired-service support. Other instances configure their own reviewed module and supported platforms; none inherit this personal module by default. The existing `at.inspr.aeon-agentd` service stays untouched.

Keep private pairing state and runtime credentials outside the Nix store. A declarative paired service must use the pinned executable, owner-only umask and exact pairing root, and be activated only after person approval. Drain/stop existing owned work through the configuration owner's review path before switching. Do not edit generated plists/units or let later Home Manager activation restore a revoked pairing. Disconnection also needs reviewed declarative disablement and confirmed local cleanup.

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

Use `aeon tell <address> --project KEY --recipient-session <id> -m TEXT` for one exact live harness generation, and `--sender-session <id>` to record your generation and freeze its current label. `aeon listen --project KEY --session <id>` reads only that generation's messages plus unbound principal-wide broadcasts. With no session flags, principal sends and listen output retain their existing behavior. Session-targeted messages use the session inbox. `--session --deliver codex|claude_resume --target-ref-file PATH` hands them to an operator-supplied exact local harness reference (a private, owned file); it never claims a principal-wide target. Session delivery also skips unbound or differently bound messages without acknowledging them. The adapter must match the selected session; this validation also requires `harness.read`. The file contains the vendor thread/session ID, not the Aeon UUID or heartbeat `session.ref`. Managed sessions continue to use agentd drain/complete-delivery. Stopped or archived recipients return 409, and their bound reply obligations close without a fabricated reply. A reply to a session-bound message may omit session bindings. An explicit binding for a different generation is still not found. `aeon tell --reply-to` fills the sender session when `CLAUDE_CODE_SESSION_ID`, or `CODEX_SESSION_ID` (otherwise `CODEX_THREAD_ID`), resolves to exactly one active generation of the caller. A lookup miss leaves the field unset. Ordinary tells do not infer a sender session. In the session panel, unbound history lives under “Other sessions”; exact duplicate posts within 60 seconds share a count, while durable messages and receipts remain separate.

`listen --follow` uses `wait_ms=25000` long-poll requests, with no two-second sleep after a response. Address-based listeners retain their existing project/address payload and use the inbox as a wake hint with a separate cursor. `--follow --deliver` remains resident on leased, foreign-worker, blocked, rerouted or temporarily unavailable adapter work, with exponential backoff capped at 30 seconds (initial delay: `--poll-interval`, default 2s). Resident reads, wake polls and acknowledgements retry transient network failures and HTTP 408/429/5xx responses with the same bounded exponential backoff. Acknowledgements retry in place after handoff, so an acknowledgement outage does not repeat local delivery or printed output during that process. Permanent errors still terminate the listener; one-shot requests do not retry. One-shot delivery retains exit codes 3 (empty), 4 (unavailable), and 5 (another worker). Ctrl-C cancels polling and backoff. Long poll holds at most 30 seconds. A webhook or a notify is a hint. The daemon then fetches authenticated state. The webhook body is ids only and is not authority.

### Operator-installed turn-boundary hooks (AEON-281)

`aeon hook claude <event>` and `aeon hook codex <event>` read hook JSON from stdin for `PostToolUse`, `UserPromptSubmit`, and `Stop`. These are synchronous harness-executed commands: no model-started watcher is needed. Inputs/outputs were checked against installed Claude Code **2.1.284**, Codex CLI **0.158.0**, the [Claude hook reference](https://code.claude.com/docs/en/hooks), and [OpenAI hook reference](https://developers.openai.com/codex/hooks). Codex has lifecycle hooks; its older `notify` command is unnecessary here.

Bind each launched harness to its **Aeon generation**, using exactly one of:

- `AEON_SESSION_ID`: the Aeon harness session UUID returned by registration.
- `AEON_SESSION_FILE`: an owned, regular, non-symlink file containing that UUID and an optional newline.
- `AEON_SESSION_STATE_DIR`: the existing `harness run-heartbeat --state-dir` directory; the hook reads only its `session.id`, never the lease. This lets a hook observe a newly registered generation without changing the environment.

The explicit ID wins over the file, and the explicit file wins over the state directory. An invalid explicit binding fails open with a content-free diagnostic and does not fall through. When none of those is set, the hook posts the harness input `session_id` to `POST /api/inbox/session-binding` and pulls with the returned Aeon generation. The vendor id is not an Aeon UUID and is never used as one. No unique active match is a quiet no-op. `aeon harness register` and `harness run-heartbeat` send `vendor_session_ref` when the harness provides one: `CLAUDE_CODE_SESSION_ID` for `--harness claude`, and `CODEX_SESSION_ID` or else `CODEX_THREAD_ID` for `--harness codex`. The value is recorded only when it differs from the private session ref and the worker lease. A registration whose private ref is already that vendor id matches the same lookup. Do not set one global Aeon session ID for unrelated sessions. Hook credentials need `inbox.read` and `inbox.send`; acknowledgement and this lookup both use `inbox.send`.

Install from the operator's shell with the released binary and the intended instance/configuration:

```sh
aeon --instance ppm hook install --harness claude --scope user --dry-run
aeon --instance ppm hook install --harness claude --scope user
```

Use `--scope project` from the project root to edit the personal `.claude/settings.local.json` (never the shared `.claude/settings.json`); user scope edits `~/.claude/settings.json` (or `CLAUDE_CONFIG_DIR/settings.json`). Review the changes in Claude's `/hooks` and restart the session as required by the harness. The installer prints only its owned hook additions/removals, preserves unrelated settings and hooks, shell-quotes the executable/configuration paths, writes atomically, and is idempotent.

Before replacing existing settings, install and uninstall save the exact previous bytes to a private (0600) timestamped sibling `settings.json.backup-<UTC timestamp>-<unique suffix>` (using the actual settings filename). No-op and dry-run commands create no backup. Unrelated values retain their JSON string spelling where possible, including literal `&`, `<`, and `>`; indentation/key order may change. Invalid JSON, symlinks, and detected concurrent edits are refused. `--dry-run` writes nothing.

Installed commands use an absolute executable path; for Nix installs the installer keeps the matching `aeon` profile symlink found on PATH instead of its resolved store path. Keep that package in the profile so the executable stays rooted and follows profile upgrades. A bare Nix store executable with no matching stable PATH entry is refused; install it in a persistent profile first. Configuration paths are absolute too.

Uninstall removes only Aeon's marked handlers:

```sh
aeon hook uninstall --harness claude --scope user --dry-run
aeon hook uninstall --harness claude --scope user
```

Codex uses the same commands with `--harness codex`; they merge `~/.codex/hooks.json` (`CODEX_HOME/hooks.json` when set), or `.codex/hooks.json` for project scope. Review and trust the new definitions in Codex `/hooks`; project scope also requires a trusted project. The installer does not override managed policy, enable disabled hooks, or bypass hook trust. Its additional-context handlers set `additionalContextLimit: 0` to avoid Codex replacing long messages with previews. Codex may still apply its own size handling to Stop continuation prompts; see the vendor's large-output documentation.

Each invocation pulls `/api/inbox/messages?session=<Aeon UUID>&exact_session=true&wait_ms=0`. That query returns only rows bound to the generation. The hook still injects and acknowledges only rows whose non-null `recipient_session_id` matches its binding; any other row stays untouched. The hook pages past skipped rows within its time budget. Both Claude and Codex inputs with `agent_id` set are quiet no-ops before fetching: subagents must not consume the parent generation's messages. Codex uses the same optional field in its [hook input schema](https://github.com/openai/codex/blob/main/codex-rs/hooks/src/schema.rs). Messages are framed as untrusted data with sender, timestamp and message ID. Bodies are JSON-quoted in full, never shortened by Aeon. A batch contains at most ten messages; later hooks retrieve any remainder. `PostToolUse` and `UserPromptSubmit` emit `hookSpecificOutput.additionalContext`; `Stop` emits `decision: "block"` with a reason only for a nonempty batch. When `stop_hook_active` is true, it returns without fetching, emitting or acknowledging, preventing repeated Stop continuations even if an acknowledgement previously failed.

Only a successful write of the complete JSON output permits `POST /api/inbox/messages/{id}/ack`. That endpoint invokes session confirmation and advances the session delivery/receipt. A broken output pipe never acknowledges. A failed acknowledgement leaves the message pending and may cause replay: delivery is at least once, not exactly once. Emitting is a transport handoff, not proof the model acted. The command returns success on runtime failures, reports only content-free errors to stderr, and has a 2.5-second total budget; installed hook entries also have a three-second harness timeout. No pending messages means no output or Stop block. Hooks run only at boundaries; guaranteed idle/mid-turn injection remains the managed agentd path.

## Harness sessions

A harness session is a public generation of a local worker on one project. `POST /api/projects/{projectId}/harness-sessions` registers it. The body names the agent principal, the harness (`codex`, `claude`, `pi`, `cursor`, or `grok`), the host, managed or unmanaged mode, worker or coordinator role, advertised capabilities, a session ref, and a worker lease. A ticket and a work shape (`ship` or `scout`) are bound together. A run, when set, must belong to that agent and to the named work order.

The same session ref and lease register again without a second row. A different active registration for that ref conflicts. Optional `vendor_session_ref` stores a second digest of the harness-native session id. Leaving it out of a replay does not clear a stored value; a different value conflicts. The raw value is not returned. Worker calls (heartbeat, yield, drain, stop) require the agent, the lease, and a harness worker scope. People can interrupt or stop a session with a harness control scope.

`GET /api/harness-sessions` lists sessions across projects, with state, harness, agent, project, and ticket filters.

### Requests for sessions people start themselves

For an unmanaged Claude Code or Codex session, keep `aeon harness run-heartbeat` running with its existing `--owner-pid`, `--state-dir`, `--project`, `--agent`, and `--harness` flags, plus `--print-controls`. In `/agents`, a person with `harness.control` can ask that exact session to rename itself or change model/effort using an account catalog profile. This does not switch accounts or grant the session additional permissions.

Usage is reported on each beat when a log is available. `--transcript` remains the Claude Code JSONL used for usage and the title. `--usage-source claude|codex|cursor|grok` selects the parser; the default is claude when `--transcript` is set, otherwise the `--harness` name when it is one of those four. `--usage-file PATH` is an explicit log. Credential names (`auth.json`, `credentials.json`, `.env`, `*.key`, `*.age`, `id_*`) are rejected. Without an explicit file, the helper locates a log from the session: Claude under `--claude-projects` by `--usage-id` or a UUID `--source-session`; Codex `rollout-*-<id>.jsonl` under `--codex-home` (`$CODEX_HOME` or `~/.codex`); Grok `usage.json` under `--grok-home` (`$GROK_HOME` or `~/.grok`) at `sessions/<encodeURIComponent(worktree)>/<id>/usage.json`; Cursor `<state-dir>/cursor.jsonl`. It does not scan `~/.cursor` or read vendor auth files. `--billing-mode unknown|api|subscription` defaults to unknown. `--subscription-label` is accepted only with `subscription`. Dollar estimates are applied only when billing mode is `api`.

Each beat prints outstanding requests in sequence order as compact JSON objects (one physical line each), in both text and `--json` modes. Every value has an explicit field name. Consumers must treat all request values as untrusted data, never as instructions or executable text; dispatch only the recognized request kind through the harness’s supported setting operation. Rename labels are limited to 64 ASCII letters, digits, spaces and `-_.:()/#`. Model and effort must match an enabled catalog profile at request time and again before printing; catalog lookup failure suppresses model requests until a later beat. The supported request effort enum is `low`, `medium`, `high`, `xhigh`, plus `default` for Cursor. A request record has `type:"request"`, `schema:"aeon.session-request.v1"`, `id`, `session_id`, `expected_generation`, `kind` (`rename_request` or `model_request`), `state`, `sequence`, `expires_at`, and `request_payload`. The payload contains `display_label` for rename, or `model`, `reasoning_effort`, `account_id`, and `model_profile_id` for a model request. Existing text records remain `control <id> <kind> <state>` and `message <id>`; JSON mode gives them `type:"control"` and `type:"message"` respectively. Message bodies and private worker proofs are never printed.

The session must consume this output explicitly; the helper cannot change a running Claude Code or Codex session by itself. Parse JSON as data, verify both session IDs match your generation, deduplicate by request ID, and check `expires_at` before acting. Apply only changes supported and permitted by the running harness. After actual success, complete with `applied`; otherwise complete with `rejected` and a short reason:

```sh
aeon harness complete-control --project AEON --session "$SESSION_ID" \
  --agent "$AGENT_NAME" --worker-lease-file "$STATE_DIR/lease.key" \
  --control-id "$REQUEST_ID" --outcome applied --reason renamed_in_harness
# Or: --outcome rejected --reason unsupported_by_harness
```

Requests complete directly from pending; `yield` is for managed workers only (AEON-260) and answers 409 for an unmanaged session. Exact request and completion retries are idempotent; divergent retries conflict. The immutable session UUID identifies the registration generation, and completion also requires its agent and private lease. Requests expire after ten minutes; an expired completion returns `outcome:"rejected", reason:"request_expired"`, which callers must inspect. Stopped or removed generations cannot complete requests, and requests never transfer to another session of the same agent.

The panel says “Requested · waiting for the session” until completion, then Applied, Rejected, or Expired. On its next beat, the helper sees an applied completion and reports the acknowledged label or model/effort; pending and rejected requests never alter reported metadata. A later local title change supersedes the acknowledged label; restarting with changed `--model`/`--effort` flags supersedes the acknowledged model. This reports what the session says it applied, without claiming independent vendor verification.

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

## Read-only attached watches (AEON-258)

The paired daemon uses only `POST /api/agent-pairing/attach` for registration,
requests, activation, polls and detachment. At startup it registers a fresh random
poll key using the runtime bearer and computer lifecycle proof. The poll key lives
only in daemon memory; the server retains only its hash in memory. It never enters
pairing.json, a setup store, a database, local replies or logs. Watch operations
require that key plus the snapshot digest; pairing.json alone cannot authorize
them. Registration ends every earlier pending, approved or active watch for that
computer: a daemon restart requires new local consent and owner approval. Server
restart loses poll authority too; restart the daemon to register again. Registration
is serialized with exchanges and cannot transfer an earlier approval to a new key.
The pairing fence permits exactly this additional route.
The nine-digit code identifies a ten-minute request;
owner lookup accepts at most ten attempts per tenant in ten minutes. Codes and
proofs never go in URLs.

The person's **Settings → Personal → Security → Session watching** setting is
stored server-side in `person_watch_security`, scoped to that person and tenant.
`GET/PUT /api/me/security/session-watching` accepts only the signed-in person;
writes require the instance’s origin. The server, never a device request, selects
one of two modes at approval:

- **Approve in Aeon** (`aeon`, default): same-origin, digest-bound person approval
  is the consent gate. The terminal WATCH prompt is a best-effort extra factor;
  a same-user process can emulate its PTY. The approval warns who requested it
  and shows process, cwd and transcript before the Allow action.
- **Also confirm on the Mac** (`local_auth`): after browser approval the daemon
  must also complete LocalAuthentication in its own process. No helper, CLI
  flag, local socket field or environment value can assert this result. Until
  confirmation succeeds there is no session, lease or shared text. Cancel,
  timeout, unavailable authentication, loss of the peer, or revocation fails
  closed. Linux and older daemons cannot approve this mode.

The separate `consent_digest` binds the request ID, snapshot digest and mode,
using the `aeon.attach.consent.v1` domain. The browser echoes it on approval;
a stale review is rejected after a setting change. The daemon validates it,
then echoes it with its authenticated confirmation for strict activation.
Only the memory-key-authenticated exchange can carry that assertion; it is a
trusted-daemon assertion, not remote OS attestation. A replacement daemon that
registers using stolen pairing credentials still needs fresh browser approval,
but the server cannot verify its executable or LocalAuthentication result.
Preventing that same-user replacement requires the separately tracked protected
device identity/installer boundary; this mode does not claim that protection.
Pending requests read the current setting; approved and active requests retain the
pinned mode. Changing settings neither upgrades nor downgrades existing watches.
Mode A retains the original snapshot digest and accepts legacy A approvals.

Native Mac confirmation needs `CGO_ENABLED=1`, Apple's Foundation,
LocalAuthentication and Security frameworks, and an installed executable named
`paimos-agentd` (the pairing installer) or `aeon-agentd` (the Nix package) with
a valid Developer ID signature by the team the build expects, hardened runtime and no
get-task-allow, library-validation or DYLD-environment exceptions. It validates
the running process through `SecCodeCopySelf`/`SecCodeCheckValidity`, checks for
a graphical login, and evaluates a fresh `LAContext` with
[`deviceOwnerAuthentication`](https://developer.apple.com/documentation/localauthentication/lapolicy/deviceownerauthentication).
The OS supplies Touch ID/device-password authentication (and other OS-supported
owner factors); the reason names the harness session PID and host. Contexts
are never reused. The prompt is asynchronous, remains revocable during polling,
and times out after 90 seconds. These checks do not replace installer provenance
or same-user OS isolation. Release darwin `paimos-agentd` is built with
`CGO_ENABLED=1` and links LocalAuthentication. Linux `paimos-agentd`, `aeon-cli`,
and the server image stay `CGO_ENABLED=0`. The Nix `aeon-agentd` package uses
the same split. At watch registration the daemon reports a non-interactive
capability: `available`, `unsupported`, `unsigned`, `no_gui`, or `policy`.
An omitted report is stored as `unreported`. Settings lists that report for
the signed-in person's connected computers and does not offer Mac confirmation
unless one reports `available`. Unsigned, ad-hoc, and headless builds still
fail closed. A signed interactive Touch ID acceptance check remains release
qualification.

### Signed release daemon (AEON-285)

Release darwin `paimos-agentd` is signed with **Developer ID Application:
Markus Barta (P66J39QV6V)**, hardened runtime and a secure timestamp, then
notarized, in the Release workflow's `agentd-darwin` job ("Sign and notarize
paimos-agentd", `scripts/sign-notarize.sh`, vendored from the
`markus-barta/apple-signing` vault). The certificate and notarization
credentials come from that vault into the `release-signing` GitHub
environment, which releases them only to `v*` tag runs; PR and branch CI never
see them. Signing happens before `SHA256SUMS` is computed.

`scripts/build-release-binaries.sh` embeds the expected team through
`-X github.com/inspr-at/paimos/internal/agentd.expectedTeamID=P66J39QV6V`
(`AEON_DEVELOPER_ID_TEAM` overrides it). The daemon compares the team of its own
valid signature with that value. An empty value (development and Nix builds),
an ad-hoc signature or another team reports `unsigned` and refuses Mac
confirmation with an explicit message.

Bare binaries cannot be stapled, so Gatekeeper looks the notarization ticket up
online on first run. To verify a downloaded daemon:

```sh
codesign -dv paimos-agentd                        # Authority=Developer ID Application: Markus Barta (P66J39QV6V), TeamIdentifier=P66J39QV6V, flags=…runtime
codesign --verify --strict --test-requirement="=notarized" paimos-agentd
```

The original pairing owner reviews the immutable host, harness, kernel process
identity, physical cwd, transcript inode, project and ticket snapshot at the
paired origin. Approval binds its digest; activation and each poll recheck the
snapshot, owner delegation, pairing and project/ticket binding. The paired
workspace is the explicit tenant/computer cwd allowlist. Outside paths fail.
A one-minute lease cannot be renewed after expiry; a fresh attach needs fresh
approval. Detached or unreachable means the watch ended, not that the process
exited. Attached sessions are unmanaged and receive no inbox or controls.

`harness.watch` is person-only, project-grantable and excluded from every
built-in role. A workspace owner can explicitly add it to a custom role and
assign that role; this does not itself give the owner conversation access.
The owner consents to the audience of people explicitly granted this permission
in the named project. No earlier turns are uploaded. The SSE watch endpoint
has no replay and keeps only a bounded in-flight delivery per connected viewer;
slow readers disconnect. Every delivery and idle second rechecks the permission
and lease. Conversation bytes never enter events, heartbeat metadata or a
server-side journal. The relay is process-local: multi-server deployments need
sticky routing for registration, watch operations and live delivery (there is
deliberately no durable key store or broker).

The mirror is agent-written, unverified text. Redaction cannot identify every
form of confidential prose: owners must refuse mixed-trust-context sessions.
Same-user hostile code is outside the current isolation boundary; file modes,
local peer checks and owner prompts do not supply OS isolation. Installer
signature verification and OS isolation remain separate rollout work under
AEON-257; this implementation does not claim either.

Run from a separate owner terminal, with the paired daemon running:

```sh
aeon-agentd attach --setup-root /absolute/setup-root --pid 1234 --harness codex \
  --project-id PROJECT_UUID --ticket-id TICKET_UUID --transcript /physical/session.jsonl
```

The local helper reads consent from its controlling terminal, never stdin or a
flag. It must belong to an existing live terminal session, cannot itself be a
session leader, and neither its ancestry nor its session leader's ancestry may
include the target harness. These checks repeat at confirmation and on every
poll, including immediately before upload. Type `WATCH`, then open the paired
instance's Agents page and choose
**Attach session**. Review the code and snapshot, then approve. Keep the terminal
open; Ctrl-C detaches without signalling the harness. Missing helper polls,
identity changes, replaced/truncated transcripts, network errors or revocation
close the watch; reconnection requires a new approval. The kernel executable
must match the enrolled harness path. Arbitrary interpreter wrappers are not
accepted. macOS and Linux have kernel identity adapters; other platforms fail
closed. The helper cannot supply a different server origin or device proof.

The tailer opens each path component without following links and pins an
owner-owned regular inode with one hard link. It starts at activation-time EOF,
bounds reads and records, and never rewinds. Plain lines and recognized
Claude/Codex JSONL text records are supported; unknown structured/tool records
are dropped. JSON escapes are decoded before redacting secret patterns,
camelCase secret names, lowercase secret/token assignments, URL userinfo,
environment assignments and private-key blocks. Lines containing Unicode
nonspacing marks are dropped. Control/format characters are
rejected. The browser displays text only and clears it on disconnect, permission
change, hidden tab or navigation; it never reconnects automatically.

### Local capacity fallback inventory (AEON-298)

`paimos-agentd capacity --setup-root /absolute/pairing-root` (or `--socket
/absolute/agentd.sock`, optionally `--account-id UUID`) reads the authenticated
local daemon's `GET /v1/lifecycle?include_capacity=1` projection. Its opt-in `capacity_accounts`
array enumerates **approved enrollments**, including separate accounts with the
same vendor or display label. It returns account ID, harness, approved display
label/plan when supplied, fallback capability and the last observation time.
It never returns local config paths, account keys, provider IDs or credentials.
Ordinary lifecycle requests retain their existing strict response shape.
This is a capability inventory, not a claim that a login is valid or quota is
available; a missing observation stays missing. Older daemons without the
inventory report unavailable rather than an empty successful discovery.

Idle capture follows fresh harness readings and never interrupts a live managed
run. The default interval is five minutes (`serve --capacity-interval`); failures
never become a zero-percent reading. Existing Codex app-server readings and
Claude in-run stream readings remain the source paths from AEON-297.

| Harness | Private home binding | Idle fallback |
| --- | --- | --- |
| Codex | Registry `home` → `CODEX_HOME` | `account/read` identity check, then `account/rateLimits/read` |
| Claude | Registry `home` → `CLAUDE_CONFIG_DIR` | Not available idle; readings start with a run |
| Grok | Registry `home` → `GROK_HOME` | Not available: billing capability unverified |
| Cursor | Registry `home` → `CURSOR_CONFIG_DIR` | Not available headless |

Homes come from each approved local registry/runtime account, not from directory
crawling or credential extraction. Cursor uses the same explicit home for its
identity probe and ACP child. Legacy Cursor registrations with no home retain
their previous behavior; once homes are configured, unbound account keys fail
closed. Codex idle capture and explicit Cursor homes use a restricted environment
so parent API keys and alternate auth paths cannot select another account.

The installed Grok artifact confirms `x.ai/billing` and the names
`BillingConfigResponse`, `BillingPeriodUsage`, and `BillingCycle`, but does not
establish their field names or quota-neutral initialization. Its fallback is
therefore capability-gated **before any process launch**. The gated transport
permits only `grok agent stdio` → `initialize` → `x.ai/billing`, with bounded
requests and owned-child cleanup. Enabling a production capability requires a
verified executable binding and an identity-checking decoder; there is currently
no production capability or user switch to guess the schema. Tests use a clearly
synthetic response, not invented vendor fields. No real Grok billing probe or
Cursor TUI/cookie extraction is used. Native Grok execution bindings and approval
requirements remain unchanged.

## Outcome events (AEON-286)

Review verdicts, fix rounds, CI results and reverts are outcome events. Record one with `aeon outcome record`. The agent key needs `outcome.write`. Repeat the same `--idempotency-key` and body after a lost response; a different body for that key conflicts. Keys starting with `auto:` are reserved.

```
aeon outcome record --ticket AEON-286 --kind review_verdict \
  --idempotency-key review-aeon-286-r1 --verdict ok \
  --reviewer-model codex --route backend --author-family grok --round 1 --blocking-count 0 \
  --session "$SESSION" --rules-version "$RULES"
```

`--kind` is `review_verdict`, `fix_round`, `ci_result` or `revert`. A review verdict is `--verdict ok` or `--verdict changes`, and may name `--author-family` and `--blocking-count`. A fix round needs `--round`. A CI result needs `--result pass` or `--result fail`, `--repo` and `--pr`, and may name the check with `--name`. A revert needs `--summary`. `--session` is the harness session UUID when the work had one. `--rules-version` may be omitted; it stays empty until a rules version is recorded. Marking a ticket done, accepted or delivered, and publishing a release, are recorded automatically. Completion records the time from the first worker marker, or from the first move to in progress, and the harness session when one is known. A published release records the same session. Do not post those two kinds.
