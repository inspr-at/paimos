// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

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
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "handler-thread-ref",
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
