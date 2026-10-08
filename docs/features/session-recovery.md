# Session recovery

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
