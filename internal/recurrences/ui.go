// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workstate"
	"github.com/jackc/pgx/v5"
)

// Retirement is row state plus an append-only audit event. Keeping the row
// retains receipts and ticket provenance; event visibility never decides whether
// the definition can be read, resumed or scheduled.
func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	m.setPaused(w, r, true, true)
}

func (m *Module) previewDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in Input
	if !decode(w, r, &in) {
		return
	}
	times := []time.Time{}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := manage(r.Context(), tx, p, in.ProjectID); err != nil {
			return err
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		if err = in.normalize(now); err != nil {
			return workorders.Fail(400, err.Error())
		}
		if err = authorizeSources(r.Context(), tx, p, in); err != nil {
			return err
		}
		if err = validateTarget(r.Context(), tx, in, false); err != nil {
			return err
		}
		times, err = Preview(in.Trigger, now, 4)
		return err
	})
	reply(w, 200, map[string]any{"times": times, "trigger_kind": in.Trigger.Kind}, err)
}

// One bounded SQL read decorates a whole page, rather than fetching each row's
// last result and overlap independently. Node joins obey the caller's project RLS.
func results(ctx context.Context, tx pgx.Tx, items []Recurrence, apply func([]Recurrence)) error {
	ids := make([]string, len(items))
	indices := map[string]int{}
	for i, item := range items {
		ids[i] = item.ID
		indices[item.ID] = i
	}
	cte, join, predicate := workstate.ReadSQL("n", "cfg")
	rows, err := tx.Query(ctx, `WITH `+cte+` SELECT DISTINCT ON (o.recurrence_id,kind) o.recurrence_id::text,kind,to_jsonb(o)-'tenant_id',coalesce(n.key,''),coalesce(n.title,''),coalesce(n.state,'')
 FROM recurrence_occurrences o LEFT JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.node_id AND n.deleted_at IS NULL
 `+join+` CROSS JOIN LATERAL (SELECT 'last' AS kind UNION ALL SELECT 'open' WHERE n.id IS NOT NULL AND `+predicate+`) k
 WHERE o.recurrence_id=ANY($1::uuid[]) ORDER BY o.recurrence_id,kind,o.number DESC`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind string
		var raw []byte
		var result Result
		if err = rows.Scan(&id, &kind, &raw, &result.NodeKey, &result.Title, &result.State); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &result.Occurrence); err != nil {
			return err
		}
		if kind == "last" {
			items[indices[id]].LastResult = &result
		} else {
			items[indices[id]].OpenPrevious = &result
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	apply(items)
	return nil
}

type HistoryEntry struct {
	ID     int64             `json:"id"`
	Type   string            `json:"type"`
	At     time.Time         `json:"at"`
	Actor  string            `json:"actor_name"`
	Before json.RawMessage   `json:"before"`
	After  json.RawMessage   `json:"after"`
	Node   map[string]string `json:"node"`
}

func (m *Module) listHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			httpError(w, 400, "invalid history cursor")
			return
		}
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "created" && filter != "skipped" && filter != "changes" {
		httpError(w, 400, "invalid history filter")
		return
	}
	items := []HistoryEntry{}
	var next *int64
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = read(r.Context(), tx, p, item.ProjectID); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT e.id,e.type,e.at,coalesce(p.name,'Recurring work'),e.before,e.after,n.key,n.title,n.state
   FROM events e LEFT JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
   LEFT JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id::text=e.after->>'node_id' AND n.deleted_at IS NULL
   WHERE e.node_id=$1 AND e.type IN ('recurrence.created','recurrence.updated','recurrence.paused','recurrence.resumed','recurrence.occurred','recurrence.skipped')
   AND (e.metadata->>'recurrence_id'=$2 OR e.after->>'id'=$2 OR e.after->>'recurrence_id'=$2)
   AND ($3::bigint=0 OR e.id<$3) AND ($4='all' OR ($4='created' AND e.type='recurrence.occurred') OR ($4='skipped' AND e.type='recurrence.skipped') OR ($4='changes' AND e.type NOT IN ('recurrence.occurred','recurrence.skipped')))
   ORDER BY e.id DESC LIMIT 51`, item.ProjectID, item.ID, before, filter)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h HistoryEntry
			var key, title, state *string
			if err = rows.Scan(&h.ID, &h.Type, &h.At, &h.Actor, &h.Before, &h.After, &key, &title, &state); err != nil {
				return err
			}
			h.At = h.At.UTC()
			if key != nil {
				h.Node = map[string]string{"key": *key, "title": *title, "state": *state}
			}
			items = append(items, h)
		}
		if len(items) > 50 {
			cursor := items[49].ID
			next = &cursor
			items = items[:50]
		}
		return rows.Err()
	})
	reply(w, 200, map[string]any{"items": items, "next_cursor": next}, err)
}

type ReleaseChoice struct {
	Publication
	Key     string  `json:"key"`
	Receipt *Result `json:"receipt,omitempty"`
}

func publicationKey(p Publication) string {
	if p.Version != "" {
		return p.ProjectID + "/version:" + p.Version
	}
	if p.ReleaseID != nil {
		return p.ProjectID + "/node:" + *p.ReleaseID
	}
	return ""
}
func (m *Module) releases(ctx context.Context, tx pgx.Tx, item Recurrence, now time.Time) ([]ReleaseChoice, bool, error) {
	rows, err := tx.Query(ctx, `SELECT r.release_node_id::text,n.title,coalesce(r.version,''),r.released_at FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL WHERE r.project_node_id=$1 AND r.state IN ('released','superseded') AND r.released_at IS NOT NULL AND r.released_at<=$2 ORDER BY r.released_at DESC,r.release_node_id DESC LIMIT 101`, item.ProjectID, now)
	if err != nil {
		return nil, false, err
	}
	pubs := map[string]ReleaseChoice{}
	for rows.Next() {
		var p Publication
		p.ProjectID = item.ProjectID
		if err = rows.Scan(&p.ReleaseID, &p.Name, &p.Version, &p.PublishedAt); err != nil {
			rows.Close()
			return nil, false, err
		}
		key := publicationKey(p)
		pubs[key] = ReleaseChoice{Publication: p, Key: key}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	var projectKey string
	if err = tx.QueryRow(ctx, `SELECT coalesce(fields->>'project_key','') FROM nodes WHERE id=$1 AND deleted_at IS NULL`, item.ProjectID).Scan(&projectKey); err != nil {
		return nil, false, err
	}
	for _, p := range m.history {
		if p.ProjectKey != projectKey || p.PublishedAt.IsZero() || p.PublishedAt.After(now) {
			continue
		}
		p.ProjectID = item.ProjectID
		key := publicationKey(p)
		if key != "" {
			pubs[key] = ReleaseChoice{Publication: p, Key: key}
		}
	}
	out := make([]ReleaseChoice, 0, len(pubs))
	for _, p := range pubs {
		out = append(out, p)
	}
	sortReleases(out)
	truncated := len(out) > 100
	if truncated {
		out = out[:100]
	}
	// Bounded by the choice page and fetched once. Existing event:id receipts
	// from part A are also recognised, so upgrading cannot duplicate a release.
	receiptRows, err := tx.Query(ctx, `SELECT to_jsonb(o)-'tenant_id',coalesce(n.key,''),coalesce(n.title,''),coalesce(n.state,''),coalesce(e.metadata->>'publication_key','') FROM recurrence_occurrences o LEFT JOIN events e ON e.tenant_id=o.tenant_id AND e.id=o.source_event_id LEFT JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.node_id AND n.deleted_at IS NULL WHERE o.recurrence_id=$1 AND (o.occurrence_key=ANY($2::text[]) OR e.metadata->>'publication_key'=ANY($3::text[]))`, item.ID, releaseReceiptKeys(out), releaseKeys(out))
	if err != nil {
		return nil, false, err
	}
	defer receiptRows.Close()
	for receiptRows.Next() {
		var raw []byte
		var result Result
		var pubkey string
		if err = receiptRows.Scan(&raw, &result.NodeKey, &result.Title, &result.State, &pubkey); err != nil {
			return nil, false, err
		}
		if err = json.Unmarshal(raw, &result.Occurrence); err != nil {
			return nil, false, err
		}
		for i := range out {
			if result.Key == "release:"+out[i].Key || pubkey == out[i].Key {
				out[i].Receipt = &result
			}
		}
	}
	return out, truncated, receiptRows.Err()
}
func releaseKeys(choices []ReleaseChoice) []string {
	keys := make([]string, len(choices))
	for i, c := range choices {
		keys[i] = c.Key
	}
	return keys
}
func releaseReceiptKeys(choices []ReleaseChoice) []string {
	keys := releaseKeys(choices)
	for i := range keys {
		keys[i] = "release:" + keys[i]
	}
	return keys
}
func (m *Module) listReleases(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	items := []ReleaseChoice{}
	truncated := false
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = read(r.Context(), tx, p, item.ProjectID); err != nil {
			return err
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		items, truncated, err = m.releases(r.Context(), tx, item, now)
		return err
	})
	reply(w, 200, map[string]any{"items": items, "truncated": truncated}, err)
}

func eventTime(t Trigger, published time.Time) time.Time {
	switch t.EventStart {
	case "hour":
		return published.Add(time.Hour)
	case "morning":
		zone := t.EventTimezone
		if zone == "" {
			zone = "UTC"
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return published
		}
		day := published.In(loc)
		return time.Date(day.Year(), day.Month(), day.Day()+1, 6, 0, 0, 0, loc).UTC()
	default:
		return published
	}
}

// The SQL claim and Go renderer use the same local-date rule. Validated IANA
// timezone names are data, never SQL interpolation.
const eventDueSQL = `(CASE r.trigger->>'event_start' WHEN 'hour' THEN coalesce((e.after->>'published_at')::timestamptz,e.at)+interval '1 hour' WHEN 'morning' THEN (((coalesce((e.after->>'published_at')::timestamptz,e.at) AT TIME ZONE coalesce(nullif(r.trigger->>'event_timezone',''),'UTC'))::date+1+time '06:00') AT TIME ZONE coalesce(nullif(r.trigger->>'event_timezone',''),'UTC')) ELSE coalesce((e.after->>'published_at')::timestamptz,e.at) END)`

func sortReleases(items []ReleaseChoice) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].Key < items[j].Key
		}
		return items[i].PublishedAt.After(items[j].PublishedAt)
	})
}
