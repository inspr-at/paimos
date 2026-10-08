# Attached owner notes (AEON-392, disabled)

The server contract adds separate, single-use messaging consent to protocol-2
attachments. Watching never grants sending. The independent helper prepares a
snapshot/pin-bound grant; only the computer owner's same-origin browser session
can approve it. Policy B additionally requires a fresh messaging nonce and a P-256 signature
verified against the browser-pinned pairing key. Its signed digest includes the
full attachment, owner, session, generation, daemon and hook tuple plus the
canonical local prompt. Watch signatures and boolean confirmations are refused;
activation consumes the messaging challenge atomically. Each
re-enablement gets a fresh message generation. Turning messages off preserves
the watch lease; message operations never renew that lease.

`AEON_ATTACHED_MESSAGES=true` also requires the deployment-owner qualification
`AEON_ATTACHED_MESSAGES_SINGLE_INSTANCE=true`. Both default off. This is an
operator attestation, not a distributed lock or automatically verified lease:
exactly one serving/sweeping Aeon process may use the database while enabled.
There must be no sibling replica, overlapping rolling restart, second service,
or separately running sweeper. Stop the old process before starting its
replacement; disable the feature if that invariant cannot be maintained.
The production hook-capability projection remains fail-closed until native qualification
work lands. `internal/attachedmsg/store.go` narrows every pairing capability
report through AEON-391's release-owned `hookcap.Project` ceiling. Tests
inject qualified fixtures; those fixtures are not production qualification.

An explicit `recipient_message_generation` always selects an attached note,
including when messaging is disabled or its attachment has ended. Such a send
is volatile or refused; it never falls back to durable chat. When messaging is
fully enabled, active attachments also enforce the policy when a caller omits
the generation or session. When disabled or unqualified, requests without a
message generation retain ordinary inbox routing, including live watched
attachments. The final durable write rechecks attachment status under the same
fence as activation while messaging is enabled, closing the lookup gap.
Session identity, project, principal, harness, host, ticket and active state are
validated under a shared session row lock retained through commit. That lock
is acquired before attachment and grant row locks; acceptance and broker
validation use that same protected predicate.
Both inbox send APIs (therefore CLI and MCP) use one attached-recipient policy.
A person needs the exact computer ownership, live project permissions, current
grant and `recipient_message_generation`. Agents can create only bounded
notification metadata; their labels and sender-session fields confer no owner
authority. Attached v1 rejects steering, action requests and reply/thread modes.
The capability route is `/api/agent-pairing/attach/{requestId}/messages`; its
`approve` and `revoke` subroutes are owner-only. Ordinary inbox reads, streams,
acks, adapter claims and managed drains cannot consume these rows.

Note bodies live only in the shared inbox payload service. Both message tables
store “Attached-session note; text not retained”, with database constraints.
Idempotency comparison uses a keyed in-memory digest; after payload loss an old
key returns existing metadata and never recreates a note. Events are private
metadata, and failure settlement does not copy a note to a coordinator or a
notice. Request/SQL logging never receives the raw body. A five-minute deadline,
a live attach lease, a 4 KiB body limit, an 8,000-character escaped frame limit,
a session burst of three (one token per ten seconds), 30 sends/minute/computer,
five pending notes/session, and 1 MiB/tenant plus 16 MiB/process payload ceilings
are enforced server-side. Notifications use separate session/computer rate
counters (the same burst/refill and 30/minute limits) and separate in-memory
metadata limits (5/session, 1,024/tenant, 16,384/process). Their text is cleared
before reservation. They never consume any owner-note rate or memory capacity.
Quota failures return 429 with `Retry-After`.

S2-3 must use the exported exact `Binding`/`Offer` contract, recheck
`ValidateGrant` under the pairing fence, commit its one attempt, then use the
single-use payload `Take`; it must never replay a body. Pass
`Take(binding, grantID, messageID, TakeTransaction{Context: ctx, Tx: tx})` in the
transaction that holds the pairing fence and revalidates the grant. `Take`
locks and re-reads the exact message tuple, accepting only queued/offered rows
before their deadline; a sibling's `content_lost` settlement denies release even
while the old process retains RAM. Missing transaction or any SQL error fails
closed. Existing three-argument calls still compile but return no body. Never
expose the result before that transaction commits; a rollback loses the taken
body rather than replaying it. Lock order follows current main: tenant → pairing
fence → tree, then compat advisory lock, session, attachment/grant, message,
delivery, receipt, event counter. Publication follows the metadata commit;
rollback discards the reservation. The service epoch binds the owning process.
Restart/disabled/expired/revoked notes settle without automatic retry. Status
adds `attached` evidence and never treats a legacy fetch/ack as model reading.
Negative controls run with `nix develop -c python3 scripts/check-attached-policy-mutations.py`
and `AEON_TEST_DATABASE_URL` pointing at a disposable test database; the script
restores exact source bytes after each owner/tuple/digest/replay mutation.
The local credential-free hook and offer/receipt broker are separate dependent
packages; this change does not activate delivery or managed controls.

Package validation (2026-10-03): the approved remote `go test -count=1 ./...`
run passed 123 packages; its only failure was a new unquoted OpenAPI description.
After correction, the payload, inbox and reporter-contract packages passed.
The final attachment run passed all 16 tests and killed all 15 security
mutations at their intended assertions, including an actual durable-body leak
when the disabled-note guard was removed. Exact source bytes were restored.
The migration guard passed against `v261003065316.0.0`: 230 unique migration
numbers, immutable published files, and expand-safe additions 1210–1212 on the original dependency branch.
AEON-660 rebases those unpublished additions to reserved migrations 1252–1254.

Fix-round validation (2026-10-03): all 18 attachment tests and the inbox package
passed locally. Barrier tests prove session row protection through commit and
revalidation after concurrent archive/binding changes for acceptance and
`ValidateGrant`. Removing the fixes on `424af4e9` makes the session-lock and
disabled-routing regressions fail at their intended assertions. Web build,
656 Node tests, 741 Vitest tests and all 17 `session-messages.spec.ts` Chromium
checks passed; the browser checks used one supervised worker on macOS.
The remote test host was off limits; Linux browser qualification and the
consolidated release review remain with the coordinator.
