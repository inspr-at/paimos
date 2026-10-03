// SPDX-License-Identifier: AGPL-3.0-only
package knowledge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestKnowledgeBacklogVisibleLinksDeduplicateIntersectAndPage(t *testing.T) {
	f := setup(t)
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "owner")
	first := addNode(t, f, "RUN-1", "runbook", "Scoped first", &f.project)
	second := addNode(t, f, "RUN-2", "runbook", "Scoped second", &f.project)
	unrelated := addNode(t, f, "RUN-3", "runbook", "Other entry", &f.project)
	foreign := addNode(t, f, "RUN-4", "runbook", "Other project context", &f.other)
	var ranked, deleted string
	ctx := dbtest.Seed(t.Context())
	err := db.InTenant(ctx, f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.a.TenantID, f.project, f.a.ID); e != nil {
			return e
		}
		node := func(key, state string) (string, error) {
			var id string
			e := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state) SELECT $1,id,$2,$2,$3,$4 FROM node_kinds WHERE tenant_id=$1 AND slug='task' RETURNING id::text`, f.a.TenantID, key, f.project, state).Scan(&id)
			return id, e
		}
		var e error
		ranked, e = node("TK-2", "done")
		if e != nil {
			return e
		}
		deleted, e = node("TK-3", "open")
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,rank,source,placed_by) VALUES($1,$2,$3,'V','person',$4)`, f.a.TenantID, f.project, ranked, f.a.ID); e != nil {
			return e
		}
		for _, pair := range [][2]string{{first, f.ticket}, {ranked, first}, {second, ranked}, {unrelated, deleted}, {foreign, f.ticket}} {
			if strings.Compare(pair[0], pair[1]) > 0 {
				pair[0], pair[1] = pair[1], pair[0]
			}
			if _, e = tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'relates')`, f.a.TenantID, pair[0], pair[1]); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, deleted)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/knowledge?project_id=" + f.project + "&ships_in=none&limit=1&q=Scoped"
	get := func(path string) ListPage {
		t.Helper()
		rr := call(t, f, f.a, "GET", path, nil)
		if rr.Code != 200 {
			t.Fatalf("%d %s", rr.Code, rr.Body.String())
		}
		var p ListPage
		if e := json.Unmarshal(rr.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	page := get(path)
	if page.Total != 2 || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first %+v", page)
	}
	next := get(path + "&cursor=" + page.NextCursor)
	if len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID || next.NextCursor != "" {
		t.Fatalf("next %+v", next)
	}
	rr := call(t, f, f.a, "GET", path+"&cursor="+page.NextCursor+"&status=archived", nil)
	if rr.Code != 400 {
		t.Fatalf("cursor mismatch: %d %s", rr.Code, rr.Body.String())
	}
	empty := get(path + "&status=archived")
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("status intersection %+v", empty)
	}
	for _, id := range []string{unrelated, foreign} {
		for _, it := range append(page.Items, next.Items...) {
			if it.ID == id {
				t.Fatal("deleted/cross-project leaked")
			}
		}
	}
	if rr = call(t, f, f.foreign, "GET", path, nil); rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
		t.Fatalf("tenant scope: %d", rr.Code)
	}
}
func TestKnowledgeScopeValidationAndK1Gate(t *testing.T) {
	f := setup(t)
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "owner")
	for _, q := range []string{"ships_in=none", "project_id=" + f.project + "&ships_in=!none", "project_id=" + f.project + "&ships_in=none&limit=201", "project_id=" + f.project + "&ships_in=none&ships_in=none"} {
		rr := call(t, f, f.a, "GET", "/api/knowledge?"+q, nil)
		if rr.Code != 400 {
			t.Fatalf("validation %s: %d", q, rr.Code)
		}
	}
	ctx := dbtest.Seed(t.Context())
	var release string
	err := db.InTenant(ctx, f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.a.TenantID, f.project, f.a.ID); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'REL-1','Planned',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='release' RETURNING id::text`, f.a.TenantID, f.project).Scan(&release); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,rank,visibility) VALUES($1,$2,$3,'V','internal')`, f.a.TenantID, f.project, release)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := call(t, f, f.a, "GET", "/api/knowledge?project_id="+f.project+"&ships_in="+release, nil)
	if rr.Code != 409 {
		t.Fatalf("K1 must stay closed: %d %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["code"] != "release_context_undecided" {
		t.Fatalf("wrong refusal %v", body)
	}
}

func TestKnowledgeBacklogTimestampCursorPagesAndTies(t *testing.T) {
	f := setup(t)
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "owner")
	ids := []string{}
	for i := range 4 {
		ids = append(ids, addNode(t, f, fmt.Sprintf("RUN-%d", i+1), "runbook", "Scoped note", &f.project))
	}
	ctx := dbtest.Seed(t.Context())
	err := db.InTenant(ctx, f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by) VALUES($1,$2,$3)`, f.a.TenantID, f.project, f.a.ID); err != nil {
			return err
		}
		for i, id := range ids {
			pair := []string{id, f.ticket}
			sort.Strings(pair)
			if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'relates')`, f.a.TenantID, pair[0], pair[1]); err != nil {
				return err
			}
			created := time.Date(2026, 10, 1, 10+i/2, 0, 0, 123456000, time.UTC)
			updated := time.Date(2026, 10, 2, 10+(3-i)/2, 0, 0, 654321000, time.UTC)
			if _, err := tx.Exec(ctx, `UPDATE nodes SET created_at=$2,updated_at=$3 WHERE id=$1`, id, created, updated); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"", "updated", "created"} {
		t.Run("sort="+order, func(t *testing.T) {
			want := append([]string{}, ids...)
			sort.Slice(want, func(i, j int) bool {
				index := func(id string) int {
					for k, v := range ids {
						if v == id {
							return k
						}
					}
					panic("missing fixture")
				}
				a, b := index(want[i]), index(want[j])
				if order == "created" && a/2 != b/2 {
					return a/2 > b/2
				}
				if (3-a)/2 != (3-b)/2 {
					return (3-a)/2 > (3-b)/2
				}
				return want[i] > want[j]
			})
			path := "/api/knowledge?project_id=" + f.project + "&ships_in=none&limit=1"
			if order != "" {
				path += "&sort=" + order
			}
			cursor := ""
			for pageIndex, id := range want {
				rr := call(t, f, f.a, "GET", path+"&cursor="+url.QueryEscape(cursor), nil)
				if rr.Code != 200 {
					t.Fatalf("page %d: %d %s", pageIndex, rr.Code, rr.Body.String())
				}
				var page ListPage
				if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if page.Total != len(ids) || len(page.Items) != 1 || page.Items[0].ID != id {
					t.Fatalf("page %d: %+v; want %s", pageIndex, page, id)
				}
				if pageIndex == len(want)-1 {
					if page.NextCursor != "" {
						t.Fatal("final page has cursor")
					}
					break
				}
				if page.NextCursor == "" {
					t.Fatal("nonfinal page missing cursor")
				}
				raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				var decoded contextCursor
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				if _, err := time.Parse(time.RFC3339Nano, decoded.Value); err != nil {
					t.Errorf("timestamp cursor is not RFC3339Nano: %q", decoded.Value)
				}
				cursor = page.NextCursor
			}
		})
	}
}
