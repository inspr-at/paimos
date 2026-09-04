// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"database/sql"
	"testing"

	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workshape"
)

func TestWorkShapeRegistrationAndPromotionAreExplicitRevisionedFacts(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	addHierarchyAgent(t, projectID, "scout")
	ticketID := addHierarchyTicket(t, projectID, 907)
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "scout", Harness: "codex", Host: "shape-host", SessionRef: "shape-ref",
		WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, TicketID: &ticketID,
		WorkShape: workshape.Scout, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true},
	}
	session, created, err := service.Register(context.Background(), input)
	if err != nil || !created {
		t.Fatalf("register scout: session=%+v created=%v err=%v", session, created, err)
	}
	if session.WorkShape != workshape.Scout || session.WorkContract.OutputKind != workshape.OutputInvestigationEvidence ||
		session.WorkContract.StageApplicability[1].Applicability != "not_applicable" {
		t.Fatalf("scout contract=%+v", session.WorkContract)
	}
	var registerBefore, registerAfter sql.NullString
	if err := paimosdb.DB.QueryRow(`SELECT before_work_shape,after_work_shape FROM harness_session_events
		WHERE harness_session_id=? AND operation='register'`, session.ID).Scan(&registerBefore, &registerAfter); err != nil ||
		registerBefore.Valid || !registerAfter.Valid || registerAfter.String != workshape.Scout {
		t.Fatalf("register assignment event before=%+v after=%+v err=%v", registerBefore, registerAfter, err)
	}

	mismatch := input
	mismatch.WorkShape = workshape.Ship
	if _, _, err := service.Register(context.Background(), mismatch); !IsCode(err, CodeConflict) {
		t.Fatalf("registration replay silently promoted scout: %v", err)
	}
	promoted, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: session.ID, ExpectedRevision: session.Revision,
		TicketID: &ticketID, WorkShape: workshape.Ship,
	})
	if err != nil || promoted.WorkShape != workshape.Ship || promoted.WorkContract.OutputKind != workshape.OutputDelivery || promoted.Revision != session.Revision+1 {
		t.Fatalf("explicit promotion=%+v err=%v", promoted, err)
	}
	var before, after sql.NullString
	if err := paimosdb.DB.QueryRow(`SELECT before_work_shape,after_work_shape FROM harness_session_events
		WHERE harness_session_id=? AND event_sequence=?`, session.ID, promoted.Revision).Scan(&before, &after); err != nil ||
		before.String != workshape.Scout || after.String != workshape.Ship {
		t.Fatalf("promotion event before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: session.ID, ExpectedRevision: session.Revision,
		TicketID: &ticketID, WorkShape: workshape.Scout,
	}); !IsCode(err, CodeConflict) {
		t.Fatalf("stale promotion revision accepted: %v", err)
	}
	if _, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: session.ID, ExpectedRevision: promoted.Revision,
		TicketID: nil, WorkShape: workshape.Ship,
	}); !IsCode(err, CodeInvalid) {
		t.Fatalf("classified detached binding accepted: %v", err)
	}
	detached, err := service.AssignBinding(context.Background(), BindingInput{
		ProjectID: projectID, SessionID: session.ID, ExpectedRevision: promoted.Revision,
		TicketID: nil, WorkShape: workshape.Unknown,
	})
	if err != nil || detached.TicketID != nil || detached.WorkShape != workshape.Unknown {
		t.Fatalf("explicit detach=%+v err=%v", detached, err)
	}
}

func TestNewTicketBindingCannotManufactureLegacyUnknownShape(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	addHierarchyAgent(t, projectID, "new-worker")
	ticketID := addHierarchyTicket(t, projectID, 908)
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "new-worker", Harness: "codex", Host: "new-host", SessionRef: "new-ref",
		WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, TicketID: &ticketID,
		SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true},
	}
	if err := ValidateRegistration(input); !IsCode(err, CodeInvalid) {
		t.Fatalf("registration preflight accepted ticket binding without shape: %v", err)
	}
	if _, _, err := service.Register(context.Background(), input); !IsCode(err, CodeInvalid) {
		t.Fatalf("new ticket binding without shape accepted: %v", err)
	}
	input.WorkShape = workshape.Unknown
	if err := ValidateRegistration(input); err != nil {
		t.Fatalf("registration preflight rejected potential legacy unknown replay: %v", err)
	}
	if _, _, err := service.Register(context.Background(), input); !IsCode(err, CodeInvalid) {
		t.Fatalf("new ticket binding manufactured legacy unknown: %v", err)
	}
	if !sameRegistration(models.HarnessSession{
		ProjectID: projectID, AgentName: input.AgentName, Harness: input.Harness, Host: input.Host,
		ManagementMode: input.ManagementMode, Role: input.Role, TicketID: input.TicketID,
		WorkShape: workshape.Unknown, SteerMode: input.SteerMode, Capabilities: input.Capabilities,
	}, input) {
		t.Fatal("exact legacy unknown replay no longer matches its stored assignment")
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
		management_mode,role,ticket_id,steer_mode,phase)
		SELECT '22222222-2222-4222-8222-222222222222',?,id,name,'codex','raw-host',zeroblob(32),zeroblob(32),
		'managed','worker',?,'none','starting' FROM project_agents WHERE project_id=? AND name='new-worker'`, projectID, ticketID, projectID); err == nil {
		t.Fatal("database accepted a newly inserted ticket binding without shape")
	}
}
