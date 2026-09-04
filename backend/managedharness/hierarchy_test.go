// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workshape"
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
	shape := ""
	if ticket != nil {
		shape = workshape.Ship
	}
	session, created, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: agent, Harness: "codex", Host: "host-" + agent,
		SessionRef: "ref-" + agent, WorkerLease: testWorkerLease, ManagementMode: ManagementManaged,
		Role: role, ParentSessionID: parent, TicketID: ticket, WorkShape: shape, SteerMode: SteerNone,
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
		TicketID: &ticketID, WorkShape: workshape.Ship, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true}}
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
		ExpectedRevision: child.Revision, ParentSessionID: nil, TicketID: nil, WorkShape: workshape.Unknown})
	if err != nil || detached.ParentSessionID != nil || detached.TicketID != nil || detached.Revision != child.Revision+1 {
		t.Fatalf("detached=%+v err=%v", detached, err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &ticketID, WorkShape: workshape.Ship}); !IsCode(err, CodeConflict) {
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

func TestExactRegistrationReplaySurvivesStoppedParent(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	addHierarchyAgent(t, projectID, "parent")
	addHierarchyAgent(t, projectID, "child")
	addHierarchyAgent(t, projectID, "new-child")
	service := NewService(paimosdb.DB)
	parent := registerHierarchySession(t, service, projectID, "parent", RoleWorker, nil, nil)
	child := registerHierarchySession(t, service, projectID, "child", RoleWorker, &parent.ID, nil)
	if _, err := service.Stop(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	replay := RegisterInput{ProjectID: projectID, AgentName: "child", Harness: "codex", Host: "host-child", SessionRef: "ref-child",
		WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, ParentSessionID: &parent.ID,
		SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true}}
	got, created, err := service.Register(context.Background(), replay)
	if err != nil || created || got.ID != child.ID {
		t.Fatalf("exact replay after parent stop: got=%+v created=%v err=%v", got, created, err)
	}
	replay.AgentName = "new-child"
	replay.Host = "host-new-child"
	replay.SessionRef = "ref-new-child"
	if _, _, err := service.Register(context.Background(), replay); !IsCode(err, CodeInvalid) {
		t.Fatalf("new child accepted stopped parent: %v", err)
	}
}

func TestAssignBindingValidatesOnlyChangedFullStateReferences(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	for _, name := range []string{"stopped-parent", "active-parent", "ticket-child", "parent-child", "two-field-child"} {
		addHierarchyAgent(t, projectID, name)
	}
	service := NewService(paimosdb.DB)
	stoppedParent := registerHierarchySession(t, service, projectID, "stopped-parent", RoleWorker, nil, nil)
	activeParent := registerHierarchySession(t, service, projectID, "active-parent", RoleWorker, nil, nil)
	firstTicket := addHierarchyTicket(t, projectID, 910)
	secondTicket := addHierarchyTicket(t, projectID, 911)
	historicalTicket := addHierarchyTicket(t, projectID, 912)

	ticketChild := registerHierarchySession(t, service, projectID, "ticket-child", RoleWorker, &stoppedParent.ID, &firstTicket)
	if _, err := service.Stop(context.Background(), stoppedParent.ID); err != nil {
		t.Fatal(err)
	}
	changedTicket, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: ticketChild.ID, ExpectedRevision: ticketChild.Revision,
		ParentSessionID: &stoppedParent.ID, TicketID: &secondTicket, WorkShape: workshape.Ship,
	})
	if err != nil || changedTicket.ParentSessionID == nil || *changedTicket.ParentSessionID != stoppedParent.ID ||
		changedTicket.TicketID == nil || *changedTicket.TicketID != secondTicket {
		t.Fatalf("ticket-only full-state CAS revalidated stopped parent: session=%+v err=%v", changedTicket, err)
	}

	parentChild := registerHierarchySession(t, service, projectID, "parent-child", RoleWorker, nil, &historicalTicket)
	stoppedChild, err := service.Stop(context.Background(), parentChild.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE issues SET deleted_at='2026-09-03 03:00:00' WHERE id=?`, historicalTicket); err != nil {
		t.Fatal(err)
	}
	changedParent, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: stoppedChild.ID, ExpectedRevision: stoppedChild.Revision,
		ParentSessionID: &activeParent.ID, TicketID: &historicalTicket, WorkShape: workshape.Ship,
	})
	if err != nil || changedParent.ParentSessionID == nil || *changedParent.ParentSessionID != activeParent.ID ||
		changedParent.TicketID == nil || *changedParent.TicketID != historicalTicket {
		t.Fatalf("parent-only full-state CAS revalidated trashed ticket: session=%+v err=%v", changedParent, err)
	}

	twoFieldChild := registerHierarchySession(t, service, projectID, "two-field-child", RoleWorker, nil, nil)
	twoFields, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: twoFieldChild.ID, ExpectedRevision: twoFieldChild.Revision,
		ParentSessionID: &activeParent.ID, TicketID: &secondTicket, WorkShape: workshape.Ship,
	})
	if err != nil || twoFields.ParentSessionID == nil || twoFields.TicketID == nil {
		t.Fatalf("simultaneous valid binding change failed: session=%+v err=%v", twoFields, err)
	}
	detached, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: twoFields.ID, ExpectedRevision: twoFields.Revision,
		ParentSessionID: nil, TicketID: nil, WorkShape: workshape.Unknown,
	})
	if err != nil || detached.ParentSessionID != nil || detached.TicketID != nil {
		t.Fatalf("simultaneous detach failed: session=%+v err=%v", detached, err)
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
		ExpectedRevision: coordinator.Revision, ParentSessionID: &child.ID, TicketID: nil, WorkShape: workshape.Unknown}); err == nil {
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
	_, foreignParentErr := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &foreign.ID, TicketID: &ticketID, WorkShape: workshape.Ship})
	if foreignParentErr == nil {
		t.Fatal("cross-project parent accepted")
	}
	missingParent := "99999999-9999-4999-8999-999999999999"
	_, missingParentErr := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &missingParent, TicketID: &ticketID, WorkShape: workshape.Ship})
	if missingParentErr == nil || foreignParentErr.Error() != missingParentErr.Error() {
		t.Fatalf("parent existence oracle: foreign=%v missing=%v", foreignParentErr, missingParentErr)
	}
	foreignTicket := addHierarchyTicket(t, otherProjectID, 1)
	_, foreignTicketErr := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &foreignTicket, WorkShape: workshape.Ship})
	if foreignTicketErr == nil {
		t.Fatal("cross-project ticket accepted")
	}
	missingTicket := int64(999999999)
	_, missingTicketErr := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &missingTicket, WorkShape: workshape.Ship})
	if missingTicketErr == nil || foreignTicketErr.Error() != missingTicketErr.Error() {
		t.Fatalf("ticket existence oracle: foreign=%v missing=%v", foreignTicketErr, missingTicketErr)
	}
	if _, err := service.Stop(context.Background(), coordinator.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: child.ID,
		ExpectedRevision: child.Revision, ParentSessionID: &coordinator.ID, TicketID: &ticketID, WorkShape: workshape.Ship}); err == nil {
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
	addHierarchyAgent(t, projectID, "new-root")
	newRoot := registerHierarchySession(t, service, projectID, "new-root", RoleWorker, nil, nil)
	root, err := service.Get(context.Background(), projectID, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{ProjectID: projectID, SessionID: root.ID,
		ExpectedRevision: root.Revision, ParentSessionID: &newRoot.ID, WorkShape: workshape.Unknown}); !IsCode(err, CodeInvalid) {
		t.Fatalf("service accepted reparenting a depth-16 subtree under a new root: %v", err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,revision=revision+1 WHERE id=?`, newRoot.ID, rootID); err == nil {
		t.Fatal("database trigger accepted an over-depth moved subtree")
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=NULL WHERE id=?`, last.ID); err == nil {
		t.Fatal("database trigger accepted a binding change without revision CAS")
	}

	addHierarchyAgent(t, projectID, "cycle-a")
	addHierarchyAgent(t, projectID, "cycle-b")
	cycleA := registerHierarchySession(t, service, projectID, "cycle-a", RoleWorker, nil, nil)
	cycleB := registerHierarchySession(t, service, projectID, "cycle-b", RoleWorker, &cycleA.ID, nil)
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,revision=revision+1 WHERE id=?`, cycleB.ID, cycleA.ID); err == nil {
		t.Fatal("database trigger accepted a shallow hierarchy cycle")
	}
}

func TestDatabaseBindingGuardsRejectInvalidReferencesAndActiveTicketMutation(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	addHierarchyAgent(t, projectID, "parent")
	addHierarchyAgent(t, projectID, "child")
	service := NewService(paimosdb.DB)
	parent := registerHierarchySession(t, service, projectID, "parent", RoleWorker, nil, nil)
	child := registerHierarchySession(t, service, projectID, "child", RoleWorker, nil, nil)

	otherProject, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Foreign hierarchy','FHI')`)
	if err != nil {
		t.Fatal(err)
	}
	otherProjectID, _ := otherProject.LastInsertId()
	addHierarchyAgent(t, otherProjectID, "foreign")
	foreign := registerHierarchySession(t, service, otherProjectID, "foreign", RoleWorker, nil, nil)
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,revision=revision+1 WHERE id=?`, foreign.ID, child.ID); err == nil {
		t.Fatal("database trigger accepted a cross-project parent")
	}

	if _, err := service.Stop(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET parent_harness_session_id=?,revision=revision+1 WHERE id=?`, parent.ID, child.ID); err == nil {
		t.Fatal("database trigger accepted a terminal parent")
	}

	deletedTicket := addHierarchyTicket(t, projectID, 904)
	if _, err := paimosdb.DB.Exec(`UPDATE issues SET deleted_at='2026-09-03 01:00:00' WHERE id=?`, deletedTicket); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET ticket_id=?,work_shape='ship',revision=revision+1 WHERE id=?`, deletedTicket, child.ID); err == nil {
		t.Fatal("database trigger accepted a deleted ticket")
	}

	activeTicket := addHierarchyTicket(t, projectID, 905)
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET ticket_id=?,work_shape='ship',revision=revision+1 WHERE id=?`, activeTicket, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE issues SET deleted_at='2026-09-03 01:00:00' WHERE id=?`, activeTicket); err == nil || !strings.Contains(err.Error(), "bound harness ticket") {
		t.Fatalf("active bound ticket mutation error=%v", err)
	}
}
