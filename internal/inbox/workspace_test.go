// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func workspaceCall(m *messaging, p tenant.Principal, method, path, body string, headers http.Header) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	for k, vs := range headers {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func workspaceStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
}
func workspaceTx(t *testing.T, w *world, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func TestProjectMessagesPagingThreadAndAddress(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	empty := workspaceCall(m, w.admin, "GET", "/api/projects/"+project+"/messages?pending=true&limit=100&newest_first=true", "", nil)
	workspaceStatus(t, empty, 200)
	if body := strings.TrimSpace(empty.Body.String()); !strings.Contains(body, `"items":[]`) || strings.Contains(body, `"items":null`) {
		t.Fatalf("empty messages %s", empty.Body.String())
	}
	root := mustCompatSend(t, m, w.sender, project, compatInput(w.recipient.ID, "root"))
	reply := compatInput(w.sender.ID, "reply")
	reply.ReplyTo = &root.ID
	child := mustCompatSend(t, m, w.recipient, project, reply)
	reply = compatInput(w.recipient.ID, "grandchild")
	reply.ReplyTo = &child.ID
	grandchild := mustCompatSend(t, m, w.sender, project, reply)
	other := mustCompatSend(t, m, w.sender, project, compatInput(w.recipient.ID, "other"))
	base := "/api/projects/" + project + "/messages"
	get := func(path string) compatPage {
		response := workspaceCall(m, w.admin, "GET", path, "", nil)
		workspaceStatus(t, response, 200)
		var p compatPage
		if err := json.Unmarshal(response.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		for _, item := range p.Items {
			if item.CreatedAt.IsZero() {
				t.Fatal("created_at missing")
			}
		}
		return p
	}
	page := get(base + "?limit=200")
	if len(page.Items) != 4 || page.Items[0].ID != root.ID {
		t.Fatal(page)
	}
	page = get(base + "?newest_first=true&limit=2")
	if len(page.Items) != 2 || page.Items[0].ID != other.ID || page.Items[1].ID != grandchild.ID {
		t.Fatal(page)
	}
	next := get(fmt.Sprintf("%s?newest_first=true&limit=2&after=%d", base, page.NextAfter))
	if len(next.Items) != 2 || next.Items[0].ID != child.ID || next.Items[1].ID != root.ID {
		t.Fatal(next)
	}
	next = get(fmt.Sprintf("%s?newest_first=true&after=%d", base, next.NextAfter))
	if len(next.Items) != 0 {
		t.Fatal("paging duplicated messages")
	}
	for _, id := range []string{root.ID, child.ID, grandchild.ID} {
		page = get(base + "?thread=" + id)
		if len(page.Items) != 3 || page.Items[0].ID != root.ID || page.Items[2].ID != grandchild.ID {
			t.Fatalf("thread %s: %+v", id, page)
		}
	}
	page = get(base + "?address=" + w.sender.ID)
	if len(page.Items) != 1 || page.Items[0].ID != child.ID {
		t.Fatal("address filter")
	}
	page = get(base + "?thread=" + child.ID + "&address=" + w.recipient.ID)
	if len(page.Items) != 2 {
		t.Fatal("intersected filters")
	}
	for _, q := range []string{"limit=201", "limit=0", "after=-1", "thread=bad", "newest_first=bad", "pending=bad", "address=bad", "address=" + w.sender.ID + "&to=" + w.recipient.ID} {
		workspaceStatus(t, workspaceCall(m, w.admin, "GET", base+"?"+q, "", nil), 400)
	}
	workspaceStatus(t, workspaceCall(m, w.sender, "GET", base, "", nil), 403)
	workspaceStatus(t, workspaceCall(m, tenant.Principal{}, "GET", base, "", nil), 401)
	foreign := insertPrincipal(t, w.db, w.outsider.TenantID, tenant.Person, "foreign-admin", []string{"super_admin"})
	workspaceStatus(t, workspaceCall(m, foreign, "GET", base, "", nil), 404)
	workspaceStatus(t, workspaceCall(m, w.recipient, "GET", base+"/listen?limit=11", "", nil), 400)
	page = get(base + "?thread=" + w.outsider.ID)
	if len(page.Items) != 0 {
		t.Fatal("unknown thread returned messages")
	}
}

func TestHeldResolutionAuthorizationReplayAndPending(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	in := compatInput(w.recipient.ID, "held")
	in.ActionRequest = true
	in.ExpectsReply = true
	held := mustCompatSend(t, m, w.sender, project, in)
	plain := mustCompatSend(t, m, w.sender, project, compatInput(w.recipient.ID, "plain"))
	base := "/api/projects/" + project + "/messages"
	path := base + "/" + held.ID + "/resolution"
	body := `{"decision":"resolved","note":"handled privately by a person"}`
	admin := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Person, "super", []string{"super_admin"})
	foreign := insertPrincipal(t, w.db, w.outsider.TenantID, tenant.Person, "foreign-super", []string{"super_admin"})
	agent := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Agent, "agent-admin", []string{"admin", "super_admin"})
	for _, p := range []tenant.Principal{w.sender, agent} {
		workspaceStatus(t, workspaceCall(m, p, "POST", path, body, nil), 403)
	}
	workspaceStatus(t, workspaceCall(m, tenant.Principal{}, "POST", path, body, nil), 401)
	workspaceStatus(t, workspaceCall(m, foreign, "POST", path, body, nil), 404)
	for _, h := range []http.Header{{"Authorization": {"Bearer fixture-key"}}, {"X-Paimos-Agent-Name": {"worker"}}, {"X-Aeon-Agent-Name": {"worker"}}} {
		workspaceStatus(t, workspaceCall(m, admin, "POST", path, body, h), 403)
	}
	workspaceStatus(t, workspaceCall(m, admin, "POST", base+"/"+plain.ID+"/resolution", body, nil), 409)
	for _, invalid := range []string{`{"decision":"approve"}`, `{"decision":"resolved","unknown":1}`, `{"decision":"resolved","note":"\u0000"}`} {
		workspaceStatus(t, workspaceCall(m, admin, "POST", path, invalid, nil), 400)
	}
	pending := func() compatPage {
		response := workspaceCall(m, admin, "GET", base+"?pending=true", "", nil)
		workspaceStatus(t, response, 200)
		var p compatPage
		if err := json.Unmarshal(response.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if len(pending().Items) != 1 {
		t.Fatal("held missing from pending")
	}
	// Concurrent exact retries must commit just one event and the first timestamp.
	results := make(chan *httptest.ResponseRecorder, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- workspaceCall(m, admin, "POST", path, body, nil) }()
	}
	wg.Wait()
	close(results)
	var first string
	for result := range results {
		workspaceStatus(t, result, 200)
		if first == "" {
			first = result.Body.String()
		} else if first != result.Body.String() {
			t.Fatal("divergent exact replay")
		}
	}
	if len(pending().Items) != 0 {
		t.Fatal("resolved request remains pending")
	}
	workspaceStatus(t, workspaceCall(m, admin, "POST", path, `{"decision":"dismissed","note":"handled privately by a person"}`, nil), 409)
	workspaceStatus(t, workspaceCall(m, admin, "POST", path, `{"decision":"resolved","note":"different"}`, nil), 409)
	response := workspaceCall(m, admin, "GET", base, "", nil)
	workspaceStatus(t, response, 200)
	var page compatPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Status != "held" || page.Items[0].HumanResolutionOutcome == nil || *page.Items[0].HumanResolutionOutcome != "resolved" || page.Items[0].ReplyObligation != "open" {
		t.Fatal("held history or obligation mutated")
	}
	workspaceTx(t, w, admin, func(tx pgx.Tx) error {
		var count int
		var content, actor string
		if err := tx.QueryRow(t.Context(), `SELECT count(*),min(after::text),min(actor_principal_id::text) FROM events WHERE type=$1`, resolutionEvent).Scan(&count, &content, &actor); err != nil {
			return err
		}
		if count != 1 || strings.Contains(content, "handled privately") || actor != admin.ID {
			t.Fatal("resolution audit wrong")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE id=$1::uuid`, held.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("held message released")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_message_deliveries WHERE message_id=$1::uuid AND state='held'`, held.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("held delivery changed")
		}
		return nil
	})
	// Both admin roles can inspect and operate message targets.
	for _, p := range []tenant.Principal{admin, w.admin} {
		workspaceStatus(t, workspaceCall(m, p, "GET", base, "", nil), 200)
		workspaceStatus(t, workspaceCall(m, p, "GET", "/api/projects/"+project+"/message-targets", "", nil), 200)
	}
	dismissed := in
	dismissed.Key = "dismissed"
	msg := mustCompatSend(t, m, w.sender, project, dismissed)
	workspaceStatus(t, workspaceCall(m, w.admin, "POST", base+"/"+msg.ID+"/resolution", `{"decision":"dismissed"}`, nil), 200)
	if len(pending().Items) != 0 {
		t.Fatal("dismissed request remains pending")
	}
}

func TestHeldResolutionEventFailureLeavesPending(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	in := compatInput(w.recipient.ID, "held-failure")
	in.ActionRequest = true
	held := mustCompatSend(t, m, w.sender, project, in)
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.Admin, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE FUNCTION reject_b7_resolution() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='inbox.action_resolved' THEN RAISE EXCEPTION 'test event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_b7_resolution BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_b7_resolution()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + project + "/messages"
	workspaceStatus(t, workspaceCall(m, w.admin, "POST", base+"/"+held.ID+"/resolution", `{"decision":"resolved"}`, nil), 500)
	response := workspaceCall(m, w.admin, "GET", base+"?pending=true", "", nil)
	workspaceStatus(t, response, 200)
	var page compatPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].HumanResolutionOutcome != nil {
		t.Fatal("failed event changed pending")
	}
}
