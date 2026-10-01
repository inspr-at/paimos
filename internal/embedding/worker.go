// SPDX-License-Identifier: AGPL-3.0-only

package embedding

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

// Options tunes the queue worker. Zero values take the defaults.
type Options struct {
	Resolve        Resolver
	Interval       time.Duration
	Batch          int
	MaxAttempts    int
	ImmediateRetry bool
	// Append overrides the event insert. Nil uses the events-table insert
	// that matches events.Writer.Append.
	Append AppendFunc
}

// Worker drains node_embedding_jobs for every tenant.
type Worker struct {
	pool      *pgxpool.Pool
	provider  Provider
	resolve   Resolver
	interval  time.Duration
	batch     int
	maxTries  int
	immediate bool
	append    AppendFunc
}

// NewWorker requires a pool and either a provider or Options.Resolve. Share
// that provider/resolver with search so query and indexed vectors match.
func NewWorker(pool *pgxpool.Pool, provider Provider, opts Options) *Worker {
	if pool == nil || (provider == nil && opts.Resolve == nil) {
		panic("embedding.NewWorker: pool and provider are required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.Batch <= 0 {
		opts.Batch = 8
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 8
	}
	if opts.Append == nil {
		opts.Append = sqlAppend
	}
	return &Worker{
		pool:      pool,
		provider:  provider,
		resolve:   opts.Resolve,
		interval:  opts.Interval,
		batch:     opts.Batch,
		maxTries:  opts.MaxAttempts,
		immediate: opts.ImmediateRetry,
		append:    opts.Append,
	}
}

// Run calls ProcessOnce immediately and then on each interval until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("embedding queue", "err", err)
			}
			timer.Reset(w.interval)
		}
	}
}

// ProcessOnce claims and finishes the current batch for every tenant.
// The count is jobs removed (stored, already current, or dropped because
// the node was deleted).
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ids, err := w.tenantIDs(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	var first error
	for _, id := range ids {
		c, err := w.processTenant(ctx, id)
		n += c
		if err != nil && first == nil {
			first = err
		}
	}
	return n, first
}

// tenantIDs reads the tenant registry. That table is not tenant-scoped, so
// this is the one query outside db.InTenant. Every job, node, embedding and
// event query runs inside db.InTenant.
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
	// The worker embeds every project's nodes for search (ADR-003 P2).
	ctx = db.AllProjects(ctx, "embedding worker")
	provider := w.provider
	if w.resolve != nil {
		var err error
		provider, err = w.resolve(ctx, tenantID)
		if err != nil {
			return 0, err
		}
	}
	if provider == nil {
		return 0, nil
	}
	model := strings.TrimSpace(provider.Model())
	if model == "" || len(model) > 200 {
		return 0, errors.New("embedding model is not configured")
	}
	live, dropped, err := w.claim(ctx, tenantID)
	if err != nil || len(live) == 0 {
		return dropped, err
	}
	texts := make([]string, len(live))
	for i, job := range live {
		texts[i] = Document(job.Title, job.Body)
	}
	vectors, err := provider.Embed(ctx, texts)
	if err != nil || len(vectors) != len(live) {
		msg := "embedding request failed"
		if err == nil {
			err = errors.New("embedding response was incomplete")
			msg = err.Error()
		}
		for _, job := range live {
			_ = w.fail(ctx, tenantID, job, msg)
		}
		return dropped, err
	}
	n := dropped
	var first error
	for i, job := range live {
		if err := Validate(vectors[i]); err != nil {
			_ = w.fail(ctx, tenantID, job, "embedding response was rejected")
			if first == nil {
				first = err
			}
			continue
		}
		ok, err := w.finish(ctx, tenantID, model, job, vectors[i])
		if err != nil {
			_ = w.fail(ctx, tenantID, job, "embedding store failed")
			if first == nil {
				first = err
			}
			continue
		}
		if ok {
			n++
		}
	}
	return n, first
}

func (w *Worker) fail(ctx context.Context, tenantID string, job claimedJob, message string) error {
	if len(message) > 300 {
		message = message[:300]
	}
	return db.InTenant(ctx, w.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE node_embedding_jobs
			SET status = 'failed', last_error = $3, updated_at = now()
			WHERE node_id = $1 AND queued_at = $2 AND status = 'running'`,
			job.NodeID, job.QueuedAt, message)
		return err
	})
}
