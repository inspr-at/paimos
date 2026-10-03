// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestLeavingScopeSelectionRetriesAndReplacement(t *testing.T) {
	f := fixture(t)
	register := func(host string) (string, string) {
		t.Helper()
		body := pauseRegistration(f)
		body["host"] = host
		w := f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", body, "")
		expect(t, w, 201)
		return decode(t, w)["id"].(string), body["worker_lease"].(string)
	}
	first, _ := register("host-a")
	second, _ := register("host-b")
	ended, endedLease := register("host-b")
	other, _ := register("host-a")
	path := func(id string) string { return "/api/projects/" + f.project + "/harness-sessions/" + id }
	expect(t, f.call(f.agent, "POST", path(ended)+"/stop", map[string]any{"reason": "stopped"}, endedLease), 200)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		id := uid()
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','other owner')`, f.person.TenantID, id); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, other, id)
		return err
	})
	deadline := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
	put := func(hosts any, agents []string) map[string]any {
		t.Helper()
		body := map[string]any{"deadline_at": deadline, "hosts": hosts}
		if agents != nil {
			body["agents"] = agents
		}
		w := f.call(f.person, "PUT", "/api/me/leaving-at", body, "")
		expect(t, w, 200)
		return decode(t, w)
	}
	// Agents determine current work; hosts are an independent stored policy.
	initial := put([]string{"host-b"}, []string{first, first, ended, other})
	if items := initial["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != first {
		t.Fatal("explicit selection included ended, foreign-owned or unselected work")
	}
	if ids := initial["agents"].([]any); len(ids) != 1 || ids[0] != first {
		t.Fatal("report exposed sessions outside the effective owned selection")
	}
	if put([]string{"host-b", "host-b"}, []string{other, ended, strings.ToUpper(first)})["request_id"] != initial["request_id"] {
		t.Fatal("equivalent normalized scope replaced controls")
	}
	w := f.call(f.person, "GET", "/api/me/leaving-at", nil, "")
	expect(t, w, 200)
	if hosts := decode(t, w)["hosts"].([]any); len(hosts) != 1 || hosts[0] != "host-b" {
		t.Fatal("reload lost the host policy")
	}
	// Changing only scope replaces pending requests, even at the same instant.
	replaced := put([]string{"host-a"}, []string{second})
	if replaced["request_id"] == initial["request_id"] || replaced["items"].([]any)[0].(map[string]any)["id"] != second {
		t.Fatal("changed scope was mistaken for a deadline retry")
	}
	w = f.call(f.person, "GET", path(first), nil, "")
	expect(t, w, 200)
	if decode(t, w)["pause"].(map[string]any)["state"] != "cancelled" {
		t.Fatal("replaced selection remained active")
	}
	// Host-only compatibility selects its running sessions, respecting ownership.
	hostOnly := put([]string{"host-a"}, nil)
	if items := hostOnly["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != first {
		t.Fatal("host-only selection escaped its host or person")
	}
	// Idle host policies and explicit empty selections are durable, not all.
	empty := put([]string{"idle-host"}, []string{})
	if len(empty["items"].([]any)) != 0 || len(empty["agents"].([]any)) != 0 {
		t.Fatal("empty agent selection paused running work")
	}
	all := put("all", nil)
	if all["hosts"] != "all" || len(all["items"].([]any)) != 2 {
		t.Fatal("all scope failed to select just owned running sessions")
	}
	for _, invalid := range []map[string]any{
		{"hosts": "this-computer"}, {"hosts": nil}, {"hosts": []string{"bad\nhost"}}, {"agents": []string{"not-a-session"}},
	} {
		invalid["deadline_at"] = deadline
		expect(t, f.call(f.person, "PUT", "/api/me/leaving-at", invalid, ""), 400)
	}
	w = f.call(f.person, "GET", "/api/me/leaving-at", nil, "")
	expect(t, w, 200)
	if decode(t, w)["request_id"] != all["request_id"] {
		t.Fatal("invalid scope changed the active request")
	}
}
