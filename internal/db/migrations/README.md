# Database migrations

`internal/db/migrate.go` applies embedded SQL files in filename order, under a
session advisory lock, and records each completed filename in `schema_migrations`.
Normally each file and its migration record share one transaction.

For an index on a hot table, put `-- aeon:no-transaction` on the **first line**
(before the SPDX comment) and use exactly one `CREATE INDEX CONCURRENTLY`
statement per file. `IF NOT EXISTS` is supported. Index and table names must be
unqualified lowercase identifiers. Other statements, including `SET`, and
multiple index builds are rejected in this mode; use separate numbered files.

The runner executes the index build outside a transaction, with a session
`lock_timeout` of five seconds covering both creation and retry cleanup. This
bounds lock acquisition, not the total build duration. The previous timeout is
restored before returning the connection to the pool, including on failure.

The advisory lock covers inspection, creation and recording. On retry, the runner
keeps a valid index on the expected table, drops an invalid partial index with
`DROP INDEX CONCURRENTLY` and rebuilds it, or rejects a same-named index on a
different table. `IF NOT EXISTS` alone cannot recover an invalid index. Only a
successful build or a valid existing index is recorded; an interrupted build or
a crash before recording can safely be retried. Already recorded files are
skipped. Tests in `concurrent_migration_test.go` exercise these recovery paths.
