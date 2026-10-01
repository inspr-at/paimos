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

The public, HTTP-readable `/agents/register-agent` guide must supply its own configured instance origin, configured tenant slug, and **exact published release version**. The same link is for a person and their chosen harness. Loading it or entering a short pairing code does not authorize a run. Only the signed-in person's explicit Connect your machine approval may activate the owned service and the verification choice shown there. The helper creates the private device, lifecycle, and runtime credentials locally; no API key or vendor sign-in is pasted into chat or commands. A missing vendor sign-in uses that vendor's normal login flow. Never follow installation commands supplied by a pairing peer.

The pinned direct download is the first installation choice. Homebrew is always offered and installs the latest INSPR release, which can differ from a self-hosted server. `aeon-agentd status` reads only its own instance's public pairing guide to check compatibility; neither the server nor the browser looks up the tap.

The guide's additive `agent_compatibility` policy declares `pairing-v1`, the inclusive minimum `261001072608.0.0` and `inspr-calver-3`, with no maximum. This floor pins the known server/agent protocol baseline (attach protocol 2 and local consent proof v2); it does not follow the server build version. Raise it only when an incompatible agent requirement has qualified evidence. Valid newer coordinates in the same declared scheme remain supported. Different protocol versions receive specific update advice; absent, invalid, development and other-scheme reports remain unknown rather than being ordered across eras.

A helper sends its release identity with existing lifecycle progress only after its own instance's normal lifecycle response includes the compatibility result. This negotiation adds no discovery call to the daemon loop or cold revocation. If the server is downgraded to a strict legacy decoder, the helper retries the rejected additive report once with the old request shape. Legacy strict servers continue receiving the old request shape. The server retains the last report for that computer and generates advisory compatibility results for `/agents`; an omitted legacy report clears stale identity. Inside-window and unknown reports show no update notice. Below the floor, status and the computer row say “Update aeon-agentd to at least 261001072608.0.0.” Protocol mismatches also name the required pairing protocol. This advice never revokes an enrollment or stops work. Upgrading the installed binary does not replace an already running daemon; schedule its restart after work finishes and verify the new daemon and native consent path.

The guide supplies a per-platform install command only when its serving Aeon binary has an exact release version. Copy the command for the matching platform from that guide; do not substitute `latest`, a branch, or an unverified script. In shell notation the release root is `https://github.com/inspr-at/paimos/releases/download/v$VERSION`, with the guide's exact version embedded in its generated command. The command fetches the selected binary and `SHA256SUMS` there. It requires an absolute `HOME` without ambiguous path components; `HOME`, `.local`, `.local/bin`, `.local/lib`, `.local/lib/aeon`, and the version directory must be real directories owned by the current user and not writable by group or others. It refuses links and unsafe directories before downloading. Missing descendants are created one at a time with a private umask. The versioned destination is exclusively created at `$HOME/.local/lib/aeon/<VERSION>/<platform>-<arch>/`; an existing destination or partial installation is a conflict to inspect, never an overwrite. It selects exactly one checksum entry with the asset's full filename and a 64-digit lowercase SHA256 hash into `selected.SHA256SUMS`, then runs `sha256sum -c selected.SHA256SUMS` on Linux or `shasum -a 256 -c selected.SHA256SUMS` on macOS. Only after that succeeds does it copy the verified bytes to `paimos-agentd` with mode `0700` and link `$HOME/.local/bin/aeon-agentd` to that file. An existing `$HOME/.local/bin/aeon-agentd` that is not a symlink into `$HOME/.local/lib/aeon/` (or whose link text contains `..`) is left untouched and the command stops before downloading. A symlink that already points inside that tree is retargeted at the new verified file. A failed download or checksum leaves no executable `paimos-agentd` and creates no `aeon-agentd` link. On success the command's only stdout line is the pair command with the instance origin shell-quoted. When `~/.local/bin` is on PATH that line is `aeon-agentd pair --url '<origin>'`. Otherwise it is the shell-quoted absolute path of `$HOME/.local/bin/aeon-agentd` with HOME expanded, and stderr has one line telling the operator to add that directory to PATH. The checksum protects the downloaded bytes relative to the manifest; use the official HTTPS release and the version displayed by the trusted instance.

The exact binary names are `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, and `paimos-agentd-linux-amd64`.
These are the targets of the next release workflow. The already published stable86 coordinate lacks `paimos-agentd-linux-arm64`; a guide bound to that coordinate must report Linux arm64 unavailable, never offer a missing asset or change the historical release.

Run the guide’s complete instance-bound command from the intended project folder. The checksum-verified direct download pinned to this server’s version is first. On macOS, Homebrew is always offered and installs the latest INSPR release; its pair line runs `env "$(brew --prefix)/bin/aeon-agentd" pair --url '…'`. Neither the server nor the browser reads the tap formula. The checksum installer selects the instance’s exact release and links `~/.local/bin/aeon-agentd`; add `~/.local/bin` to PATH before running the copied pair command. The installer’s own only stdout line is already executable: `aeon-agentd pair --url '<origin>'` when that directory is on PATH, otherwise the shell-quoted absolute path of the link with HOME expanded, plus a one-line stderr hint to add the directory to PATH. No binary or address substitution is needed. `aeon-agentd status` tells you if this server needs a different version by checking the invoked helper against the server’s declared compatibility window; exact release equality is unnecessary. `--json` exposes `version_status` as `compatible`, `update_required`, `protocol_mismatch`, `unknown`, or `unavailable`. An unreachable guide or one without a usable compatibility policy is `unavailable`. This checks the invoked helper, not the already running daemon.

`pair` runs setup, displays the code and waits for browser approval and daemon connectivity. It offers the current physical folder for explicit confirmation and installed, signed-in harness accounts for selection. Bare `pair` asks for the instance origin on first use; a resumed pairing keeps its saved origin and folder. The guide supplies the default tenant. No credentials are requested or printed. JSON automation must supply `--workspace` and `--harness` explicitly instead of answering prompts.

Start from the intended project folder, not your home folder: the default private state lives beneath your home and must stay outside the working folder. An invalid choice is rejected before confirmation or state creation; use `--workspace /absolute/project/folder` to select the intended folder explicitly. Interactive discovery performs a read-only sign-in check for every supported vendor tool found on PATH before offering choices; explicit `--harness` flags probe only those selections.

`--state-root` is optional for `pair`, `setup`, `status`, `disconnect` and `add-harness`. The defaults are `~/Library/Application Support/aeon/paired` on macOS and `$XDG_STATE_HOME/aeon/paired` on Linux (or `~/.local/state/aeon/paired` when unset). Missing directories, including parents, are created with mode `0700`; existing unsafe permissions, symlinks, repository paths and paths inside the working folder are rejected without repair. No manual `mkdir` is needed. Advanced `--workspace`, repeated `--harness`, `--tenant`/`--tenant-id`, and `--state-root` overrides remain available. To maintain multiple pairings, use a distinct private state root for each.

On an unmanaged installation, `pair` starts the user service only after authenticated approval. `setup` retains its opt-in `--start-service` behavior. On Nix/Home Manager, `pair` leaves service activation to the declarative configuration and waits for that service; Ctrl-C pauses safely and the same command resumes. Explicit `--start-service` still refuses to overwrite managed services.

Homebrew-owned services use `$(brew --prefix)/opt/aeon-agentd/bin/aeon-agentd`, verified against the invoked binary, so the service path survives upgrades, `brew unlink`, and conflicts in Homebrew’s top-level `bin`. Status, disconnect and Claude dependency repin remain available from a versioned binary even if the stable installer link is missing; receipt and declarative ownership checks still protect service changes.

**Upgrade limitation:** `brew upgrade aeon-agentd` changes the installed bytes but neither drains nor restarts an existing daemon; rerunning `pair` also does not restart it. Automatic old-daemon handover is not implemented. The configuration owner must pause new dispatch, finish active work and resolve unconfirmed processes/accounting before restarting the exact owned service. For an imperatively paired service that is confirmed idle, macOS uses `launchctl kickstart -k "gui/$(id -u)/cm.aeon.agentd"`; Linux uses `systemctl --user restart cm.aeon.agentd.service`. These are restart commands, not a safe drain mechanism; never use them to interrupt active work or control a foreign/declarative unit. Existing services created with the former top-level Homebrew `bin` path retain that path; keep its link intact until a reviewed service migration or fresh pairing. Nix/Home Manager upgrades and service changes stay in the owning configuration’s review path.

Before qualifying an upgraded macOS release, verify the new daemon’s executable identity and native Touch ID attach behavior on that daemon: cancel must refuse attachment, a fresh successful Touch ID approval must attach, and stale approval must not transfer from the old process. A matching CLI version, signing check, old-daemon approval or fixture test is not native Touch ID evidence. This fix does not supply that hardware-backed release qualification.

Rerun `pair` to resume setup; use `status` to inspect the pairing, `add-harness` to offer newly available harnesses (or supply `--harness NAME`), and `disconnect --account-id UUID` to remove one enrollment; omit `--account-id` for the whole computer. When an older agentd is earlier on PATH, add a harness with the same binary: `env "$(brew --prefix)/bin/aeon-agentd" add-harness` or `env "$HOME/.nix-profile/bin/aeon-agentd" add-harness`. Add `--state-root ROOT` when using a nondefault pairing. New harnesses still need fresh person approval. `status` prints the reason code and fix command for each harness that is not ready and each blocked account; the computer stays ready while any harness works. For an already connected Codex, Cursor or pi account whose interpreter pin is blocked, `add-harness --harness NAME` renews only that pin, without a new request; Claude pins use `repin --harness claude`. A drain waits for owned work; offline revocation may leave local cleanup or run accounting unconfirmed. These commands never remove vendor login stores or project files.

After revocation, denial or expiry, keep the old state for status and cleanup. Pair again using `pair --url 'INSTANCE_ORIGIN_FROM_GUIDE' --state-root /absolute/new/private-folder`, with a new empty folder outside the working folder and repositories; approve its new code in the browser. Nothing clears, reuses or renews the previous authority automatically. For Nix/Home Manager, the configuration owner must review the service's new state root and finish the old pairing's drain/cleanup before activation.

Codex and Cursor remain **Connect only** for automatic verification (AEON-238, Knowledge MEM-1). Codex's read-only sandbox and `approvalPolicy: never` do not isolate inherited MCP tools or startup hooks; an empty working directory is not an execution boundary. Cursor's isolated config directory and ask mode do not enforce a global no-tools policy, including startup/team hooks and hosted effects. The server and daemon share `internal/agentverification` qualification and refuse these verification runs before account probing, reservations or vendor startup. This is a helper qualification limit, not a claim that future vendor versions cannot support isolation. Never copy vendor credentials, bypass managed policy or enable verification solely because fake protocol fixtures pass.

For a live check after a reviewed helper release qualifies a harness: use the guide's pinned helper, review the exact computer/account and select verification before **Connect your machine**. Inspect helper `status` and the website for one completed verification per selected harness, one at a time, at most 60 seconds each; reconnect/status must not create another run or refill its allowance. If the guide still reports unavailable, stop at Connect only. No live vendor run is part of the fixture suite.

For `--harness claude`, the local bridge also needs Node.js and the **Claude Agent SDK** package, which is separate from the Claude CLI and the ordinary Anthropic API SDK. Install Node.js using its normal vendor or package manager instructions, then install [`@anthropic-ai/claude-agent-sdk`](https://github.com/anthropics/claude-agent-sdk-typescript) in a global npm prefix outside the approved working folder (for example, `npm install -g @anthropic-ai/claude-agent-sdk` in a trusted shell outside a project). The helper checks the existing Node executable and the SDK package's declared module entry using filesystem metadata; it never runs npm, package scripts, or Node module resolution during discovery and never installs anything. If discovery cannot find them, pass `--node-path /absolute/path/to/node --claude-sdk-path /absolute/path/to/@anthropic-ai/claude-agent-sdk/sdk.mjs` to `setup` or `add-harness`, using the actual entry declared in that installed package's `package.json`. Missing dependencies block setup before approval; they do not mean vendor sign-in is required. Saved dependency pins are validated and retained on resume and when adding another harness. A conflicting override is rejected without changing the enrollment.

For npm-launched Codex, Cursor, Claude and pi, setup also records a private per-account Node path and version. The `--node-path` override applies to these launchers and shell wrappers. Setup, probes and runs use the pinned interpreter ahead of the default service PATH, without `NODE_OPTIONS` or `NODE_PATH`. Missing or unsafe interpreters block startup; an old npm enrollment without a pin requires fresh guided setup. Native Grok uses its qualified native executable and needs no Node interpreter. Pi probes serialize per account, and a nonprivate pi profile reports a permissions action. No interpreter paths or versions are uploaded in pairing requests.

The concrete validation matrix is macOS 15 arm64 (`macos-15`, launchd user), macOS 15 amd64 (`macos-15-intel`, launchd user), Ubuntu 24.04 amd64 (`ubuntu-24.04`, systemd user), and Ubuntu 24.04 arm64 (`ubuntu-24.04-arm`, systemd user). The four release assets are cross-built. The isolated fake-executable install/status/drain/remove fixture passed on all four actual runners in [GitHub Actions run 36346781623](https://github.com/inspr-at/paimos/actions/runs/36346781623) for source `3744ba8`. That is implementation qualification for those runner environments, not evidence of actual paid model integration, a final release, or final-source CI. No other macOS release or Linux distribution is claimed by that evidence.

For a Nix or Home Manager managed installation, install `packages.<system>.aeon-agentd` from a reviewed exact Aeon release pin that includes `pair`, then run `env "$HOME/.nix-profile/bin/aeon-agentd" pair --url 'INSTANCE_ORIGIN_FROM_GUIDE'`. A service module alone does not ensure that binary or a package new enough for pairing. Nix-owned harnesses are detected and pinned without changing their binaries. No service file or running process is adopted by pairing.

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

The explicit ID wins over the file, and the explicit file wins over the state directory. An invalid explicit binding fails open with a content-free diagnostic and does not fall through. When none of those is set, the hook reads `session_id` from the hook JSON and resolves a UUID through `~/.aeon/sessions/index/<session-uuid>`. That file is mode 0600, owner-only, and not a symlink. Its text is three lines: the absolute `harness run-heartbeat --state-dir`, the owner pid, and that process's start time. Publish and removal take a lock file in the index directory. The helper writes the entry when it starts with `--source-session` and refuses startup, naming that source UUID, when another live state directory already holds it. It removes the entry on every shutdown path, including owner exit, SIGTERM, and a failed `/stop`, and only when the entry still names its own state directory. The hook uses the directory's `session.id` only while `state.json` is that same live generation and the recorded owner is still that process. A stopped generation, a dead or reused owner pid, a symlink, or any other unreadable index entry is a quiet no-op and does not fall through. A UUID `session_id` with no live index entry is a quiet no-op: no `POST /api/inbox/session-binding` and no stderr. A non-UUID harness session id still posts to that endpoint and pulls with the returned Aeon generation. The vendor id is not an Aeon UUID and is never used as one. No unique active match is a quiet no-op. `aeon harness register` and `harness run-heartbeat` send `vendor_session_ref` when the harness provides one: `CLAUDE_CODE_SESSION_ID` for `--harness claude`, and `CODEX_SESSION_ID` or else `CODEX_THREAD_ID` for `--harness codex`. The value is recorded only when it differs from the private session ref and the worker lease. A registration whose private ref is already that vendor id matches the same lookup. Do not set one global Aeon session ID for unrelated sessions. Hook credentials need `inbox.read` and `inbox.send`; acknowledgement and this lookup both use `inbox.send`.

Install from the operator's shell with the released binary and the intended instance/configuration:

```sh
aeon --instance ppm hook install --harness claude --scope user --dry-run
aeon --instance ppm hook install --harness claude --scope user
```

Use `--scope project` from the project root to edit the personal `.claude/settings.local.json` (never the shared `.claude/settings.json`); user scope edits `~/.claude/settings.json` (or `CLAUDE_CONFIG_DIR/settings.json`). Review the changes in Claude's `/hooks` and restart the session as required by the harness. The installer prints its owned hook additions/removals and the hook's own binding precedence. An explicit `AEON_SESSION_ID`, `AEON_SESSION_FILE`, or `AEON_SESSION_STATE_DIR` wins over the index. A live match prints `bound: <label>`. With none set, the label is the live generation for `CLAUDE_CODE_SESSION_ID` (or the Codex session id), or `not bound: run harness run-heartbeat with --source-session`. An invalid explicit binding prints `not bound: explicit session binding is invalid`; an unavailable one prints `not bound: explicit session binding is unavailable`. Neither falls through to the index. When the explicit generation and the index disagree, the status stays `bound:` to the explicit generation and the next line is `conflict: session index binds a different generation`. It preserves unrelated settings and hooks, shell-quotes the executable/configuration paths, writes atomically, and is idempotent.

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

Usage is reported on each beat when a log is available. `--transcript` remains the Claude Code JSONL used for usage and the title. `--usage-source claude|codex|cursor|grok` selects the parser; the default is claude when `--transcript` is set, otherwise the `--harness` name when it is one of those four. `--usage-file PATH` is an explicit log. Credential names (`auth.json`, `credentials.json`, `.env`, `*.key`, `*.age`, `id_*`) are rejected. Without an explicit file, the helper locates a log from the session: Claude under `--claude-projects` by `--usage-id` or a UUID `--source-session`; Codex `rollout-*-<id>.jsonl` under `--codex-home` (`$CODEX_HOME` or `~/.codex`); Grok `usage.json` under `--grok-home` (`$GROK_HOME` or `~/.grok`) at `sessions/<encodeURIComponent(worktree)>/<id>/usage.json`; Cursor `<state-dir>/cursor.jsonl`. Without a vendor id, Codex discovery reads only the first `session_meta` line of allowed rollouts, matches its `payload.cwd` to the registered worktree exactly, and selects the newest `payload.timestamp` after registration. Grok matches the encoded worktree directory and selects the newest session whose fenced `summary.json` has `created_at` after registration; `usage.json.updatedAt` alone cannot prove that a session is new. Both searches are bounded at 4,000 entries and pin the first match in private heartbeat state, retaining the same log and cursor after a helper restart. Sessions missing creation metadata, a bound worktree, or a recorded start remain undiscovered; use an explicit id or file for them and for resumed sessions. Keep one discoverable new vendor session per registered worktree; pass an id/file when concurrent sessions share it. It does not scan `~/.cursor` or read vendor auth files. `--billing-mode unknown|api|subscription` defaults to unknown. `--subscription-label` is accepted only with `subscription`. Dollar estimates are applied only when billing mode is `api`.

### Coordinator recipe for usage (AEON-503)

Start the heartbeat registration before launching a new vendor session in its worktree, then keep the helper alive until that process exits. The CLI command is `run-heartbeat` (`heartbeat` sends a single manual beat):

```sh
aeon harness run-heartbeat --project AEON --agent "$AGENT_NAME" \
  --harness codex --owner-pid "$OWNER_PID" --state-dir "$STATE_DIR" \
  --worktree "$WORKTREE" --ticket "$TICKET" --work-shape ship \
  --model "$MODEL" --effort "$EFFORT" --usage-source codex \
  --billing-mode subscription --subscription-label 'ChatGPT'
```

For Grok use `--harness grok --usage-source grok` and the appropriate billing label. For Cursor, the launcher must capture its stream-json output from the beginning into `$STATE_DIR/cursor.jsonl`; retain that capture through the final usage flush. Claude uses `--transcript`. When the launcher knows the vendor id or log, prefer `--usage-id` or `--usage-file`; this also supports resumed sessions whose creation precedes registration. The helper persists aggregate reports before posting and retries unacknowledged reports on restart. Only counters and model attribution reach Aeon; session metadata, transcripts and prompts stay local.

`harness register` and `harness run-heartbeat` report `harness_version` from a local `--version` probe bounded to one second and 4 KiB of stdout. Missing binaries, failures, unrecognised output and timeouts leave it empty. `--harness-version` explicitly supplies the launcher-known version and avoids the probe. A restart reuses the registered generation; it does not rewrite its version.

| Source | Input / output | Cached input | Reasoning | Cost |
| --- | --- | --- | --- | --- |
| Claude managed / JSONL | Vendor usage; inclusive input | Cache reads; cache creation stays ordinary input | No separate counter in the managed bridge | Managed vendor USD total when present; unmanaged API pricing only with explicit API billing |
| Codex managed / rollout | Cumulative totals, attributed by model | Vendor cached-input counter | Vendor counter when present | No measured dollar amount; unmanaged API pricing only with explicit API billing |
| Grok unmanaged | `usage.json` session or per-model totals; input includes `inputTokens + cachedReadTokens + cacheCreationTokens` | Cache reads | `reasoningTokens` when present | `costUsdTicks` is ignored; unmanaged API pricing only with explicit API billing |
| Grok managed ACP | Explicit cumulative `inputTokens` / `outputTokens` when present; absent counters stay unknown | `cachedReadTokens` when present | `reasoningTokens` when present | Cumulative USD `cost.amount` when present |
| Pi managed RPC | Completed assistant `message_end.message.usage`; input includes `input + cacheRead + cacheWrite`, summed per provider/model | `cacheRead`; complete cache fields required for inclusive input | No separate counter in this message schema | Reported per-message `usage.cost.total`; a vendor estimate, not proof of a charge |
| Cursor managed ACP / launcher JSONL | Current ACP cost/context updates supply no throughput tokens; a prompt result with complete explicit Cursor usage is accepted. Launcher `result.usage` supplies turn totals | `cacheReadTokens`; `cacheWriteTokens` stays ordinary input | `reasoningTokens` only when explicitly present | Managed USD cost updates when present |

Managed Grok emits cumulative session reports and monotonic deltas into run telemetry; duplicate or stale token totals add no tokens. Pi counts completed assistant messages once by provider/model and message timestamp, ignoring repeated streaming, turn and agent-end records. Tool-supplied usage and separate compaction usage are not included in this Pi path. Grok/Cursor ACP `used` and `size` describe context occupancy and never become throughput tokens. Unavailable counters remain unknown in session reports; zero deltas in run telemetry do not establish a known zero. These parsers do not infer reasoning counts or dollar cost from text or token ratios. Vendor protocol references: [Pi RPC](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md), [Grok headless/ACP](https://docs.x.ai/build/cli/headless-scripting). Grok filesystem metadata and Pi message fields were checked against the installed artifacts on 2026-10-01; fixtures exercise these shapes without vendor authentication.

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
proofs never go in request URLs or query strings. The one exception is the link
the attach helper prints on the person's own terminal, `/agents#attach=<code>`:
a fragment is never sent to a server or a referrer. The router removes it from the
address bar before anything else runs, so it never reaches a sign-in return address
or a report, and keeps it in memory only until the Agents page fills the lookup field
with it (exactly nine digits, else ignored). A session that ended drops it, and opening
the link looks up and approves nothing.

Attach reads and decisions stay with the tenant, person and authentication
generation that started them. Each continuation checks that scope in the same
synchronous turn as applying its answer. Starting sign-in or sign-out also
invalidates other tabs through a random authentication-change marker (no code,
cookie or identity in browser storage). Those tabs drop attach codes, reviews,
permissions and outstanding answers while retaining drafts; sign in explicitly
to resume there, even when the same person signs out and back in elsewhere.
Closing a review leaves its submitted decision bound to that identity: an accepted
response finishes decoding and refreshes both the pending requests and canonical
session lists even after the dialog closes. A changed identity still aborts and
drops the old response; it cannot update the next person's lists or review.

`GET /api/agent-pairing/attach/pending` lets the signed-in computer owner list
their requests that are not a session yet, waiting ones first: every pending or
approved request until it expires, however many newer ones ended after it, then up to four ended in the last fifteen minutes, detached (declined or
cancelled) and unreachable (expired; an unpolled request past its expiry is reported
so without changing the row). Each item is the immutable snapshot and digests lookup returns,
so the owner can review and approve it without typing a code, with every
existing approval check. It never returns the code, the Touch ID challenge or a
session, changes no row and needs no origin header; /agents polls it while visible.
Nothing that waits is ever cut off: a person may have at most 32 requests waiting or
approved at once, and a new request beyond that is refused with HTTP 429, error code
`attach_live_limit`, which the helper prints in plain words. Declining, approving into a
session or letting one expire frees a slot; a retry of an existing request is still
answered. This bound is about what the person has to review, apart from the ten-minute
creation windows per tenant and per computer.

The person's **Settings → Personal → Security → Session watching** setting is
stored server-side in `person_watch_security`, scoped to that person and tenant.
`GET/PUT /api/me/security/session-watching` accepts only the signed-in person;
writes require the instance’s origin. The server, never a device request, selects
one of two modes at approval:

- **Approve in Aeon** (`aeon`): same-origin, digest-bound person approval
  is the consent gate. The terminal WATCH prompt is a best-effort extra factor;
  a same-user process can emulate its PTY. The approval warns who requested it
  and shows process, cwd and transcript before the Allow action. Saving this
  mode opts out of Mac confirmation. A program running as that user can open
  another terminal and request the review.
- **Also confirm on the Mac** (`local_auth`): on an upgraded pairing, browser
  approval issues a random, one-use `local_auth_nonce`. The daemon signs
  with the pairing's P-256 Secure Enclave key. The wire signature is base64
  ASN.1 DER ECDSA over
  `SHA-256("aeon.attach.local-consent.v2\0" + consent_digest + "\0" + nonce + "\0" + reason)`.
  The reason is the canonical Touch ID prompt:
  `Allow watching the conversation <harness> session PID <pid> on <host>`,
  or `Allow status only (no conversation text) for ...` for a metadata-only attach.
  A signature over any other reason is rejected. The server verifies it against
  the immutable public key pinned by browser-approved pairing, then consumes the
  nonce in the session/lease transaction. A bare `local_confirmed: true`, wrong
  key, nonce, reason, stale digest, expired approval or replay is refused. No
  text is accepted before activation. Changing biometric enrollment invalidates
  the key; re-pair to restore Touch ID. The prompt a different local process
  displays is not inside the Secure Enclave signature. Enclave keys use
  biometry access control, which cannot also pin the daemon's designated
  requirement; generic-password items are pinned to that requirement.

Until the person saves a choice, a pending attach on a computer whose
browser-approved pairing pinned a public key uses `local_auth` and requires
that key's signature. The daemon's capability report cannot select Aeon
approval. The snapshot platform must match the platform stored at pairing; a
mismatch is rejected. Linux and pairings without a pinned key stay on Aeon
approval. SSH to a Mac that can sign shows the Touch ID prompt on that Mac's
screen, not in the SSH terminal. Settings shows `local_auth` with
`consent_saved: false` when any connected computer has a browser-pinned key,
including when another computer would still approve in Aeon and when the
upgraded computer reports that Touch ID cannot run. Saving `local_auth`
applies it to every upgraded computer and fails closed where confirmation
cannot run; pairings without a key retain Aeon approval until re-paired.
Saving `aeon` keeps Aeon approval where the snapshot platform matches the
paired platform. Touch ID needs a person at the Mac. Ancestry and session
checks are defence in depth, not a guarantee that same-user code cannot
request its own attach.

The `consent_digest` binds request ID, snapshot digest and mode with the
`aeon.attach.consent.v1` domain. Settings changes affect pending requests;
approved and active watches retain their pin. Existing pairings without a key
use Aeon approval until re-paired, even if they report Touch ID availability.
Settings says “upgrade this computer’s pairing to enable Touch ID”. Migration
1047 ends in-flight watches approved under the old boolean protocol, requiring
fresh consent. Daemon registration cannot install or replace a public key, and
Add harness preserves both the public key and the original local key identity.

AEON-467 rollout: attach transport remains protocol 2; startup registration also
declares `local_consent_proof_version: 2`, and the server advertises that required
version in registration and attach responses. An omitted version means v1.
Protocol-2 registrations with v1 or an unknown version fail closed with HTTP 409
`update_agentd` and an actionable “upgrade paimos-agentd” error, before browser
approval or Touch ID. Protocol-1 registrations still allow ordinary daemon work
while attachment remains disabled. The version is bound to the memory-only poll
key at registration; a later claim cannot upgrade it. V1 signatures never verify.

Roll out the server and updated signed `paimos-agentd` together, then restart
the daemon and request fresh attach approval. Deploying the server first disables
attachment for AEON-460 daemons until that upgrade; ordinary work continues.
Upgrading the daemon first against a server with strict older request decoding
also leaves attachment disabled until the server is upgraded. The updated daemon
requires the server's v2 acknowledgement. Existing pairing capabilities, pinned
public keys and local Enclave key identities remain valid; this proof-format
upgrade does not require re-pairing or key rotation. Pairings without a pinned
key still require their separate pairing upgrade to enable Touch ID.

Release Darwin builds enable `-tags aeon_enclave` with `CGO_ENABLED=1` and link
Apple's Security, Foundation and LocalAuthentication frameworks. The native
boundary validates the running hardened Developer ID daemon, team `P66J39QV6V`,
identifier `paimos-agentd`, and refuses debugging, DYLD environment or disabled
library validation. At pairing it creates a non-exportable P-256 key using
`kSecAttrTokenIDSecureEnclave`, `biometryCurrentSet` and `privateKeyUsage`.
Every signature uses a fresh cancellable `LAContext`, without authentication
reuse or a password fallback. Cancellation, expiry, revocation and changed
process identity fail closed. Unsigned/Nix development builds do not enable
this path. Linux remains CGO-free and uses Aeon approval.

In the same release build, private pairing state (including device, runtime and
lifecycle capabilities) and the runtime bearer live in Keychain generic-password
items in the device-local legacy file Keychain, which is not iCloud-synced.
New items explicitly request `kSecAttrSynchronizable=false` and retain
`kSecAttrAccess` for the signed-daemon ACL. The legacy backend strips
accessibility and synchronization attributes from stored items; it provides no
accessibility-class or device-bound backup guarantee. Their ACL trusts the
validated daemon's designated requirement, with root as ACL owner rather than
the user's UID. Existing items must have the same code signing requirement and
restrictive ACL; permissive pre-created items are
refused. Requirement introspection is weak-linked and fails closed if macOS
cannot provide it. Reads suppress authorization prompts and an ACL denial never
falls back to disk. First access imports existing
`pairing.json` and `runtime.key`, verifies the persisted item, overwrites the
exact private source inode, and unlinks it. Interrupted cleanup resumes safely;
symlinks, hardlinks and conflicting state are refused. Overwriting does not
promise physical erasure of APFS snapshots or backups. Public `runtime.json`
remains on disk; it contains no bearer or private key. Its server address,
computer/tenant/principal identities, approved folder and local key identity
must match the protected pairing before cold-start lifecycle or bearer requests;
changing that disk file cannot redirect credentials.

The server verifies possession of the browser-pinned key; this is not remote
hardware attestation. A new pairing still needs the person to trust the installed
daemon and review the requested computer. Automated tests use an injectable
signer and cover server proof verification, migration and unsigned native denial.
A memory-only SecItem fixture mirrors legacy attribute pruning and checks stored
item readback, including preservation of the supplied ACL identity; it never
accesses a real Keychain and does not qualify the native ACL.
A signed interactive Mac qualification must additionally prove actual enclave
creation, Touch ID success/cancel, changed-biometry invalidation and Keychain ACL
refusal to a separate unsigned process before release. Local unsigned checks do
not supply that hardware or ACL qualification.

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
for the existing signing diagnostics (`AEON_DEVELOPER_ID_TEAM` overrides that
value). The enclave and Keychain boundary additionally requires the fixed team
`P66J39QV6V`, identifier `paimos-agentd` and hardened runtime; changing a build
variable cannot relax it. Development/Nix builds without `aeon_enclave`, ad-hoc
signatures and other teams report `unsigned` and refuse Mac confirmation.

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
include the target harness. These checks are defence in depth: they repeat at
confirmation and on every poll, including immediately before upload, and
same-user code can still open an independent terminal and request the review.
Ancestors are identified from kernel metadata only. On Linux that is
`/proc/<pid>/stat` plus the directory uid, so a root-owned sshd, su or sudo
ancestor stays acceptable; the target still needs its executable and cwd. On a
Mac with an upgraded pairing whose daemon reports that Touch ID can run, the
unsaved default also requires an enclave-signed confirmation before a session
exists. Type `WATCH`, then open the paired
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

## Outcome proposals (AEON-378)

The server's daily deterministic analysis runs for the configured doctrine App tenant. Defaults are a 14-day window, three affected tickets, more than two fix rounds, five open draft reservations and five draft attempts per UTC day. `doctrine.Options.Analysis` bounds these host settings. There is no LLM client or summarizer setting: token use is zero. Runs are claimed durably once per UTC day; an uncertain GitHub write retains its slot and request UUID for the next day's retry. Each tenant/rule has at most one active analysis reservation, including unfinished writes. Analysis never approves, merges, releases, changes a pin or reverts a rule.

Outcomes, exception votes and the current learnings inbox are grouped by the server-recorded merged instruction version at the observation time, harness and ticket kind. Missing provenance is left unattributed. Fixed vocabulary classes (validation, security, scope), high fix rounds, CI failures, reverts and exception votes map to one matching indexed rule. Ambiguous or absent targets become internal notes. The proposed clarification uses the AEON-319 path, creates a GitHub draft with `aeon-proposal`, and passes the same main-file and private-quotation guards. Private-text refusals become internal notes; an unavailable private guard retains its reservation for the next daily retry. Private evidence text and workspace URLs never enter the public PR; its fixed explanation includes the recurrence count, baseline and intended direction. Ticket/event references remain in Settings → Agent rules → Proposals, available only to people with workspace `rules.read`, `outcome.read` and `knowledge.read`.

After an observed merge, comparison requires a later rules version with instruction provenance matching the complete changed file hash, in the same harness and ticket kind, and at least three eligible observations. A repository pin alone never proves use. If the harness does not report those bytes, the comparison stays pending. Baseline context includes review rounds, fix rounds, CI failures, reverts, exception votes and time to done. Rates include successful observations; review and fix rounds use each ticket's highest recorded round. Exception and revert rates describe observed tickets, not silent/unobserved work. A completed comparison adds one System-authored ticket comment with sample sizes and the delta; it is descriptive, not causal. Findings persist references, aggregate numbers and hashes, never proposed rule prose. Scans are bounded at 5,000 samples and 500 projects and refuse a truncated learnings page rather than propose from an incomplete scan. Tests inject a fake forge and must never contact real GitHub.
