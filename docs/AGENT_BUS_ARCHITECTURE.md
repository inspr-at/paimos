# Instant Agent Bus Architecture

> **Status:** slices 1–4 implemented by PAI-826 in M154 / 5.18.0. Slices 5–7
> remain follow-up work.
>
> **Scope:** durable, seconds-latency, bidirectional delivery for PAIMOS agent
> messages. The PAIMOS ledger remains authoritative. Companion security
> invariants are in [AGENT_MESSAGE_SECURITY.md](AGENT_MESSAGE_SECURITY.md).

## 1. Outcome

A successful `paimos tell` must create one durable PAIMOS message and wake the
allowed receiver within seconds when that receiver has an enabled delivery
target. The sender can request one of two delivery levels:

- `simple`: enqueue a new user turn, or wake a receiver that can consume the
  message as a new turn.
- `steer`: try to add the message to a turn that is currently `inProgress`.
  When that is impossible, fall back to `simple`; never invent a vendor
  command.

This design supports both directions:

- Codex to Amy: `ppm` commits the message and POSTs the configured generic
  HTTPS webhook for Amy's Grok Bot routine.
- Amy to Codex: Amy writes through the existing PAIMOS send surface; a live
  Codex-side delivery worker sees the durable row within the current two-second
  follow interval and uses the exact Codex primitive selected by the message
  level.
- Codex to or from Phil: the same flow applies to Phil's explicitly registered
  target. PAIMOS does not infer a vendor or session from the name `phil`.
- Claude can be added through the supported simple paths below. Claude steer
  remains unsupported in this version of the design.

“Instant” means the first delivery attempt starts within seconds. It does not
mean that PAIMOS can wake a powered-off workstation or a local vendor session
with no running delivery worker.

## 2. Current state

The current production path at `04dea97` is a secure durable inbox, not a push
bus:

1. `POST /api/projects/{id}/messages` (normally `paimos tell`) writes an
   `agent_messages` row.
2. The server derives the sender from `X-Paimos-Agent-Name`, resolves the
   `<harness>:<agent>` receiver, applies the receiver's allowlist, action
   hold, hop, rate, body-size, and secret checks, and commits the row.
3. A receiver learns about an eligible row only by calling
   `GET /api/projects/{id}/messages/listen`, either once or through
   `paimos listen --follow`. Follow mode polls every two seconds.
4. `paimos listen --deliver` can hand the server-framed body to a local vendor
   adapter and advances the durable cursor only after successful handoff.

The current envelope has no delivery level. `--deliver-mode queue|steer` is
process configuration consulted only by the Codex listener, so the sender's
intent is not durable and different messages in the same inbox cannot request
different levels.

```mermaid
sequenceDiagram
    participant S as Sending agent
    participant P as PAIMOS API
    participant L as PAIMOS ledger
    participant R as Receiver process
    participant V as Vendor session

    S->>P: paimos tell / POST message
    P->>P: attribution + allowlist + security checks
    P->>L: INSERT durable envelope
    L-->>P: committed message_id and cursor
    P-->>S: delivered or held
    Note over P,R: No message-created wake event exists
    alt live listen process
        loop every 2 seconds
            R->>P: GET messages/listen
            P->>L: read after durable cursor
            L-->>P: eligible rows
            P-->>R: security-framed envelope
        end
        R->>V: local adapter handoff
        R->>P: POST messages/ack after success
    else Amy in Grok Bot
        Note over R,V: No PAIMOS inbound; hourly polling is only a stopgap
    end
```

### 2.1 Current invariants to preserve

- Public addresses remain `<harness>:<registered-agent>`.
- The write path derives the sender from trusted attribution. The current
  canonical `from` value is `paimos:<agent>`; the sender cannot choose it in
  the body.
- The allowlist is keyed by registered sender and receiver agent IDs. The
  address harness selects a delivery context; it is not a second authorization
  principal.
- An unlisted or action-request row is held and is never returned by listen.
  `message allow` is currently prospective: it does not replay the old held
  row. The verified flow therefore required a resend after allowlisting.
- Hop is system-owned and limited to 10. A `reply_to` advances from its parent;
  otherwise an existing `thread_id` advances from that thread's latest durable
  row, and a new thread starts at 1. Delivery retries are attempts for the same
  row and do not increment hop.
- At most 10 messages enter one attributed inbox page.
- Receiver reads are security-framed by the server. Human inspection keeps the
  structured raw envelope.
- An acknowledgement is monotonic and can name only a delivered, non-action
  row in the attributed inbox.

Two existing names must not acquire new meanings:

- `thread_id` is the PAIMOS conversation and hop-chain ID. It is **not** a
  Codex, Claude, Grok, or Cursor session ID.
- `agent_messages.delivered` and `delivered_at` currently mean “eligible for
  the receiver inbox after security checks.” They do **not** prove vendor
  handoff. Vendor delivery needs a separate state record.

## 3. Target architecture

The target has four layers:

1. **Ledger:** immutable message content and security decision.
2. **Delivery intent:** first-class `simple` or `steer`, plus a deterministic
   `simple` fallback and an operator-owned target binding.
3. **Delivery coordinator:** a transactional outbox and attempt state linked
   to the ledger row. It stores no second copy of message content.
4. **Adapters:** a local listener for workstation-owned sessions, or an HTTPS
   wake POST for a remotely routable target such as a Grok Bot routine.

```text
sender
  |
  | attributed POST /messages
  v
PAIMOS ledger -- same transaction --> delivery row/outbox reference
  |                                      |
  | source of truth                      +--> HTTPS wake dispatcher
  |                                      |      (Grok Bot routine)
  |                                      |
  +<-- attributed local listener --------+
         (Codex, later Claude/Grok CLI)
                    |
                    v
              exact vendor primitive
```

The delivery coordinator is not a mailbox. It contains only routing state,
attempt timestamps, typed outcomes, and a foreign key to the canonical
message. A vendor response is not copied back into PAIMOS. A receiving agent
that wants to reply writes a new attributed `paimos tell`.

### 3.1 Target registration

Each receiver address has at most one enabled primary delivery-target version
and, optionally, one enabled `simple` fallback target. Most bindings use the
same target for both levels; the second target exists only when the primary
steer target cannot perform a simple wake. The selector uses that fallback for
a `simple` message only when the primary adapter is the steer-only
`agentd_codex`; it preserves the established rule that a `steer` request uses
the fallback when the primary policy itself is capped at `simple`. Changing either target creates a
new version instead of mutating one already snapshotted onto a message.

Conceptual target fields:

| Field | Meaning |
|---|---|
| `id` | Opaque PAIMOS target ID |
| `instance` | Exactly one PAIMOS instance, for example `ppm` |
| `project_id`, `address` | Receiver ownership and `<harness>:<agent>` inbox |
| `adapter` | `codex`, `agentd_codex`, `claude_resume`, `claude_channel`, `grok_build`, or `grok_bot_routine` |
| `target_kind` | For example `codex_thread`, `agentd_session`, `claude_session`, `grok_session`, or `https_webhook` |
| `target_ref` | Receiver-owned vendor thread/session reference or encrypted webhook URL |
| `maximum_level` | Receiver policy: `simple` or `steer`; default `simple` |
| `role` | `primary` or `simple_fallback`; unique while enabled per receiver |
| `enabled`, `version` | Explicit activation and immutable version |
| `target_secret` | Optional receiver-owned sender secret the vendor requires in one wake request header (`grok_bot_routine`: the routine sender key sent as `Authorization: Bearer`); encrypted separately from `target_ref`, write-only, never a message field (PAI-828) |

Only the operator control plane may create, change, enable, or requeue a
target. A sender can request a level but cannot supply a vendor thread,
webhook URL, credentials, or `maximum_level`. Untrusted message text and
metadata never mutate target or allowlist configuration.

For Codex, `target_ref` must be a Codex session/rollout UUID or an exact Codex
session name accepted by `codex queue --thread`. It must never be a Cursor chat
UUID. PAIMOS conversation `thread_id` is never used as this value.

### 3.2 Commit and dispatch

For an allowlisted, non-action message, one database transaction:

1. inserts the canonical `agent_messages` row;
2. resolves and snapshots the enabled primary and optional simple-fallback
   target versions, if any; and
3. inserts one unique delivery row/outbox reference.

A held row creates no runnable delivery attempt. Allowlisting remains
prospective in the first implementation: the held historical row stays held
and the sender resends. If the product later needs release, it must be an
explicit, operator-only, audited `release` operation, not a side effect of
allowing a sender.

If no target exists, the message remains durably readable and its single
delivery-intent row is `blocked` with `target_missing`; it is not acknowledged
or dropped. Registering a target can explicitly requeue it by atomically
assigning the first target snapshot and transitioning that same row. A target
already snapshotted onto an attempted delivery is never changed.

For local sessions, the existing `listen --follow` process is the delivery
worker and provides a current upper-bound polling delay of approximately two
seconds. The server cannot directly reach a local Codex control socket. For
webhook targets, an `ppm` server worker starts the POST immediately after the
commit becomes visible.

### 3.3 Delivery state

Conceptual `agent_message_deliveries` fields:

| Field | Meaning |
|---|---|
| `delivery_id` | Stable UUID and retry/idempotency key |
| `message_id` | Unique canonical message; exactly one delivery-intent row |
| `primary_target_id`, `fallback_target_id` | Nullable snapshotted target versions |
| `requested_level` | Immutable copy of the message level |
| `effective_level` | `simple` or `steer` actually used; set on success |
| `state` | `pending`, `leased`, `retry`, `blocked`, `handed_off`, or `dead` |
| `fallback_reason` | Typed reason such as `idle`, `unsupported`, `policy_capped`, `target_missing`, `not_steerable`, `transport_error` |
| `attempt_count`, `next_attempt_at`, `lease_until` | Retry and competing-worker control |
| `last_error_code` | Typed and redacted; never vendor output or credentials |
| `handed_off_at` | Successful vendor handoff time |

`message_id` is unique in this table. This avoids nullable-target uniqueness
ambiguity and represents one delivery intent, not one mailbox per target. A
worker leases the oldest row for one address so concurrent workers cannot
inject or acknowledge messages out of order. Lease expiry permits recovery
after a crash. Completion is idempotent.

The existing inbox cursor advances only after:

- a local vendor primitive reports success; or
- the configured webhook returns a successful 2xx acceptance.

A command exit of zero, successful Codex JSON-RPC response, successful Claude
channel write, or webhook 2xx means “handoff accepted,” not “the model
understood or acted on the content.” Exactly-once model processing is not
possible across vendor boundaries.

## 4. Message contract

### 4.1 Canonical envelope additions

The canonical envelope gains the following server-normalized fields:

```json
{
  "delivery_level": "steer",
  "delivery_fallback": "simple",
  "delivery_target": {
    "primary": {
      "binding_id": "019c...",
      "kind": "codex_thread"
    },
    "simple_fallback": null
  }
}
```

Rules:

- `delivery_level` is required on stored/output rows and is one of `simple` or
  `steer`. Existing rows and clients that omit it default to `simple`.
- `delivery_fallback` is `simple` in this contract. It is stored so replay has
  deterministic behavior rather than inheriting future process defaults.
- `delivery_target` is nullable and server-owned. It contains only an opaque
  primary binding/version ID, optional simple-fallback binding/version ID, and
  non-secret kinds. The actual `target_ref` values are disclosed only to the
  authorized delivery worker.
- `delivery_level`, `delivery_fallback`, and `delivery_target` are required
  output properties. `delivery_target` is JSON `null` when unresolved.
- The HTTP send request accepts `"delivery_level":"simple|steer"`.
  `paimos tell` exposes the same field as `--level simple|steer`, defaulting to
  `simple`. Both reject caller-supplied target, fallback, effective level, or
  delivery state.
- The message's requested level is immutable. Downgrade is recorded on the
  delivery row as `effective_level=simple` plus `fallback_reason`; PAIMOS does
  not rewrite the sender's request.
- Receiver target policy can cap `steer` to `simple`. An allowlisted sender may
  request an interrupt but cannot force one.

The additive database sketch is:

```sql
ALTER TABLE agent_messages
  ADD COLUMN delivery_level TEXT NOT NULL DEFAULT 'simple'
    CHECK(delivery_level IN ('simple', 'steer'));
ALTER TABLE agent_messages
  ADD COLUMN delivery_fallback TEXT NOT NULL DEFAULT 'simple'
    CHECK(delivery_fallback = 'simple');
ALTER TABLE agent_messages
  ADD COLUMN delivery_primary_target_id TEXT;
ALTER TABLE agent_messages
  ADD COLUMN delivery_fallback_target_id TEXT;
```

The follow-up implementation must update the closed JSON schema, Go envelope,
HTTP request, CLI JSON shape, data-model documentation, migration tests, and
old-row backfill together. This document does not reserve a migration number.
Slice 1 has no target registry: it always stores and returns
`"delivery_target": null` and performs no target lookup. Slice 2 introduces
resolution and the object form.

### 4.2 Send idempotency

The current CLI sends an `Idempotency-Key` header, but the message handler does
not consume it. The target bus must make message creation idempotent before
automatic sender retry is enabled:

- The header remains optional for backward compatibility. A send without it
  keeps current non-idempotent behavior.
- Accept an opaque 1–128-byte key. One logical `paimos tell` invocation creates
  one key before its first HTTP attempt and reuses that key for every retry.
- Scope the key to instance, project, and attributed sender.
- In the same transaction as message creation, atomically reserve the scoped
  key, normalized-request hash, and resulting `message_id`. A unique constraint
  serializes concurrent duplicates; the loser reads the winning row.
- A retry with the same key and same request returns the original
  representation and status without creating a row or delivery intent.
- Reuse with a different request fails with HTTP 409 and a stable
  `agent_message_idempotency_conflict` code.
- Keep the reservation for the lifetime of the canonical message. A future
  message-retention feature must expire both atomically.
- No raw key, body, target reference, or credential appears in normal logs.

Delivery retries reuse the stable `delivery_id`; they never create another
message and never increment hop.

## 5. Level and fallback semantics

`simple` and `steer` describe receiver interruption semantics, not transport
priority.

| Requested state | Result |
|---|---|
| `simple` with usable target | Use that target's supported new-turn or wake primitive |
| `steer`, Codex target, latest turn is `inProgress` and steerable | Use `turn/steer`; record effective `steer` |
| `steer`, Codex thread is idle or has no `inProgress` turn | Use `codex queue`; reason `idle` |
| `steer`, Codex returns `activeTurnNotSteerable` or the expected turn races | Use `codex queue`; reason `not_steerable` |
| `steer`, Codex daemon/proxy/initialize/read/steer transport fails | Use `codex queue`; reason `transport_error` |
| `steer`, Codex returns a malformed frame, initialize/read returns a JSON-RPC rejection, steer returns an undocumented rejection, or a successful response has invalid/contradictory thread/turn status, history, IDs, or schema | Leave unacknowledged and retry; do not mask protocol drift with queue fallback |
| `steer`, receiver policy has `maximum_level=simple` | Use the configured simple target; reason `policy_capped` |
| `steer`, adapter has no steer primitive | Use its supported simple primitive; reason `unsupported` |
| `steer`, no thread target, but a distinct simple target is configured | Use that simple target; reason `target_missing` |
| Any level with no usable simple target | Leave unacknowledged and `blocked`; do not invent a target or command |
| Any other transient transport failure | Leave unacknowledged and retry the same delivery |

For Codex, simple fallback still requires a valid Codex thread target. If
neither the requested target nor a configured default exists, queueing is
impossible and the correct result is `blocked`, not a Cursor chat UUID guess.
Codex fallback logs contain only a controlled phase and typed reason. Raw
vendor diagnostics, target references, message bodies, and secret-like values
are not copied into listener output. Queue stdout and stderr are discarded;
only a controlled nonzero-exit error survives a failed handoff.

## 6. Target sequences

### 6.1 Codex to Amy, then Amy to Codex

```mermaid
sequenceDiagram
    autonumber
    participant C as Codex agent
    participant P as ppm API/ledger
    participant D as ppm webhook dispatcher
    participant G as Amy Grok Bot routine
    participant A as Amy chat
    participant W as Codex-side listener

    C->>P: attributed tell to paimos:amy, level=simple
    P->>P: allowlist/security checks
    P->>P: commit message + webhook delivery reference
    P-->>C: durable message_id
    D->>G: HTTPS POST agent_message.available
    Note over D,G: Stable delivery_id; framed content; no credentials or target secrets
    G-->>D: 2xx accepted
    D->>P: mark handed_off and advance Amy cursor
    G->>A: routine wakes Amy with a new turn

    A->>P: existing authenticated tell to codex:codex
    P->>P: commit message + Codex delivery reference
    W->>P: attributed listen, within about 2 seconds
    P-->>W: framed envelope + receiver-owned Codex target
    alt level=simple
        W->>C: codex queue --thread THREAD --message TEXT
    else level=steer and turn inProgress
        W->>C: app-server proxy handshake and turn/steer
    else steer unavailable or idle
        W->>C: codex queue --thread THREAD --message TEXT
    end
    W->>P: complete and acknowledge after success
```

The outbound action from Amy is an existing PAIMOS send through whatever
authenticated PAIMOS surface her routine is configured to use. There is no
`grok send`, `grok queue`, or Grok Bot chat-injection command.

### 6.2 Steer decision at the Codex adapter

```mermaid
sequenceDiagram
    participant W as PAIMOS Codex worker
    participant X as codex app-server proxy
    participant Q as codex queue

    W->>X: HTTP Upgrade handshake (proxied stream is WebSocket frames)
    X-->>W: 101 Switching Protocols
    W->>X: initialize {clientInfo, capabilities:{experimentalApi:true}}
    X-->>W: initialize result
    W->>X: initialized
    W->>X: thread/read {threadId, includeTurns:true}
    X-->>W: thread.status and turns
    alt latest usable status is inProgress
        W->>X: turn/steer {threadId, expectedTurnId, input:[{type:"text",text}]}
        alt success
            X-->>W: success
        else not steerable or turn race
            X-->>W: typed error
            W->>Q: codex queue --thread THREAD --message TEXT
            Q-->>W: exit 0
        end
    else no inProgress turn
        W->>Q: codex queue --thread THREAD --message TEXT
        Q-->>W: exit 0
    else daemon, proxy, initialize, read, or steer transport error
        W->>Q: codex queue --thread THREAD --message TEXT
        Q-->>W: exit 0
    else rejection or malformed/mismatched successful response
        W-->>W: fail delivery for retry; do not queue
    end
```

`codex app-server daemon start` must already have started the daemon. The
per-message worker uses `codex app-server proxy` over stdio; PAIMOS does not
open the control socket itself.

### 6.3 Directional behavior

| Direction | Requested level | Effective behavior |
|---|---|---|
| Codex -> Amy | `simple` | `ppm` POSTs Amy's generic routine webhook |
| Codex -> Amy | `steer` | Grok Bot steer is unsupported; POST the same simple wake and record fallback |
| Amy -> Codex | `simple` | Codex-side worker calls `codex queue` |
| Amy -> Codex | `steer` | `turn/steer` only for an `inProgress` turn; otherwise `codex queue` |
| Codex <-> Phil | either | Same rules using Phil's registered target; no target is inferred from his name |
| Any direction -> Claude | `steer` | Claude steer is unsupported; use a configured simple path or remain blocked |

## 7. Webhook wake contract

### 7.1 Who POSTs

The PAIMOS instance that owns the ledger row POSTs. A message in `ppm` can
cause only the `ppm` dispatcher to call a target registered in `ppm`. For Amy,
the destination is the generic HTTPS webhook generated for her Grok Bot
routine. Grok Bot has no native PAIMOS listener and no public API that injects
into an arbitrary existing chat.

The POST is a transient handoff derived from the ledger, not a second mailbox.
The dispatcher reloads the immutable message by ID when sending; the outbox
does not store a second body.

### 7.2 Payload

The proposed versioned wake payload is:

```json
{
  "event": "agent_message.available",
  "version": 1,
  "instance": "ppm",
  "delivery_id": "019c...",
  "project": "PHAROS",
  "message_id": "01a03c88-b041-7c3d-bcfa-23aa05674d8f",
  "cursor": 1234,
  "to": "paimos:amy",
  "requested_level": "steer",
  "effective_level": "simple",
  "fallback_reason": "unsupported",
  "content": "<paimos-message ...>...server-framed untrusted content..."
}
```

The payload contains the server-added security frame and only the metadata
needed to present and deduplicate the event. It omits arbitrary message
metadata, vendor thread/session references, webhook URLs, API keys, auth
tokens, cookies, and all other secrets. The existing body secret rejection
still applies before a message can enter this path. The request carries the
stable `Idempotency-Key` and, for `grok_bot_routine`, the routine sender key
as `Authorization: Bearer <sender key>` (section 7.3); the key is never part
of the payload.

### 7.3 Authentication and endpoint safety

For a direct Grok Bot routine webhook, use only the generic HTTPS webhook
primitive:

- Treat the vendor-issued webhook URL as a capability secret.
- Store it encrypted through the PAIMOS secret boundary and render it only to
  the outbound HTTP client.
- Never place it in a message, wake body, audit detail, error, or UI response.
- Require HTTPS, an operator-approved hostname, bounded DNS resolution and
  connect timeouts, and rejection of loopback, link-local, and private
  destinations unless an explicit deployment policy allows them.
- Disable redirects, or revalidate every redirect hostname, resolved address,
  and connected IP against the same policy before following it.
- Authenticate every wake exactly the way the vendor's own trigger card does.
  A Grok Bot routine created with the "When a webhook fires" trigger issues a
  POST URL, a **sender key**, and a ready-made `Authorization: Bearer <sender
  key>` header (verified 2026-08-27 against the vendor's trigger card and its
  public support guidance:
  [webhook URL and sender key on the desktop trigger card](https://forum.cursor.com/t/webhook-url-missing-on-ios/169589),
  [example `curl … -H "Authorization: Bearer crsr_…"`](https://forum.cursor.com/t/grok-bot-webhooks-are-failing-with-internal-server-error/169323)).
  PAIMOS stores only the raw sender key (`target_secret`, CLI
  `--target-key-file`), encrypted under its own secretvault domain, and adds
  the header at dispatch. Do not invent any other signing scheme for Grok Bot.
- Treat the sender key as a capability secret exactly like the URL: never
  place it in a message, wake body, audit detail, error, log, list/status
  response, listen disclosure, ticket, or process argument. The CLI accepts
  both values only from stdin or from a regular, owner-only (`0600`),
  single-linked file owned by the caller, opened without following symlinks.

A `grok_bot_routine` target without a sender key is refused at registration,
and a version that predates the key column is never dispatched: the delivery
blocks with `target_secret_missing` without contacting the endpoint until a
new version is registered with the key. For a PAIMOS-controlled adapter
endpoint, a follow-up may additionally sign the request with an encrypted
per-target secret through the same `SecretHeaderPlugin` capability. That is
an internal HTTP adapter contract, not a Grok, Claude, or Codex primitive.

The receiver's outbound reply uses separately scoped PAIMOS credentials and
trusted agent attribution. A webhook URL never grants PAIMOS write access.

### 7.4 Retry and idempotency

- First POST starts immediately after commit.
- Retry network failures, timeouts, HTTP 408/425/429, and 5xx with jittered
  backoff beginning in seconds and capped at one minute. Honor a bounded
  `Retry-After`.
- Treat other 4xx responses as target/configuration failures and mark the
  delivery `blocked`; do not hammer the endpoint.
- Every retry uses the same `delivery_id`, payload, and `Idempotency-Key`
  header. A 2xx response marks handoff complete and stops retry.
- An ambiguous network failure can produce a duplicate webhook invocation.
  The routine should deduplicate by `delivery_id` when its platform permits.
  PAIMOS does not claim exactly-once Grok Bot execution.
- Exhausted automatic attempts mark the delivery `dead` but leave the
  canonical message readable and operator-requeueable.

Retry state and errors contain no message body, vendor response, capability
URL, or credential.

## 8. Adapter matrix

“Current” describes `04dea97`, with the Claude rows updated for PAI-827. “Target” describes the smallest supported
mapping; anything marked **UNSUPPORTED** must not be replaced with a guessed
CLI or socket frame.

| Receiver | Level | Allowed vendor primitive | Current | Target and fallback |
|---|---|---|---|---|
| Codex | `simple` | `codex queue --thread <THREAD> --message <TEXT>` | Implemented by `listen --deliver codex`; mode is process-wide | Supported. `<THREAD>` is a Codex rollout/session UUID or exact session name, never a Cursor chat UUID |
| Codex | `steer` | Start daemon with `codex app-server daemon start`; worker runs `codex app-server proxy`, whose proxied stream carries the WebSocket HTTP Upgrade handshake and one JSON-RPC message per text frame; performs `initialize` (`capabilities.experimentalApi=true`) + `initialized`, reads exactly one `thread/read {threadId,includeTurns:true}`, selects the latest nonempty `inProgress` turn, and calls `turn/steer {threadId, expectedTurnId, input:[{type:"text",text}]}` | Implemented (PAI-825 follow-up; 5.17.3–5.18.0 wrote JSON lines into the proxy and never got an `initialize` answer) | Supported. Idle, not loaded, race, not-steerable, and genuine app-server transport failure fall back to the exact queue primitive; completed initialize/read JSON-RPC rejections, malformed/mismatched successful responses, and any other `turn/steer` rejection (unknown method, request-shape drift, sub-agent ownership, internal error) fail the delivery instead of queueing |
| Claude local | `simple` | `claude -p --resume <session_id>` or `claude -p --cloud <session_id>` | Implemented by `listen --deliver claude` from a receiver-owned `claude_resume` / `claude_session` target; framed body over stdin, zero exit is handoff, completed through `delivery-complete` | Supported. A local UUID resumes an idle session; a `session_…`/`cse_…` id queues a cloud follow-up |
| Claude Channels | `simple` | MCP `notifications/claude/channel`; session opts in with `--channels` or `--dangerously-load-development-channels` | Research-preview channel path under `paimos serve --mcp-stdio --channel-as`; leases `claude_channel` targets and completes each push | Supported simple push when explicitly enabled; successful JSON-RPC write is handoff |
| Claude | `steer` | **UNSUPPORTED** | Falls back to the selected simple primitive with `fallback_reason=unsupported`; Claude targets are fixed to `maximum_level=simple` | Fall back to a configured simple resume/cloud or Channels target; otherwise remain blocked |
| Grok CLI / Grok Build | `simple` | `grok --single` (or `-p`) with `--resume`; current adapter uses one bounded `--single --resume` turn | Implemented behind `--enable-grok-build-delivery` | Supported only as an explicit receiver-owned target |
| Grok CLI / Grok Build | `steer` | **UNSUPPORTED** | No steer primitive | Fall back to the exact simple resume primitive |
| Grok Bot | `simple` | Generic HTTPS webhook that triggers Amy's routine | No PAIMOS inbound; current docs correctly call it receive-by-human | Preferred instant wake. PAIMOS POSTs the framed event to the configured routine webhook |
| Grok Bot | `steer` | **UNSUPPORTED** | No public inbound chat injection or steer API | Fall back to the same simple routine wake; never claim mid-turn steering |

The following names are deliberately absent because they do not exist:

- no `codex steer` CLI;
- no `claude steer` or send-to-session CLI;
- no undocumented Claude inbox `{"type":"user",...}` frame;
- no `grok send`, `grok queue`, or `grok steer`;
- no Grok Bot arbitrary existing-chat injection API.

`turn/start` and `turn/interrupt` are real Codex app-server methods but are not
part of this message-delivery contract. Delivery does not start or cancel a
receiver's work.

## 9. Security and trust boundaries

1. **Allowlist before outbox.** Only an allowlisted, non-action row gets a
   runnable delivery. A target binding does not bypass the allowlist.
2. **Attribution is mandatory.** Sender identity comes from the authenticated
   request; listener/ack identity must match the receiver address.
3. **Steer is receiver-capped.** `delivery_level=steer` is a request, not
   authority. The receiver target must explicitly allow it.
4. **Bodies are untrusted data.** The server frame is added before every local
   or webhook handoff. Message text cannot grant consent, allow a sender,
   alter configuration, merge code, deploy, or approve a typed action.
5. **Action requests remain human-only.** `is_action_request` and heuristic
   action matches never enter the outbox or an agent inbox.
6. **No secrets.** Existing body checks remain. Arbitrary metadata is omitted
   from webhooks. Credentials and capability URLs stay in encrypted target
   configuration and process headers/environment.
7. **Hop and rate bounds remain.** A new conversational reply increments hop;
   a delivery retry does not. The current 10-hop, 10-writes/minute, 32 KiB,
   and 10-per-turn bounds remain.
8. **No command construction from text.** Vendor programs are invoked with
   fixed argv or exact JSON-RPC methods. No shell is involved.
9. **Acknowledge after handoff.** Failed or unavailable adapters leave the
   row pending. No adapter advances a cursor before its primitive succeeds.
10. **No implicit historical release.** Allowing a sender does not suddenly
    inject previously held content.

### 9.1 `ppm` and `pma` isolation

`ppm` and `pma` are separate buses:

- Every message, target, delivery ID, idempotency key, outbox row, credential
  reference, cursor, and audit record belongs to exactly one instance.
- A dispatcher has one configured instance/base URL and refuses target rows
  for another instance.
- A wake payload carries the non-secret instance name. A receiver configured
  for `ppm` rejects `pma` and vice versa.
- There is no cross-instance target lookup, credential fallback, cursor
  advance, resend, or dead-letter drain.
- Cross-instance relay is out of scope; if ever required, it needs a separate
  explicit bridge design and allowlist on both sides.

## 10. Known repository contradictions and gaps

Official vendor behavior in the adapter matrix wins over contradictory repo
text:

1. `docs/AGENT_INTERFACE.md` says Claude uses
   `CLAUDE_CODE_MESSAGING_SOCKET`, and the old 5.16.0 changelog says native
   socket delivery exists. Live `deliverClaude` always returns adapter
   unavailable, and official socket documentation defines only optional auth,
   not a user-message frame. The socket claim, `--deliver-target` help that
   advertises a Claude Unix socket, and the dead
   `CLAUDE_CODE_MESSAGING_SOCKET` lookup are repo bugs; no implementation may
   invent the frame.
2. The PAI-825 changelog says Codex falls back when a live turn is not
   steerable. 5.18.0 added that fallback, but 5.17.3–5.18.0 also wrote
   newline-delimited JSON into `codex app-server proxy`, whose proxied stream
   is documented as the WebSocket HTTP Upgrade handshake plus frames, so the
   daemon never answered `initialize` and every live steer ended as
   `initialize Codex app-server: EOF` with an orphaned native proxy. The
   PAI-825 follow-up speaks the documented framing, checks `thread/read`
   status before paying for turn discovery, and bounds a silent proxy with a
   precise error. The target fallback table above is the implemented behavior.
3. `--deliver-mode` is accepted for all listeners but used only by Codex.
   Message-level intent removes that silent process-wide mismatch.
4. The CLI emits `Idempotency-Key` on POST, but the message endpoint does not
   currently enforce it.
5. Held messages are not retroactively released by `message allow`; a new send
   is required. Documentation and UI must continue to say this plainly.

## 11. Smallest shippable ticket split

Slices 1–4 are implemented by PAI-826; later slices remain planned.

### Slice 1 — durable delivery intent (shipped)

- Add `delivery_level`, fixed `delivery_fallback=simple`, and nullable
  server-owned target binding to the durable envelope and closed JSON schema.
- Default existing clients and rows to `simple`.
- Always persist/output `delivery_target: null`; Slice 1 has no target lookup.
- Add `paimos tell --level simple|steer` and the matching HTTP
  `delivery_level`; reject sender-supplied target, fallback, or effective
  state.
- Enforce the optional, atomic, message-lifetime POST idempotency contract in
  section 4.2, including concurrent duplicate and conflict tests.
- Add migration, API, CLI, schema, security, and compatibility tests.
- Do not change vendor delivery behavior yet.

This slice requires no vendor verb.

### Slice 2 — target registry and delivery state (shipped)

- Add versioned, operator-owned per-instance receiver target bindings.
- Add the linked delivery/outbox state machine, unique delivery ID, leasing,
  oldest-first ordering, typed fallback reasons, requeue, and redacted audit.
- Preserve current held-row and cursor semantics.
- Add receiver `maximum_level` policy with default `simple`.
- Add SSRF and secret-storage tests before enabling HTTPS targets.

### Slice 3 — Grok Bot routine wake (shipped)

- Add the `ppm` outbound webhook dispatcher and the versioned,
  security-framed, metadata-minimized payload.
- Configure Amy's receiver binding with the vendor-generated generic routine
  webhook capability URL.
- Implement retry, 2xx handoff acknowledgement, stable delivery ID, duplicate
  tolerance, and dead-letter/requeue visibility.
- Acceptance: a Codex tell wakes Amy's routine in seconds without a poll.
- Explicitly label steer to Grok Bot as fallback-to-simple.
- PAI-828: send the routine's sender key as `Authorization: Bearer` from the
  encrypted per-target `target_secret` (CLI `--target-key-file`, file or
  stdin only); refuse a `grok_bot_routine` target without it and never
  dispatch a pre-key version.

### Slice 4 — message-level Codex delivery (shipped)

- Make the live Codex-side listener select behavior from each message instead
  of one process-wide `--deliver-mode`.
- Resolve only receiver-owned Codex thread targets.
- Reuse the exact existing `codex queue` and app-server proxy JSON-RPC
  primitives.
- Add idle, missing-target, expected-turn race, and
  `activeTurnNotSteerable` fallback tests.
- Acceptance: Amy or Phil can tell Codex with `simple` or `steer`; successful
  handoff acknowledges once.

### Slice 5 — Claude simple delivery

- First map the existing opt-in Channels path to `simple` delivery state.
- Separately add and test idle local delivery using only
  `claude -p --resume <session_id>` or
  `claude -p --cloud <session_id>`.
- Keep steer **UNSUPPORTED** and fall back to the selected simple binding.
- Remove the false Claude messaging-socket claims, stale CLI help, and dead
  socket-target lookup.

### Slice 6 — Grok CLI target parity

- Wire receiver-owned Grok session targets to the existing bounded
  `grok --single`/`-p` plus `--resume` simple adapter.
- Preserve the explicit experimental enable gate, canonical lowercase session
  UUID check, fixed no-shell argv, disabled tools/planning/subagents/web, one
  turn, discarded vendor response, and zero-exit acknowledgement.
- Keep Grok steer **UNSUPPORTED** and record fallback to `simple`.

### Slice 7 — operations and rollout

- Add target health, pending/blocked/dead counts, last typed error, retry and
  operator requeue controls without exposing body or credentials.
- Add end-to-end tests for both directions, process crash/lease recovery,
  duplicate webhook attempts, cursor ordering, instance isolation, and
  allowlist/action/hop invariants.
- Roll out to one `ppm` project and receiver target at a time. `pma` requires
  its own independent configuration and test.

## 12. Non-goals

- No merge or automatic merge behavior.
- No direct Codex control-socket connection and no bespoke frame code: the
  WebSocket framing the daemon requires is spoken only through the documented
  `codex app-server proxy` byte pipe with the standard Go WebSocket client.
- No invented vendor CLI, JSON frame, or Grok Bot chat API.
- No Grok Bot mid-turn steer.
- No Claude mid-turn steer in this contract.
- No OpenClaw or Merlin adapter.
- No PAIMOS-to-PAIMOS cross-instance bridge.
- No automatic execution of free-text action requests.
- No automatic allowlist, target, configuration, deploy, or merge changes from
  an agent message.
- No automatic release of historical held rows.
- No exactly-once claim for model attention or vendor-side execution.
- No storage of vendor responses as implicit replies.

## 13. Managed harness worker binding (PAI-848)

The additive M161 `harness_sessions` resource binds one active harness address
to an encrypted `agent_message_targets` version. It is intentionally separate
from `agent_runs` and all PAI-809 supervisory-control tables/actions. The
private vendor session reference remains target ciphertext; M161 retains only
its domain-separated digest and target FK for idempotent identity and redacted
host/session attribution.

Identity and address uniqueness apply only while a generation is active. A
stopped row is immutable history; re-registering the same stable external
reference creates a fresh public row and reuses its matching enabled encrypted
target version, so delivery snapshots from the prior generation remain FIFO-
drainable without exposing or rewriting the private reference.

`managed_harness` is a local adapter with `MaximumLevel=steer`. It has no
delivery primitive: the PAI-849 daemon leases the complete address FIFO through
`ListInbox` without a level filter and commits through `CompleteLocalDelivery`.
Simple-only managed sessions use the same path; steer-capable sessions must
complete older simple work before later steer. This keeps the message ledger
authoritative for FIFO, leases, requested/effective levels, fallback reasons,
handoff time, and cursor acknowledgement. The harness drain response strips
the decrypted target reference before crossing HTTP. Unmanaged steer
additionally requires the existing `codex` adapter and a `steer` target cap;
CLI validation is only an early error, not the security boundary.
