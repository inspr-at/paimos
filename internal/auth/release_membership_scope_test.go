// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Exercise the mounted endpoint through the real bearer authentication and
// permission middleware. A handler-only principal fixture misses the outer
// agent route allowlist.
func TestAgentKeyReleaseMembershipReadScopeAndVisibility(t *testing.T) {
	reset(t)
	m := newMod(t, Config{})
	tenantID := insertTenant(t, "membership-agent", "Membership agent")
	otherTenantID := insertTenant(t, "membership-other", "Membership other")

	createProjectAndTicket := func(tid, key string) (string, string) {
		t.Helper()
		var projectID, ticketID string
		err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
				SELECT $1::uuid,id,$2,$2 FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
				RETURNING nodes.id::text`, tid, key+"-1").Scan(&projectID); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
				SELECT $1::uuid,id,$2,$2,$3::uuid FROM node_kinds WHERE tenant_id=$1::uuid AND slug='ticket'
				RETURNING nodes.id::text`, tid, key+"-2", projectID).Scan(&ticketID)
		})
		if err != nil {
			t.Fatal(err)
		}
		return projectID, ticketID
	}
	projectID, ticketID := createProjectAndTicket(tenantID, "MEM")
	otherProjectID, otherTicketID := createProjectAndTicket(tenantID, "MBR")
	foreignProjectID, foreignTicketID := createProjectAndTicket(otherTenantID, "MBC")
	owner := tenant.Principal{TenantID: tenantID, Kind: tenant.Person, Roles: []string{"super_admin"}}
	if err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles)
			VALUES($1::uuid,'person','Membership owner',ARRAY['super_admin']) RETURNING id::text`, tenantID).Scan(&owner.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, tenantID, owner.ID)
	}); err != nil {
		t.Fatal(err)
	}

	reader, err := m.createAgentKey(t.Context(), owner, "membership-reader", "", []string{"nodes.read", "releases.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	withoutScope, err := m.createAgentKey(t.Context(), owner, "membership-no-read", "", []string{"nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Node visibility requires nodes.read. The key's backing grant covers only
	// one project, so releases.read cannot expose another project.
	if err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET scope_type='project',scope_id=$2::uuid
			WHERE tenant_id=$1::uuid AND principal_id=$3::uuid`, tenantID, projectID, reader.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	releases.New(appPool).Mount(mux)
	secured := m.Middleware(mux)
	request := func(method, project, token string, ids ...string) *httptest.ResponseRecorder {
		t.Helper()
		path := "/api/projects/" + project + "/release-memberships?" + url.Values{"ticket_node_id": ids}.Encode()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		_, req.Pattern = mux.Handler(req)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, req)
		return w
	}

	got := request(http.MethodGet, projectID, reader.Token, ticketID, otherTicketID, foreignTicketID)
	if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("same project read: %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Tickets []struct {
			TicketID  string  `json:"ticket_node_id"`
			ReleaseID *string `json:"release_node_id"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tickets) != 1 || body.Tickets[0].TicketID != ticketID || body.Tickets[0].ReleaseID != nil {
		t.Fatalf("membership visibility: %+v", body.Tickets)
	}
	if got := request(http.MethodHead, projectID, reader.Token, ticketID); got.Code != http.StatusOK {
		t.Fatalf("HEAD read: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, projectID, withoutScope.Token, ticketID); got.Code != http.StatusForbidden {
		t.Fatalf("missing releases.read: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, projectID, reader.Token, ticketID); got.Code != http.StatusForbidden {
		t.Fatalf("read key gained mutation: %d %s", got.Code, got.Body.String())
	}
	var denial string
	for _, tc := range []struct{ project, ticket string }{{otherProjectID, otherTicketID}, {foreignProjectID, foreignTicketID}} {
		got := request(http.MethodGet, tc.project, reader.Token, tc.ticket)
		if got.Code != http.StatusForbidden {
			t.Fatalf("foreign project read: %d %s", got.Code, got.Body.String())
		}
		if denial != "" && got.Body.String() != denial {
			t.Fatalf("cross-tenant denial disclosed a different response")
		}
		denial = got.Body.String()
	}
}
