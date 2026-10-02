// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAccountEventHistoryAndSSEUseCurrentOwnerSharing(t *testing.T) {
	d, agent, _ := fixture(t)
	owner := tenant.Principal{TenantID: agent.TenantID, Kind: tenant.Person}
	peer := owner
	var id string
	var event Event
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, agent.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Owner',ARRAY['admin']) RETURNING id::text`, agent.TenantID).Scan(&owner.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Teammate admin',ARRAY['admin']) RETURNING id::text`, agent.TenantID).Scan(&peer.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id,linked_at) VALUES($1,'local','codex','daemon',$2,'Main',$3,now()) RETURNING id::text`, agent.TenantID, agent.ID, owner.ID).Scan(&id); err != nil {
			return err
		}
		var err error
		event, err = Append(t.Context(), tx, agent, Change{Type: "account.probed", After: map[string]any{"account_id": id, "state": "available", "openrouter_credits": map[string]any{"remaining": 17}, "windows": []any{map[string]any{"used_percent": 41, "resets_at": "2026-10-03T12:00:00Z"}}, "probe_failure": "private-code"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, peer.TenantID, peer.ID)
	dbtest.BindLegacy(t, d, owner.TenantID, owner.ID)
	m := New(d.App).(*module)
	page, err := m.read(t.Context(), peer, "", event.ID-1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || strings.Contains(string(page.Items[0].After), "remaining") || strings.Contains(string(page.Items[0].After), "used_percent") || !strings.Contains(string(page.Items[0].After), "available") {
		t.Fatalf("history privacy: %+v", page.Items)
	}
	own, err := m.read(t.Context(), owner, "", event.ID-1, 5)
	if err != nil || len(own.Items) != 1 || !strings.Contains(string(own.Items[0].After), `"remaining":17`) {
		t.Fatalf("owner history: %+v %v", own.Items, err)
	}
	// The SSE server exercises the real durable replay path, not a separate
	// mock serializer. Deadline guards a hang; no sleep establishes ordering.
	mux := http.NewServeMux()
	m.Mount(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), peer)))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream?after="+strconv.FormatInt(event.ID-1, 10), nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("SSE status %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	received := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) != nil || e.Type != "account.probed" {
			continue
		}
		if strings.Contains(string(e.After), "remaining") || strings.Contains(string(e.After), "private-code") {
			t.Fatalf("SSE quota leak: %s", line)
		}
		received = true
		break
	}
	res.Body.Close()
	if !received {
		t.Fatalf("SSE did not replay account event: %v", scanner.Err())
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	page, err = m.read(t.Context(), peer, "", event.ID-1, 5)
	if err != nil || !strings.Contains(string(page.Items[0].After), `"remaining":17`) {
		t.Fatalf("sharing not applied on replay: %+v %v", page.Items, err)
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	page, err = m.read(t.Context(), peer, "", event.ID-1, 5)
	if err != nil || strings.Contains(string(page.Items[0].After), "remaining") {
		t.Fatalf("withdrawal not applied on replay: %+v %v", page.Items, err)
	}
	var original []byte
	if err := d.Admin.QueryRow(t.Context(), `SELECT after FROM events WHERE tenant_id=$1 AND id=$2`, agent.TenantID, event.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var persisted, initial any
	if json.Unmarshal(original, &persisted) != nil || json.Unmarshal(event.After, &initial) != nil || !reflect.DeepEqual(persisted, initial) {
		t.Fatalf("durable audit was mutated: %s", original)
	}
}
