# Native session requests and control

AEON-1304 supplies the P2f interaction services behind the existing, disabled
native-session contract. I2 owns server/agentd/panel mounting and command claims.
No provider, automatic session start or permission mode is enabled by this slice.
The request preview needs Opus design before G_UI release.

`interaction.New` requires a current-authority adapter. Every operation enters
the tenant, tree and pairing fence, locks the exact binding, checks live target
permissions with `RequireTx`, and verifies the immutable owner/tool/settings
grant. The adapter must check the current native turn, paired worker proof,
specific watch consent and qualification; public correlation IDs confer no
authority. It performs no network/native I/O or event append.

An explicit control attach creates one fresh holder epoch. A live holder blocks
another tab, including the same person's tab. Read-only attach and reconnect
cannot acquire or renew control. The server derives a 30-second TTL, bounded by
the original grant; the client heartbeats every five seconds and subtracts
monotonic request time from `expires_in_ms`. Renewal requires the exact viewer,
actor, binding, epoch and transport-header capability. Capabilities are derived
in process memory; SQL stores only their digests. Detach affects viewing/control
only. Public snapshots keep quota facts owner-only and never advertise local
TUI takeover to a web caller. End control requires the owner or project
administrator and the current revision/epoch. Neither operation changes writer
lifetime or root occupancy.
`CheckControlTx` must run again under the final command-claim fence. The stream
owner enforces its separate global viewer cap and emits lease expiry/end-control
notifications from these authoritative transitions.

The paired runtime routes privacy-filtered request metadata through the existing
`questions.AskTx` Decision Desk service. Its context binds the full runtime,
request, turn and effect. Only `once` human decisions can resolve native requests;
inbox messages, reused/Always answers and agents cannot approve. Tool requests
support only `allow_once`/`deny`; questions support an exact answer/deny. No tool
arguments or answer bodies enter the new SQL sidecars. Existing desk storage owns
the authorized summary/answer. Request creation updates only already-held native
rows after the desk event and acquires no new locks after its event counter.

The server-owned request policy sets the step-up requirement; producer metadata
cannot lower it. Protected allows require an explicit `StepUp.VerifyTx` adapter that consumes the
existing exact human/phone proof in the final transaction. Without that adapter
they return 428; no approval/permission-mode expansion can bypass it. I2 must
wire only a qualified adapter to the existing human controls, never treat an
authorization UUID or a message as proof. Permission mode/full access still
requires its separate existing person-controlled flow.

Server resolution is acceptance only. I2/P3 must persist/claim the bounded
decision command under the original person's current authority and report actual
native evidence; a replay acknowledgement cannot execute a callback. The live
`nativeinteraction.Broker` resolves its buffered result once under the host's
owned-writer fence and verifies the current turn/grant via its mandatory authority
adapter. The host drives `Broker.Expire` from its lifecycle tick so waiting
callbacks receive a denial at expiry. Registration subtracts transit time from the server TTL. Binding
activation, close or restart expires callbacks; nothing reconstructs an allow.
Tombstones prevent re-registering settled callback IDs within the bounded runtime.

Migration 1348 adds tenant-isolated controller, viewer and interaction metadata
with DSAR classifications. Server construction creates a fresh boot ID, so old
leases and callbacks cannot be renewed or resolved after an API restart. The
native runtime may remain running; an explicit fresh request/attach is required.
No account credential, local vendor history, tool body or plaintext capability
is stored or shared, and the vendor binary remains unchanged.

The `NativeRequests` preview shows only the current binding's bounded request
series and opens the existing Decision Desk. It captures actor, binding,
revision, request and effect; aborts on identity/permission changes; and discards
late or mismatched completions. Controls stay above content that grows downward;
the phone pane scrolls content inside its parent frame. The preview never decides
a request itself. I2 supplies a monotonic time captured before fetching TTLs and
an authenticated identity-bound desk opener.

Verification targets the stale-authority risk: observed competing DB locks for
concurrent attach, injected TTL clocks, exact-once callbacks, wrong effects,
human-versus-agent authority, protected allows, exact answers and restart
expiration. The browser spec measures controls at 390/1024/1440 in light/dark,
uses long German labels, and rejects stale person/runtime and response identities.
All browser artifacts use `testInfo.outputPath`; no live model calls are needed.

Worker acceptance evidence (2026-10-13):

- PASS — one bounded holder epoch, concurrent attach serialization, explicit
  control, read-only reconnect, final claim revocation/runtime checks, renewal
  expiry and detach preserving the running session: guarded remote
  `TestControllerEpochFencesConcurrentAttach`.
- PASS — existing Decision Desk routing with exact request/turn/effect/runtime
  metadata and server-owned step-up requirements: guarded remote
  `TestDeskDecisionExactEffectExpiryAndRevocation`, including a complete
  2048-byte summary retained in the existing findings slot.
- PASS — agent/message rejection, wrong-effect rejection, revoked grants and
  one callback delivery: the desk test plus guarded remote
  `TestCallbackExactOnceAndRuntimeFence`.
- PASS — only allow-once/deny/exact-answer, protected allows closed without
  human verification, and callbacks/leases expiring on restart: the desk test,
  `TestCallbackExpiryRevocationAndRestart` and
  `TestServerRestartExpiresInteractions`.
- PASS — injected-clock/observed-lock concurrency and seven remote browser
  cases for identity and control stability at 390/1024/1440 in light/dark.
  Screenshots live under `web/test-results/AEON-1304/`; the final worker status
  identifies the commit used for the refreshed browser evidence.

Strict Go and web tier inventories passed. The guarded static check passed all
42 checks with zero optional skips, including typecheck, lint, ownership,
manifests and migration policy. PR CI remains authoritative for merging.
The initial two desk-fixture failures were corrected by preserving the acting
key creator identity; the affected server package then passed all three tests.
No test was loosened or skipped.

Fix round 2 corrected the injected fixture clock to PostgreSQL's microsecond
precision. A deterministic submicrosecond sample reproduced the reported
reconnect failure twice against `d6ff2ff3`'s interaction logic: SQL discarded
789 nanoseconds, making an unchanged lease report 24999 rather than 25000 ms.
The existing concurrency test retains its exact TTL assertions and additionally
checks the original viewer and unchanged viewer/controller expiry instants on
reconnect. Production lease, permission and expiry behavior is unchanged.
After correction, all five behavior tests across both affected Go packages passed
twice through the guarded remote runner, then passed again after merging current
main at `85c5ef4ad`. Guarded `ci-static --merge-main` on that merged commit passed
all 42 checks with zero skips, including strict Go/web tiers and web-shard unit
regressions. The canonical tier merge preserved all five ESSENTIAL Go rows and
seven browser rows; this round adds no test identities or registry rows.
The fresh browser spec could not start: two attempts stopped at other live
browser-suite locks, and the final attempt hit the remote capacity guard (exit
3). The component and spec are unchanged by this fix; fresh Linux Chromium
evidence remains with authoritative PR CI. No foreign process was touched and
no local browser fallback was used.

R0 additions are exactly five ESSENTIAL Go rows, seven ESSENTIAL browser rows,
one `stable-controls` spec entry (20 seconds is an unmeasured scheduling estimate),
three DSAR table/column entries, and
`nativesessions/interaction/store.go` in `agentpairing.Lock`'s caller inventory.
Existing source ownership globs already cover all new sources. No permission,
contract, dependency, version or shared-runtime wiring was changed.

Remaining coordinator gates are I2 integration, a qualified existing human/phone
step-up adapter, G_UI (needs Opus design), authoritative PR CI and the external
cross-family review. They are not worker release approvals. No origin push,
deployment, live cutover or model review was performed.
