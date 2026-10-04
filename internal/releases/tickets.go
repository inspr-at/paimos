// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type ticketInput struct {
	Title          string  `json:"title"`
	FeatureID      *string `json:"feature_node_id,omitempty"`
	Included       bool    `json:"included"`
	Revision       int64   `json:"expected_revision"`
	IdempotencyKey string  `json:"idempotency_key"`
}

func (m *module) createTicket(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	in := ticketInput{Included: true}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	// Reject explicit nulls: all supplied fields have non-null contract types.
	var raw map[string]json.RawMessage
	if err := d.Decode(&raw); err != nil || raw == nil || d.Decode(new(any)) != io.EOF {
		respond(w, nil, fail(400, "invalid ticket"))
		return
	}
	for _, v := range raw {
		if string(v) == "null" {
			respond(w, nil, fail(400, "ticket fields cannot be null"))
			return
		}
	}
	body, _ := json.Marshal(raw)
	d = json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		respond(w, nil, fail(400, "invalid ticket"))
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 512 || in.Revision < 1 || strings.TrimSpace(in.IdempotencyKey) == "" || utf8.RuneCountInString(in.IdempotencyKey) > 128 {
		respond(w, nil, fail(400, "title, expected_revision and idempotency_key are required within their limits"))
		return
	}
	if in.FeatureID != nil {
		id := strings.ToLower(*in.FeatureID)
		if !uuid.MatchString(id) {
			respond(w, nil, fail(400, "invalid feature_node_id"))
			return
		}
		in.FeatureID = &id
	}
	var out Walker
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = addTicket(r.Context(), tx, p, strings.ToLower(r.PathValue("projectId")), strings.ToLower(r.PathValue("releaseId")), in)
		return err
	})
	if err == nil {
		out.Warnings = []string{"Tickets need English and German pills and benefits before Done; edit them in ticket details."}
	}
	respond(w, out, err)
}

func addTicket(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, release string, in ticketInput) (Walker, error) {
	var out Walker
	// Match R1 structural writes and the release planner's lock order.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
		return out, err
	}
	var person bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='person')`, p.ID).Scan(&person); err != nil {
		return out, err
	}
	if !person {
		return out, fail(403, "person required")
	}
	var current *string
	if err := tx.QueryRow(ctx, `SELECT current_release_node_id::text FROM journey_projects WHERE project_node_id=$1 FOR UPDATE`, project).Scan(&current); err != nil {
		return out, err
	}
	var state string
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT state,revision FROM journey_releases WHERE project_node_id=$1 AND release_node_id=$2 FOR UPDATE`, project, release).Scan(&state, &revision); err != nil {
		return out, err
	}
	// Bind the receipt to this operation, release and person as well as its body.
	raw, err := json.Marshal(struct {
		Operation string
		Release   string
		Actor     string
		Input     ticketInput
	}{"release.create_ticket", release, p.ID, in})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var previous string
	err = tx.QueryRow(ctx, `SELECT request_sha256 FROM journey_action_receipts WHERE project_node_id=$1 AND idempotency_key=$2`, project, in.IdempotencyKey).Scan(&previous)
	if err == nil {
		if previous != hash {
			return out, fail(409, "idempotency key was used for a different request")
		}
		return load(ctx, tx, project, release)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if state != "planning" || current == nil || *current != release {
		return out, fail(409, "tickets can only be created for the current planning release")
	}
	if revision != in.Revision {
		return out, fail(409, "release revision changed")
	}
	before, err := load(ctx, tx, project, release)
	if err != nil {
		return out, err
	}
	parent := project
	if in.FeatureID != nil {
		// A feature is its epic node, and must still be beneath this project.
		err = tx.QueryRow(ctx, `WITH RECURSIVE tree AS (
   SELECT id FROM nodes WHERE id=$1 AND deleted_at IS NULL
   UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
  ) SELECT f.feature_node_id::text FROM journey_features f
  JOIN tree t ON t.id=f.feature_node_id
  JOIN nodes n ON n.id=f.feature_node_id AND n.tenant_id=f.tenant_id
  JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id
  WHERE f.project_node_id=$1 AND f.feature_node_id=$2 AND k.slug IN ('work','epic')`, project, *in.FeatureID).Scan(&parent)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fail(404, "feature not found in project")
		}
		if err != nil {
			return out, err
		}
	}
	var allows bool
	if err := tx.QueryRow(ctx, `SELECT k.allowed_child_kinds IS NULL OR CASE WHEN EXISTS(SELECT 1 FROM node_kinds WHERE slug='work') THEN 'work' ELSE 'ticket' END=ANY(k.allowed_child_kinds)
  FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id
  WHERE n.id=$1 AND n.deleted_at IS NULL`, parent).Scan(&allows); err != nil {
		return out, err
	}
	if !allows {
		return out, fail(409, "parent kind cannot contain a ticket")
	}
	var schema jsonschema.Schema
	var schemaJSON []byte
	if err := tx.QueryRow(ctx, `SELECT field_schema FROM node_kinds WHERE slug IN ('work','ticket') ORDER BY (slug='work') DESC LIMIT 1`).Scan(&schemaJSON); err != nil {
		return out, err
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return out, err
	}
	resolved, err := schema.Resolve(nil) // No remote schema loader.
	if err != nil {
		return out, err
	}
	if err := resolved.Validate(map[string]any{}); err != nil {
		return out, fail(409, "ticket kind requires fields unavailable in quick creation")
	}
	var id string
	var snapshot json.RawMessage
	err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title,position)
  SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,$3,
   coalesce((SELECT max(position)+1024 FROM nodes WHERE parent_id=$2 AND deleted_at IS NULL),1024)
  FROM node_kinds k WHERE k.slug IN ('work','ticket') ORDER BY (k.slug='work') DESC LIMIT 1
  RETURNING id::text,to_jsonb(nodes)||jsonb_build_object('position',position::text)`, p.TenantID, parent, in.Title).Scan(&id, &snapshot)
	if err != nil {
		return out, err
	}
	if _, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "node.created", After: snapshot}); err != nil {
		return out, err
	}
	var assigned *string
	if in.Included {
		assigned = &release
	}
	_, err = tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,feature_node_id,release_node_id,walker_position,source,scope_revision_required)
  VALUES($1,$2,$3,$4,$5,(SELECT coalesce(max(walker_position)+1,0) FROM journey_tickets WHERE project_node_id=$3),'manual',true)`, p.TenantID, id, project, in.FeatureID, assigned)
	if err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_releases SET revision=revision+1 WHERE release_node_id=$1`, release); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_projects SET revision=revision+1,updated_at=now() WHERE project_node_id=$1`, project); err != nil {
		return out, err
	}
	out, err = load(ctx, tx, project, release)
	if err != nil {
		return out, err
	}
	event, err := events.Append(ctx, tx, p, events.Change{NodeID: &release, Type: "journey.release_planned", Before: before, After: out})
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO journey_action_receipts(tenant_id,project_node_id,idempotency_key,request_sha256,resulting_revision,event_id) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, project, in.IdempotencyKey, hash, out.Revision, event.ID)
	return out, err
}
