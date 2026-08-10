// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/backend/db"

	_ "modernc.org/sqlite"
)

func TestBoundedIntakeImpactCandidates(t *testing.T) {
	issueHits := make([]map[string]any, 0, 13)
	for i := 12; i >= 1; i-- {
		issueHits = append(issueHits, map[string]any{
			"issue_key": fmt.Sprintf("ALPH-%02d", i),
			"score":     float64(i),
		})
	}
	issueHits = append(issueHits, map[string]any{"issue_key": "ALPH-12"})

	reached := map[string]bool{}
	for i := 1; i <= 30; i++ {
		reached[fmt.Sprintf("ALPH-%02d", i)] = true
	}

	got := boundedIntakeImpactCandidates(issueHits, reached, 20)
	if len(got) != 20 {
		t.Fatalf("candidate count = %d, want 20", len(got))
	}
	for i := 0; i < 12; i++ {
		want := fmt.Sprintf("ALPH-%02d", 12-i)
		if got[i].IssueKey != want || got[i].Via != "retrieval" {
			t.Fatalf("retrieval candidate %d = %+v, want key %s", i, got[i], want)
		}
	}
	for i, want := range []string{"ALPH-13", "ALPH-14", "ALPH-15", "ALPH-16", "ALPH-17", "ALPH-18", "ALPH-19", "ALPH-20"} {
		if got[12+i].IssueKey != want || got[12+i].Via != "graph" {
			t.Fatalf("graph candidate %d = %+v, want key %s", i, got[12+i], want)
		}
	}
}

func TestResolveIntakeImpactIssues_ProjectScopedAndLiveOnly(t *testing.T) {
	openIntakeTestDB(t)
	alphaID, betaID := seedMatchProjects(t)
	if _, err := db.DB.Exec(
		`INSERT INTO issues(project_id, issue_number, type, title, status, deleted_at)
		 VALUES(?, 2, 'ticket', 'deleted alpha issue', 'new', CURRENT_TIMESTAMP)`, alphaID); err != nil {
		t.Fatal(err)
	}

	got, err := resolveIntakeImpactIssues(context.Background(), alphaID, []intakeImpactCandidate{
		{IssueKey: "ALPH-1"},
		{IssueKey: "ALPH-2"},
		{IssueKey: "BETA-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("resolved issues = %+v, want only ALPH-1", got)
	}
	alpha := got["ALPH-1"]
	if alpha.IssueID == 0 || alpha.Title != "rocket telemetry ingestion pipeline for launch data" {
		t.Fatalf("ALPH-1 = %+v", alpha)
	}
	if _, ok := got["BETA-1"]; ok {
		t.Fatalf("project-scoped lookup leaked project %d into project %d", betaID, alphaID)
	}
}

func TestSortIntakeImpactsArtifact_Deterministic(t *testing.T) {
	first := intakeImpactsArtifact{
		Impacted: []intakeImpactEntry{{IssueKey: "ALPH-9"}, {IssueKey: "ALPH-2"}},
		Related:  []intakeImpactEntry{{IssueKey: "ALPH-8"}, {IssueKey: "ALPH-1"}},
		GraphHits: []intakeGraphHit{
			{EntityType: "repo", Title: "Zulu"},
			{EntityType: "anchor", Title: "Alpha"},
		},
	}
	second := intakeImpactsArtifact{
		Impacted: []intakeImpactEntry{{IssueKey: "ALPH-2"}, {IssueKey: "ALPH-9"}},
		Related:  []intakeImpactEntry{{IssueKey: "ALPH-1"}, {IssueKey: "ALPH-8"}},
		GraphHits: []intakeGraphHit{
			{EntityType: "anchor", Title: "Alpha"},
			{EntityType: "repo", Title: "Zulu"},
		},
	}

	sortIntakeImpactsArtifact(&first)
	sortIntakeImpactsArtifact(&second)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("sorted artifacts differ:\nfirst  %+v\nsecond %+v", first, second)
	}
}

// TestIntakeImpacts_HeuristicAndLLMCategories: retrieval-only hits land in
// "related" in degraded mode (no LLM categories); an LLM category promotes
// the entry into "impacted" with the correct mapped relation type. The
// contracts RelationTypes enum is never extended — only mapped onto.
func TestIntakeImpacts_HeuristicAndLLMCategories(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	alphaID, _ := seedMatchProjects(t)

	// Pin the session to alpha so the stage has a target project.
	if _, err := db.DB.Exec(
		`UPDATE intake_sessions SET pinned_project_id=? WHERE id=?`, alphaID, sessionID); err != nil {
		t.Fatal(err)
	}
	s, err := loadIntakeSession(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}

	query := "rocket telemetry ingestion pipeline"

	// Degraded (no LLM categories): the retrieval hit is context → related.
	runIntakeImpactsStage(context.Background(), s, query, nil)
	var payload string
	if err := db.DB.QueryRow(
		`SELECT payload_json FROM intake_events WHERE session_id=? AND kind='impacts' ORDER BY seq DESC LIMIT 1`,
		sessionID).Scan(&payload); err != nil {
		t.Fatalf("impacts event missing (degraded mode): %v", err)
	}
	var out intakeImpactsArtifact
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatal(err)
	}
	if out.ProjectID != alphaID {
		t.Fatalf("impacts project_id = %d, want %d", out.ProjectID, alphaID)
	}
	if len(out.Related) == 0 || out.Related[0].IssueKey != "ALPH-1" {
		t.Fatalf("degraded impacts: related = %+v", out.Related)
	}
	if out.Related[0].Category != "related" {
		t.Fatalf("degraded category = %s, want related", out.Related[0].Category)
	}

	// LLM category "conflicts" → impacted bucket, mapped to 'impacts'.
	runIntakeImpactsStage(context.Background(), s, query, map[string]string{"ALPH-1": "conflicts"})
	if err := db.DB.QueryRow(
		`SELECT payload_json FROM intake_events WHERE session_id=? AND kind='impacts' ORDER BY seq DESC LIMIT 1`,
		sessionID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Impacted) != 1 || out.Impacted[0].Category != "conflicts" ||
		out.Impacted[0].MappedRelation != "impacts" {
		t.Fatalf("LLM-categorized impacts = %+v", out.Impacted)
	}
}
