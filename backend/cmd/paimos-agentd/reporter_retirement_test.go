// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	reporterRuntimeGeneration = "44444444-4444-4444-8444-444444444444"
	reporterRuntimeID         = "55555555-5555-4555-8555-555555555555"
)

type retirementReporterController struct {
	state       agentd.ReporterState
	stops       int
	partial     bool
	stopErr     error
	failApplied bool
}

func (*retirementReporterController) Interrupt(context.Context, string, agentd.ControlRequest) (agentd.Receipt, error) {
	return agentd.Receipt{}, errors.New("unexpected interrupt")
}

func (c *retirementReporterController) Stop(_ context.Context, session string, request agentd.ControlRequest) (agentd.Receipt, error) {
	c.stops++
	if c.stopErr != nil {
		return agentd.Receipt{}, c.stopErr
	}
	if c.partial {
		return agentd.Receipt{Operation: "stop", SessionID: session, CorrelationID: request.CorrelationID}, nil
	}
	return agentd.Receipt{Operation: "stop", SessionID: session, Instance: request.Instance, ProjectID: request.ProjectID,
		Identity: request.Identity, RequestedLevel: "steer", EffectiveLevel: "steer", Primitive: "test owned reap",
		CorrelationID: request.CorrelationID, AppliedAt: time.Now().UTC()}, nil
}

func (*retirementReporterController) Reject(context.Context, string, agentd.ControlRequest, agentd.ErrorCode) error {
	return errors.New("unexpected reject")
}

func (c *retirementReporterController) CheckpointReporter(_ context.Context, _ string, _ agentd.ControlRequest, state agentd.ReporterState) error {
	if c.failApplied && state.Pending != nil && state.Pending.Kind == managedharness.RetirementKind && state.Pending.Outcome == "applied" {
		return errors.New("journal unavailable")
	}
	c.state = state
	return nil
}

func retirementClaimFixture() models.HarnessRetirementClaim {
	return models.HarnessRetirementClaim{
		ID: reporterControlID, ProjectID: 6, HarnessSessionID: publicReporterSession, HarnessSessionRevision: 10,
		RequestedActivitySequence: 7, RuntimeID: reporterRuntimeID, RuntimeGeneration: reporterRuntimeGeneration,
		SessionGeneration: localReporterSession, State: "claimed", RequestedAt: "2026-09-09T10:00:00Z", ClaimedAt: "2026-09-09T10:00:01Z",
	}
}

func retirementSessionFixture(kind agentd.EventKind) agentd.Session {
	return agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, ActivitySequence: 7, LastEventKind: kind,
		Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop},
		Reporter:     agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}}
}

func retirementRemoteSession() harnessSessionResponse {
	return harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex", Phase: "working", Revision: 11, ActivitySequence: 7}
}

func retirementOutcomeFixture(outcome, reason string) models.HarnessRetirementOutcome {
	state := "failed"
	if outcome == "applied" {
		state = "stopping"
	} else if reason == "outcome_unknown" {
		state = "outcome_unknown"
	}
	return models.HarnessRetirementOutcome{ID: reporterControlID, ProjectID: 6, HarnessSessionID: publicReporterSession,
		CorrelationID: reporterControlID, Kind: managedharness.RetirementKind, RequestedRevision: 10, State: state, Reason: reason,
		RequestedAt: "2026-09-09T10:00:00Z", OwnedStopReceipt: outcome == "applied"}
}

func TestCLIReporterRetirementWaitsForExactTurnBoundaryThenStopsWithReceipt(t *testing.T) {
	claim := retirementClaimFixture()
	commands := []string{}
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		commands = append(commands, args[2])
		switch args[2] {
		case "heartbeat":
			return json.Marshal(retirementRemoteSession())
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: retirementRemoteSession(), Retirements: []models.HarnessRetirementClaim{claim}})
		case "retirement-ready":
			ready := claim
			ready.State, ready.StoppingAt = "stopping", "2026-09-09T10:00:02Z"
			return json.Marshal(ready)
		case "complete-retirement":
			if !slices.Contains(args, "--outcome") || !slices.Contains(args, "applied") || !slices.Contains(args, reporterControlID) {
				t.Fatalf("retirement completion args=%v", args)
			}
			return json.Marshal(retirementOutcomeFixture("applied", "applied"))
		case "mark-stopped":
			return reporterSessionEvidence("worker", "stopped"), nil
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	controller := &retirementReporterController{}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	session := retirementSessionFixture(agentd.EventTurnCompleted)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: reporterRuntimeGeneration, Instance: "ppm", Sessions: []agentd.Session{session}}); err != nil {
		t.Fatal(err)
	}
	if controller.stops != 1 || controller.state.Pending != nil || !controller.state.Closed || !slices.Equal(commands, []string{"heartbeat", "yield", "retirement-ready", "complete-retirement", "mark-stopped"}) {
		t.Fatalf("commands=%v stops=%d state=%+v", commands, controller.stops, controller.state)
	}
}

func TestCLIReporterRetirementUsesOwnedSupervisorStopAndPersistsClosure(t *testing.T) {
	process := &reporterTestProcess{done: make(chan struct{})}
	stateRoot := t.TempDir()
	supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "ppm", StateRoot: stateRoot, Adapters: []agentd.Adapter{reporterTestAdapter{process: process}}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := supervisor.Start(context.Background(), agentd.StartRequest{Adapter: agentd.AdapterCodex, Workspace: t.TempDir(), Prompt: "fixture", Identity: "codex:worker", ProjectID: 6})
	if err != nil {
		t.Fatal(err)
	}
	request := agentd.ControlRequest{Instance: "ppm", ProjectID: 6, Identity: "codex:worker"}
	initialReporter := agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}
	if err := supervisor.CheckpointReporter(context.Background(), started.ID, request, initialReporter); err != nil {
		t.Fatal(err)
	}
	status := supervisor.Status()
	session := status.Sessions[0]
	session.LastEventKind, session.ActivitySequence = agentd.EventTurnCompleted, 7
	claim := retirementClaimFixture()
	claim.RuntimeGeneration, claim.SessionGeneration = status.DaemonID, session.ID
	remote := retirementRemoteSession()
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "heartbeat":
			return json.Marshal(remote)
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: remote, Retirements: []models.HarnessRetirementClaim{claim}})
		case "retirement-ready":
			ready := claim
			ready.State, ready.StoppingAt = "stopping", "2026-09-09T10:00:02Z"
			return json.Marshal(ready)
		case "complete-retirement":
			return json.Marshal(retirementOutcomeFixture("applied", "applied"))
		case "mark-stopped":
			return reporterSessionEvidence("worker", "stopped"), nil
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(supervisor)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: status.DaemonID, Instance: "ppm", Sessions: []agentd.Session{session}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	default:
		t.Fatal("owned child was not stopped and reaped")
	}
	persisted := supervisor.Status().Sessions[0]
	if !terminalAgentdState(persisted.State) || !persisted.Reporter.RemoteClosed || !persisted.Reporter.Closed || persisted.Reporter.Pending != nil {
		t.Fatalf("persisted session=%+v", persisted)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "ppm", StateRoot: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	recovered := reopened.Status().Sessions
	if len(recovered) != 1 || !recovered[0].Reporter.Closed || recovered[0].Reporter.Pending != nil {
		t.Fatalf("recovered reporter closure=%+v", recovered)
	}
}

func TestCLIReporterRetirementDoesNotUsePublicIdleAsFinishEvidence(t *testing.T) {
	claim := retirementClaimFixture()
	commands := []string{}
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		commands = append(commands, args[2])
		if args[2] == "heartbeat" {
			return json.Marshal(retirementRemoteSession())
		}
		if args[2] == "yield" {
			return json.Marshal(harnessYieldResponse{Session: retirementRemoteSession(), Retirements: []models.HarnessRetirementClaim{claim}})
		}
		return nil, errors.New("unexpected reporter command")
	}
	controller := &retirementReporterController{}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: reporterRuntimeGeneration, Instance: "ppm", Sessions: []agentd.Session{retirementSessionFixture(agentd.EventToolStarted)}}); err != nil {
		t.Fatal(err)
	}
	if controller.stops != 0 || controller.state.Pending != nil || !slices.Equal(commands, []string{"heartbeat", "yield"}) {
		t.Fatalf("commands=%v stops=%d state=%+v", commands, controller.stops, controller.state)
	}
}

func TestCLIReporterRetirementRejectsStaleRuntimeGeneration(t *testing.T) {
	claim := retirementClaimFixture()
	claim.RuntimeGeneration = "66666666-6666-4666-8666-666666666666"
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[2] == "heartbeat" {
			return json.Marshal(retirementRemoteSession())
		}
		if args[2] == "yield" {
			return json.Marshal(harnessYieldResponse{Session: retirementRemoteSession(), Retirements: []models.HarnessRetirementClaim{claim}})
		}
		return nil, errors.New("unexpected reporter command")
	}
	controller := &retirementReporterController{}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: reporterRuntimeGeneration, Instance: "ppm", Sessions: []agentd.Session{retirementSessionFixture(agentd.EventTurnCompleted)}})
	if err == nil || controller.stops != 0 || controller.state.Pending != nil {
		t.Fatalf("error=%v stops=%d state=%+v", err, controller.stops, controller.state)
	}
}

func TestCLIReporterRetirementNotReadyKeepsRequestWithoutStop(t *testing.T) {
	claim := retirementClaimFixture()
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "heartbeat":
			return json.Marshal(retirementRemoteSession())
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: retirementRemoteSession(), Retirements: []models.HarnessRetirementClaim{claim}})
		case "retirement-ready":
			return []byte(`{"error_code":"harness_session_retirement_not_ready"}`), errors.New("conflict")
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	controller := &retirementReporterController{}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: reporterRuntimeGeneration, Instance: "ppm", Sessions: []agentd.Session{retirementSessionFixture(agentd.EventTurnCompleted)}}); err != nil {
		t.Fatal(err)
	}
	if controller.stops != 0 || controller.state.Pending != nil {
		t.Fatalf("not-ready request invoked stop or retained ambiguous checkpoint: stops=%d state=%+v", controller.stops, controller.state)
	}
}

func TestCLIReporterRetirementReceiptCheckpointFailureRecoversAsUnknownWithoutReplay(t *testing.T) {
	claim := retirementClaimFixture()
	controller := &retirementReporterController{failApplied: true}
	firstRunner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "heartbeat":
			return json.Marshal(retirementRemoteSession())
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: retirementRemoteSession(), Retirements: []models.HarnessRetirementClaim{claim}})
		case "retirement-ready":
			ready := claim
			ready.State, ready.StoppingAt = "stopping", "2026-09-09T10:00:02Z"
			return json.Marshal(ready)
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, firstRunner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	session := retirementSessionFixture(agentd.EventTurnCompleted)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: reporterRuntimeGeneration, Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
		t.Fatal("expected receipt checkpoint failure")
	}
	if controller.stops != 1 || controller.state.Pending == nil || controller.state.Pending.Reason != "outcome_unknown" {
		t.Fatalf("stops=%d state=%+v", controller.stops, controller.state)
	}

	controller.failApplied = false
	commands := []string{}
	replayRunner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		commands = append(commands, args[2])
		switch args[2] {
		case "complete-retirement":
			return json.Marshal(retirementOutcomeFixture("rejected", "outcome_unknown"))
		case "mark-stopped":
			return reporterSessionEvidence("worker", "stopped"), nil
		default:
			return nil, errors.New("unexpected replay command")
		}
	}
	recovered := session
	recovered.State, recovered.Reporter = agentd.StateOwnershipLost, controller.state
	restarted, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, replayRunner, newMemoryReporterLeaseStore())
	_ = restarted.BindController(controller)
	if err := restarted.ReportStatus(context.Background(), agentd.Status{DaemonID: "77777777-7777-4777-8777-777777777777", Instance: "ppm", Sessions: []agentd.Session{recovered}}); err != nil {
		t.Fatal(err)
	}
	if controller.stops != 1 || !slices.Equal(commands, []string{"complete-retirement", "mark-stopped"}) || !controller.state.Closed {
		t.Fatalf("commands=%v stops=%d state=%+v", commands, controller.stops, controller.state)
	}
}

func TestCLIReporterRetirementInvalidStopReceiptIsOutcomeUnknown(t *testing.T) {
	claim := retirementClaimFixture()
	controller := &retirementReporterController{partial: true}
	completedUnknown := false
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "heartbeat":
			return json.Marshal(retirementRemoteSession())
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: retirementRemoteSession(), Retirements: []models.HarnessRetirementClaim{claim}})
		case "retirement-ready":
			ready := claim
			ready.State, ready.StoppingAt = "stopping", "2026-09-09T10:00:02Z"
			return json.Marshal(ready)
		case "complete-retirement":
			completedUnknown = slices.Contains(args, "rejected") && slices.Contains(args, "outcome_unknown")
			return json.Marshal(retirementOutcomeFixture("rejected", "outcome_unknown"))
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{DaemonID: reporterRuntimeGeneration, Instance: "ppm", Sessions: []agentd.Session{retirementSessionFixture(agentd.EventTurnCompleted)}}); err != nil {
		t.Fatal(err)
	}
	if controller.stops != 1 || !completedUnknown || controller.state.Pending != nil {
		t.Fatalf("stops=%d completed_unknown=%v state=%+v", controller.stops, completedUnknown, controller.state)
	}
}
