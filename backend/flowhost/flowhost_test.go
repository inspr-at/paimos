// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package flowhost

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/delivery"
)

func TestImportedClaimIsNotRequirementsPass(t *testing.T) {
	principal, err := auth.NewSessionPrincipal("12345678-1234-4234-9234-123456789abc", 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	reviewID := int64(9)
	now := time.Date(2026, 9, 8, 8, 41, 0, 0, time.UTC)
	imported := BuildInput{
		Principal: principal, ProjectID: 11, ProjectName: "Stream", AppVersion: "26.09.08",
		Workflow: baselinebatch.Workflow{
			ProjectID: 11, INSPRStreamEnabled: true,
			Draft: &baselinebatch.Draft{
				ID: 3, ReviewValid: false,
				Baseline: baselinebatch.BaselineClaim{
					BaselineRef: "baseline:v1", ContentDigest: "sha256:" + strings.Repeat("ab", 32),
					Authenticity:              baselinebatch.ImportedClaimAuthenticity,
					ImportedClaimedApprovedBy: "party:forged",
				},
				Requirements: []baselinebatch.Requirement{{Ref: "req.login", Statement: "Users sign in"}},
			},
		},
		Now: now,
	}
	state, err := Build(imported)
	if err != nil {
		t.Fatal(err)
	}
	walkForbidden(t, state)
	if state.IdentityContext.PrincipalKind != "local_host" {
		t.Fatalf("principal_kind=%s", state.IdentityContext.PrincipalKind)
	}
	if state.IdentityContext.OrganizationRef != nil {
		t.Fatalf("invented org %v", state.IdentityContext.OrganizationRef)
	}
	if state.IdentityContext.Display.FixtureLabel == "" || strings.Contains(strings.ToLower(state.IdentityContext.Display.UserLabel), "@") {
		t.Fatalf("display %+v", state.IdentityContext.Display)
	}
	if state.Prerequisites["requirementsBaseline"].Status != "unknown" {
		t.Fatalf("imported claim treated as pass: %+v", state.Prerequisites["requirementsBaseline"])
	}
	if state.Prerequisites["pharosTarget"].Status != "unknown" || state.Prerequisites["janusGate"].Status != "unknown" {
		t.Fatalf("invented downstream %+v", state.Prerequisites)
	}
	if state.Delivery["stageEvidence"].([]string)[0] != "unknown" {
		t.Fatalf("stage0=%v", state.Delivery["stageEvidence"])
	}
	if state.Progress["overall"].(ProgressSnapshot).Forecast.Kind != "educated_guess" {
		t.Fatalf("forecast %+v", state.Progress["overall"])
	}

	reviewed := imported
	reviewed.Workflow.Draft.ReviewValid = true
	reviewed.Workflow.Draft.ReviewID = &reviewID
	pass, err := Build(reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if pass.Prerequisites["requirementsBaseline"].Status != "pass" {
		t.Fatalf("current review not mapped: %+v", pass.Prerequisites["requirementsBaseline"])
	}
	if pass.Delivery["stageEvidence"].([]string)[0] != "performed" {
		t.Fatalf("reviewed stage0=%v", pass.Delivery["stageEvidence"])
	}
}

func TestPausedBatchStaysHonestAndUnknownPharos(t *testing.T) {
	principal, err := auth.NewSessionPrincipal("12345678-1234-4234-9234-123456789abc", 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	percent := 40.0
	eta := int64(1800)
	state, err := Build(BuildInput{
		Principal: principal, ProjectID: 4, ProjectName: "Paused",
		Workflow: baselinebatch.Workflow{
			INSPRStreamEnabled: true,
			ActiveBatch: &baselinebatch.Batch{
				ID: 8, BatchKey: "batch-8", Status: baselinebatch.BatchPaused,
				ControlState: baselinebatch.ControlPaused, ReviewID: 3,
				Progress: baselinebatch.Progress{
					Stages: []baselinebatch.StageView{
						{StageKey: delivery.StageImplementation, Performed: false, Stale: false},
					},
					SetupRequired: baselinebatch.SetupRequiredPharosRegistration,
				},
				Forecasts: []baselinebatch.Forecast{{
					Subject: "overall", Percent: percent, ETASeconds: &eta,
					Kind: baselinebatch.ForecastGuess, Label: "guessed", Observed: false, Fresh: false,
					AsOf: "2026-09-08T08:00:00.000Z",
				}},
			},
		},
		Now: time.Date(2026, 9, 8, 8, 41, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Delivery["status"] != "blocked" {
		t.Fatalf("paused status=%v", state.Delivery["status"])
	}
	if state.Prerequisites["pharosTarget"].Status != "unknown" || state.Prerequisites["pharosTarget"].TargetRef != nil {
		t.Fatalf("pharos %+v", state.Prerequisites["pharosTarget"])
	}
	if !strings.Contains(state.Progress["freshnessLabel"].(string), "Paused") {
		t.Fatalf("freshness=%v", state.Progress["freshnessLabel"])
	}
	overall := state.Progress["overall"].(ProgressSnapshot)
	if overall.Forecast.PercentComplete != percent || overall.Forecast.Kind != "educated_guess" {
		t.Fatalf("percent/kind %+v", overall.Forecast)
	}
}

func TestDisabledStreamFailsClosed(t *testing.T) {
	principal, err := auth.NewSessionPrincipal("12345678-1234-4234-9234-123456789abc", 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(BuildInput{Principal: principal, ProjectID: 1, Workflow: baselinebatch.Workflow{}, Now: time.Now().UTC()})
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("err=%v", err)
	}
}

func TestStartIntentRoutesWithoutExecuting(t *testing.T) {
	principal, err := auth.NewSessionPrincipal("12345678-1234-4234-9234-123456789abc", 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := Build(BuildInput{
		Principal: principal, ProjectID: 15, ProjectName: "Host",
		Workflow: baselinebatch.Workflow{INSPRStreamEnabled: true},
		Now:      time.Date(2026, 9, 8, 8, 41, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := json.Marshal(map[string]any{
		"status": "present", "principalRef": state.IdentityContext.PrincipalRef,
		"projectRef": state.IdentityContext.ProjectRef, "actorKind": state.IdentityContext.ActorKind,
		"bindingRef": state.IdentityContext.BindingRef, "contextRevision": state.IdentityContext.ContextRevision,
		"issuedAt": state.IdentityContext.IssuedAt, "expiresAt": state.IdentityContext.ExpiresAt,
		"freshUntil": state.IdentityContext.FreshUntil,
	})
	result, status, err := Handle(state, principal, 15, IntentRequest{Type: IntentStart, Identity: binding}, time.Date(2026, 9, 8, 8, 41, 5, 0, time.UTC))
	if err != nil || status != 200 || result.Executed || result.Location != "/projects/15?tab=overview#baseline-batch" {
		t.Fatalf("start %+v status=%d err=%v", result, status, err)
	}

	agent, err := auth.NewAPIKeyPrincipal(9, 2, auth.ScopeSet{auth.ScopeAll: {}})
	if err != nil {
		t.Fatal(err)
	}
	agentState, err := Build(BuildInput{
		Principal: agent, ProjectID: 15, ProjectName: "Host",
		Workflow: baselinebatch.Workflow{INSPRStreamEnabled: true},
		Now:      time.Date(2026, 9, 8, 8, 41, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	agentBinding, _ := json.Marshal(map[string]any{
		"status": "present", "principalRef": agentState.IdentityContext.PrincipalRef,
		"projectRef": agentState.IdentityContext.ProjectRef, "actorKind": agentState.IdentityContext.ActorKind,
		"bindingRef": agentState.IdentityContext.BindingRef, "contextRevision": agentState.IdentityContext.ContextRevision,
		"issuedAt": agentState.IdentityContext.IssuedAt, "expiresAt": agentState.IdentityContext.ExpiresAt,
		"freshUntil": agentState.IdentityContext.FreshUntil,
	})
	denied, deniedStatus, deniedErr := Handle(agentState, agent, 15, IntentRequest{Type: IntentStart, Identity: agentBinding}, time.Date(2026, 9, 8, 8, 41, 5, 0, time.UTC))
	if deniedErr != ErrForbidden || deniedStatus != 403 || denied.Executed {
		t.Fatalf("agent start %+v status=%d err=%v", denied, deniedStatus, deniedErr)
	}

	stale, staleStatus, staleErr := Handle(state, principal, 15, IntentRequest{Type: IntentReview, Identity: binding}, time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC))
	if staleErr != ErrStale || staleStatus != 409 || stale.Executed {
		t.Fatalf("stale %+v status=%d err=%v", stale, staleStatus, staleErr)
	}
}

func TestNextTenMinuteBoundary(t *testing.T) {
	got := NextTenMinuteBoundary(time.Date(2026, 9, 8, 8, 41, 0, 0, time.UTC))
	if got != time.Date(2026, 9, 8, 8, 50, 0, 0, time.UTC) {
		t.Fatalf("got %s", got)
	}
}

func walkForbidden(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	walkValue(t, decoded, "$")
}

func walkValue(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case []any:
		for i, child := range typed {
			walkValue(t, child, path+"[]")
			_ = i
		}
	case map[string]any:
		for key, child := range typed {
			if forbiddenKey(key) {
				t.Fatalf("forbidden key %s at %s", key, path)
			}
			walkValue(t, child, path+"."+key)
		}
	case string:
		if strings.Contains(typed, "@") || strings.Contains(typed, "party:forged") {
			t.Fatalf("forbidden string %q at %s", typed, path)
		}
	}
}

func forbiddenKey(key string) bool {
	switch strings.ToLower(key) {
	case "token", "secret", "cookie", "email", "session", "subject", "sub", "role", "roles", "password", "authorization", "bearer", "csrf":
		return true
	default:
		return strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "email")
	}
}
