# Per-run local resources

AEON-1089 provides an opt-in allocator for routine workspace consumers (S11),
dispatch (S13) and lead runtime (S14). Existing agentd runs, test runners and
release behavior stay unchanged. No routine permission or admission is implied
by a local allocation.

`internal/runisolation.Acquire(ctx, root, Owner)` reserves two loopback TCP ports
(application, Postgres), an SQL-safe database name and a private temp directory.
All workers using one host/account use the same private registry root outside
their worktrees. `Owner` binds tenant, selected account, host and a unique
assignment/attempt ID. A replay of any retained attempt refuses allocation;
recovery preserves its evidence and uses a newly admitted attempt only after
the prior process exit is confirmed. Cross-process file locking serializes
allocation; actual sockets reserve ports until fixture handoff. Orphaned and
unknown reservations remain excluded. The registry holds at most 1,024 retained
attempts; reaching its bound requires explicit operator archival, with no
automatic deletion.

`Lease.Environment()` supplies `AEON_RUN_DATABASE`, `AEON_RUN_APP_PORT`,
`AEON_RUN_POSTGRES_PORT`, `AEON_RUN_TEMP_DIR`, `AEON_RUN_ID` and the temporary
directory variables. Consumers replace inherited entries with these values.
Database names are allocations, not a request to create or drop a database.
Use the selected account's existing database connection authority; never embed
connection credentials in the registry or derive account choice from a prompt.
The run must use its allocated database and temp directory for fixture data.

`Control` starts a separate guardian through the caller's executable and its
explicit internal `Guard` entry point. S11 can embed that entry point in agentd;
the optional `cmd/aeon-isolation` helper already provides it. The controller's
open stdin pipe represents its lifetime. Cancellation or controller exit,
including SIGKILL, closes the pipe. The guardian cancels the foreground fixture
command and cleans its newly owned process group through `ownedprocess` before
recording completion and releasing the allocation. A root command that exits
also causes remaining group members to be stopped. Ordinary command failures
and uncertain cleanup return failure.

Fixtures must remain foreground within that owned group: start Postgres with
`postgres -D "$AEON_RUN_TEMP_DIR/pg" -p "$AEON_RUN_POSTGRES_PORT"`, rather than
a daemonizing launcher; use an equivalent foreground mode for test servers.
Initialize that run's cluster separately and use its allocated database name.
Do not attach to a shared server or change an unrelated cluster. The helper
does not manage containers, detached processes, or processes that escape their
group. A fixture must fail on a port bind error rather than attach to an
existing service. During handoff the socket closes before the fixture binds;
the registry retains the port lease, preventing reuse by cooperating workers.
Unrelated software can still race that bind, so the consumer must check it.

Example after building the optional helper:

```sh
aeon-isolation run --root /approved/private/registry \
  --tenant tenant-id --account selected-account-id --host host-id \
  --run assignment-attempt-id -- foreground-fixture-command
aeon-isolation check --root /approved/private/registry
```

`Check(ctx, root)` and the `check` command report each allocation's owning run,
ports, database, temp directory, fixture root PID, state and `orphan` flag. They
do not create directories, delete files, signal processes or read process
arguments/environments. A nonterminal record without a held ownership lock is
an orphan. If the guardian itself or the host crashes, automatic cleanup cannot
be confirmed: retained evidence remains visible and its resources are not
reallocated. Saved PIDs are informational and never authorize cleanup. Operator
reconciliation must establish exact process identity and exit before archival.
The registry retains temporary files and receipts after confirmed completion.
Checks take the existing allocator lock before scanning, so an in-progress
allocation is never mistaken for abandoned evidence. A failed allocation removes
only the directory it just created, before returning any lease. A process crash
before its record is saved leaves an `incomplete` report identified by allocation
ID, without invented owner or resource values. Such attempts count toward the
1,024 bound, refuse replay and retain their files; other attempts can continue.
An incomplete attempt without a held lease is reported as an orphan, including
when the crash occurred before the lease lock was created.
Malformed or oversized evidence and capped scans return an error;
partial reports are never claimed complete.

The behavior checks cover simultaneous allocation, socket reservation, retained
attempt rejection, orphan ownership, preserved fixture data, normal completion,
cancellation and a real controller crash. FIFO barriers and witness descriptors
prove descendant exit and that an unrelated fixture survives. No migration,
public API change, global launch switch or version change is required.

Validation for AEON-1089: remote `go test -race ./internal/runisolation
./internal/ownedprocess ./cmd/aeon-isolation` passed (the helper is compiled;
behavior lives in the allocator package). Ten repeated allocation race checks
also passed after applying the existing Darwin file-creation guard. The locked
remote `node scripts/ci-static.mjs --merge-main` exited 0 with all 42 checks
passed and no optional skips; the ownership audit passed all 33 checks.
