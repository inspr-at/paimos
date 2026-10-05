// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

func uid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type harnessFixture struct {
	db                     *dbtest.DB
	mux                    *http.ServeMux
	person, agent, foreign tenant.Principal
	project, ticket, key   string
}

func fixture(t *testing.T) *harnessFixture {
	t.Helper()
	return fixtureWithOwnershipClock(t, nil)
}

func fixtureWithOwnershipClock(t *testing.T, now func() time.Time) *harnessFixture {
	return fixtureWithKind(t, now, "work")
}

func fixtureWithKind(t *testing.T, now func() time.Time, kind string) *harnessFixture {
	t.Helper()
	f := &harnessFixture{db: dbtest.Open(t), mux: http.NewServeMux()}
	f.person = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	f.agent = tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent}
	f.foreign = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.person, f.foreign} {
		err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, p.TenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Harness Test')`, p.TenantID, "h-"+p.TenantID)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.person, f.agent, f.foreign} {
		f.tx(t, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,$4)`, p.TenantID, p.ID, p.Kind, "worker")
			return err
		})
	}
	// Handlers see project data only through a binding (ADR-003 P2).
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	dbtest.BindRole(t, f.db, f.person.TenantID, f.agent.ID, "member")
	f.project, f.ticket = uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var projectKind, ticketKind string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug='project'`).Scan(&projectKind); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug=$1`, kind).Scan(&ticketKind); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) VALUES($1,$2,'HTS-1',$3,'Harness project')`, f.person.TenantID, f.project, projectKind); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) VALUES($1,$2,'HTS-2',$3,'Harness ticket',$4)`, f.person.TenantID, f.ticket, ticketKind, f.project)
		return err
	})
	secret := uid()
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(uid(), "-", "")
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'harness-test',$3,$4,$5)`, f.agent.TenantID, f.agent.ID, prefix, hex.EncodeToString(sum[:]), []string{"harness.read", "harness.write", "harness.worker", "harness.control"})
		return err
	})
	f.key = "aeon_" + prefix + "_" + secret
	// Registration needs worker authority; keep read authority absent so the
	// live-list privacy fixtures still exercise a worker-only caller.
	f.agent.Scopes = []string{"harness.worker"}
	if now == nil {
		harness.New(f.db.App, nodes.CapturePlanningStart).Mount(f.mux)
	} else {
		harness.NewWithOwnershipClock(f.db.App, now).Mount(f.mux)
	}
	return f
}
func (f *harnessFixture) tx(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func (f *harnessFixture) call(p tenant.Principal, method, path string, body any, lease string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(context.Background(), p))
	if p.Kind == tenant.Agent {
		r.Header.Set("Authorization", "Bearer "+f.key)
	}
	if lease != "" {
		r.Header.Set("X-Aeon-Worker-Lease", lease)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %d %s: %v", w.Code, w.Body.String(), err)
	}
	return result
}
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func TestHarnessGenerationAndOwnedControls(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "generation-lease-000000000000000000000001"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host", "harness_session_ref": "vendor-ref-00000000000000001", "worker_lease": lease, "management_mode": "managed", "role": "coordinator", "ticket_node_id": f.ticket, "work_shape": "scout", "advertised_capabilities": []string{"inbox", "status", "interrupt", "stop"}}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	s := decode(t, w)
	id := s["id"].(string)
	if strings.Contains(w.Body.String(), lease) || strings.Contains(w.Body.String(), "vendor-ref") {
		t.Fatal("registration exposed private proof")
	}
	w = f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != id {
		t.Fatal("exact replay created a new generation")
	}
	path := base + "/" + id
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, "wrong-generation-lease-0000000000"), 403)
	w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease)
	expect(t, w, 200)
	w = f.call(f.person, "GET", base+"/orchestrator", nil, "")
	expect(t, w, 200)
	if decode(t, w)["state"] != "resolved" {
		t.Fatal(w.Body.String())
	}
	expect(t, f.call(f.agent, "POST", path+"/controls/interrupt", map[string]any{}, ""), 403)
	w = f.call(f.person, "POST", path+"/controls/interrupt", map[string]any{}, "")
	expect(t, w, 201)
	controlID := decode(t, w)["id"].(string)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	controls := decode(t, w)["controls"].([]any)
	if len(controls) != 1 || controls[0].(map[string]any)["id"] != controlID {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.agent, "POST", path+"/controls/"+controlID+"/complete", map[string]any{"outcome": "applied", "reason": "applied"}, lease)
	expect(t, w, 200)
	expect(t, f.call(f.agent, "POST", path+"/controls/"+controlID+"/complete", map[string]any{"outcome": "rejected", "reason": "failed"}, lease), 409)
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	w = f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "stopped"}, lease)
	expect(t, w, 200)
	if decode(t, w)["phase"] != "stopped" {
		t.Fatal(w.Body.String())
	}
	expect(t, f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease), 403)
	w = f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	if decode(t, w)["id"] == id {
		t.Fatal("stopped generation reused public id")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'harness.%'`).Scan(&n)
		if err == nil && n < 6 {
			t.Errorf("only %d harness events", n)
		}
		return err
	})
}

func TestHarnessInboxLeaseAndExactAck(t *testing.T) {
	for _, outcome := range []string{"", "failed"} {
		for _, targeted := range []bool{false, true} {
			t.Run(fmt.Sprintf("outcome=%s/targeted=%t", outcome, targeted), func(t *testing.T) { testHarnessInboxSettlement(t, outcome, targeted) })
		}
	}
}
func testHarnessInboxSettlement(t *testing.T, outcome string, targeted bool) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "generation-lease-000000000000000000000002"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host", "harness_session_ref": "vendor-ref-00000000000000002", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox"}}
	w := f.call(f.person, "POST", base, registration, "")
	expect(t, w, 201)
	sessionID := decode(t, w)["id"].(string)
	path := base + "/" + sessionID
	var recipientSession any
	if targeted {
		recipientSession = sessionID
	}
	messageID := uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		e, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "inbox.sent", After: map[string]any{"id": messageID}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id) VALUES($1,$2,$3,$4,$5,'first message',$6,$7)`, f.person.TenantID, messageID, f.person.ID, f.agent.ID, e.ID, uid(), recipientSession)
		return err
	})
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	var deliveries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &deliveries); err != nil || len(deliveries) != 1 {
		t.Fatalf("drain %s: %v", w.Body.String(), err)
	}
	d := deliveries[0]
	if d["body"] != "first message" || d["message_id"] != messageID {
		t.Fatal(d)
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	var replay []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil || len(replay) != 1 || replay[0]["delivery_id"] != d["delivery_id"] {
		t.Fatalf("lease replay %s: %v", w.Body.String(), err)
	}
	body := map[string]any{"delivery_id": d["delivery_id"], "cursor": d["cursor"], "effective_level": "simple"}
	if outcome == "failed" {
		body["outcome"], body["failure_reason"] = "failed", "outcome_unconfirmed"
	}
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", map[string]any{"delivery_id": d["delivery_id"], "cursor": d["cursor"], "outcome": "failed"}, lease), 400)
	expect(t, f.call(f.foreign, "POST", path+"/complete-delivery", body, lease), 403)
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", map[string]any{"delivery_id": d["delivery_id"], "cursor": 999, "effective_level": "simple"}, lease), 409)
	w = f.call(f.agent, "POST", path+"/complete-delivery", body, lease)
	expect(t, w, 200)
	expect(t, f.call(f.agent, "POST", path+"/complete-delivery", body, lease), 200)
	if outcome == "failed" {
		// A conflicting late success cannot upgrade uncertainty to handoff.
		delete(body, "outcome")
		delete(body, "failure_reason")
		expect(t, f.call(f.agent, "POST", path+"/complete-delivery", body, lease), 200)
		f.tx(t, f.agent, func(tx pgx.Tx) error {
			if !targeted {
				var count int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_receipts WHERE message_id=$1`, messageID).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Fatal("session failure created an unbound receipt")
				}
				return nil
			}
			var state, reason string
			if err := tx.QueryRow(t.Context(), `SELECT state,failure_reason FROM inbox_receipts WHERE message_id=$1`, messageID).Scan(&state, &reason); err != nil {
				return err
			}
			if state != "failed" || reason != "outcome_unconfirmed" {
				t.Fatalf("lost failed receipt: %s %s", state, reason)
			}
			return nil
		})
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
}

func TestHarnessBindingRevisionAndHierarchy(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	register := func(ref string, parent *string) string {
		body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host", "harness_session_ref": ref, "worker_lease": "generation-lease-000000000000000000000003", "management_mode": "managed", "role": "worker", "parent_harness_session_id": parent, "advertised_capabilities": []string{"status"}}
		w := f.call(f.person, "POST", base, body, "")
		expect(t, w, 201)
		return decode(t, w)["id"].(string)
	}
	parent := register("vendor-ref-00000000000000003", nil)
	child := register("vendor-ref-00000000000000004", &parent)
	bind := func(session string, revision int, parentID *string) *httptest.ResponseRecorder {
		return f.call(f.person, "PATCH", base+"/"+session+"/binding", map[string]any{"expected_revision": revision, "parent_harness_session_id": parentID, "ticket_node_id": f.ticket, "work_shape": "ship"}, "")
	}
	w := bind(child, 1, &parent)
	expect(t, w, 200)
	if decode(t, w)["revision"] != float64(2) {
		t.Fatal(w.Body.String())
	}
	expect(t, bind(child, 1, &parent), 409)
	expect(t, bind(parent, 1, &child), 409)
	w = bind(parent, 1, nil)
	expect(t, w, 200)
	if decode(t, w)["work_shape"] != "ship" {
		t.Fatal(w.Body.String())
	}
	var n int
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.bound'`).Scan(&n)
	})
	if n != 2 {
		t.Fatalf("binding events = %d, want 2", n)
	}
}

func TestHarnessPluginConstructor(t *testing.T) {
	if _, err := plugins.Builtin(harness.Plugin); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalRegistrationAndHeartbeatWithoutRequestContract(t *testing.T) {
	f := fixture(t) // All migrations, including the widened CHECKs, have run.
	base := "/api/projects/" + f.project + "/harness-sessions"
	call := func(path string, body any, lease string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
		r.Header.Set("Authorization", "Bearer "+f.key)
		if lease != "" {
			r.Header.Set("X-Aeon-Worker-Lease", lease)
		}
		if _, present := r.Header["Aeon-Contract"]; present {
			t.Fatal("historical request unexpectedly declares a contract")
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	for _, name := range []string{"codex", "claude", "pi", "cursor", "grok"} {
		t.Run(name, func(t *testing.T) {
			lease := "historical-lease-0000000000000000000-" + name
			w := call(base, map[string]any{"agent_principal_id": f.agent.ID, "harness": name, "host": "historical-reporter", "harness_session_ref": "historical-vendor-session-" + name, "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"status"}}, "")
			expect(t, w, http.StatusCreated)
			id := decode(t, w)["id"].(string)
			w = call(base+"/"+id+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease)
			expect(t, w, http.StatusOK)
			if got := decode(t, w); got["harness"] != name || got["phase"] != "working" {
				t.Fatal("historical heartbeat changed:", got)
			}
		})
	}
}
