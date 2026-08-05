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

// PAI-708 (epic PAI-703). Impact analysis for intake sessions — no LLM of
// its own: mixed retrieval into the target project plus blast-radius over
// the top issue hits. Categories (touches / conflicts / extends) come from
// the intake_spec output when the AI loop is healthy; the degraded
// heuristic is blast-radius-reached → touches, retrieval-only → related.
// Categories are ANALYSIS LABELS ONLY — on create they map onto existing
// relation types (touches→related, extends→follows_from, conflicts→impacts);
// the RelationTypes enum is deliberately untouched (PLANNING_HIERARCHY).

package handlers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// intakeCategoryRelation maps analysis categories to filed relation types.
var intakeCategoryRelation = map[string]string{
	"touches":   "related",
	"extends":   "follows_from",
	"conflicts": "impacts",
}

type intakeImpactEntry struct {
	IssueID        int64   `json:"issue_id,omitempty"`
	IssueKey       string  `json:"issue_key"`
	Title          string  `json:"title"`
	Category       string  `json:"category"`
	MappedRelation string  `json:"mapped_relation,omitempty"`
	Score          float64 `json:"score,omitempty"`
	Via            string  `json:"via"` // "retrieval" | "graph"
}

type intakeGraphHit struct {
	EntityType string `json:"entity_type"`
	Title      string `json:"title"`
}

// intakeCandidateIssues returns the top retrieval issue hits for the
// target project — fed to intake_spec so the model can categorize
// relations against real issues, and reused as impact roots.
func intakeCandidateIssues(projectID int64, query string) ([]map[string]any, error) {
	hits, _, err := retrieveProjectContextHits(projectID, query, 12)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, h := range hits {
		if h["entity_type"] == "issue" {
			if key, _ := h["issue_key"].(string); key != "" {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

// runIntakeImpactsStage computes and stores the impacts artifact.
// specRelations carries the LLM's {issue_key → category} judgments (empty
// in degraded mode → heuristics only).
func runIntakeImpactsStage(ctx context.Context, s *intakeSession, query string, specRelations map[string]string) {
	target := s.activeProjectID()
	if target == nil || strings.TrimSpace(query) == "" {
		return
	}
	hits, _, err := retrieveProjectContextHits(*target, query, 12)
	if err != nil {
		publishIntakeStage(s.ID, "impacts", "error", "retrieve")
		return
	}

	issueHits := []map[string]any{}
	graphHits := []intakeGraphHit{}
	for _, h := range hits {
		if h["entity_type"] == "issue" {
			if key, _ := h["issue_key"].(string); key != "" {
				issueHits = append(issueHits, h)
				continue
			}
		}
		if title, _ := h["title"].(string); title != "" {
			et, _ := h["entity_type"].(string)
			if len(graphHits) < 5 {
				graphHits = append(graphHits, intakeGraphHit{EntityType: et, Title: title})
			}
		}
	}

	// Blast radius over the top ≤3 issue hits: everything reachable within
	// 2 hops is "touched" by working near those issues.
	reached := map[string]bool{}
	roots := 0
	for _, h := range issueHits {
		if roots >= 3 {
			break
		}
		id := entityIDAsInt64(h["entity_id"])
		if id == 0 {
			continue
		}
		roots++
		blast, err := computeBlastRadius(*target, "issue", id, 2)
		if err != nil {
			continue
		}
		collectBlastIssueKeys(blast, reached)
	}

	impacted := []intakeImpactEntry{}
	related := []intakeImpactEntry{}
	seen := map[string]bool{}
	addEntry := func(key, title string, score float64, via string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		issueID, _ := auth.ResolveIssueRef(key)
		category := specRelations[key]
		if category == "" {
			if reached[key] {
				category = "touches"
			} else {
				category = "related"
			}
		}
		e := intakeImpactEntry{
			IssueID: issueID, IssueKey: key, Title: title, Category: category,
			MappedRelation: intakeCategoryRelation[category], Score: score, Via: via,
		}
		if category == "related" {
			related = append(related, e)
		} else {
			impacted = append(impacted, e)
		}
	}
	for _, h := range issueHits {
		key, _ := h["issue_key"].(string)
		title, _ := h["title"].(string)
		score, _ := h["score"].(float64)
		addEntry(key, title, score, "retrieval")
	}
	for key := range reached {
		addEntry(key, intakeIssueTitleByKey(ctx, key), 0, "graph")
	}

	payload, _ := json.Marshal(map[string]any{
		"impacted":   impacted,
		"related":    related,
		"graph_hits": graphHits,
	})
	count, err := intakeEventCount(ctx, db.DB, s.ID)
	if err != nil || count >= intakeMaxEventsPerSess {
		return
	}
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(ctx, tx, s.ID, "impacts", "ai", "", string(payload))
	if err == nil && tx.Commit() == nil {
		publishIntakeEvent(ctx, s.ID, seq)
		publishIntakeStage(s.ID, "impacts", "ok", "")
	}
}

func entityIDAsInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// collectBlastIssueKeys pulls issue keys out of the blast-radius response
// shape {reached: {issue: [{issue_key,...}], ...}} defensively.
func collectBlastIssueKeys(blast map[string]any, into map[string]bool) {
	reached, _ := blast["reached"].(map[string]any)
	if reached == nil {
		return
	}
	entries, _ := reached["issue"].([]map[string]any)
	if entries == nil {
		if raw, ok := reached["issue"].([]any); ok {
			for _, e := range raw {
				if m, ok := e.(map[string]any); ok {
					if key, _ := m["issue_key"].(string); key != "" {
						into[key] = true
					}
				}
			}
		}
		return
	}
	for _, m := range entries {
		if key, _ := m["issue_key"].(string); key != "" {
			into[key] = true
		}
	}
}

func intakeIssueTitleByKey(ctx context.Context, key string) string {
	parts := strings.SplitN(key, "-", 2)
	if len(parts) != 2 {
		return ""
	}
	var title string
	_ = db.DB.QueryRowContext(ctx, `
		SELECT i.title FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE p.key = ? AND i.issue_number = ? AND i.deleted_at IS NULL`,
		parts[0], parts[1]).Scan(&title)
	return title
}
