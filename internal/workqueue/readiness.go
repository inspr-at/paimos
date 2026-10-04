// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// Readiness is advice only. Add and pickup recheck the live ticket.
type Readiness struct {
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
func Check(kind, state, title, body string, fields map[string]any, namedBlocker bool) Readiness {
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
