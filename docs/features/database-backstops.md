# Database backstops and work-kind counts

AEON-1230 adds PostgreSQL defaults for the application's login role in its
database: `statement_timeout = 30s`, `lock_timeout = 5s`, and
`idle_in_transaction_session_timeout = 30s`. `db.Open` installs only settings
whose source is PostgreSQL's default, then recycles startup connections so the
new login defaults apply. Explicit server, database, role and connection tuning
is preserved, including an explicitly disabled timeout. The application role
changes only its own database-specific defaults; it needs no additional role or
RLS privileges. These defaults also apply to dedicated listener connections.
Installation takes a transaction-scoped advisory lock per role and database,
within its ten-second deadline, so concurrent starts serialize catalogue writes
before either startup reaches the migration lock.

SSE listeners wait outside transactions; the idle transaction timeout does not
close them. Chat and delivery history commit authentication/read transactions
before socket or vendor I/O. The shared pool and its admission limits remain the
same. A long SQL job can call `db.SetLocalStatementTimeout` with a positive,
justified budget inside its final transaction, retaining authorization and lock
ordering. That override ends at commit or rollback. Network/vendor waits must
remain outside the transaction; background context alone never raises budgets.

Migrations temporarily use a five-minute statement budget, five-second lock
budget and one-minute idle transaction budget on their advisory-lock connection.
Concurrent index creation and invalid-index cleanup use a thirty-minute
statement budget with the same lock and idle budgets. The advisory-lock wait is
bounded too. Settings are restored after success, failure or cancellation;
failed restoration closes the connection. A file's explicit `SET LOCAL` budget
still applies within that file. Already valid indexes and migration retry
records retain their existing recovery behavior.

Migration **1344** adds `nodes_work_kind_area_idx` concurrently over
`(tenant_id, fields->>'area')`, for live nodes with a non-null area. It adds no
tables or columns, and retains the existing node RLS policies and FORCE RLS.
The work-kind list selects its bounded cursor page first, then aggregates
visible nodes once for all selected areas. Grouping sets provide both workspace
totals and project totals, distinguishing project-less nodes from workspace
totals. Counts retain their prior semantics, including archived kinds, project
visibility, deleted-node exclusion and tenant isolation. API fields and routes
are unchanged.

The PostgreSQL 18 behavior fixture contains 12,000 nodes, project-less nodes,
two projects, deleted nodes, archived kinds and another tenant. One measured
run changed the count plan from 47 node scan loops to one grouped scan, from
17,159 shared buffer hits to 399, and from 46.729 ms to 4.815 ms. These are
fixture measurements, not latency guarantees; the test asserts plan structure
and count parity under tenant and project visibility rather than a time limit.

Operators can start slow-statement observation at one second, using their
existing privileged database administration authority:

```sql
ALTER ROLE aeon IN DATABASE aeon SET log_min_duration_statement = '1s';
```

Use the actual login role/database names. The ordinary application role does
not request additional privileges for this logging setting. PostgreSQL documents
[role/database defaults](https://www.postgresql.org/docs/18/sql-alterrole.html),
[timeout semantics](https://www.postgresql.org/docs/18/runtime-config-client.html)
and the [logging privilege requirement](https://www.postgresql.org/docs/18/runtime-config-logging.html).

Validation for AEON-1230 passed the database backstop/stream/backfill behavior
test, work-kind count parity, concurrent-index crash recovery, both negative
controls, all 22 work-kind browser cases and all 42 merge-main static checks.
The full affected Go run passed 126 packages; failed schema/fixture cases in
20 further packages passed focused retries after contract generation and with
parallel cloning tested separately at its original eight-way concurrency.
The remaining planning performance guard also failed on unchanged base
`f95e4d39e`: 206.845 ms against 200 ms, versus 204.071 ms with this change.
Its assertions and budget remain unchanged.
