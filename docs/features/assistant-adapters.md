<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Hosted assistant adapters

Hosted assistants use the existing principal inbox. Grok is the first vendor;
`HostedAssistantAdapter` supplies the vendor payload while the inbox owns
encrypted configuration, public HTTPS delivery, signatures, fenced claims,
retry backoff, idempotency and delivery receipts. No vendor process is started.

The tenant registry is exposed within each project:

- `GET /api/projects/{projectId}/assistant-adapters` lists up to 100 entries
  using `limit` and an `after` target-ID cursor; `next_after` is null at the end.
- `POST` to that collection binds an existing active agent principal to a
  `grok` endpoint. Supply `principal_id`, a `grok_bot:…` address, `endpoint` and
  the receiver's existing `webhook_secret`. An Aeon API key is rejected as a
  webhook credential. A replacement creates a new immutable target version.
- `GET /api/projects/{projectId}/assistant-adapters/{adapterId}` returns the
  redacted configuration and last delivery success/failure timestamps.
- `POST /api/projects/{projectId}/assistant-adapters/{adapterId}/test-send`
  defaults to `dry_run: true`. It validates the current encrypted configuration
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
responses contain neither. Health retains the last success and last failure
independently; compare their timestamps to determine which happened most
recently. A dry-run does not establish delivery health. Health and delivery
results commit together, so a failed completion cannot record a success.

List results include disabled historical versions. `enabled` identifies the
binding used for new messages; each version's health describes attempts made
against that version's endpoint. A replacement starts with no delivery history.

## Webhook protocol and receipts

The Grok JSON payload and its existing Bearer authentication header are
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
This adapter slice creates no keys, grants, assistant class or policy.

## Upgrade and scope

Migration `1435_assistant_adapters.sql` backfills all existing
`grok_bot_routine` targets as `grok` registry entries, preserving target IDs,
versions, pending-delivery references and encrypted bytes. An additive trigger
also projects Grok targets inserted by older writers during a rolling upgrade.
Queued deliveries keep their original target version when an endpoint is
replaced. The existing dispatcher is used; no second queue or background loop
is needed. The existing 32-byte messaging encryption key remains authoritative.

Approval requests, policy/budget enforcement, Routine ownership and the Agents
page remain in their existing packages and the other AEON-754 slices. Only Grok
is registered. OpenAI and Meta adapters are deferred; this feature adds no UI.

The existing `TestRoutineDispatcherUsesEncryptedTargetAndFencedCompletion`
behaviour test now includes production-authenticated round-trip, exact injected
retry boundaries, signature/redaction, dry-run, target version, authorization
revocation and restricted-owner migration subtests. Its existing test tier is
retained; no browser case or coordinator-owned tier record is added.
