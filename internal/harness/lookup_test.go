// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestHarnessSessionLookup(t *testing.T) {
	f := fixture(t)
	id := attentionSession(t, f)
	const note = "lookup must not echo this note"
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_activity_notes(tenant_id, session_id, note) VALUES ($1,$2,$3)`, f.person.TenantID, id, note)
		return err
	})
	path := "/api/projects/" + f.project + "/harness-sessions/" + id + "/lookup"

	held := make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	t.Cleanup(release)
	locked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `SELECT id FROM harness_sessions WHERE id=$1 FOR UPDATE`, id); err != nil {
				return err
			}
			close(locked)
			<-held
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("lock holder: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("could not lock the session row")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(tenant.WithPrincipal(ctx, f.person))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	release()
	if err := <-done; err != nil {
		t.Fatalf("lock holder: %v", err)
	}
	expect(t, rec, 200)
	if strings.Contains(rec.Body.String(), note) || strings.Contains(rec.Body.String(), "activity_history") || strings.Contains(rec.Body.String(), "controls") {
		t.Fatalf("lookup included session detail: %s", rec.Body.String())
	}
	body := decode(t, rec)
	if len(body) != 5 || body["id"] != id || body["project_id"] != f.project || body["agent_principal_id"] != f.agent.ID || body["stopped_at"] != nil || body["archived_at"] != nil {
		t.Fatalf("lookup: %#v", body)
	}

	detail := f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+id, nil, "")
	expect(t, detail, 200)
	if !strings.Contains(detail.Body.String(), note) {
		t.Fatal("session detail dropped activity history")
	}

	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	expect(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/"+uid()+"/lookup", nil, ""), 404)
	expect(t, f.call(f.person, "GET", "/api/projects/"+uid()+"/harness-sessions/"+id+"/lookup", nil, ""), 404)
	expect(t, f.call(f.person, "GET", "/api/projects/"+f.project+"/harness-sessions/not-a-uuid/lookup", nil, ""), 400)

	quiet := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent}
	secret := uid()
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(uid(), "-", "")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','quiet')`, quiet.TenantID, quiet.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'quiet',$3,$4,$5)`, quiet.TenantID, quiet.ID, prefix, hex.EncodeToString(sum[:]), []string{"harness.write"})
		return err
	})
	denied := httptest.NewRequest(http.MethodGet, path, nil).WithContext(tenant.WithPrincipal(context.Background(), quiet))
	denied.Header.Set("Authorization", "Bearer aeon_"+prefix+"_"+secret)
	deniedRec := httptest.NewRecorder()
	f.mux.ServeHTTP(deniedRec, denied)
	expect(t, deniedRec, 403)

	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now(),archived_at=now(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`, id)
		return err
	})
	endedRec := f.call(f.person, "GET", path, nil, "")
	expect(t, endedRec, 200)
	ended := decode(t, endedRec)
	if ended["stopped_at"] == nil || ended["archived_at"] == nil || ended["agent_principal_id"] != f.agent.ID {
		t.Fatalf("ended lookup: %#v", ended)
	}
}
