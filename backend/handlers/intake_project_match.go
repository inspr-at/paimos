// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-706 (epic PAI-703). Project detection for voice-intake sessions.
//
// Two stages:
//   Stage A (deterministic, every run): FTS bm25 aggregate over the
//     session owner's accessible projects + literal name/key mention
//     boost + sticky-incumbent bonus. Cheap; runs even in degraded mode.
//   Stage B (LLM, only while unresolved): intake_project_match ranks the
//     top Stage-A candidates. Clamp rule: a project with ZERO lexical
//     evidence can never score ≥ intakeLLMNoEvidenceCap, so a
//     hallucinated match can't trip the default 90% auto-switch.
//
// INV-INTAKE-03: the candidate universe is strictly
// auth.AccessibleProjectIDsForUser(owner) — non-accessible project names
// or ids never enter prompts, events, or responses.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

func authAccessibleForUserImpl(userID int64) []int64 {
	return auth.AccessibleProjectIDsForUser(userID)
}

const (
	intakeMatchMaxCandidates  = 5
	intakeLLMNoEvidenceCap    = 85
	intakeDeterministicCap    = 60
	intakeMentionBoost        = 30
	intakeStickyBoost         = 10
	intakeHysteresisGap       = 10
	intakeIncumbentWeakBelow  = 50
	intakeMatchResolvedScore  = 90 // stop LLM ranking once top ≥ this and gap is clear
	intakeMatchClearGap       = 15
	intakeConfidenceThreshold = 90 // instance default; app_settings overrides
)

func init() {
	replaceAction(actionDescriptor{
		Key:         "intake_project_match",
		Label:       "Intake: project match",
		Surface:     "intake",
		Placement:   "text",
		Handler:     intakeProjectMatchHandler,
		Implemented: true,
	})
}

type intakeMatchCandidate struct {
	ProjectID  int64  `json:"project_id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Score      int    `json:"score"`
	Confidence string `json:"confidence"`
	Rationale  string `json:"rationale,omitempty"`
	lexical    bool
}

// intakeProjectCandidates is Stage A. Returns candidates sorted by score
// desc, all drawn from accessibleIDs (nil = admin → all projects).
func intakeProjectCandidates(ctx context.Context, transcript string, accessibleIDs []int64, incumbent *int64) ([]intakeMatchCandidate, error) {
	type projRow struct {
		id        int64
		key, name string
	}
	// Load the accessible project universe (id, key, name).
	q := `SELECT id, key, name FROM projects WHERE status = 'active'`
	args := []any{}
	if accessibleIDs != nil {
		if len(accessibleIDs) == 0 {
			return []intakeMatchCandidate{}, nil
		}
		q += ` AND id IN (` + strings.Repeat("?,", len(accessibleIDs)-1) + `?)` // #nosec G202 -- placeholders only
		for _, id := range accessibleIDs {
			args = append(args, id)
		}
	}
	rows, err := db.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := map[int64]projRow{}
	for rows.Next() {
		var p projRow
		if err := rows.Scan(&p.id, &p.key, &p.name); err != nil {
			return nil, err
		}
		projects[p.id] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return []intakeMatchCandidate{}, nil
	}

	raw := map[int64]float64{}
	ftsQuery := intakeFTSQuery(transcript)
	if ftsQuery != "" {
		// bm25 is a rank where smaller is better; negate to accumulate.
		// Relevance proxy: matching-issue count per project. bm25() is not
		// usable in joined contexts under modernc's FTS5 build, and a count
		// is plenty for a pre-filter the LLM re-ranks anyway.
		frows, err := db.DB.QueryContext(ctx, `
			SELECT i.project_id, COUNT(*)
			FROM search_index
			JOIN issues i ON i.id = search_index.entity_id
			WHERE search_index.entity_type = 'issue'
			  AND search_index MATCH ?
			  AND i.deleted_at IS NULL
			  AND i.project_id IS NOT NULL
			GROUP BY i.project_id`, ftsQuery)
		if err != nil {
			log.Printf("intake match: fts query: %v", err)
		}
		if err == nil {
			defer frows.Close()
			for frows.Next() {
				var pid int64
				var score float64
				if err := frows.Scan(&pid, &score); err == nil {
					if _, ok := projects[pid]; ok && score > 0 {
						raw[pid] += score
					}
				}
			}
		}
	}

	// Literal project name/key mentions in the transcript are the
	// strongest lexical evidence.
	lower := strings.ToLower(transcript)
	mention := map[int64]bool{}
	for id, p := range projects {
		if p.key != "" && strings.Contains(lower, strings.ToLower(p.key)) {
			mention[id] = true
		}
		if len(p.name) >= 4 && strings.Contains(lower, strings.ToLower(p.name)) {
			mention[id] = true
		}
	}

	var maxRaw float64
	for _, v := range raw {
		if v > maxRaw {
			maxRaw = v
		}
	}
	out := []intakeMatchCandidate{}
	for id, p := range projects {
		score := 0
		if maxRaw > 0 && raw[id] > 0 {
			score = int((raw[id] / maxRaw) * float64(intakeDeterministicCap-20))
			score += 20 // any bm25 evidence is worth a floor
		}
		if mention[id] {
			score += intakeMentionBoost
		}
		if incumbent != nil && *incumbent == id {
			score += intakeStickyBoost
		}
		if score == 0 {
			continue
		}
		if score > intakeDeterministicCap {
			score = intakeDeterministicCap
		}
		out = append(out, intakeMatchCandidate{
			ProjectID: id, Key: p.key, Name: p.name,
			Score: score, Confidence: intakeConfidenceForScore(score),
			lexical: raw[id] > 0 || mention[id],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > intakeMatchMaxCandidates {
		out = out[:intakeMatchMaxCandidates]
	}
	return out, nil
}

var intakeWordRe = regexp.MustCompile(`[a-zA-Z0-9]{4,}`)

// intakeFTSQuery builds a safe OR-query from the transcript's distinctive
// words. Values never reach SQL unescaped — each token is quoted for FTS5.
func intakeFTSQuery(transcript string) string {
	tail := transcript
	if len(tail) > 1500 {
		tail = tail[len(tail)-1500:]
	}
	words := intakeWordRe.FindAllString(tail, -1)
	seen := map[string]bool{}
	tokens := []string{}
	for _, w := range words {
		lw := strings.ToLower(w)
		if seen[lw] || intakeStopWords[lw] {
			continue
		}
		seen[lw] = true
		tokens = append(tokens, `"`+lw+`"`)
		if len(tokens) >= 24 {
			break
		}
	}
	return strings.Join(tokens, " OR ")
}

var intakeStopWords = map[string]bool{
	"that": true, "this": true, "with": true, "have": true, "should": true,
	"would": true, "could": true, "there": true, "their": true, "about": true,
	"which": true, "when": true, "then": true, "than": true, "from": true,
	"also": true, "because": true, "really": true, "need": true, "want": true,
	"like": true, "just": true, "some": true, "more": true, "will": true,
}

func intakeConfidenceForScore(score int) string {
	switch {
	case score >= 80:
		return "high"
	case score >= 50:
		return "med"
	default:
		return "low"
	}
}

// ── Stage B: LLM rank ────────────────────────────────────────────────

type intakeProjectMatchParams struct {
	Candidates []intakeMatchCandidate `json:"candidates"`
}

type intakeProjectMatchBody struct {
	Candidates []struct {
		ProjectID int64  `json:"project_id"`
		Score     int    `json:"score"`
		Rationale string `json:"rationale"`
	} `json:"candidates"`
}

func intakeProjectMatchHandler(ax *aiActionContext) (any, string, int, int, string, error) {
	if strings.TrimSpace(ax.Text) == "" {
		return nil, "", 0, 0, "", &userError{status: 400, msg: "intake_project_match requires transcript text"}
	}
	var params intakeProjectMatchParams
	if len(ax.Params) > 0 {
		_ = json.Unmarshal(ax.Params, &params)
	}
	if len(params.Candidates) == 0 {
		return newNoOpResult("no candidate projects"), "", 0, 0, "", nil
	}

	systemPrompt := resolveActionPromptWithPreset(ax, "intake_project_match")
	var u strings.Builder
	u.WriteString("Candidate projects:\n")
	for _, c := range params.Candidates {
		fmt.Fprintf(&u, "- id=%d key=%s name=%q (lexical score %d)\n", c.ProjectID, c.Key, c.Name, c.Score)
	}
	u.WriteString("\nTranscript:\n")
	u.WriteString(ax.Text)
	u.WriteString("\n\nScore how well the SPOKEN IDEA belongs to each candidate project (0-100). Return the JSON object.")

	ctx, cancel := context.WithTimeout(ax.Ctx, 45*time.Second)
	defer cancel()
	var body intakeProjectMatchBody
	model, ptok, ctok, finish, err := callJSONAction(ctx, ax, systemPrompt, u.String(), 800, &body)
	if err != nil {
		return nil, model, ptok, ctok, finish, err
	}
	return body, model, ptok, ctok, finish, nil
}

// mergeIntakeMatchScores applies the LLM scores onto the Stage-A
// candidates with the no-evidence clamp.
func mergeIntakeMatchScores(candidates []intakeMatchCandidate, llm intakeProjectMatchBody) []intakeMatchCandidate {
	byID := map[int64]int{}
	for i := range candidates {
		byID[candidates[i].ProjectID] = i
	}
	for _, l := range llm.Candidates {
		i, ok := byID[l.ProjectID] // LLM cannot introduce projects outside the candidate set
		if !ok {
			continue
		}
		score := min(max(l.Score, 0), 100)
		if !candidates[i].lexical && score > intakeLLMNoEvidenceCap {
			score = intakeLLMNoEvidenceCap
		}
		if score > candidates[i].Score {
			candidates[i].Score = score
		}
		candidates[i].Confidence = intakeConfidenceForScore(candidates[i].Score)
		if l.Rationale != "" {
			candidates[i].Rationale = l.Rationale
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	return candidates
}

// applyIntakeHysteresis decides the detected project: the incumbent is
// displaced only by a clearly better challenger or when itself weak.
func applyIntakeHysteresis(candidates []intakeMatchCandidate, incumbent *int64, incumbentScore int) (*int64, int) {
	if len(candidates) == 0 {
		return incumbent, incumbentScore
	}
	top := candidates[0]
	if incumbent == nil || *incumbent == top.ProjectID {
		return &top.ProjectID, top.Score
	}
	if top.Score >= incumbentScore+intakeHysteresisGap || incumbentScore < intakeIncumbentWeakBelow {
		return &top.ProjectID, top.Score
	}
	return incumbent, incumbentScore
}

// runIntakeMatchStage executes project detection for one pipeline cycle.
// Stage A always runs (deterministic, works in degraded mode, capped so it
// can never auto-switch); Stage B (LLM) runs only when a provider is
// available, budgets allow, and detection is still unresolved. Returns the
// prompt/completion tokens spent (for the session meter).
func runIntakeMatchStage(ctx context.Context, s *intakeSession, transcript string, ax *aiActionContext, allowLLM bool) (ptok, ctok int) {
	accessible := authAccessibleProjectIDsForUser(s.UserID)
	incumbentScore := s.DetectedScore
	firstDetection := s.DetectedProjectID == nil && s.PinnedProjectID == nil
	candidates, err := intakeProjectCandidates(ctx, transcript, accessible, s.DetectedProjectID)
	if err != nil {
		publishIntakeStage(s.ID, "project_match", "error", "store")
		return 0, 0
	}
	threshold := intakeConfidenceThresholdFor(ctx, s.UserID)

	// PAI-715: while nothing is selected yet, the user's most recently
	// visited project gets a head start (+10) for the first two detection
	// rounds — the very likely target wins the opening exchange faster.
	// Once anything is detected or pinned, the normal hysteresis rules
	// own every switch.
	if firstDetection {
		var rounds int
		_ = db.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM intake_events WHERE session_id=? AND kind='project_match'`,
			s.ID).Scan(&rounds)
		if rounds < 2 {
			if recent := intakeMostRecentProject(ctx, s.UserID, accessible); recent != 0 {
				boosted := false
				for i := range candidates {
					if candidates[i].ProjectID == recent {
						candidates[i].Score = min(candidates[i].Score+intakeStickyBoost, 100)
						candidates[i].Confidence = intakeConfidenceForScore(candidates[i].Score)
						boosted = true
						break
					}
				}
				if !boosted {
					if key, name, ok := intakeProjectLabel(ctx, recent); ok {
						candidates = append(candidates, intakeMatchCandidate{
							ProjectID: recent, Key: key, Name: name,
							Score: intakeStickyBoost, Confidence: "low",
							Rationale: "recently worked on",
						})
					}
				}
				sort.Slice(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
			}
		}
	}

	unresolved := len(candidates) > 0 &&
		(candidates[0].Score < intakeMatchResolvedScore ||
			(len(candidates) > 1 && candidates[0].Score-candidates[1].Score < intakeMatchClearGap))
	if allowLLM && ax != nil && unresolved {
		params, _ := json.Marshal(intakeProjectMatchParams{Candidates: candidates})
		requestID := newAIRequestID()
		mx := *ax
		mx.Params = params
		mx.Text = intakeMatchTranscriptTail(transcript)
		started := time.Now()
		body, model, p, c, _, err := intakeProjectMatchHandler(&mx)
		latency := time.Since(started)
		outcome := "ok"
		errorClass := ""
		if err != nil {
			outcome, errorClass = "fail_upstream", "upstream"
		}
		auditAction(requestID, mx.UserID, "intake_project_match", "", "", 0, model, outcome, latency, p, c, mx.Options)
		recordAICall(ctx, aiCallArgs{
			RequestID: requestID, UserID: &mx.UserID, ActionKey: "intake_project_match",
			Surface: "intake", Provider: mx.Settings.Provider, Model: model,
			ProfileID: mx.Options.ProfileID, Effort: mx.Options.Effort,
			PromptPresetRef: mx.Options.PromptPresetRef, ContextPack: mx.Options.ContextPack,
			PromptTokens: p, CompletionTokens: c, Outcome: outcome, ErrorClass: errorClass,
			LatencyMs: latency.Milliseconds(),
		})
		if p > 0 || c > 0 {
			RecordUsage(mx.UserID, p, c)
		}
		ptok, ctok = p, c
		if err == nil {
			if llm, ok := body.(intakeProjectMatchBody); ok {
				candidates = mergeIntakeMatchScores(candidates, llm)
			}
		}
	}

	detected, score := applyIntakeHysteresis(candidates, s.DetectedProjectID, incumbentScore)
	changed := (detected == nil) != (s.DetectedProjectID == nil) ||
		(detected != nil && s.DetectedProjectID != nil && *detected != *s.DetectedProjectID) ||
		score != incumbentScore
	if !changed && len(candidates) == 0 {
		return ptok, ctok
	}

	if changed {
		if _, err := db.DB.ExecContext(ctx,
			`UPDATE intake_sessions SET detected_project_id=?, detected_score=? WHERE id=?`,
			detected, score, s.ID); err != nil {
			publishIntakeStage(s.ID, "project_match", "error", "store")
			return ptok, ctok
		}
		s.DetectedProjectID, s.DetectedScore = detected, score
	}

	payload, _ := json.Marshal(map[string]any{
		"matches":             candidates,
		"threshold":           threshold,
		"detected_project_id": detected,
		"detected_score":      score,
		// PAI-715: the first selection is threshold-free client-side —
		// there is nothing to displace, so any signal beats no project.
		"first_detection": firstDetection,
	})
	count, err := intakeEventCount(ctx, db.DB, s.ID)
	if err != nil || count >= intakeMaxEventsPerSess {
		return ptok, ctok
	}
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return ptok, ctok
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(ctx, tx, s.ID, "project_match", "ai", "", string(payload))
	if err == nil && tx.Commit() == nil {
		publishIntakeEvent(ctx, s.ID, seq)
	}
	return ptok, ctok
}

func intakeMatchTranscriptTail(transcript string) string {
	if len(transcript) > 4096 {
		return transcript[len(transcript)-4096:]
	}
	return transcript
}

// intakeMostRecentProject returns the user's most recently visited
// accessible project (0 = none).
func intakeMostRecentProject(ctx context.Context, userID int64, accessible []int64) int64 {
	var pid int64
	if err := db.DB.QueryRowContext(ctx,
		`SELECT project_id FROM user_recent_projects WHERE user_id=? ORDER BY visited_at DESC LIMIT 1`,
		userID).Scan(&pid); err != nil {
		return 0
	}
	if accessible == nil || slices.Contains(accessible, pid) {
		return pid
	}
	return 0
}

func intakeProjectLabel(ctx context.Context, projectID int64) (key, name string, ok bool) {
	err := db.DB.QueryRowContext(ctx,
		`SELECT key, name FROM projects WHERE id=? AND status='active'`, projectID).Scan(&key, &name)
	return key, name, err == nil
}

// authAccessibleProjectIDsForUser is swappable for tests.
var authAccessibleProjectIDsForUser = func(userID int64) []int64 {
	return authAccessibleForUserImpl(userID)
}

// intakeConfidenceThresholdFor resolves the effective auto-switch
// threshold: per-user override → instance app_settings → default 90.
func intakeConfidenceThresholdFor(ctx context.Context, userID int64) int {
	var userVal *int
	_ = db.DB.QueryRowContext(ctx,
		`SELECT intake_confidence_threshold FROM users WHERE id=?`, userID).Scan(&userVal)
	if userVal != nil && *userVal >= 50 && *userVal <= 100 {
		return *userVal
	}
	return intakeConfidenceThresholdInstance(ctx)
}

func intakeConfidenceThresholdInstance(ctx context.Context) int {
	var raw string
	if err := db.DB.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key='intake_confidence_threshold'`).Scan(&raw); err == nil {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err == nil && n >= 50 && n <= 100 {
			return n
		}
	}
	return intakeConfidenceThreshold
}
