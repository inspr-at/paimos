// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

func TestJanusPrerequisiteRequiresOwnedSuccessAndCurrentSeal(t *testing.T) {
	for _, scenario := range []string{"current", "revoked", "registration_added", "next_execution", "paused", "cancelled", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			f := openAgentBridgeFixture(t, ModeAssisted)
			ctx := context.Background()
			f.registerPharos()
			janusID := f.registerJanus()
			f.reportBuildAndQA()
			if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
				t.Fatal(err)
			}
			check := func(want bool) {
				t.Helper()
				snapshot, stored := f.snapshot(), f.stored()
				state := BatchActive
				if scenario == "paused" {
					state = BatchPaused
				}
				if scenario == "cancelled" {
					state = BatchCancelled
				}
				if scenario == "next_execution" || scenario == "stale" {
					for i := range snapshot.Stages {
						stage := &snapshot.Stages[i]
						if scenario == "next_execution" && stage.StageKey == delivery.StageVerification {
							id := int64(999)
							stage.ExecutionStartEventID = &id
						}
						if scenario == "stale" && stage.StageKey == delivery.StageDeployment {
							stage.Stale = true
						}
					}
				}
				tx, err := appdb.DB.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				progress := Progress{}
				if err := projectJanusPrerequisite(ctx, tx, stored, snapshot, &progress, state); err != nil {
					t.Fatal(err)
				}
				if (progress.JanusPrerequisite != nil) != want {
					t.Fatalf("projection=%+v, want present=%v", progress.JanusPrerequisite, want)
				}
			}
			check(false)
			gen := currentGeneration(f.snapshot(), delivery.StageDeployment)
			dep, err := f.ext.CreateHandoff(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), "projection-dependency",
				externalstage.CreateHandoffRequest{StageKey: delivery.StageDeployment, ExecutionNumber: gen.ExecutionNumber,
					ExpectedPlanRevision: gen.PlanRevision, ExpectedAuthorityEpoch: gen.AuthorityEpoch,
					ReporterRegistrationID: janusID, ExpiresAt: f.now.Add(time.Hour).Format(time.RFC3339Nano)})
			if err != nil {
				t.Fatal(err)
			}
			principal := externalstage.Principal{Kind: "api_key"}
			if err := appdb.DB.QueryRow(`SELECT k.user_id,k.id FROM api_keys k JOIN external_stage_reporter_registrations r ON r.api_key_id=k.id WHERE r.id=?`, janusID).Scan(&principal.UserID, &principal.APIKeyID); err != nil {
				t.Fatal(err)
			}
			secret, err := f.ext.Mint(ctx, f.operator, dep.HandoffID, 0, false)
			if err != nil {
				t.Fatal(err)
			}
			observed := f.now.Format(time.RFC3339Nano)
			if _, err := f.ext.Accept(ctx, principal, dep.HandoffID, "projection-accept", secret, externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ext.Report(ctx, principal, dep.HandoffID, "projection-active", secret, externalstage.ReportRequest{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observed}); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Second)
			observed = f.now.Format(time.RFC3339Nano)
			authorized := true
			if _, err := f.ext.Report(ctx, principal, dep.HandoffID, "projection-success", secret, externalstage.ReportRequest{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed,
				JanusEvidence: &externalstage.JanusEvidence{Kind: externalstage.EvidenceKindAuthorization, Result: externalstage.EvidenceResultSatisfied, Authorized: &authorized, ObservedAt: observed}}); err != nil {
				t.Fatal(err)
			}
			check(false) // A dependency alone does not prove the owned deployment succeeded.
			owner := f.liveHandoff(delivery.StageDeployment)
			ownerSecret, err := f.ext.Mint(ctx, f.operator, owner.HandoffID, 0, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.ext.Accept(ctx, f.reporter, owner.HandoffID, "projection-owner-accept", ownerSecret, externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ext.ReportV2(ctx, f.reporter, owner.HandoffID, "projection-owner-active", ownerSecret, externalstage.ReportRequestV2{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observed}); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Second)
			observed = f.now.Format(time.RFC3339Nano)
			evidence := f.artifact(externalstage.EvidenceKindDeployment)
			evidence.ObservedAt = observed
			if _, err := f.ext.ReportV2(ctx, f.reporter, owner.HandoffID, "projection-owner-success", ownerSecret, externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed, PharosEvidence: &evidence}); err != nil {
				t.Fatal(err)
			}
			if scenario == "revoked" {
				if _, err := f.ext.RevokeReporter(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), "projection-revoke", janusID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "registration_added" {
				f.registerJanusAs("new-required-check", "new")
			}
			check(scenario == "current")
			if scenario == "current" {
				batch, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
				if err != nil {
					t.Fatal(err)
				}
				if batch.Progress.JanusPrerequisite == nil {
					t.Fatalf("public batch projection lost evidence: status=%s setup=%s", batch.Status, batch.Progress.SetupRequired)
				}
				workflow, err := f.svc.Workflow(ctx, f.actor, f.projectID)
				if err != nil {
					t.Fatal(err)
				}
				if workflow.ActiveBatch == nil || workflow.ActiveBatch.Progress.JanusPrerequisite == nil {
					t.Fatal("Flow workflow lost current evidence")
				}
			}
		})
	}
}

func TestJanusPrerequisiteCannotBeImportedThroughBatchJSON(t *testing.T) {
	original := Batch{Progress: Progress{JanusPrerequisite: &JanusPrerequisiteEvidence{HandoffID: "private-owner", StageKey: "deployment", ObservedAt: "2026-09-14T10:00:00Z"}}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var imported Batch
	if err := json.Unmarshal(encoded, &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Progress.JanusPrerequisite != nil {
		t.Fatal("internal evidence crossed JSON boundary")
	}
	if err := json.Unmarshal([]byte(`{"progress":{"JanusPrerequisite":{"HandoffID":"forged"},"janus_prerequisite":{"handoff_id":"forged"}}}`), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Progress.JanusPrerequisite != nil {
		t.Fatal("imported claim became internal evidence")
	}
}
