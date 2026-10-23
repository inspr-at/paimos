<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Assistant adapters

Assistants use the existing principal inbox. Every adapter reports a
`delivery_mode`: `push` for a continuously reachable vendor endpoint, or `pull`
for an assistant that fetches its inbox during a run. Grok is the first vendor;
`HostedAssistantAdapter` supplies the vendor payload while the inbox owns
encrypted configuration, public HTTPS delivery, signatures, fenced claims,
retry backoff, idempotency and delivery receipts. No vendor process is started.

The tenant registry is exposed within each project:

- `GET /api/projects/{projectId}/assistant-adapters` lists up to 100 entries
  using `limit` and an `after` target-ID cursor; `next_after` is null at the end.
- `POST` to that collection binds an existing active agent principal to a
  `grok` binding. Supply `principal_id` and a `grok_bot:…` address.
  With no endpoint credentials, Grok defaults to `delivery_mode: "pull"`.
  For push, set `delivery_mode: "push"`, `endpoint` and the receiver's existing
  `webhook_secret`. Legacy requests that supply endpoint credentials without a
  mode keep their push behaviour. Pull rejects endpoint credentials. An Aeon
  API key is rejected as a webhook credential. A replacement creates a new
  immutable target version.
- `GET /api/projects/{projectId}/assistant-adapters/{adapterId}` returns the
  redacted configuration and health for its delivery mode.
- `POST /api/projects/{projectId}/assistant-adapters/{adapterId}/test-send`
  defaults to `dry_run: true`. It validates the current delivery binding
  without queueing work or making an outbound HTTP request. A live test requires
  `dry_run: false`, `body` (up to 4096 characters), and `idempotency_key`.
  The response identifies the queued inbox message and its existing receipt.

All registry routes require the existing `inbox.manage` permission in the
named project. Registry creation and test sends additionally require a person
session; live tests also recheck `inbox.send`. Authorization is checked again
under the access-change fence in the final write. A test cannot silently follow
a replacement target. Tenant RLS and project visibility apply to the registry.
Existing message-target routes retain their existing permissions and behaviour.

URLs and webhook credentials stay in the existing AES-GCM encrypted target,
bound to its tenant, project and immutable ID. The registry, events and health
responses contain neither. Push health retains the last success and last failure
independently; compare their timestamps to determine which happened most
recently. A dry-run does not establish delivery health. Health and delivery
results commit together, so a failed completion cannot record a success.

List results include disabled historical versions. `enabled` identifies the
binding used for new messages; each push version's health describes attempts
made against that version's endpoint. A replacement starts with no delivery history.

## Pull delivery and existing assistants

A routine host may be suspended between runs. Pull mode makes no outbound
request, acquires no outbound lease and consumes no retry attempts. The durable
inbox retains messages while the host sleeps. A routine uses its own existing
scoped key with `GET /api/inbox/messages?wait_ms=0`, then acknowledges individual
messages through `POST /api/inbox/messages/{messageId}/ack`. Fetching is
replayable until acknowledgement; only acknowledgement moves the sender's
receipt from `queued` to `handed_off`. The existing project message-listen API
also records polling for configured pull adapters.
Direct messages addressed by principal UUID follow the same pull mode without
requiring a project-target address.

Pull health uses the database/server clock: `last_pull_at` records a successful
poll, including an empty one. `oldest_unread_at` and
`oldest_unread_age_seconds` describe unacknowledged, unexpired messages in the
principal inbox. A stale timestamp (for example, no pull for three hours) is a
polling alarm; it is never presented as a failed push. Pull health does not set
push success/failure fields. These fields do not imply that work was completed.

Existing owner-bound `messaging_only` principals, including assistants created
with `aeon agents assistant create`, are implicit pull adapters. Their owner
can inspect them through the project registry without registering a target.
The adapter ID and address are the existing principal UUID, the vendor is
`generic`, and `implicit` is true. `project_id` is the authorized inspection
context; this projection grants no project access. Other owners' implicit
entries are not returned. IDs, keys, scopes and `aeon agents assistant rotate`
remain unchanged. Messaging-only principals stay pull even if a legacy webhook
target exists; old generic wake registrations cannot cause outbound retries.

Pull messages have no automatic online-delivery deadline. An explicit sender
`expires_at` still applies. Existing queued receipts with an old automatic
deadline remain available until acknowledgement or explicit expiry. Host
suspension therefore does not turn a durable inbox into a `no_listener` failure.
Before first pickup the sender sees `status: "sent"` with
`waiting_for_pickup: true`; polling changes this to `delivered`, and the existing
acknowledgement changes the receipt to `handed_off`.

Unread and unacknowledged pull messages have a separate seven-day retention
limit (168 elapsed hours) from acceptance, shortened by an explicit sender
expiry. This does not change the tenant's online delivery deadline or push/session behaviour. At the
retention limit the receipt fails with `pickup_expired`, and one System notice
tells the sender **not picked up within 7 days**. Failed messages cannot later
be pulled or acknowledged. Disabling the assistant principal or its configured
address fails queued messages on the next sweep (normally within 15 seconds)
with `assistant_disabled`; revoking or expiring all usable inbox keys uses
`assistant_revoked`. A rotated key with a usable replacement remains healthy.
Replacing a configured target retains queued messages' original pull binding.

## Webhook protocol and receipts

For push mode, the Grok JSON payload and its existing Bearer authentication header are
preserved. Messages retain their untrusted-data security notice. The shared
dispatcher additionally sends:

- `Idempotency-Key`: the durable delivery ID, unchanged across retries.
- `X-Aeon-Webhook-Timestamp`: Unix seconds for this attempt.
- `X-Aeon-Webhook-Signature`: `v1=` followed by the lowercase hexadecimal
  HMAC-SHA256 of `timestamp + "." + delivery_id + "." + exact_request_body`,
  using the existing webhook credential as the HMAC key.

Receivers verify the signature in constant time, reject timestamps outside a
short replay window (for example five minutes), and durably deduplicate the
delivery ID before responding. A 2xx response confirms handoff, advances the
inbox cursor and records the sender's `handed_off` receipt. It does not assert
that the assistant has finished the work. A transport failure or retryable HTTP
response keeps the receipt queued. Retries back off by 2, 4, 8, 16, 32, then
64 seconds, with at most eight claims. Non-retryable 4xx responses (except 408,
425 and 429) and exhausted attempts fail the receipt. The existing inbox
deadline/sweeper policy still applies. Redirects, proxies, private addresses and
DNS rebinding to private addresses are rejected; network work is time bounded
and remote response bodies are discarded within a byte limit.

Inbound messages and replies authenticate with the assistant's own scoped Aeon
key through the existing API/MCP. Ordinary project-bound agent keys can use
the project messages API, including `reply_to`; messaging-only identities use
the existing principal inbox API and its narrowly provisioned scopes. Webhook
credentials and Aeon API keys are never inserted into outbound message data.
Adapter registration creates no keys, grants, assistant class or policy.

## Upgrade and scope

Migration `1435_assistant_adapters.sql` backfills all existing
`grok_bot_routine` targets as `grok` registry entries, preserving target IDs,
versions, pending-delivery references and encrypted bytes. An additive trigger
also projects Grok targets inserted by older writers during a rolling upgrade.
Queued deliveries keep their original target version when an endpoint is
replaced. Push uses the existing dispatcher. Pull uses the existing durable
inbox; no second queue or background loop is needed. Expansion
`1436_assistant_pull_health.sql` adds only a nullable principal health timestamp
and pull/unread lookup indexes. Existing owners have no migration step. The existing
32-byte messaging encryption key remains authoritative.

Approval requests, policy/budget enforcement, Routine ownership and the Agents
page remain in their existing packages and the other AEON-754 slices. Only Grok
is registered. OpenAI and Meta adapters are deferred; this feature adds no UI.

The existing `TestRoutineDispatcherUsesEncryptedTargetAndFencedCompletion`
behaviour test now includes production-authenticated round-trip, exact injected
retry boundaries, signature/redaction, dry-run, target version, authorization
revocation, pull polling/receipts/health without outbound attempts, frozen-host
retention, and pre-expansion Amy-shaped provisioning/rotation subtests. The
pickup-retention subtest covers the expired legacy online deadline, both
configured and implicit assistants, the exact retention boundary, one-time
sender notices, key rotation/revocation, disabling and replacement with push. Its
existing test tier is retained; no browser case or coordinator-owned tier record
is added.
