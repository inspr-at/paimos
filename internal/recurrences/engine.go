// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
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

// WithReleaseSubscriptionGate binds S04's live, person-owned output-project
// switch. The callback runs under the tenant/tree fence, must use tx and must
// not perform external work or acquire locks after the event counter. A missing
// callback is disabled; this slice cannot invent project execution consent.
func (m *Module) WithReleaseSubscriptionGate(gate func(context.Context, pgx.Tx, string) (bool, error)) *Module {
	m.releaseSubscriptionGate = gate
	return m
}

func (m *Module) subscriptionEnabled(ctx context.Context, tx pgx.Tx, project string) (bool, error) {
	if m.releaseSubscriptionGate == nil {
		return false, nil
	}
	return m.releaseSubscriptionGate(ctx, tx, project)
}

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
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recurrences WHERE NOT paused AND retired_at IS NULL)`).Scan(&active)
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
	if err = m.syncSubscriptionPublications(ctx, actor); err != nil {
		return err
	}
	if err = m.syncPublications(ctx, actor); err != nil {
		return err
	}
	failedIDs := []string{}
	deferredIDs := []string{}
	claimedIDs := []string{}
	failures := []error{}
	for i := 0; i < batchSize; i++ {
		processed := false
		var claimed Recurrence
		err = db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
			// Each claim bounds database work before scanning source events or
			// holding the access/tree fence; a timeout rolls back the whole unit.
			if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
				return err
			}
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
			r, err := scanRecurrence(tx.QueryRow(ctx, `SELECT `+recurrenceColumns+` FROM recurrences r WHERE NOT paused AND NOT (id=ANY($2::uuid[])) AND NOT (id=ANY($3::uuid[])) AND
    retired_at IS NULL AND
    ((trigger->>'kind'='time' AND next_at<=$1) OR (trigger->>'kind'='event' AND EXISTS(SELECT 1 FROM events e WHERE (`+sourceTypeSQL+`) AND e.id>r.event_cursor))) ORDER BY next_at NULLS LAST,cardinality(array_positions($4::uuid[],id)),event_cursor,id LIMIT 1`, now, failedIDs, deferredIDs, claimedIDs))
			claimed = r
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
			if r.Trigger.Event != "release.published" {
				return m.consumeSource(ctx, tx, actor, r, now, &deferredIDs)
			}
			if releaseSubscription(r.Input) {
				return m.consumePublication(ctx, tx, actor, r, now, &deferredIDs)
			}
			var id int64
			var eventAt time.Time
			if err = tx.QueryRow(ctx, `SELECT e.id,e.at FROM events e JOIN recurrences r ON r.id=$3 WHERE e.type='release.published' AND e.node_id=$1 AND e.id>$2 AND `+eventDueSQL+`<=$4 ORDER BY e.id DESC LIMIT 1`, r.ProjectID, r.EventCursor, r.ID, now).Scan(&id, &eventAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// Apply delays only after claiming this row, so a malformed
					// publication cannot abort the query for the whole tenant.
					// Yield deferred rows for this pass without advancing the cursor.
					deferredIDs = append(deferredIDs, r.ID)
					return nil
				}
				return err
			}
			source, key, err := sourceContext(ctx, tx, tenantID, r, []sourceCandidate{{id: id, at: eventAt}})
			if err != nil {
				return err
			}
			// Historical publication discovery, or an unsynced release during pause,
			// advances the cursor without creating stale work after resume.
			if !source.publishedAt.Before(r.ActiveSince) && !source.publishedAt.After(now) {
				// Preserve part A's event:id receipts on upgrade.
				var prior bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recurrence_occurrences WHERE recurrence_id=$1 AND source_event_id=$2)`, r.ID, id).Scan(&prior); err != nil {
					return err
				}
				if !prior {
					_, err = occurSource(ctx, tx, actor, r, key, eventTime(r.Trigger, source.publishedAt), &id, source.Name, source.Version, source)
				}
				if err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `UPDATE recurrences SET event_cursor=$2 WHERE id=$1`, r.ID, id)
			return err
		})
		if err != nil {
			if claimed.ID == "" || ctx.Err() != nil {
				return errors.Join(append(failures, err)...)
			}
			// A bad row must not repeatedly win LIMIT 1. Keep its durable cursor
			// for a later retry, but exclude it for the rest of this bounded pass.
			// Its transaction rolled back, so partial tickets/receipts cannot leak.
			failedIDs = append(failedIDs, claimed.ID)
			failure := fmt.Errorf("recurrence %s: %w", claimed.ID, err)
			failures = append(failures, failure)
			slog.ErrorContext(ctx, "recurrence failed; continuing tenant pass", "recurrence_id", claimed.ID, "err", err)
			if err := m.recordFailure(ctx, actor, claimed); err != nil {
				failures = append(failures, fmt.Errorf("record recurrence %s failure: %w", claimed.ID, err))
			}
			continue
		}
		if !processed {
			break
		}
		// Every pending event recurrence gets a turn before another receives a
		// second claim. Cursor order breaks ties across passes; the history is
		// bounded by batchSize and never changes the durable definition.
		claimedIDs = append(claimedIDs, claimed.ID)
	}
	return errors.Join(failures...)
}

// Explicit subscriptions consume one oldest publication per transaction. A
// blocked overlap or delayed publication creates no receipt and advances no
// cursor; replicas and retries return to the same durable publication.
func (m *Module) consumePublication(ctx context.Context, tx pgx.Tx, actor tenant.Principal, r Recurrence, now time.Time, deferred *[]string) error {
	enabled, err := m.subscriptionEnabled(ctx, tx, r.ProjectID)
	if err != nil {
		return err
	}
	if !enabled {
		*deferred = append(*deferred, r.ID)
		return nil
	}
	var id int64
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT e.id,e.at FROM events e JOIN recurrences r ON r.id=$1
 WHERE e.id>$2 AND `+sourceTypeSQL+` ORDER BY e.id LIMIT 1`, r.ID, r.EventCursor).Scan(&id, &at)
	if err != nil {
		return err
	}
	source, key, err := sourceContext(ctx, tx, actor.TenantID, r, []sourceCandidate{{id: id, at: at}})
	if err != nil {
		return err
	}
	if !source.publishedAt.Before(r.ActiveSince) {
		if eventTime(r.Trigger, source.publishedAt).After(now) {
			*deferred = append(*deferred, r.ID)
			return nil
		}
		// Recognise old event:id receipts as well as the canonical key.
		var prior bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recurrence_occurrences WHERE recurrence_id=$1 AND source_event_id=$2)`, r.ID, id).Scan(&prior); err != nil {
			return err
		}
		if !prior {
			_, err = occurSource(ctx, tx, actor, r, key, eventTime(r.Trigger, source.publishedAt), &id, source.Name, source.Version, source)
			if errors.Is(err, errPublicationPending) {
				*deferred = append(*deferred, r.ID)
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE recurrences SET event_cursor=$2 WHERE id=$1`, r.ID, id)
	return err
}

// Persist a value-free diagnostic after the failed occurrence rolled back.
// Existing node locks precede the recurrence lock and the event counter stays
// last. Foreground work can make us yield; the returned/logged failure remains
// visible and the next tenant pass retries the row.
func (m *Module) recordFailure(ctx context.Context, actor tenant.Principal, r Recurrence) error {
	return db.InTenant(ctx, m.pool, actor.TenantID, func(tx pgx.Tx) error {
		got, err := lock(ctx, tx, actor.TenantID, true)
		if err != nil || !got {
			return err
		}
		var id string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE id=$1 FOR KEY SHARE`, r.ProjectID).Scan(&id); err != nil {
			return err
		}
		if _, err = load(ctx, tx, r.ID, true); err != nil {
			return err
		}
		return record(ctx, tx, actor, r.ProjectID, "recurrence.failed", nil, map[string]string{"recurrence_id": r.ID, "reason": "processing_failed"})
	})
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
   AND EXISTS(SELECT 1 FROM recurrences c WHERE c.project_id=r.project_node_id AND NOT c.paused AND c.retired_at IS NULL AND c.trigger->>'kind'='event' AND NOT (c.trigger->>'event'='release.published' AND coalesce(c.trigger#>'{filter,project_ids}','[]'::jsonb)<>'[]'::jsonb))
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
 AND EXISTS(SELECT 1 FROM recurrences c WHERE c.project_id=n.id AND NOT c.paused AND c.retired_at IS NULL AND c.trigger->>'kind'='event' AND NOT (c.trigger->>'event'='release.published' AND coalesce(c.trigger#>'{filter,project_ids}','[]'::jsonb)<>'[]'::jsonb))`, p.ProjectKey).Scan(&project)
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

// The immutable binary history supplies all publications for explicit source
// subscriptions. Existing release events are also consumed directly. This does
// not add a publisher to the retired Journey system or copy source work items.
func (m *Module) syncSubscriptionPublications(ctx context.Context, actor tenant.Principal) error {
	if m.releaseSubscriptionGate == nil {
		return nil
	}
	return db.InTenant(ctx, m.pool, actor.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		got, err := lock(ctx, tx, actor.TenantID, true)
		if err != nil || !got {
			return err
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		history := append([]Publication(nil), m.history...)
		sort.SliceStable(history, func(i, j int) bool { return history[i].PublishedAt.Before(history[j].PublishedAt) })
		pending := []Publication{}
		seen := map[string]bool{}
		for _, p := range history {
			if len(pending) == batchSize {
				break
			}
			if p.Version == "" || len(p.Version) > 128 || p.PublishedAt.IsZero() || p.PublishedAt.After(now) {
				continue
			}
			// Query only enabled candidates with still-unsynced publications. A
			// bounded page cannot keep old history ahead of later publications.
			rows, err := tx.Query(ctx, `SELECT DISTINCT n.id::text,r.project_id::text FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 JOIN recurrences r ON (r.trigger#>'{filter,project_ids}') ? n.id::text
 WHERE n.fields->>'project_key'=$1 AND n.deleted_at IS NULL AND k.slug='project'
 AND NOT r.paused AND r.retired_at IS NULL AND `+releaseSubscriptionSQL+`
 AND NOT EXISTS(SELECT 1 FROM events e WHERE e.type='release.published' AND e.metadata->>'publication_key'=n.id::text||'/version:'||$2)
 ORDER BY n.id::text,r.project_id::text LIMIT 101`, p.ProjectKey, p.Version)
			if err != nil {
				return err
			}
			type candidate struct{ source, target string }
			candidates := []candidate{}
			for rows.Next() {
				var c candidate
				if err := rows.Scan(&c.source, &c.target); err != nil {
					rows.Close()
					return err
				}
				candidates = append(candidates, c)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(candidates) > 100 {
				return workorders.Fail(409, "release source subscription page exceeds limits")
			}
			for _, c := range candidates {
				enabled, err := m.subscriptionEnabled(ctx, tx, c.target)
				if err != nil {
					return err
				}
				p.ProjectID, p.Name = c.source, boundedText(p.Name, 256)
				if key := publicationKey(p); enabled && !seen[key] && len(pending) < batchSize {
					seen[key] = true
					pending = append(pending, p)
				}
			}
		}
		ids := make([]string, 0, len(pending))
		for _, p := range pending {
			ids = append(ids, p.ProjectID)
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, p := range pending {
			meta, _ := json.Marshal(map[string]string{"job": Job, "publication_key": publicationKey(p)})
			if _, err := events.Append(ctx, tx, actor, events.Change{NodeID: &p.ProjectID, Type: "release.published", After: p, Metadata: meta}); err != nil {
				return err
			}
		}
		return nil
	})
}
