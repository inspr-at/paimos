// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Definition separates the visibility/owner of a routine from its output.
// Input.ProjectID and ParentID retain their legacy meaning as the output target.
// An assignment is saved intent only; neither it nor a queued leaf grants launch.
type Definition struct {
	Scope            DefinitionScope `json:"scope"`
	OwnerPrincipalID string          `json:"owner_principal_id"`
	Assignment       *Assignment     `json:"assignment,omitempty"`
}

type DefinitionScope struct {
	Kind      string `json:"kind"`
	ProjectID string `json:"project_id,omitempty"`
}

type Assignment struct {
	Goal                string              `json:"goal"`
	Sources             []SourceReference   `json:"sources"`
	Role                string              `json:"role"`
	WorkKindID          string              `json:"work_kind_id"`
	AllowedActions      []string            `json:"allowed_actions"`
	RuntimeRequirements RuntimeRequirements `json:"runtime_requirements"`
	Budget              DefinitionBudget    `json:"budget"`
}

// Sources are tenant node references, including knowledge nodes. They cannot
// carry credentials, URLs to fetch, or arbitrary external request instructions.
type SourceReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type RuntimeRequirements struct {
	NeedsNativeHost bool   `json:"needs_native_host"`
	NeedsBrowser    bool   `json:"needs_browser"`
	RuntimeClass    string `json:"runtime_class"`
}

// Money uses integral micro-USD, never floating-point prices or subscription
// list-price comparisons. Qualification/enforcement belongs to later slices.
type DefinitionBudget struct {
	Mode                 string `json:"mode"`
	TokenCeiling         *int64 `json:"token_ceiling,omitempty"`
	MoneyCeilingMicroUSD *int64 `json:"money_ceiling_microusd,omitempty"`
}

func (d *Definition) normalize() error {
	if !workorders.UUID(d.OwnerPrincipalID) {
		return fmt.Errorf("definition owner must be a person UUID")
	}
	d.OwnerPrincipalID = strings.ToLower(d.OwnerPrincipalID)
	switch d.Scope.Kind {
	case "personal", "workspace":
		if d.Scope.ProjectID != "" {
			return fmt.Errorf("only project scope has a project_id")
		}
	case "project":
		if !workorders.UUID(d.Scope.ProjectID) {
			return fmt.Errorf("project definition scope requires a project_id")
		}
		d.Scope.ProjectID = strings.ToLower(d.Scope.ProjectID)
	default:
		return fmt.Errorf("definition scope must be project, personal or workspace")
	}
	a := d.Assignment
	if a == nil {
		return nil
	}
	a.Goal = strings.TrimSpace(a.Goal)
	if a.Goal == "" || len(a.Goal) > 16384 || len(a.Sources) > 32 || len(a.AllowedActions) > 5 {
		return fmt.Errorf("assignment exceeds its limits")
	}
	if !modelregistry.KnownRouteRole(a.Role) || !workorders.UUID(a.WorkKindID) {
		return fmt.Errorf("assignment requires a registry role and work_kind_id")
	}
	a.WorkKindID = strings.ToLower(a.WorkKindID)
	if a.Sources == nil {
		a.Sources = []SourceReference{}
	}
	seen := map[string]bool{}
	for i := range a.Sources {
		s := &a.Sources[i]
		s.ID = strings.ToLower(s.ID)
		if (s.Kind != "node" && s.Kind != "knowledge") || !workorders.UUID(s.ID) || seen[s.ID] {
			return fmt.Errorf("sources must be unique node or knowledge UUID references")
		}
		seen[s.ID] = true
	}
	if a.AllowedActions == nil {
		a.AllowedActions = []string{}
	}
	seen = map[string]bool{}
	for _, action := range a.AllowedActions {
		switch action {
		case "work.create", "work.update", "knowledge.write", "pr.open", "pipeline.request":
		default:
			return fmt.Errorf("unknown routine action")
		}
		if seen[action] {
			return fmt.Errorf("allowed actions must be unique")
		}
		seen[action] = true
	}
	native := a.RuntimeRequirements
	if native.RuntimeClass != "native_coding" && native.RuntimeClass != "native_browser" {
		return fmt.Errorf("runtime_class must be native_coding or native_browser")
	}
	if !native.NeedsNativeHost || native.NeedsBrowser != (native.RuntimeClass == "native_browser") {
		return fmt.Errorf("native runtime requirements are inconsistent")
	}
	b := &a.Budget
	if b.Mode == "" {
		b.Mode = "off"
	}
	positive := func(n *int64) bool { return n != nil && *n > 0 && *n <= 1000000000000 }
	switch b.Mode {
	case "off":
		if b.TokenCeiling != nil || b.MoneyCeilingMicroUSD != nil {
			return fmt.Errorf("budget off must not include ceilings")
		}
	case "tokens":
		if !positive(b.TokenCeiling) || b.MoneyCeilingMicroUSD != nil {
			return fmt.Errorf("token budget requires only a positive token ceiling")
		}
	case "money":
		if !positive(b.MoneyCeilingMicroUSD) || b.TokenCeiling != nil {
			return fmt.Errorf("money budget requires only a positive micro-USD ceiling")
		}
	case "both":
		if !positive(b.TokenCeiling) || !positive(b.MoneyCeilingMicroUSD) {
			return fmt.Errorf("combined budget requires both positive ceilings")
		}
	default:
		return fmt.Errorf("budget mode must be off, tokens, money or both")
	}
	return nil
}

func canonicalPerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT c.id::text FROM principals p JOIN principals c ON c.tenant_id=p.tenant_id AND c.id=coalesce(p.linked_to,p.id)
 WHERE p.id=$1 AND p.status='active' AND c.status='active' AND p.kind='person' AND c.kind='person'`, p.ID).Scan(&id)
	return id, err
}

func manageDefinition(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Input) error {
	if err := manage(ctx, tx, p, in.ProjectID); err != nil {
		return err
	}
	d := in.Definition
	if d == nil {
		return nil
	}
	if d.Scope.Kind == "personal" {
		if p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		id, err := canonicalPerson(ctx, tx, p)
		if err != nil {
			return err
		}
		if id != d.OwnerPrincipalID {
			return authz.ErrForbidden
		}
		return nil
	}
	return manage(ctx, tx, p, d.Scope.ProjectID)
}

func readDefinition(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Input) error {
	if err := read(ctx, tx, p, in.ProjectID); err != nil {
		return err
	}
	d := in.Definition
	if d == nil {
		return nil
	}
	if d.Scope.Kind == "personal" {
		if p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		id, err := canonicalPerson(ctx, tx, p)
		if err != nil {
			return err
		}
		if id != d.OwnerPrincipalID {
			return authz.ErrForbidden
		}
		return nil
	}
	return read(ctx, tx, p, d.Scope.ProjectID)
}

// validateDefinition runs under the same tenant/tree fence as the final write.
// The canonical owner must independently hold the output and source authority;
// the saving administrator's wider rights never stand in for the owner's.
func validateDefinition(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Input) error {
	d := in.Definition
	if d == nil {
		return nil
	}
	if err := manageDefinition(ctx, tx, p, in); err != nil {
		return err
	}
	owner := tenant.Principal{ID: d.OwnerPrincipalID, TenantID: p.TenantID, Kind: tenant.Person}
	canonical, err := canonicalPerson(ctx, tx, owner)
	if err != nil {
		return err
	}
	if canonical != owner.ID {
		return workorders.Fail(400, "definition owner must be canonical")
	}
	if d.Scope.Kind != "personal" {
		effective, err := authz.EffectiveTx(ctx, tx, owner, d.Scope.ProjectID)
		if err != nil {
			return err
		}
		admin := func(r *authz.RoleRef) bool { return r != nil && (r.Key == "owner" || r.Key == "admin") }
		if !admin(effective.Workspace.Role) && (effective.Project == nil || !admin(effective.Project.Role)) {
			return authz.ErrForbidden
		}
		if err := manage(ctx, tx, owner, d.Scope.ProjectID); err != nil {
			return err
		}
	}
	for _, permission := range []string{"nodes.read", "nodes.write", Permission} {
		if err := authz.RequireTx(ctx, tx, owner, permission, authz.Scope{ProjectID: in.ProjectID}); err != nil {
			return err
		}
	}
	if in.QueueEach {
		for _, permission := range []string{"run.create", "work_orders.write"} {
			if err := authz.RequireTx(ctx, tx, owner, permission, authz.Scope{ProjectID: in.ProjectID}); err != nil {
				return err
			}
		}
	}
	if err := authorizeSources(ctx, tx, owner, in); err != nil {
		return err
	}
	if d.Scope.Kind == "project" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project')`, d.Scope.ProjectID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return pgx.ErrNoRows
		}
	}
	if d.Assignment == nil {
		return nil
	}
	a := d.Assignment
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_kinds WHERE id=$1 AND archived_at IS NULL AND (project_id IS NULL OR project_id=$2))`, a.WorkKindID, in.ProjectID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return workorders.Fail(400, "assignment work kind is unavailable in the output project")
	}
	for _, source := range a.Sources {
		var project, kind string
		if err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,''),k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL`, source.ID).Scan(&project, &kind); err != nil {
			return err
		}
		permission := "nodes.read"
		if source.Kind == "knowledge" {
			switch kind {
			case "runbook", "guideline", "memory", "external_system", "related_project", "decision":
			default:
				return workorders.Fail(400, "knowledge source must refer to a knowledge entry")
			}
			permission = "knowledge.read"
		}
		for _, reader := range []tenant.Principal{p, owner} {
			if err := authz.RequireTx(ctx, tx, reader, permission, authz.Scope{ProjectID: project}); err != nil {
				return err
			}
		}
	}
	return nil
}

// definitions decorates a bounded page in one query. Its RLS uses the same
// visibility boundary as the recurrence row and its definition audit events.
func definitions(ctx context.Context, tx pgx.Tx, items []Recurrence, apply func([]Recurrence)) error {
	ids := make([]string, len(items))
	indices := make(map[string]int, len(items))
	for i, item := range items {
		ids[i], indices[item.ID] = item.ID, i
	}
	rows, err := tx.Query(ctx, `SELECT recurrence_id::text,scope_type,coalesce(scope_project_id::text,''),owner_principal_id::text,assignment FROM recurrence_definitions WHERE recurrence_id=ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d Definition
		var raw []byte
		if err := rows.Scan(&id, &d.Scope.Kind, &d.Scope.ProjectID, &d.OwnerPrincipalID, &raw); err != nil {
			return err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &d.Assignment); err != nil {
				return err
			}
		}
		items[indices[id]].Definition = &d
	}
	if err := rows.Err(); err != nil {
		return err
	}
	apply(items)
	return nil
}

// Insert the projection before its marked recurrence; its composite FK is
// deferred until commit so INSERT RETURNING is already scope protected by RLS.
func saveDefinition(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in Input) error {
	if in.Definition == nil {
		return nil
	}
	d := in.Definition
	var assignment any
	if d.Assignment != nil {
		raw, err := json.Marshal(d.Assignment)
		if err != nil {
			return err
		}
		assignment = raw
	}
	_, err := tx.Exec(ctx, `INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment)
 VALUES($1,$2,$3,nullif($4,'')::uuid,$5,$6,$7,$8) ON CONFLICT(tenant_id,recurrence_id) DO UPDATE SET assignment=EXCLUDED.assignment,execute_consent=false,consent_revision=0,consented_by_principal_id=NULL,consented_at=NULL`, p.TenantID, id, d.Scope.Kind, d.Scope.ProjectID, d.OwnerPrincipalID, in.ProjectID, in.ParentID, assignment)
	return err
}
