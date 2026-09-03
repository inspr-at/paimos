// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func addHierarchyAgent(t *testing.T, projectID int64, name string) {
	t.Helper()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
		t.Fatal(err)
	}
}

func addHierarchyTicket(t *testing.T, projectID, number int64) int64 {
	t.Helper()
	result, err := paimosdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,?,'ticket','Hierarchy test','in-progress')`, projectID, number)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func registerHierarchySession(t *testing.T, service *Service, projectID int64, agent, role string, parent *string, ticket *int64) models.HarnessSession {
	t.Helper()
	session, created, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: agent, Harness: "codex", Host: "host-" + agent,
		SessionRef: "ref-" + agent, WorkerLease: testWorkerLease, ManagementMode: ManagementManaged,
		Role: role, ParentSessionID: parent, TicketID: ticket, SteerMode: SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err != nil || !created {
		t.Fatalf("register %s: session=%+v created=%v err=%v", agent, session, created, err)
	}
	return session
}

func TestPersistentHierarchyBindingCASAndEventHistory(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	addHierarchyAgent(t, projectID, "coordinator")
	addHierarchyAgent(t, projectID, "child")
	ticketID := addHierarchyTicket(t, projectID, 903)
	service := NewService(paimosdb.DB)
	coordinator := registerHierarchySession(t, service, projectID, "coordinator", RoleCoordinator, nil, nil)
	if _, err := service.HeartbeatWithActivity(context.Background(), coordinator.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		t.Fatal(err)
	}
	child := registerHierarchySession(t, service, projectID, "child", RoleWorker, &coordinator.ID, &ticketID)
	if child.ParentSessionID == nil || *child.ParentSessionID != coordinator.ID || child.TicketID == nil || *child.TicketID != ticketID {
		t.Fatalf("durable binding missing: %+v", child)
	}

	replay := RegisterInput{ProjectID: projectID, AgentName: "child", Harness: "codex", Host: "host-child", SessionRef: "ref-child",
		WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, ParentSessionID: &coordinator.ID,
		TicketID: &ticketID, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true}}
	if _, created, err := service.Register(context.Background(), replay); err != nil || created {
		t.Fatalf("exact replay: created=%v err=%v", created, err)
	}
	replay.ParentSessionID = nil
	if _, _, err := service.Register(context.Background(), replay); !IsCode(err, CodeConflict) {
		t.Fatalf("registration silently rebound a generation: %v", err)
	}

	projection, err := service.ProjectOrchestrator(context.Background(), projectID)
	if err != nil || projection.State != "resolved" || projection.Session == nil || projection.Session.ID != coordinator.ID {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	detached, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: nil, TicketID: nil})
	if err != nil || detached.ParentSessionID != nil || detached.TicketID != nil || detached.Revision != child.Revision+1 {
		t.Fatalf("detached=%+v err=%v", detached, err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &ticketID}); !IsCode(err, CodeConflict) {
		t.Fatalf("stale binding CAS accepted: %v", err)
	}

	var operation string
	var beforeParent sql.NullString
	var afterParent sql.NullString
	var beforeTicket sql.NullInt64
	var afterTicket sql.NullInt64
	if err := paimosdb.DB.QueryRow(`SELECT operation,before_parent_harness_session_id,after_parent_harness_session_id,before_ticket_id,after_ticket_id
		FROM harness_session_events WHERE harness_session_id=? AND event_sequence=?`, child.ID, detached.Revision).
		Scan(&operation, &beforeParent, &afterParent, &beforeTicket, &afterTicket); err != nil {
		t.Fatal(err)
	}
	if operation != "binding_changed" || !beforeParent.Valid || beforeParent.String != coordinator.ID || afterParent.Valid ||
		!beforeTicket.Valid || beforeTicket.Int64 != ticketID || afterTicket.Valid {
		t.Fatalf("binding event lost before/after values: op=%s parent=%+v/%+v ticket=%+v/%+v", operation, beforeParent, afterParent, beforeTicket, afterTicket)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_session_events SET operation='heartbeat' WHERE harness_session_id=? AND event_sequence=?`, child.ID, detached.Revision); err == nil {
		t.Fatal("immutable binding event was updated")
	}
}

func TestHierarchyRejectsCrossProjectTerminalCyclesAndAmbiguity(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	addHierarchyAgent(t, projectID, "coordinator")
	addHierarchyAgent(t, projectID, "child")
	addHierarchyAgent(t, projectID, "other-coordinator")
	ticketID := addHierarchyTicket(t, projectID, 903)
	service := NewService(paimosdb.DB)
	coordinator := registerHierarchySession(t, service, projectID, "coordinator", RoleCoordinator, nil, nil)
	child := registerHierarchySession(t, service, projectID, "child", RoleWorker, &coordinator.ID, &ticketID)

	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: coordinator.ID,
		ExpectedRevision: coordinator.Revision, ParentSessionID: &child.ID, TicketID: nil}); err == nil {
		t.Fatal("hierarchy cycle accepted")
	}
	otherProject, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Other hierarchy','OTH')`)
	if err != nil {
		t.Fatal(err)
	}
	otherProjectID, _ := otherProject.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'foreign')`, otherProjectID); err != nil {
		t.Fatal(err)
	}
	foreign := registerHierarchySession(t, service, otherProjectID, "foreign", RoleWorker, nil, nil)
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &foreign.ID, TicketID: &ticketID}); err == nil {
		t.Fatal("cross-project parent accepted")
	}
	foreignTicket := addHierarchyTicket(t, otherProjectID, 1)
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &foreignTicket}); err == nil {
		t.Fatal("cross-project ticket accepted")
	}
	if _, err := service.Stop(context.Background(), coordinator.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &ticketID}); err == nil {
		t.Fatal("terminal parent accepted")
	}

	registerHierarchySession(t, service, projectID, "other-coordinator", RoleCoordinator, nil, nil)
	projection, err := service.ProjectOrchestrator(context.Background(), projectID)
	if err != nil || projection.State != "unset" || projection.Reason != "coordinator_unknown" || projection.Session != nil {
		t.Fatalf("unknown coordinator was promoted: projection=%+v err=%v", projection, err)
	}
	addHierarchyAgent(t, projectID, "third-coordinator")
	registerHierarchySession(t, service, projectID, "third-coordinator", RoleCoordinator, nil, nil)
	projection, err = service.ProjectOrchestrator(context.Background(), projectID)
	if err != nil || projection.State != "ambiguous" || projection.Session != nil {
		t.Fatalf("multiple coordinators were resolved: projection=%+v err=%v", projection, err)
	}
}

func TestHierarchyDepthBoundAndDatabaseGuardsCannotBeBypassed(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	var parent *string
	var last models.HarnessSession
	for depth := 0; depth <= MaxHierarchyDepth; depth++ {
		name := fmt.Sprintf("depth-%02d", depth)
		addHierarchyAgent(t, projectID, name)
		last = registerHierarchySession(t, service, projectID, name, RoleWorker, parent, nil)
		parent = &last.ID
	}
	addHierarchyAgent(t, projectID, "too-deep")
	_, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "too-deep", Harness: "codex", Host: "host-too-deep", SessionRef: "ref-too-deep",
		WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, ParentSessionID: parent,
		SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err == nil {
		t.Fatal("over-depth hierarchy accepted")
	}
	var rootID string
	if err := paimosdb.DB.QueryRow(`SELECT id FROM harness_sessions WHERE agent_name='depth-00'`).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,revision=revision+1 WHERE id=?`, last.ID, rootID); err == nil {
		t.Fatal("database trigger accepted a hierarchy cycle")
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=NULL WHERE id=?`, last.ID); err == nil {
		t.Fatal("database trigger accepted a binding change without revision CAS")
	}
}
