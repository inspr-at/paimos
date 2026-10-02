// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// validateTarget locks existing node rows in UUID order before the recurrence
// row or any event. It rechecks the live parent/project relationship on every
// occurrence, so a moved/deleted parent never writes into another project.
func validateTarget(ctx context.Context, tx pgx.Tx, in Input, lockNodes bool) error {
	ids := append([]string{in.ProjectID, in.ParentID}, in.Template.Tags...)
	sort.Strings(ids)
	query := `SELECT n.id::text FROM nodes n WHERE n.id=ANY($1::uuid[]) ORDER BY n.id`
	if lockNodes {
		query += ` FOR UPDATE OF n`
	}
	rows, err := tx.Query(ctx, query, ids)
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
	var valid, allows bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes project JOIN node_kinds pk ON pk.tenant_id=project.tenant_id AND pk.id=project.kind_id WHERE project.id=$1 AND pk.slug='project' AND project.deleted_at IS NULL),
 k.allowed_child_kinds IS NULL OR $3=ANY(k.allowed_child_kinds)
 FROM nodes parent JOIN node_kinds k ON k.tenant_id=parent.tenant_id AND k.id=parent.kind_id WHERE parent.id=$2 AND parent.project_id=$1 AND parent.deleted_at IS NULL`, in.ProjectID, in.ParentID, in.Template.Type).Scan(&valid, &allows)
	if err != nil {
		return err
	}
	if !valid {
		return pgx.ErrNoRows
	}
	if !allows {
		return workorders.Fail(409, "parent kind cannot contain the template type")
	}
	var tags int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL AND k.slug='tag' AND (n.project_id IS NULL OR n.project_id=$2)`, in.Template.Tags, in.ProjectID).Scan(&tags)
	if err != nil {
		return err
	}
	if tags != len(in.Template.Tags) {
		return workorders.Fail(400, "tag not found in this project or workspace")
	}
	return nil
}
func ensureActor(ctx context.Context, m *Module, tenantID string) (tenant.Principal, error) {
	p := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Name: "Recurring work", Roles: []string{"recurring_work"}}
	err := db.InTenant(db.AllProjects(ctx, "recurring work actor provisioning"), m.pool, tenantID, func(tx pgx.Tx) error {
		// First-use principal-link locks and the principal.created event commit in
		// their own transaction, before any ticket tree lock (as in status autopilot).
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-recurring-actor:'||$1,0))`, tenantID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND name='Recurring work' AND roles @> ARRAY['recurring_work']::text[]`).Scan(&p.ID)
		created := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !created {
			return err
		}
		if created {
			if err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','Recurring work',ARRAY['recurring_work']) RETURNING id::text`, tenantID).Scan(&p.ID); err != nil {
				return err
			}
		}
		// Provision the established keyless queue holder before any tree lock.
		if _, err = tx.Exec(ctx, `INSERT INTO principals(id,tenant_id,kind,name) VALUES($1,$1,'agent','Next free agent (queue holder)') ON CONFLICT(id) DO NOTHING`, tenantID); err != nil {
			return err
		}
		if !created {
			return nil
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "principal.created", After: map[string]any{"id": p.ID, "kind": "agent", "name": p.Name, "roles": p.Roles}})
		return err
	})
	return p, err
}
func (m *Module) runNow(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Key        string `json:"idempotency_key"`
		Revision   int64  `json:"expected_revision"`
		Force      bool   `json:"force_overlap"`
		ReleaseKey string `json:"release_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Key) == "" || len(in.Key) > 128 || len(in.ReleaseKey) > 256 || in.Revision < 0 {
		httpError(w, 400, "idempotency_key must be 1..128 bytes")
		return
	}
	// Provision the system principal only after an initial caller-scoped check.
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		return authorizeDefinition(r.Context(), tx, p, item.Input)
	})
	if err != nil {
		reply(w, 200, nil, err)
		return
	}
	actor, err := ensureActor(r.Context(), m, p.TenantID)
	if err != nil {
		reply(w, 200, nil, err)
		return
	}
	var out Occurrence
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := lock(r.Context(), tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(r.Context(), tx, r.PathValue("recurrenceId"), false)
		if err != nil {
			return err
		}
		if err = authorizeDefinition(r.Context(), tx, p, item.Input); err != nil {
			return err
		}
		if in.Revision != 0 && in.Revision != item.Revision {
			return workorders.Fail(409, "recurrence revision changed")
		}
		now, err := m.clock(r.Context(), tx)
		if err != nil {
			return err
		}
		key, name, version := "manual:"+in.Key, "", ""
		if in.ReleaseKey != "" {
			if item.Trigger.Kind != "event" {
				return workorders.Fail(400, "release_key requires an event trigger")
			}
			choices, _, err := m.releases(r.Context(), tx, item, now)
			if err != nil {
				return err
			}
			found := false
			for _, choice := range choices {
				if choice.Key == in.ReleaseKey {
					found = true
					if choice.Receipt != nil {
						out = choice.Receipt.Occurrence
						return nil
					}
					key = "release:" + choice.Key
					name = choice.Name
					version = choice.Version
					break
				}
			}
			if !found {
				return workorders.Fail(404, "published release not found")
			}
		}
		out, err = occur(r.Context(), tx, actor, item, key, now, nil, name, version, in.Force)
		return err
	})
	reply(w, 200, out, err)
}
func httpError(w http.ResponseWriter, status int, message string) {
	workorders.WriteError(w, workorders.Fail(status, message))
}

// occur takes no event lock until all the ticket, receipt, order and run writes
// have finished. A failure rolls the entire transaction back; the durable key
// and count are committed atomically, including overlap skips.
func occur(ctx context.Context, tx pgx.Tx, actor tenant.Principal, r Recurrence, key string, at time.Time, eventID *int64, name, version string, forceOverlap ...bool) (Occurrence, error) {
	existing, err := scanOccurrence(tx.QueryRow(ctx, `SELECT `+occurrenceColumns+` FROM recurrence_occurrences WHERE recurrence_id=$1 AND occurrence_key=$2`, r.ID, key))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Occurrence{}, err
	}
	reason := ""
	// Target locks precede recurrence/receipt locks. The tree lock serializes all
	// structural writers and preceding ticket status edits for the overlap check.
	if err = validateTarget(ctx, tx, r.Input, true); err != nil {
		var he *workorders.Error
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &he) {
			reason = "target_unavailable"
		} else {
			return Occurrence{}, err
		}
	}
	r, err = load(ctx, tx, r.ID, true)
	if err != nil {
		return Occurrence{}, err
	}
	if reason == "" && r.OverlapPolicy == "skip" && !(len(forceOverlap) > 0 && forceOverlap[0]) {
		var open bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recurrence_occurrences o JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.node_id WHERE o.recurrence_id=$1 AND n.deleted_at IS NULL AND regexp_replace(lower(btrim(n.state)),'[[:space:]-]+','_','g') NOT IN ('done','cancelled','canceled','archived','delivered','accepted'))`, r.ID).Scan(&open)
		if err != nil {
			return Occurrence{}, err
		}
		if open {
			reason = "previous_occurrence_open"
		}
	}
	o := Occurrence{RecurrenceID: r.ID, Key: key, Number: r.OccurrenceCount + 1, ScheduledAt: at, SourceEventID: eventID, Outcome: "skipped", Reason: reason}
	changes := []events.Change{}
	if reason == "" {
		title := render(r.Template.Title, o.Number, at, r.Trigger, name, version)
		body := render(r.Template.Description, o.Number, at, r.Trigger, name, version)
		criteria := make([]string, len(r.Template.Criteria))
		for i, c := range r.Template.Criteria {
			criteria[i] = render(c, o.Number, at, r.Trigger, name, version)
		}
		if strings.TrimSpace(title) == "" || len(title) > 512 || len(body) > 65536 {
			o.Reason = "rendered_template_invalid"
		} else {
			tags := []string{}
			for _, tag := range r.Template.Tags {
				var tagName string
				if err = tx.QueryRow(ctx, `SELECT title FROM nodes WHERE id=$1`, tag).Scan(&tagName); err != nil {
					return Occurrence{}, err
				}
				tags = append(tags, tagName)
			}
			fields := map[string]any{"type": r.Template.Type, "tags": tags, "priority": r.Template.Priority, "acceptance_criteria": criteria, "recurrence_id": r.ID, "occurrence_key": key, "occurrence_number": o.Number}
			if r.Template.EstimateHours > 0 {
				fields["estimate_hours"] = r.Template.EstimateHours
				fields["estimate_source"] = "agent"
				fields["estimate_by"] = actor.ID
				fields["estimate_at"] = at.UTC().Format(time.RFC3339Nano)
			}
			for _, c := range criteria {
				if strings.TrimSpace(c) == "" || len(c) > 4096 {
					o.Reason = "rendered_template_invalid"
				}
			}
			if o.Reason == "" {
				// Resolve only the local kind schema; no remote loaders. Custom tenant
				// schemas remain authoritative for these ordinary node fields.
				var schemaJSON []byte
				var schema jsonschema.Schema
				if err = tx.QueryRow(ctx, `SELECT field_schema FROM node_kinds WHERE slug=$1`, r.Template.Type).Scan(&schemaJSON); err != nil {
					return Occurrence{}, err
				}
				if err = json.Unmarshal(schemaJSON, &schema); err != nil {
					return Occurrence{}, err
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					return Occurrence{}, err
				}
				if err = resolved.Validate(fields); err != nil {
					o.Reason = "template_schema_invalid"
				}
			}
			if o.Reason == "" {
				raw, _ := json.Marshal(fields)
				var id string
				var snapshot json.RawMessage
				err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,parent_id,title,body,fields,state,position)
     SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,$2,$3,$4,$5,'open',
     coalesce((SELECT max(position)+1024 FROM nodes WHERE parent_id=$2 AND deleted_at IS NULL),1024)
     FROM node_kinds k WHERE k.slug=$6 RETURNING id::text,to_jsonb(nodes)-'tenant_id'`, actor.TenantID, r.ParentID, title, body, raw, r.Template.Type).Scan(&id, &snapshot)
				if err != nil {
					return Occurrence{}, err
				}
				o.NodeID = &id
				o.Outcome = "created"
				changes = append(changes, events.Change{NodeID: &id, Type: "node.created", After: snapshot})
				if r.QueueEach {
					queueChanges, err := workqueue.EnqueueSystem(ctx, tx, actor, id)
					if err != nil {
						return Occurrence{}, err
					}
					changes = append(changes, queueChanges...)
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE recurrences SET occurrence_count=$2,updated_at=clock_timestamp() WHERE id=$1`, r.ID, o.Number); err != nil {
		return Occurrence{}, err
	}
	o, err = scanOccurrence(tx.QueryRow(ctx, `INSERT INTO recurrence_occurrences(tenant_id,recurrence_id,occurrence_key,number,scheduled_at,node_id,source_event_id,outcome,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+occurrenceColumns, actor.TenantID, r.ID, key, o.Number, at, o.NodeID, eventID, o.Outcome, o.Reason))
	if err != nil {
		return Occurrence{}, err
	}
	typ := "recurrence.occurred"
	if o.Outcome == "skipped" {
		typ = "recurrence.skipped"
	}
	changes = append(changes, events.Change{NodeID: &r.ProjectID, Type: typ, After: o})
	metadata, _ := json.Marshal(map[string]any{"job": Job, "recurrence_id": r.ID, "occurrence_key": key, "reason": o.Reason})
	for _, change := range changes {
		change.Metadata = metadata
		if _, err = events.Append(ctx, tx, actor, change); err != nil {
			return Occurrence{}, err
		}
	}
	return o, nil
}
