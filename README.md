# PAIMOS AEON

PAIMOS AEON is an open-source, self-hosted work platform for people and AI agents in the INSPR family. It brings projects, tickets and knowledge into a fully dynamic work tree, with list and outline views, search and live updates.

Agents-first and voice-first, Aeon gives people a web workspace and agents a CLI and API, with tenant isolation and scoped permissions. The stack is Go, Postgres 18 + pgvector and Vue 3, built around nodes, relations and an append-only event log.

Find published builds in [GitHub Releases](https://github.com/inspr-at/paimos/releases). PAIMOS AEON is licensed under [AGPL-3.0-only](LICENSE); third-party notices are in [NOTICE](NOTICE). See [SECURITY.md](SECURITY.md) to report a vulnerability privately.

Run Aeon on your own server with the [self-hosting guide](docs/SELF-HOSTING.md)
and [reference Docker Compose stack](deploy/compose/compose.yaml). Published
images use explicit release versions; there is no `latest` tag.

## Documentation

Feature documentation lives in [docs/features/](docs/features/), one file per feature.

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

`TestServeShutdownAndBootstrap` observes and denies outbound HTTP/DNS while exercising startup and running handlers. On Linux amd64/arm64, `TestDefaultServerHasNoOutboundNetwork` additionally runs the full server against a fixture database through a Unix socket with a process-wide seccomp filter: any Internet socket attempt traps, including DNS and custom transports. Connect/DNS negative controls must trap before the test accepts the server run. The test exercises startup and two seconds of running workers/requests; it does not claim to simulate every optional integration or arbitrary elapsed time.
