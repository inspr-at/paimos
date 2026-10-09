// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Selection grants no worker authority. Only the selected generation's lease
// can finish this transition; ordinary launch/dispatch admission stays closed.
const adoptionPending = "adoption_pending"

// selectionCleared is an unclaimed adoption whose session choice was cancelled.
// It grants no generation. Claim stays closed until an explicit start or a new adoption.
const selectionCleared = "selection_cleared"
const adoptEligibleSQL = `role='coordinator' AND parent_id IS NULL
 AND stopped_at IS NULL AND archived_at IS NULL AND phase NOT IN ('stopping','stopped')
 AND NOT coalesce(pause_record->>'state' IN ('requested','planned','paused','resume_requested'),false)
 AND NOT coalesce((pause_record->>'stop_requested')::boolean,false) AND ` + leadReportingFreshSQL

type leadCandidate struct {
	ID         string    `json:"id"`
	Label      *string   `json:"display_label"`
	Harness    string    `json:"harness"`
	Host       string    `json:"host"`
	Management string    `json:"management_mode"`
	ReportedAt time.Time `json:"reported_at"`
	createdAt  time.Time
}

func adoptionOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (string, error) {
	if p.Kind != tenant.Person {
		return "", workorders.Fail(403, "person required to adopt lead")
	}
	if err := leadOwner(ctx, tx, p, id, p.ID); err != nil {
		return "", err
	}
	var owner string
	err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1`, p.ID).Scan(&owner)
	return owner, err
}

func (m *Module) leadCandidates(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx, id := r.Context(), r.PathValue("projectId")
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: id}); err != nil {
		return nil, err
	}
	owner, err := adoptionOwner(ctx, tx, p, id)
	if err != nil {
		return nil, err
	}
	limit, err := workorders.Limit(r)
	if err != nil {
		return nil, err
	}
	// Bind cursors to the person and project; size is checked before decoding.
	fingerprint := p.TenantID + ":" + p.ID + ":" + id
	var cursor sessionCursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 2048 {
			return nil, workorders.Fail(400, "invalid cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || json.Unmarshal(b, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.At.IsZero() || !workorders.UUID(cursor.ID) {
			return nil, workorders.Fail(400, "invalid cursor")
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text,display_label,harness,host,management,coalesce(heartbeat_at,created_at),created_at FROM harness_sessions WHERE project_id=$1 AND owner_principal_id=$2 AND `+adoptEligibleSQL+` AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4::uuid)) ORDER BY created_at DESC,id DESC LIMIT $5`, id, owner, cursorTime(cursor), nullable(cursor.ID), limit+1)
	if err != nil {
		return nil, err
	}
	out := struct {
		Items      []leadCandidate `json:"items"`
		NextCursor *string         `json:"next_cursor"`
	}{Items: []leadCandidate{}}
	for rows.Next() {
		var s leadCandidate
		if err = rows.Scan(&s.ID, &s.Label, &s.Harness, &s.Host, &s.Management, &s.ReportedAt, &s.createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		out.Items = append(out.Items, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		raw, _ := json.Marshal(sessionCursor{fingerprint, last.createdAt, last.ID})
		next := base64.RawURLEncoding.EncodeToString(raw)
		out.NextCursor = &next
	}
	return out, nil
}

func (m *Module) adoptLead(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Revision  *int64 `json:"expected_revision"`
		SessionID string `json:"session_id"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision == nil || *in.Revision < 0 || !workorders.UUID(in.SessionID) {
		return nil, workorders.Fail(400, "valid revision and session required")
	}
	ctx, id := r.Context(), r.PathValue("projectId")
	if err := leadFence(ctx, tx, id); err != nil {
		return nil, err
	}
	owner, err := adoptionOwner(ctx, tx, p, id)
	if err != nil {
		return nil, err
	}
	active, err := leadProject(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, workorders.Fail(409, "project archived")
	}
	l, err := loadLead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if l.Revision != *in.Revision {
		return nil, workorders.Fail(409, "lead revision conflict")
	}
	if l.Revision > 0 && l.owner != owner {
		return nil, workorders.Fail(403, "lead owner required; explicit ownership handoff required")
	}
	if l.SessionID != nil {
		return nil, workorders.Fail(409, "bound lead requires ordinary succession")
	}
	if l.State == "paused" {
		return nil, workorders.Fail(409, "explicit start request required after pause")
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT coalesce(owner_principal_id=$3,false) AND (`+adoptEligibleSQL+`) FROM harness_sessions WHERE project_id=$1 AND id=$2 FOR NO KEY UPDATE`, id, in.SessionID, owner).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !eligible {
		return nil, workorders.Fail(409, "eligible owned reporting root coordinator required")
	}
	if err != nil {
		return nil, err
	}
	before := l
	l, err = scanLead(tx.QueryRow(ctx, `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,state,reason) VALUES($1,$2,$3,$4,'waiting_for_room',$5) ON CONFLICT(tenant_id,project_id) DO UPDATE SET session_id=excluded.session_id,state=excluded.state,reason=excluded.reason,dispatch_key_id=NULL,revision=project_leads.revision+1,updated_at=clock_timestamp() RETURNING `+leadColumns, p.TenantID, id, owner, in.SessionID, adoptionPending))
	if err != nil {
		return nil, err
	}
	if err = workorders.Record(ctx, tx, p, id, "lead.adoption_requested", before, l); err != nil {
		return nil, err
	}
	return projectLead(ctx, tx, p, l)
}

// cancelLeadAdoption clears only the unclaimed session selection. It does not
// pause, stop or reparent the harness session, and it takes no session lock.
func (m *Module) cancelLeadAdoption(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Revision *int64 `json:"expected_revision"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision == nil || *in.Revision < 0 {
		return nil, workorders.Fail(400, "valid revision required")
	}
	ctx, id := r.Context(), r.PathValue("projectId")
	if err := leadFence(ctx, tx, id); err != nil {
		return nil, err
	}
	owner, err := adoptionOwner(ctx, tx, p, id)
	if err != nil {
		return nil, err
	}
	l, err := loadLead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if l.Revision != *in.Revision {
		return nil, workorders.Fail(409, "lead revision conflict")
	}
	if l.owner != owner {
		return nil, workorders.Fail(403, "lead owner required; explicit ownership handoff required")
	}
	if l.Reason != adoptionPending || l.Generation != 0 || l.SessionID == nil {
		return nil, workorders.Fail(409, "unclaimed adoption required")
	}
	before := l
	l, err = scanLead(tx.QueryRow(ctx, `UPDATE project_leads SET session_id=NULL,state='waiting_for_room',reason=$2,dispatch_key_id=NULL,revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 AND reason=$3 AND generation=0 AND session_id IS NOT NULL RETURNING `+leadColumns, id, selectionCleared, adoptionPending))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(409, "unclaimed adoption required")
	}
	if err != nil {
		return nil, err
	}
	if err = workorders.Record(ctx, tx, p, id, "lead.adoption_cancelled", before, l); err != nil {
		return nil, err
	}
	return projectLead(ctx, tx, p, l)
}
