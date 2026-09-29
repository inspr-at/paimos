// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func estimateAgent(t *testing.T, p tenant.Principal) tenant.Principal {
	t.Helper()
	agent := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Agent, Name: "estimate-worker", Roles: []string{"admin"}, Scopes: []string{"nodes.read", "nodes.write", "harness.read"}, KeyCreatorID: p.ID}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent',$2,$3) RETURNING id::text`, p.TenantID, agent.Name, agent.Roles).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, p.TenantID, agent.ID, "admin")
	return agent
}
func estimateFieldsOf(t *testing.T, n nodeJSON) map[string]any {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal(n.Fields, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEstimateValidationAttributionAndPreservation(t *testing.T) {
	p := newPrincipal(t, "estimates")
	agent := estimateAgent(t, p)
	kind := kindBySlug(t, p, "ticket")
	for _, bad := range []string{`0`, `-1`, `200.1`, `"2"`, `true`, `{}`, `1e999`} {
		code, body := call(t, &p, "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Invalid","fields":{"estimate_hours":%s}}`, kind.ID, bad))
		if code != 400 {
			t.Fatalf("%s: %d %s", bad, code, body)
		}
	}
	for _, actor := range []tenant.Principal{p, agent} {
		n := mustNode(t, actor, fmt.Sprintf(`{"kind_id":%q,"title":"Estimate","fields":{"estimate_hours":1.50,"estimate_by":%q,"estimate_at":"fake","estimate_confirmed":true}}`, kind.ID, p.ID))
		if n.Estimate == nil || n.Estimate.Hours == nil || *n.Estimate.Hours != 1.5 || n.Estimate.EstimatedChildren != 0 || n.Estimate.OpenChildren != 0 || n.Estimate.By == nil || n.Estimate.By.ID != actor.ID || n.Estimate.By.Name != actor.Name {
			t.Fatalf("create estimate view: %+v", n.Estimate)
		}
		f := estimateFieldsOf(t, n)
		if f["estimate_source"] != string(actor.Kind) || f["estimate_by"] != actor.ID || f["estimate_confirmed"] != (actor.Kind == tenant.Person) {
			t.Fatalf("provenance: %v", f)
		}
		if _, err := time.Parse(time.RFC3339Nano, f["estimate_at"].(string)); err != nil {
			t.Fatal(err)
		}
		// A replacement of other fields must not turn an agent draft into a person estimate.
		f["priority"] = "high"
		raw, _ := json.Marshal(map[string]any{"fields": f})
		code, body := call(t, &p, "PATCH", "/api/nodes/"+n.ID, string(raw))
		updated := decode[nodeJSON](t, code, body, 200)
		after := estimateFieldsOf(t, updated)
		if after["estimate_by"] != actor.ID || after["estimate_at"] != f["estimate_at"] {
			t.Fatal("unrelated edit rewrote provenance", after)
		}
		code, body = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"estimate_hours":1.5}}`)
		updated = decode[nodeJSON](t, code, body, 200)
		if estimateFieldsOf(t, updated)["estimate_source"] != "person" {
			t.Fatal("person confirmation did not attribute")
		}
		code, body = call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"estimate_hours":2,"estimate_source":"person"}}`)
		if code != 400 {
			t.Fatalf("spoof: %d %s", code, body)
		}
		code, body = call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"estimate_hours":null,"estimate_by":"spoof","priority":"low"}}`)
		updated = decode[nodeJSON](t, code, body, 200)
		if f := estimateFieldsOf(t, updated); len(f) != 1 || f["priority"] != "low" {
			t.Fatal("clear metadata", f)
		}
	}
	missing := mustNode(t, agent, fmt.Sprintf(`{"kind_id":%q,"title":"Draft"}`, kind.ID))
	if missing.Estimate != nil || strings.Contains(strings.Join(missing.Warnings, " "), "--estimate") || !strings.Contains(strings.Join(missing.Warnings, " "), "fields.estimate_hours") {
		t.Fatal("api warning", missing.Estimate, missing.Warnings)
	}
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/nodes", strings.NewReader(fmt.Sprintf(`{"kind_id":%q,"title":"CLI draft"}`, kind.ID)))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), agent))
	req.Header.Set("X-Aeon-Client", "cli")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	cliNode := decode[nodeJSON](t, rec.Code, rec.Body.Bytes(), http.StatusCreated)
	if !strings.Contains(strings.Join(cliNode.Warnings, " "), "--estimate") || strings.Contains(strings.Join(cliNode.Warnings, " "), "fields.estimate_hours") {
		t.Fatal("cli warning", cliNode.Warnings)
	}
	other := addPrincipal(t, "estimate-other")
	code, _ := call(t, &other, "PATCH", "/api/nodes/"+missing.ID, `{"fields":{"estimate_hours":2}}`)
	if code != 404 {
		t.Fatalf("cross tenant write: %d", code)
	}
	code, _ = call(t, &other, "GET", "/api/nodes/"+missing.ID, "")
	if code != 404 {
		t.Fatalf("cross tenant read: %d", code)
	}
}

func TestEstimateSortRollupAndRevision(t *testing.T) {
	p := newPrincipal(t, "estimate-rollup")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	epicKind := kindBySlug(t, p, "epic")
	task := kindBySlug(t, p, "task")
	proj := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, project.ID))
	epic := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Epic","parent_id":%q,"fields":{"estimate_hours":99}}`, epicKind.ID, proj.ID))
	makeChild := func(kind, parent, fields string) nodeJSON {
		return mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Work","parent_id":%q,"fields":%s}`, kind, parent, fields))
	}
	a := makeChild(ticket.ID, epic.ID, `{"estimate_hours":2}`)
	b := makeChild(ticket.ID, epic.ID, `{"estimate_hours":0.5}`)
	missing := makeChild(ticket.ID, epic.ID, `{}`)
	closed := makeChild(ticket.ID, epic.ID, `{"estimate_hours":10}`)
	deleted := makeChild(ticket.ID, epic.ID, `{"estimate_hours":20}`)
	_ = makeChild(task.ID, a.ID, `{"estimate_hours":50}`) // no double count
	// Custom closed state categories and malformed legacy values both stay safe.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=field_schema||'{"states":[{"state":"shipped","category":"done"}]}'::jsonb WHERE id=$1`, ticket.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET state='shipped' WHERE id=$1`, closed.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"estimate_hours":"invalid"}'::jsonb WHERE id=$1`, missing.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := call(t, &p, "GET", "/api/nodes/"+epic.ID, "")
	view := decode[nodeJSON](t, code, body, 200).Estimate
	if view == nil || view.Hours == nil || *view.Hours != 2.5 || view.OpenChildren != 3 || view.EstimatedChildren != 2 {
		t.Fatalf("rollup: %+v %s", view, body)
	}
	for _, direction := range []string{"estimate", "-estimate"} {
		var got []string
		cursor := ""
		for {
			path := "/api/nodes?parent_id=" + epic.ID + "&hide_closed=true&sort=" + direction + "&limit=1"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			code, body = call(t, &p, "GET", path, "")
			page := decode[nodePage](t, code, body, 200)
			for _, item := range page.Items {
				got = append(got, item.ID)
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		first, second := b.ID, a.ID
		if direction == "-estimate" {
			first, second = second, first
		}
		if len(got) != 3 || got[0] != first || got[1] != second || got[2] != missing.ID {
			t.Fatal("sort", direction, got)
		}
	}
	code, body = call(t, &p, "GET", "/api/nodes?parent_id="+proj.ID+"&sort=estimate", "")
	page := decode[nodePage](t, code, body, 200)
	if len(page.Items) != 1 || page.Items[0].Estimate == nil || *page.Items[0].Estimate.Hours != 2.5 {
		t.Fatalf("list rollup %s", body)
	}
	// A stale agent revision cannot overwrite or claim a person's estimate.
	agent := estimateAgent(t, p)
	code, body = call(t, &p, "PATCH", "/api/nodes/"+a.ID, `{"fields":{"estimate_hours":3}}`)
	personEstimate := decode[nodeJSON](t, code, body, 200)
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	r := httptest.NewRequest("PATCH", "/api/nodes/"+a.ID, strings.NewReader(`{"fields":{"estimate_hours":4}}`))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), agent))
	r.Header.Set("If-Unmodified-Since", a.UpdatedAt.Format(time.RFC3339Nano))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 412 {
		t.Fatalf("stale estimate: %d %s", w.Code, w.Body.String())
	}
	code, body = call(t, &p, "GET", "/api/nodes/"+a.ID, "")
	preserved := decode[nodeJSON](t, code, body, 200)
	f := estimateFieldsOf(t, preserved)
	if f["estimate_hours"] != float64(3) || f["estimate_source"] != "person" || f["estimate_by"] != p.ID || !preserved.UpdatedAt.Equal(personEstimate.UpdatedAt) {
		t.Fatalf("stale agent write changed person estimate: %s", body)
	}
}

func TestWorkingAgentConfirmsEstimate(t *testing.T) {
	p := newPrincipal(t, "estimate-confirm")
	agent := estimateAgent(t, p)
	proj := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	n := mustNode(t, agent, fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Work","fields":{"estimate_hours":2}}`, kindBySlug(t, p, "ticket").ID, proj.ID))
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(repeat('00',32),'hex'),decode(repeat('01',32),'hex'),'working')`, p.TenantID, proj.ID, agent.ID, n.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := call(t, &agent, "PATCH", "/api/nodes/"+n.ID, `{"fields":{"estimate_hours":2}}`)
	updated := decode[nodeJSON](t, code, body, 200)
	if f := estimateFieldsOf(t, updated); f["estimate_confirmed"] != true || f["estimate_source"] != "agent" {
		t.Fatal("working agent confirmation", f)
	}
}
