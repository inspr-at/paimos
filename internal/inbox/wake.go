// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// WorkerOptions tunes webhook delivery. Zero values take the defaults.
type WorkerOptions struct {
	Interval    time.Duration
	Batch       int
	MaxAttempts int
}

// Worker delivers webhook wake hints. Run it beside the HTTP server.
type Worker struct {
	pool     *pgxpool.Pool
	interval time.Duration
	batch    int
	maxTries int
	lease    time.Duration
	poster   wakePoster
}

type wakePoster interface {
	Post(ctx context.Context, webhookURL string, body []byte) (status int, err error)
}

type wakeMeta struct {
	MessageID string `json:"message_id"`
	TargetID  string `json:"target_id"`
	EventID   int64  `json:"event_id"`
	Attempts  int    `json:"attempts,omitempty"`
	Status    *int   `json:"status,omitempty"`
}

type claimedWake struct {
	messageID   string
	targetID    string
	recipientID string
	url         string
	eventID     int64
	attempts    int
}

// NewWorker binds the wake queue to pool. The coordinator starts Worker.Run.
func NewWorker(pool *pgxpool.Pool, opts WorkerOptions) *Worker {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.Batch <= 0 {
		opts.Batch = 8
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 8
	}
	return &Worker{
		pool:     pool,
		interval: opts.Interval,
		batch:    opts.Batch,
		maxTries: opts.MaxAttempts,
		lease:    30 * time.Second,
		poster:   safePoster{},
	}
}

// Run delivers due wakes immediately and then on each interval until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("inbox wake", "err", err)
			}
			timer.Reset(w.interval)
		}
	}
}

// ProcessOnce delivers the current batch for every tenant.
// The count is wakes that received a 2xx response.
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ids, err := w.tenantIDs(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	for _, id := range ids {
		c, err := w.processTenant(ctx, id)
		n += c
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// tenantIDs reads the tenant registry. That table is not tenant-scoped, so
// this is the one query outside db.InTenant. Every inbox query runs inside it.
func (w *Worker) tenantIDs(ctx context.Context) ([]string, error) {
	rows, err := w.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (w *Worker) processTenant(ctx context.Context, tenantID string) (int, error) {
	// A system job without a principal: wake rows and their events are
	// workspace rows (ADR-003 P2).
	ctx = db.NoProjects(ctx, "inbox wake worker")
	claimed, err := w.claim(ctx, tenantID)
	if err != nil || len(claimed) == 0 {
		return 0, err
	}
	var n int
	var first error
	for _, wake := range claimed {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		body, err := wakePayload(wake.messageID, wake.eventID)
		if err != nil {
			return n, err
		}
		status, postErr := w.poster.Post(ctx, wake.url, body)
		if postErr != nil || status < 200 || status >= 300 {
			if err := w.finishFail(ctx, tenantID, wake, status); err != nil && first == nil {
				first = err
			}
			continue
		}
		if err := w.finishOK(ctx, tenantID, wake, status); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

func wakePayload(messageID string, eventID int64) ([]byte, error) {
	return json.Marshal(struct {
		MessageID string `json:"message_id"`
		EventID   int64  `json:"event_id"`
	}{messageID, eventID})
}

type dueWake struct {
	claimedWake
	enabled bool
	expired bool
}

func (w *Worker) claim(ctx context.Context, tenantID string) ([]claimedWake, error) {
	var claimed []claimedWake
	err := db.InTenant(ctx, w.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT w.message_id::text, w.target_id::text,
			m.recipient_principal_id::text, m.sent_event_id, t.webhook_url, t.enabled,
			(m.expires_at IS NOT NULL AND m.expires_at <= clock_timestamp())
			FROM inbox_wakes w
			JOIN inbox_messages m ON m.tenant_id = w.tenant_id AND m.id = w.message_id
			JOIN inbox_delivery_targets t ON t.tenant_id = w.tenant_id AND t.id = w.target_id
			WHERE m.chat_thread_id IS NULL AND w.delivered_at IS NULL
			  AND w.attempts < $1
			  AND w.next_attempt_at <= clock_timestamp()
			  AND t.kind = 'webhook'
			ORDER BY w.next_attempt_at, w.message_id, w.target_id
			LIMIT $2
			FOR NO KEY UPDATE OF w, t SKIP LOCKED`, w.maxTries, w.batch)
		if err != nil {
			return err
		}
		var due []dueWake
		for rows.Next() {
			var item dueWake
			if err := rows.Scan(&item.messageID, &item.targetID, &item.recipientID, &item.eventID, &item.url, &item.enabled, &item.expired); err != nil {
				rows.Close()
				return err
			}
			due = append(due, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range due {
			if !item.enabled || item.expired || item.url == "" {
				if err := w.drop(ctx, tx, tenantID, item.claimedWake); err != nil {
					return err
				}
				continue
			}
			attempts, err := w.markAttempt(ctx, tx, tenantID, item.claimedWake)
			if err != nil {
				return err
			}
			item.attempts = attempts
			claimed = append(claimed, item.claimedWake)
		}
		return nil
	})
	return claimed, err
}

func (w *Worker) markAttempt(ctx context.Context, tx pgx.Tx, tenantID string, wake claimedWake) (int, error) {
	var attempts int
	if err := tx.QueryRow(ctx, `UPDATE inbox_wakes
		SET attempts = attempts + 1,
		    next_attempt_at = clock_timestamp() + ($3::int * interval '1 second')
		WHERE message_id = $1::uuid AND target_id = $2::uuid AND delivered_at IS NULL
		RETURNING attempts`, wake.messageID, wake.targetID, int(w.lease.Seconds())).Scan(&attempts); err != nil {
		return 0, err
	}
	wake.attempts = attempts
	_, err := events.Append(ctx, tx, tenant.Principal{ID: wake.recipientID, TenantID: tenantID}, events.Change{
		Type:  "inbox.wake_attempted",
		After: wakeMeta{MessageID: wake.messageID, TargetID: wake.targetID, EventID: wake.eventID, Attempts: attempts},
	})
	return attempts, err
}

func (w *Worker) drop(ctx context.Context, tx pgx.Tx, tenantID string, wake claimedWake) error {
	tag, err := tx.Exec(ctx, `UPDATE inbox_wakes
		SET attempts = $3,
		    next_attempt_at = clock_timestamp() + interval '24 hours'
		WHERE message_id = $1::uuid AND target_id = $2::uuid AND delivered_at IS NULL`,
		wake.messageID, wake.targetID, w.maxTries)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, err = events.Append(ctx, tx, tenant.Principal{ID: wake.recipientID, TenantID: tenantID}, events.Change{
		Type:  "inbox.wake_dropped",
		After: wakeMeta{MessageID: wake.messageID, TargetID: wake.targetID, EventID: wake.eventID, Attempts: w.maxTries},
	})
	return err
}

func (w *Worker) finishOK(ctx context.Context, tenantID string, wake claimedWake, status int) error {
	return db.InTenant(ctx, w.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE inbox_wakes
			SET delivered_at = clock_timestamp(), last_status = $3
			WHERE message_id = $1::uuid AND target_id = $2::uuid AND delivered_at IS NULL`,
			wake.messageID, wake.targetID, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		_, err = events.Append(ctx, tx, tenant.Principal{ID: wake.recipientID, TenantID: tenantID}, events.Change{
			Type:  "inbox.wake_delivered",
			After: wakeMeta{MessageID: wake.messageID, TargetID: wake.targetID, EventID: wake.eventID, Attempts: wake.attempts, Status: &status},
		})
		return err
	})
}

func (w *Worker) finishFail(ctx context.Context, tenantID string, wake claimedWake, status int) error {
	return db.InTenant(ctx, w.pool, tenantID, func(tx pgx.Tx) error {
		delay := int(backoff(wake.attempts).Seconds())
		eventType := "inbox.wake_failed"
		if wake.attempts >= w.maxTries {
			delay = 24 * 60 * 60
			eventType = "inbox.wake_abandoned"
		}
		var statusArg any
		if status >= 100 && status <= 599 {
			statusArg = status
		}
		tag, err := tx.Exec(ctx, `UPDATE inbox_wakes
			SET last_status = $3,
			    next_attempt_at = clock_timestamp() + ($4::int * interval '1 second')
			WHERE message_id = $1::uuid AND target_id = $2::uuid AND delivered_at IS NULL`,
			wake.messageID, wake.targetID, statusArg, delay)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		var statusPtr *int
		if statusArg != nil {
			statusPtr = &status
		}
		_, err = events.Append(ctx, tx, tenant.Principal{ID: wake.recipientID, TenantID: tenantID}, events.Change{
			Type:  eventType,
			After: wakeMeta{MessageID: wake.messageID, TargetID: wake.targetID, EventID: wake.eventID, Attempts: wake.attempts, Status: statusPtr},
		})
		return err
	})
}

func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 6 {
		shift = 6
	}
	d := time.Second << shift
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}
