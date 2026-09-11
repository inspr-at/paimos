// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"testing"
	"time"
)

func retirementJournalSession(t *testing.T, completion ReporterCompletion) Session {
	t.Helper()
	now := time.Now().UTC()
	return Session{
		ID: "11111111-1111-4111-8111-111111111111", Identity: "codex:worker", ProjectID: 6,
		Workspace: t.TempDir(), Managed: true, State: StateRunning,
		Capabilities: []Capability{CapabilityStatus, CapabilityStop}, StartedAt: now, HeartbeatAt: now,
		Reporter: ReporterState{
			PublicSessionID: "22222222-2222-4222-8222-222222222222",
			Capabilities:    []Capability{CapabilityStatus, CapabilityStop},
			Pending:         &completion,
		},
	}
}

func TestRegistryJournalPersistsAndRecoversRetirementCompletion(t *testing.T) {
	for _, completion := range []ReporterCompletion{
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "retire_after_work", Outcome: "applied", Reason: "applied"},
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "retire_after_work", Outcome: "rejected", Reason: "outcome_unknown"},
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "retire_after_work", Outcome: "rejected", Reason: "not_running"},
	} {
		t.Run(completion.Outcome+"_"+completion.Reason, func(t *testing.T) {
			root := t.TempDir()
			journal, err := openRegistryJournal(root, "ppm-retirement", 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.put(retirementJournalSession(t, completion)); err != nil {
				t.Fatal(err)
			}
			reopened, err := openRegistryJournal(root, "ppm-retirement", 1)
			if err != nil {
				t.Fatal(err)
			}
			recovered := reopened.recovered()
			if len(recovered) != 1 || recovered[0].Reporter.Pending == nil || *recovered[0].Reporter.Pending != completion || recovered[0].State != StateOwnershipLost {
				t.Fatalf("recovered=%+v want pending=%+v with ownership lost", recovered, completion)
			}
		})
	}
}

func TestRegistryJournalRejectsUnknownOutcomeOutsideRetirement(t *testing.T) {
	for _, completion := range []ReporterCompletion{
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "interrupt", Outcome: "rejected", Reason: "outcome_unknown"},
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "stop", Outcome: "rejected", Reason: "outcome_unknown"},
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "retire_after_work", Outcome: "applied", Reason: "outcome_unknown"},
		{ControlID: "33333333-3333-4333-8333-333333333333", Kind: "handoff", Outcome: "rejected", Reason: "failed"},
	} {
		t.Run(completion.Kind+"_"+completion.Outcome+"_"+completion.Reason, func(t *testing.T) {
			journal, err := openRegistryJournal(t.TempDir(), "ppm-retirement-rejection", 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.put(retirementJournalSession(t, completion)); err == nil {
				t.Fatalf("invalid reporter completion persisted: %+v", completion)
			}
		})
	}
}
