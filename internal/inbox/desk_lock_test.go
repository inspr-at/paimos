// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type deskEventBarrier struct {
	pgx.Tx
	before func()
}

func (b *deskEventBarrier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if b.before != nil && strings.Contains(sql, "INSERT INTO events") {
		before := b.before
		b.before = nil
		before()
	}
	return b.Tx.QueryRow(ctx, sql, args...)
}

func TestDeskReplyReservesBeforeEventWithConcurrentSend(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	in := compatInput(w.recipient.ID, "parent")
	in.ExpectsReply = true
	parent := mustCompatSend(t, m, w.sender, project, in)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ready := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var deskID string
	if err := w.db.Admin.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&deskID); err != nil {
		t.Fatal(err)
	}
	var pid uint32
	var reserved bool
	go func() {
		done <- db.InTenant(tenant.WithPrincipal(ctx, w.recipient), w.db.App, w.recipient.TenantID, func(tx pgx.Tx) error {
			pid = tx.Conn().PgConn().PID()
			barrier := &deskEventBarrier{Tx: tx, before: func() {
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages m JOIN inbox_compat_messages c ON c.tenant_id=m.tenant_id AND c.id=m.id JOIN inbox_message_deliveries d ON d.tenant_id=m.tenant_id AND d.message_id=m.id JOIN inbox_receipts r ON r.tenant_id=m.tenant_id AND r.message_id=m.id JOIN inbox_reply_obligations o ON o.tenant_id=m.tenant_id AND o.reply_message_id=m.id WHERE m.id=$1 AND o.message_id=$2 AND o.closed_at IS NOT NULL)`, deskID, parent.ID).Scan(&reserved)
				if err != nil {
					t.Error(err)
				}
				close(ready)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}}
			return RecordDeskReply(ctx, barrier, w.recipient, DeskReply{ID: deskID, RootID: parent.ID, ReplyToID: parent.ID, ProjectID: project, RecipientID: w.sender.ID, RecipientName: w.sender.Name, Body: "desk answer"})
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("desk stopped before event barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !reserved {
		t.Error("desk appended first event before reserving message, delivery, receipt and obligation rows")
	}
	sent := make(chan error, 1)
	reply := compatInput(w.sender.ID, "concurrent-reply")
	reply.ReplyTo = &parent.ID
	go func() { _, err := m.commitMessage(ctx, w.recipient, project, reply); sent <- err }()
	// Observe the actual database wait, not elapsed time. The ordinary sender
	// must wait on the same obligation BEFORE acquiring the event counter.
	var waitingSQL string
	for {
		err := w.db.Admin.QueryRow(ctx, `SELECT coalesce((SELECT query FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) AND datname=current_database() LIMIT 1),'')`, pid).Scan(&waitingSQL)
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		if waitingSQL != "" {
			break
		}
		select {
		case err := <-sent:
			close(release)
			t.Fatalf("send bypassed obligation barrier: %v", err)
		default:
		}
	}
	if !strings.Contains(waitingSQL, "inbox_reply_obligations") || !strings.HasPrefix(strings.TrimSpace(waitingSQL), "SELECT") {
		t.Errorf("send did not reserve obligation before event: %s", waitingSQL)
	}
	close(release)
	for _, ch := range []chan error{done, sent} {
		if err := <-ch; err != nil {
			t.Error(err)
		}
	}
	var replies, receipts, closed int
	if err := w.db.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM inbox_compat_messages WHERE reply_to_id=$1),(SELECT count(*) FROM inbox_receipts r JOIN inbox_compat_messages c ON c.tenant_id=r.tenant_id AND c.id=r.message_id WHERE c.reply_to_id=$1),(SELECT count(*) FROM events WHERE type='inbox.reply_obligation_closed' AND after->>'message_id'=$1::text)`, parent.ID).Scan(&replies, &receipts, &closed); err != nil {
		t.Fatal(err)
	}
	if replies != 2 || receipts != 2 || closed != 1 {
		t.Fatalf("replies=%d receipts=%d closed=%d", replies, receipts, closed)
	}
}

func TestMessageEventReservationCannotCommitIncomplete(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	parent := mustCompatSend(t, m, w.sender, project, compatInput(w.recipient.ID, "reservation-parent"))
	for _, table := range []string{"inbox_messages", "inbox_compat_messages"} {
		t.Run(table, func(t *testing.T) {
			reachedCommit := false
			err := db.InTenant(tenant.WithPrincipal(t.Context(), w.sender), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
				// A reservation may be transiently empty, but it must never persist.
				if _, err := tx.Exec(t.Context(), `UPDATE `+table+` SET sent_event_id=NULL WHERE id=$1`, parent.ID); err != nil {
					return err
				}
				reachedCommit = true
				return nil
			})
			var constraint *pgconn.PgError
			if !reachedCommit || !errors.As(err, &constraint) || constraint.Code != "23502" {
				t.Fatalf("deferred reservation guard: reached commit=%t err=%v", reachedCommit, err)
			}
			var valid bool
			if err := w.db.Admin.QueryRow(t.Context(), `SELECT sent_event_id IS NOT NULL FROM `+table+` WHERE id=$1`, parent.ID).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if !valid {
				t.Fatal("incomplete event reservation persisted")
			}
		})
	}
}
