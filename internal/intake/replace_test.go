// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func replacementFixture(t *testing.T) (*dbtest.DB, fixture, *http.ServeMux, sourceView) {
	t.Helper()
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	grantIntake(t, database.App, fx.tenantA, fx.agent, fx.person, fx.projectA)
	mux := http.NewServeMux()
	New(database.App).Mount(mux)
	w := fx.post(t, mux, fx.person, "", "/intake/sources", sourceBody("note", "Evidence", "replace-source", ""))
	if w.Code != 201 {
		t.Fatalf("source %d %s", w.Code, w.Body.String())
	}
	var source sourceView
	decodeJSON(t, w, &source)
	return database, fx, mux, source
}

func TestAtomicReplacementLifecycle(t *testing.T) {
	database, fx, mux, source := replacementFixture(t)
	old := fx.mustDraft(t, mux, fx.brief(source.ID, "", 0, "old-draft", "Old", "Original"))
	path := "/intake/drafts/" + old.ID + "/replace"
	body := fx.brief(source.ID, "", 0, "replace-key", "New", "Replacement")
	before := count(t, database, `SELECT count(*) FROM intake_drafts`)
	bad := fx.brief("10000000-0000-4000-8000-000000000099", "", 0, "invalid-replace", "Bad", "Missing citation")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, path, bad), 422, "citation_invalid")
	if n := count(t, database, `SELECT count(*) FROM intake_drafts`); n != before {
		t.Fatal("failed replacement leaked a draft")
	}
	first := fx.post(t, mux, fx.agent, fx.token, path, body)
	if first.Code != 201 {
		t.Fatalf("replace: %d %s", first.Code, first.Body.String())
	}
	var next draftView
	decodeJSON(t, first, &next)
	if next.SupersedesDraftID == nil || *next.SupersedesDraftID != old.ID || next.Status != "proposed" {
		t.Fatalf("replacement link %+v", next)
	}
	assertErrorCode(t, fx.post(t, mux, fx.person, "", "/intake/drafts/"+old.ID+"/accept", `{"expected_base_event_id":0}`), 409, "draft_superseded")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, path, fx.brief(source.ID, "", 0, "second-replace", "Other", "Other")), 409, "draft_superseded")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, path, body+"\n"), 409, "idempotency_conflict")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, path, strings.Replace(body, `"kind":"brief"`, `"kind":"invalid"`, 1)), 409, "idempotency_conflict")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, "/intake/drafts/"+next.ID+"/replace", body), 409, "idempotency_conflict")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", body), 409, "idempotency_conflict")
	accepted := fx.post(t, mux, fx.person, "", "/intake/drafts/"+next.ID+"/accept", `{"expected_base_event_id":0}`)
	if accepted.Code != 200 {
		t.Fatalf("accept replacement %d %s", accepted.Code, accepted.Body.String())
	}
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, "/intake/drafts/"+next.ID+"/replace", fx.brief(source.ID, "", 0, "accepted-replace", "Impossible", "Terminal")), 409, "already_accepted")
	// A new Module models a restart. Replay returns the original response bytes
	// (proposed) even though the replacement is now accepted in the snapshot.
	restarted := http.NewServeMux()
	New(database.App).Mount(restarted)
	retry := fx.post(t, restarted, fx.agent, fx.token, path, body)
	if retry.Code != first.Code || !bytes.Equal(retry.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("retry changed original receipt")
	}
	var snap snapshot
	decodeJSON(t, fx.get(t, restarted, fx.person, ""), &snap)
	states := map[string]string{}
	for _, d := range snap.Drafts {
		states[d.ID] = d.Status
	}
	if states[old.ID] != "superseded" || states[next.ID] != "accepted" {
		t.Fatalf("snapshot states %+v", states)
	}
	if count(t, database, `SELECT count(*) FROM intake_draft_replacements`) != 1 || count(t, database, `SELECT count(*) FROM events WHERE type='intake.draft_superseded'`) != 1 {
		t.Fatal("duplicate replacement effect")
	}
	// Immutable receipt and RLS hold even for the schema-owning app role.
	err := db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE intake_draft_replacements SET operation_key='changed'`)
		return err
	})
	if err == nil {
		t.Fatal("receipt was mutable")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), database.App, fx.tenantB, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM intake_draft_replacements`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("cross-tenant receipt visible")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(db.NoProjects(t.Context(), "replacement project visibility test"), database.App, fx.tenantA, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM intake_draft_replacements`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("receipt visible without project binding")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplacementConcurrency(t *testing.T) {
	database, fx, mux, source := replacementFixture(t)
	for _, mode := range []string{"accept-replace", "replace-replace", "exact-retries"} {
		t.Run(mode, func(t *testing.T) {
			for n := 0; n < 4; n++ {
				key := fmt.Sprintf("%s-%d", mode, n)
				old := fx.mustDraft(t, mux, fx.brief(source.ID, "", 0, key+"-old", "Old", "Original"))
				path := "/intake/drafts/" + old.ID + "/replace"
				body := fx.brief(source.ID, "", 0, key+"-new", "New", "Replacement")
				start := make(chan struct{})
				results := make(chan *httptest.ResponseRecorder, 2)
				go func() { <-start; results <- fx.post(t, mux, fx.agent, fx.token, path, body) }()
				go func() {
					<-start
					switch mode {
					case "accept-replace":
						results <- fx.post(t, mux, fx.person, "", "/intake/drafts/"+old.ID+"/accept", `{"expected_base_event_id":0}`)
					case "replace-replace":
						results <- fx.post(t, mux, fx.agent, fx.token, path, fx.brief(source.ID, "", 0, key+"-other", "Other", "Competing"))
					default:
						results <- fx.post(t, mux, fx.agent, fx.token, path, body)
					}
				}()
				close(start)
				a, b := <-results, <-results
				if mode == "exact-retries" {
					if a.Code != 201 || b.Code != 201 || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
						t.Fatalf("retry race %d/%d %s %s", a.Code, b.Code, a.Body.String(), b.Body.String())
					}
				} else {
					if a.Code == 409 {
						a, b = b, a
					}
					if a.Code != 200 && a.Code != 201 {
						t.Fatalf("no winner: %d %s", a.Code, a.Body.String())
					}
					code := "draft_superseded"
					if a.Code == 200 {
						code = "already_accepted"
					}
					assertErrorCode(t, b, 409, code)
				}
				var accepted, replaced int
				if err := database.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM intake_draft_acceptances WHERE draft_id=$1::uuid),(SELECT count(*) FROM intake_draft_replacements WHERE old_draft_id=$1::uuid)`, old.ID).Scan(&accepted, &replaced); err != nil {
					t.Fatal(err)
				}
				if accepted+replaced != 1 {
					t.Fatalf("terminal arbitration violated: accepted=%d replaced=%d", accepted, replaced)
				}
			}
		})
	}
}

func TestDelegatedIntakeAgainstGenerationDouble(t *testing.T) {
	database := dbtest.Open(t)
	fx := newFixture(t, database)
	keys, c, a := delegatedFixture(t)
	c.TenantID = fx.tenantA
	c.ProjectID = fx.projectA
	c.Subject = fx.agent.ID
	c.Actor = &tokens.Actor{Subject: fx.person.ID}
	a.TenantID = c.TenantID
	a.ProjectID = c.ProjectID
	a.PluginPrincipalID = c.Subject
	a.RequesterPrincipalID = c.Actor.Subject
	a.Grant.TenantID = c.TenantID
	a.Grant.ProjectID = c.ProjectID
	a.Grant.PluginPrincipalID = c.Subject
	a.Grant.RequesterPrincipalID = c.Actor.Subject
	store := &authorityDouble{state: a}
	m := NewDelegated(database.App, keys, store)
	m.clock = func() time.Time { return time.Unix(authNow, 0) }
	mux := http.NewServeMux()
	m.Mount(mux)
	mint := func(claims tokens.Claims) string {
		t.Helper()
		token, err := keys.MintDelegated(t.Context(), claims)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := mint(c)
	post := func(path, body string) *httptest.ResponseRecorder {
		return fx.post(t, mux, tenant.Principal{}, token, path, body)
	}
	first := post("/intake/sources", sourceBody("conversation", "Conversation", "delegated-source", ""))
	if first.Code != 201 {
		t.Fatalf("delegated source: %d %s", first.Code, first.Body.String())
	}
	var source sourceView
	decodeJSON(t, first, &source)
	if w := post("/intake/transcript-turns", turnBody(source.ID, 0, "person", fx.person.ID, "Person evidence", "delegated-turn")); w.Code != 201 {
		t.Fatalf("turn: %d %s", w.Code, w.Body.String())
	}
	body := fx.brief(source.ID, "", 0, "delegated-draft", "Draft", "Person request")
	first = post("/intake/drafts", body)
	if first.Code != 201 {
		t.Fatalf("draft: %d %s", first.Code, first.Body.String())
	}
	var old draftView
	decodeJSON(t, first, &old)
	if old.RequesterPrincipalID == nil || *old.RequesterPrincipalID != fx.person.ID {
		t.Fatal("requester not returned")
	}
	var requester, plugin string
	if err := database.Admin.QueryRow(t.Context(), `SELECT requester_principal_id::text, proposed_by_principal_id::text FROM intake_drafts WHERE id=$1::uuid`, old.ID).Scan(&requester, &plugin); err != nil {
		t.Fatal(err)
	}
	if requester != fx.person.ID || plugin != fx.agent.ID {
		t.Fatal("requester and author conflated")
	}
	if count(t, database, `SELECT count(*) FROM agent_permission_grants`) != 0 {
		t.Fatal("ephemeral grant persisted")
	}
	spoof := strings.Replace(body, `"idempotency_key":`, fmt.Sprintf(`"requester_principal_id":%q,"idempotency_key":`, fx.agentB.ID), 1)
	assertErrorCode(t, post("/intake/drafts", spoof), 403, "forbidden")
	replaceBody := fx.brief(source.ID, "", 0, "delegated-replace", "Revised", "New text")
	replacePath := "/intake/drafts/" + old.ID + "/replace"
	replaced := post(replacePath, replaceBody)
	if replaced.Code != 201 {
		t.Fatalf("replace %d %s", replaced.Code, replaced.Body.String())
	}
	routes := []struct{ method, path, body string }{
		{"POST", "/intake/sources", sourceBody("note", "Evidence", "new-source", "")},
		{"POST", "/intake/transcript-turns", turnBody(source.ID, 1, "person", fx.person.ID, "Evidence", "new-turn")},
		{"POST", "/intake/drafts", fx.brief(source.ID, "", 0, "new-draft", "Draft", "Evidence")},
		{"POST", replacePath, replaceBody}, {"GET", "/intake", ""},
	}
	for _, condition := range []string{"generation", "epoch", "tombstone", "grant", "missing", "unavailable"} {
		t.Run(condition, func(t *testing.T) {
			store.state = a
			store.err = nil
			switch condition {
			case "generation":
				store.state.Generation++
			case "epoch":
				store.state.AuthEpoch++
			case "tombstone":
				store.state.Revoked = true
			case "grant":
				store.state.Grant = nil
			case "missing":
				store.err = ErrAuthorityNotFound
			case "unavailable":
				store.err = fmt.Errorf("offline")
			}
			for _, route := range routes {
				w := call(mux, tenant.Principal{}, token, route.method, "/api/projects/"+fx.projectA+route.path, route.body)
				if route.method == "GET" && (condition == "generation" || condition == "grant") {
					if w.Code != 200 {
						t.Fatalf("read incorrectly fenced %d %s", w.Code, w.Body.String())
					}
					continue
				}
				status, code := 409, "revoked"
				switch condition {
				case "generation":
					code = "fenced_generation"
				case "grant", "missing":
					status = 403
					code = "forbidden"
				case "unavailable":
					status = 503
					code = "unavailable"
				}
				assertErrorCode(t, w, status, code)
			}
		})
	}
	store.state = a
	store.err = nil
	store.state.Generation++
	g := *a.Grant
	g.Generation = store.state.Generation
	store.state.Grant = &g
	c.Generation = store.state.Generation
	token = mint(c)
	retry := post(replacePath, replaceBody)
	if retry.Code != 201 || !bytes.Equal(retry.Body.Bytes(), replaced.Body.Bytes()) {
		t.Fatal("current generation cannot replay original payload")
	}
	assertErrorCode(t, post("/intake/drafts/"+old.ID+"/accept", `{"expected_base_event_id":0}`), 403, "forbidden")
	// Wrong scope is refused before any tenant lookup or mutation.
	foreign := c
	foreign.ProjectID = fx.projectB
	token = mint(foreign)
	assertErrorCode(t, post("/intake/sources", routes[0].body), 403, "forbidden")
}

func TestTargetRefusalCodes(t *testing.T) {
	database, fx, mux, source := replacementFixture(t)
	personTarget, personBase := fx.nodeWithEvent(t, database, "memory", "MEM-1", "Person", "Original", fx.person.ID)
	personDraft := fx.mustDraft(t, mux, fx.brief(source.ID, personTarget, personBase, "person-target", "Changed", "Rewrite"))
	assertErrorCode(t, fx.post(t, mux, fx.person, "", "/intake/drafts/"+personDraft.ID+"/accept", `{"expected_base_event_id":`+formatInt(personBase)+`}`), 409, "person_authored")
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, "/intake/drafts", fx.brief(source.ID, personTarget, personBase+99, "stale-target", "Changed", "Rewrite")), 409, "target_changed")
	// No content event is an independent refusal, distinct from base drift.
	var target string
	if err := database.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,body,parent_id) SELECT $1::uuid,'MEM-2',id,'Missing','Text',$2::uuid FROM node_kinds WHERE tenant_id=$1::uuid AND slug='memory' RETURNING id::text`, fx.tenantA, fx.projectA).Scan(&target); err != nil {
		t.Fatal(err)
	}
	var event int64
	if err := database.Admin.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1::uuid,$2::uuid,$3::uuid,'intake.source_recorded','{}') RETURNING id`, fx.tenantA, fx.agent.ID, target).Scan(&event); err != nil {
		t.Fatal(err)
	}
	missing := fx.mustDraft(t, mux, fx.brief(source.ID, target, event, "missing-target", "Changed", "Rewrite"))
	assertErrorCode(t, fx.post(t, mux, fx.person, "", "/intake/drafts/"+missing.ID+"/accept", `{"expected_base_event_id":`+formatInt(event)+`}`), 409, "no_recorded_content")
	agentTarget, base := fx.nodeWithEvent(t, database, "memory", "MEM-3", "Agent", "Original", fx.agent.ID)
	one := fx.mustDraft(t, mux, fx.brief(source.ID, agentTarget, base, "first-target", "Changed", "Rewrite"))
	two := fx.mustDraft(t, mux, fx.brief(source.ID, agentTarget, base, "second-target", "Other", "Other"))
	if w := fx.post(t, mux, fx.person, "", "/intake/drafts/"+one.ID+"/accept", `{"expected_base_event_id":`+formatInt(base)+`}`); w.Code != 200 {
		t.Fatalf("accept %d %s", w.Code, w.Body.String())
	}
	assertErrorCode(t, fx.post(t, mux, fx.person, "", "/intake/drafts/"+two.ID+"/accept", `{"expected_base_event_id":`+formatInt(base)+`}`), 409, "already_accepted_on_target")
}

func TestReplacementTransportRefusals(t *testing.T) {
	_, fx, mux, source := replacementFixture(t)
	old := fx.mustDraft(t, mux, fx.brief(source.ID, "", 0, "transport-old", "Old", "Original"))
	path := "/intake/drafts/" + old.ID + "/replace"
	for _, tt := range []struct {
		name, body string
		p          tenant.Principal
		token      string
		status     int
		code       string
	}{
		{"anonymous", "", tenant.Principal{}, "", 401, "unauthenticated"},
		{"person", "", fx.person, "", 403, "forbidden"},
		{"malformed", "{", fx.agent, fx.token, 400, "invalid_request"},
		{"oversized", strings.Repeat(" ", 1<<20) + "{}", fx.agent, fx.token, 413, "too_large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorCode(t, fx.post(t, mux, tt.p, tt.token, path, tt.body), tt.status, tt.code)
		})
	}
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, "/intake/drafts/10000000-0000-4000-8000-000000000099/replace", fx.brief(source.ID, "", 0, "unknown-old", "New", "Text")), 404, "not_found")
	// Integer limits are enforced before a lossy database conversion.
	assertErrorCode(t, fx.post(t, mux, fx.agent, fx.token, path, fx.brief(source.ID, fx.projectA, tokens.MaxSafeInteger+1, "unsafe-base", "New", "Text")), 400, "invalid_request")
}
