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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

func TestPrepareHarnessRetirementRetriesOutstandingLeasedDelivery(t *testing.T) {
	t.Setenv("PAIMOS_SECRET_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	openChangesTestDB(t)
	ctx := context.Background()

	projectResult, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Retirement readiness','HRR')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	for _, name := range []string{"worker", "sender"} {
		if _, err = db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
			t.Fatal(err)
		}
	}

	humanResult, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin)
		VALUES('retirement-human','disabled','admin','super_admin','active',1)`)
	if err != nil {
		t.Fatal(err)
	}
	humanID, _ := humanResult.LastInsertId()
	credentialID := uuid.NewString()
	if _, err = db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at)
		VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), humanID, credentialID); err != nil {
		t.Fatal(err)
	}
	human, err := auth.NewSessionPrincipal(credentialID, humanID, humanID, false)
	if err != nil {
		t.Fatal(err)
	}

	service := managedharness.NewService(db.DB)
	session, _, err := service.Register(ctx, managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "retirement-handler-host",
		SessionRef: uuid.NewString(), WorkerLease: handlerWorkerLease,
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker,
		SteerMode:    managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	reporterResult, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin)
		VALUES('retirement-handler-reporter','disabled','admin','super_admin','active',1)`)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ := reporterResult.LastInsertId()
	keyResult, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'retirement-handler','retirement-handler-hash','rhr','agent-controls:runner')`, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := keyResult.LastInsertId()
	runtimeID := uuid.NewString()
	if _, err = db.DB.Exec(`INSERT INTO lifecycle_runtimes(
		id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at)
		VALUES(?,?,?,?,?,?,zeroblob(32),'{}',strftime('%Y-%m-%dT%H:%M:%fZ','now','+10 minutes'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		runtimeID, projectID, uuid.NewString(), session.Host, reporterID, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, session.ID, runtimeID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	bus := agentmessage.NewService(db.DB)
	if err = bus.AllowSender(ctx, projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(ctx, agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "settle this accepted work", DeliveryLevel: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := bus.ListInbox(ctx, agentmessage.InboxInput{
		ProjectID: projectID, Address: managedharness.Address(session), Agent: session.AgentName,
		WorkerAdapter: agentmessage.AdapterManagedHarness, DeliveryLevel: "simple", TargetID: session.MessageTargetID, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("leased inbox page=%+v err=%v", page, err)
	}
	work := page.Messages[0].DeliveryWork

	requested, err := service.RequestRetirementCAS(ctx, human, projectID, session.ID, managedharness.BrowserControlRequest{
		ExpectedRevision: session.Revision, RequestKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Yield(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.HeartbeatWithActivity(ctx, session.ID, managedharness.PhaseWorking, managedharness.ActivityEvidence{
		Sequence: 1, Kind: managedharness.ActivityCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	reporter, err := auth.NewAPIKeyPrincipal(keyID, reporterID, auth.ScopeSet{auth.ScopeAgentControlsRunner: {}})
	if err != nil {
		t.Fatal(err)
	}

	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+strconv.FormatInt(projectID, 10)+"/harness-sessions/"+session.ID+"/retirements/"+requested.Retirement.ID+"/ready", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(AgentNameHeader, session.AgentName)
		req.Header.Set(harnessWorkerLeaseHeader, handlerWorkerLease)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatInt(projectID, 10))
		route.URLParams.Add("sessionID", session.ID)
		route.URLParams.Add("retirementID", requested.Retirement.ID)
		requestContext := context.WithValue(req.Context(), chi.RouteCtxKey, route)
		return req.WithContext(auth.WithPrincipal(requestContext, reporter))
	}

	notReady := httptest.NewRecorder()
	prepareHarnessRetirement(notReady, request())
	if notReady.Code != http.StatusConflict || !strings.Contains(notReady.Body.String(), `"code":"`+managedharness.CodeRetirementNotReady+`"`) {
		t.Fatalf("not-ready status=%d body=%s", notReady.Code, notReady.Body.String())
	}
	stillPending, err := service.GetRetirement(ctx, projectID, session.ID, requested.Retirement.ID)
	if err != nil || stillPending.State != "finishing" {
		t.Fatalf("not-ready response changed retirement: %+v err=%v", stillPending, err)
	}

	if _, err = bus.CompleteLocalDelivery(ctx, agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: managedharness.Address(session), Agent: session.AgentName,
		Cursor: message.Cursor, DeliveryID: work.DeliveryID, EffectiveLevel: "simple", TargetID: session.MessageTargetID,
	}); err != nil {
		t.Fatal(err)
	}
	ready := httptest.NewRecorder()
	prepareHarnessRetirement(ready, request())
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", ready.Code, ready.Body.String())
	}
	var claim models.HarnessRetirementClaim
	if err = json.Unmarshal(ready.Body.Bytes(), &claim); err != nil {
		t.Fatal(err)
	}
	if claim.ID != requested.Retirement.ID || claim.State != "stopping" {
		t.Fatalf("ready claim=%+v", claim)
	}
}
