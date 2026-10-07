// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Module struct {
	pool   *pgxpool.Pool
	config crossreview.AppConfig
	secret []byte
	github GitHub
	fanout func(context.Context, []byte) error
	now    func() time.Time
}

func New(pool *pgxpool.Pool, config crossreview.AppConfig, secret []byte, github GitHub, fanout func(context.Context, []byte) error) *Module {
	return &Module{pool: pool, config: config, secret: append([]byte(nil), secret...), github: github, fanout: fanout, now: func() time.Time { return time.Now().UTC() }}
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/github/webhook", m.webhook)
	mux.HandleFunc("GET /api/delivery", m.list)
	mux.HandleFunc("GET /api/nodes/{id}/delivery", m.list)
	mux.HandleFunc("POST /api/delivery/{itemId}/hold", m.hold)
	mux.HandleFunc("DELETE /api/delivery/{itemId}/hold", m.hold)
	mux.HandleFunc("GET /api/settings/delivery", m.settings)
	mux.HandleFunc("PUT /api/settings/delivery", m.settings)
	mux.HandleFunc("GET /api/projects/{projectId}/delivery-settings", m.settings)
	mux.HandleFunc("PUT /api/projects/{projectId}/delivery-settings", m.settings)
}
func (m *Module) observationTx(ctx context.Context, tx pgx.Tx, p Pull, at time.Time) (Observation, error) {
	pr := p.Number
	o := Observation{Repository: m.config.Repository, PR: &pr, Branch: p.Branch, Head: p.Head, Base: p.Base, Open: p.Open, Merged: p.Merged, Queued: p.Queued, QueueHead: p.QueueHead, Checks: p.Checks, At: at}
	o.ID = stableID(m.config.TenantID, o.Repository, subject(o.PR, nil))
	before, err := load(ctx, tx, o.ID)
	if err != nil {
		return o, err
	}
	if before != nil {
		o.Ticket, o.Project, o.LinkSource = before.Ticket, before.Project, before.LinkSource
		o.HoldReason = before.HeldReason
		o.HeldFrom = before.HeldFrom
		if before.Head == o.Head {
			o.QueueFailure = before.Observation.QueueFailure
		}
	}
	if err = linkTx(ctx, tx, &o, p.Title); err != nil {
		return o, err
	}
	if err = platformTx(ctx, tx, &o); err != nil {
		return o, err
	}
	o.Settings, err = settingsTx(ctx, tx, o.Project)
	// PR linkage replaces the ticket's pre-push row. Keep any explicit hold.
	if err == nil && o.Ticket != nil {
		preID := stableID(m.config.TenantID, o.Repository, subject(nil, o.Ticket))
		pre, e := load(ctx, tx, preID)
		if e != nil {
			return o, e
		}
		if pre != nil && o.HoldReason == nil {
			o.HoldReason = pre.HeldReason
			o.HeldFrom = pre.HeldFrom
		}
	}
	return o, err
}

// QueueFailedTx is AEON-850's transition hook, not a failure classifier. The
// caller must hold the tenant fence and append no earlier event in this tx.
func (m *Module) QueueFailedTx(ctx context.Context, tx pgx.Tx, tid, id string, at time.Time) error {
	i, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	if i == nil {
		return pgx.ErrNoRows
	}
	o := i.Observation
	o.QueueFailure = true
	o.Queued = false
	o.At = at
	_, err = recordTx(ctx, tx, tid, internalRecord("queue_failed", at, []Observation{o}))
	return err
}

// Replay fan-out independently of projection acknowledgement. A failed legacy
// handler stays retryable, even after the signed delivery was deduplicated.
func (m *Module) fanoutRecord(ctx context.Context, id string) error {
	if m.fanout == nil {
		return nil
	}
	service := db.AllProjects(ctx, "delivery crossreview fan-out")
	var raw []byte
	var action, event string
	var done bool
	err := db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT event,action,observations,fanout_done FROM delivery_github_events WHERE delivery_id=$1`, id).Scan(&event, &action, &raw, &done)
	})
	if err != nil {
		return err
	}
	if done || event != "pull_request" {
		return nil
	}
	var os []Observation
	if err = json.Unmarshal(raw, &os); err != nil {
		return err
	}
	for _, o := range os {
		if o.PR == nil {
			continue
		}
		body, _ := json.Marshal(map[string]any{"action": action, "pull_request": map[string]any{"number": *o.PR, "head": map[string]string{"sha": o.Head}, "base": map[string]any{"sha": o.Base, "repo": map[string]string{"full_name": o.Repository}}}})
		if err = m.fanout(ctx, body); err != nil {
			return err
		}
	}
	return db.InTenant(service, m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, m.config.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE delivery_github_events SET fanout_done=true WHERE delivery_id=$1`, id)
		return err
	})
}
func (m *Module) duplicate(ctx context.Context, id string) (bool, error) {
	exists := false
	err := db.InTenant(db.AllProjects(ctx, "delivery deduplication"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_github_events WHERE delivery_id=$1)`, id).Scan(&exists)
	})
	return exists, err
}

var errNotConfigured = errors.New("delivery installation is not configured")
