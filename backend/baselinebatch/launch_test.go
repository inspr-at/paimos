// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/contracts"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const (
	testReviewedPlanDigest = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	testOperationDigest    = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
)

func TestDelegatedLaunchPatchDistinguishesOmittedFromExplicitOff(t *testing.T) {
	var omitted PatchDraftRequest
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil || omitted.DelegatedLaunch.Set {
		t.Fatalf("omitted patch=%+v err=%v", omitted.DelegatedLaunch, err)
	}
	var disabled PatchDraftRequest
	if err := json.Unmarshal([]byte(`{"delegated_launch":null}`), &disabled); err != nil ||
		!disabled.DelegatedLaunch.Set || disabled.DelegatedLaunch.Value != nil {
		t.Fatalf("disabled patch=%+v err=%v", disabled.DelegatedLaunch, err)
	}
	var open PatchDraftRequest
	if err := json.Unmarshal([]byte(`{"delegated_launch":{"target_ref":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","workflow":"deploy-production","environment":"production","expires_at":"2026-09-09T12:00:00Z","max_launches":1,"extra":true}}`), &open); err == nil {
		t.Fatal("delegated launch patch accepted an unknown field")
	}
}

func prepareDelegatedLaunch(t *testing.T) (*bridgeFixture, storedHandoff, []byte, externalstage.LaunchCandidate) {
	t.Helper()
	f := openDelegatedBridgeFixture(t)
	f.registerPharos()
	f.reportBuildAndQA()
	got := f.authorizeHandoff()
	if got.LaunchGrant == nil || got.LaunchGrant.TargetRef != f.targetRef || got.LaunchGrant.MaxLaunches != 1 ||
		got.LaunchGrant.UsedLaunches != 0 || got.LaunchGrant.State != "active" {
		t.Fatalf("launch grant=%+v", got.LaunchGrant)
	}
	handoff := f.liveHandoff("deployment")
	secret, err := f.ext.Mint(context.Background(), f.operator, handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.Accept(context.Background(), f.reporter, handoff.HandoffID, "accept-launch", secret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: f.now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	candidate := externalstage.LaunchCandidate{
		Schema: externalstage.LaunchAdmissionSchema, Version: externalstage.LaunchAdmissionVersion,
		TargetRef: f.targetRef, Workflow: delegatedLaunchWorkflow, Environment: "production-eu1",
		Artifact:           f.artifact(externalstage.EvidenceKindDeployment).Artifact,
		ReviewedPlanDigest: testReviewedPlanDigest, OperationBindingDigest: testOperationDigest,
		ObservedAt: f.now.Format(time.RFC3339Nano),
	}
	return f, handoff, secret, candidate
}

func TestAutomaticModeDoesNotMintLaunchAuthorityByDefault(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAutomatic)
	if f.batch.DelegatedLaunch != nil || f.batch.LaunchGrant != nil {
		t.Fatalf("automatic mode silently minted launch authority: %+v", f.batch)
	}
}

func TestDelegatedLaunchDoesNotFallBackToSoleLegacyRegistration(t *testing.T) {
	f := openDelegatedBridgeFixture(t)
	targetRef := f.targetRef
	f.targetRef = ""
	f.registerPharos()
	f.targetRef = targetRef
	registrationID, err := f.svc.currentPharosOwnerRegistration(context.Background(), f.operator,
		fmt.Sprintf("issue:%d", f.batch.IssueID), f.batch.DelegatedLaunch)
	if err != nil || registrationID != 0 {
		t.Fatalf("delegated launch selected sole targetless registration id=%d err=%v", registrationID, err)
	}
}

func TestDelegatedLaunchCandidateCannotReflectHandoffSecret(t *testing.T) {
	f, handoff, secret, candidate := prepareDelegatedLaunch(t)
	candidate.ReviewedPlanDigest = "sha256:" + hex.EncodeToString(secret)
	if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
		"secret-reflection", secret, candidate); !errors.Is(err, externalstage.ErrInvalid) {
		t.Fatalf("secret reflection err=%v", err)
	}
}

func TestDelegatedLaunchAdmissionAndConsumeAreExactlyOnceUnderConcurrency(t *testing.T) {
	f, handoff, secret, candidate := prepareDelegatedLaunch(t)
	ctx := context.Background()

	const workers = 32
	responses := make([][]byte, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			responses[index], errs[index] = f.ext.AdmitLaunch(ctx, f.reporter, handoff.HandoffID,
				"launch-candidate-one", secret, candidate)
		}()
	}
	wait.Wait()
	for index := range workers {
		if errs[index] != nil {
			t.Fatalf("candidate worker %d: %v", index, errs[index])
		}
		if !bytes.Equal(responses[0], responses[index]) {
			t.Fatalf("candidate worker %d returned different durable bytes", index)
		}
	}
	var admission externalstage.LaunchAdmission
	if err := json.Unmarshal(responses[0], &admission); err != nil {
		t.Fatal(err)
	}
	if admission.TargetRef != f.targetRef || admission.MaxLaunches != 1 || admission.UsedLaunches != 0 ||
		admission.State != "issued" || admission.CredentialEpoch != 1 {
		t.Fatalf("admission=%+v", admission)
	}
	changed := candidate
	changed.OperationBindingDigest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	if _, err := f.ext.AdmitLaunch(ctx, f.reporter, handoff.HandoffID, "launch-candidate-one", secret, changed); !errors.Is(err, externalstage.ErrConflict) {
		t.Fatalf("changed candidate err=%v", err)
	}

	request := externalstage.ConsumeLaunchAdmissionRequest{Schema: externalstage.LaunchAdmissionSchema,
		Version: externalstage.LaunchAdmissionVersion, AdmissionDigest: admission.AdmissionDigest}
	receipts := make([][]byte, workers)
	errs = make([]error, workers)
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			receipts[index], errs[index] = f.ext.ConsumeLaunch(ctx, f.reporter, handoff.HandoffID,
				admission.AdmissionID, "launch-consume-one", secret, request)
		}()
	}
	wait.Wait()
	for index := range workers {
		if errs[index] != nil {
			t.Fatalf("consume worker %d: %v", index, errs[index])
		}
		if !bytes.Equal(receipts[0], receipts[index]) {
			t.Fatalf("consume worker %d returned different durable bytes", index)
		}
	}
	var receipt externalstage.LaunchReceipt
	if err := json.Unmarshal(receipts[0], &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LaunchNumber != 1 || receipt.State != "consumed" || receipt.CredentialEpoch != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	var admissionCount, consumedCount int
	if err := appdb.DB.QueryRow(`SELECT COUNT(*),SUM(state='consumed') FROM external_stage_launch_admissions WHERE grant_id=?`,
		admission.GrantID).Scan(&admissionCount, &consumedCount); err != nil {
		t.Fatal(err)
	}
	if admissionCount != 1 || consumedCount != 1 {
		t.Fatalf("admissions=%d consumed=%d", admissionCount, consumedCount)
	}
	if _, err := f.svc.Control(ctx, f.actor, f.projectID, f.batch.ID,
		ControlRequest{Action: "revoke_launch", RequestKey: "97800000-0000-4000-8000-000000000003"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoke consumed grant err=%v", err)
	}

	// A consumed receipt is historical idempotent evidence. Later authority
	// expiry must not turn its exact replay into a fresh consume or alter bytes.
	f.now = f.now.Add(2 * time.Hour)
	replayed, err := f.ext.ConsumeLaunch(ctx, f.reporter, handoff.HandoffID, admission.AdmissionID,
		"launch-consume-one", secret, request)
	if err != nil || !bytes.Equal(receipts[0], replayed) {
		t.Fatalf("historical receipt replay err=%v equal=%v", err, bytes.Equal(receipts[0], replayed))
	}
}

func TestDelegatedLaunchAdmissionAndConsumeSurviveRollbackAndRestart(t *testing.T) {
	f, handoff, secret, candidate := prepareDelegatedLaunch(t)
	ctx := context.Background()
	if _, err := appdb.DB.Exec(`CREATE TRIGGER test_launch_admit_crash BEFORE INSERT ON external_stage_launch_admissions
		BEGIN SELECT RAISE(ABORT,'injected launch admission crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.AdmitLaunch(ctx, f.reporter, handoff.HandoffID, "rollback-candidate", secret, candidate); err == nil {
		t.Fatal("admission unexpectedly survived injected persistence failure")
	}
	var count int
	if err := appdb.DB.QueryRow(`SELECT COUNT(*) FROM external_stage_launch_admissions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("admission rollback count=%d err=%v", count, err)
	}
	if _, err := appdb.DB.Exec(`DROP TRIGGER test_launch_admit_crash`); err != nil {
		t.Fatal(err)
	}
	admissionRaw, err := f.ext.AdmitLaunch(ctx, f.reporter, handoff.HandoffID, "rollback-candidate", secret, candidate)
	if err != nil {
		t.Fatal(err)
	}
	var admission externalstage.LaunchAdmission
	if err := json.Unmarshal(admissionRaw, &admission); err != nil {
		t.Fatal(err)
	}
	request := externalstage.ConsumeLaunchAdmissionRequest{Schema: externalstage.LaunchAdmissionSchema,
		Version: externalstage.LaunchAdmissionVersion, AdmissionDigest: admission.AdmissionDigest}
	if _, err := appdb.DB.Exec(`CREATE TRIGGER test_launch_consume_crash BEFORE UPDATE OF state ON external_stage_launch_admissions
		WHEN OLD.state='issued' BEGIN SELECT RAISE(ABORT,'injected launch consume crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.ConsumeLaunch(ctx, f.reporter, handoff.HandoffID, admission.AdmissionID,
		"rollback-consume", secret, request); err == nil {
		t.Fatal("consume unexpectedly survived injected persistence failure")
	}
	var state string
	var receipt []byte
	if err := appdb.DB.QueryRow(`SELECT state,receipt_bytes FROM external_stage_launch_admissions WHERE admission_id=?`,
		admission.AdmissionID).Scan(&state, &receipt); err != nil || state != "issued" || receipt != nil {
		t.Fatalf("consume rollback state=%q receipt=%q err=%v", state, receipt, err)
	}
	if _, err := appdb.DB.Exec(`DROP TRIGGER test_launch_consume_crash`); err != nil {
		t.Fatal(err)
	}
	receiptRaw, err := f.ext.ConsumeLaunch(ctx, f.reporter, handoff.HandoffID, admission.AdmissionID,
		"rollback-consume", secret, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := appdb.DB.Close(); err != nil {
		t.Fatal(err)
	}
	appdb.DB = nil
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	restarted, err := externalstage.NewService(appdb.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: &f.now},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.ConsumeLaunch(ctx, f.reporter, handoff.HandoffID, admission.AdmissionID,
		"rollback-consume", secret, request)
	if err != nil || !bytes.Equal(receiptRaw, replayed) {
		t.Fatalf("restart receipt replay err=%v equal=%v", err, bytes.Equal(receiptRaw, replayed))
	}
}

func TestDelegatedLaunchRefusesTargetDriftAndHumanRevocation(t *testing.T) {
	t.Run("target drift", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		if _, err := appdb.DB.Exec(`UPDATE project_environments SET host_alias='changed-host',updated_at=datetime('now','+1 second')
			WHERE project_id=?`, f.projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"drifted-target", secret, candidate); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("target drift err=%v", err)
		}
	})

	t.Run("human revoke", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		raw, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"revoke-candidate", secret, candidate)
		if err != nil {
			t.Fatal(err)
		}
		var admission externalstage.LaunchAdmission
		if err := json.Unmarshal(raw, &admission); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Control(context.Background(), f.actor, f.projectID, f.batch.ID,
			ControlRequest{Action: "revoke_launch", RequestKey: "97800000-0000-4000-8000-000000000001"}); err != nil {
			t.Fatal(err)
		}
		request := externalstage.ConsumeLaunchAdmissionRequest{Schema: externalstage.LaunchAdmissionSchema,
			Version: externalstage.LaunchAdmissionVersion, AdmissionDigest: admission.AdmissionDigest}
		if _, err := f.ext.ConsumeLaunch(context.Background(), f.reporter, handoff.HandoffID, admission.AdmissionID,
			"consume-after-revoke", secret, request); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("consume after revoke err=%v", err)
		}
	})

	t.Run("paused batch", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		raw, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"pause-candidate", secret, candidate)
		if err != nil {
			t.Fatal(err)
		}
		var admission externalstage.LaunchAdmission
		if err := json.Unmarshal(raw, &admission); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Control(context.Background(), f.actor, f.projectID, f.batch.ID,
			ControlRequest{Action: "pause", RequestKey: "97800000-0000-4000-8000-000000000004"}); err != nil {
			t.Fatal(err)
		}
		request := externalstage.ConsumeLaunchAdmissionRequest{Schema: externalstage.LaunchAdmissionSchema,
			Version: externalstage.LaunchAdmissionVersion, AdmissionDigest: admission.AdmissionDigest}
		if _, err := f.ext.ConsumeLaunch(context.Background(), f.reporter, handoff.HandoffID, admission.AdmissionID,
			"consume-paused", secret, request); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("consume paused err=%v", err)
		}
	})

	t.Run("credential rotation", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		raw, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"rotate-candidate", secret, candidate)
		if err != nil {
			t.Fatal(err)
		}
		var admission externalstage.LaunchAdmission
		if err := json.Unmarshal(raw, &admission); err != nil {
			t.Fatal(err)
		}
		rotated, err := f.ext.Mint(context.Background(), f.operator, handoff.HandoffID, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		request := externalstage.ConsumeLaunchAdmissionRequest{Schema: externalstage.LaunchAdmissionSchema,
			Version: externalstage.LaunchAdmissionVersion, AdmissionDigest: admission.AdmissionDigest}
		if _, err := f.ext.ConsumeLaunch(context.Background(), f.reporter, handoff.HandoffID, admission.AdmissionID,
			"consume-rotated", rotated, request); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("consume rotated err=%v", err)
		}
	})

	t.Run("expired root grant", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		f.now = f.now.Add(2 * time.Hour)
		if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"expired-candidate", secret, candidate); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("expired root err=%v", err)
		}
	})

	t.Run("granting human loses project authority", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		if _, err := appdb.DB.Exec(`UPDATE users SET role='external',is_super_admin=0 WHERE id=?`, f.userID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"authority-revoked", secret, candidate); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("lost authority err=%v", err)
		}
	})

	t.Run("review binding drift", func(t *testing.T) {
		f, handoff, secret, candidate := prepareDelegatedLaunch(t)
		if _, err := appdb.DB.Exec(`UPDATE baseline_batch_reviews SET binding_hash=? WHERE id=?`,
			"0000000000000000000000000000000000000000000000000000000000000000", f.batch.ReviewID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
			"review-drift", secret, candidate); !errors.Is(err, externalstage.ErrConflict) {
			t.Fatalf("review drift err=%v", err)
		}
	})
}

func TestDelegatedLaunchRefusesIssuedAdmissionReplayAfterAuthorityLoss(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   error
		mutate func(*testing.T, *bridgeFixture, storedHandoff, *[]byte)
	}{
		{"human revoke", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := f.svc.Control(context.Background(), f.actor, f.projectID, f.batch.ID,
				ControlRequest{Action: "revoke_launch", RequestKey: "97800000-0000-4000-8000-000000000011"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"paused batch", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := f.svc.Control(context.Background(), f.actor, f.projectID, f.batch.ID,
				ControlRequest{Action: "pause", RequestKey: "97800000-0000-4000-8000-000000000012"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"credential rotation current secret", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, handoff storedHandoff, secret *[]byte) {
			rotated, err := f.ext.Mint(context.Background(), f.operator, handoff.HandoffID, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			*secret = rotated
		}},
		{"credential rotation stale secret", externalstage.ErrNotFound, func(t *testing.T, f *bridgeFixture, handoff storedHandoff, _ *[]byte) {
			if _, err := f.ext.Mint(context.Background(), f.operator, handoff.HandoffID, 1, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"target drift", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := appdb.DB.Exec(`UPDATE project_environments SET host_alias='changed-host',updated_at=datetime('now','+1 second')
				WHERE project_id=?`, f.projectID); err != nil {
				t.Fatal(err)
			}
		}},
		{"expired root and admission", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			f.now = f.now.Add(2 * time.Hour)
		}},
		{"granting human loses project authority", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := appdb.DB.Exec(`UPDATE users SET role='external',is_super_admin=0 WHERE id=?`, f.userID); err != nil {
				t.Fatal(err)
			}
		}},
		{"review binding drift", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := appdb.DB.Exec(`UPDATE baseline_batch_reviews SET binding_hash=? WHERE id=?`,
				"0000000000000000000000000000000000000000000000000000000000000000", f.batch.ReviewID); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalidated review", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := appdb.DB.Exec(`UPDATE baseline_batch_reviews SET invalidated_at=datetime('now') WHERE id=?`, f.batch.ReviewID); err != nil {
				t.Fatal(err)
			}
		}},
		{"human session expired", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := appdb.DB.Exec(`UPDATE sessions SET expires_at=? WHERE credential_id=?`,
				f.now.Add(-time.Minute).Format(time.RFC3339Nano), f.actor.SessionCredentialID); err != nil {
				t.Fatal(err)
			}
		}},
		{"lifecycle cancelled", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := f.svc.Control(context.Background(), f.actor, f.projectID, f.batch.ID,
				ControlRequest{Action: "cancel", RequestKey: "97800000-0000-4000-8000-000000000013"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"qa predecessor lost", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if f.batch.DeliveryID == nil || f.batch.AttemptID == nil {
				t.Fatal("missing delivery")
			}
			if _, err := appdb.DB.Exec(`UPDATE delivery_stage_latest SET semantic_stage_event_id=NULL
				WHERE delivery_id=? AND attempt_id=? AND stage_key='qa'`, *f.batch.DeliveryID, *f.batch.AttemptID); err != nil {
				t.Fatal(err)
			}
		}},
		{"execution authority drift", externalstage.ErrNotFound, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if f.batch.DeliveryID == nil || f.batch.AttemptID == nil {
				t.Fatal("missing delivery")
			}
			if _, err := appdb.DB.Exec(`DELETE FROM delivery_stage_latest
				WHERE delivery_id=? AND attempt_id=? AND stage_key='deployment'`, *f.batch.DeliveryID, *f.batch.AttemptID); err != nil {
				t.Fatal(err)
			}
		}},
		{"registration revoked", externalstage.ErrNotFound, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			var registrationID int64
			if err := appdb.DB.QueryRow(`SELECT id FROM external_stage_reporter_registrations
				WHERE project_id=? AND reporter_role='owner' AND revoked_at IS NULL`, f.projectID).Scan(&registrationID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ext.RevokeReporter(context.Background(), f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID),
				"replay-registration-revoke", registrationID); err != nil {
				t.Fatal(err)
			}
		}},
		{"draft binding opened", externalstage.ErrConflict, func(t *testing.T, f *bridgeFixture, _ storedHandoff, _ *[]byte) {
			if _, err := appdb.DB.Exec(`UPDATE baseline_batch_drafts SET status='open' WHERE id=?`, f.batch.DraftID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, handoff, secret, candidate := prepareDelegatedLaunch(t)
			if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
				"issued-replay", secret, candidate); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, f, handoff, &secret)
			if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID,
				"issued-replay", secret, candidate); !errors.Is(err, tc.want) {
				t.Fatalf("issued admission replay err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestDelegatedLaunchCancelBeforeWriterDoesNotSpendAuthority(t *testing.T) {
	f, handoff, secret, candidate := prepareDelegatedLaunch(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.ext.AdmitLaunch(ctx, f.reporter, handoff.HandoffID, "cancelled-candidate", secret, candidate); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admit err=%v", err)
	}
	var count int
	if err := appdb.DB.QueryRow(`SELECT COUNT(*) FROM external_stage_launch_admissions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled admit spent authority count=%d err=%v", count, err)
	}
	if _, err := f.ext.AdmitLaunch(context.Background(), f.reporter, handoff.HandoffID, "cancelled-candidate", secret, candidate); err != nil {
		t.Fatalf("fresh admit after cancel err=%v", err)
	}
}
