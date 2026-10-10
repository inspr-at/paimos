// SPDX-License-Identifier: AGPL-3.0-only
package crossreview

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func (m *Module) mountPolicy(mux *http.ServeMux) {
	for _, path := range []string{"/api/settings/review-policy", "/api/projects/{projectId}/review-policy"} {
		mux.HandleFunc("GET "+path, workorders.Endpoint(m.pool, "reviewpolicy.read", false, 200, m.readPolicy))
		mux.HandleFunc("PUT "+path, workorders.Endpoint(m.pool, "reviewpolicy.manage", false, 200, m.writePolicy))
	}
	mux.HandleFunc("DELETE /api/projects/{projectId}/review-policy", workorders.Endpoint(m.pool, "reviewpolicy.manage", false, 200, m.writePolicy))
}

func policyProject(r *http.Request, tx pgx.Tx, write bool) (*string, error) {
	id := r.PathValue("projectId")
	if id == "" {
		return nil, nil
	}
	if !workorders.UUID(id) || id == "00000000-0000-0000-0000-000000000000" {
		return nil, workorders.Fail(400, "invalid project id")
	}
	query := `SELECT n.state FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project'`
	if write {
		query += " FOR NO KEY UPDATE OF n"
	}
	var state string
	if err := tx.QueryRow(r.Context(), query, id).Scan(&state); err != nil {
		return nil, err
	}
	if write && state != "active" {
		return nil, workorders.Fail(409, "project is unavailable for policy changes")
	}
	return &id, nil
}

func policyScope(project *string) authz.Scope {
	if project == nil {
		return authz.Scope{}
	}
	return authz.Scope{ProjectID: *project}
}

func (m *Module) readPolicy(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	project, err := policyProject(r, tx, false)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireTx(r.Context(), tx, p, "reviewpolicy.read", policyScope(project)); err != nil {
		return nil, err
	}
	return reviewgate.LoadFamilyPolicyTx(r.Context(), tx, project)
}

func (m *Module) writePolicy(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if len(r.Header.Get("If-Unmodified-Since")) > 64 {
		return nil, workorders.Fail(400, "invalid review policy revision")
	}
	var in reviewgate.FamilyPolicy
	if r.Method != http.MethodDelete {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || len(raw) > 4096 {
			return nil, workorders.Fail(400, "review policy exceeds its bound")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF || in.AllowedFamilies == nil || !in.Valid() {
			return nil, workorders.Fail(400, "invalid review family policy")
		}
	}
	ctx := r.Context()
	// Tenant admission precedes the tree and current project row, and the
	// authority check remains under the access-change fence through commit.
	if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
		return nil, err
	}
	project, err := policyProject(r, tx, true)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireTx(ctx, tx, p, "reviewpolicy.manage", policyScope(project)); err != nil {
		return nil, err
	}
	before, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	if revision := r.Header.Get("If-Unmodified-Since"); revision != "" {
		matches := revision == "none" && before.Policy == nil
		if before.UpdatedAt != nil {
			at, parseErr := time.Parse(time.RFC3339Nano, revision)
			matches = parseErr == nil && at.Equal(*before.UpdatedAt)
		}
		if !matches {
			return nil, workorders.Fail(412, "review policy changed; reload before saving or Undo")
		}
	}
	if r.Method == http.MethodDelete {
		_, err = tx.Exec(ctx, `DELETE FROM cross_family_policies WHERE project_id=$1::uuid`, project)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO cross_family_policies(tenant_id,project_id,mode,allowed_families,updated_by)
 VALUES($1,$2,$3,$4,$5)
 ON CONFLICT (tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid))) DO UPDATE
 SET mode=EXCLUDED.mode,allowed_families=EXCLUDED.allowed_families,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp()`, p.TenantID, project, in.Mode, in.AllowedFamilies, p.ID)
	}
	if err != nil {
		return nil, err
	}
	out, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	// Re-publish policy decisions even when their commit status state remains
	// success. Every resource update precedes the event-counter lock.
	_, err = tx.Exec(ctx, `UPDATE work_order_reviews v SET github_reported_state='',
 github_status=CASE WHEN github_status='success' THEN 'pending' ELSE github_status END
 FROM nodes n WHERE n.tenant_id=v.tenant_id AND n.id=v.ticket_node_id
 AND v.github_status NOT IN ('unconfigured','stale') AND ($1::uuid IS NULL OR n.project_id=$1)`, project)
	if err != nil {
		return nil, err
	}
	if _, err := events.Append(ctx, tx, p, events.Change{NodeID: project, Type: "review_policy.changed", Before: before, After: out}); err != nil {
		return nil, err
	}
	return out, nil
}
