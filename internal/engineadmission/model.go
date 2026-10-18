// SPDX-License-Identifier: AGPL-3.0-only
// Package engineadmission records advisory decisions only. It has no launcher,
// claim, review, git write, merge, or deployment capability.
package engineadmission

import (
	"math"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
)

type Request struct {
	RequestID     string  `json:"request_id,omitempty"`
	Kind          string  `json:"kind"`
	Harness       string  `json:"harness,omitempty"`
	Project       string  `json:"project"`
	Estimate      float64 `json:"estimate"`
	ScriptAllowed *bool   `json:"script_allowed,omitempty"`
}

type Decision struct {
	RequestID    string    `json:"request_id"`
	ProjectID    string    `json:"project_id"`
	Allowed      bool      `json:"allowed"`
	Reason       string    `json:"reason"`
	RetryAfter   *int64    `json:"retry_after"`
	Mode         string    `json:"mode"`
	Enforced     bool      `json:"enforced"`
	ScriptAgrees *bool     `json:"script_agrees"`
	EvaluatedAt  time.Time `json:"evaluated_at"`
}

type Settings struct {
	ProjectID     string `json:"project_id"`
	ShadowEnabled bool   `json:"shadow_enabled"`
	Revision      int64  `json:"revision"`
}

func (r Request) valid() bool {
	switch r.Kind {
	case "first_build", "fix", "merge", "land", "review":
	default:
		return false
	}
	if r.Harness != "" && agentplan.HarnessLabel(r.Harness) == "" {
		return false
	}
	if len(r.Project) < 1 || len(r.Project) > 64 || math.IsNaN(r.Estimate) || math.IsInf(r.Estimate, 0) || r.Estimate <= 0 || r.Estimate > 168 || len(r.RequestID) > 128 {
		return false
	}
	for _, c := range r.RequestID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// planReason retains plan-gate's harness-off precedence and finishing reserve.
// Fix, merge, land and review use every dial slot; only first builds leave two.
func planReason(r Request, s agentplan.Snapshot) string {
	if r.Harness == "" {
		return "harness_required"
	}
	if s.Plan.Validate() != nil {
		return "plan_unreadable"
	}
	limit, exists := s.Limits[r.Harness]
	if exists && limit.Mode == agentplan.Off {
		return "harness_off"
	}
	total := 0
	for _, n := range s.Running {
		if n < 0 {
			return "plan_unreadable"
		}
		total = min(agentplan.MaxTotal+1, total+min(n, agentplan.MaxTotal+1))
	}
	reserve := 0
	if r.Kind == "first_build" {
		reserve = 2
	}
	if total >= s.Total {
		return "plan_total"
	}
	if total+reserve >= s.Total {
		return "finishing_reserve"
	}
	if exists && limit.Mode == agentplan.AtMost && s.Running[r.Harness] >= limit.Value {
		return "harness_limit"
	}
	return ""
}

func retry(reason string, until *time.Time, now time.Time) *int64 {
	switch reason {
	case "allowed", "shadow_disabled", "harness_required", "harness_off", "context":
		return nil
	}
	seconds := int64(30)
	if until != nil && until.After(now) {
		seconds = max(1, int64(math.Ceil(until.Sub(now).Seconds())))
	}
	return &seconds
}
