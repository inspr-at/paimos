package managedharness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func openManagedHarnessTestDB(t *testing.T) (int64, int64) {
	t.Helper()
	previousDataDir := os.Getenv("DATA_DIR")
	previousTestMode := os.Getenv("PAIMOS_TEST_MODE")
	t.Cleanup(func() {
		_ = paimosdb.DB.Close()
		paimosdb.DB = nil
		_ = os.Setenv("DATA_DIR", previousDataDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", previousTestMode)
	})
	_ = os.Setenv("DATA_DIR", t.TempDir())
	_ = os.Setenv("PAIMOS_TEST_MODE", "1")
	_ = os.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := paimosdb.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('harness-actor','x','member','active')`); err != nil {
		t.Fatal(err)
	}
	project, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness test','HST')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID); err != nil {
		t.Fatal(err)
	}
	return projectID, agentID
}

func TestManagedSteerUsesCanonicalLeaseAndCompletion(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "private-thread-ref",
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "steer observation", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongPage, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName,
		WorkerAdapter: agentmessage.AdapterManagedHarness, DeliveryLevel: "steer", TargetID: "11111111-1111-4111-8111-111111111111", Limit: 10,
	})
	if err != nil || len(wrongPage.Messages) != 0 {
		t.Fatalf("wrong target leased work: page=%#v err=%v", wrongPage, err)
	}
	page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName,
		WorkerAdapter: agentmessage.AdapterManagedHarness, DeliveryLevel: "steer", TargetID: session.MessageTargetID, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("page=%#v", page)
	}
	work := page.Messages[0].DeliveryWork
	if work.TargetRef != "private-thread-ref" {
		t.Fatal("canonical worker did not decrypt the encrypted session ref")
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName, Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer", TargetID: "22222222-2222-4222-8222-222222222222",
	}); err == nil {
		t.Fatal("wrong target completed leased work")
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName, Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer", TargetID: session.MessageTargetID,
	}); err != nil {
		t.Fatal(err)
	}
	var state, requested, effective, fallback, handedOff string
	if err := paimosdb.DB.QueryRow(`SELECT state,requested_level,effective_level,fallback_reason,handed_off_at FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&state, &requested, &effective, &fallback, &handedOff); err != nil {
		t.Fatal(err)
	}
	if state != "handed_off" || requested != "steer" || effective != "steer" || fallback != "" || handedOff == "" {
		t.Fatalf("state=%s requested=%s effective=%s fallback=%s handed_off_at=%s", state, requested, effective, fallback, handedOff)
	}
}

func TestRegisterEnforcesManagedAndExternalSteerTruth(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	base := RegisterInput{ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0",
		SessionRef: "thread-1", ManagementMode: ManagementManaged, Role: RoleWorker,
		SteerMode: SteerOwned, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true}}
	session, created, err := service.Register(context.Background(), base)
	if err != nil || !created {
		t.Fatalf("register: session=%#v created=%v err=%v", session, created, err)
	}
	if session.AgentName != "worker" || session.ManagementMode != ManagementManaged || !session.Capabilities.Steer {
		t.Fatalf("session=%#v", session)
	}
	if _, created, err := service.Register(context.Background(), base); err != nil || created {
		t.Fatalf("exact registration replay: created=%v err=%v", created, err)
	}
	encoded, err := json.Marshal(session)
	if err != nil || strings.Contains(string(encoded), "thread-1") || strings.Contains(string(encoded), "session_ref") {
		t.Fatalf("private session reference escaped response: %s err=%v", encoded, err)
	}
	var digest []byte
	if err := paimosdb.DB.QueryRow(`SELECT session_ref_digest FROM harness_sessions WHERE id=?`, session.ID).Scan(&digest); err != nil || len(digest) != 32 {
		t.Fatalf("digest len=%d err=%v", len(digest), err)
	}
	if _, err := service.Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}

	unmanagedCodex := base
	unmanagedCodex.Host, unmanagedCodex.SessionRef = "mbp1", "thread-2"
	unmanagedCodex.ManagementMode, unmanagedCodex.SteerMode = ManagementUnmanaged, SteerCodexExternal
	unmanagedCodex.Capabilities.Interrupt, unmanagedCodex.Capabilities.Stop = false, false
	target, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "thread-2", MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	unmanagedCodex.MessageTargetID = target.ID
	unmanagedSession, _, err := service.Register(context.Background(), unmanagedCodex)
	if err != nil {
		t.Fatalf("documented Codex external steer rejected: %v", err)
	}
	if _, err := service.Stop(context.Background(), unmanagedSession.ID); err != nil {
		t.Fatal(err)
	}
	simpleTarget, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "thread-simple", MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	capped := unmanagedCodex
	capped.Host, capped.SessionRef, capped.MessageTargetID = "mbp-cap", "thread-simple", simpleTarget.ID
	if _, _, err := service.Register(context.Background(), capped); !IsCode(err, CodeCapabilityInvalid) {
		t.Fatalf("unmanaged steer exceeded target MaximumLevel: %v", err)
	}

	unmanagedClaude := unmanagedCodex
	unmanagedClaude.Harness, unmanagedClaude.Host, unmanagedClaude.SessionRef = "claude", "mbp2", "11111111-1111-4111-8111-111111111111"
	if _, _, err := service.Register(context.Background(), unmanagedClaude); !IsCode(err, CodeCapabilityInvalid) {
		t.Fatalf("unmanaged Claude steer error=%v", err)
	}

	privateSocket := base
	privateSocket.Host, privateSocket.SessionRef = "mbp3", "/tmp/private.sock"
	if _, _, err := service.Register(context.Background(), privateSocket); !IsCode(err, CodeInvalid) {
		t.Fatalf("private socket error=%v", err)
	}
}

func TestStoppedSessionCanRegisterNewActiveGeneration(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "stable-thread-ref",
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	}
	first, created, err := service.Register(context.Background(), input)
	if err != nil || !created {
		t.Fatalf("first register: created=%v err=%v", created, err)
	}
	if _, err := service.Heartbeat(context.Background(), first.ID, PhaseWorking); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stop(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	second, created, err := service.Register(context.Background(), input)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("replacement register: first=%s second=%s created=%v err=%v", first.ID, second.ID, created, err)
	}
	old, err := service.GetByID(context.Background(), first.ID)
	if err != nil || old.Phase != PhaseStopped {
		t.Fatalf("old session=%#v err=%v", old, err)
	}
	if _, err := service.Heartbeat(context.Background(), second.ID, PhaseWorking); err != nil {
		t.Fatalf("replacement heartbeat: %v", err)
	}
	yielded, err := service.Yield(context.Background(), second.ID)
	if err != nil || yielded.Session.ID != second.ID || yielded.Session.Phase != PhaseYielded || yielded.Session.YieldSequence != 1 {
		t.Fatalf("replacement yield=%#v err=%v", yielded, err)
	}

	const replays = 8
	var wg sync.WaitGroup
	results := make(chan models.HarnessSession, replays)
	errors := make(chan error, replays)
	createdResults := make(chan bool, replays)
	for range replays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, replayCreated, replayErr := service.Register(context.Background(), input)
			results <- session
			createdResults <- replayCreated
			errors <- replayErr
		}()
	}
	wg.Wait()
	close(results)
	close(createdResults)
	close(errors)
	for replayErr := range errors {
		if replayErr != nil {
			t.Fatalf("active replay: %v", replayErr)
		}
	}
	for replayCreated := range createdResults {
		if replayCreated {
			t.Fatal("active replay created another generation")
		}
	}
	for replay := range results {
		if replay.ID != second.ID {
			t.Fatalf("active replay id=%s want %s", replay.ID, second.ID)
		}
	}
	var total, active int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*),SUM(phase<>'stopped') FROM harness_sessions WHERE project_id=? AND harness=? AND host=?`, projectID, input.Harness, input.Host).Scan(&total, &active); err != nil {
		t.Fatal(err)
	}
	if total != 2 || active != 1 {
		t.Fatalf("history total=%d active=%d", total, active)
	}
}

func TestYieldClaimsOwnedInterruptAndStopRequests(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "thread-1",
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	interrupt, err := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := service.RequestControl(context.Background(), session.ID, ControlStop, 1)
	if err != nil {
		t.Fatal(err)
	}
	yielded, err := service.Yield(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if yielded.Session.YieldSequence != 1 || yielded.Session.Phase != PhaseYielded || len(yielded.Controls) != 2 {
		t.Fatalf("yield=%#v", yielded)
	}
	if yielded.Controls[0].ID != interrupt.ID || yielded.Controls[1].ID != stop.ID ||
		yielded.Controls[0].State != ControlClaimed || yielded.Controls[1].State != ControlClaimed {
		t.Fatalf("controls=%#v", yielded.Controls)
	}
	completed, err := service.CompleteControl(context.Background(), session.ID, interrupt.ID, ControlApplied, ReasonApplied)
	if err != nil || completed.State != ControlApplied {
		t.Fatalf("complete=%#v err=%v", completed, err)
	}
}

func TestUnmanagedSessionCannotRequestOwnedControl(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	target, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "thread-1", MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "thread-1", MessageTargetID: target.ID,
		ManagementMode: ManagementUnmanaged, Role: RoleCoordinator, SteerMode: SteerCodexExternal,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1); !IsCode(err, CodeCapabilityUnavailable) {
		t.Fatalf("error=%v", err)
	}
}
