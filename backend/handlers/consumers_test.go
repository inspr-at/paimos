// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestConsumerHTTPClosedWireAndAuthority(t *testing.T) {
	openChangesTestDB(t)
	res, e := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('consumer-http','disabled','admin','super_admin','active',1)`)
	if e != nil {
		t.Fatal(e)
	}
	user, _ := res.LastInsertId()
	project := seedChangesProject(t, "CNS")
	res, e = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture','not-a-credential','fixture','*')`, user)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := res.LastInsertId()
	p, _ := auth.NewAPIKeyPrincipal(key, user, auth.ParseScopes("*"))
	const proof = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	runtime, e := lifecycleintents.NewService(db.DB).RegisterRuntime(context.Background(), p, project, proof, lifecycleintents.Registration{Generation: uuid.NewString(), Host: "http-fixture", AccountLabel: "chatgpt", Workspaces: []lifecycleintents.Workspace{}, Profiles: []lifecycleintents.Profile{}})
	if e != nil {
		t.Fatal(e)
	}
	router := chi.NewRouter()
	RegisterConsumerRoutes(router)
	call := func(principal auth.Principal, path, body string, proofs ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/projects/%d/consumers/v1/%s", project, path), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for _, value := range proofs {
			r.Header.Add(lifecycleintents.RuntimeLeaseHeader, value)
		}
		r = r.WithContext(auth.WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("cacheable response")
		}
		return w
	}
	body := fmt.Sprintf(`{"runtime_id":%q,"runtime_generation":%q,"sequence":1,"layer":"reporter","state":"healthy","reason":"recovered","failure_count":0}`, runtime.ID, runtime.Generation)
	if out := call(p, "runtime-health", body, proof); out.Code != 200 {
		t.Fatalf("health %d %s", out.Code, out.Body.String())
	}
	for _, bad := range []string{`null`, body + `{}`, strings.TrimSuffix(body, "}") + `,"prompt":"fixture-private"}`, strings.Replace(body, `"sequence":1`, `"sequence":1.0`, 1), strings.TrimSuffix(body, "}") + `,"state":"healthy"}`, strings.Repeat("x", 4097)} {
		out := call(p, "runtime-health", bad, proof)
		if out.Code != 400 || strings.Contains(out.Body.String(), "fixture-private") {
			t.Fatalf("unsafe body %d", out.Code)
		}
	}
	for _, proofs := range [][]string{nil, {runtime.Generation}, {proof, proof}} {
		if out := call(p, "runtime-health", body, proofs...); out.Code != 403 {
			t.Fatalf("runtime proof accepted %d", out.Code)
		}
	}
	human, _ := auth.NewSessionPrincipal(uuid.NewString(), user, user, false)
	if out := call(human, "runtime-health", body, proof); out.Code != 403 {
		t.Fatal("human accepted on daemon route")
	}
	for _, id := range []string{runtime.ID, uuid.NewString()} {
		out := call(p, "streams/"+id+"/attempts/"+uuid.NewString()+"/execute", `{"expected_revision":1}`, proof)
		if out.Code != 403 || strings.TrimSpace(out.Body.String()) != `{"error":"consumer_unavailable"}` {
			t.Fatal("existence oracle")
		}
	}
}
