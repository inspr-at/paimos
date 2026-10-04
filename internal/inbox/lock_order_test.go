// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestSendDoesNotWaitForTargetWriterAfterEvent(t *testing.T) {
	for _, writer := range []string{"disable", "claim"} {
		t.Run(writer, func(t *testing.T) {
			w := newWorld(t)
			m := newModule(w.db.App)
			url := "https://example.invalid/wake"
			target, err := m.createTarget(t.Context(), w.recipient, w.recipient.ID, "webhook", &url)
			if err != nil {
				t.Fatal(err)
			}
			if writer == "claim" {
				if _, err := m.send(t.Context(), w.sender, sendInput{Recipient: w.recipient.ID, Body: "first", Key: "first"}); err != nil {
					t.Fatal(err)
				}
			}
			pool, barrier, ctx := dbtest.BarrierPool(t, w.db.App, func(sql string) bool {
				if writer == "disable" {
					return strings.Contains(sql, "UPDATE inbox_delivery_targets SET enabled")
				}
				return strings.Contains(sql, "FOR ") && strings.Contains(sql, "SKIP LOCKED") && strings.Contains(sql, "FROM inbox_wakes w")
			})
			first := make(chan error, 1)
			go func() {
				if writer == "disable" {
					first <- newModule(pool).disableTarget(ctx, w.recipient, target.ID)
				} else {
					_, err := NewWorker(pool, WorkerOptions{}).claim(db.NoProjects(ctx, "inbox wake worker"), w.recipient.TenantID)
					first <- err
				}
			}()
			pid := barrier.Wait(t, ctx)
			second := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				_, err := m.send(ctx, w.sender, sendInput{Recipient: w.recipient.ID, Body: "second", Key: "second"})
				second <- err
				close(done)
			}()
			lock := dbtest.BlockedOrDone(t, ctx, w.db.Admin, pid, done)
			barrier.Release()
			if err := dbtest.Await(t, ctx, first); err != nil {
				t.Fatal(err)
			}
			if err := dbtest.Await(t, ctx, second); err != nil {
				t.Fatal(err)
			}
			if lock != "" {
				t.Fatalf("send acquired event counter then waited on target writer (%s)", lock)
			}
			if writer == "disable" {
				wakes, err := NewWorker(w.db.App, WorkerOptions{}).claim(db.NoProjects(ctx, "inbox wake worker"), w.recipient.TenantID)
				if err != nil || len(wakes) != 0 {
					t.Fatalf("disabled target claimed: %v %v", wakes, err)
				}
			}
		})
	}
}
