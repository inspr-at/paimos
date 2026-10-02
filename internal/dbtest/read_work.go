// SPDX-License-Identifier: AGPL-3.0-only
package dbtest

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// ReadWork measures actual result rows materialized by a transactional reader.
// It deliberately avoids elapsed-time assertions, which depend on host load.
type ReadWork struct{ Statements, Rows int }
type countedTx struct {
	pgx.Tx
	work *ReadWork
}
type countedRows struct {
	pgx.Rows
	work *ReadWork
}
type countedRow struct {
	pgx.Row
	work *ReadWork
}

func CountReads(tx pgx.Tx, work *ReadWork) pgx.Tx { return countedTx{tx, work} }
func (tx countedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.work.Statements++
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return countedRows{rows, tx.work}, nil
}
func (tx countedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.work.Statements++
	return countedRow{tx.Tx.QueryRow(ctx, sql, args...), tx.work}
}
func (rows countedRows) Next() bool {
	ok := rows.Rows.Next()
	if ok {
		rows.work.Rows++
	}
	return ok
}
func (row countedRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err == nil {
		row.work.Rows++
	}
	return err
}
