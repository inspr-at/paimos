// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type notificationWaitFunc func(context.Context) (*pgconn.Notification, error)

func (f notificationWaitFunc) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	return f(ctx)
}
func (notificationWaitFunc) Close(context.Context) error { return nil }

// Risk: repeated/forged hints multiply reads or advance a trusted cursor, and
// backlog or missed hints postpone revocation/reconciliation beyond 15 seconds.
// All time advances are injected; no wall-clock delay establishes an outcome.
func TestHarnessNotificationCursorCoalescingAndAuthorityDeadline(t *testing.T) {
	stopped := errors.New("end of notification script")
	revoked := errors.New("current authority revoked")
	notice := func(tenantID string, id int64) *pgconn.Notification {
		return &pgconn.Notification{Payload: fmt.Sprintf(`{"tenant_id":%q,"id":%d}`, tenantID, id)}
	}
	t.Run("burst and forged position", func(t *testing.T) {
		now := time.Now()
		deadline := now.Add(15 * time.Second)
		var checks []int64
		var sent []string
		s := harnessNotificationStream{tenantID: "one", now: func() time.Time { return now },
			check: func(after int64) (int64, bool, error) {
				checks = append(checks, after)
				switch len(checks) {
				case 1, 2:
					return 100, false, nil
				default:
					return 101, true, nil
				}
			},
			send: func(name string) error { sent = append(sent, name); return nil },
		}
		queued := []*pgconn.Notification{notice("one", 1)}
		for id := int64(1); id <= 100; id++ {
			queued = append(queued, notice("one", id))
		}
		queued = append(queued, notice("other", 101), notice("one", 0), notice("one", 1_000_000), notice("one", 101))
		s.run(t.Context(), notificationWaitFunc(func(ctx context.Context) (*pgconn.Notification, error) {
			if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
				t.Fatalf("burst moved authority deadline: %v", got)
			}
			if len(queued) == 0 {
				return nil, stopped
			}
			n := queued[0]
			queued = queued[1:]
			return n, nil
		}))
		if !reflect.DeepEqual(checks, []int64{0, 100, 100}) || s.after != 101 || !reflect.DeepEqual(sent, []string{"harness.wake"}) {
			t.Fatalf("checks=%v cursor=%d sent=%v", checks, s.after, sent)
		}
	})
	t.Run("missed hint and resync", func(t *testing.T) {
		now := time.Now()
		var checks []int64
		var sent []string
		s := harnessNotificationStream{tenantID: "one", after: 7, now: func() time.Time { return now },
			check: func(after int64) (int64, bool, error) {
				checks = append(checks, after)
				return 55, len(checks) == 1, nil
			},
			send: func(name string) error { sent = append(sent, name); return nil },
		}
		waits := 0
		s.run(t.Context(), notificationWaitFunc(func(ctx context.Context) (*pgconn.Notification, error) {
			waits++
			switch waits {
			case 1:
				now, _ = ctx.Deadline()
				return nil, context.DeadlineExceeded
			case 2:
				return &pgconn.Notification{Payload: `{"tenant_id":"one","resync":true}`}, nil
			default:
				return nil, stopped
			}
		}))
		if !reflect.DeepEqual(checks, []int64{7, 55}) || s.after != 55 || !reflect.DeepEqual(sent, []string{"harness.wake", "stream.ping", "harness.wake"}) {
			t.Fatalf("checks=%v cursor=%d sent=%v", checks, s.after, sent)
		}
	})
	for _, mode := range []string{"idle", "backlog", "data"} {
		t.Run("revoked "+mode, func(t *testing.T) {
			now := time.Now()
			checks, waits := 0, 0
			s := harnessNotificationStream{tenantID: "one", after: 7, now: func() time.Time { return now },
				check: func(after int64) (int64, bool, error) {
					checks++
					if after != 7 {
						t.Fatalf("untrusted cursor %d", after)
					}
					return 0, false, revoked
				},
				send: func(name string) error { t.Fatalf("revoked stream sent %s", name); return nil },
			}
			s.run(t.Context(), notificationWaitFunc(func(ctx context.Context) (*pgconn.Notification, error) {
				waits++
				if waits > 1 {
					t.Fatal("revoked stream continued draining backlog")
				}
				if mode == "data" {
					return notice("one", 8), nil
				}
				now, _ = ctx.Deadline()
				if mode == "idle" {
					return nil, context.DeadlineExceeded
				}
				return notice("one", 7), nil
			}))
			if checks != 1 || waits != 1 || s.after != 7 {
				t.Fatalf("checks=%d waits=%d cursor=%d", checks, waits, s.after)
			}
		})
	}
}
