// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPortalPublicationGuard(t *testing.T) {
	admin := newPrincipal(t, "portal-pub")
	member := addRolePrincipal(t, admin, "Mina", "member", tenant.Person, nil)
	agent := addRolePrincipal(t, admin, "Portal agent", "admin", tenant.Agent, []string{"nodes.write", "settings.manage"})
	wishKind := kindBySlug(t, admin, "portal_wish").ID
	featureKind := kindBySlug(t, admin, "portal_feature").ID
	ticketKind := kindBySlug(t, admin, "ticket").ID

	status, body := call(t, &admin, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Quiet wish","state":"pending"}`, wishKind))
	wish := decode[nodeJSON](t, status, body, http.StatusCreated)
	status, body = call(t, &member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Ordinary"}`, ticketKind))
	ticket := decode[nodeJSON](t, status, body, http.StatusCreated)

	denied := func(p *tenant.Principal, method, path, payload string) {
		t.Helper()
		code, raw := call(t, p, method, path, payload)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s as %s: %d %s", method, path, p.Kind, code, raw)
		}
	}
	denied(&member, http.MethodPatch, "/api/nodes/"+wish.ID, `{"state":"published"}`)
	denied(&agent, http.MethodPatch, "/api/nodes/"+wish.ID, `{"state":"published"}`)
	if got := readNodeState(t, admin, wish.ID); got != "pending" {
		t.Fatalf("wish state %s", got)
	}
	if code, raw := call(t, &member, http.MethodPatch, "/api/nodes/"+wish.ID, `{"title":"Renamed wish"}`); code != http.StatusOK {
		t.Fatalf("member title: %d %s", code, raw)
	}
	denied(&member, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+wish.ID+`","`+ticket.ID+`"],"state":"published"}`)
	denied(&agent, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+wish.ID+`"],"state":"published"}`)
	if got := readNodeState(t, admin, wish.ID); got != "pending" {
		t.Fatalf("wish after bulk %s", got)
	}
	if got := readNodeState(t, admin, ticket.ID); got != "open" {
		t.Fatalf("ticket after bulk %s", got)
	}
	if code, raw := call(t, &member, http.MethodPatch, "/api/nodes/"+ticket.ID, `{"state":"qa"}`); code != http.StatusOK {
		t.Fatalf("member ticket: %d %s", code, raw)
	}

	status, body = call(t, &admin, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Radar","state":"live","fields":{"legal_basis":"§ 1"}}`, featureKind))
	feature := decode[nodeJSON](t, status, body, http.StatusCreated)
	denied(&member, http.MethodPatch, "/api/nodes/"+feature.ID, `{"fields":{"legal_basis":"secret"}}`)
	denied(&agent, http.MethodPatch, "/api/nodes/"+feature.ID, `{"fields":{"decline_reason":"no"}}`)
	if code, raw := call(t, &admin, http.MethodPatch, "/api/nodes/"+feature.ID, `{"fields":{"legal_basis":"§ 2"}}`); code != http.StatusOK {
		t.Fatalf("admin fields: %d %s", code, raw)
	}

	denied(&member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Sneaky","state":"published"}`, wishKind))
	denied(&agent, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Agent wish","state":"published"}`, wishKind))
	status, body = call(t, &admin, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Public wish","state":"published"}`, wishKind))
	decode[nodeJSON](t, status, body, http.StatusCreated)
	if code, raw := call(t, &member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Draft wish"}`, wishKind)); code != http.StatusCreated {
		t.Fatalf("member draft: %d %s", code, raw)
	}

	code, raw := call(t, &admin, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+wish.ID+`"],"state":"published"}`)
	batch := decode[bulkResult](t, code, raw, http.StatusOK)
	if batch.EventID == nil {
		t.Fatal("missing bulk event")
	}
	dbtest.BindRole(t, testDB, admin.TenantID, admin.ID, "member")
	undoCode, undoBody := callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &admin, http.MethodPost, "/api/events/"+strconv.FormatInt(*batch.EventID, 10)+"/undo", "")
	if undoCode != http.StatusForbidden {
		t.Fatalf("undo: %d %s", undoCode, undoBody)
	}
	if got := readNodeState(t, admin, wish.ID); got != "published" {
		t.Fatalf("undo changed wish to %s", got)
	}
}

func addRolePrincipal(t *testing.T, admin tenant.Principal, name, role string, kind tenant.PrincipalKind, scopes []string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: admin.TenantID, Kind: kind, Name: name, Roles: []string{role}, Scopes: scopes}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,$2,$3,ARRAY[$4]::text[]) RETURNING id::text`, admin.TenantID, string(kind), name, role).Scan(&p.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, admin.TenantID, p.ID, role)
	return p
}

func readNodeState(t *testing.T, p tenant.Principal, id string) string {
	t.Helper()
	var state string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1::uuid`, id).Scan(&state)
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
