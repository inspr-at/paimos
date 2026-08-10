// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"

	_ "modernc.org/sqlite"
)

// seedMatchProjects creates two active projects with FTS-indexed issues.
func seedMatchProjects(t *testing.T) (alphaID, betaID int64) {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO projects(name, key, status) VALUES('Alpha Rocket', 'ALPH', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	alphaID, _ = res.LastInsertId()
	res, err = db.DB.Exec(`INSERT INTO projects(name, key, status) VALUES('Beta Garden', 'BETA', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	betaID, _ = res.LastInsertId()
	seedIssue := func(pid int64, num int, title string) {
		res, err := db.DB.Exec(
			`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,?,?,?,'new')`,
			pid, num, "ticket", title)
		if err != nil {
			t.Fatal(err)
		}
		iid, _ := res.LastInsertId()
		if _, err := db.DB.Exec(
			`INSERT INTO search_index(entity_type, entity_id, content) VALUES('issue', ?, ?)`,
			iid, title); err != nil {
			t.Fatal(err)
		}
	}
	seedIssue(alphaID, 1, "rocket telemetry ingestion pipeline for launch data")
	seedIssue(betaID, 1, "garden watering schedule with soil sensors")
	return alphaID, betaID
}

// TestIntakeProjectCandidates_CharterBeatsBacklogVolume reproduces PAI-756:
// a small project whose charter fits the generated specification must not be
// hidden by a large project with many vaguely matching tickets.
func TestIntakeProjectCandidates_CharterBeatsBacklogVolume(t *testing.T) {
	openIntakeTestDB(t)
	insertProject := func(name, key, description string) int64 {
		res, err := db.DB.Exec(
			`INSERT INTO projects(name, key, description, status) VALUES(?,?,?,'active')`,
			name, key, description)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	janusID := insertProject("JANUS", "JANUS", "Vaultwarden credential vault for encrypted team secrets and passwords")
	largeID := insertProject("Platform Program", "PLAT", "General product platform and delivery backlog")
	for n := 1; n <= 20; n++ {
		res, err := db.DB.Exec(
			`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,?,?,'Add secure tool integration', 'new')`,
			largeID, n, "ticket")
		if err != nil {
			t.Fatal(err)
		}
		iid, _ := res.LastInsertId()
		if _, err := db.DB.Exec(
			`INSERT INTO search_index(entity_type, entity_id, content) VALUES('issue', ?, 'secure tool integration')`, iid); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 5; n++ {
		insertProject("Filler Project", "FILL"+string(rune('A'+n)), "Unrelated planning workspace")
	}

	input := intakeMatchInput{
		SpecTitle:    "Encrypted secrets tool",
		SpecSummary:  "Add a secure tool for storing team credentials and passwords.",
		SpecMarkdown: "The tool keeps shared secrets encrypted and retrievable by the team.",
		Transcript:   "we need this tool integrated",
	}
	candidates, err := intakeProjectCandidates(context.Background(), input, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 7 {
		t.Fatalf("Stage B candidate universe has %d projects, want all 7: %+v", len(candidates), candidates)
	}
	if candidates[0].ProjectID != janusID {
		t.Fatalf("charter-fit project did not beat backlog volume: %+v", candidates)
	}

	prompt := intakeProjectMatchUserPrompt(input, candidates)
	if !strings.Contains(prompt, `name="JANUS"`) || !strings.Contains(prompt, "Vaultwarden credential vault") {
		t.Fatalf("Stage B prompt omitted JANUS charter: %s", prompt)
	}
	if !strings.Contains(prompt, "SPECIFICATION TITLE:\nEncrypted secrets tool") ||
		!strings.Contains(prompt, "SPECIFICATION SUMMARY:\nAdd a secure tool") {
		t.Fatalf("Stage B prompt omitted weighted specification fields: %s", prompt)
	}
}

// TestIntakeProjectCandidates_RestrictedUniverse enforces INV-INTAKE-03:
// projects outside the accessible set never appear as candidates, even
// when the transcript matches their content exactly.
func TestIntakeProjectCandidates_RestrictedUniverse(t *testing.T) {
	openIntakeTestDB(t)
	alphaID, betaID := seedMatchProjects(t)

	transcript := "the garden watering schedule needs soil sensors and the rocket telemetry pipeline"
	input := intakeMatchInput{Transcript: transcript}

	// Unrestricted (admin, nil): both projects surface.
	all, err := intakeProjectCandidates(context.Background(), input, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[int64]bool{}
	for _, c := range all {
		found[c.ProjectID] = true
	}
	if !found[alphaID] || !found[betaID] {
		t.Fatalf("admin candidates missing projects: %+v", all)
	}

	// Restricted to alpha only: beta must never appear.
	restricted, err := intakeProjectCandidates(context.Background(), input, []int64{alphaID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range restricted {
		if c.ProjectID == betaID {
			t.Fatalf("restricted candidates leaked inaccessible project: %+v", restricted)
		}
	}
	if len(restricted) == 0 || restricted[0].ProjectID != alphaID {
		t.Fatalf("restricted candidates should contain alpha: %+v", restricted)
	}

	// Empty accessible set: zero candidates.
	none, err := intakeProjectCandidates(context.Background(), input, []int64{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("no-access user got candidates: %+v", none)
	}
}

// TestIntakeMatch_NoEvidenceClamp: an LLM score for a candidate without
// lexical evidence is capped below the default 90 auto-switch threshold.
func TestIntakeMatch_NoEvidenceClamp(t *testing.T) {
	candidates := []intakeMatchCandidate{
		{ProjectID: 1, Key: "A", Score: 40, lexical: true},
		{ProjectID: 2, Key: "B", Score: 10, lexical: false},
	}
	llm := intakeProjectMatchBody{}
	llm.Candidates = append(llm.Candidates,
		struct {
			ProjectID int64  `json:"project_id"`
			Score     int    `json:"score"`
			Rationale string `json:"rationale"`
		}{ProjectID: 1, Score: 95, Rationale: "explicit"},
		struct {
			ProjectID int64  `json:"project_id"`
			Score     int    `json:"score"`
			Rationale string `json:"rationale"`
		}{ProjectID: 2, Score: 97, Rationale: "hallucinated"},
		struct {
			ProjectID int64  `json:"project_id"`
			Score     int    `json:"score"`
			Rationale string `json:"rationale"`
		}{ProjectID: 999, Score: 100, Rationale: "not in candidate set"},
	)
	merged := mergeIntakeMatchScores(candidates, llm)
	byID := map[int64]intakeMatchCandidate{}
	for _, c := range merged {
		byID[c.ProjectID] = c
	}
	if byID[1].Score != 95 {
		t.Errorf("lexical candidate score = %d, want 95", byID[1].Score)
	}
	if byID[2].Score != intakeLLMNoEvidenceCap {
		t.Errorf("no-evidence candidate score = %d, want clamped %d", byID[2].Score, intakeLLMNoEvidenceCap)
	}
	if byID[2].Score >= 90 {
		t.Errorf("clamped score %d could trip the default 90 threshold", byID[2].Score)
	}
	if _, ok := byID[999]; ok {
		t.Error("LLM introduced a project outside the candidate set")
	}
}

// TestIntakeMatch_Hysteresis: the incumbent survives close challengers
// and falls to clearly better ones or when itself weak.
func TestIntakeMatch_Hysteresis(t *testing.T) {
	inc := int64(1)
	// Challenger only +5 ahead, incumbent strong → incumbent stays.
	got, score := applyIntakeHysteresis(
		[]intakeMatchCandidate{{ProjectID: 2, Score: 65}}, &inc, 60)
	if got == nil || *got != 1 || score != 60 {
		t.Errorf("weak challenger displaced incumbent: got %v score %d", got, score)
	}
	// Challenger +10 ahead → displaces.
	got, score = applyIntakeHysteresis(
		[]intakeMatchCandidate{{ProjectID: 2, Score: 72}}, &inc, 60)
	if got == nil || *got != 2 || score != 72 {
		t.Errorf("clear challenger did not displace: got %v score %d", got, score)
	}
	// Incumbent weak (<50) → any top candidate takes over.
	got, _ = applyIntakeHysteresis(
		[]intakeMatchCandidate{{ProjectID: 3, Score: 45}}, &inc, 40)
	if got == nil || *got != 3 {
		t.Errorf("weak incumbent survived: got %v", got)
	}
	// No incumbent → top candidate wins outright.
	got, _ = applyIntakeHysteresis(
		[]intakeMatchCandidate{{ProjectID: 4, Score: 30}}, nil, 0)
	if got == nil || *got != 4 {
		t.Errorf("no-incumbent case: got %v", got)
	}
}

// TestIntakeConfidenceThreshold_Resolution: user override → instance
// setting → default, with bounds enforced at each level.
func TestIntakeConfidenceThreshold_Resolution(t *testing.T) {
	openIntakeTestDB(t)
	var userID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='io-member'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if got := intakeConfidenceThresholdFor(ctx, userID); got != intakeConfidenceThreshold {
		t.Errorf("default threshold = %d, want %d", got, intakeConfidenceThreshold)
	}
	if _, err := db.DB.Exec(
		`INSERT INTO app_settings(key, value) VALUES('intake_confidence_threshold','75')`); err != nil {
		t.Fatal(err)
	}
	if got := intakeConfidenceThresholdFor(ctx, userID); got != 75 {
		t.Errorf("instance threshold = %d, want 75", got)
	}
	if _, err := db.DB.Exec(
		`UPDATE users SET intake_confidence_threshold=95 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if got := intakeConfidenceThresholdFor(ctx, userID); got != 95 {
		t.Errorf("user threshold = %d, want 95", got)
	}
	// Out-of-bounds stored values fall through to the next level.
	if _, err := db.DB.Exec(
		`UPDATE users SET intake_confidence_threshold=30 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if got := intakeConfidenceThresholdFor(ctx, userID); got != 75 {
		t.Errorf("out-of-bounds user value should fall back to instance: got %d", got)
	}
}
