// SPDX-License-Identifier: AGPL-3.0-only

package deliveryvote

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestDeliveryRoutesArePersonRead(t *testing.T) {
	for _, pattern := range []string{
		"GET /api/nodes/{nodeId}/delivery-ratings",
		"GET /api/harness-sessions/{sessionId}/delivery-rating",
		"PUT /api/harness-sessions/{sessionId}/delivery-rating",
	} {
		if authz.RoutePermissions[pattern] != "nodes.read" {
			t.Fatalf("%s = %q", pattern, authz.RoutePermissions[pattern])
		}
	}
}

func TestRejectsAgentAndInvalidRating(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	person := tenant.Principal{ID: id(), TenantID: id(), Kind: tenant.Person}
	agent := tenant.Principal{ID: id(), TenantID: person.TenantID, Kind: tenant.Agent}
	session := id()
	call := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(context.Background(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := call(agent, http.MethodPut, "/api/harness-sessions/"+session+"/delivery-rating", `{"score":4,"tags":[],"comment":""}`); w.Code != http.StatusForbidden {
		t.Fatalf("agent %d %s", w.Code, w.Body.String())
	}
	if w := call(agent, http.MethodGet, "/api/harness-sessions/"+session+"/delivery-rating", ""); w.Code != http.StatusForbidden {
		t.Fatalf("agent get %d %s", w.Code, w.Body.String())
	}
	if w := call(tenant.Principal{}, http.MethodGet, "/api/nodes/"+id()+"/delivery-ratings", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anon %d", w.Code)
	}
	if w := call(person, http.MethodGet, "/api/nodes/not-a-uuid/delivery-ratings", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad node %d", w.Code)
	}
	path := "/api/harness-sessions/" + session + "/delivery-rating"
	long := strings.Repeat("a", 2001)
	for _, body := range []string{
		`{"score":0,"tags":[],"comment":""}`,
		`{"score":6,"tags":[],"comment":""}`,
		`{"score":1.5,"tags":[],"comment":""}`,
		`{"score":4,"tags":["nope"],"comment":""}`,
		`{"score":4,"tags":["quality","quality"],"comment":""}`,
		`{"score":4,"comment":""}`,
		`{"score":4,"tags":[]}`,
		`{"score":4,"tags":[],"comment":"","extra":true}`,
		`{"score":4,"tags":[],"comment":"` + long + `"}`,
		`{"score":4,"tags":[],"comment":"bad\u0001"}`,
	} {
		if w := call(person, http.MethodPut, path, body); w.Code != http.StatusBadRequest {
			t.Fatalf("body %s -> %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSavesOneVotePerPerson(t *testing.T) {
	f := newFix(t)
	project, epic, ticket, other := id(), id(), id(), id()
	f.node(t, project, "project", "VT1-1", "Visible", "")
	f.node(t, epic, "epic", "VT1-2", "Epic", project)
	f.node(t, ticket, "ticket", "VT1-3", "Ticket", epic)
	f.node(t, other, "ticket", "VT1-4", "Sibling", project)
	dbtest.BindRole(t, f.db, f.person.TenantID, f.member.ID, "member")

	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	stopped := created.Add(24 * time.Hour)
	session := id()
	f.session(t, session, project, ticket, "gpt-4.1", "desk", created, &stopped)

	if w := f.call(f.guest, http.MethodGet, "/api/harness-sessions/"+session+"/delivery-rating", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unbound guest %d %s", w.Code, w.Body.String())
	}
	f.bindGuest(t, project)
	if w := f.call(f.guest, http.MethodGet, "/api/harness-sessions/"+session+"/delivery-rating", ""); w.Code != http.StatusOK {
		t.Fatalf("bound guest %d %s", w.Code, w.Body.String())
	}
	if w := f.call(f.foreign, http.MethodGet, "/api/harness-sessions/"+session+"/delivery-rating", ""); w.Code != http.StatusNotFound {
		t.Fatalf("foreign %d %s", w.Code, w.Body.String())
	}
	loose := id()
	f.session(t, loose, project, "", "gpt-4.1", "desk", created, &stopped)
	if w := f.call(f.person, http.MethodPut, "/api/harness-sessions/"+loose+"/delivery-rating", `{"score":4,"tags":[],"comment":""}`); w.Code != http.StatusConflict {
		t.Fatalf("no ticket %d %s", w.Code, w.Body.String())
	}

	first := f.put(t, f.person, session, `{"score":5,"tags":["taste","quality"],"comment":"  kept\nline  "}`)
	if first.Mine == nil || first.Mine.Score != 5 || first.Mine.Comment != "kept\nline" || strings.Join(first.Mine.Tags, ",") != "quality,taste" {
		t.Fatalf("mine %+v", first.Mine)
	}
	if first.Votes != 1 || first.Average == nil || *first.Average != "5.00" || first.AccountLabel == nil || *first.AccountLabel != "desk" || first.Model == nil || *first.Model != "gpt-4.1" {
		t.Fatalf("first %+v", first)
	}
	if first.Signals.ReviewRounds != 0 || first.Signals.CIFailures != 0 || first.Signals.Reverts != 0 {
		t.Fatalf("signals %+v", first.Signals)
	}
	again := f.put(t, f.person, session, `{"score":5,"tags":["rework"],"comment":"edited"}`)
	if again.Votes != 1 || again.Mine == nil || again.Mine.Comment != "edited" || strings.Join(again.Mine.Tags, ",") != "rework" {
		t.Fatalf("edit %+v votes %d", again.Mine, again.Votes)
	}
	if n := f.count(t, session); n != 1 {
		t.Fatalf("rows %d", n)
	}
	second := f.put(t, f.member, session, `{"score":3,"tags":[],"comment":""}`)
	if second.Votes != 2 || second.Average == nil || *second.Average != "4.00" || second.Mine == nil || second.Mine.Score != 3 {
		t.Fatalf("member %+v", second)
	}
	home := f.get(t, f.person, session)
	if home.Mine == nil || home.Mine.Score != 5 || home.Votes != 2 || home.Average == nil || *home.Average != "4.00" {
		t.Fatalf("home still owns its vote %+v", home)
	}
	if !bytes.Contains(f.raw(t, f.person, session), []byte(`"average":"4.00"`)) {
		t.Fatal("average was not a decimal string")
	}

	f.exec(t, f.person, `UPDATE harness_sessions SET model = 'gpt-4.2', account_label = 'other' WHERE id = $1`, session)
	live := f.get(t, f.person, session)
	if live.Model == nil || *live.Model != "gpt-4.2" || live.AccountLabel == nil || *live.AccountLabel != "other" {
		t.Fatalf("live metadata %+v", live)
	}
	var stored string
	f.scan(t, f.person, &stored, `SELECT model FROM agent_delivery_votes WHERE session_id = $1 AND voter_principal_id = $2`, session, f.person.ID)
	if stored != "gpt-4.1" {
		t.Fatalf("snapshot changed before the next save: %s", stored)
	}
	f.put(t, f.person, session, `{"score":5,"tags":["rework"],"comment":"edited"}`)
	f.scan(t, f.person, &stored, `SELECT model FROM agent_delivery_votes WHERE session_id = $1 AND voter_principal_id = $2`, session, f.person.ID)
	if stored != "gpt-4.2" {
		t.Fatalf("snapshot was not refreshed: %s", stored)
	}
	var before, after string
	f.scan(t, f.person, &before, `SELECT coalesce(before->>'model','') FROM events WHERE type = 'delivery.rated' AND node_id = $1 ORDER BY id DESC LIMIT 1`, ticket)
	f.scan(t, f.person, &after, `SELECT after->>'model' FROM events WHERE type = 'delivery.rated' AND node_id = $1 ORDER BY id DESC LIMIT 1`, ticket)
	if before != "gpt-4.1" || after != "gpt-4.2" {
		t.Fatalf("audit before %s after %s", before, after)
	}

	page := f.list(t, f.person, epic)
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != session {
		t.Fatalf("epic page %+v", page.Sessions)
	}
	sibling := id()
	f.session(t, sibling, project, other, "gpt-4.1", "desk", created, &stopped)
	page = f.list(t, f.person, epic)
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != session {
		t.Fatalf("sibling leaked %+v", page.Sessions)
	}

	f.signals(t, project, ticket, session, created, stopped)
	rated := f.get(t, f.person, session)
	if rated.Signals.ReviewRounds != 2 || rated.Signals.CIFailures != 2 || rated.Signals.Reverts != 1 {
		t.Fatalf("signals %+v", rated.Signals)
	}

	if _, err := f.db.Admin.Exec(context.Background(), `UPDATE principals SET status = 'deactivated' WHERE id = $1`, f.quiet.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.call(f.quiet, http.MethodPut, "/api/harness-sessions/"+session+"/delivery-rating", `{"score":1,"tags":[],"comment":""}`); w.Code != http.StatusForbidden {
		t.Fatalf("deactivated %d %s", w.Code, w.Body.String())
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_delivery_votes
			(tenant_id, session_id, ticket_node_id, voter_principal_id, score, tags, comment, harness)
			VALUES ($1,$2,$3,$4,4,'{}','','codex')`, f.person.TenantID, session, ticket, f.agent.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "limited to active people") {
		t.Fatalf("agent insert: %v", err)
	}
}

func (f *fix) signals(t *testing.T, project, ticket, session string, created, stopped time.Time) {
	t.Helper()
	inside := created.Add(time.Hour)
	outside := stopped.Add(24 * time.Hour)
	f.event(t, ticket, "review.verdict", `{"round":"1"}`, inside, nil)
	f.event(t, ticket, "review.verdict", `{"round":"1"}`, inside.Add(time.Minute), nil)
	f.event(t, ticket, "gate.finding", `{}`, inside.Add(2*time.Minute), nil)
	f.event(t, ticket, "ci.failed", `{}`, inside, nil)
	f.event(t, ticket, "ci.failed", `{}`, outside, nil)
	f.event(t, ticket, "ci.failure", fmt.Sprintf(`{"session_id":%q}`, session), outside, nil)
	f.event(t, ticket, "check.failed", fmt.Sprintf(`{"session_id":%q}`, id()), inside, nil)
	original := f.event(t, ticket, "node.updated", `{}`, inside, nil)
	f.event(t, ticket, "node.updated", `{}`, inside.Add(time.Minute), &original)
	sibling := id()
	f.node(t, sibling, "ticket", "VT1-9", "Elsewhere", project)
	f.event(t, sibling, "ci.failed", `{}`, inside, nil)
	decoy := f.event(t, ticket, "node.updated", `{}`, inside, nil)
	f.event(t, ticket, "delivery.rated", fmt.Sprintf(`{"session_id":%q}`, session), inside, &decoy)
}

type fix struct {
	db                           *dbtest.DB
	mux                          *http.ServeMux
	person, member, guest, quiet tenant.Principal
	agent, foreign               tenant.Principal
}

func newFix(t *testing.T) *fix {
	t.Helper()
	f := &fix{db: dbtest.Open(t), mux: http.NewServeMux()}
	f.person = tenant.Principal{ID: id(), TenantID: id(), Kind: tenant.Person}
	f.member = tenant.Principal{ID: id(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.guest = tenant.Principal{ID: id(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.quiet = tenant.Principal{ID: id(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.agent = tenant.Principal{ID: id(), TenantID: f.person.TenantID, Kind: tenant.Agent}
	f.foreign = tenant.Principal{ID: id(), TenantID: id(), Kind: tenant.Person}
	for _, p := range []tenant.Principal{f.person, f.foreign} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES ($1,$2,'Votes')`, p.TenantID, "vt-"+p.TenantID[:8])
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.person, f.member, f.guest, f.quiet, f.agent, f.foreign} {
		f.exec(t, p, `INSERT INTO principals(tenant_id,id,kind,name) VALUES ($1,$2,$3,'voter')`, p.TenantID, p.ID, string(p.Kind))
	}
	dbtest.BindRole(t, f.db, f.person.TenantID, f.person.ID, "admin")
	dbtest.BindRole(t, f.db, f.foreign.TenantID, f.foreign.ID, "admin")
	New(f.db.App).Mount(f.mux)
	return f
}

func (f *fix) exec(t *testing.T, p tenant.Principal, sql string, args ...any) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), sql, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fix) scan(t *testing.T, p tenant.Principal, dest any, sql string, args ...any) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), sql, args...).Scan(dest)
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fix) node(t *testing.T, nodeID, kind, key, title, parent string) {
	t.Helper()
	var parentArg any
	if parent != "" {
		parentArg = parent
	}
	f.exec(t, f.person, `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id)
		SELECT $1,$2,$3,k.id,$4,$5 FROM node_kinds k WHERE k.slug = $6`, f.person.TenantID, nodeID, key, title, parentArg, kind)
}

func (f *fix) bindGuest(t *testing.T, project string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(context.Background(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		SELECT $1,$2,r.id,'project',$3 FROM roles r WHERE r.tenant_id = $1 AND r.key = 'guest'`, f.person.TenantID, f.guest.ID, project); err != nil {
		t.Fatal(err)
	}
}

func (f *fix) session(t *testing.T, sessionID, project, ticket, model, account string, created time.Time, stopped *time.Time) {
	t.Helper()
	shape, phase := "unknown", "working"
	var ticketArg, stoppedArg, modelArg, accountArg any
	if ticket != "" {
		shape = "ship"
		ticketArg = ticket
	}
	if stopped != nil {
		phase = "stopped"
		stoppedArg = *stopped
	}
	if model != "" {
		modelArg = model
	}
	if account != "" {
		accountArg = account
	}
	sum := sha256.Sum256([]byte(sessionID))
	lease := sha256.Sum256([]byte("lease:" + sessionID))
	f.exec(t, f.person, `INSERT INTO harness_sessions(
		tenant_id,id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,
		ref_digest,lease_digest,model,account_label,phase,created_at,stopped_at,heartbeat_at)
		VALUES ($1,$2,$3,$4,$5,'codex','test-host','unmanaged','worker',$6,$7,$8,$9,$10,$11,$12,$13,$12)`,
		f.person.TenantID, sessionID, project, f.agent.ID, ticketArg, shape, sum[:], lease[:], modelArg, accountArg, phase, created, stoppedArg)
}

func (f *fix) event(t *testing.T, node, typ, after string, at time.Time, undo *int64) int64 {
	t.Helper()
	var eventID int64
	var undoArg any
	if undo != nil {
		undoArg = *undo
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id, actor_principal_id, node_id, type, after, at, undo_of)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7) RETURNING id`, f.person.TenantID, f.person.ID, node, typ, after, at, undoArg).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	return eventID
}

func (f *fix) count(t *testing.T, session string) int {
	t.Helper()
	var n int
	f.scan(t, f.person, &n, `SELECT count(*) FROM agent_delivery_votes WHERE session_id = $1`, session)
	return n
}

func (f *fix) call(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(context.Background(), p))
	if body == "" {
		r = httptest.NewRequest(method, path, nil).WithContext(tenant.WithPrincipal(context.Background(), p))
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *fix) put(t *testing.T, p tenant.Principal, session, body string) Rating {
	t.Helper()
	w := f.call(p, http.MethodPut, "/api/harness-sessions/"+session+"/delivery-rating", body)
	if w.Code != http.StatusOK {
		t.Fatalf("put %d %s", w.Code, w.Body.String())
	}
	var rating Rating
	if err := json.Unmarshal(w.Body.Bytes(), &rating); err != nil {
		t.Fatal(err)
	}
	return rating
}

func (f *fix) get(t *testing.T, p tenant.Principal, session string) Rating {
	t.Helper()
	w := f.call(p, http.MethodGet, "/api/harness-sessions/"+session+"/delivery-rating", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get %d %s", w.Code, w.Body.String())
	}
	var rating Rating
	if err := json.Unmarshal(w.Body.Bytes(), &rating); err != nil {
		t.Fatal(err)
	}
	return rating
}

func (f *fix) raw(t *testing.T, p tenant.Principal, session string) []byte {
	t.Helper()
	w := f.call(p, http.MethodGet, "/api/harness-sessions/"+session+"/delivery-rating", "")
	if w.Code != http.StatusOK {
		t.Fatalf("raw %d %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func (f *fix) list(t *testing.T, p tenant.Principal, nodeID string) Page {
	t.Helper()
	w := f.call(p, http.MethodGet, "/api/nodes/"+nodeID+"/delivery-ratings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func id() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
