// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type scopeErrorTx struct {
	pgx.Tx
	queryErr, restoreErr error
	restored             bool
}
type scopeErrorRow struct {
	prior bool
	err   error
}

func (r scopeErrorRow) Scan(dst ...any) error {
	if r.prior {
		*dst[0].(*string) = "project-fence"
	}
	return r.err
}
func (tx *scopeErrorTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if query == "SELECT coalesce(current_setting('aeon.visible_projects', true), '')" {
		return scopeErrorRow{prior: true}
	}
	return scopeErrorRow{err: tx.queryErr}
}
func (tx *scopeErrorTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	if len(args) > 0 {
		tx.restored = true
		return pgconn.CommandTag{}, tx.restoreErr
	}
	return pgconn.CommandTag{}, nil
}
func TestProjectLookupPreservesQueryFailure(t *testing.T) {
	want := errors.New("injected query failure")
	tx := &scopeErrorTx{queryErr: want, restoreErr: errors.New("transaction aborted")}
	if _, err := withAllProjects(t.Context(), tx, "query", "run"); !errors.Is(err, want) {
		t.Fatalf("lost original error: %v", err)
	}
	if !tx.restored {
		t.Fatal("visibility restoration not attempted")
	}
	for _, missing := range []error{nil, pgx.ErrNoRows} {
		tx := &scopeErrorTx{queryErr: missing}
		if id, err := withAllProjects(t.Context(), tx, "query", "run"); err != nil || id != "" || !tx.restored {
			t.Fatalf("nullable lookup: %q %v", id, err)
		}
	}
}
