# Daemon ledger boundary

The server advertises `ledger-v1` in `server_capabilities` on the pairing guide
and every lifecycle view. A shared ledger requires that advertisement from each
member's server. Missing capability means shared mode is refused.

A connected, redeemed computer calls `POST /api/agent-pairing/self/ledger` with
its random lowercase UUID `generation` using its existing runtime authority
(`run.claim`). It must first register as a local member and import its occupancy
under its dispatch mutex. The server atomically stores the generation, activates
tenant ledger mode and the account-use rollback floor, and appends
`agent_pairing.ledger_enrolled`. Identical retries are idempotent.

Once activated, every computer in the tenant needs an enrolled generation and
the matching `X-Aeon-Ledger-Generation` header. Without them, queued work is empty
and Route and claim return 409 `ledger_enrollment_required`, including replayed
routes and verification runs. Classic key-file daemons have no paired computer
and are refused. Reported agent versions are diagnostic only. Telemetry may
continue to settle already-owned work through its existing authorization.

Fresh, replacement and Add harness approvals in ledger mode require the
`ledger-v1` device capability alongside `managed_runs`. Add harness invalidates
the computer's old enrollment; a crash before import/enrollment leaves it closed.
A rebuild replaces the generation and stale requests fail. Ledger mode cannot
be reset by updating its timestamp.

Existing computers see `ledger_mode` at their next lifecycle read and can enroll
without pairing again. The client supports the handover request. Automatic local
membership, occupancy import and enrollment on daemon startup belong to S5;
this server slice does not enable shared dispatch before that protocol exists.
