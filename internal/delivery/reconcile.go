// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func unlock(conn *pgxpool.Conn, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); err != nil {
		_ = conn.Conn().Close(ctx)
	}
}

// observationLock serializes network snapshots between webhook/reconciliation
// replicas. Session locks span reads; no row transaction spans network I/O.
func (m *Module) observationLock(ctx context.Context) (context.Context, func(), error) {
	conn, release, err := db.Acquire(ctx, m.pool)
	if err != nil {
		return ctx, nil, err
	}
	key := "aeon-delivery-observation:" + m.config.TenantID
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, key); err != nil {
		release()
		return ctx, nil, err
	}
	return db.WithConnection(ctx, m.pool, conn), func() { unlock(conn, key); release() }, nil
}
func (m *Module) Run(ctx context.Context) {
	app := &crossreview.GitHubApp{Config: m.config}
	if m.github == nil || !app.Configured(m.config.TenantID, m.config.Repository) {
		return
	}
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		m.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Module) sweep(ctx context.Context) {
	// One lane per replica; installation binding limits the tenant scan.
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	ctx = db.WithConnection(ctx, m.pool, conn)
	key := "aeon-delivery-reconciliation"
	var locked bool
	if conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked) != nil || !locked {
		return
	}
	defer unlock(conn, key)
	rows, err := conn.Query(ctx, `SELECT id::text FROM tenants WHERE id=$1`, m.config.TenantID)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return
	}
	for _, id := range ids {
		op, cancel := context.WithTimeout(ctx, 4*time.Minute)
		err = m.Reconcile(op, id)
		cancel()
		if err != nil {
			slog.Warn("delivery reconciliation incomplete", "tenant_id", id)
		}
	}
}

// Reconcile heals missed webhook heads/checks and derives pre-PR rows from
// completed platform runs/reviews. No GitHub writes are available to it.
func (m *Module) Reconcile(ctx context.Context, tid string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if tid != m.config.TenantID || m.github == nil {
		return errNotConfigured
	}
	ctx, release, err := m.observationLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	pulls, err := m.github.OpenPulls(ctx)
	if err != nil {
		return err
	}
	if len(pulls) > 2000 {
		return errRead
	}
	seen := map[int64]bool{}
	for _, p := range pulls {
		if p.Number <= 0 || seen[p.Number] {
			return errRead
		}
		seen[p.Number] = true
	}
	service := db.AllProjects(ctx, "delivery reconciliation")
	// Open inventory cannot prove a disappeared PR merged. Read each missing
	// tracked subject, in bounded pages, before recording its closed/merged state.
	var cursor string
	for {
		missing := []int64{}
		count := 0
		err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id::text,pull_request FROM delivery_items WHERE repository=$1 AND state<>'merged' AND pull_request IS NOT NULL AND ($2::uuid IS NULL OR id>$2) ORDER BY id LIMIT 100`, m.config.Repository, nullable(cursor))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				var n int64
				if err = rows.Scan(&id, &n); err != nil {
					return err
				}
				cursor = id
				count++
				if !seen[n] {
					missing = append(missing, n)
				}
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, n := range missing {
			p, err := m.github.Pull(ctx, n)
			if errors.Is(err, errMissing) {
				// Confirmed absence cannot prove merged or closed. Leave the
				// row and keep healing open pulls and platform facts.
				continue
			}
			if err != nil {
				return err
			}
			pulls = append(pulls, p)
			seen[n] = true
		}
		if count < 100 {
			break
		}
	}
	for start := 0; start < len(pulls); start += 100 {
		end := min(start+100, len(pulls))
		at := m.now()
		err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, tid); err != nil {
				return err
			}
			os := []Observation{}
			for _, p := range pulls[start:end] {
				o, err := m.observationTx(ctx, tx, p, at)
				if err != nil {
					return err
				}
				// Polling does not erase queue/hold facts unless the head moved or closed.
				if !o.Open {
					o.Queued = false
					o.QueueHead = ""
				}
				os = append(os, o)
			}
			_, err := recordTx(ctx, tx, tid, internalRecord("reconcile", at, os))
			return err
		})
		if err != nil {
			return err
		}
	}
	if err = m.refreshPlatform(ctx, tid); err != nil {
		return err
	}
	// Retry any fan-out that was interrupted after the projection committed.
	var seq int64
	for {
		type entry struct {
			seq int64
			id  string
		}
		page := []entry{}
		err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT sequence,delivery_id FROM delivery_github_events WHERE sequence>$1 AND NOT fanout_done ORDER BY sequence LIMIT 100`, seq)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var e entry
				if err = rows.Scan(&e.seq, &e.id); err != nil {
					return err
				}
				page = append(page, e)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, e := range page {
			if err = m.fanoutRecord(ctx, e.id); err != nil {
				return err
			}
			seq = e.seq
		}
		if len(page) < 100 {
			break
		}
	}
	return nil
}
func (m *Module) refreshPlatform(ctx context.Context, tid string) error {
	service := db.AllProjects(ctx, "delivery platform facts")
	var cursor string
	for {
		count := 0
		err := db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, tid); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT t.id::text,t.project_id::text FROM nodes t WHERE t.deleted_at IS NULL AND ($1::uuid IS NULL OR t.id>$1)
    AND (EXISTS(SELECT 1 FROM nodes n JOIN work_orders w ON w.node_id=n.id JOIN agent_runs r ON r.work_order_id=n.id WHERE n.parent_id=t.id AND w.kind='build' AND r.status='completed')
      OR EXISTS(SELECT 1 FROM work_order_reviews v WHERE v.ticket_node_id=t.id AND v.repository=$2))
    AND NOT EXISTS(SELECT 1 FROM delivery_items d WHERE d.ticket_node_id=t.id AND d.repository=$2 AND d.pull_request IS NOT NULL)
    ORDER BY t.id LIMIT 100`, nullable(cursor), m.config.Repository)
			if err != nil {
				return err
			}
			type ticket struct {
				id      string
				project *string
			}
			page := []ticket{}
			for rows.Next() {
				var t ticket
				if err = rows.Scan(&t.id, &t.project); err != nil {
					rows.Close()
					return err
				}
				page = append(page, t)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			count = len(page)
			os := []Observation{}
			at := m.now()
			for _, t := range page {
				o := Observation{ID: stableID(tid, m.config.Repository, subject(nil, &t.id)), Ticket: &t.id, Project: t.project, Repository: m.config.Repository, At: at}
				before, err := load(ctx, tx, o.ID)
				if err != nil {
					return err
				}
				if before != nil {
					o.HoldReason = before.HeldReason
					o.HeldFrom = before.HeldFrom
					o.Branch = before.Branch
				}
				// A review row is the authoritative pre-PR repository/head binding.
				var head string
				err = tx.QueryRow(ctx, `SELECT head_sha FROM work_order_reviews WHERE ticket_node_id=$1 AND repository=$2 ORDER BY created_at DESC,work_order_id DESC LIMIT 1`, t.id, m.config.Repository).Scan(&head)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				o.Head = head
				if head != "" {
					source := "review_row"
					o.LinkSource = &source
				}
				if err = platformTx(ctx, tx, &o); err != nil {
					return err
				}
				o.Settings, err = settingsTx(ctx, tx, o.Project)
				if err != nil {
					return err
				}
				os = append(os, o)
				cursor = t.id
			}
			if len(os) == 0 {
				return nil
			}
			_, err = recordTx(ctx, tx, tid, internalRecord("platform", at, os))
			return err
		})
		if err != nil {
			return err
		}
		if count < 100 {
			return nil
		}
	}
}

// normalizedEqual excludes observation time for polling compaction.
func normalizedEqual(a, b Observation) bool {
	a.At = time.Time{}
	b.At = time.Time{}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Current platform rows remain authoritative even when no GitHub event arrives.
// Record newly observed run/review/settings facts so subsequent rebuilds retain
// their state_since instead of starting a new deadline at each rebuild.
func (m *Module) refreshExistingFacts(ctx context.Context, tid string) error {
	var cursor string
	for {
		count := 0
		err := db.InTenant(db.AllProjects(ctx, "delivery rebuild current platform facts"), m.pool, tid, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, tid); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT `+columns+` FROM delivery_items WHERE ($1::uuid IS NULL OR id>$1) ORDER BY id LIMIT 100`, nullable(cursor))
			if err != nil {
				return err
			}
			items := []Item{}
			for rows.Next() {
				i, err := scan(rows)
				if err != nil {
					rows.Close()
					return err
				}
				items = append(items, i)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			count = len(items)
			os := []Observation{}
			at := m.now()
			for _, i := range items {
				o := i.Observation
				cursor = i.ID
				if i.State == Merged {
					continue
				}
				if err = platformTx(ctx, tx, &o); err != nil {
					return err
				}
				o.Settings, err = settingsTx(ctx, tx, o.Project)
				if err != nil {
					return err
				}
				if !normalizedEqual(o, i.Observation) {
					o.At = at
					os = append(os, o)
				}
			}
			if len(os) == 0 {
				return nil
			}
			_, err = recordTx(ctx, tx, tid, internalRecord("platform", at, os))
			return err
		})
		if err != nil {
			return err
		}
		if count < 100 {
			return nil
		}
	}
}
