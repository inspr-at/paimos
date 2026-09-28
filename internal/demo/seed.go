// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

type seeder struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	slug      string
	tenantID  string
	api       *api
	admin     tenant.Principal
	ivo       tenant.Principal
	nia       tenant.Principal
	scribe    tenant.Principal
	scribeKey string
	clerk     tenant.Principal
	clerkKey  string
	kinds     map[string]string
	ids       map[string]string
	lumenID   string
	out       Summary
	afterStep func(string) error
}

type idBody struct {
	ID string `json:"id"`
}

type nodeBody struct {
	ID     string          `json:"id"`
	Key    string          `json:"key"`
	Fields json.RawMessage `json:"fields"`
}

func (s *seeder) run() error {
	id, err := tenantbootstrap.ResolveSlug(s.ctx, s.pool, s.slug)
	if err != nil {
		return fmt.Errorf("tenant %q: %w", s.slug, err)
	}
	s.tenantID = id
	// Serialize seeds for this tenant so a concurrent invocation sees the
	// completion marker after the first transaction commits.
	if err := db.InTenant(db.NoProjects(s.ctx, "demo seed"), s.pool, id, func(tx pgx.Tx) error {
		return tx.QueryRow(s.ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, id).Scan(&id)
	}); err != nil {
		return fmt.Errorf("lock tenant %q: %w", s.slug, err)
	}
	if s.admin, err = s.person("demo-operator", "Demo Operator", "admin"); err != nil {
		return err
	}
	if s.ivo, err = s.person("ivo-quill", "Ivo Quill", "member"); err != nil {
		return err
	}
	if s.nia, err = s.person("nia-frost", "Nia Frost", "member"); err != nil {
		return err
	}
	api, err := newAPI(s.ctx, s.pool)
	if err != nil {
		return err
	}
	s.api = api
	done, err := s.complete()
	if err != nil {
		return err
	}
	if done {
		return s.finish(true)
	}
	if err := s.tree(); err != nil {
		return err
	}
	if err := s.step("tree"); err != nil {
		return err
	}
	if err := s.knowledge(); err != nil {
		return err
	}
	if err := s.relate(); err != nil {
		return err
	}
	if err := s.agents(); err != nil {
		return err
	}
	if err := s.step("agents"); err != nil {
		return err
	}
	if err := s.journey(); err != nil {
		return err
	}
	if err := s.work(); err != nil {
		return err
	}
	if err := s.hours(); err != nil {
		return err
	}
	if err := s.mark(); err != nil {
		return err
	}
	return s.finish(false)
}

func (s *seeder) step(name string) error {
	if s.afterStep != nil {
		return s.afterStep(name)
	}
	return nil
}

func (s *seeder) person(subject, name, role string) (tenant.Principal, error) {
	id, err := tenantbootstrap.BindOIDC(s.ctx, s.pool, s.slug, issuer, subject, name, role)
	if err != nil {
		return tenant.Principal{}, fmt.Errorf("bind %s: %w", name, err)
	}
	return tenant.Principal{ID: id, TenantID: s.tenantID, Kind: tenant.Person, Name: name, Roles: []string{role}}, nil
}

func (s *seeder) complete() (bool, error) {
	code, raw, err := s.api.call(s.admin, "", http.MethodGet, "/api/node-keys/"+markerKey, nil, nil)
	if err != nil {
		return false, err
	}
	if code == http.StatusNotFound {
		return false, nil
	}
	if code != http.StatusOK {
		return false, fmt.Errorf("node key %s: status %d: %s", markerKey, code, clip(raw))
	}
	var node nodeBody
	if err := json.Unmarshal(raw, &node); err != nil {
		return false, err
	}
	var fields map[string]any
	if len(node.Fields) > 0 {
		if err := json.Unmarshal(node.Fields, &fields); err != nil {
			return false, err
		}
	}
	if fields[markerField] == markerComplete {
		s.lumenID = node.ID
		return true, nil
	}
	return false, nil
}

func (s *seeder) tree() error {
	if err := s.loadKinds(); err != nil {
		return err
	}
	s.ids = map[string]string{}
	for _, project := range projects() {
		fields := map[string]any{"project_key": project.route}
		if project.key == markerKey {
			fields[markerField] = "pending"
		}
		id, err := s.node(s.kinds["project"], project.key, project.title, project.body, "open", "", fields)
		if err != nil {
			return err
		}
		if project.key == markerKey {
			s.lumenID = id
		}
		for _, epic := range project.epics {
			epicID, err := s.node(s.kinds["epic"], epic.key, epic.title, "Fictional epic for screenshots.", "open", id, nil)
			if err != nil {
				return err
			}
			for _, ticket := range epic.tickets {
				body := "Fictional ticket for screenshots."
				if ticket.rich {
					body = richBody()
				}
				fields := map[string]any{}
				// Authored fictional examples for newly seeded completed tickets.
				// Completed demo tenants return before this path; no backfill.
				if ticket.pillEN != "" {
					fields["pill_en"], fields["pill_de"] = ticket.pillEN, ticket.pillDE
					fields["benefit_en"], fields["benefit_de"] = ticket.benefitEN, ticket.benefitDE
				}
				if ticket.priority != "" {
					fields["priority"] = ticket.priority
				}
				who := s.admin
				switch ticket.assignee {
				case 1:
					fields["assignee"] = s.ivo.ID
					who = s.ivo
				case 2:
					fields["assignee"] = s.nia.ID
					who = s.nia
				}
				ticketID, err := s.node(s.kinds["ticket"], ticket.key, ticket.title, body, ticket.state, epicID, fields)
				if err != nil {
					return err
				}
				if ticket.comment != "" {
					if err := s.comment(who, ticketID, ticket.comment); err != nil {
						return err
					}
				}
				if ticket.rich {
					if err := s.comment(s.nia, ticketID, "Fictional second note: the card sample is not a catalog record."); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (s *seeder) node(kindID, key, title, body, state, parent string, fields map[string]any) (string, error) {
	in := map[string]any{"kind_id": kindID, "key": key, "title": title, "body": body, "state": state}
	if parent != "" {
		in["parent_id"] = parent
	}
	if len(fields) > 0 {
		in["fields"] = fields
	}
	var out nodeBody
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/nodes", in, http.StatusCreated, &out, nil); err != nil {
		return "", fmt.Errorf("node %s: %w", key, err)
	}
	s.ids[key] = out.ID
	return out.ID, nil
}

func (s *seeder) comment(who tenant.Principal, nodeID, markdown string) error {
	if who.ID == "" {
		who = s.admin
	}
	return s.api.do(who, "", http.MethodPost, "/api/nodes/"+nodeID+"/comments", map[string]any{"body_markdown": markdown}, http.StatusCreated, nil, nil)
}

func (s *seeder) loadKinds() error {
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := s.api.do(s.admin, "", http.MethodGet, "/api/kinds", nil, http.StatusOK, &page, nil); err != nil {
		return err
	}
	s.kinds = map[string]string{}
	for _, row := range page.Items {
		s.kinds[row.Slug] = row.ID
	}
	for _, slug := range []string{"project", "epic", "ticket"} {
		if s.kinds[slug] == "" {
			return fmt.Errorf("kind %s is not seeded", slug)
		}
	}
	return nil
}

func (s *seeder) knowledge() error {
	for _, entry := range knowledgeEntries() {
		in := map[string]any{
			"project_id": s.lumenID,
			"type":       entry.typ,
			"slug":       entry.slug,
			"title":      entry.title,
			"body":       entry.body,
			"status":     "active",
		}
		if entry.meta != nil {
			in["metadata"] = entry.meta
		}
		var out idBody
		if err := s.api.do(s.admin, "", http.MethodPost, "/api/knowledge", in, http.StatusCreated, &out, nil); err != nil {
			return fmt.Errorf("knowledge %s: %w", entry.slug, err)
		}
		s.ids["k:"+entry.slug] = out.ID
	}
	return nil
}

func (s *seeder) relate() error {
	pairs := []struct{ source, target, typ string }{
		{"LT-2", "LT-3", "blocks"},
		{"LT-5", "LT-6", "relates"},
		{"LT-1", "LE-1", "implements"},
		{"HT-7", "HT-8", "duplicates"},
		{"NT-7", "LT-1", "cites"},
		{"LT-1", "k:lumen-context", "cites"},
	}
	for _, pair := range pairs {
		in := map[string]string{
			"source_node_id": s.ids[pair.source],
			"target_node_id": s.ids[pair.target],
			"type":           pair.typ,
		}
		if in["source_node_id"] == "" || in["target_node_id"] == "" {
			return fmt.Errorf("relation %s %s missing endpoint", pair.source, pair.target)
		}
		if err := s.api.do(s.admin, "", http.MethodPost, "/api/relations", in, http.StatusCreated, nil, nil); err != nil {
			return fmt.Errorf("relation %s %s %s: %w", pair.source, pair.typ, pair.target, err)
		}
	}
	return nil
}

func (s *seeder) mark() error {
	in := map[string]any{"fields": map[string]any{"project_key": "LUMEN", markerField: markerComplete}}
	return s.api.do(s.admin, "", http.MethodPatch, "/api/nodes/"+s.lumenID, in, http.StatusOK, nil, nil)
}

func (s *seeder) finish(already bool) error {
	var projects, tickets int
	var stage string
	err := db.InTenant(tenant.WithPrincipal(s.ctx, s.admin), s.pool, s.tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(s.ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND n.deleted_at IS NULL`).Scan(&projects); err != nil {
			return err
		}
		if err := tx.QueryRow(s.ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='ticket' AND n.deleted_at IS NULL`).Scan(&tickets); err != nil {
			return err
		}
		return tx.QueryRow(s.ctx, `SELECT coalesce(r.state,'') FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.project_node_id WHERE n.key=$1`, markerKey).Scan(&stage)
	})
	if err != nil {
		return err
	}
	viewStage := ""
	if s.lumenID != "" {
		view, err := s.journeyView(s.lumenID)
		if err != nil {
			return err
		}
		viewStage = view.Stage
	}
	s.out = Summary{Slug: s.slug, TenantID: s.tenantID, Already: already, Projects: projects, Tickets: tickets, Stage: viewStage}
	if viewStage == "" {
		s.out.Stage = stage
	}
	return nil
}

func clip(raw []byte) string {
	if len(raw) > 600 {
		return string(raw[:600])
	}
	return string(raw)
}
