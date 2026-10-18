// SPDX-License-Identifier: AGPL-3.0-only

package chat

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

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
)

func uid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type fixture struct {
	d                                      *dbtest.DB
	m                                      *Module
	mux                                    *http.ServeMux
	alice, bob, admin, agent, foreign      tenant.Principal
	project, secondProject, foreignProject string
	key                                    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), mux: http.NewServeMux()}
	tid, other := uid(), uid()
	for _, id := range []string{tid, other} {
		if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Chat fixture')`, id, "chat-"+id); err != nil {
			t.Fatal(err)
		}
	}
	f.alice = tenant.Principal{ID: uid(), TenantID: tid, Kind: tenant.Person}
	f.bob = tenant.Principal{ID: uid(), TenantID: tid, Kind: tenant.Person}
	f.admin = tenant.Principal{ID: uid(), TenantID: tid, Kind: tenant.Person}
	f.agent = tenant.Principal{ID: uid(), TenantID: tid, Kind: tenant.Agent, Scopes: []string{"chat.receive", "harness.worker", "inbox.send"}}
	f.foreign = tenant.Principal{ID: uid(), TenantID: other, Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.alice, f.bob, f.admin, f.agent, f.foreign} {
		if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,$4)`, p.TenantID, p.ID, p.Kind, "chat fixture"); err != nil {
			t.Fatal(err)
		}
		role := "member"
		if p.ID == f.admin.ID {
			role = "owner"
		}
		dbtest.BindRole(t, f.d, p.TenantID, p.ID, role)
	}
	prefix := strings.ReplaceAll(tid, "-", "") + "fixture"
	secret := uid()
	sum := sha256.Sum256([]byte(secret))
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,prefix,hash,scopes,name,created_by_principal_id) VALUES($1,$2,$3,$4,$5,'chat fixture',(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, tid, f.agent.ID, prefix, hex.EncodeToString(sum[:]), f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
	f.key = "aeon_" + prefix + "_" + secret
	f.project, f.secondProject, f.foreignProject = uid(), uid(), uid()
	for i, project := range []string{f.project, f.secondProject, f.foreignProject} {
		projectTenant := tid
		if i == 2 {
			projectTenant = other
		}
		if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,$3,id,'Chat project' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, projectTenant, project, fmt.Sprintf("CHAT-%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	f.m = New(f.d.App, Options{Enabled: true})
	f.m.Mount(f.mux)
	inbox.New(f.d.App).Mount(f.mux)
	harness.New(f.d.App).Mount(f.mux)
	return f
}
func (f *fixture) call(p tenant.Principal, method, path string, body any, lease string) *httptest.ResponseRecorder {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
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
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	expect(t, w, 200)
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *fixture) role(t *testing.T, p tenant.Principal, project, kind, slot string) Role {
	return decode[Role](t, f.call(p, "POST", "/api/projects/"+project+"/chat-roles", map[string]string{"kind": kind, "slot_key": slot}, ""))
}
func (f *fixture) thread(t *testing.T, p tenant.Principal, role Role) Thread {
	return decode[Thread](t, f.call(p, "POST", "/api/projects/"+role.ProjectID+"/chat-threads/resolve", map[string]string{"role_id": role.ID}, ""))
}
func (f *fixture) session(t *testing.T, owner tenant.Principal, project, role, management string) (string, string) {
	t.Helper()
	return f.sessionWith(t, f.agent.ID, owner, project, role, management, "inbox")
}

// sessionWith registers a live session for agent with the given capabilities.
func (f *fixture) sessionWith(t *testing.T, agent string, owner tenant.Principal, project, role, management string, caps ...string) (string, string) {
	t.Helper()
	id := uid()
	lease := "fixture-lease-" + uid()
	sum := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	ref := sha256.Sum256([]byte(id))
	_, err := f.d.Admin.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,capabilities,ref_digest,lease_digest,account_label,display_label,model)
 VALUES($1,$2,$3,$4,$5,'claude','private-host-fixture',$6,$7,$10,$8,$9,'private-account-fixture','private-label-fixture','private-model-fixture')`, owner.TenantID, id, project, agent, owner.ID, management, role, ref[:], sum[:], caps)
	if err != nil {
		t.Fatal(err)
	}
	return id, lease
}
func (f *fixture) bind(t *testing.T, p tenant.Principal, thread Thread, session, expected string) Thread {
	return decode[Thread](t, f.call(p, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": expected, "session_id": session}, ""))
}
func TestDurableIdentityAndPrivateRoleHandover(t *testing.T) {
	f := newFixture(t)
	role := f.role(t, f.alice, f.project, "lead", "lead")
	again := f.role(t, f.alice, f.project, "lead", "lead")
	if role.ID != again.ID || role.ConversationScope != "person_project" {
		t.Fatal("lead role not idempotent/scoped")
	}
	thread := f.thread(t, f.alice, role)
	againThread := f.thread(t, f.alice, role)
	if thread.ID != againThread.ID || thread.BindingEpoch != "0" || thread.Readiness.State != "offline" {
		t.Fatal("thread not durable offline identity")
	}
	bob := f.thread(t, f.bob, f.role(t, f.bob, f.project, "lead", "lead"))
	project := f.thread(t, f.alice, f.role(t, f.alice, f.secondProject, "lead", "lead"))
	worker := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "aeon-618-r2"))
	foreign := f.thread(t, f.foreign, f.role(t, f.foreign, f.foreignProject, "lead", "lead"))
	expect(t, f.call(f.alice, "GET", "/api/chat-threads/"+foreign.ID, nil, ""), 404)
	expect(t, f.call(f.foreign, "POST", "/api/projects/"+f.foreignProject+"/chat-threads/resolve", map[string]string{"role_id": role.ID}, ""), 404)
	bobSession, bobLease := f.session(t, f.bob, f.project, "coordinator", "unmanaged")
	f.bind(t, f.bob, bob, bobSession, "0")
	bobProof := WorkerBindingRequest{ConversationID: bob.ID, SessionID: bobSession, BindingEpoch: "1"}
	decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", bobProof, bobLease))
	workerSession, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
	f.bind(t, f.alice, worker, workerSession, "0")
	otherWorker := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "aeon-618-r3"))
	expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+otherWorker.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": workerSession}, ""), 409)

	if bob.ID == thread.ID || project.ID == thread.ID || worker.ID == thread.ID {
		t.Fatal("people/projects/assignments merged")
	}
	for _, p := range []tenant.Principal{f.bob, f.admin, f.foreign, f.agent} {
		expect(t, f.call(p, "GET", "/api/chat-threads/"+thread.ID, nil, ""), 404)
		expect(t, f.call(p, "POST", "/api/projects/"+f.project+"/chat-threads/resolve", map[string]string{"role_id": role.ID}, ""), 404)
	}
	first, lease := f.session(t, f.alice, f.project, "coordinator", "unmanaged")
	bound := f.bind(t, f.alice, thread, first, "0")
	if bound.ID != thread.ID || bound.BindingEpoch != "1" || bound.Revision != "2" || len(bound.Readiness.Capabilities) != 0 || bound.Readiness.State != "unavailable" {
		t.Fatal("binding invented readiness or changed conversation")
	}
	proof := WorkerBindingRequest{ConversationID: thread.ID, SessionID: first, BindingEpoch: "1"}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", bobProof, lease), 404)
	w := f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", proof, lease)
	verified := decode[Thread](t, w)
	if verified.ID != thread.ID {
		t.Fatal("worker got another conversation")
	}
	for _, hidden := range []string{"private-", "lease", "host", "model", "account", "quota", "session_id", "principal_id"} {
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatalf("chat DTO leaks %s", hidden)
		}
	}
	for _, badLease := range []string{"", "wrong-lease-fixture-00000000000000000"} {
		expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", proof, badLease), 404)
	}
	forged := proof
	forged.ConversationID = bob.ID
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", forged, lease), 404)
	forged = proof
	forged.SessionID = uid()
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", forged, lease), 404)
	forged = proof
	forged.BindingEpoch = "2"
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", forged, lease), 404)
	expect(t, f.call(f.alice, "POST", "/api/chat-deliveries/binding/resolve", proof, lease), 404)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.worker'] WHERE principal_id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", proof, lease), 404)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
	noScope := f.agent
	noScope.Scopes = []string{"inbox.send"}
	expect(t, f.call(noScope, "POST", "/api/chat-deliveries/binding/resolve", proof, lease), 404)
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", map[string]string{"harness_session_ref": first, "role_id": role.ID}, lease), 400)
	expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+worker.ID+"/binding", map[string]string{"expected_epoch": "1", "session_id": first}, ""), 404)
	second, secondLease := f.session(t, f.alice, f.project, "coordinator", "unmanaged")
	expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": second}, ""), 409)
	successor := f.bind(t, f.alice, thread, second, "1")
	if successor.ID != thread.ID || successor.BindingEpoch != "2" {
		t.Fatal("handover rewrote identity")
	}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", proof, lease), 404)
	proof.SessionID = second
	proof.BindingEpoch = "2"
	decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", proof, secondLease))
	var historical, active int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE valid_to IS NULL) FROM chat_session_bindings WHERE role_id=$1`, role.ID).Scan(&historical, &active); err != nil {
		t.Fatal(err)
	}
	if historical != 2 || active != 1 {
		t.Fatal("binding history was erased")
	}
	// Registration ownership transfer cannot reuse an already tainted context.
	f.preGuardMutation(t, `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, first, f.bob.ID)
	expect(t, f.call(f.bob, "POST", "/api/chat-threads/"+bob.ID+"/binding", map[string]string{"expected_epoch": "1", "session_id": first}, ""), 404)
}

func TestBindingRejectsForeignWrongRoleManagedAndStaleRegistration(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "lead", "lead"))
	for _, c := range []struct {
		owner                     tenant.Principal
		project, role, management string
	}{
		{f.bob, f.project, "coordinator", "unmanaged"}, {f.alice, f.secondProject, "coordinator", "unmanaged"},
		{f.alice, f.project, "worker", "unmanaged"}, {f.alice, f.project, "coordinator", "managed"},
	} {
		id, _ := f.session(t, c.owner, c.project, c.role, c.management)
		expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": id}, ""), 404)
	}
	id, _ := f.session(t, f.alice, f.project, "coordinator", "unmanaged")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET created_at=clock_timestamp()-interval '3 minutes',heartbeat_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": id}, ""), 404)
	// A managed agentd run is selectable only with the chat stream capability
	// and is otherwise held to exactly the same owner/project/role/liveness rules.
	for _, c := range []struct {
		owner         tenant.Principal
		project, role string
		caps          []string
	}{
		{f.alice, f.project, "coordinator", []string{"inbox", "steer", "interrupt"}}, {f.bob, f.project, "coordinator", []string{"chat"}},
		{f.alice, f.secondProject, "coordinator", []string{"chat"}}, {f.alice, f.project, "worker", []string{"chat"}},
	} {
		id, _ := f.sessionWith(t, f.agent.ID, c.owner, c.project, c.role, "managed", c.caps...)
		expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": id}, ""), 404)
	}
	for _, mutation := range []string{
		`UPDATE harness_sessions SET created_at=clock_timestamp()-interval '3 minutes',heartbeat_at=NULL WHERE id=$1`,
		`UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`,
		`UPDATE harness_sessions SET phase='stopping' WHERE id=$1`,
	} {
		id, _ := f.sessionWith(t, f.agent.ID, f.alice, f.project, "coordinator", "managed", "chat")
		if _, err := f.d.Admin.Exec(t.Context(), mutation, id); err != nil {
			t.Fatal(err)
		}
		expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": id}, ""), 404)
	}
	managed, _ := f.sessionWith(t, f.agent.ID, f.alice, f.project, "coordinator", "managed", "chat")
	if bound := f.bind(t, f.alice, thread, managed, "0"); bound.BindingEpoch != "1" || bound.Readiness.State != "unavailable" {
		t.Fatalf("managed chat registration not selectable: %+v", bound)
	}
	for _, epoch := range []string{"", "01", "-1", "9223372036854775808"} {
		expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": epoch, "session_id": id}, ""), 400)
	}
	expect(t, f.call(f.alice, "POST", "/api/projects/"+f.project+"/chat-roles", map[string]string{"kind": "worker", "slot_key": "worker"}, ""), 400)
	expect(t, f.call(f.alice, "POST", "/api/projects/"+f.project+"/chat-roles", map[string]string{"kind": "lead", "slot_key": "lead", "owner_person_id": f.bob.ID}, ""), 400)
}

func TestBindingVersusRevocationUsesFinalAccessFence(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "lead", "lead"))
	id, _ := f.session(t, f.alice, f.project, "coordinator", "unmanaged")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	revoke, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Rollback(ctx)
	if _, err = revoke.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.alice.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = revoke.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	// The database reports the conflicting lock: no sleeps or elapsed-time
	// assertion can accidentally pass without the operations overlapping.
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "0", "session_id": id}, "")
	}()
	observed := false
	for !observed {
		if ctx.Err() != nil {
			t.Fatal("binding never reached access fence")
		}
		if err = f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM tenants WHERE id=$1 FOR NO KEY UPDATE%')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
	}
	if err = revoke.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		expect(t, w, 404)
	case <-ctx.Done():
		t.Fatal("binding did not finish")
	}
	var count int
	if err = f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM chat_session_bindings WHERE role_id=$1`, thread.Role.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("binding committed after permission revocation")
	}
}

func TestDisabledAndBoundedContract(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/chat-threads/"+uid(), nil))
	expect(t, w, 404)
	for _, key := range []string{"chat.read", "chat.send", "chat.bind", "chat.receive"} {
		if _, ok := authz.Lookup(key); !ok {
			t.Fatal("missing permission", key)
		}
	}
	if p, _ := authz.Lookup("chat.bind"); p.AgentGrantable {
		t.Fatal("chat binding unexpectedly agent grantable")
	}
	f := newFixture(t)
	r := httptest.NewRequest("POST", "/api/projects/"+f.project+"/chat-roles", strings.NewReader(`{"kind":"lead","slot_key":"`+strings.Repeat("x", 9000)+`"}`)).WithContext(tenant.WithPrincipal(t.Context(), f.alice))
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	expect(t, w, 400)
}

// Risk: a greatest-position watermark marks unloaded gaps as seen, or a
// project administrator/cursor can read another person's private conversation.
func TestParticipantHistoryPreservesExactSeenGapsAndCursorScope(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "history"))
	session, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
	first, _ := f.chatMessage(t, thread, session)
	gap, _ := f.chatMessage(t, thread, session)
	last, _ := f.chatMessage(t, thread, session)
	path := "/api/chat-threads/" + thread.ID
	latest := decode[historyPage](t, f.call(f.alice, "GET", path+"/messages?limit=1", nil, ""))
	if len(latest.Items) != 1 || latest.Items[0].Message.ID != last || !latest.MoreBefore || latest.Before == nil {
		t.Fatalf("latest page: %+v", latest)
	}
	marker := decode[seenMarker](t, f.call(f.alice, "PUT", path+"/read-marker", seenUnion{[]string{first, last}, "0"}, ""))
	if marker.Prefix != "0" || len(marker.Chunks) == 0 {
		t.Fatalf("marker inferred unloaded prefix: %+v", marker)
	}
	page := decode[historyPage](t, f.call(f.alice, "GET", path+"/messages", nil, ""))
	if len(page.Items) != 3 || page.Items[0].ReadState != "seen" || page.Items[1].Message.ID != gap || page.Items[1].ReadState != "known_unread" || page.Items[2].ReadState != "seen" {
		t.Fatalf("exact gaps lost: %+v", page)
	}
	prior := decode[historyPage](t, f.call(f.alice, "GET", path+"/messages?limit=1&before="+*latest.Before, nil, ""))
	if len(prior.Items) != 1 || prior.Items[0].Message.ID != gap {
		t.Fatalf("before cursor: %+v", prior)
	}
	replay := decode[seenMarker](t, f.call(f.alice, "PUT", path+"/read-marker", seenUnion{[]string{first, last}, "0"}, ""))
	if replay.Revision != marker.Revision {
		t.Fatal("seen replay changed revision")
	}
	for _, person := range []tenant.Principal{f.bob, f.admin, f.foreign, f.agent} {
		expect(t, f.call(person, "GET", path+"/messages", nil, ""), 404)
		expect(t, f.call(person, "PUT", path+"/read-marker", seenUnion{[]string{gap}, "0"}, ""), 404)
	}
	other := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "other-history"))
	expect(t, f.call(f.alice, "GET", "/api/chat-threads/"+other.ID+"/messages?before="+*latest.Before, nil, ""), 400)
	expect(t, f.call(f.alice, "GET", path+"/messages?limit=101", nil, ""), 400)
	expect(t, f.call(f.alice, "GET", path+"/messages?before="+*latest.Before+"&after="+*latest.Before, nil, ""), 400)
	expect(t, f.call(f.alice, "PUT", path+"/read-marker", seenUnion{[]string{gap}, "1"}, ""), 409)
	expect(t, f.call(f.alice, "PUT", path+"/read-marker", seenUnion{[]string{uid()}, "0"}, ""), 404)
	// A history read has no fetch/ACK/delivery side effects.
	var changed bool
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT bool_or(acked_at IS NOT NULL OR fetched_at IS NOT NULL) FROM inbox_messages WHERE chat_thread_id=$1`, thread.ID).Scan(&changed); err != nil || changed {
		t.Fatalf("history manufactured delivery evidence: %v %v", changed, err)
	}
}
