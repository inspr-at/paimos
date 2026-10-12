# Owned native sessions: C0 contract

AEON-1266 publishes the additive `native-session/1.0` contract. All 22 public
routes are mounted with authenticated disabled handlers: `503`,
`{"reason":"feature_disabled","request_id":null,"retryable":false}`. Responses
carry `Aeon-Contract: native-session/1.0` and `Cache-Control: no-store`, including
authentication failures. The module has no database or native-service fields,
does not read request bodies, and cannot start a runtime. `New(Services)` refuses
enablement even with `Qualified: true`; production mounts only `NewDisabled()`.
This is groundwork and remains hidden from release notes.

The frozen source is `internal/nativecontract/contract.json`: the AEON-1262 C0
plan plus LEAD's eleven `c0-inputs.md` answers dated 2026-10-13 00:20. The newer
answers refine stream cursors, event variants, projection and heartbeat timing;
all original plan schema fields and public routes are retained. There are 59
closed schemas, 22 public routes and 17 owner-socket routes. Go ports live in
`internal/nativecontract/ports.go`; Go DTOs, TypeScript DTOs, routes, bounds and
the OpenAPI area share the frozen source. All required nullable fields remain
present. Dates use RFC3339; sequence values are decimal strings bounded to
uint64, preserving precision in JavaScript.

Regenerate with `python3 internal/nativecontract/generate.py`, then
`node api/generate.mjs --write`. `gofmt` must be on PATH. Check declaration parity
with `python3 internal/nativecontract/generate.py --check` and bundle parity with
`node api/generate.mjs --check`. Only the area fragment is tracked; the shared
OpenAPI bundle stays ignored build output. OpenAPI paths omit `/api` because the
base server URL already supplies it. Existing API contracts, attribution
commands and attachment commands are unchanged.

Each logical `owned_session_id` is stable. `harness_session_id`, `runtime_epoch`
and `daemon_generation` remain distinct proofs. A fresh native writer increments
the separate integer session `generation`, including resume, restart, take and
return. Each event carries identical `seq` and `sequence` decimal strings,
gapless within that generation. SSE uses `Last-Event-ID: <generation>:<seq>`;
this position never grants authority. Actor, tenant, viewer ID, exact binding,
view consent and expiry must be checked independently before replay and on every
frame and keepalive. `SequenceGuard` rejects stale bindings, holes, duplicates
and uint64 rollover without advancing on failure.

`GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/events` accepts
an exclusive `before` sequence and the expected `generation`; `limit` defaults
to 200, with a maximum of 500 and 1 MiB. It returns newest first with explicit
truncation and `next_before`. The living replay buffer remains capped at 64
frames / 256 KiB / 120 seconds. Expired positions return a conflict or explicit
gap and snapshot reload. Only authorized explicit finals use existing chat
storage and retention; raw native JSON, interim token chunks, private reasoning,
tool bodies and terminal bytes never enter a new durable log.

The kind selects the payload exactly. Original variants are snapshot,
structured, expert, terminal, receipt, approval, controller, quota, gap and end.
The C0 refinements add:

| Event | Payload and meaning |
| --- | --- |
| `assistant.delta` | `message_id`, bounded `text`; clients merge by message ID. |
| `assistant.final` | Full approved explicit `text`, `message_id`, nullable `input_id`, `inferred`. Native correlation wins; otherwise the first final after delivered input is explicitly inferred. Token assembly is never final evidence. |
| `tool.start` / `tool.result` | Paired `call_id`; name, approved argument/result summaries and duration. Summaries are bounded to 2048 bytes and subject to output policy; raw tool inputs/results remain excluded. |
| `input.delivered` | `input_id`, `from` principal, `level: immediate \| next_step`, bounded text; only correlated delivery evidence. |
| `input.not_confirmed` / `input.generation_changed` | `input_id`, `retryable: true`; timeout or deliberate resend after resume, subject to uncertainty/ownership checks. |
| `input.session_ended` / `input.refused` | `input_id`, `retryable: false`; final failure. |
| `stream.paused` | One row with `reason: human_control`, `generation` while a person drives the TUI. |
| `control.lease_expired` | `generation`; the server expires an unreachable holder or host. |
| `control.ended_by` | `by` principal and bounded `reason`; owner/admin ended control. |

The structured stream pauses during takeover in v1. `source: transcript` is
reserved for a separately qualified lower-fidelity v2 and is rejected in v1;
`source: native` is current. The server owns a 5-second heartbeat / 30-second
grace lease. Lease expiry, turn completion, viewer detach and an API outage do
not prove native exit or free occupancy. The holder's PTY size wins; viewers
letterbox and cannot resize or send device replies, paste or keystrokes.

Session capabilities include `take_supported`, `events_supported`,
`interrupt_supported`, `chat_supported`. The take, interrupt and input aliases
must agree; clients expose only advertised operations. Web v1 never advertises
take. Agent principals, including owner-workstation agents, never receive PTY
streams or `terminal_viewer_url`. The URL is nullable and available only to
people with current session-read authority. `PersonProjection` strips the URL
and terminal/Expert/take capabilities for agents; the transport must enforce the
same policy. The disabled module returns server-side `403/wrong_principal` for
agent terminal requests and end-control requests.
Private paired-daemon publication is a producer operation, distinct from viewer
access; publishing a qualified frame never grants the paired agent a PTY stream.

`POST /api/sessions/{id}/control/end` is reserved for a person who owns the
session or administers its project. Its exact binding, controller epoch and
revision are required. The holder is notified, `control.ended_by` is emitted,
and control returns to the agent only through the existing serialized exit and
successor-grant barrier. There is no force-take. The matching local operation is
`POST /v1/native-sessions/{ownedSessionId}/control/end`.

Local HTTP stays on the protected owner-only Unix socket. Every request and
response, including GET/SSE, carries
`X-Aeon-Session-Protocol: aeon-native-session/1`. Mutation envelopes contain
exactly protocol, request ID, method, expected binding and method-specific
params. Start has a null expected binding; other methods bind to the current
runtime and nested request IDs must agree. The existing kernel peer/token and
paired-principal proofs remain mandatory. Viewer and control proof travel in
headers, never URLs. Local profile, actor, process and tool-capability types
refuse JSON serialization/deserialization; closing a tool capability erases its
run-bound proof bytes.

`Validator` rejects missing/extra/duplicate fields, unsupported variants, wrong
bindings, mismatched event payloads, invalid formats, byte/depth/item bounds,
uint64 overflow, unavailable capabilities and invalid conditional arms.
`ValidateBoundJSON` consumes trusted transport context; `DecodeLocal` additionally
checks protocol, method and request correlation. These are validation helpers,
not authorization or provider qualification. Enabled implementations must check
current grant and permissions under the final access/native write lock, preserve
tenant → tree → rows → sorted blobs → event counter lock order, and qualify the
provider-specific Expert allowlist before any export. The complete privacy
allowlist, bounds and conditional rules are in the frozen source.

Input evidence follows accepted → queued → write_started → write_acknowledged →
turn_started → turn_completed. Applied control and exit_confirmed require real
correlated native/owned-exit evidence. Uncertain writes are never automatically
reinjected. Existing chat/inbox receipt enums keep their current meanings.
Final-drain claims are exact, expiring and publication-only; command and
final-drain claims cannot launch or resume. Native root reservations have null
`run_id`; workers use their existing leaf run reservation without double-counting.

Owned sessions use the workspace owner's own vendor accounts. Credential pooling,
sharing, intermediate claude.ai tokens, automatic account hopping and vendor
binary modification are prohibited. Hosted tenant enablement requires BYO API
keys or vendor-approved flows and its separate terms/OS/grant qualification.
No new secrets, permissions, tables or migrations are part of C0.

Fix round 4 includes the coordinator's explicit R0 ownership grant for the
22 native routes. Their outer declarations reuse `harness.read`,
`harness.control`, `harness.worker`, `chat.send` / `inbox.send`, and
`questions.decide`. Enabled handlers still owe current target visibility,
mode-specific permissions, controller/owner proofs, bounded actor grants and
transaction-fenced checks. No role or key ceiling is widened.

Authentication applies the native header, no-store policy and `NativeError`
envelope to 401/403 responses for exact native patterns. Existing authentication,
authorization and pairing decisions are preserved. Person-only attach, takeover,
approval and end-control routes reject agent keys; Expert/terminal viewer access
also remains person-only. The five daemon claim, receipt, event, final and command
routes cross only the existing live pairing boundary; unrelated routes and
revoked pairing retain their denials.

The granted R0 rows are applied in this branch:

- `internal/authz/routes_native_sessions.go` and its `testdata/domains` fixture
  declare all 22 patterns using existing permissions. The real-mux inventory
  mounts the disabled module alongside the existing production modules.
- S2 (`agent-runtime`) owns `internal/nativecontract/**` and
  `internal/nativesessions/**` in `scripts/audit/slices.json`. Existing S1
  auth/cmd, reporter/API ownership and S9 TypeScript ownership cover the rest.
- The Go tier manifest registers the two native contract tests, the disabled
  route test and `internal/auth:TestNativeRoutesThroughProductionAuthentication`
  as ESSENTIAL. Both Go shard tables add unmeasured whole-package entries for
  `nativecontract` and `nativesessions` to shard 2. Auth's existing whole-package
  entries automatically include its new test.
- `reportercontract.Current` includes the `native-session` pin derived by
  `NativeSessionPin`; the established pin tool regenerates `pins.json`.
  JSON, SSE, 401, 403 and disabled 503 shapes are pinned at `native-session/1.0`;
  legacy versions and pins retain their previous values. The version and route
  header inventories include the new surface and all 22 routes.
- No web test/shard, DSAR, permission registry/label, built-in exclusion,
  key-ceiling fixture or shared-fence row changes: this round adds no UI,
  migration, permission, role grant or DB lock caller.

The production-chain regression uses real session cookies and agent keys with
live roles, the real tenant boundary and the server's route, authentication,
doctrine, event-position and transport middleware. All routes cover anonymous,
stale-cookie, authorized-person, forbidden-person, empty-key, delegated-agent
and paired-daemon requests; it also checks alternate input scopes, person-only
view denials, unchanged unrelated routes, zero body reads, zero session/run/event
side effects and pairing revocation. Browser validation is not applicable.

Validation results are recorded after the guarded remote checks. The affected
planner selects the full CI lane because C0 changes the OpenAPI contract; final
PR CI and the cross-family review remain coordinator-owned.
