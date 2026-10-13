# Native session stream (AEON-1268, P2a)

This package supplies transient transport for one agentd-owned native binding.
Production wiring remains disabled in C0; I1/I2 connect these ports to the
qualified supervisor, authenticated owner socket, Store and final outbox. No
vendor binary, account, login state, legacy chat route or live daemon is changed.

`internal/nativestream.LocalAdapter` mounts every frozen C0 local route on the
existing owner-only Unix-socket mux. It requires `X-Aeon-Session-Protocol:
aeon-native-session/1`, strict method-specific envelopes, matching nested request
IDs and exact runtime/binding selectors. `LocalAuthority.Authenticate` must use
the existing bearer **and kernel peer** proof. `WithAccess` must independently
resolve the current binding and validate the server-issued operation grant,
viewer/client, consent, expiry, controller capability and any effect/step-up.
The bounded envelope is available to that authorizer as untrusted selectors.
The adapter does not open a network listener or create/read a credential file.
Control writes require `X-Aeon-Attach-Capability`; streams require the attach
result's nonsecret `X-Aeon-Viewer-ID`. Proofs never enter URLs, logs or frames.
Detach calls only Host.Detach. Host retains the final native-write/ownership
recheck; no database transaction may span native I/O.

`Outbound.Observe` accepts ordered observations from one trusted native reader
with the exact runtime epoch. Structured, Expert, receipt, approval and quota
projections share gapless source positions in that binding/generation.
`Outbound.Pump` uses the existing paired-daemon/worker proof to publish bounded
C0 batches through `HTTPPublisher`; redirects are refused, responses bounded
and acknowledgements checked against the exact binding. A transient HTTP
acknowledgement is not input delivery, final persistence or process exit evidence.
Its OperationResult carries the real current operation receipt; the adapter
does not invent a new receipt operation or change the frozen C0 enum.

`stream.Relay.Register` takes a trusted current Store projection. Publishers
cannot create a binding. `Relay.Publish` requires a paired private publisher
fence and independently validates/filters every frame before an atomic RAM
append. `Relay` implements C0 FrameSource through a request-scoped authenticated
`Viewer` context. `HTTPHandler.Stream` provides the authorized public SSE read
handler for later mounting. Its first event is a permission-filtered snapshot.
Agents receive structured events only; terminal viewer URLs, Expert and terminal
capabilities are stripped from their snapshots, including workstation agents.
Each view remains pinned to its exact runtime and never follows a successor.

Every session has one shared replay ring: at most 64 frames and 256 KiB, expired
after 120 seconds without a publication. Readers keep positions and one
unbuffered delivery slot, without content queues per subscriber. The registry
holds at most 128 current sessions and 128 viewers, with at most eight viewers
per logical owned session, including viewers draining from a replaced runtime.
The outbound ring uses the same bounds. Publication batches have at most 16
frames and 256 KiB. Source ordering is strict, and an exact retained retry is
idempotent; changed/reordered frames are rejected.

`Last-Event-ID`/`after` is the frozen C0 `<generation>:<seq>` position. It is never
authority: authenticated viewer grants independently bind caller, tenant,
session, runtime and view. A stale/evicted position fails before SSE headers with
409. Eviction during a stream emits `gap` then `end`; an API outage emits a gap
for lost outbound positions before the retained frames. A gap accounts for the
missing source positions and cannot imply complete replay. `Relay.Page` and
`HTTPHandler.LivePage` expose only bounded transient pages, with `truncated`
and bounded `before` cursors. The owning durable events handler separately reads
authorized history; these RAM pages contain no final answers.

All frame and keepalive delivery runs inside the current access-change fence.
Rechecking before queueing is insufficient. The fence must remain held through
the actual channel send or HTTP Write/Flush so revocation cannot race delivery.
Writes have a three-second deadline; streams rotate after five minutes and
recheck access every fifteen seconds while idle. Missing deadline support fails
closed. Revocation closes the viewer and emits only a content-free `end`, never
a queued frame. Reader disconnect and stream rotation do not stop the runtime.

Expert projection parses at most 1 MiB, depth 16, and rejects duplicate keys.
Compiled Claude/Codex event/field allowlists preserve native envelope casing.
Only qualified assistant text blocks/deltas may carry text. Credential/config
fields, reasoning blocks, tool argument/result bodies, arbitrary commands,
environments and native final-result text are omitted. Native correlations become
domain-separated opaque references. Arbitrary native key names and supplied
redaction paths cannot leak through omission metadata. Unknown providers/events,
malformed shapes, more than 64 redactions or oversized exports yield an empty
`frame_json`, `omitted: true` and safe metadata. ACP/Pi bodies remain omitted until
their provider qualification adds a tested projection. Tool summaries never
export raw bodies through the structured path.

`ExportPolicy.Text` must connect the existing authorized output policy. Mandatory
credential-shaped text filtering additionally runs before and after that policy;
missing qualification omits Expert text and refuses structured text export.
Terminal frames require an explicit qualified screen-export policy, owner watch
consent, taken mode and the exact terminal runtime epoch. Login/auth PTYs remain
ineligible. This boundary does not claim filesystem isolation or terminal renderer
safety; the separately qualified read-only renderer owns escape/device-reply
handling. No terminal input or resize can come from a web viewer.

Only explicit `ProviderEvent.Final` values reach C0 `FinalSink.Persist`, with
exact binding and validated final IDs. Sink refusal/rejection/expiry is surfaced.
No token assembly, raw transcript import or final text enters live replay. P3a
owns final-drain authorization, durable retry, retention and current-permission
rechecks; this transport adds no durable store and preserves legacy chat receipts.

Acceptance verification uses fake ports only. The tests establish queued-frame
revocation and outbound/subscriber stalls with barriers; clocks control replay
expiry and keepalives, while timeouts only detect hangs. No real model account,
browser, review gate, release or live cutover is involved.

The opt-in `-args -native-negative-control` test flag reruns the retention
assertion with a temporary Go compiler overlay removing the pre-publication
expiry. It must fail specifically because expired content was retained. Fixtures
use `t.TempDir`; the real source and work branch remain valid, with no throwaway
commit or second worktree.

R0 handoff for this same PR:

- Assign `internal/nativestream/**` to audit slice S2 (`agent-runtime`). Existing
  `internal/nativesessions/**` already covers the server package.
- Register all following tests in `scripts/ci/go-test-tiers.json` as ESSENTIAL:
  `internal/nativestream`: `TestNativeExpertProjectionFiltersAndBounds`,
  `TestTransientRingReportsLossAndRejectsStaleReplay`,
  `TestLocalHostProtocolRequiresPeerGrantAndExactBinding`,
  `TestSSEFencesQueuedContentAndKeepalive`,
  `TestOutboundLossRemainsTransientAndFinalUsesSink`,
  `TestHTTPPublisherBoundsAndBindsAcknowledgement`.
- Register `internal/nativesessions/stream` tests as ESSENTIAL:
  `TestSlowSubscriberRevocationAndViewIsolation`,
  `TestLiveGapClosesAndBindingReplacementRefusesOldView`,
  `TestServerHTTPAuthenticatesViewerAndEmitsGapEnd`.
- There are no new public/local contract routes, permissions, migrations,
  database locks, DSAR columns, contract pins or web test/shard rows. I1/I2 own
  mounting the already-authored routes and adapting final publication receipts.
- Run the targeted packages through `remote-test.sh`, affected selection via
  `node scripts/test-tiers/affected.mjs --base origin/main`, and guarded
  `ci-static-locked.sh` after the serialized R0 registrations. Any refusal or
  incomplete result remains an explicit handoff; authoritative PR CI must pass.
