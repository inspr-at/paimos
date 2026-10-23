// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Risk: discovery or a cross-project send can escape its current owner/tenant,
// silently lose the sender's project, choose another generation, or claim a
// queued message was delivered. This runs under the registered session suite.
func testPeerDirectoryAndDelivery(t *testing.T) {
	w, m, sourceProject, srv := messagingWorld(t, true)
	owner := w.recipient
	w.sender.KeyCreatorID = owner.ID
	w.sender.Scopes = []string{"harness.read", "inbox.send", "inbox.read", "inbox.receipt"}
	w.agent.KeyCreatorID = owner.ID
	w.agent.Scopes = w.sender.Scopes
	project := peerTestProject(t, w, owner, "OPS")
	workspaceTx(t, w, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_build_object('project_key','AEON') WHERE id=$1`, sourceProject)
		return err
	})
	source := peerTestSession(t, w, sourceProject, w.sender, owner, "aeon-lead")
	target := peerTestSession(t, w, project, w.agent, owner, "ops-lead")
	foreignOwner := peerTestSession(t, w, project, w.agent, w.other, "ops-lead")
	unknownOwner := messageTestSession(t, w, project, w.agent, "unconfirmed")
	stale := peerTestSession(t, w, project, w.agent, owner, "stale")
	stopped := peerTestSession(t, w, project, w.agent, owner, "stopped")
	workspaceTx(t, w, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp()-interval '3 minutes' WHERE id=$1`, stale)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, stopped)
		return err
	})
	foreignProject := peerTestProject(t, w, w.outsider, "PMA")
	foreignAgent := insertPrincipal(t, w.db, w.outsider.TenantID, tenant.Agent, "foreign-peer", nil)
	foreignSession := peerTestSession(t, w, foreignProject, foreignAgent, w.outsider, "ops-lead")
	get := func(p tenant.Principal, query string) PeerPage {
		t.Helper()
		response := workspaceCall(m, p, http.MethodGet, "/api/peers"+query, "", nil)
		workspaceStatus(t, response, http.StatusOK)
		return mustJSON[PeerPage](t, response.Body.Bytes())
	}
	page := get(w.sender, "")
	if len(page.Items) != 2 || page.NextAfter != nil {
		t.Fatalf("owner directory: %+v", page)
	}
	byID := map[string]Peer{}
	for _, item := range page.Items {
		byID[item.SessionID] = item
	}
	peer := byID[target]
	if peer.Name != "ops-lead" || peer.ProjectID != project || peer.Project != "OPS" || peer.Harness != "codex" || peer.State != "working/idle" || peer.Address != "session:"+target || peer.PrincipalID != w.agent.ID || byID[source].Project != "AEON" {
		t.Fatalf("peer evidence: %+v", page)
	}
	for _, hidden := range []string{foreignOwner, unknownOwner, stale, stopped, foreignSession} {
		if found := get(w.sender, "?session="+hidden); len(found.Items) != 0 {
			t.Fatalf("hidden session discovered: %+v", found)
		}
	}
	if len(get(owner, "?name=ops-lead").Items) != 1 || len(get(w.other, "?name=ops-lead").Items) != 1 || len(get(w.outsider, "?name=ops-lead").Items) != 1 {
		t.Fatal("name directory did not remain within each owner and tenant")
	}
	first := get(w.sender, "?limit=1")
	if len(first.Items) != 1 || first.NextAfter == nil {
		t.Fatal("first keyset page is not bounded")
	}
	next := get(w.sender, "?limit=1&after="+*first.NextAfter)
	if len(next.Items) != 1 || next.NextAfter != nil || next.Items[0].SessionID == first.Items[0].SessionID {
		t.Fatal("keyset page duplicated or omitted a peer")
	}
	if len(get(w.sender, "?project="+project).Items) != 1 {
		t.Fatal("project filter lost its scope")
	}
	// Sharing an owner never grants project visibility or harness.read.
	restricted := insertPrincipal(t, w.db, owner.TenantID, tenant.Agent, "project-peer-reader", nil)
	restricted.KeyCreatorID, restricted.Scopes = owner.ID, []string{"harness.read"}
	if _, err := w.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, restricted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, owner.TenantID, restricted.ID, sourceProject); err != nil {
		t.Fatal(err)
	}
	if visible := get(restricted, ""); len(visible.Items) != 1 || visible.Items[0].SessionID != source {
		t.Fatalf("owner directory widened project access: %+v", visible)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?after=bad", "?session=", "?project=bad", "?name=", "?name=" + url.QueryEscape(strings.Repeat("x", 161))} {
		workspaceStatus(t, workspaceCall(m, w.sender, "GET", "/api/peers"+query, "", nil), 400)
	}
	workspaceStatus(t, workspaceCall(m, tenant.Principal{}, "GET", "/api/peers", "", nil), 401)
	for _, denied := range []tenant.Principal{
		{ID: w.sender.ID, TenantID: w.sender.TenantID, Kind: tenant.Agent, Scopes: w.sender.Scopes},
		{ID: w.sender.ID, TenantID: w.sender.TenantID, Kind: tenant.Agent, Scopes: w.sender.Scopes, KeyCreatorID: owner.ID, KeyPersonID: w.other.ID},
		{ID: w.sender.ID, TenantID: w.sender.TenantID, Kind: tenant.Agent, Scopes: w.sender.Scopes, KeyCreatorID: w.outsider.ID},
		{ID: w.sender.ID, TenantID: w.sender.TenantID, Kind: tenant.Agent, Scopes: []string{"inbox.send"}, KeyCreatorID: owner.ID},
	} {
		workspaceStatus(t, workspaceCall(m, denied, "GET", "/api/peers", "", nil), 403)
	}
	post := func(p tenant.Principal, recipient, key string, parent *string) *httptest.ResponseRecorder {
		t.Helper()
		payload := map[string]any{"body": "coordinate through the inbox", "idempotency_key": key, "sender_session_id": source, "expects_reply": true}
		if parent != nil {
			payload["reply_to"] = *parent
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return workspaceCall(m, p, "POST", "/api/peers/"+recipient+"/messages", string(raw), nil)
	}
	response := post(w.sender, target, "peer-cross-project", nil)
	workspaceStatus(t, response, 201)
	sent := mustJSON[CompatMessage](t, response.Body.Bytes())
	if sent.RecipientSessionID == nil || *sent.RecipientSessionID != target || sent.SenderSessionID == nil || *sent.SenderSessionID != source || sent.SenderProjectID == nil || *sent.SenderProjectID != sourceProject || sent.SenderProject != "AEON" || sent.SenderLabel != "aeon-lead (AEON)" || sent.DeliveryTarget != nil || sent.Status != "accepted" {
		t.Fatalf("cross-project attribution: %+v", sent)
	}
	if countSQL(t, w.db.App, owner.TenantID, `SELECT count(*) FROM inbox_compat_messages WHERE id=$1 AND project_id=$2`, sent.ID, project) != 1 || countSQL(t, w.db.App, owner.TenantID, `SELECT count(*) FROM events WHERE type='inbox.compat_sent' AND after->>'id'=$1 AND after->>'sender_project_id'=$2`, sent.ID, sourceProject) != 1 {
		t.Fatal("message or sender project not recorded in recipient project")
	}
	replayed := post(w.sender, target, "peer-cross-project", nil)
	workspaceStatus(t, replayed, 201)
	if mustJSON[CompatMessage](t, replayed.Body.Bytes()).ID != sent.ID {
		t.Fatal("peer retry created a new message")
	}
	if receipt := mustReceipt(t, srv, w.sender.ID, sent.ID); receipt.State != "queued" || receipt.HandedOffAt != nil {
		t.Fatalf("acceptance claimed delivery: %+v", receipt)
	}
	// Raw inbox pickup reaches only the exact generation; its frozen label
	// carries the source project even for clients using the older message shape.
	status, raw := do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+target, "", nil)
	picked := mustJSON[Page](t, raw)
	if status != 200 || len(picked.Items) != 1 || picked.Items[0].ID != sent.ID || picked.Items[0].SenderLabel != "aeon-lead (AEON)" {
		t.Fatalf("exact inbox pickup %d %s", status, raw)
	}
	status, raw = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+sent.ID+"/ack", "", nil)
	if status != 200 || mustReceipt(t, srv, w.sender.ID, sent.ID).State != "handed_off" {
		t.Fatalf("receipt did not confirm ack %d %s", status, raw)
	}
	// A counterpart reply crosses back to the sender's project while retaining
	// the thread and settling the original durable reply obligation.
	answer := compatInput(w.sender.ID, "peer-answer")
	answer.peer, answer.RecipientSessionID, answer.SenderSessionID, answer.ReplyTo = true, &source, &target, &sent.ID
	answered := mustCompatSend(t, m, w.agent, sourceProject, answer)
	if answered.ThreadID != sent.ThreadID || answered.Hop != 2 || answered.SenderProjectID == nil || *answered.SenderProjectID != project || answered.SenderProject != "OPS" || countSQL(t, w.db.App, owner.TenantID, `SELECT count(*) FROM inbox_reply_obligations WHERE message_id=$1 AND reply_message_id=$2 AND closed_at IS NOT NULL`, sent.ID, answered.ID) != 1 {
		t.Fatalf("cross-project reply: %+v", answered)
	}
	for _, recipient := range []string{foreignOwner, foreignSession, unknownOwner, stale, stopped} {
		workspaceStatus(t, post(w.sender, recipient, "denied-"+recipient, nil), 404)
	}
	badSource := answer
	badSource.To, badSource.RecipientSessionID, badSource.SenderSessionID = w.agent.ID, &target, &target
	if _, err := m.commitMessage(t.Context(), w.sender, project, badSource); err != errNotFound {
		t.Fatalf("another principal's sender session accepted: %v", err)
	}
	// Workstation keys can own several sessions under the same principal.
	// Delivery is still exact-session; it must not become a principal broadcast.
	sibling := peerTestSession(t, w, project, w.sender, owner, "same-principal-peer")
	siblingResponse := post(w.sender, sibling, "same-principal", nil)
	workspaceStatus(t, siblingResponse, 201)
	siblingMessage := mustJSON[CompatMessage](t, siblingResponse.Body.Bytes())
	// The constraint adjustment must preserve tenant FKs and the ban on
	// unbound or same-generation principal self-messages, even below the API.
	for _, probe := range []struct {
		sql, id, value, code string
	}{
		{`UPDATE inbox_compat_messages SET sender_session_id=$2 WHERE id=$1`, sent.ID, foreignSession, "23503"},
		{`UPDATE inbox_messages SET sender_session_id=recipient_session_id WHERE id=$1 AND $2::text IS NOT NULL`, siblingMessage.ID, "bound", "23514"},
		{`UPDATE inbox_compat_messages SET sender_session_id=NULL WHERE id=$1 AND $2::text IS NOT NULL`, siblingMessage.ID, "unbound", "23514"},
	} {
		err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, owner.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), probe.sql, probe.id, probe.value)
			return err
		})
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != probe.code {
			t.Fatalf("constraint failed for the wrong reason: got %v, want SQLSTATE %s", err, probe.code)
		}
	}

	t.Run("final ownership fence", func(t *testing.T) { testPeerFinalFence(t, w, m, w.sender, target, source, w.other.ID, false) })
	t.Run("final permission fence", func(t *testing.T) { testPeerFinalFence(t, w, m, w.sender, sibling, source, w.other.ID, true) })
}

func peerTestProject(t *testing.T, w *world, p tenant.Principal, key string) string {
	t.Helper()
	var id string
	workspaceTx(t, w, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,$2,$2,jsonb_build_object('project_key',$3::text) FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, p.TenantID, key+"-1", key).Scan(&id)
	})
	return id
}

func peerTestSession(t *testing.T, w *world, project string, agent, owner tenant.Principal, name string) string {
	t.Helper()
	id := messageTestSession(t, w, project, agent, name)
	workspaceTx(t, w, agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2,heartbeat_at=clock_timestamp(),phase='working',activity='idle' WHERE id=$1`, id, owner.ID)
		return err
	})
	return id
}

func testPeerFinalFence(t *testing.T, w *world, m *messaging, sender tenant.Principal, target, source, nextOwner string, revoke bool) {
	var role string
	if revoke {
		// Built-in grants are assembled from the registry, so use an explicit
		// custom role whose single send permission can actually be revoked.
		workspaceTx(t, w, sender, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'peer_sender','Peer sender') RETURNING id::text`, sender.TenantID).Scan(&role); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,p FROM unnest(ARRAY['nodes.read','harness.read','inbox.send']) p`, sender.TenantID, role); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, sender.ID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, sender.TenantID, sender.ID, role)
			return err
		})
	}
	// Pause after directory resolution, before the final mutation transaction.
	pool, barrier, ctx := dbtest.BarrierPool(t, w.db.App, func(sql string) bool {
		return strings.Contains(sql, "ORDER BY s.id LIMIT $8")
	})
	copyModule, copyBase := *m, *m.base
	copyBase.pool, copyModule.base = pool, &copyBase
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/peers/"+target+"/messages", strings.NewReader(fmt.Sprintf(`{"body":"must not persist","idempotency_key":"fenced-%t","sender_session_id":%q}`, revoke, source)))
		r.SetPathValue("sessionId", target)
		r = r.WithContext(tenant.WithPrincipal(ctx, sender))
		response := httptest.NewRecorder()
		copyModule.sendPeerMessage(response, r)
		finished <- response
	}()
	barrier.Wait(t, ctx)
	before := countSQL(t, w.db.App, sender.TenantID, `SELECT count(*) FROM inbox_compat_messages`)
	err := db.InTenant(dbtest.Seed(ctx), w.db.App, sender.TenantID, func(tx pgx.Tx) error {
		if err := attachedmsg.Lock(ctx, tx); err != nil {
			return err
		}
		if revoke {
			tag, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE permission='inbox.send' AND role_id=$1`, role)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 || authz.RequireTx(ctx, tx, sender, "harness.read", authz.Scope{}) != nil || authz.RequireTx(ctx, tx, sender, "inbox.send", authz.Scope{}) == nil {
				return fmt.Errorf("fixture did not revoke only the current send permission")
			}
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, target, nextOwner)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	guard, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response := dbtest.Await(t, guard, finished)
	want := 404
	if revoke {
		want = 403
	}
	workspaceStatus(t, response, want)
	if countSQL(t, w.db.App, sender.TenantID, `SELECT count(*) FROM inbox_compat_messages`) != before {
		t.Fatal("stale directory evidence authorized a message")
	}
}
