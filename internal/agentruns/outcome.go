// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	commitSHARe  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	commitPathRe = regexp.MustCompile(`/commit/([0-9a-f]{7,40})(?:[^0-9a-f]|$)`)
	mergedWordRe = regexp.MustCompile(`(?:^|[^a-z])merged(?:[^a-z]|$)`)
)

func commitSHA(s string) bool {
	return commitSHARe.MatchString(s)
}

func commitSubject(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 200 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func deriveOutcomeDetail(status string, shas, refs []string) string {
	merged, pr, committed := false, false, false
	for _, sha := range shas {
		if commitSHA(strings.ToLower(strings.TrimSpace(sha))) {
			committed = true
		}
	}
	for _, ref := range refs {
		m, p, c := classifyEvidenceRef(ref)
		merged = merged || m
		pr = pr || p
		committed = committed || c
	}
	switch {
	case merged:
		return "merged"
	case pr:
		return "pr_opened"
	case committed:
		return "committed"
	case status == "cancelled" || status == "ownership_lost":
		return "abandoned"
	default:
		return "no_commit"
	}
}

func classifyEvidenceRef(ref string) (merged, pr, committed bool) {
	s := strings.ToLower(strings.TrimSpace(ref))
	if commitSHA(s) || commitPathRe.MatchString(s) {
		committed = true
	}
	if strings.Contains(s, "/pull/") || strings.Contains(s, "/pulls/") || strings.Contains(s, "/merge_requests/") {
		pr = true
		cleaned := strings.ReplaceAll(s, "merge_requests", "")
		if strings.Contains(cleaned, "/merge") || strings.Contains(s, "merged=true") || mergedWordRe.MatchString(s) {
			merged = true
		}
	}
	return merged, pr, committed
}

func waitingMillis(ctx context.Context, tx pgx.Tx, runID string) (int64, error) {
	rows, err := tx.Query(ctx, `SELECT status, at FROM run_telemetry WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var waiting int64
	var prevStatus string
	var prevAt time.Time
	var have bool
	for rows.Next() {
		var status *string
		var at time.Time
		if err := rows.Scan(&status, &at); err != nil {
			return 0, err
		}
		if have && prevStatus == "waiting" {
			if gap := at.Sub(prevAt).Milliseconds(); gap > 0 {
				waiting += gap
			}
		}
		prevStatus = ""
		if status != nil {
			prevStatus = *status
		}
		prevAt = at
		have = true
	}
	return waiting, rows.Err()
}

func runEvidence(ctx context.Context, tx pgx.Tx, runID, orderID string, commits []GitCommit) ([]string, []string, error) {
	shas := make([]string, 0, len(commits))
	for _, c := range commits {
		shas = append(shas, strings.ToLower(c.SHA))
	}
	rows, err := tx.Query(ctx, `SELECT c->>'sha' FROM harness_sessions s
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(s.commits)='array' THEN s.commits ELSE '[]'::jsonb END) c
		WHERE s.run_id=$1`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, nil, err
		}
		shas = append(shas, sha)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	refRows, err := tx.Query(ctx, `SELECT reference FROM work_evidence WHERE run_id=$1 AND work_order_id=$2`, runID, orderID)
	if err != nil {
		return nil, nil, err
	}
	defer refRows.Close()
	var refs []string
	for refRows.Next() {
		var ref string
		if err := refRows.Scan(&ref); err != nil {
			return nil, nil, err
		}
		refs = append(refs, ref)
	}
	return shas, refs, refRows.Err()
}

func applyRunUsage(ctx context.Context, tx pgx.Tx, v *Run, t Telemetry) error {
	if !terminal(v.Status) || v.StartedAt == nil || v.EndedAt == nil || v.DurationMS == nil {
		return nil
	}
	waiting, err := waitingMillis(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	active := *v.DurationMS - waiting
	if active < 0 {
		active = 0
	}
	shas, refs, err := runEvidence(ctx, tx, v.ID, v.OrderID, t.GitCommits)
	if err != nil {
		return err
	}
	detail := deriveOutcomeDetail(v.Status, shas, refs)
	next, err := scan(tx.QueryRow(ctx, `UPDATE agent_runs SET active_ms=$2, outcome_detail=$3 WHERE id=$1 RETURNING `+columns, v.ID, active, detail))
	if err != nil {
		return err
	}
	*v = next
	return nil
}
