package handlers

import (
	"context"
	"encoding/json"
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

func TestLifecycleHTTPClosedBodiesAndCredentialSeparation(t *testing.T) {
	openChangesTestDB(t)
	res, e := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('lifecycle-http','disabled','admin','super_admin','active',1)`)
	if e != nil {
		t.Fatal(e)
	}
	user, _ := res.LastInsertId()
	project := seedChangesProject(t, "LIF")
	credential := uuid.NewString()
	if _, e = db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), user, credential); e != nil {
		t.Fatal(e)
	}
	human, _ := auth.NewSessionPrincipal(credential, user, user, false)
	res, e = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture','not-a-credential','fixture','*')`, user)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := res.LastInsertId()
	reporter, _ := auth.NewAPIKeyPrincipal(key, user, auth.ParseScopes("*"))
	runtime, e := lifecycleintents.NewService(db.DB).RegisterRuntime(context.Background(), reporter, project, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", lifecycleintents.Registration{Generation: uuid.NewString(), Host: "http-fixture", AccountLabel: "chatgpt", Profiles: []lifecycleintents.Profile{}, Workspaces: []lifecycleintents.Workspace{}})
	if e != nil {
		t.Fatal(e)
	}
	router := chi.NewRouter()
	RegisterLifecycleIntentRoutes(router)
	call := func(principal auth.Principal, method, suffix, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, fmt.Sprintf("/projects/%d/lifecycle/v1/%s", project, suffix), strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	valid := fmt.Sprintf(`{"request_key":%q,"operation":"repair","runtime_id":%q,"runtime_generation":%q,"account_label":"chatgpt","ttl_seconds":60,"repair_layer":"reporter"}`, uuid.NewString(), runtime.ID, runtime.Generation)
	for _, field := range []string{"argv", "shell", "prompt", "credentials", "target_ref", "workspace_path", "instance", "machine_id"} {
		body := strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,%q:"fixture-forbidden"}`, field)
		res := call(human, "POST", "intents", body)
		if res.Code != 400 || strings.Contains(res.Body.String(), "fixture-forbidden") {
			t.Fatalf("field %s status=%d", field, res.Code)
		}
	}
	for _, body := range []string{strings.TrimSuffix(valid, "}") + `,"operation":"start"}`, valid + `{}`, strings.Repeat("x", 8193), "null", strings.TrimSuffix(valid, "}") + `,"agent_name":null}`, strings.Replace(valid, `"ttl_seconds":60`, `"ttl_seconds":60.5`, 1), strings.Replace(valid, `"ttl_seconds":60`, `"ttl_seconds":601`, 1)} {
		if res := call(human, "POST", "intents", body); res.Code != 400 {
			t.Fatalf("malicious body status=%d", res.Code)
		}
	}
	if res := call(reporter, "POST", "intents", valid); res.Code != 403 {
		t.Fatal("API key accepted browser submit")
	}
	if res := call(human, "POST", "runtimes/"+runtime.ID+"/claim", `{}`); res.Code != 403 {
		t.Fatal("session accepted daemon claim")
	}
	response := call(human, "POST", "intents", valid)
	if response.Code != 201 {
		t.Fatalf("submit=%d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("response cacheable")
	}
	var in lifecycleintents.Intent
	if json.Unmarshal(response.Body.Bytes(), &in) != nil {
		t.Fatal("invalid response")
	}
	response = call(human, "GET", "intents/"+in.ID, "")
	if response.Code != 200 {
		t.Fatal("get failed")
	}
	for _, forbidden := range []string{"lease_digest", "session_credential_id", "api_key_id", "worker_lease", "target_ref"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("private field %s exposed", forbidden)
		}
	}
	response = call(human, "POST", "intents/"+in.ID+"/cancel", `{"expected_revision":1}`)
	if response.Code != 200 {
		t.Fatal("cancel failed")
	}
	response = call(human, "GET", "intents/"+in.ID+"/events", "")
	if response.Code != 200 {
		t.Fatal("events failed")
	}
	if _, e = db.DB.Exec(`UPDATE users SET role_key='admin',is_super_admin=0 WHERE id=?`, user); e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"runtimes", "intents/" + in.ID, "intents/" + uuid.NewString()} {
		response = call(human, "GET", suffix, "")
		if response.Code != 403 || strings.TrimSpace(response.Body.String()) != `{"error":"lifecycle_unavailable"}` {
			t.Fatal("unauthorized existence oracle")
		}
	}
}
