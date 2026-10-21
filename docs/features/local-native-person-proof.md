# Local native person authentication

`aeon session attach OWNED_ID --project KEY --session-cookie-file FILE` and
`aeon session take OWNED_ID --project KEY --session-cookie-file FILE
[--hand-back]` use an existing person login session. The attachment alias
`aeon attach --session` accepts the same attach flags. The file contains the
existing `aeon_session` value; no new key or login session is minted. Agent
attribution and API-key clients are refused before session or socket work.
URL and pairing-root lookup do not load or migrate an agent key.

The local request retains the protected socket bearer and carries the person
cookie separately. The existing agentd wrapper verifies the bearer and the
current kernel peer. `nativeattach.PersonBridge` then verifies the cookie
against the daemon's configured instance and tenant, using a bounded,
redirect-refusing request to `/api/me`. The caller cannot select that instance
or supply the authenticated person's ID. The bridge also binds the target to
the owning supervisor's locally observed daemon and current generation before
any server mutation. Cookies and control capabilities are
invocation/request scoped, excluded from DTOs and redacted in diagnostics.
Neither socket possession nor the paired agent becomes person authority.

Attach, renew and detach use the existing server interaction store. Control
renewal carries its existing capability privately. Input, interrupt, stop,
take and hand-back first use the existing public person mutation, then the
owning core host claims the accepted operation. Its final transactions retain
current permission, controller, consent, revision and epoch checks. Actual
runtime changes and their audit events remain the core's responsibility.
The bridge validates response identity and preserves a transition's original
request generation; it never emits another mode-change audit event.

The optional `X-Aeon-Native-Local-Person: 1` header selects the CLI projection
only for a keyless authenticated person. It conveys no authority. Ordinary web
reads keep their existing take projection; agents and keys cannot promote
that selector into person or terminal authority. The integrating store retains
only capabilities from its current consent/runtime qualification. Response
schemas, schema pins and route identities are unchanged.

## Remaining production dependencies

This is a partial delivery of AEON-1626, not completed interactive acceptance.
The production composition still has no qualified PTY launcher/qualification
adapter, advertises `EventsSupported: false`, and supplies no qualified watch
export consent. `NativeHost.PTYInput` and `Resize` still return `ErrUnsupported`;
its native receipts/history methods are also stubs. The bridge deliberately
refuses local stream/history/receipts and PTY input/resize without their current
viewer/write authority. These paths must not be enabled by inventing consent,
a provider qualification or a person identity from the socket.

Finishing interactive attach and take requires the owning-host integration in
`internal/agentd/native_session.go` and `internal/agentd/native_control.go`, plus
qualified terminal, stream/export and consent adapters in the production
composition. AEON-1626's brief excludes `internal/agentd/**` and the core's
store/consent implementation. None of those files or qualification flags are
changed in this delivery. The public interaction integration adds one
`control_claim` authorization hook inside the existing controller-acquisition
transaction in `internal/nativesessions/interaction/leases.go`; it reuses the
core's person/owner/project-admin check. A terminal attach also requires its
real owner export consent; person authentication alone cannot provide it.

`TestNativePersonBootstrapAttachTakeHandBack` uses the real protected socket
and kernel-peer wrapper, real public module and both CLI wire implementations
with a fake qualified store/host. It covers person attach/detach, control
renewal, take, fresh-epoch hand-back, stale-epoch rejection, agent and foreign
session refusal, web/local projection separation and denial at the final
server write before host execution. It proves bootstrap/control transport,
not real-provider PTY qualification or interactive terminal I/O. The core's
SQL transition test separately proves its existing audit/ownership semantics.

## Validation

The guarded runner refused the primary host as YELLOW and used its approved
secondary test lane.
The protected-socket scenario passed on `81c2e3443`; targeted race checks of
that scenario, the native CLI commands, the unchanged independent-person
socket anchor and both SQL control scenarios passed on `5c5e2323f`.
The full native attach, PTY, interaction and public-module packages passed
with `-race -count=1` on `90f3e77fe`. This includes the PTY socket's person,
viewer and generation regression. Removing just the independent person-cookie
forwarding on the isolated negative-control branch (`4a49b5d8a`) made the
protected-socket scenario fail at its initial attach with `grant_missing`.
That negative change is absent from the work branch.

The full harness and reporter-contract packages passed on `e34a75bd2`; the
latter's response-schema pins remain unchanged. The full CLI run was red in
unchanged fixtures on the secondary lane: `TestRunHeartbeatLiveOwnerKeepsBeating`,
`TestRunHeartbeatLiveOwnerAgainstDatabase` (missing `/bin/sleep`),
`TestHarnessVersionProbeIsBoundedAndOptional`, and
`TestRulesCompareCLIReportsBlockerAndRefusesUnsafeIO`. The full agentd command
package was red in `TestRunningEnrollmentFencesTheCandidatesOwnHome`,
`TestRunningEnrollmentKeepsDefaultProfileRepair` (fixture launcher execution),
and `TestReleaseExistingDraftGuardFailsClosed` (missing `/bin/bash`). Those
assertions and fixtures are unchanged; no failure is skipped or loosened.

The ownership audit passed all 33 checks and canonical manifest/diff checks
passed. New ESSENTIAL rows are the socket bootstrap scenario, CLI person-session
refusals and the PTY socket person/generation case. Existing ownership globs
cover all new source files. Optional header additions are confined to the
native-session authored contract and regenerated through `api/generate.mjs`.
No new permission, route, response schema, migration or secret is introduced.
The static wrapper returned exit 3 because the primary test host is not GREEN; static
qualification remains incomplete and needs coordinator/PR CI evidence.
