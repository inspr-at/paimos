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
	"testing"

	"github.com/inspr-at/paimos/backend/db"

	_ "modernc.org/sqlite"
)

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
