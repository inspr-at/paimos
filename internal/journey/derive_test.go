// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"testing"
	"time"
)

func TestStageRailReportsExactGate(t *testing.T) {
	f := facts{
		Revision:           3,
		RequirementsDigest: "digest",
		BuildGateID:        "build-gate",
		GateLiveByID: map[string]bool{
			"build-gate": true,
		},
	}
	stage := func(key string) JourneyStage {
		t.Helper()
		for _, item := range stageRail(f, stageBuild, false) {
			if item.Key == key {
				return item
			}
		}
		t.Fatalf("missing stage %s", key)
		return JourneyStage{}
	}
	if got := stage(stagePlan); got.GateScope != ScopeBuild || !got.GateLive || ptrVal(got.GateApprovalID) != "build-gate" {
		t.Fatalf("plan gate: %+v", got)
	}
	if got := stage(stageBuild); got.GateScope != ScopeCandidate || got.GateLive || got.GateApprovalID != nil {
		t.Fatalf("build stage must report absent candidate gate: %+v", got)
	}
	if got := stage(stageRequirements); got.GateScope != requirementsScope(3, "digest") {
		t.Fatalf("requirements scope: %+v", got)
	}
	f.CandidateGateID = "candidate-gate"
	f.GateLiveByID["candidate-gate"] = true
	if got := stage(stageBuild); got.GateScope != ScopeCandidate || !got.GateLive || ptrVal(got.GateApprovalID) != "candidate-gate" {
		t.Fatalf("candidate gate: %+v", got)
	}
}

func TestGateOfferStatesInJourneyProjection(t *testing.T) {
	const expiry = "2026-09-27T09:30:00Z"
	for _, tc := range []struct {
		name, decision, want                                    string
		requestOpen, grantExists, revoked, grantLive, available bool
	}{
		{"pending", "", "pending", true, false, false, false, false},
		{"approved_live", "approved", "approved_live", true, true, false, true, true},
		{"expired pending request", "", "expired", false, false, false, false, false},
		{"expired request", "approved", "expired", false, true, false, true, false},
		{"expired grant", "approved", "expired", true, true, false, false, false},
		{"revoked", "approved", "revoked", true, true, true, false, false},
		{"rejected", "denied", "rejected", true, false, false, false, false},
		{"grant missing", "approved", "grant_missing", true, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := gateOfferState(tc.decision, tc.requestOpen, tc.grantExists, tc.revoked, tc.grantLive)
			if state != tc.want {
				t.Fatalf("state = %q, want %q", state, tc.want)
			}
			f := facts{
				Profile: "personal", ImportedStage: stageBuild,
				Release:   &releaseFacts{ID: "release", Number: 1, State: "candidate"},
				Candidate: gateOffer{ID: "request", State: state, Live: state == "approved_live", ExpiresAt: expiry, DecidedBy: tc.decision},
			}
			projection := derive(f)
			stage := projection.Stages[stageIndex(stageBuild)]
			if stage.GateOfferState != tc.want || ptrVal(stage.GateOfferID) != "request" || stage.GateOfferExpiresAt != expiry {
				t.Fatalf("build gate offer = %+v", stage)
			}
			if projection.NextAction.Available != tc.available {
				t.Fatalf("next action = %+v", projection.NextAction)
			}
			if state == "expired" {
				want := "Gate approval expired. The agent asks again for a fresh one."
				if tc.decision == "" {
					want = "Gate request expired. The agent asks again for a fresh one."
				}
				if projection.NextAction.Reason != want {
					t.Fatalf("expired reason = %q, want %q", projection.NextAction.Reason, want)
				}
			}
		})
	}
}

func TestDeriveStagesAndNextAction(t *testing.T) {
	release := func(state string, access bool) *releaseFacts {
		return &releaseFacts{ID: "rel", Number: 1, State: state, AccessRequired: access}
	}
	live := func(id string) gateOffer { return gateOffer{ID: id, DecidedBy: "person", Live: true} }
	cases := []struct {
		name      string
		facts     facts
		stage     string
		next      string
		available bool
		reason    string
		shape     string
		access    string
		live      string
		blocked   bool
	}{
		{
			name:  "inspire until a brief is accepted",
			facts: facts{Profile: "personal"},
			stage: stageInspire, next: actionContinueIntake, available: true,
			shape: "later", access: "later", live: "later",
		},
		{
			name:  "confirm an accepted brief",
			facts: facts{Profile: "professional", AcceptedBrief: true},
			stage: stageInspire, next: actionConfirmBrief, available: true,
			shape: "later", access: "later", live: "later",
		},
		{
			name:  "personal skips shape after the brief",
			facts: facts{Profile: "personal", BriefConfirmed: true, RequirementCount: 1, Requirements: live("req")},
			stage: stageRequirements, next: actionApproveRequirements, available: true,
			shape: "skipped", access: "later", live: "later",
		},
		{
			name:  "professional shape waits for a gate",
			facts: facts{Profile: "professional", BriefConfirmed: true},
			stage: stageShape, next: actionDecide, reason: reasonShapeGate,
			shape: "current", access: "later", live: "later",
		},
		{
			name:  "park stays on shape",
			facts: facts{Profile: "personal", BriefConfirmed: true, Decision: "park", Shape: live("again")},
			stage: stageShape, next: actionReopen, available: true,
			shape: "current", access: "later", live: "later",
		},
		{
			name:  "go without an agreement returns to requirements",
			facts: facts{Profile: "professional", BriefConfirmed: true, Decision: "go"},
			stage: stageRequirements, next: actionApproveRequirements, reason: reasonReqNone,
			shape: "done", access: "later", live: "later",
		},
		{
			name: "manual scope change needs a fresh agreement",
			facts: facts{
				Profile: "professional", BriefConfirmed: true, Decision: "go",
				RequirementsRevision: 2, AgreedRequirementsRevision: 2, RequirementCount: 1,
				Release: release("planning", false), ScopeRevision: true,
			},
			stage: stageRequirements, next: actionApproveRequirements, reason: reasonScope,
			shape: "done", access: "later", live: "later",
		},
		{
			name: "plan without tickets",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("planning", false),
			},
			stage: stagePlan, next: actionStartBuild, reason: reasonNoTickets,
			shape: "skipped", access: "skipped", live: "later",
		},
		{
			name: "professional cap blocks the plan",
			facts: facts{
				Profile: "professional", BriefConfirmed: true, Decision: "go",
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("planning", false), IncludedTickets: 1,
				HasCap: true, CapCents: 1000, PlanCents: 1200, Build: live("build"),
			},
			stage: stagePlan, next: actionStartBuild, reason: reasonOverCap,
			shape: "done", access: "skipped", live: "later",
		},
		{
			name: "spent hours count against the cap",
			facts: facts{
				Profile: "enterprise", BriefConfirmed: true, Decision: "go",
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("planning", false), IncludedTickets: 1,
				HasCap: true, CapCents: 1000, PlanCents: 600, SpentCents: 500,
			},
			stage: stagePlan, next: actionStartBuild, reason: reasonOverCap,
			shape: "done", access: "skipped", live: "later",
		},
		{
			name: "start build when the gate and the cap agree",
			facts: facts{
				Profile: "professional", BriefConfirmed: true, Decision: "reduce_scope",
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("planning", false), IncludedTickets: 2,
				HasCap: true, CapCents: 1000, PlanCents: 400, SpentCents: 500, Build: live("build"),
			},
			stage: stagePlan, next: actionStartBuild, available: true,
			shape: "done", access: "skipped", live: "later",
		},
		{
			name: "active build is a passive wait",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("building", false), IncludedTickets: 1, OpenReleaseTickets: 1,
			},
			stage: stageBuild, next: actionWaitForBuild, reason: reasonBuilding,
			shape: "skipped", access: "skipped", live: "later",
		},
		{
			name: "enterprise reviewer must be someone else",
			facts: facts{
				Profile: "enterprise", BriefConfirmed: true, Decision: "go",
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release:     release("candidate", false),
				BriefAuthor: "author", BuildStarter: "builder",
				Candidate: gateOffer{ID: "cand", DecidedBy: "builder", Live: true},
			},
			stage: stageBuild, next: actionApproveCandidate, reason: reasonReviewer,
			shape: "done", access: "skipped", live: "later",
		},
		{
			name: "enterprise drafts block the candidate",
			facts: facts{
				Profile: "enterprise", BriefConfirmed: true, Decision: "go",
				RequirementsRevision: 1, AgreedRequirementsRevision: 1, DraftCount: 1,
				Release:     release("candidate", false),
				BriefAuthor: "author", BuildStarter: "builder",
				Candidate: live("cand"),
			},
			stage: stageBuild, next: actionApproveCandidate, reason: reasonDrafts,
			shape: "done", access: "skipped", live: "later",
		},
		{
			name: "independent candidate approval",
			facts: facts{
				Profile: "enterprise", BriefConfirmed: true, Decision: "go",
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release:     release("candidate", false),
				BriefAuthor: "author", BuildStarter: "builder", RequirementsDecider: "author",
				Candidate: gateOffer{ID: "cand", DecidedBy: "reviewer", Live: true},
			},
			stage: stageBuild, next: actionApproveCandidate, available: true,
			shape: "done", access: "skipped", live: "later",
		},
		{
			name: "missing deployment evidence blocks deploy",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("deploying", false), DeployGateID: "dep",
			},
			stage: stageDeploy, next: actionApproveDeploy, reason: reasonDeployEvidence, blocked: true,
			shape: "skipped", access: "skipped", live: "later",
		},
		{
			name: "failed deploy offers a retry",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("deploying", false), DeployOutcome: outcomeFailed,
				Deploy: live("again"),
			},
			stage: stageDeploy, next: actionRetryDeploy, available: true, blocked: true,
			shape: "skipped", access: "skipped", live: "later",
		},
		{
			name: "successful deploy without access is live",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("deploying", false), DeployGateID: "dep",
				DeployOutcome: outcomeSucceeded, VerifyOutcome: outcomeSucceeded,
			},
			stage: stageLive, next: actionPlanNext, available: true,
			shape: "skipped", access: "skipped", live: "current",
		},
		{
			name: "access change is not skipped",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release: release("deploying", true), DeployGateID: "dep",
				DeployOutcome: outcomeSucceeded, VerifyOutcome: outcomeSucceeded,
				Access: live("permit"),
			},
			stage: stageAccess, next: actionApprovePermit, available: true,
			shape: "skipped", access: "current", live: "later",
		},
		{
			name: "prior release stays live while the next plan is current",
			facts: facts{
				Profile: "personal", BriefConfirmed: true,
				RequirementsRevision: 1, AgreedRequirementsRevision: 1,
				Release:       &releaseFacts{ID: "rel2", Number: 2, State: "planning"},
				PriorReleased: true, IncludedTickets: 1, Build: live("build"),
			},
			stage: stagePlan, next: actionStartBuild, available: true,
			shape: "skipped", access: "skipped", live: "done",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derive(tc.facts)
			if got.Stage != tc.stage || got.NextAction.Key != tc.next || got.NextAction.Available != tc.available || got.NextAction.Reason != tc.reason {
				t.Fatalf("stage %s next %s available %v reason %q", got.Stage, got.NextAction.Key, got.NextAction.Available, got.NextAction.Reason)
			}
			if state(got, stageShape) != tc.shape || state(got, stageAccess) != tc.access || state(got, stageLive) != tc.live {
				t.Fatalf("shape %s access %s live %s", state(got, stageShape), state(got, stageAccess), state(got, stageLive))
			}
			wantState := "current"
			if tc.blocked {
				wantState = "blocked"
			}
			if state(got, tc.stage) != wantState {
				t.Fatalf("stage state %s", state(got, tc.stage))
			}
			if len(got.Stages) != 8 {
				t.Fatalf("stages %d", len(got.Stages))
			}
		})
	}
}

func TestFoldHandoffsUsesTheAttemptWindow(t *testing.T) {
	rows := []handoffRow{
		{ID: "old-verify", Stage: "deploy", Operation: "verify", State: "failed", Result: "failed", Attempt: 1, Epoch: 1, At: unix(10)},
		{ID: "deploy", Stage: "deploy", Operation: "deploy", State: "succeeded", Result: "succeeded", Attempt: 1, Epoch: 1, At: unix(30)},
		{ID: "verify", Stage: "deploy", Operation: "verify", State: "succeeded", Result: "succeeded", Attempt: 2, Epoch: 2, At: unix(31)},
		{ID: "apply", Stage: "access", Operation: "apply", State: "succeeded", Result: "succeeded", Attempt: 1, Epoch: 1, At: unix(40)},
	}
	deploy, access, deployOutcome, verifyOutcome, accessOutcome := foldHandoffs(rows, unix(20), unix(0))
	if deploy.ID != "verify" && deploy.ID != "deploy" {
		t.Fatalf("deploy handoff %s", deploy.ID)
	}
	if deployOutcome != outcomeSucceeded || verifyOutcome != outcomeSucceeded || accessOutcome != outcomeSucceeded || access.ID != "apply" {
		t.Fatalf("outcomes %s %s %s access %s", deployOutcome, verifyOutcome, accessOutcome, access.ID)
	}
	_, _, deployOutcome, verifyOutcome, _ = foldHandoffs(rows, unix(0), unix(0))
	if verifyOutcome != outcomeSucceeded {
		t.Fatalf("latest verify attempt should win, got %s outcome %s", verifyOutcome, deployOutcome)
	}
	if deploy.ID != "verify" || deploy.Attempt != 2 || deploy.Epoch != 2 || access.Attempt != 1 || access.Epoch != 1 {
		t.Fatalf("handoff identity: deploy=%+v access=%+v", deploy, access)
	}
}

func TestStageRailHandoffIdentity(t *testing.T) {
	f := facts{Profile: "personal", DeployHandoff: handoffIdentity{ID: "deploy-id", Attempt: 3, Epoch: 4}}
	stages := stageRail(f, stageDeploy, false)
	for _, stage := range stages {
		if stage.Key == stageDeploy {
			if stage.HandoffID == nil || *stage.HandoffID != "deploy-id" || stage.HandoffAttempt == nil || *stage.HandoffAttempt != 3 || stage.HandoffAuthorityEpoch == nil || *stage.HandoffAuthorityEpoch != 4 {
				t.Fatalf("deploy identity: %+v", stage)
			}
		} else if stage.HandoffAttempt != nil || stage.HandoffAuthorityEpoch != nil {
			t.Fatalf("unexpected handoff identity: %+v", stage)
		}
	}
}

func state(j Journey, key string) string {
	for _, stage := range j.Stages {
		if stage.Key == key {
			return stage.State
		}
	}
	return ""
}

func unix(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

func TestStageSource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts facts
		want  string
	}{
		{"native journey", facts{Profile: "personal"}, "journey"},
		{"imported plan", facts{Profile: "personal", ImportedStage: stagePlan}, "derived"},
		{"recorded current release", facts{Profile: "personal", ImportedStage: stagePlan, CurrentReleaseRecorded: true, Release: &releaseFacts{State: "planning"}}, "journey"},
		{"recorded candidate", facts{Profile: "personal", ImportedStage: stageBuild, Release: &releaseFacts{State: "candidate"}}, "journey"},
		{"imported build", facts{Profile: "personal", ImportedStage: stageBuild, Release: &releaseFacts{ID: "release", State: "planning"}}, "derived"},
		{"imported live", facts{Profile: "personal", ImportedStage: stageLive, Release: &releaseFacts{ID: "release", State: "planning"}}, "derived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := derive(tc.facts).StageSource; got != tc.want {
				t.Fatalf("stage source %q, want %q", got, tc.want)
			}
		})
	}
}
