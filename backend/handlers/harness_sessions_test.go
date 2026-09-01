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
	for _, fragment := range []string{`"worker_lease": {"type": "string", "writeOnly": true`, `"name": "X-Paimos-Harness-Worker-Lease"`, `"required": ["agent_name", "harness", "host", "harness_session_ref", "worker_lease"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("OpenAPI missing %s", fragment)
		}
	}
}

func TestHarnessSessionRoutesAreProjectScopedAndDistinct(t *testing.T) {
	router := chi.NewRouter()
	RegisterHarnessSessionRoutes(router)
	want := map[string]bool{
		"GET /projects/{id}/harness-sessions":                                            false,
		"POST /projects/{id}/harness-sessions":                                           false,
		"GET /projects/{id}/harness-sessions/{sessionID}":                                false,
		"POST /projects/{id}/harness-sessions/{sessionID}/heartbeat":                     false,
		"POST /projects/{id}/harness-sessions/{sessionID}/yield":                         false,
		"POST /projects/{id}/harness-sessions/{sessionID}/drain":                         false,
		"POST /projects/{id}/harness-sessions/{sessionID}/complete-delivery":             false,
		"POST /projects/{id}/harness-sessions/{sessionID}/drain-steer":                   false,
		"POST /projects/{id}/harness-sessions/{sessionID}/complete-steer":                false,
		"POST /projects/{id}/harness-sessions/{sessionID}/controls/{kind}":               false,
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
}
