// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const handlerWorkerLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestHarnessOpenAPIRequiresWorkerLeaseOnEveryWorkerMutation(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, `"$ref": "#/components/parameters/HarnessWorkerLease"`) != 8 {
		t.Fatal("worker lease header is not attached to exactly the eight worker mutation routes")
	}
	if strings.Count(text, `"description": "Uniform non-enumerating worker authorization failure"`) != 8 {
		t.Fatal("worker mutation routes do not share one non-enumerating authorization contract")
	}
	for _, fragment := range []string{
		`"worker_lease": {"type": "string", "writeOnly": true`,
		`"name": "X-Paimos-Harness-Worker-Lease"`,
		`"required": ["agent_name", "harness", "host", "harness_session_ref", "worker_lease"`,
		`"HarnessControlOutcome"`,
		`"summary": "Get one scoped typed control outcome"`,
		`"description": "Uniform non-enumerating worker authorization failure"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("OpenAPI missing %s", fragment)
		}
	}
}

func TestHarnessOpenAPIKeepsSessionAndTicketIdentitiesDistinct(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	harness := contract.Components.Schemas["HarnessSession"].Properties
	product := contract.Components.Schemas["ProductSession"].Properties
	for _, field := range []string{"id", "parent_harness_session_id", "ticket_id"} {
		if _, ok := harness[field]; !ok {
			t.Fatalf("HarnessSession missing distinct %s field", field)
		}
	}
	for _, foreign := range []string{"product_session_id", "attribution_session_id"} {
		if _, ok := harness[foreign]; ok {
			t.Fatalf("HarnessSession overloads foreign identity %s", foreign)
		}
	}
	if _, ok := product["product_session_id"]; !ok {
		t.Fatal("ProductSession lost its dedicated product_session_id")
	}
	for _, foreign := range []string{"parent_harness_session_id", "ticket_id"} {
		if _, ok := product[foreign]; ok {
			t.Fatalf("ProductSession overloads harness binding %s", foreign)
		}
	}
}

func TestHarnessSessionRoutesAreProjectScopedAndDistinct(t *testing.T) {
	router := chi.NewRouter()
	RegisterHarnessSessionRoutes(router)
	want := map[string]bool{
		"GET /projects/{id}/harness-sessions":                                            false,
		"GET /projects/{id}/harness-sessions/orchestrator":                               false,
		"POST /projects/{id}/harness-sessions":                                           false,
		"GET /projects/{id}/harness-sessions/{sessionID}":                                false,
		"PATCH /projects/{id}/harness-sessions/{sessionID}/binding":                      false,
		"POST /projects/{id}/harness-sessions/{sessionID}/heartbeat":                     false,
		"POST /projects/{id}/harness-sessions/{sessionID}/yield":                         false,
		"POST /projects/{id}/harness-sessions/{sessionID}/drain":                         false,
		"POST /projects/{id}/harness-sessions/{sessionID}/complete-delivery":             false,
		"POST /projects/{id}/harness-sessions/{sessionID}/drain-steer":                   false,
		"POST /projects/{id}/harness-sessions/{sessionID}/complete-steer":                false,
		"POST /projects/{id}/harness-sessions/{sessionID}/controls/{kind}":               false,
		"GET /projects/{id}/harness-sessions/{sessionID}/controls/{controlID}":           false,
		"POST /projects/{id}/harness-sessions/{sessionID}/controls/{controlID}/complete": false,
		"POST /projects/{id}/harness-sessions/{sessionID}/stop":                          false,
	}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, ok := want[key]; ok {
			want[key] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route, found := range want {
		if !found {
			t.Errorf("missing %s", route)
		}
	}
}

func TestHarnessSteerCompatibilityDrainCompletesOlderSimpleFIFO(t *testing.T) {
	t.Setenv("PAIMOS_SECRET_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	openChangesTestDB(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness handler','HSH')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	for _, name := range []string{"worker", "sender"} {
		if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	session, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "handler-thread-ref", WorkerLease: handlerWorkerLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := agentmessage.NewService(db.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	simple, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "simple first", DeliveryLevel: "simple"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "steer second", DeliveryLevel: "steer"}); err != nil {
		t.Fatal(err)
	}

	request := func(method, suffix string, body []byte) *http.Request {
		req := httptest.NewRequest(method, "/projects/"+strconv.FormatInt(projectID, 10)+"/harness-sessions/"+session.ID+suffix, bytes.NewReader(body))
		req.Header.Set(AgentNameHeader, session.AgentName)
		req.Header.Set(harnessWorkerLeaseHeader, handlerWorkerLease)
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatInt(projectID, 10))
		route.URLParams.Add("sessionID", session.ID)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	}
	drainRecorder := httptest.NewRecorder()
	drainHarnessSteer(drainRecorder, request(http.MethodPost, "/drain-steer", []byte(`{}`)))
	if drainRecorder.Code != http.StatusOK {
		t.Fatalf("drain status=%d body=%s", drainRecorder.Code, drainRecorder.Body.String())
	}
	var page agentmessage.InboxPage
	if err := json.Unmarshal(drainRecorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Cursor != simple.Cursor || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.RequestedLevel != "simple" {
		t.Fatalf("steer compatibility drain skipped older simple work: %#v", page)
	}
	work := page.Messages[0].DeliveryWork
	if work.TargetRef != "" {
		t.Fatal("private target reference escaped the harness drain response")
	}
	payload, _ := json.Marshal(completeSteerRequest{Cursor: simple.Cursor, DeliveryID: work.DeliveryID, EffectiveLevel: "simple"})
	completeRecorder := httptest.NewRecorder()
	completeHarnessSteer(completeRecorder, request(http.MethodPost, "/complete-steer", payload))
	if completeRecorder.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeRecorder.Code, completeRecorder.Body.String())
	}

	nextRecorder := httptest.NewRecorder()
	drainHarnessSteer(nextRecorder, request(http.MethodPost, "/drain-steer", []byte(`{}`)))
	if nextRecorder.Code != http.StatusOK {
		t.Fatalf("next drain status=%d body=%s", nextRecorder.Code, nextRecorder.Body.String())
	}
	if err := json.Unmarshal(nextRecorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.RequestedLevel != "steer" {
		t.Fatalf("later steer remained FIFO-blocked: %#v", page)
	}
}

func TestHarnessWorkerLeaseRejectsSpoofMissingDuplicateAndCrossSessionProof(t *testing.T) {
	t.Setenv("PAIMOS_SECRET_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	openChangesTestDB(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness auth','HSA')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	user, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('harness-auth','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	for _, name := range []string{"worker", "other"} {
		if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}
	service := managedharness.NewService(db.DB)
	session, _, err := service.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "host-a", SessionRef: "ref-a", WorkerLease: handlerWorkerLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherLease := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	other, _, err := service.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "other", Harness: "codex", Host: "host-b", SessionRef: "ref-b", WorkerLease: otherLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	request := func(sessionID, agent string, leases []string, suffix string, body []byte) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/projects/1/harness-sessions/"+sessionID+suffix, bytes.NewReader(body))
		req.Header.Set(AgentNameHeader, agent)
		for _, lease := range leases {
			req.Header.Add(harnessWorkerLeaseHeader, lease)
		}
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatInt(projectID, 10))
		route.URLParams.Add("sessionID", sessionID)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	}
	for name, leases := range map[string][]string{
		"missing":   nil,
		"wrong":     {otherLease},
		"duplicate": {handlerWorkerLease, handlerWorkerLease},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			heartbeatHarnessSession(recorder, request(session.ID, "worker", leases, "/heartbeat", []byte(`{"phase":"working"}`)))
			if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "harness_session_worker_authorization_failed") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	wrongAgent := httptest.NewRecorder()
	heartbeatHarnessSession(wrongAgent, request(session.ID, "other", []string{handlerWorkerLease}, "/heartbeat", []byte(`{"phase":"working"}`)))
	if wrongAgent.Code != http.StatusForbidden || !strings.Contains(wrongAgent.Body.String(), "harness_session_worker_authorization_failed") {
		t.Fatalf("right lease with spoofed agent status=%d body=%s", wrongAgent.Code, wrongAgent.Body.String())
	}
	exact := httptest.NewRecorder()
	heartbeatHarnessSession(exact, request(session.ID, "worker", []string{handlerWorkerLease}, "/heartbeat", []byte(`{"phase":"working"}`)))
	if exact.Code != http.StatusOK {
		t.Fatalf("exact owner status=%d body=%s", exact.Code, exact.Body.String())
	}
	control, err := service.RequestControl(context.Background(), session.ID, managedharness.ControlInterrupt, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Yield(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	completionRequest := request(session.ID, "worker", nil, "/controls/"+control.ID+"/complete", []byte(`{"outcome":"applied","reason":"applied"}`))
	route := chi.RouteContext(completionRequest.Context())
	route.URLParams.Add("controlID", control.ID)
	denied := httptest.NewRecorder()
	completeHarnessControl(denied, completionRequest)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("receipt-less completion status=%d", denied.Code)
	}
	var state string
	if err := db.DB.QueryRow(`SELECT state FROM harness_session_controls WHERE id=?`, control.ID).Scan(&state); err != nil || state != managedharness.ControlClaimed {
		t.Fatalf("unauthorized completion mutated state=%q err=%v", state, err)
	}
	emptyStop := httptest.NewRecorder()
	stopHarnessSession(emptyStop, request(session.ID, "worker", []string{handlerWorkerLease}, "/stop", nil))
	if emptyStop.Code != http.StatusOK || !strings.Contains(emptyStop.Body.String(), `"closed_reason":"stopped"`) {
		t.Fatalf("empty-body stop status=%d body=%s", emptyStop.Code, emptyStop.Body.String())
	}
}

func TestHarnessWorkerMutationsUseUniformNonEnumeratingAuthorization(t *testing.T) {
	t.Setenv("PAIMOS_SECRET_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	openChangesTestDB(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness uniform auth','HUA')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	otherProject, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness other auth','HOB')`)
	if err != nil {
		t.Fatal(err)
	}
	otherProjectID, _ := otherProject.LastInsertId()
	user, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('harness-uniform','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	for _, pair := range []struct {
		projectID int64
		name      string
	}{{projectID, "worker"}, {projectID, "other"}, {otherProjectID, "foreign"}} {
		if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, pair.projectID, pair.name); err != nil {
			t.Fatal(err)
		}
	}
	service := managedharness.NewService(db.DB)
	session, _, err := service.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "uniform-host", SessionRef: "uniform-ref", WorkerLease: handlerWorkerLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherLease := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	other, _, err := service.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "other", Harness: "codex", Host: "other-host", SessionRef: "other-ref", WorkerLease: otherLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := service.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: otherProjectID, AgentName: "foreign", Harness: "codex", Host: "foreign-host", SessionRef: "foreign-ref", WorkerLease: otherLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := service.RequestControl(context.Background(), session.ID, managedharness.ControlInterrupt, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Yield(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}

	type mutation struct {
		name    string
		handler http.HandlerFunc
		body    string
	}
	mutations := []mutation{
		{"heartbeat", heartbeatHarnessSession, `{"phase":"working"}`},
		{"yield", yieldHarnessSession, `{}`},
		{"drain", drainHarnessDeliveries, `{}`},
		{"complete-delivery", completeHarnessDelivery, `{}`},
		{"drain-steer", drainHarnessSteer, `{}`},
		{"complete-steer", completeHarnessSteer, `{}`},
		{"complete-control", completeHarnessControl, `{"outcome":"applied","reason":"applied"}`},
		{"stop", stopHarnessSession, `{}`},
	}
	type denial struct {
		name      string
		sessionID string
		agent     string
		leases    []string
	}
	denials := []denial{
		{"missing-session", "", "worker", []string{handlerWorkerLease}},
		{"nonexistent-session", uuid.NewString(), "worker", []string{handlerWorkerLease}},
		{"wrong-project-session", foreign.ID, "foreign", []string{otherLease}},
		{"missing-lease", session.ID, "worker", nil},
		{"duplicate-lease", session.ID, "worker", []string{handlerWorkerLease, handlerWorkerLease}},
		{"wrong-generation-lease", session.ID, "worker", []string{otherLease}},
		{"spoofed-agent", session.ID, "other", []string{handlerWorkerLease}},
		{"cross-session-proof", other.ID, "other", []string{handlerWorkerLease}},
	}
	request := func(d denial, body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/projects/1/harness-sessions/"+d.sessionID, strings.NewReader(body))
		if d.agent != "" {
			req.Header.Set(AgentNameHeader, d.agent)
		}
		for _, lease := range d.leases {
			req.Header.Add(harnessWorkerLeaseHeader, lease)
		}
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatInt(projectID, 10))
		route.URLParams.Add("sessionID", d.sessionID)
		route.URLParams.Add("controlID", control.ID)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	}
	var expectedBody string
	for _, mutation := range mutations {
		for _, denial := range denials {
			t.Run(mutation.name+"/"+denial.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				mutation.handler(recorder, request(denial, mutation.body))
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if expectedBody == "" {
					expectedBody = recorder.Body.String()
				}
				if recorder.Body.String() != expectedBody || !strings.Contains(expectedBody, "harness_session_worker_authorization_failed") {
					t.Fatalf("non-uniform authorization body=%s want=%s", recorder.Body.String(), expectedBody)
				}
			})
		}
	}
	var phase, controlState string
	var revision, yieldSequence int64
	if err := db.DB.QueryRow(`SELECT phase,revision,yield_sequence FROM harness_sessions WHERE id=?`, session.ID).Scan(&phase, &revision, &yieldSequence); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT state FROM harness_session_controls WHERE id=?`, control.ID).Scan(&controlState); err != nil {
		t.Fatal(err)
	}
	if phase != managedharness.PhaseYielded || revision != 2 || yieldSequence != 1 || controlState != managedharness.ControlClaimed {
		t.Fatalf("denied worker mutation changed state: phase=%s revision=%d yield=%d control=%s", phase, revision, yieldSequence, controlState)
	}
}

func TestGetHarnessControlReturnsScopedNonSecretOutcome(t *testing.T) {
	t.Setenv("PAIMOS_SECRET_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	openChangesTestDB(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness outcome','HCO')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	user, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('harness-outcome','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	service := managedharness.NewService(db.DB)
	session, _, err := service.Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "outcome-host", SessionRef: "private-outcome-ref", WorkerLease: handlerWorkerLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := service.RequestControl(context.Background(), session.ID, managedharness.ControlInterrupt, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Yield(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteControl(context.Background(), session.ID, control.ID, managedharness.ControlApplied, managedharness.ReasonApplied); err != nil {
		t.Fatal(err)
	}
	request := func(project int64, sessionID, controlID string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/projects/1/harness-sessions/"+sessionID+"/controls/"+controlID, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatInt(project, 10))
		route.URLParams.Add("sessionID", sessionID)
		route.URLParams.Add("controlID", controlID)
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	}
	recorder := httptest.NewRecorder()
	getHarnessControl(recorder, request(projectID, session.ID, control.ID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var outcome models.HarnessControlOutcome
	if err := json.Unmarshal(recorder.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.ProjectID != projectID || outcome.HarnessSessionID != session.ID || outcome.ID != control.ID ||
		outcome.CorrelationID != control.ID || outcome.Kind != managedharness.ControlInterrupt ||
		outcome.State != managedharness.ControlApplied || outcome.Outcome != managedharness.ControlApplied ||
		outcome.Reason != managedharness.ReasonApplied || outcome.RequestedAt == "" || outcome.ClaimedAt == "" || outcome.CompletedAt == "" {
		t.Fatalf("outcome=%+v", outcome)
	}
	for _, secret := range []string{"private-outcome-ref", handlerWorkerLease, "message_target_id", "requested_by_user_id"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, recorder.Body.String())
		}
	}
	for name, req := range map[string]*http.Request{
		"wrong-project": request(projectID+1, session.ID, control.ID),
		"wrong-session": request(projectID, uuid.NewString(), control.ID),
		"wrong-control": request(projectID, session.ID, uuid.NewString()),
	} {
		t.Run(name, func(t *testing.T) {
			denied := httptest.NewRecorder()
			getHarnessControl(denied, req)
			if denied.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", denied.Code, denied.Body.String())
			}
		})
	}
}
