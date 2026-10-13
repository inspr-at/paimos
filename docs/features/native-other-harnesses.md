<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Native ACP and Pi worker profiles

P5b (AEON-1308) supplies `nativecontract.Launcher` and `NativeTransport`
extensions in `internal/nativeproviders/acp` and `internal/nativeproviders/pi`.
Cursor, Grok, Gemini and OpenCode use their existing ACP process shape; Pi uses
the existing JSONL RPC shape. I2 owns production wiring into agentd. These
packages do not start a process themselves, add a library, change the frozen
C0 contract, or enable an account/provider in production.

The coordinator supplies a local `acp.Qualification` only after
`G_PROVIDER_PINS`: exact binary path, SHA-256, version, protocol (`acp/1` or
`pi-rpc/1`) and the approved tool/settings policy digests. There is no built-in
approved installation. The qualification digest also covers the codec revision,
so changing a binary, policy or codec changes its capability identity. Missing
pins and unknown providers produce closed matrix rows and visible allowlisted
errors. The tests use explicitly synthetic `fixture-<provider>-1` pins, never
real installations or subscriptions. Fixture success is protocol evidence;
real OS, account and authority qualification remains the coordinator's gate.

`Matrix(qualification)` reports every exact pin independently:

| Operation | Cursor ACP | Grok ACP | Gemini ACP | OpenCode ACP | Pi RPC |
| --- | --- | --- | --- | --- | --- |
| Live process persistence | Yes | Yes | Yes | Yes | Yes |
| Disk history / successor resume | Unavailable | Unavailable | Unavailable | Unavailable | Unavailable |
| Input during a turn | Queue at boundary | Queue at boundary | Queue at boundary | Queue at boundary | Native next-step / follow-up |
| Correlated input completion | Prompt response | Prompt response | Prompt response | Prompt response | Unavailable |
| Public chat composer | Qualified prompt input | Qualified prompt input | Qualified prompt input | Qualified prompt input | Unavailable |
| Explicit publishable final | Unavailable | Unavailable | Unavailable | Unavailable | Unavailable |
| Interrupt exact active turn | Cancel + completion | Unavailable | Cancel + completion | Cancel + completion | Unavailable |
| Approval / question decision | Unavailable | Unavailable | Unavailable | Unavailable | Unavailable |
| Graceful exit | Owner proof | Owner proof | Owner proof | Owner proof | Owner proof |
| Force exit / takeover / terminal | Unavailable | Unavailable | Unavailable | Unavailable | Unavailable |
| Native quota | Unavailable | Unavailable | Unavailable | Unavailable | Unavailable |
| Confined/routine/review qualification | Unchanged | Unchanged | Unchanged | Unchanged | Unchanged |

All supported rows above require the exact approved local pin and owner
implementation. ACP's correlated `session/prompt` result with `end_turn`
produces `turn_completed`, not an invented assistant final. Delta chunks never
become durable finals. ACP `now` refuses while busy; `after_turn` waits for the
current correlated response. The queue is bounded to 32 pending calls and each
input to 64 KiB. Expiry, current identity and policy are rechecked after waiting.
Malformed responses, unknown stop reasons and ambiguous writes hold further
inputs instead of opening another turn. Cancel writes are serialized against
prompt writes and turn transitions so an old interrupt cannot cancel a successor.
An interrupt reports `applied` only when the matching prompt response confirms
`stopReason: cancelled`. If normal `end_turn` completion wins after the cancel
write, the input still gets `turn_completed` and the interrupt refuses with
`input_outcome_unconfirmed`. A completed turn cannot accept another interrupt.
The [ACP prompt lifecycle](https://agentclientprotocol.com/protocol/v1/prompt-turn)
defines the streamed updates and correlated prompt completion used here.

Pi `prompt` command IDs prove only `write_acknowledged`. The native `steer` and
`followUp` streaming behaviors keep their own semantics; there is no inferred
link from the next assistant output to a particular input. C0's existing
`Input`/`ChatSupported` pair stays false and `Send` refuses the public composer.
The optional local `NextStep` method retains bounded worker-control steering
without claiming C0 chat delivery. Abort/clear-queue and resume
remain unavailable without the separate held-queue reconciliation. The
[official Pi RPC documentation](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/rpc.md)
distinguishes command acknowledgements from session events; the release gate
must verify the schema of the exact installed version, not assume current docs
qualify it.

Native workers require a non-null existing run binding, structured mode, a
current supervised/manual grant, disabled disk history, matching policy digests
and a resolved account home outside the approved workspace. Only a small,
credential-free environment allowlist is accepted, including the required
provider account-directory variable. Pi suppresses extensions, skills, prompt
templates and themes and keeps `--no-session`; no approval bypass flags are
added. Gemini requires its approved system-settings overlay and supported
model/thinking budget. OpenCode confirms the effective model and effort from
native config-option responses. Cursor/Grok/Gemini confirm the native session's
model; the owner separately verifies the enrolled account and native tool
boundary. Cursor's existing inherited-tool safety constraints remain in force;
its [ACP documentation](https://cursor.com/docs/cli/acp) describes native MCP and
permission interactions, not a no-tools authority guarantee.

I2 adapts `acp.OpenWire`/`acp.Wire` to the existing owned transport. The owner
must validate the actual binary/account/workspace/policy before fork and
recheck current grant, account and immutable policy **inside every final
serialized Request/Notify write**. It enforces the grant's runtime/token/tool
ceilings, preserves account occupancy and never substitutes another account.
`Observe` runs in reader order before later request responses are released.
The optional `written` callback runs once after the complete request write and
before its events; it is a serialization barrier, never delivery evidence.
The codec passes defensive copies of the resolved profile. No credentials are
read or copied and no arbitrary SDK/runtime configuration is accepted.
`Open` requires a null `InitialInput`: I2 obtains the handle and starts consuming
`Events()` before sending the authorized task through ACP `Send` or Pi's local
`NextStep`. A populated
initial input is refused before fork, avoiding a blocked open or unseen stream.

Close is graceful only. Success requires the original process digest, observed
root exit and absent process group. Uncertain exit keeps the handle/occupancy;
even a failed open after a possible fork returns its transport for reconciliation.
Force-stop authority belongs to the existing person-authorized owner path.
After confirmed exit the event channel closes. Live output contains only
bounded native assistant text, allowlisted tool status and lifecycle state;
reasoning, tool arguments/results, native private IDs, quota guesses and raw
finals are omitted. Raw frames are bounded to 1 MiB and depth 16 before decode.
Expert and terminal export capabilities remain false.

The named qualification matrix tests cover all five synthetic pins, unsupported
resume/chat/approvals, privacy filtering, immutable account/policy ceilings,
correlated ACP responses, clean-boundary queueing, exact-turn interruption and
honest exit evidence. Tests use barriers and injected clocks; timeout guards
only detect hangs. The targeted packages are `./internal/nativeproviders/acp/...`
and `./internal/nativeproviders/pi/...`, run through the coordinator's approved
remote-test lane. The only shared metadata additions are these tests' ESSENTIAL
rows in `scripts/ci/go-test-tiers.json`; source ownership already matches R0's
`internal/native*/**` coverage. No routes, permissions, migrations, dependency
versions or release versions change. Production enablement, cross-family
review and release remain coordinator-owned.

Validation on 2026-10-13, implementation commit `62661b58e`:

- PASS — ACP/Pi worker codecs implement the frozen Launcher/NativeTransport
  ports using the existing owner seam, with no dependency or contract changes.
- PASS — exact-pin matrix separates persistence/resume, busy input, approvals,
  final correlation, graceful/force exit and quota for all five fixture providers.
- PASS — ACP waits for clean turn completion; Pi local next-step steering
  produces only a command acknowledgement and leaves C0 input/chat closed.
- PASS — unsupported operations, permission requests and extension questions
  refuse visibly; no automatic approvals or free-text delivery evidence.
- PASS — all eight ESSENTIAL tests passed via remote `go test -race` for both
  packages, including privacy, wrong-identity/policy and confined-ceiling cases.

Guarded `ci-static-locked.sh` checked committed HEAD plus current main and
exited 0: 42 checks passed, no optional skips, including Go/web tier checks,
Go vet, web typecheck/lint, ownership coverage and contract/migration guards.
The earlier local web-tier attempt had no dependencies; its guarded install
was refused by the local host load guard. The successful remote static run
supplied the web validation without bypassing that guard. Authoritative PR CI,
cross-family review, real provider-pin enablement and production wiring remain
coordinator-owned. No origin push or deployment was performed.
