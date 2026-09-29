// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func boundMessage(t *testing.T, f *harnessFixture, session, sender, recipient string) (string, int64) {
	t.Helper()
	id, inboxID := uid(), uid()
	var eventID int64
	f.tx(t, f.person, func(tx pgx.Tx) error {
		event, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "inbox.sent", NodeID: &f.project, After: map[string]any{"fixture": true}})
		if err != nil {
			return err
		}
		eventID = event.ID
		if _, err = tx.Exec(t.Context(), `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key)
			VALUES($1,$2::text::uuid,$3,$4,$5,'Synthetic private body',$2::text)`, f.person.TenantID, inboxID, sender, recipient, event.ID); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,recipient_address,body,key_digest,request_digest,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level,recipient_session_id)
			VALUES($1,$2::text::uuid,$3,$4,$5,'paimos:fixture','Synthetic private body',$2::text,$2::text,$6::text::uuid,$7,false,false,'simple',$8::text::uuid)`,
			f.person.TenantID, id, f.project, sender, recipient, inboxID, event.ID, session)
		return err
	})
	return id, eventID
}

func markerCount(t *testing.T, f *harnessFixture, p tenant.Principal) int {
	t.Helper()
	var n int
	err := db.InTenant(tenant.WithPrincipal(t.Context(), p), f.db.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM session_read_markers`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSessionReadMarkerIsolationAndMonotonic(t *testing.T) {
	f := fixture(t)
	session := attentionSession(t, f)
	otherSession := attentionSession(t, f)
	firstID, firstEvent := boundMessage(t, f, session, f.agent.ID, f.person.ID)
	secondID, secondEvent := boundMessage(t, f, session, f.agent.ID, f.person.ID)
	if secondEvent <= firstEvent {
		t.Fatalf("events not ordered: %d then %d", firstEvent, secondEvent)
	}
	otherID, otherEvent := boundMessage(t, f, otherSession, f.agent.ID, f.person.ID)
	other := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','other')`, other.TenantID, other.ID)
		return err
	})
	dbtest.BindRole(t, f.db, other.TenantID, other.ID, "admin")

	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/read-marker"
	unset := f.call(f.person, "GET", path, nil, "")
	expect(t, unset, 200)
	empty := decode(t, unset)
	if empty["session_id"] != session || empty["last_read_message_id"] != nil || empty["last_read_event_id"] != nil || empty["read_at"] != nil {
		t.Fatalf("unset marker: %#v", empty)
	}
	expect(t, f.call(f.agent, "GET", path, nil, ""), 403)
	expect(t, f.call(f.agent, "PUT", path, map[string]any{"last_read_message_id": firstID, "last_read_event_id": firstEvent}, ""), 403)

	put := f.call(f.person, "PUT", path, map[string]any{"last_read_message_id": firstID, "last_read_event_id": firstEvent}, "")
	expect(t, put, 200)
	stored := decode(t, put)
	if stored["last_read_message_id"] != firstID || stored["last_read_event_id"].(float64) != float64(firstEvent) || stored["read_at"] == nil {
		t.Fatalf("first marker: %#v", stored)
	}
	firstAt := stored["read_at"].(string)

	// A collapsed group may name its first message and a later event in the session.
	collapsed := f.call(f.person, "PUT", path, map[string]any{"last_read_message_id": firstID, "last_read_event_id": secondEvent}, "")
	expect(t, collapsed, 200)
	stored = decode(t, collapsed)
	if stored["last_read_message_id"] != secondID || stored["last_read_event_id"].(float64) != float64(secondEvent) {
		t.Fatalf("collapsed marker: %#v", stored)
	}
	aheadAt := stored["read_at"].(string)
	if aheadAt == firstAt {
		t.Fatal("forward update did not move read_at")
	}

	regression := f.call(f.person, "PUT", path, map[string]any{"last_read_message_id": firstID, "last_read_event_id": firstEvent}, "")
	expect(t, regression, 200)
	stored = decode(t, regression)
	if stored["last_read_message_id"] != secondID || stored["last_read_event_id"].(float64) != float64(secondEvent) || stored["read_at"] != aheadAt {
		t.Fatalf("regression moved the marker: %#v", stored)
	}
	again := decode(t, f.call(f.person, "GET", path, nil, ""))
	if again["last_read_event_id"].(float64) != float64(secondEvent) {
		t.Fatalf("get after regression: %#v", again)
	}

	theirs := decode(t, f.call(other, "GET", path, nil, ""))
	expect(t, f.call(other, "GET", path, nil, ""), 200)
	if theirs["last_read_message_id"] != nil || theirs["last_read_event_id"] != nil {
		t.Fatalf("other person saw the marker: %#v", theirs)
	}
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	expect(t, f.call(f.person, "PUT", path, map[string]any{"last_read_message_id": otherID, "last_read_event_id": otherEvent}, ""), 400)
	expect(t, f.call(f.person, "PUT", path, map[string]any{"last_read_message_id": firstID, "extra": 1}, ""), 400)
	expect(t, f.call(f.person, "PUT", "/api/projects/"+f.project+"/harness-sessions/"+uid()+"/read-marker", map[string]any{"last_read_message_id": firstID, "last_read_event_id": firstEvent}, ""), 404)

	if markerCount(t, f, f.person) != 1 || markerCount(t, f, other) != 0 || markerCount(t, f, f.agent) != 0 {
		t.Fatalf("counts person=%d other=%d agent=%d", markerCount(t, f, f.person), markerCount(t, f, other), markerCount(t, f, f.agent))
	}
	var system int
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM session_read_markers`).Scan(&system)
	}); err != nil {
		t.Fatal(err)
	}
	if system != 0 {
		t.Fatalf("service path saw %d markers", system)
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.db.App, f.agent.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO session_read_markers(tenant_id,person_id,session_id,last_read_message_id,last_read_event_id) VALUES($1,$2,$3,$4,$5)`,
			f.agent.TenantID, f.agent.ID, session, secondID, secondEvent)
		return err
	})
	if err == nil {
		t.Fatal("agent insert was accepted")
	}
	own := f.call(other, "PUT", path, map[string]any{"last_read_message_id": firstID, "last_read_event_id": firstEvent}, "")
	expect(t, own, 200)
	if markerCount(t, f, f.person) != 1 || markerCount(t, f, other) != 1 {
		t.Fatalf("after other person's mark person=%d other=%d", markerCount(t, f, f.person), markerCount(t, f, other))
	}
	if decode(t, f.call(f.person, "GET", path, nil, ""))["last_read_event_id"].(float64) != float64(secondEvent) {
		t.Fatal("other person's update changed the first marker")
	}
}
