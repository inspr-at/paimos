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

AEON-417 B adds an external **shadow authority** in `scripts/ci-authority` and a
disposable Linux guest init in `scripts/ci-executor`. Install reviewed binaries
outside all candidate workspaces; do not run this controller from a PR checkout.
The authority authenticates the original webhook body with HMAC-SHA256, reads
the numeric repository identity and current main/PR/queue state from GitHub,
and reconstructs the complete plan from its dedicated bare mirror. PR plans
bind the source head and the API-resolved merge commit separately. Queue plans
enumerate every constituent through Git parents and recheck the active queue
ref. Checks are revalidated after reconciliation. Its output includes each
existing required context, every extra inventory context, and `ci/trusted`, all
with **pending** status. There is no check writer or activation flag.

`ci-authority shadow --config /absolute/controller/config.json --event
pull_request --delivery <delivery-id> --signature <X-Hub-Signature-256> --webhook
/absolute/controller/event.json --generation <positive-generation>` reads only
Git objects and GitHub API state. Configuration has schema
`aeon.ci.authority-config.v1`, `mirror`, a `git` object with absolute `path` and
raw SHA-256 `digest`, and `authority`
containing `repository_id`, `repository`, the reviewed `policy` pin,
`environment_digest`, and `verifier_app_id` (zero until provisioned). Supply
`AEON_CI_WEBHOOK_SECRET` and a read-only `AEON_CI_READ_TOKEN` to the controller
process through approved credential storage; neither is forwarded to execution
or output. The installed authority requires a separate Linux host and verifies
root ownership, non-writable parents and Git bytes before opening its mirror.
Mirror refresh is a separate trusted ingress responsibility. The
current replay/generation guard lasts for one controller process; production
needs durable serialized ingress before any authority activation.

`observe` additionally needs a `profile`, `admission_public_key`, signed
`--admission`, `--obligation`, `--run-id`, and `--job-id`. The profile fixes each
obligation's absolute `/opt/aeon/` command, stage, reporter and expected manifest,
plus raw SHA-256 pins for QEMU, firmware, kernel, initrd and a read-only ext4 root
image. It also binds harness/toolchain/environment digests, security epoch,
separate QEMU UID/GID, CPU/memory limits and timeout. The supervisor deep-copies
these settings and requires `infrastructure: hosted-disposable`; the provider
must independently attest that placement. This backend supports Linux/amd64
only and never routes candidates to the trusted main pool or production hosts.
Each metadata/build/test stage boots a new Linux/KVM VM with
read-only framed task/source disks, no network, no host mount and no monitor or
control socket. The root guest init uses a read-only, `nosuid` executor image and
an unprivileged candidate process in private tmpfs storage with a fixed offline
environment. Source export reads every verified Git blob directly, ignores
candidate export attributes, and refuses symlinks/submodules and unsafe paths.
Only bounded content-addressed opaque artifacts can cross the result interface.
Candidate stdout is never interpreted as a host receipt or GitHub check.

Admission is independently Ed25519-signed over the plan, exact candidate commit,
complete tree-manifest digest, policy, executor, approved harness, environment,
epoch, review target/record and a maximum 24-hour validity window. This admits
the complete executable closure, including package initializers, JS helpers,
dependency/config and lifecycle code. It cannot establish honest assertions in
unreviewed code. Supervisor observations have an in-process private seal;
serializing one loses that provenance. Trusted ingress can revoke an admission;
expiry and revocation are rechecked after execution and during reconciliation.
Durable signatures/revocation, source
run/job verification, full receipts and reuse remain E's responsibility. Partial
reruns are refused. The browser/application VM connection and approved browser
reporter remain D/H work and currently fail closed.

This is an unactivated implementation: independently reviewed VM images,
Linux/KVM hosted provisioning and a real boot/tamper probe remain required.
The tests cover protocol, admission and supervisor ownership, including attack
classes from all five AEON-421 reviews; they do not certify a live VM boundary.
App provisioning, expected-App per-context ruleset probes, durable ingress and
review revocation are later coordinator/OPS steps. No workflow, runner route,
required check, version or execution selection changes here. Keep full existing
CI and both optimization switches off until those prerequisites are proven.
Build the guest init with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`; the approved
kernel needs built-in devtmpfs, virtio block/PCI and ext4 support. The pinned
rootfs needs `/workspace`, `/tmp`, `/proc`, `/dev` mountpoints and all approved
tools/dependencies under `/opt/aeon`. No image is produced or provisioned by
this worker, and missing images, recipes or admission refuse execution.

The Decision Desk UI (AEON-567) lives at `/decision-desk`. Open and answered
questions are paged separately; refresh retains up to ten loaded pages per state
(1,000 questions), with remaining results stated explicitly. Each memo freezes its
source for the round. New arrivals wait for the next round, including rounds opened
from Decided. Expired approvals appear in history. Approvals and tier requests
require an explicit choice; Enter on another focused control performs that control's
action. In a field, Enter finishes editing and the platform modifier plus Enter
submits. Access loss, expiry and changed sources block the write and retain drafts.

Tier requests are read only for sessions advertising AEON-436's `service_tier_v1`.
AEON-455 server availability selects request-bound phone verification on every
screen size; a server without that package keeps the existing approvals API.
Held replies address the original principal UUID and, when supplied, its exact
session. Related ticket records use permission-checked node relations; specialised
Always/doctrine publishing and its context projection remain upstream package
integration work. The unavailable stamps explain their restrictions in the memo.

Decision Desk question groundwork (AEON-562): `aeon ask --project KEY
--option '["Title","Description","Answer"]' "Question"` stores a bounded,
project-scoped question and returns immediately. Add `--ticket KEY`,
`--context-file PATH`, `--recommend 1 --why "Reason"`, `--meanwhile parked`,
`--meanwhile-text "Other work"`, `--keep once` or `--anyway "New evidence"`
as needed. Retain the printed `--request-id UUID` and exact input for retries;
`aeon ask status UUID` reads the durable question after the original session ends.
`--session UUID` is a verified public harness generation, separate from the
CLI's global attribution `--session-id`. Named project/ticket lookup uses the
existing node read permissions; UUID addressing needs only the question scopes.
The MCP `ask` tool accepts `{project, ticket?, input}` with the same typed HTTP
input and mandatory request ID; `ask_status` accepts `{question_id}`.

The additive API is documented in `api/openapi.yaml`: project question create/list,
question get/status/person decision and a permission-filtered `/decision-desk`
question projection. Answered lists accept `state=answered&order=desc` (AEON-611):
newest current answer first, with question ID as the tie-breaker. Follow the
bounded `next_cursor` using `cursor`, with the same tenant, principal and project;
permissions are checked again per page. Descending reads require offset zero.
The Decided view follows these cursors; “Load 100 more” resumes the saved cursor
with one page request. Refresh rechecks its loaded pages from the newest decision,
so a fresh answer stays available for correction. Continuation keeps questions
before protected requests, hides action requests projected into questions, and
preserves decisions recorded in the desk while that page is loading. Decisions
committed during pagination may require a refresh from the first page. Default and
Open reads keep oldest-first creation order. `questions.ask` and `questions.read`
are explicit agent key scopes; `questions.decide` is person-only. Owner/admin/member roles receive all
three, viewer receives read, guest/customer receive none. Existing keys gain no
new scopes. Agents read only questions they asked, with only their memberships.
Generic node/knowledge CRUD cannot modify question/decision authority.

Questions and immutable answer revisions use protected nodes plus tenant/project
projections. Each asker retains input, principal, exact original session, source
request, reply-root UUID and comment destination (ticket, or question node).
The dispatcher materializes each reserved reply root as a held counterpart for
its answering person. The original request is never released or executed.
Person answers use a database-clock ten-second grace window; edits restart it.
A bounded durable dispatcher serializes with edits and commits each asker's inbox
and comment effects independently. Revisions dispatched before a change remain
in the log; the new typed correction names its `replaces` answer ID. The inbox
outbox state `delivered` means dispatched, while `receipt_state` reports `queued`,
`handed_off`, or `failed`. CLI status distinguishes dispatch from receiver proof.

A signed-in addressed person with inbox management and question permissions can
use `tell --reply-to UUID --session-cookie-file PATH` on a held request. The
file contains only the `aeon_session` cookie value; the command uses the selected
instance URL (or `AEON_URL` / `PAIMOS_URL`), sends that cookie without an
Authorization header, and ignores ambient agent keys and sender sessions.
This records the answer/outbox and settles the request atomically, returning
`status: pending`, a question ID, revision and deadline. Other callers retain the hidden-parent refusal. Resolve/dismiss and
permission approvals keep their existing immediate semantics. Desk dispatch and
ordinary replies reserve the obligation, message, delivery and receipt rows
before appending events. Migration 1117 permits event references to be filled
in within that transaction and rejects incomplete reservations at commit.

The additive `harness-session/2.7` response contract includes optional `desk_answers`
references; registration and heartbeat request requirements remain unchanged.
Ended generations retain bounded durable answer references. Only a verified
same-project/principal continuation receives the latest answer; an absent
successor stays visibly `successor_pending`. Continuation briefs carry references
whose private text is resolved through authorized `ask status`, preventing
harness metadata readers from gaining question content. No old process is woken.
Outcome effects (AEON-565) apply after the finalized grace revision. Once keeps
an immutable Decided record. Always publishes a protected Knowledge entry of type
`decision`, using the existing knowledge list/filter/resolve/graph surfaces and
CLI taxonomy. A correction immediately withdraws and archives the earlier Always
answer, independently of grace or failure of its replacement; lineage remains.
Withdrawing an active Decision requires `knowledge.write` on its project,
including when correcting to Once.
Requirement appends one criterion with its exact answer marker, preserves the
other ticket fields, and compares the captured ticket revision before writing.
Concurrent edits leave a visible `ticket_revision_conflict`; decide again after
reviewing the ticket. Requirement is unavailable for list or structured criteria.
Corrections replace only the exact tracked criterion; edited or removed criteria
remain untouched and appear in `effect_data.review_required` on the new effect.
Missing, moved or otherwise non-editable prior tickets also require review;
their criteria stay untouched while the rest of the correction applies.
Carried reviews retain each distinct kind and reference. Refreshing a review's
reason on a later correction preserves the reason on superseded effect revisions.
Doctrine requires `doctrine: {source_id, path, rule_key, rule_sha256, tldr_en?,
tldr_de?}` on the question or person decision. It creates only a pending AEON-444
inbox draft; the existing person-only submit/dismiss and AEON-319 publication gates
remain authoritative. Dismissed and expired drafts are already retired. Published
or person-edited proposals remain untouched and appear in `review_required`; the
rest of the answer can apply, and the existing Doctrine path governs any manual
correction. This review provenance carries across later answers; a Doctrine
correction requiring that review creates no new draft; a person must handle
the correction through the existing Doctrine path.
CLI status reads the current proposal lifecycle, including dismissal and promotion.
No rule is described as changed merely by proposing.
Status exposes `outcomes` with availability and a why for disabled stamps, plus
per-revision `effect_data` and safe failure explanations. Availability reads load
permissions once and report a cheap Doctrine `mapping_present` hint; exact mapping
and quotation checks run when deciding and applying. Expensive inbox preparation
runs before the tenant mutation fence, then rechecks authority and cache versions
inside the write. Transient failures retry with current permissions, including
`stale_source` preparation races and an expired `public_main_unavailable` cache.
Each transient failure schedules its next attempt 30 seconds later. A stale
public-main cache keeps retrying until it is refreshed or the answer is replaced.
Permanent conflicts expose `retryable: false` and require a reviewed new decision; failed
outcome writes never appear applied. Encoded Doctrine inputs exceeding the
4096-byte persistence bound return 422 and are stored only for Doctrine.
`source_handover_id` remains unavailable pending its verified ask-source adapter.
