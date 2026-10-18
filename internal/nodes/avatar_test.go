// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// People in list and project payloads say whether they have a picture, so the
// web client asks for /api/people/{id}/avatar/{size} only when there is one
// (U27): no profile, and a profile without a picture, are both false.
func TestPeopleSayWhetherTheyHaveAPicture(t *testing.T) {
	p := newPrincipal(t, "avatar-people")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "work")
	main := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Main"}`)
	ctx := t.Context()
	ids := map[string]string{}
	err := db.InTenant(dbtest.Seed(ctx), appPool, p.TenantID, func(tx pgx.Tx) error {
		for _, name := range []string{"Mira", "Ann"} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', $2) RETURNING id::text`, p.TenantID, name).Scan(&id); err != nil {
				return err
			}
			ids[name] = id
		}
		// Mira has a picture; Ann has a profile without one; the caller has no profile.
		if _, err := tx.Exec(ctx, `INSERT INTO personal_profiles (tenant_id, principal_id, avatar_original_hash, avatar_hashes) VALUES ($1, $2, $3, $4::jsonb)`,
			p.TenantID, ids["Mira"], strings.Repeat("a", 64), `{"32":"`+strings.Repeat("b", 64)+`","64":"`+strings.Repeat("c", 64)+`"}`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO personal_profiles (tenant_id, principal_id, first_name) VALUES ($1, $2, 'Ann')`, p.TenantID, ids["Ann"]); err != nil {
			return err
		}
		for _, actor := range []string{ids["Mira"], ids["Ann"]} {
			if _, err := tx.Exec(ctx, `INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after) VALUES ($1, $2, $3, 'node.updated', '{}'::jsonb)`, p.TenantID, actor, main.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"Mira", "Ann", "me"} {
		assignee := p.ID
		if who != "me" {
			assignee = ids[who]
		}
		mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"For `+who+`","parent_id":"`+main.ID+`","fields":{"assignee":"`+assignee+`"}}`)
	}

	status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+main.ID+"&kind=work", "")
	list := decode[nodePage](t, status, body, http.StatusOK)
	got := map[string]bool{}
	for _, item := range list.Items {
		if item.Assignee == nil {
			t.Fatalf("assignee missing: %#v", item)
		}
		got[item.Assignee.Name] = item.Assignee.HasAvatar
	}
	if len(got) != 3 || !got["Mira"] || got["Ann"] || got["avatar-people"] {
		t.Fatalf("list has_avatar = %v", got)
	}
	if !strings.Contains(string(body), `"has_avatar":false`) {
		t.Fatalf("has_avatar false must be sent, not omitted: %s", body)
	}

	status, body = call(t, &p, http.MethodGet, "/api/projects", "")
	projects := decode[projectPage](t, status, body, http.StatusOK)
	people := map[string]bool{}
	for _, item := range projects.Items {
		if item.ID == main.ID {
			for _, person := range item.People {
				people[person.Name] = person.HasAvatar
			}
		}
	}
	if len(people) != 3 || !people["Mira"] || people["Ann"] || people["avatar-people"] {
		t.Fatalf("project people has_avatar = %v", people)
	}
}
