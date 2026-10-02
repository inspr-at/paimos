// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Publication is an immutable release-history entry. Product entries resolve
// their project key inside each tenant; they never broadcast to other projects.
type Publication struct {
	ProjectKey  string
	ProjectID   string    `json:"project_id"`
	ReleaseID   *string   `json:"release_id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
}

const batchSize = 50

func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := m.runAll(ctx); err != nil && ctx.Err() == nil {
			slog.Error("recurring work", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Module) runAll(ctx context.Context) error {
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	failures := []error{}
	for _, id := range ids {
		if err = m.RunTenant(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("tenant %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// RunTenant is a bounded pass. Each claim commits independently, preserving a
// durable key/cursor through crashes. Replicas yield on tenant/tree locks and
// reload the due row after obtaining them. All comparisons use the DB clock.
func (m *Module) RunTenant(ctx context.Context, tenantID string) error {
	ctx = db.AllProjects(ctx, "recurrence scheduler")
	var active bool
	if err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recurrences WHERE NOT paused)`).Scan(&active)
	}); err != nil {
		return err
	}
	if !active {
		return nil
	}
	actor, err := ensureActor(ctx, m, tenantID)
	if err != nil {
		return err
	}
	if err = m.syncPublications(ctx, actor); err != nil {
		return err
	}
	for i := 0; i < batchSize; i++ {
		processed := false
		err = db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
			got, err := lock(ctx, tx, tenantID, true)
			if err != nil || !got {
				return err
			}
			now, err := m.clock(ctx, tx)
			if err != nil {
				return err
			}
			// The tenant/tree claim comes before node rows and recurrence rows. This
			// single-row unit never takes a later resource lock after events.Append.
			r, err := scanRecurrence(tx.QueryRow(ctx, `SELECT `+recurrenceColumns+` FROM recurrences r WHERE NOT paused AND
    ((trigger->>'kind'='time' AND next_at<=$1) OR (trigger->>'kind'='event' AND EXISTS(SELECT 1 FROM events e WHERE e.type='release.published' AND e.node_id=r.project_id AND e.id>r.event_cursor AND `+eventDueSQL+`<=$1))) ORDER BY next_at NULLS LAST,id LIMIT 1`, now))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			processed = true
			if r.Trigger.Kind == "time" {
				s, err := parseSchedule(r.Trigger)
				if err != nil {
					return err
				}
				at, err := s.latest(now)
				if err != nil {
					return err
				}
				if at.Before(*r.NextAt) {
					return fmt.Errorf("recurrence cursor precedes its schedule")
				}
				next, err := s.next(now)
				if err != nil {
					return err
				}
				if _, err = occur(ctx, tx, actor, r, "time:"+at.UTC().Format(time.RFC3339), at, nil, "", ""); err != nil {
					return err
				}
				// occur already owns this row; updating only scalar cursor columns takes
				// no later resource lock, and rolls back atomically with the receipt.
				_, err = tx.Exec(ctx, `UPDATE recurrences SET next_at=$2 WHERE id=$1`, r.ID, next)
				return err
			}
			var id int64
			var raw []byte
			var eventAt time.Time
			if err = tx.QueryRow(ctx, `SELECT e.id,e.after,e.at FROM events e JOIN recurrences r ON r.id=$3 WHERE e.type='release.published' AND e.node_id=$1 AND e.id>$2 AND `+eventDueSQL+`<=$4 ORDER BY e.id DESC LIMIT 1`, r.ProjectID, r.EventCursor, r.ID, now).Scan(&id, &raw, &eventAt); err != nil {
				return err
			}
			var pub Publication
			if err = json.Unmarshal(raw, &pub); err != nil {
				return err
			}
			if pub.PublishedAt.IsZero() {
				pub.PublishedAt = eventAt
			}
			// Historical publication discovery, or an unsynced release during pause,
			// advances the cursor without creating stale work after resume.
			if !pub.PublishedAt.Before(r.ActiveSince) && !pub.PublishedAt.After(now) {
				key := publicationKey(pub)
				if key == "" {
					key = fmt.Sprintf("event:%d", id)
				} else {
					key = "release:" + key
				}
				// Preserve part A's event:id receipts on upgrade.
				var prior bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recurrence_occurrences WHERE recurrence_id=$1 AND source_event_id=$2)`, r.ID, id).Scan(&prior); err != nil {
					return err
				}
				if !prior {
					_, err = occur(ctx, tx, actor, r, key, eventTime(r.Trigger, pub.PublishedAt), &id, pub.Name, pub.Version)
				}
				if err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `UPDATE recurrences SET event_cursor=$2 WHERE id=$1`, r.ID, id)
			return err
		})
		if err != nil {
			return err
		}
		if !processed {
			break
		}
	}
	return nil
}

// syncPublications projects durable journey state and the binary's immutable
// release history onto release.published. Only each project's latest published
// release is needed: downtime intentionally coalesces older publications. The
// same project/version identity unifies the two sources, preventing double runs.
func (m *Module) syncPublications(ctx context.Context, actor tenant.Principal) error {
	return db.InTenant(ctx, m.pool, actor.TenantID, func(tx pgx.Tx) error {
		got, err := lock(ctx, tx, actor.TenantID, true)
		if err != nil || !got {
			return err
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (r.project_node_id) r.project_node_id::text,r.release_node_id::text,n.title,coalesce(r.version,''),r.released_at
   FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL
   WHERE r.state IN ('released','superseded') AND r.released_at IS NOT NULL AND r.released_at<=$1
   AND EXISTS(SELECT 1 FROM recurrences c WHERE c.project_id=r.project_node_id AND NOT c.paused AND c.trigger->>'kind'='event')
   ORDER BY r.project_node_id,r.released_at DESC,r.release_node_id DESC`, now)
		if err != nil {
			return err
		}
		pubs := map[string]Publication{}
		for rows.Next() {
			var p Publication
			if err = rows.Scan(&p.ProjectID, &p.ReleaseID, &p.Name, &p.Version, &p.PublishedAt); err != nil {
				rows.Close()
				return err
			}
			pubs[p.ProjectID] = p
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, p := range m.history {
			if p.Version == "" || p.PublishedAt.IsZero() || p.PublishedAt.After(now) {
				continue
			}
			var project string
			err = tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.fields->>'project_key'=$1 AND k.slug='project' AND n.deleted_at IS NULL
 AND EXISTS(SELECT 1 FROM recurrences c WHERE c.project_id=n.id AND NOT c.paused AND c.trigger->>'kind'='event')`, p.ProjectKey).Scan(&project)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			p.ProjectID = project
			if prior, ok := pubs[project]; !ok || p.PublishedAt.After(prior.PublishedAt) {
				pubs[project] = p
			}
		}
		// Lock every existing source project before the first event-counter write.
		ids := []string{}
		for id := range pubs {
			ids = append(ids, id)
		}
		rows, err = tx.Query(ctx, `SELECT id::text FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		// Collect the bounded event batch before appending: no new node locks later.
		pending := []struct {
			p   Publication
			key string
		}{}
		for _, p := range pubs {
			key := p.ProjectID + "/version:" + p.Version
			if p.Version == "" && p.ReleaseID != nil {
				key = p.ProjectID + "/node:" + *p.ReleaseID
			}
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE type='release.published' AND metadata->>'publication_key'=$1)`, key).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				pending = append(pending, struct {
					p   Publication
					key string
				}{p, key})
			}
		}
		for i, item := range pending {
			if i == batchSize {
				break
			}
			meta, _ := json.Marshal(map[string]string{"job": Job, "publication_key": item.key})
			if _, err = events.Append(ctx, tx, actor, events.Change{NodeID: &item.p.ProjectID, Type: "release.published", After: item.p, Metadata: meta}); err != nil {
				return err
			}
		}
		return nil
	})
}
