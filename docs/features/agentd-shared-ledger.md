# Two paired daemons on one Mac

AEON-1056 implements the S5 local ledger from the accepted account-matrix
concept. A named pairing keeps its own server authority, working folder, state,
service label and logs. Tickets, tenant identifiers, account identifiers and
credentials are never written to the shared ledger or sent to a peer's server.

```sh
aeon-agentd pair --url https://work.example.test --tenant TENANT --workspace /absolute/working/folder --instance pma
aeon-agentd ledger status
aeon-agentd uninstall --instance pma
```

The `pma` root is `~/Library/Application Support/aeon/paired-pma`, its service is
`cm.aeon.agentd.pma`, and its logs are in `~/Library/Logs/aeon-agentd/pma/`. The
existing default pairing retains its root and label. A duplicate canonical
origin and tenant is refused even when the working folders differ. Instance
names are local names, never credentials or server selectors.

All installed daemon definitions, including stopped services, must reference a
binary advertising `ledger-v1`. Loaded launchd programs are checked separately.
Both servers must advertise `ledger-v1` through a fresh lifecycle response.
The pairing command refuses shared operation before changing a service or its
receipt if a capability or ownership check fails. Each existing daemon must be
running so it can perform its own authenticated, fenced handover. Nix/Home
Manager installations remain configuration-owned and refuse this imperative
multi-instance transition until their owning module supports instances.

The owner holds its dispatch mutex across import and server enrolment. Every
restart re-imports before work admission. Queued reads, Route and claims carry
the enrolled generation header. Tenant ledger mode is monotonic; the last
remaining daemon continues to use the ledger after a peer is uninstalled.

The private ledger directory contains a permanent `ledger.lock`, atomically
replaced `ledger.json`, and independent member tombstones with a durable digest
catalog. Atomic writes fsync the file and directory. The generation is a fresh
random UUID. Local HMAC fingerprints use that private generation as a
consistency salt; they require no new authentication keys and no authority from
another server. Unverified logins are `unknown` and serialize their harness
across instances. A missing local account binding conservatively conflicts
with every harness.

Each dispatch group counts once against the lowest positive machine cap. Its
pending holds cover the candidate subset pruned under the same lock. A pinned
account waits when occupied. The owner writes the exact subset and group ID in
its private `route_pending` journal before Route. Confirmed routing narrows the
hold to the selected login. The ledger records `launching` before the durable
possible-fork boundary, then `running` with process provenance. Uncertain Start
errors remain occupied. A generic Route conflict does not prove an attempt is
over; terminal run state must independently confirm that result.

Peers never release another member's groups. The owner replays uncertain
attempts with the same group and subset; unreachable servers retain occupancy.
Owner import removes pre-Route groups without private attempt evidence. Only
observed exit or proof that both the recorded root and its verified process
group are absent permits releasing possible-fork evidence. Unknown PID/group
provenance remains occupied. No process is adopted or signalled during recovery.

```sh
aeon-agentd ledger rebuild
```

A rebuild retains the damaged data file for inspection, chooses a fresh
generation and blocks admission until every durable member has imported and
enrolled. A removed plist does not remove its member. Status identifies a
`member without service`; reinstall its service or uninstall through that
member's own root and reconciliation. A missing/unreadable member or catalog
blocks admission and rebuild. An unpublished PID after a possible fork is
unverifiable and remains occupied. Never delete tombstones to bypass a stall.

`peer_running` telemetry is advisory and is sent only after an explicit
`peer-running-v1` advertisement; current servers without that advertisement
receive no extra capacity field. The local ledger remains the admission gate.

Do not run agentd by hand; use the installed services. Classic key-file daemons
get no work in ledger-mode tenants. Manual `psql` needs the capability flag
(`SET aeon.account_use_capable = 'on'`). The accepted remaining risk is a daemon paired to a different
server or tenant without ledger mode: it can still book a vendor login twice.
