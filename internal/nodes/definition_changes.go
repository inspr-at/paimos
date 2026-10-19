// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func definitionAdminsTx(ctx context.Context, tx pgx.Tx, project *string) ([]workorders.EntryPerson, int, error) {
	permission := "settings.manage"
	if project != nil {
		permission = "nodes.write"
	}
	// Resolve built-in permissions from the canonical registry, just as RequireTx
	// does. Custom roles use role_permissions. This is one set read, not one
	// authorization query for every name, and never truncates the context itself.
	builtinRoles := []string{}
	for _, role := range []string{"owner", "admin", "member", "viewer", "guest", "customer"} {
		permissions, _ := authz.BuiltinPermissions(role)
		if slices.Contains(permissions, permission) {
			builtinRoles = append(builtinRoles, role)
		}
	}
	rows, err := tx.Query(ctx, `WITH admins AS (SELECT DISTINCT p.id,p.name FROM principals p
	 JOIN role_bindings b ON b.tenant_id=p.tenant_id AND b.principal_id=p.id
	 JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
	 WHERE p.kind='person' AND p.status='active' AND p.linked_to IS NULL AND NOT coalesce(p.messaging_only,false) AND
	 (b.scope_type='workspace' AND r.key IN ('owner','admin') OR b.scope_type='project' AND b.scope_id=$1::uuid AND r.key='admin')
	 AND EXISTS (SELECT 1 FROM role_bindings permitted
	 JOIN roles pr ON pr.tenant_id=permitted.tenant_id AND pr.id=permitted.role_id
	 WHERE permitted.tenant_id=p.tenant_id AND permitted.principal_id=p.id
	 AND (permitted.scope_type='workspace' OR $1::uuid IS NOT NULL AND permitted.scope_type='project' AND permitted.scope_id=$1::uuid)
	 AND (pr.builtin AND pr.key=ANY($2::text[]) OR NOT pr.builtin AND EXISTS
	 (SELECT 1 FROM role_permissions rp WHERE rp.tenant_id=pr.tenant_id AND rp.role_id=pr.id AND rp.permission=$3))))
	 SELECT id::text,name,count(*) OVER() FROM admins ORDER BY id::text LIMIT 100`, project, builtinRoles, permission)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	people := []workorders.EntryPerson{}
	total := 0
	for rows.Next() {
		var person workorders.EntryPerson
		if err = rows.Scan(&person.ID, &person.Name, &total); err != nil {
			return nil, 0, err
		}
		people = append(people, person)
	}
	return people, max(0, total-len(people)), rows.Err()
}

func definitionContextTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, current, workspace *workqueue.EntryDefinition) (*entryDefinitionContext, error) {
	out := &entryDefinitionContext{ProjectID: project, Definition: current, Workspace: workspace, Pending: []workorders.DefinitionChange{}}
	var err error
	out.Admins, out.AdminsMore, err = definitionAdminsTx(ctx, tx, &project)
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Person {
		err = authz.RequireProjectAdminTx(ctx, tx, p, project)
		if err != nil && !errors.Is(err, authz.ErrForbidden) {
			return nil, err
		}
		out.CanManage = err == nil
	}
	if current != nil && current.Source == "project" {
		state, err := workorders.DefinitionCopyStateTx(ctx, tx, current.ID)
		if err != nil {
			return nil, err
		}
		state = state.WithFallback(current, workspace)
		out.Base = state.Base
		if workspace != nil {
			out.Pending = workorders.PendingDefinitionChanges(state.Reviewed, workspace.Checks)
		}
	}
	return out, nil
}

func (m *Module) handleDefinitionDecision(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	project, ok := pathUUID(w, r.PathValue("projectId"), "invalid project id")
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeErr(w, authz.ErrForbidden)
		return
	}
	var in struct {
		Revision  time.Time `json:"expected_revision"`
		Workspace time.Time `json:"expected_workspace_revision"`
		CheckID   string    `json:"check_id"`
		Decision  string    `json:"decision"`
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(body, &in); err != nil || in.Revision.IsZero() || in.Workspace.IsZero() || len(in.CheckID) < 1 || len(in.CheckID) > 80 || (in.Decision != "accept" && in.Decision != "keep") {
		writeErr(w, badRequest("both revisions, check_id and accept or keep are required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out *entryDefinitionContext
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		if err := authz.RequireProjectAdminTx(ctx, tx, p, project); err != nil {
			return err
		}
		var live string
		if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project'`, project).Scan(&live); err != nil {
			return err
		}
		current, err := workorders.EntryDefinitionTx(ctx, tx, &project, false)
		if err != nil {
			return err
		}
		workspace, err := workorders.EntryDefinitionTx(ctx, tx, nil, true)
		if err != nil {
			return err
		}
		if current == nil || workspace == nil || !current.Revision.Equal(in.Revision) || !workspace.Revision.Equal(in.Workspace) {
			return conflict("definition or workspace default has changed")
		}
		state, err := workorders.DefinitionCopyStateTx(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		state = state.WithFallback(current, workspace)
		pending := workorders.PendingDefinitionChanges(state.Reviewed, workspace.Checks)
		var selected *workorders.DefinitionChange
		for i := range pending {
			if pending[i].CheckID == in.CheckID {
				selected = &pending[i]
				break
			}
		}
		if selected == nil {
			return conflict("this workspace change is no longer waiting")
		}
		next := current.Checks
		if in.Decision == "accept" {
			next = workorders.ApplyDefinitionChange(next, *selected)
			if err = workorders.ValidateEntryRules(next); err != nil {
				return err
			}
		}
		reviewed := workorders.ApplyDefinitionChange(state.Reviewed, *selected)
		fields := entryJSON(map[string]any{"checks": next, "copied_from_revision": current.CopiedFrom, "copied_from_checks": state.Base, "reviewed_checks": reviewed})
		if len(fields) > 65536 {
			return badRequest("definition and copy state exceed 64 KiB")
		}
		var revision time.Time
		// Keeping a version acknowledges metadata only. Retain the enforced
		// definition revision, so existing person ticks and owner assignments
		// remain valid. The tree fence and pending-check check serialize repeats.
		changed := !sameJSON(entryJSON(next), entryJSON(current.Checks))
		if err = tx.QueryRow(ctx, `UPDATE nodes SET fields=$2::jsonb,updated_at=CASE WHEN $3 THEN greatest(clock_timestamp(),updated_at+interval '1 microsecond') ELSE updated_at END WHERE id=$1 RETURNING updated_at`, current.ID, string(fields), changed).Scan(&revision); err != nil {
			return err
		}
		saved := *current
		saved.Checks, saved.Revision = next, revision
		out, err = definitionContextTx(ctx, tx, p, project, &saved, workspace)
		if err != nil {
			return err
		}
		// Complete authorization, state and response reads before event counter.
		return m.record(ctx, tx, p.ID, &current.ID, "definition.change_decided", current, map[string]any{"definition": saved, "workspace_revision": workspace.Revision, "check_id": in.CheckID, "decision": in.Decision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = notFound("project not found")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type definitionProjectSummary struct {
	ProjectID   string `json:"project_id"`
	Key         string `json:"key"`
	Differences int    `json:"difference_count"`
	Pending     int    `json:"pending_count"`
}
type workspaceDefinitionContext struct {
	Definition  *workqueue.EntryDefinition `json:"definition"`
	CanManage   bool                       `json:"can_manage"`
	Admins      []workorders.EntryPerson   `json:"admins"`
	AdminsMore  int                        `json:"admins_more"`
	Projects    []definitionProjectSummary `json:"projects"`
	DifferCount int                        `json:"differ_count"`
	UsesDefault []string                   `json:"uses_default"`
	Next        *string                    `json:"next_cursor"`
}

func (m *Module) handleWorkspaceDefinitionContext(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" {
		if after, ok = pathUUID(w, after, "invalid project cursor"); !ok {
			return
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeErr(w, badRequest("invalid limit"))
			return
		}
	}
	out := workspaceDefinitionContext{Projects: []definitionProjectSummary{}, UsesDefault: []string{}}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{}); err != nil {
			return err
		}
		var err error
		out.Definition, err = workorders.EntryDefinitionTx(ctx, tx, nil, true)
		if err != nil {
			return err
		}
		out.Admins, out.AdminsMore, err = definitionAdminsTx(ctx, tx, nil)
		if err != nil {
			return err
		}
		if p.Kind == tenant.Person {
			err = authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
			if err != nil && !errors.Is(err, authz.ErrForbidden) {
				return err
			}
			out.CanManage = err == nil
		}
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.deleted_at IS NULL AND k.slug='project' ORDER BY n.id LIMIT 1001`)
		if err != nil {
			return err
		}
		ids := []string{}
		total := 0
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			total++
			if check("nodes.read", id) {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if total > 1000 {
			return &httpError{status: 422, msg: "definition project coverage exceeds 1000 projects"}
		}
		projects, err := workorders.DefinitionProjectsTx(ctx, tx, ids)
		if err != nil {
			return err
		}
		for _, project := range projects {
			if project.Definition == nil {
				out.UsesDefault = append(out.UsesDefault, project.Key)
				continue
			}
			differences := workorders.DefinitionDifferenceCount(project.Definition.Checks, out.Definition.Checks)
			if differences == 0 {
				continue
			}
			out.DifferCount++
			if project.ProjectID <= after {
				continue
			}
			if len(out.Projects) == limit {
				if out.Next == nil {
					last := out.Projects[len(out.Projects)-1].ProjectID
					out.Next = &last
				}
				continue
			}
			state := project.Copy.WithFallback(project.Definition, out.Definition)
			pending := workorders.PendingDefinitionChanges(state.Reviewed, out.Definition.Checks)
			out.Projects = append(out.Projects, definitionProjectSummary{project.ProjectID, project.Key, differences, len(pending)})
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}
