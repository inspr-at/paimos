// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	digest     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	labelToken = "SOURCE-LABEL-ZEBRA-91"
	turnToken  = "TURN-BODY-ZEBRA-91"
	draftToken = "DRAFT-BODY-ZEBRA-91"
	titleToken = "DRAFT-TITLE-ZEBRA-91"
)

func TestIntakePreservesPersonEdits(t *testing.T) {
	ctx := t.Context()
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	foreignEvents := count(t, database, `SELECT count(*) FROM events WHERE tenant_id=$1`, fx.tenantB)
	mux := http.NewServeMux()
	New(database.App).Mount(mux)

	if w := call(mux, tenant.Principal{}, "", http.MethodGet, "/api/projects/"+fx.projectA+"/intake", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anon %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.agent, fx.writeToken, "/intake/sources", sourceBody("note", labelToken, "src-1", "")); w.Code != http.StatusForbidden {
		t.Fatalf("key without scope %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.agent, "", "/intake/sources", sourceBody("note", labelToken, "src-1", "")); w.Code != http.StatusForbidden {
		t.Fatalf("agent without key %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("url", labelToken, "src-cred", "https://user:pass@example.com/a")); w.Code != http.StatusBadRequest {
		t.Fatalf("credential locator %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("file", labelToken, "src-file", "")); strings.Contains(w.Body.String(), `"file_id"`) || w.Code != http.StatusBadRequest {
		t.Fatalf("file without id %d %s", w.Code, w.Body.String())
	}
	fileBody := `{"kind":"file","label":"x","file_id":"` + fx.projectA + `","content_sha256":"` + digest + `","idempotency_key":"src-missing-file"}`
	if w := fx.post(t, mux, fx.person, "", "/intake/sources", fileBody); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not owned") {
		t.Fatalf("unowned file %d %s", w.Code, w.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM intake_sources`); n != 0 {
		t.Fatalf("rejected sources were stored: %d", n)
	}

	first := fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("conversation", labelToken, "src-1", "ppm:AEON-36"))
	if first.Code != http.StatusCreated {
		t.Fatalf("source %d %s", first.Code, first.Body.String())
	}
	var src sourceView
	decodeJSON(t, first, &src)
	replay := fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("conversation", labelToken, "src-1", "ppm:AEON-36"))
	var srcAgain sourceView
	decodeJSON(t, replay, &srcAgain)
	if replay.Code != http.StatusCreated || srcAgain.ID != src.ID {
		t.Fatalf("source replay %d %s", replay.Code, replay.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM events WHERE tenant_id = $1 AND type = 'intake.source_recorded'`, fx.tenantA); n != 1 {
		t.Fatalf("source events %d", n)
	}
	if w := fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("note", "other", "src-1", "")); w.Code != http.StatusConflict {
		t.Fatalf("source key mismatch %d %s", w.Code, w.Body.String())
	}
	assertPayloadOmits(t, database, fx.tenantA, "intake.source_recorded", labelToken)

	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/transcript-turns", turnBody(src.ID, 0, "agent", fx.agent.ID, turnToken, "turn-1")); w.Code != http.StatusCreated {
		t.Fatalf("turn %d %s", w.Code, w.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM events WHERE tenant_id = $1 AND type = 'intake.turn_appended'`, fx.tenantA); n != 1 {
		t.Fatalf("turn events %d", n)
	}
	assertPayloadOmits(t, database, fx.tenantA, "intake.turn_appended", turnToken)
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/transcript-turns", turnBody(src.ID, 0, "agent", fx.agent.ID, turnToken, "turn-1")); w.Code != http.StatusCreated {
		t.Fatalf("turn replay %d %s", w.Code, w.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM events WHERE tenant_id = $1 AND type = 'intake.turn_appended'`, fx.tenantA); n != 1 {
		t.Fatalf("turn replay wrote another event: %d", n)
	}
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/transcript-turns", turnBody(src.ID, 2, "agent", fx.agent.ID, "skip", "turn-gap")); w.Code != http.StatusConflict {
		t.Fatalf("ordinal gap %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/transcript-turns", turnBody(src.ID, 1, "agent", fx.agentB.ID, "other agent", "turn-impersonate")); w.Code != http.StatusForbidden {
		t.Fatalf("impersonation %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/transcript-turns", turnBody(src.ID, 1, "person", fx.person.ID, "Ada said this", "turn-person")); w.Code != http.StatusCreated {
		t.Fatalf("recorded person turn %d %s", w.Code, w.Body.String())
	}

	if w := fx.post(t, mux, fx.person, "", "/intake/drafts", ""); w.Code != http.StatusForbidden {
		t.Fatalf("person propose %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", fx.brief(src.ID, "", 0, "no-grant", "Brief", "text")); w.Code != http.StatusForbidden {
		t.Fatalf("propose without grant %d %s", w.Code, w.Body.String())
	}
	badCite := fx.brief(fx.projectB, "", 0, "bad-cite", "Brief", draftToken)
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", badCite); w.Code != http.StatusForbidden {
		t.Fatalf("ungranted bad citation %d %s", w.Code, w.Body.String())
	}

	grantIntake(t, database.App, fx.tenantA, fx.agent, fx.person, fx.projectA)
	beforeDrafts := count(t, database, `SELECT count(*) FROM intake_drafts`)
	beforeEvents := count(t, database, `SELECT count(*) FROM events WHERE tenant_id = $1 AND type = 'intake.draft_proposed'`, fx.tenantA)
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", fx.brief("00000000-0000-0000-0000-000000000099", "", 0, "missing-source", "Brief", draftToken)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad citation %d %s", w.Code, w.Body.String())
	}
	if count(t, database, `SELECT count(*) FROM intake_drafts`) != beforeDrafts || count(t, database, `SELECT count(*) FROM events WHERE tenant_id = $1 AND type = 'intake.draft_proposed'`, fx.tenantA) != beforeEvents {
		t.Fatal("failed draft wrote a row or event")
	}

	personNode, personEvent := fx.nodeWithEvent(t, database, "memory", "MEM-1", "Person title", "Person body", fx.person.ID)
	proposed := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", fx.brief(src.ID, personNode, personEvent, "person-edit", "Rewritten", "Agent rewrite"))
	if proposed.Code != http.StatusCreated {
		t.Fatalf("propose onto person text %d %s", proposed.Code, proposed.Body.String())
	}
	var personDraft draftView
	decodeJSON(t, proposed, &personDraft)
	if personDraft.Status != "rejected" {
		t.Fatalf("person-edited draft status %s", personDraft.Status)
	}
	if title, body := fx.nodeText(t, database, personNode); title != "Person title" || body != "Person body" {
		t.Fatalf("propose patched the node to %q %q", title, body)
	}
	acceptPerson := fx.post(t, mux, fx.person, "", "/intake/drafts/"+personDraft.ID+"/accept", `{"expected_base_event_id":`+formatInt(personEvent)+`}`)
	if acceptPerson.Code != http.StatusConflict || !strings.Contains(acceptPerson.Body.String(), "preserved") {
		t.Fatalf("accept person edit %d %s", acceptPerson.Code, acceptPerson.Body.String())
	}
	if title, body := fx.nodeText(t, database, personNode); title != "Person title" || body != "Person body" {
		t.Fatalf("accept overwrote person text %q %q", title, body)
	}
	if n := count(t, database, `SELECT count(*) FROM intake_draft_acceptances WHERE draft_id = $1`, personDraft.ID); n != 0 {
		t.Fatal("rejected accept was stored")
	}

	agentNode, agentEvent := fx.nodeWithEvent(t, database, "memory", "MEM-2", "Agent title", "Agent body", fx.agent.ID)
	draftsBefore := count(t, database, `SELECT count(*) FROM intake_drafts`)
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", fx.brief(src.ID, agentNode, agentEvent+99, "stale-propose", "Stale", "Stale body")); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "target changed") {
		t.Fatalf("stale propose %d %s", w.Code, w.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM intake_drafts`); n != draftsBefore {
		t.Fatalf("stale propose stored a draft: %d", n)
	}
	agentDraft := fx.mustDraft(t, mux, fx.brief(src.ID, agentNode, agentEvent, "agent-edit", "Accepted title", "Accepted body"))
	if w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts/"+agentDraft.ID+"/accept", `{"expected_base_event_id":`+formatInt(agentEvent)+`}`); w.Code != http.StatusForbidden {
		t.Fatalf("agent accept %d %s", w.Code, w.Body.String())
	}
	stale := fx.post(t, mux, fx.person, "", "/intake/drafts/"+agentDraft.ID+"/accept", `{"expected_base_event_id":1}`)
	if agentEvent == 1 {
		t.Fatal("fixture event id collided with the stale expectation")
	}
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale expected base %d %s", stale.Code, stale.Body.String())
	}
	accepted := fx.post(t, mux, fx.person, "", "/intake/drafts/"+agentDraft.ID+"/accept", `{"expected_base_event_id":`+formatInt(agentEvent)+`}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("accept agent text %d %s", accepted.Code, accepted.Body.String())
	}
	var acceptedView draftView
	decodeJSON(t, accepted, &acceptedView)
	if acceptedView.Status != "accepted" || acceptedView.TargetNodeID == nil || *acceptedView.TargetNodeID != agentNode {
		t.Fatalf("accepted view %+v", acceptedView)
	}
	if title, body := fx.nodeText(t, database, agentNode); title != "Accepted title" || body != "Accepted body" {
		t.Fatalf("accepted text %q %q", title, body)
	}
	again := fx.post(t, mux, fx.person, "", "/intake/drafts/"+agentDraft.ID+"/accept", `{"expected_base_event_id":`+formatInt(agentEvent)+`}`)
	if again.Code != http.StatusOK {
		t.Fatalf("accept replay %d %s", again.Code, again.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM intake_draft_acceptances WHERE draft_id = $1`, agentDraft.ID); n != 1 {
		t.Fatalf("acceptances %d", n)
	}
	last := fx.lastEvent(t, database, agentNode)
	later := fx.mustDraft(t, mux, fx.brief(src.ID, agentNode, last, "later-draft", "Later title", "Later body"))
	if later.Status != "rejected" {
		t.Fatalf("later draft status %s", later.Status)
	}
	if w := fx.post(t, mux, fx.person, "", "/intake/drafts/"+later.ID+"/accept", `{"expected_base_event_id":`+formatInt(last)+`}`); w.Code != http.StatusConflict {
		t.Fatalf("later accept %d %s", w.Code, w.Body.String())
	}
	if title, _ := fx.nodeText(t, database, agentNode); title != "Accepted title" {
		t.Fatalf("later draft patched accepted text to %q", title)
	}

	requirement := fx.mustDraft(t, mux, fx.requirement(src.ID, "req-1"))
	assertPayloadOmits(t, database, fx.tenantA, "intake.draft_proposed", draftToken)
	assertPayloadOmits(t, database, fx.tenantA, "intake.draft_proposed", titleToken)
	reqAccept := fx.post(t, mux, fx.person, "", "/intake/drafts/"+requirement.ID+"/accept", `{"expected_base_event_id":0}`)
	if reqAccept.Code != http.StatusOK {
		t.Fatalf("accept requirement %d %s", reqAccept.Code, reqAccept.Body.String())
	}
	var reqView draftView
	decodeJSON(t, reqAccept, &reqView)
	if reqView.Status != "accepted" || reqView.TargetNodeID == nil || len(reqView.Suggestions) != 1 || !reqView.Suggestions[0].Later || !reqView.Suggestions[0].AccessChange {
		t.Fatalf("requirement view %+v", reqView)
	}
	var kind string
	if err := database.Admin.QueryRow(ctx, `
		SELECT k.slug FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = $1::uuid`, *reqView.TargetNodeID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "requirement" {
		t.Fatalf("requirement node kind %s", kind)
	}
	if n := count(t, database, `SELECT count(*) FROM journey_requirements WHERE requirement_node_id=$1`, *reqView.TargetNodeID); n != 0 {
		t.Fatalf("acceptance wrote retired Flow metadata: %d", n)
	}
	if n := count(t, database, `SELECT count(*) FROM journey_tickets`); n != 0 {
		t.Fatalf("acceptance invented tickets: %d", n)
	}
	replayReq := fx.post(t, mux, fx.person, "", "/intake/drafts/"+requirement.ID+"/accept", `{"expected_base_event_id":0}`)
	if replayReq.Code != http.StatusOK {
		t.Fatalf("requirement replay %d %s", replayReq.Code, replayReq.Body.String())
	}
	if n := count(t, database, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id WHERE k.slug = 'requirement'`); n != 1 {
		t.Fatalf("requirement nodes %d", n)
	}
	assertPayloadOmits(t, database, fx.tenantA, "intake.draft_accepted", draftToken)

	brief := fx.mustDraft(t, mux, fx.brief(src.ID, "", 0, "brief-1", "Brief title", "Brief body"))
	if w := fx.post(t, mux, fx.person, "", "/intake/drafts/"+brief.ID+"/accept", `{"expected_base_event_id":0}`); w.Code != http.StatusOK {
		t.Fatalf("accept brief %d %s", w.Code, w.Body.String())
	}
	if title, _ := fx.nodeText(t, database, fx.projectA); title != "Journey" {
		t.Fatalf("brief accept renamed the project to %q", title)
	}
	if n := count(t, database, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id WHERE k.slug = 'memory' AND n.parent_id = $1`, fx.projectA); n != 3 {
		t.Fatalf("memory nodes %d", n)
	}

	var snap snapshot
	decodeJSON(t, fx.get(t, mux, fx.person, ""), &snap)
	if snap.Sources == nil || snap.Turns == nil || snap.Drafts == nil || len(snap.Sources) != 1 || len(snap.Turns) != 2 {
		t.Fatalf("snapshot %+v", snap)
	}
	if w := fx.get(t, mux, fx.personB, ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross tenant read %d %s", w.Code, w.Body.String())
	}
	other := call(mux, fx.personB, "", http.MethodGet, "/api/projects/"+fx.projectB+"/intake", "")
	var otherSnap snapshot
	if other.Code != http.StatusOK {
		t.Fatalf("tenant B intake %d %s", other.Code, other.Body.String())
	}
	decodeJSON(t, other, &otherSnap)
	if len(otherSnap.Sources) != 0 || len(otherSnap.Drafts) != 0 {
		t.Fatalf("tenant B saw tenant A intake %+v", otherSnap)
	}
	if n := count(t, database, `SELECT count(*) FROM events WHERE tenant_id = $1`, fx.tenantB); n != foreignEvents {
		t.Fatalf("events leaked into tenant B: %d", n)
	}

	read := call(mux, fx.agent, "Bearer "+fx.readToken, http.MethodGet, "/api/projects/"+fx.projectA+"/intake", "")
	if read.Code != http.StatusOK {
		t.Fatalf("read scope %d %s", read.Code, read.Body.String())
	}
	if w := call(mux, fx.agent, fx.readToken, http.MethodPost, "/api/projects/"+fx.projectA+"/intake/sources", sourceBody("note", "nope", "src-read", "")); w.Code != http.StatusForbidden {
		t.Fatalf("read scope write %d %s", w.Code, w.Body.String())
	}
}

type fixture struct {
	tenantA    string
	tenantB    string
	person     tenant.Principal
	personB    tenant.Principal
	agent      tenant.Principal
	agentB     tenant.Principal
	projectA   string
	projectB   string
	token      string
	readToken  string
	writeToken string
}

func newFixture(t *testing.T, database *dbtest.DB) fixture {
	t.Helper()
	fx := fixture{
		tenantA: insertTenant(t, database.Admin, "intake-a"),
		tenantB: insertTenant(t, database.Admin, "intake-b"),
	}
	fx.person = insertPrincipal(t, database.Admin, fx.tenantA, tenant.Person, "Ada")
	fx.personB = insertPrincipal(t, database.Admin, fx.tenantB, tenant.Person, "Bea")
	fx.agent = insertPrincipal(t, database.Admin, fx.tenantA, tenant.Agent, "Aithema")
	fx.agentB = insertPrincipal(t, database.Admin, fx.tenantA, tenant.Agent, "Other")
	fx.projectA = insertProject(t, database.Admin, fx.tenantA, "PRJ-1", "Journey")
	fx.projectB = insertProject(t, database.Admin, fx.tenantB, "PRJ-1", "Other journey")
	fx.token = insertKey(t, database.Admin, fx.agent, []string{scopeWrite})
	fx.readToken = insertKey(t, database.Admin, fx.agent, []string{scopeRead})
	fx.writeToken = insertKey(t, database.Admin, fx.agent, []string{"events:read"})
	return fx
}

func (fx fixture) post(t *testing.T, mux *http.ServeMux, p tenant.Principal, token, suffix, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(mux, p, token, http.MethodPost, "/api/projects/"+fx.projectA+suffix, body)
}

func (fx fixture) get(t *testing.T, mux *http.ServeMux, p tenant.Principal, suffix string) *httptest.ResponseRecorder {
	t.Helper()
	return call(mux, p, "", http.MethodGet, "/api/projects/"+fx.projectA+"/intake"+suffix, "")
}

func (fx fixture) brief(source, target string, base int64, key, title, body string) string {
	return draftJSON("brief", "", target, source, "", base, key, title, body, false)
}

func (fx fixture) requirement(source, key string) string {
	return draftJSON("requirement", "functional", "", source, "", 0, key, titleToken, draftToken, true)
}

func (fx fixture) mustDraft(t *testing.T, mux *http.ServeMux, body string) draftView {
	t.Helper()
	w := fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("draft %d %s", w.Code, w.Body.String())
	}
	var out draftView
	decodeJSON(t, w, &out)
	return out
}

func (fx fixture) nodeWithEvent(t *testing.T, database *dbtest.DB, slug, key, title, body, actor string) (string, int64) {
	t.Helper()
	if slug == "requirement" {
		if err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `SELECT aeon_seed_requirement_kind($1::uuid)`, fx.tenantA)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	if err := database.Admin.QueryRow(t.Context(), `
		INSERT INTO nodes (tenant_id, key, kind_id, title, body, parent_id)
		SELECT $1::uuid, $2, id, $4, $5, $6::uuid FROM node_kinds
		WHERE tenant_id = $1::uuid AND slug = $3
		RETURNING id::text`, fx.tenantA, key, slug, title, body, fx.projectA).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var eventID int64
	after := `{"title":` + jsonString(title) + `,"body":` + jsonString(body) + `}`
	if err := database.Admin.QueryRow(t.Context(), `
		INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'node.created', $4::jsonb)
		RETURNING id`, fx.tenantA, actor, id, after).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	return id, eventID
}

func (fx fixture) nodeText(t *testing.T, database *dbtest.DB, id string) (string, string) {
	t.Helper()
	var title, body string
	if err := database.Admin.QueryRow(t.Context(), `SELECT title, body FROM nodes WHERE id = $1::uuid`, id).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	return title, body
}

func (fx fixture) lastEvent(t *testing.T, database *dbtest.DB, nodeID string) int64 {
	t.Helper()
	var id int64
	if err := database.Admin.QueryRow(t.Context(), `SELECT COALESCE(MAX(id), 0) FROM events WHERE node_id = $1::uuid`, nodeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertTenant(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPrincipal(t *testing.T, pool *pgxpool.Pool, tenantID string, kind tenant.PrincipalKind, name string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: tenantID, Kind: kind, Name: name}
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		tenantID, string(kind), name).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	// Handlers see project data only through a binding (ADR-003 P2).
	dbtest.BindRoleWith(t, pool, tenantID, p.ID, "member")
	return p
}

func insertProject(t *testing.T, pool *pgxpool.Pool, tenantID, key, title string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO nodes (tenant_id, key, kind_id, title, body)
		SELECT $1::uuid, $2, id, $3, '' FROM node_kinds
		WHERE tenant_id = $1::uuid AND slug = 'project'
		RETURNING id::text`, tenantID, key, title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO journey_projects (tenant_id, project_node_id) VALUES ($1::uuid, $2::uuid)`, tenantID, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertKey(t *testing.T, pool *pgxpool.Pool, p tenant.Principal, scopes []string) string {
	t.Helper()
	secret := hex.EncodeToString([]byte(p.Name + strings.Join(scopes, ",") + "-secret-value!!"))
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(p.TenantID, "-", "") + hex.EncodeToString(sum[:8])
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO agent_keys (tenant_id, principal_id, name, prefix, hash, scopes,created_by_principal_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6,(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`,
		p.TenantID, p.ID, p.Name, prefix, hex.EncodeToString(sum[:]), scopes); err != nil {
		t.Fatal(err)
	}
	return "aeon_" + prefix + "_" + secret
}

func grantIntake(t *testing.T, pool *pgxpool.Pool, tenantID string, agent, person tenant.Principal, projectID string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO approval_requests (
				tenant_id, proposed_by_principal_id, agent_principal_id,
				scope, resource_kind, resource_id, rationale, expires_at)
			VALUES ($1::uuid, $2::uuid, $2::uuid, 'intake.write', 'node', $3::uuid, 'propose intake', now() + interval '1 day')
			RETURNING id::text`, tenantID, agent.ID, projectID).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO approval_decisions (tenant_id, request_id, decided_by_principal_id, decision, reason)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'approved', 'ok')`, tenantID, id, person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO agent_permission_grants (
				tenant_id, approval_request_id, agent_principal_id, scope, resource_kind, resource_id, valid_until)
			SELECT tenant_id, id, agent_principal_id, scope, resource_kind, resource_id, expires_at
			FROM approval_requests WHERE id = $1::uuid`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func call(mux *http.ServeMux, p tenant.Principal, token, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		if strings.HasPrefix(token, "Bearer ") {
			req.Header.Set("Authorization", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	if p.ID != "" {
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func sourceBody(kind, label, key, locator string) string {
	payload := map[string]any{
		"kind": kind, "label": label, "content_sha256": digest, "idempotency_key": key,
	}
	if locator != "" {
		payload["locator"] = locator
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func turnBody(source string, ordinal int, speaker, principal, body, key string) string {
	payload := map[string]any{
		"source_id": source, "ordinal": ordinal, "speaker": speaker, "body": body, "idempotency_key": key,
	}
	if principal != "" {
		payload["speaker_principal_id"] = principal
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func draftJSON(kind, requirementKind, target, source, turn string, base int64, key, title, body string, suggest bool) string {
	payload := map[string]any{
		"kind": kind, "title": title, "body": body, "base_event_id": base, "idempotency_key": key,
		"citations": []map[string]any{{"source_id": source, "locator": "p:1"}},
	}
	if requirementKind != "" {
		payload["requirement_kind"] = requirementKind
	}
	if target != "" {
		payload["target_node_id"] = target
	}
	if turn != "" {
		cites := payload["citations"].([]map[string]any)
		cites[0]["turn_id"] = turn
	}
	if suggest {
		payload["ticket_suggestions"] = []map[string]any{{
			"title": "Ship the gate", "estimated_hours": 1.5, "later": true, "access_change": true,
		}}
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("status %d body %s: %v", w.Code, w.Body.String(), err)
	}
}

func count(t *testing.T, database *dbtest.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := database.Admin.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertPayloadOmits(t *testing.T, database *dbtest.DB, tenantID, eventType, token string) {
	t.Helper()
	rows, err := database.Admin.Query(t.Context(), `
		SELECT coalesce(before::text, ''), coalesce(after::text, '')
		FROM events WHERE tenant_id = $1::uuid AND type = $2`, tenantID, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var before, after string
		if err := rows.Scan(&before, &after); err != nil {
			t.Fatal(err)
		}
		seen++
		if strings.Contains(before, token) || strings.Contains(after, token) {
			t.Fatalf("%s payload contains %s", eventType, token)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatalf("no %s events", eventType)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func formatInt(n int64) string {
	return strconv.FormatInt(n, 10)
}
