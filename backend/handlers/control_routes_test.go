// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/supervision"
	_ "modernc.org/sqlite"
)

func TestControlRouteMountsOnlyFrozenMethodsAndFamilies(t *testing.T) {
	router := chi.NewRouter()
	router.Route("/api", func(router chi.Router) {
		router.Route("/agent-mode", MountAgentModeControlRoutes)
		MountRunnerControlRoutes(router)
	})
	var got []string
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{
		"GET /api/agent-mode/control-capability-grants/{controlID}",
		"GET /api/agent-mode/control-commands/{controlID}",
		"POST /api/agent-mode/control-capability-grants/{controlID}",
		"POST /api/agent-mode/control-commands/{controlID}",
		"POST /api/agent-mode/deliveries/{deliveryKey}/control-capability-grants",
		"POST /api/agent-mode/deliveries/{deliveryKey}/control-commands",
		"POST /api/control-capability-leases/{controlID}",
		"POST /api/control-commands/{controlID}",
		"POST /api/runs/{id}/control-capability-leases",
		"POST /api/runs/{id}/control-commands",
		"POST /api/runs/{id}/input-requests",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounted routes=%v want=%v", got, want)
	}
}

func TestControlAwareRouterRefusalsStayPrivateAndClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "not found", method: http.MethodPost, path: "/api/control-capability-leases/opaque", status: http.StatusNotFound},
		{name: "method", method: http.MethodDelete, path: "/api/control-commands/opaque", status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			router.NotFound(ControlAwareNotFound)
			router.MethodNotAllowed(ControlAwareMethodNotAllowed)
			router.Post("/api/control-commands/{controlID}", func(http.ResponseWriter, *http.Request) {})
			request := httptest.NewRequest(tc.method, tc.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.status, recorder.Body.String())
			}
			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control=%q", got)
			}
			if strings.Contains(recorder.Body.String(), "opaque") || strings.Contains(recorder.Body.String(), `"instance"`) {
				t.Fatalf("refusal leaked resource identity: %s", recorder.Body.String())
			}
			if recorder.Header().Get(RequestIDHeader) == "" {
				t.Fatal("refusal omitted server request id")
			}
		})
	}
}

func TestCommandCreateBodyDiscriminatorIsClosed(t *testing.T) {
	valid := []struct {
		action supervision.Action
		body   commandCreateBody
	}{
		{action: "issue.priority.set", body: commandCreateBody{Priority: "high"}},
		{action: "run.cancel.queued", body: commandCreateBody{RunID: json.Number("17")}},
		{action: "run.cancel.running", body: commandCreateBody{RunID: json.Number("17")}},
		{action: "input.respond", body: commandCreateBody{InputRequestID: "opaque", InputRequestRevision: json.Number("1"), InputResponse: "approve"}},
		{action: "input.respond", body: commandCreateBody{InputRequestID: "opaque", InputRequestRevision: json.Number("1"), InputResponse: "choice", ChoiceOrdinal: json.Number("2")}},
		{action: "run.pause", body: commandCreateBody{RunID: json.Number("17"), RuntimeRevision: json.Number("3")}},
		{action: "run.resume", body: commandCreateBody{RunID: json.Number("17"), RuntimeRevision: json.Number("3")}},
	}
	for _, tc := range valid {
		if err := validateCommandCreateBody(tc.body, tc.action); err != nil {
			t.Fatalf("action=%s valid body rejected: %v", tc.action, err)
		}
	}
	invalid := []struct {
		action supervision.Action
		body   commandCreateBody
	}{
		{action: "issue.priority.set", body: commandCreateBody{Priority: "high", RunID: json.Number("17")}},
		{action: "run.cancel.running", body: commandCreateBody{}},
		{action: "input.respond", body: commandCreateBody{InputRequestID: "opaque", InputRequestRevision: json.Number("1"), InputResponse: "approve", ChoiceOrdinal: json.Number("1")}},
		{action: "run.pause", body: commandCreateBody{RunID: json.Number("17")}},
	}
	for _, tc := range invalid {
		if err := validateCommandCreateBody(tc.body, tc.action); err == nil {
			t.Fatalf("action=%s invalid body accepted: %+v", tc.action, tc.body)
		}
	}
}

func TestCommandDTOAlwaysCarriesSafeBindingLabels(t *testing.T) {
	value := supervision.CommandProjection{CommandID: "opaque", Action: "run.cancel.running", Status: "pending_confirmation",
		Display: supervision.DisplayValues{IssueKey: "PAI-809", DeliveryKey: "issue:809"}}
	raw, err := json.Marshal(commandDTO(value))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	display, ok := decoded["display"].(map[string]any)
	if !ok || display["issue_key"] != "PAI-809" || display["delivery_key"] != "issue:809" {
		t.Fatalf("command display=%v body=%s", decoded["display"], raw)
	}
}

func TestGrantDTOCarriesOnlyFrozenLiveTargetShapes(t *testing.T) {
	projection := supervision.GrantProjection{Actions: supervision.Actions(), Targets: []supervision.GrantTarget{
		{Action: "issue.priority.set"},
		{Action: "run.cancel.queued", RunID: 17},
		{Action: "run.cancel.running", RunID: 18},
		{Action: "input.respond", InputRequestID: "approval-opaque", InputRequestRevision: 2, InputKind: "approval"},
		{Action: "input.respond", InputRequestID: "choice-opaque", InputRequestRevision: 3, InputKind: "choice",
			OptionCodes: []string{"choice_1", "choice_2"}},
		{Action: "run.pause", RunID: 18, RuntimeState: "running", RuntimeRevision: 4},
		{Action: "run.resume", RunID: 18, RuntimeState: "paused", RuntimeRevision: 5},
	}}
	raw, err := json.Marshal(grantDTO(projection))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Targets []map[string]any `json:"targets"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	wantKeys := [][]string{
		{"action"},
		{"action", "run_id"},
		{"action", "run_id"},
		{"action", "input_kind", "input_request_id", "input_request_revision"},
		{"action", "input_kind", "input_request_id", "input_request_revision", "option_codes"},
		{"action", "run_id", "runtime_revision", "runtime_state"},
		{"action", "run_id", "runtime_revision", "runtime_state"},
	}
	if len(decoded.Targets) != len(wantKeys) {
		t.Fatalf("targets=%v body=%s", decoded.Targets, raw)
	}
	for index, target := range decoded.Targets {
		got := make([]string, 0, len(target))
		for key := range target {
			got = append(got, key)
		}
		sort.Strings(got)
		sort.Strings(wantKeys[index])
		if !reflect.DeepEqual(got, wantKeys[index]) {
			t.Fatalf("target %d keys=%v want=%v body=%s", index, got, wantKeys[index], raw)
		}
	}
	if got := decoded.Targets[4]["option_codes"]; !reflect.DeepEqual(got, []any{"choice_1", "choice_2"}) {
		t.Fatalf("choice option_codes=%v", got)
	}
	emptyRaw, err := json.Marshal(grantDTO(supervision.GrantProjection{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emptyRaw), `"targets":[]`) {
		t.Fatalf("empty grant omitted targets: %s", emptyRaw)
	}
}

func TestRunnerLeaseRevokeRequiresDeviceAndForbidsActions(t *testing.T) {
	for _, body := range []string{
		`{"operation":"revoke","revision":1}`,
		`{"operation":"revoke","revision":1,"device_id":"device-a","supported_actions":[]}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/control-capability-leases/opaque", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		recorder := httptest.NewRecorder()
		TransitionControlLease(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRunnerPullWrongDeviceIsPrivateAndHasNoEffect(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prior := db.DB
	db.DB = database
	t.Cleanup(func() { db.DB = prior })
	if _, err := database.Exec(`CREATE TABLE control_capability_leases(
		lease_id TEXT,revision INTEGER,agent_run_id INTEGER,user_id INTEGER,actor_api_key_id INTEGER,device_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_capability_leases VALUES(
		'lease-private-canary',1,17,5,11,'device-private-canary')`); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewAPIKeyPrincipal(11, 5, auth.ScopeSet{auth.ScopeAgentControlsRunner: {}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"lease_id":"lease-private-canary","lease_revision":1,"device_id":"wrong-private-canary","cursor":0}`
	request := httptest.NewRequest(http.MethodPost, "/api/runs/17/control-commands", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
	router := chi.NewRouter()
	router.Post("/api/runs/{id}/control-commands", PullControlCommands)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "private-canary") {
		t.Fatalf("wrong-device refusal leaked binding: %s", recorder.Body.String())
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM control_capability_leases`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lease facts changed count=%d err=%v", count, err)
	}
}
