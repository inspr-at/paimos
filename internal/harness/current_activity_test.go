// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCurrentActivityPolicyFreshnessHistoryAndIsolation(t *testing.T) {
	f := fixture(t)
	lease := "current-activity-lease-000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.agent, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "activity-test", "harness_session_ref": "current-activity-ref-0000000000001", "worker_lease": lease, "management_mode": "unmanaged", "role": "worker"}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	seq := 0
	beat := func(extra map[string]any, code int) map[string]any {
		t.Helper()
		seq++
		body := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": seq}
		for k, v := range extra {
			body[k] = v
		}
		w := f.call(f.agent, "POST", path+"/heartbeat", body, lease)
		expect(t, w, code)
		return decode(t, w)
	}
	auto := func(text string) map[string]any {
		return map[string]any{"text": text, "source": "auto", "at": time.Now().UTC().Format(time.RFC3339Nano)}
	}
	settings := "/api/settings/agent-activity"
	expect(t, f.call(f.person, "GET", settings, nil, ""), 200)
	if decode(t, f.call(f.person, "GET", settings, nil, ""))["mode"] != "agent_summary" {
		t.Fatal("wrong default policy")
	}
	expect(t, f.call(f.agent, "PUT", settings, map[string]any{"mode": "off"}, ""), 403)
	expect(t, f.call(f.person, "PUT", settings, map[string]any{"mode": "other"}, ""), 400)
	result := beat(map[string]any{"doing": "Implementing activity", "tool_activity": auto("Running Go tests")}, 200)
	if result["activity"] != "busy" || result["agent_activity_mode"] != "agent_summary" || result["current_activity"].(map[string]any)["source"] != "agent" {
		t.Fatalf("additive heartbeat: %v", result)
	}
	beat(map[string]any{"doing": "Implementing activity"}, 200)
	detail := decode(t, f.call(f.person, "GET", path, nil, ""))
	if len(detail["current_activity_history"].([]any)) != 1 {
		t.Fatal("unchanged summary appended history")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET doing_at=now()-interval '11 minutes' WHERE id=$1`, id)
		return err
	})
	detail = decode(t, f.call(f.person, "GET", path, nil, ""))
	if detail["current_activity"].(map[string]any)["text"] != "Running Go tests" {
		t.Fatal("expired summary did not fall back to tool")
	}
	expect(t, f.call(f.person, "PUT", settings, map[string]any{"mode": "tool_activity"}, ""), 200)
	result = beat(map[string]any{"doing": "Ignored agent phrase", "activity_note": "Ignored legacy note", "tool_activity": auto("Committing")}, 200)
	if result["current_activity"].(map[string]any)["source"] != "auto" || result["activity_note"] != nil {
		t.Fatal("tool-only policy collected or displayed an agent note")
	}
	beat(map[string]any{"tool_activity": auto("arbitrary PRIVATE_ARGUMENT")}, 400)
	for _, text := range []string{"Editing AKIAIOSFODNN7EXAMPLE.go", "Editing sk-live.go", "Editing ghp_example.ts", "Editing xoxb-example.ts", "Editing id-rsa.go"} {
		beat(map[string]any{"tool_activity": auto(text)}, 400)
	}
	bad := auto("Working")
	bad["source"] = "agent"
	beat(map[string]any{"tool_activity": bad}, 400)
	bad = auto("Working")
	bad["at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	beat(map[string]any{"tool_activity": bad}, 400)
	expect(t, f.call(f.person, "PUT", settings, map[string]any{"mode": "off"}, ""), 200)
	result = beat(map[string]any{"doing": "Ignored", "activity_note": "Ignored", "tool_activity": auto("Pushing")}, 200)
	if result["current_activity"] != nil || result["agent_activity_mode"] != "off" {
		t.Fatal("off displayed activity")
	}
	detail = decode(t, f.call(f.person, "GET", path, nil, ""))
	if _, ok := detail["current_activity_history"]; ok {
		t.Fatal("off exposed stored history")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var doing, tool string
		if err := tx.QueryRow(t.Context(), `SELECT doing,tool_activity FROM harness_sessions WHERE id=$1`, id).Scan(&doing, &tool); err != nil {
			return err
		}
		if doing != "Implementing activity" || tool != "Committing" {
			t.Fatal("off or tool-only policy stored excluded text")
		}
		return nil
	})
	f.tx(t, f.foreign, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_current_activity WHERE session_id=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("foreign tenant read activity")
		}
		return nil
	})
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	expect(t, f.call(f.person, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 100, "doing": "Spoofed"}, lease), 403)
	expect(t, f.call(f.person, "PUT", settings, map[string]any{"mode": "agent_summary"}, ""), 200)
	beat(map[string]any{"doing": "Invalid\nsummary"}, 400)
	for _, text := range []string{"A\u0301KIAIOSFODNN7EXAMPLE", "abcdefghijkl\u0301mnopqrstuvwx", "A\u20ddKIAIOSFODNN7EXAMPLE"} {
		beat(map[string]any{"doing": text}, 400)
	}
	for i := 0; i < 23; i++ {
		beat(map[string]any{"doing": fmt.Sprintf("Phase %d", i)}, 200)
	}
	detail = decode(t, f.call(f.person, "GET", path, nil, ""))
	if len(detail["current_activity_history"].([]any)) != 20 {
		t.Fatal("current activity history is unbounded")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var forced bool
		err := tx.QueryRow(t.Context(), `SELECT relforcerowsecurity FROM pg_class WHERE oid='harness_current_activity'::regclass`).Scan(&forced)
		if !forced {
			t.Fatal("activity is not FORCE RLS")
		}
		return err
	})
}

func TestCurrentActivityProjectIsolation(t *testing.T) {
	f := fixture(t)
	lease := "project-activity-lease-" + uid()
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.agent, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "activity-test",
		"harness_session_ref": "project-activity-ref-" + uid(), "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{
		"phase": "working", "activity_sequence": 1, "doing": "Implementing activity",
	}, lease), 200)

	hidden := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	otherProject := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title)
			SELECT $1,$2,'ACT-3',kind_id,'Other project' FROM nodes WHERE id=$3`, f.person.TenantID, otherProject, f.project); err != nil {
			return err
		}
		for _, binding := range []struct {
			principal tenant.Principal
			project   string
		}{{hidden, otherProject}, {reader, f.project}} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Activity reader')`, binding.principal.TenantID, binding.principal.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
				SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, binding.principal.TenantID, binding.principal.ID, binding.project); err != nil {
				return err
			}
		}
		return nil
	})

	// Use principal visibility, not the fixture's all-project service context.
	ctx := tenant.WithPrincipal(t.Context(), hidden)
	if err := db.InTenant(ctx, f.db.App, hidden.TenantID, func(tx pgx.Tx) error {
		var sessions, activities int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM harness_sessions WHERE id=$1`, id).Scan(&sessions); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM harness_current_activity WHERE session_id=$1`, id).Scan(&activities); err != nil {
			return err
		}
		if sessions != 0 || activities != 0 {
			t.Fatalf("same-tenant principal saw hidden project: sessions=%d activities=%d", sessions, activities)
		}
		for _, query := range []string{
			`UPDATE harness_current_activity SET text='Working' WHERE session_id=$1`,
			`DELETE FROM harness_current_activity WHERE session_id=$1`,
		} {
			tag, err := tx.Exec(ctx, query, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				t.Fatalf("same-tenant principal modified %d hidden activity rows", tag.RowsAffected())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(ctx, f.db.App, hidden.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO harness_current_activity(tenant_id,session_id,text,source,at)
			VALUES($1,$2,'Working','auto',clock_timestamp())`, hidden.TenantID, id)
		return err
	})
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatalf("same-tenant insert was not rejected by RLS: %v", err)
	}
	expect(t, f.call(hidden, "GET", path, nil, ""), 404)
	expect(t, f.call(reader, "GET", path, nil, ""), 200)

	ctx = tenant.WithPrincipal(t.Context(), reader)
	if err := db.InTenant(ctx, f.db.App, reader.TenantID, func(tx pgx.Tx) error {
		var count int
		var text string
		if err := tx.QueryRow(ctx, `SELECT count(*), min(text) FROM harness_current_activity WHERE session_id=$1`, id).Scan(&count, &text); err != nil {
			return err
		}
		if count != 1 || text != "Implementing activity" {
			t.Fatalf("visible activity changed by denied writes: count=%d text=%q", count, text)
		}
		tag, err := tx.Exec(ctx, `UPDATE harness_current_activity SET text='Working' WHERE session_id=$1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			t.Fatal("project reader could not write visible activity")
		}
		_, err = tx.Exec(ctx, `INSERT INTO harness_current_activity(tenant_id,session_id,text,source,at)
			VALUES($1,$2,'Running Go tests','auto',clock_timestamp())`, reader.TenantID, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
