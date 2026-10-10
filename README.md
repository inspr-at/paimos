# PAIMOS AEON

PAIMOS AEON is the [What of INSPR](docs/features/web-workspace.md#product-role-and-release-evidence): planning work for people and AI agents, plus the agent-platform engine. It is open-source and self-hosted.

Aeon is agents-first. Voice and Aithema requirements intake are [planned (AEON-822)](docs/features/web-workspace.md#planned-work); [shipped capabilities and their release evidence](docs/features/web-workspace.md#shipped-capabilities) live in the feature docs.

Find published builds in [GitHub Releases](https://github.com/inspr-at/paimos/releases). PAIMOS AEON is licensed under [AGPL-3.0-only](LICENSE); third-party notices are in [NOTICE](NOTICE). See [SECURITY.md](SECURITY.md) to report a vulnerability privately.

Run Aeon on your own server with the [self-hosting guide](docs/SELF-HOSTING.md)
and [reference Docker Compose stack](deploy/compose/compose.yaml). Published
images use explicit release versions; there is no `latest` tag.

## Documentation

Feature documentation lives in [docs/features/](docs/features/), one file per feature.

## Database pool capacity (AEON-995)

`AEON_DATABASE_URL` defaults to **16 query connections**, independent of host
CPU count. Its existing `pool_max_conns` URL or keyword setting overrides this;
the minimum is 3. Two query slots are reserved for requests and health probes,
so background loops collectively acquire at most `pool_max_conns - 2` slots.
The shared admission limit follows worker contexts into per-tenant goroutines
and callbacks, and releases with rows/transactions rather than loop timers.
Advisory-lock workers reuse their retained session for sequential tenant
transactions; tenant settings, authorization and replica locks still apply.

Size Postgres `max_connections` for the sum of all serving processes' query
pool maxima, **plus dedicated LISTEN sessions**, operator/migration connections
and other database clients. SSE, inbox long polls and configured phone-push
listeners already use connections outside the query pool. Increasing CPU count
no longer silently changes the application's connection budget. Startup logs
record the effective pool maximum, minimum, reserve and background limit.

`/api/ready` and `/api/health` acquire within 100ms and ping within an overall
500ms request budget. Readiness remains fail-closed: foreground saturation
returns 503 with `reason: pool_exhausted` and acquired/waiting counts; a failed
connection or ping returns `database_unavailable`. Liveness still returns 200
with `db: down` when its database probe fails. Readiness includes content-free
pool counters and the p95 of the latest 256 completed acquisitions (including
failed acquisitions and background admission waits). A warning logs those
statistics when acquisition waits remain present for more than five seconds.

The complete `serve.go` worker inventory is below. Counts are simultaneous
query-pool slots per loop; the shared `pool_max_conns - 2` limit also applies
across every row. Tenant scans close their rows before tenant work starts.
Advisory-lock lanes retain their one session and reuse it for sequential
per-tenant work, with no nested pool acquire.

| Worker entrypoint | Maximum query slots | Retained session / fan-out |
| --- | ---: | --- |
| `aithemaHost.Run` | 1 | Sequential callbacks; enabled with signing keys |
| `attachedMessages.Run` | 1 | Sequential tenant sweep |
| `runRoutineDispatchers` | 1 per tenant | Startup scan closes before tenant goroutines start |
| `embedding.Worker.Run` | 1 | Sequential claim, provider call, finish |
| `parentBenefits.Run` | 1 | Sequential claim, provider call, finish |
| `runConfirmationJobs` | C + 1 | C render jobs plus scanner; C = `AEON_PDF_CONCURRENCY` (1–4) |
| `portalMod.RunLimitSweep` | 1 | One expiry transaction |
| `inbox.Worker.Run` | 1 | Sequential webhook claim, send, finish |
| `harness.RunLostContactSweeper` | 1 | Sequential tenant passes and deadline reads |
| `nodes.RunWorkLifecycle` | 1 | Sequential bounded tenant pages |
| `modelMod.Run` | 1 | Sequential catalog refresh |
| `inbox.Sweeper.Run` | 1 | Advisory-lock session reused for scan and tenant transactions |
| `knowledge.Tagger.Run` | 1 | Advisory-lock session reused for scan and tenant transactions |
| `statusAuto.Run` | 1 | Sequential tenant passes |
| `recurringWork.Run` | 1 | Sequential tenant passes |
| `phoneMod.Run` | 1 | Plus one dedicated LISTEN connection outside the pool |
| `questionsMod.Run` | 1 | Sequential tenant dispatch |
| `doctrineMod.EnsurePrivateGuards` | 1 | One-time sequential guard rebuild |
| `doctrineMod.RunOutcomeAnalysis` | 1 | Sequential inbox and daily analysis |
| `reviewMod.RunStatusReporter` | 2 | One retained session per independent reporter/refresh lane |
| `deliveryMod.Run` | 1 | Reconciliation and observation locks share one session, including fan-out |
| `deliveryMod.RunAlerts` | 1 | Per-tenant advisory session reused by alert transactions |
| `deliveryMod.RunAudit` | 1 | Advisory session reused by audit transactions |
| `db.WatchPool` | 0 | In-memory counters only |
| HTTP Serve goroutine | 0 | Request handlers acquire separately as foreground work |

Admission, work queue, review/shipping projections, usage dashboards and chat
are mounted request modules; they add no process-wide worker loop in
`serve.go`. Event and inbox stream listeners are request-owned dedicated
sessions outside the query pool. Direct pool acquisition under a retained
session context is rejected and counted as `nested_acquires`, so an accidental
regression fails without consuming another slot.

## Server outbound calls (AEON-493)

With default optional configuration and no opted-in tenant integrations, the active external-service call list is **empty**. Startup, scheduled default workers, health and installation-guide rendering do not contact GitHub, the tap, an update feed or a telemetry service. Postgres is the required operator-configured database dependency (`AEON_DATABASE_URL`), not an external-service integration; use a local socket or local address when the installation must have no network dependency. DNS resolution for the optional destinations below occurs only when their activation requires it.

This is the canonical inventory of server egress; there is no global switch that silently disables a configured integration. Disabling an integration means removing its activation, and an existing database may retain previously opted-in targets.

| Call and destination | Default | Activation / switch |
| --- | --- | --- |
| OIDC discovery, signing keys and token exchange at the configured identity provider | No issuer/client configured | `AEON_OIDC_ISSUER` and `AEON_OIDC_CLIENT_ID`; requests follow sign-in/token verification. Clear the issuer/client to disable. |
| Zitadel user lookup, creation and invitations | No provisioner | `AEON_IDENTITY_PROVISIONER=zitadel` plus `AEON_ZITADEL_URL`, tenant/org and token-file configuration; authorized invite operations. `none` or unset disables it. |
| Workspace model chat and embedding requests to the configured OpenAI-compatible endpoint | Off by default; lexical search only | A person with `settings.manage` enables the workspace provider and selects each feature. Disabling the provider or deselecting a feature stops its model calls. **Test connection** is the only model call while the provider is disabled; it requires an explicit person request and sends a synthetic prompt. Saving never contacts the model. |
| Inbox webhook delivery, including target DNS checks | No registered webhook receivers | An authorized receiver registers its webhook target; unregister/replace it to disable. Delivery workers run by default but have no external destination until configured. |
| Messaging routine webhook wakes | No registered routine webhook targets; messaging off in production without a key file | `AEON_MESSAGING_KEY_FILE` enables messaging (development uses an ephemeral key); an authorized routine receiver selects a webhook target. Remove that target to disable wakes. |
| GitHub doctrine metadata/tree/blob reads | No registered remote sources or requested sync | Authorized doctrine source registration and explicit sync/proposal operations select the repository and immutable pin. Private reads additionally require `AEON_DOCTRINE_CREDENTIALS_DIR` and a tenant/repository allowlist. Remove the source to disable future reads. |
| GitHub doctrine proposal token, branch/content and PR operations | App unconfigured | Complete `AEON_DOCTRINE_APP_*`, installation, tenant, gate-login and DCO configuration plus an authorized proposal/gate action. Remove the App configuration to disable. |
| GitHub cross-review token/status publication | App unconfigured | Complete `AEON_REVIEW_APP_ID`, `AEON_REVIEW_INSTALLATION_ID`, `AEON_REVIEW_APP_KEY_FILE`, tenant and repository configuration plus recorded review work. Remove the App configuration to disable. |
| OpenRouter public model catalog | No OpenRouter account/model selection | Authorized model changes on an enrolled `pi` account with provider `openrouter` perform an advisory catalog lookup. Changing/removing the provider account stops these lookups; no scheduled catalog polling. |
| Aithema service preview documents and host callbacks | Tenant service unconfigured | Authorized tenant host settings select `service_url` and qualified service evidence; remove those settings to disable. `AEON_AITHEMA_OPERATOR_LOCAL_SERVICES` only permits exact operator-local destinations and does not activate a service. |
| CRM provider search/fetch | Compiled server uses no provider adapters | A custom host must explicitly wire `PluginWithProviders` / `NewWithProviders`, grant integration access and configure a provider binding/secret reference. Remove that binding/adapter to disable. |

Separate processes have their own explicit destinations: `aeon-agentd` contacts its paired instance and selected model providers; user-run checksum installers and Homebrew fetch release/package assets; build/release/history tooling contacts the forge, registries and configured historical import sources. Those are not server startup workers. Classic migration readers remain historical CLI-only paths; they do not run in the server and Classic Paimos stays retired.

`TestServeShutdownAndBootstrap` observes and denies outbound HTTP/DNS while exercising startup and running handlers. On Linux amd64/arm64, `TestDefaultServerHasNoOutboundNetwork` additionally runs the full server against a fixture database through a Unix socket with a process-wide seccomp filter: any Internet socket attempt traps, including DNS and custom transports. Connect/DNS negative controls must trap before the test accepts the server run. The DNS control dials a numeric nameserver when host lookup returns without a socket, so a files-only nsswitch still has to trap. The filter is installed with `no_new_privs` and `seccomp(2)` on one pinned OS thread, because the flag is per thread; `TestDenyFilterSurvivesGoroutineMigration` forces a scheduler hop between the two calls and requires an unpinned control to fail with EACCES. The test exercises startup and two seconds of running workers/requests; it does not claim to simulate every optional integration or arbitrary elapsed time.
