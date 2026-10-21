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
or supply the authenticated person's ID. Cookies and control capabilities are
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
changed in this delivery. A terminal attach also requires its real owner export
consent; person authentication alone cannot provide it.

`TestNativePersonBootstrapAttachTakeHandBack` uses the real protected socket
and kernel-peer wrapper, real public module and both CLI wire implementations
with a fake qualified store/host. It covers person attach/detach, control
renewal, take, fresh-epoch hand-back, stale-epoch rejection, agent and foreign
session refusal, web/local projection separation and denial at the final
server write before host execution. It proves bootstrap/control transport,
not real-provider PTY qualification or interactive terminal I/O. The core's
SQL transition test separately proves its existing audit/ownership semantics.
