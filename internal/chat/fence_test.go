// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (f *fixture) chatMessage(t *testing.T, thread Thread, session string) (string, int64) {
	t.Helper()
	var event int64
	id := uid()
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,'chat.message_sent',jsonb_build_object('conversation_id',$4::text,'message_id',$5::text)) RETURNING id`, f.alice.TenantID, f.alice.ID, f.project, thread.ID, id).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,recipient_session_id,sent_event_id,body,idempotency_key,chat_thread_id) VALUES($1,$2,$3,$4,$5,$6,'private-chat-canary',$7,$8)`, f.alice.TenantID, id, f.alice.ID, f.agent.ID, session, event, "chat/"+id, thread.ID); err != nil {
		t.Fatal(err)
	}
	return id, event
}
func TestNewModeExcludedFromLegacyReadAckReplyStreamAndDrain(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "lead", "lead"))
	session, lease := f.session(t, f.alice, f.project, "coordinator", "unmanaged")
	f.bind(t, f.alice, thread, session, "0")
	id, event := f.chatMessage(t, thread, session)
	// Legacy sessions accept principal + ID without a lease. Neither that ID
	// nor a shared principal can open the new storage mode.
	for _, query := range []string{"", "?session=" + session, "?session=" + session + "&exact_session=true"} {
		w := f.call(f.agent, "GET", "/api/inbox/messages"+query, nil, "")
		expect(t, w, 200)
		var page inbox.Page
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 || page.NextAfter != 0 {
			t.Fatal("new mode leaked through legacy read/cursor")
		}
	}
	expect(t, f.call(f.agent, "POST", "/api/inbox/messages/"+id+"/ack", map[string]any{}, ""), 404)
	expect(t, f.call(f.alice, "GET", "/api/inbox/messages/"+id+"/receipt", nil, ""), 404)
	w := f.call(f.alice, "GET", "/api/inbox/message-status?ids="+id, nil, "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), id) {
		t.Fatal("new mode leaked through legacy status")
	}
	// The raw reply path is person-authenticated and cannot import a new-mode
	// parent into a legacy chain even when the person owns the conversation.
	expect(t, f.call(f.alice, "POST", "/api/inbox/messages", map[string]any{"recipient_principal_id": f.agent.ID, "body": "raw reply", "idempotency_key": "raw-parent", "reply_to_id": id}, ""), 404)
	legacy := decode[inbox.Message](t, f.call(f.alice, "POST", "/api/inbox/messages", map[string]any{"recipient_principal_id": f.agent.ID, "body": "legacy-visible-canary", "idempotency_key": "legacy-visible"}, ""))
	// A strict snapshot checks the complete unbound legacy response field set.
	payload := f.call(f.agent, "GET", "/api/inbox/messages", nil, "")
	expect(t, payload, 200)
	var raw struct {
		Items     []map[string]any `json:"items"`
		NextAfter int64            `json:"next_after"`
	}
	if err := json.Unmarshal(payload.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Items) != 1 || raw.Items[0]["id"] != legacy.ID || raw.NextAfter != legacy.SentEventID {
		t.Fatal("legacy read changed or mixed new rows")
	}
	keys := []string{}
	for key := range raw.Items[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"acked_at", "body", "created_at", "id", "idempotency_key", "recipient_principal_id", "sender_principal_id", "sent_event_id"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("strict legacy snapshot fields %v want %v", keys, want)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream := &stopStream{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, canary: "legacy-visible-canary"}
	request := httptest.NewRequest("GET", "/api/inbox/stream", nil).WithContext(tenant.WithPrincipal(ctx, f.agent))
	f.mux.ServeHTTP(stream, request)
	if !strings.Contains(stream.Body.String(), "legacy-visible-canary") || strings.Contains(stream.Body.String(), "private-chat-canary") || strings.Contains(stream.Body.String(), id) {
		t.Fatal("legacy stream leaked chat or lost legacy message")
	}
	// Managed drain is another legacy reader. A differently managed registration
	// sharing the service principal must leave the chat row untouched.
	managed, managedLease := f.session(t, f.alice, f.project, "coordinator", "managed")
	drain := f.call(f.agent, "POST", "/api/projects/"+f.project+"/harness-sessions/"+managed+"/drain", map[string]any{}, managedLease)
	expect(t, drain, 200)
	if strings.Contains(drain.Body.String(), id) || strings.Contains(drain.Body.String(), "private-chat-canary") {
		t.Fatal("managed drain imported chat")
	}
	var changed bool
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT acked_at IS NOT NULL OR fetched_at IS NOT NULL FROM inbox_messages WHERE id=$1`, id).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("legacy path changed chat delivery evidence")
	}
	// A qualified worker transaction receives exactly its own conversation;
	// entering a nested legacy transaction clears that verified authority.
	proof := WorkerBindingRequest{ConversationID: thread.ID, SessionID: session, BindingEpoch: "1"}
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Aeon-Worker-Lease", lease)
	req.Header.Set("Authorization", "Bearer "+f.key)
	pctx := tenant.WithPrincipal(t.Context(), f.agent)
	if err := db.InTransaction(pctx, f.d.App, func(pctx context.Context) error {
		return db.InTenant(pctx, f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
			if err := accessFence(pctx, tx, f.agent); err != nil {
				return err
			}
			if _, err := AuthorizeWorkerTx(pctx, tx, req, f.agent, proof); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(pctx, `SELECT count(*) FROM inbox_messages WHERE id=$1`, id).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Fatal("verified exact chat worker cannot read")
			}
			if err := db.InTenant(pctx, f.d.App, f.agent.TenantID, func(legacy pgx.Tx) error {
				if err := legacy.QueryRow(pctx, `SELECT count(*) FROM inbox_messages WHERE id=$1`, id).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Fatal("nested legacy transaction inherited chat authority")
				}
				return nil
			}); err != nil {
				return err
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	// Ordinary and service event readers cannot learn private event IDs/existence.
	for _, p := range []tenant.Principal{f.alice, f.admin, f.agent, f.bob, f.foreign} {
		if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE id=$1 AND type='chat.message_sent'`, event).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("general event reader leaks private chat")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inbox.NewSweeper(f.d.App).SweepTenant(t.Context(), f.alice.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT acked_at IS NOT NULL OR fetched_at IS NOT NULL FROM inbox_messages WHERE id=$1`, id).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("legacy sweeper changed chat")
	}
}

type stopStream struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	canary string
}

func (w *stopStream) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Contains(w.Body.String(), w.canary) {
		w.cancel()
	}
}

func TestRLSAndLegacyProjectionGuards(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "lead", "lead"))
	session, _ := f.session(t, f.alice, f.project, "coordinator", "unmanaged")
	id, _ := f.chatMessage(t, thread, session)
	for _, p := range []tenant.Principal{f.alice, f.bob, f.admin, f.agent, f.foreign} {
		if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE id=$1`, id).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("RLS allows legacy chat read")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTenant(db.AllProjects(t.Context()), f.d.App, f.alice.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE id=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("service visibility bypassed chat")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO inbox_receipts(tenant_id,message_id,state) VALUES($1,$2,'queued')`,
		`INSERT INTO harness_deliveries(tenant_id,session_id,message_id,cursor) VALUES($1,$3,$2,1)`,
	} {
		var err error
		if strings.Contains(query, "harness_deliveries") {
			_, err = f.d.Admin.Exec(t.Context(), query, f.alice.TenantID, id, session)
		} else {
			_, err = f.d.Admin.Exec(t.Context(), query, f.alice.TenantID, id)
		}
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "legacy transport cannot reference chat messages" {
			t.Fatalf("guard returned wrong failure: %v", err)
		}
	}
}
