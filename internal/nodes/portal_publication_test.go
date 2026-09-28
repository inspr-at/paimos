// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/cli"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/portal"
	"github.com/inspr-at/paimos/internal/relations"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPortalMutationGuard(t *testing.T) {
	admin := newPrincipal(t, "portal-guard")
	member := addRolePrincipal(t, admin, "Mina", "member", tenant.Person, nil)
	agent := addRolePrincipal(t, admin, "Portal agent", "admin", tenant.Agent, []string{
		"nodes.read", "nodes.write", "nodes.move", "nodes.delete",
		"events.read", "events.undo", "events.undo_other",
		"relations.write", "relations.delete", "settings.manage",
	})
	productKind := kindBySlug(t, admin, "portal_product").ID
	featureKind := kindBySlug(t, admin, "portal_feature").ID
	wishKind := kindBySlug(t, admin, "portal_wish").ID
	ticketKind := kindBySlug(t, admin, "ticket").ID
	product := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Public product","state":"published"}`, productKind))
	other := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Private product","state":"unpublished"}`, productKind))

	actors := []struct {
		name  string
		p     tenant.Principal
		allow bool
	}{
		{"member", member, false},
		{"agent", agent, false},
		{"moderator", admin, true},
	}

	type portalCase struct {
		name      string
		allowCode int
		setup     func(t *testing.T) (run func(tenant.Principal) int, snap func() string)
	}
	cases := []portalCase{
		{name: "patch-title", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			feature := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Feature title","state":"live","parent_id":%q}`, featureKind, product.ID))
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodPatch, "/api/nodes/"+feature.ID, `{"title":"Renamed feature"}`)
			}, func() string { return readNodeColumn(t, admin, feature.ID, "title") }
		}},
		{name: "patch-body", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			feature := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Feature body","state":"live","parent_id":%q,"body":"Original copy"}`, featureKind, product.ID))
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodPatch, "/api/nodes/"+feature.ID, `{"body":"Rewritten copy"}`)
			}, func() string { return readNodeColumn(t, admin, feature.ID, "body") }
		}},
		{name: "patch-state", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"State wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodPatch, "/api/nodes/"+wish.ID, `{"state":"hidden"}`)
			}, func() string { return readNodeColumn(t, admin, wish.ID, "state") }
		}},
		{name: "move", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Move wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodPost, "/api/nodes/"+wish.ID+"/move", `{"parent_id":null}`)
			}, func() string { return readNodeColumn(t, admin, wish.ID, "parent") }
		}},
		{name: "bulk-parent", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Bulk parent wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			body := fmt.Sprintf(`{"ids":[%q],"parent_id":%q}`, wish.ID, other.ID)
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodPost, "/api/nodes/bulk", body)
			}, func() string { return readNodeColumn(t, admin, wish.ID, "parent") }
		}},
		{name: "bulk-state", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Bulk state wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			body := fmt.Sprintf(`{"ids":[%q],"state":"hidden"}`, wish.ID)
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodPost, "/api/nodes/bulk", body)
			}, func() string { return readNodeColumn(t, admin, wish.ID, "state") }
		}},
		{name: "delete", allowCode: http.StatusNoContent, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Delete wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			return func(p tenant.Principal) int {
				return callCode(t, &p, http.MethodDelete, "/api/nodes/"+wish.ID, "")
			}, func() string { return readNodeColumn(t, admin, wish.ID, "deleted") }
		}},
		{name: "undo", allowCode: http.StatusCreated, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Undo wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			code, raw := call(t, &admin, http.MethodPost, "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q],"parent_id":%q}`, wish.ID, other.ID))
			batch := decode[bulkResult](t, code, raw, http.StatusOK)
			if batch.EventID == nil || readNodeColumn(t, admin, wish.ID, "parent") != other.ID {
				t.Fatalf("undo setup parent %s event %v", readNodeColumn(t, admin, wish.ID, "parent"), batch.EventID)
			}
			path := "/api/events/" + strconv.FormatInt(*batch.EventID, 10) + "/undo"
			mod := events.New(appPool, events.WithUndoHandlers(UndoHandlers()))
			return func(p tenant.Principal) int {
				status, _ := callAs(t, mod, &p, http.MethodPost, path, "")
				return status
			}, func() string { return readNodeColumn(t, admin, wish.ID, "parent") }
		}},
		{name: "apply", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			feature := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Apply feature","state":"live","parent_id":%q}`, featureKind, product.ID))
			tokens := map[string]tenant.Principal{
				"member-portal-token":    member,
				"agent-portal-token":     agent,
				"moderator-portal-token": admin,
			}
			inner := http.NewServeMux()
			New(appPool, nil).Mount(inner)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
				if !ok {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				inner.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
			}))
			t.Cleanup(srv.Close)
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY_FILE", "")
			plan := "update:\n  - ref: " + feature.ID + "\n    fields:\n      title: Applied catalog title\n"
			tokenFor := map[string]string{member.ID: "member-portal-token", agent.ID: "agent-portal-token", admin.ID: "moderator-portal-token"}
			return func(p tenant.Principal) int {
				t.Setenv("AEON_API_KEY", tokenFor[p.ID])
				var stderr bytes.Buffer
				code := cli.Run([]string{"aeon", "apply", "--from-file", "-"}, strings.NewReader(plan), io.Discard, &stderr)
				if code == 0 {
					return http.StatusOK
				}
				if strings.Contains(stderr.String(), "403") {
					return http.StatusForbidden
				}
				t.Fatalf("apply exit %d: %s", code, stderr.String())
				return 0
			}, func() string { return readNodeColumn(t, admin, feature.ID, "title") }
		}},
		{name: "import", allowCode: http.StatusOK, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Import wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			return func(p tenant.Principal) int {
				ctx := dbtest.Seed(t.Context())
				err := db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `UPDATE nodes SET title=$2 WHERE id=$1::uuid`, wish.ID, "Imported catalog title"); err != nil {
						return err
					}
					_, err := events.Append(ctx, tx, p, events.Change{
						NodeID: &wish.ID,
						Type:   "import.node_updated",
						Before: map[string]any{"id": wish.ID, "title": "Import wish", "parent_id": product.ID},
						After:  map[string]any{"id": wish.ID, "title": "Imported catalog title", "parent_id": product.ID},
					})
					return err
				})
				if err == nil {
					return http.StatusOK
				}
				if errors.Is(err, events.ErrForbidden) {
					return http.StatusForbidden
				}
				t.Fatal(err)
				return 0
			}, func() string { return readNodeColumn(t, admin, wish.ID, "title") }
		}},
		{name: "relation", allowCode: http.StatusCreated, setup: func(t *testing.T) (func(tenant.Principal) int, func() string) {
			wish := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Relation wish","state":"published","parent_id":%q}`, wishKind, product.ID))
			body := fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"relates"}`, wish.ID, product.ID)
			mod := relations.New(appPool)
			return func(p tenant.Principal) int {
				status, raw := callAs(t, mod, &p, http.MethodPost, "/api/relations", body)
				if status != http.StatusCreated && status != http.StatusForbidden {
					t.Fatalf("relation: %d %s", status, raw)
				}
				return status
			}, func() string { return strconv.Itoa(countRelations(t, admin, wish.ID)) }
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, snap := tc.setup(t)
			for _, actor := range actors {
				before := snap()
				code := run(actor.p)
				after := snap()
				if actor.allow {
					if code != tc.allowCode || after == before {
						t.Fatalf("%s: code %d snap %q -> %q", actor.name, code, before, after)
					}
					continue
				}
				if code != http.StatusForbidden || after != before {
					t.Fatalf("%s: code %d snap %q -> %q", actor.name, code, before, after)
				}
			}
		})
	}

	if code, raw := call(t, &member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Draft wish"}`, wishKind)); code != http.StatusForbidden {
		t.Fatalf("member draft: %d %s", code, raw)
	}
	if code, raw := call(t, &admin, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Moderated wish","state":"published"}`, wishKind)); code != http.StatusCreated {
		t.Fatalf("moderator wish: %d %s", code, raw)
	}
	if code, raw := call(t, &member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Ordinary ticket"}`, ticketKind)); code != http.StatusCreated {
		t.Fatalf("member ticket: %d %s", code, raw)
	}
	if code, raw := call(t, &member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Nested ticket","parent_id":%q}`, ticketKind, product.ID)); code != http.StatusForbidden {
		t.Fatalf("member ticket under product: %d %s", code, raw)
	}
	nested := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Placed ticket","parent_id":%q}`, ticketKind, product.ID))
	if code, raw := call(t, &member, http.MethodPatch, "/api/nodes/"+nested.ID, `{"title":"Edited ticket"}`); code != http.StatusOK {
		t.Fatalf("member ticket edit: %d %s", code, raw)
	}
	if code, raw := call(t, &member, http.MethodPost, "/api/nodes/"+nested.ID+"/move", `{"parent_id":null}`); code != http.StatusForbidden {
		t.Fatalf("member ticket out: %d %s", code, raw)
	}
	if parent := readNodeColumn(t, admin, nested.ID, "parent"); parent != product.ID {
		t.Fatalf("ticket parent %s", parent)
	}
	if code, raw := call(t, &admin, http.MethodPost, "/api/nodes/"+nested.ID+"/move", `{"parent_id":null}`); code != http.StatusOK {
		t.Fatalf("moderator ticket out: %d %s", code, raw)
	}

	pending := mustCreateNode(t, admin, fmt.Sprintf(`{"kind_id":%q,"title":"Pending wish","state":"pending","parent_id":%q}`, wishKind, product.ID))
	mod := portal.New(appPool, false, bytes.Repeat([]byte{12}, 32))
	if code, raw := callAs(t, mod, &admin, http.MethodPost, "/api/portal/wishes/"+pending.ID+"/publish", "{}"); code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, raw)
	}
	if got := readNodeColumn(t, admin, pending.ID, "state"); got != "published" {
		t.Fatalf("published state %s", got)
	}
	if err := appendPortal(t, agent, pending.ID, "portal.wish_submitted"); err != nil {
		t.Fatal(err)
	}
	if err := appendPortal(t, agent, pending.ID, "portal.vote_cast"); err != nil {
		t.Fatal(err)
	}
	if err := appendPortal(t, member, pending.ID, "comment.created"); err != nil {
		t.Fatal(err)
	}
	if err := appendPortal(t, agent, pending.ID, "node.updated"); !errors.Is(err, events.ErrForbidden) {
		t.Fatalf("agent catalog write: %v", err)
	}
}

func mustCreateNode(t *testing.T, p tenant.Principal, body string) nodeJSON {
	t.Helper()
	status, raw := call(t, &p, http.MethodPost, "/api/nodes", body)
	return decode[nodeJSON](t, status, raw, http.StatusCreated)
}

func callCode(t *testing.T, p *tenant.Principal, method, path, body string) int {
	t.Helper()
	status, raw := call(t, p, method, path, body)
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusNoContent && status != http.StatusForbidden {
		t.Fatalf("%s %s: %d %s", method, path, status, raw)
	}
	return status
}

func readNodeColumn(t *testing.T, p tenant.Principal, id, column string) string {
	t.Helper()
	var title, body, state string
	var parent *string
	var deleted bool
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT title, body, state, parent_id::text, deleted_at IS NOT NULL FROM nodes WHERE id=$1::uuid`, id).Scan(&title, &body, &state, &parent, &deleted)
	})
	if err != nil {
		t.Fatal(err)
	}
	switch column {
	case "title":
		return title
	case "body":
		return body
	case "state":
		return state
	case "parent":
		if parent == nil {
			return ""
		}
		return *parent
	case "deleted":
		if deleted {
			return "deleted"
		}
		return "live"
	default:
		t.Fatalf("column %s", column)
		return ""
	}
}

func countRelations(t *testing.T, p tenant.Principal, id string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM node_relations WHERE source_node_id=$1::uuid OR target_node_id=$1::uuid`, id).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func appendPortal(t *testing.T, p tenant.Principal, id, eventType string) error {
	t.Helper()
	ctx := dbtest.Seed(t.Context())
	return db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, p, events.Change{
			NodeID: &id,
			Type:   eventType,
			After:  map[string]any{"id": id, "title": "catalog"},
		})
		return err
	})
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
