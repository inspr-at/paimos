// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// BlockingTx is a bounded, redacted eligibility probe. A relation can itself be
// hidden by project RLS, so first collect its sources in a savepoint with full
// project visibility, then judge them under the original caller visibility.
// Tenant RLS is never widened. Call under the shared tenant/tree fence; this
// returns no hidden source identities and grants no authority to mutate them.
func BlockingTx(ctx context.Context, tx pgx.Tx, nodeID string) (string, error) {
	const limit = 64
	sources := []string{}
	err := withFullOrder(ctx, tx, func(step pgx.Tx) error {
		rows, err := step.Query(ctx, `SELECT source_node_id::text FROM node_relations WHERE target_node_id=$1 AND type='blocks' ORDER BY source_node_id LIMIT 65`, nodeID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			sources = append(sources, id)
		}
		return rows.Err()
	})
	if err != nil {
		return "", err
	}
	if len(sources) > limit {
		return "blocker_scan_partial", nil
	}
	for _, id := range sources {
		var resolved bool
		err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL OR aeon_work_status_category(n.state,k.field_schema) IN ('done','delivered','accepted','cancelled','archived') FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1`, id).Scan(&resolved)
		if err == pgx.ErrNoRows {
			return "blocker_unavailable", nil
		}
		if err != nil {
			return "", err
		}
		if !resolved {
			return "dependency_wait", nil
		}
	}
	return "", nil
}

// Readiness is advice only. Add and pickup recheck the live ticket.
type Readiness struct {
	Stale                  bool     `json:"stale"`
	Queueable              bool     `json:"queueable"`
	Ready                  bool     `json:"ready"`
	Missing                []string `json:"missing"`
	SuggestedEstimateHours float64  `json:"suggested_estimate_hours"`
	SecurityReviewRequired bool     `json:"security_review_required"`
}

func State(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_"), "-", "_")
}
func Hours(fields map[string]any) float64 {
	h, ok := fields["estimate_hours"].(float64)
	if !ok || math.IsNaN(h) || math.IsInf(h, 0) || h <= 0 || h > 200 {
		return 0
	}
	return h
}
func Criteria(fields map[string]any) []string {
	out := []string{}
	switch value := fields["acceptance_criteria"].(type) {
	case string:
		for _, line := range strings.Split(value, "\n") {
			if line = strings.TrimSpace(line); strings.Trim(line, "- *#[]xX\t") != "" {
				out = append(out, line)
			}
		}
	case []any:
		for _, v := range value {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}
func Fields(raw []byte) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

var securityWords = regexp.MustCompile(`(?i)\b(security|permissions?|authorization|authentication|auth|rls|credentials?)\b`)

func Security(title, body string, fields map[string]any) bool {
	required, _ := fields["security_review_required"].(bool)
	return required || securityWords.MatchString(title+" "+body)
}

// stale is authoritative evidence from Stale under the mutation locks. Callers
// without that evidence cannot queue progress.
func Check(kind, state, title, body string, fields map[string]any, namedBlocker bool, stale ...bool) Readiness {
	r := Readiness{Missing: []string{}, SuggestedEstimateHours: 2, SecurityReviewRequired: Security(title, body, fields)}
	switch fields["complexity"] {
	case "S":
		r.SuggestedEstimateHours = 1
	case "L":
		r.SuggestedEstimateHours = 8
	case "M":
		r.SuggestedEstimateHours = 3
	}
	switch State(state) {
	case "new", "open", "backlog", "blocked":
		r.Queueable = kind == "ticket" || kind == "task" || kind == "work"
	case "in_progress", "progress", "active":
		r.Stale = (kind == "ticket" || kind == "task" || kind == "work") && len(stale) == 1 && stale[0]
		r.Queueable = r.Stale
	}
	if !r.Queueable {
		r.Missing = append(r.Missing, "status")
	}
	if Hours(fields) == 0 {
		r.Missing = append(r.Missing, "estimate")
	}
	if len(Criteria(fields)) == 0 {
		r.Missing = append(r.Missing, "criteria")
	}
	if State(state) == "blocked" && !namedBlocker {
		r.Missing = append(r.Missing, "blocker")
	}
	r.Ready = r.Queueable && len(r.Missing) == 0
	return r
}
