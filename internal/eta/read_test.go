// SPDX-License-Identifier: AGPL-3.0-only
package eta

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

var queryStopped = errors.New("test query stopped")

type deadlineTx struct {
	pgx.Tx
	t              *testing.T
	called         bool
	callerDeadline time.Time
}

func (tx *deadlineTx) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	tx.called = true
	deadline, ok := ctx.Deadline()
	if !ok {
		tx.t.Error("aggregate query has no deadline")
	}
	if tx.callerDeadline.IsZero() {
		if deadline.After(time.Now().Add(5 * time.Second)) {
			tx.t.Error("aggregate deadline exceeds five seconds")
		}
	} else if !deadline.Equal(tx.callerDeadline) {
		tx.t.Error("aggregate extended caller deadline")
	}
	return nil, queryStopped
}
func TestAggregateReadsHaveDeadline(t *testing.T) {
	for _, one := range []bool{false, true} {
		for _, short := range []bool{false, true} {
			t.Run(map[bool]string{true: "one", false: "batch"}[one]+map[bool]string{true: "-caller", false: "-default"}[short], func(t *testing.T) {
				ctx := t.Context()
				tx := &deadlineTx{t: t}
				if short {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
					tx.callerDeadline, _ = ctx.Deadline()
				}
				var err error
				if one {
					_, err = One(ctx, tx, "00000000-0000-4000-8000-000000000001")
				} else {
					_, err = LoadAggregates(ctx, tx, []string{"00000000-0000-4000-8000-000000000001"})
				}
				if !tx.called || !errors.Is(err, queryStopped) {
					t.Fatalf("query failure lost: %v", err)
				}
			})
		}
	}
}
