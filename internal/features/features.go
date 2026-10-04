// SPDX-License-Identifier: AGPL-3.0-only

// Package features evaluates rollout flags. Flags control availability, never
// authorization: callers still enforce the permissions of the gated operation.
package features

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const WorkspaceSummary = "workspace-summary"

type Definition struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Catalog names code that actually ships. Every definition defaults OFF;
// adding an override cannot invent a feature or grant access to an operation.
func Catalog() []Definition {
	return []Definition{{WorkspaceSummary, "Workspace summary", "Show project and work counts in Workspace settings."}}
}

type Evaluation struct {
	Definition
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

type Setting struct {
	Evaluation
	Override *bool `json:"override"`
	Revision int64 `json:"revision"`
}

type Write struct {
	Enabled          *bool
	ExpectedRevision int64
}

type Saved struct {
	Feature Setting `json:"feature"`
	EventID *int64  `json:"event_id"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func known(key string) bool {
	for _, d := range Catalog() {
		if d.Key == key {
			return true
		}
	}
	return false
}

// evaluate preserves explicit false: OFF at a narrower scope overrides ON.
func evaluate(d Definition, tenantOverride, projectOverride *bool) Evaluation {
	e := Evaluation{Definition: d, Source: "default"}
	if tenantOverride != nil {
		e.Enabled, e.Source = *tenantOverride, "tenant"
	}
	if projectOverride != nil {
		e.Enabled, e.Source = *projectOverride, "project"
	}
	return e
}

// Evaluate reads committed overrides on every call; replicas share the same
// database state, without a process cache or restart requirement.
func (s *Service) Evaluate(ctx context.Context, p tenant.Principal, projectID string) ([]Evaluation, error) {
	settings, err := s.list(ctx, p, projectID, false)
	if err != nil {
		return nil, err
	}
	out := make([]Evaluation, 0, len(settings))
	for _, setting := range settings {
		out = append(out, setting.Evaluation)
	}
	return out, nil
}

// Enabled is the server-side hook. Unknown keys fail closed; errors always
// return false. It does not replace authorization for the gated operation.
func (s *Service) Enabled(ctx context.Context, p tenant.Principal, key, projectID string) (bool, error) {
	if !known(key) {
		return false, nil
	}
	items, err := s.Evaluate(ctx, p, projectID)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Key == key {
			return item.Enabled, nil
		}
	}
	return false, nil
}

func (s *Service) Settings(ctx context.Context, p tenant.Principal, projectID string) ([]Setting, error) {
	return s.list(ctx, p, projectID, true)
}

func (s *Service) list(ctx context.Context, p tenant.Principal, projectID string, admin bool) ([]Setting, error) {
	if err := validActor(p); err != nil {
		return nil, err
	}
	if err := validProjectID(projectID); err != nil {
		return nil, err
	}
	ctx = tenant.WithPrincipal(ctx, p)
	var out []Setting
	err := db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authorize(ctx, tx, p, projectID, admin); err != nil {
			return err
		}
		var err error
		out, err = load(ctx, tx, p.TenantID, projectID)
		return err
	})
	return out, err
}

func authorize(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, admin bool) error {
	permission, scope := "nodes.read", authz.Scope{ProjectID: projectID, AnyProject: projectID == ""}
	if admin {
		if p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		permission, scope = "settings.manage", authz.Scope{}
	}
	if err := authz.RequireTx(ctx, tx, p, permission, scope); err != nil {
		return err
	}
	if projectID != "" {
		var found bool
		err := tx.QueryRow(ctx, `SELECT true FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
            WHERE n.tenant_id=$1::uuid AND n.id=$2::uuid AND k.slug='project' AND n.deleted_at IS NULL`, p.TenantID, projectID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return fault(404, "project not found")
		}
		return err
	}
	return nil
}

func load(ctx context.Context, tx pgx.Tx, tenantID, projectID string) ([]Setting, error) {
	rows, err := tx.Query(ctx, `SELECT key, project_id::text, enabled, revision FROM features
        WHERE tenant_id=$1::uuid AND (project_id IS NULL OR project_id=$2::uuid)`, tenantID, nullProject(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type override struct {
		enabled  *bool
		revision int64
	}
	baseline, project := map[string]override{}, map[string]override{}
	for rows.Next() {
		var key string
		var id *string
		var o override
		if err := rows.Scan(&key, &id, &o.enabled, &o.revision); err != nil {
			return nil, err
		}
		if id == nil {
			baseline[key] = o
		} else {
			project[key] = o
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Setting, 0, len(Catalog()))
	for _, d := range Catalog() {
		b, pr := baseline[d.Key], project[d.Key]
		selected := b
		if projectID != "" {
			selected = pr
		}
		out = append(out, Setting{Evaluation: evaluate(d, b.enabled, pr.enabled), Override: selected.enabled, Revision: selected.revision})
	}
	return out, nil
}

type snapshot struct {
	Key       string  `json:"key"`
	ProjectID *string `json:"project_id"`
	Override  *bool   `json:"override"`
	Revision  int64   `json:"revision"`
}

// Save checks authorization and revision and appends the audit in the same
// transaction as the mutation. A per-tenant/key lock also serializes the first
// insert and tenant/project changes, while unrelated flags remain independent.
func (s *Service) Save(ctx context.Context, p tenant.Principal, key, projectID string, in Write) (Saved, error) {
	var out Saved
	if err := validActor(p); err != nil {
		return out, err
	}
	if !known(key) {
		return out, fault(404, "feature not found")
	}
	if err := validProjectID(projectID); err != nil {
		return out, err
	}
	if in.ExpectedRevision < 0 {
		return out, fault(400, "expected revision must be nonnegative")
	}
	ctx = tenant.WithPrincipal(ctx, p)
	err := db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authorize(ctx, tx, p, projectID, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "feature:"+p.TenantID+":"+key); err != nil {
			return err
		}
		items, err := load(ctx, tx, p.TenantID, projectID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Key == key {
				out.Feature = item
				break
			}
		}
		if out.Feature.Revision != in.ExpectedRevision {
			return fault(409, "feature settings changed; reload before saving")
		}
		if sameBool(out.Feature.Override, in.Enabled) {
			return nil
		}
		before := snapshot{key, nullProject(projectID), out.Feature.Override, out.Feature.Revision}
		var revision int64
		err = tx.QueryRow(ctx, `INSERT INTO features(tenant_id,key,project_id,enabled)
            VALUES($1::uuid,$2,$3::uuid,$4)
            ON CONFLICT (tenant_id,key,project_id) DO UPDATE SET enabled=EXCLUDED.enabled,
                revision=features.revision+1,updated_at=clock_timestamp() RETURNING revision`,
			p.TenantID, key, nullProject(projectID), in.Enabled).Scan(&revision)
		if err != nil {
			return err
		}
		after := snapshot{key, nullProject(projectID), in.Enabled, revision}
		event, err := events.Append(ctx, tx, p, events.Change{NodeID: nullProject(projectID), Type: "feature.updated", Before: before, After: after})
		if err != nil {
			return err
		}
		out.EventID = &event.ID
		items, err = load(ctx, tx, p.TenantID, projectID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Key == key {
				out.Feature = item
				break
			}
		}
		return nil
	})
	return out, err
}

func sameBool(a, b *bool) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
func nullProject(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
