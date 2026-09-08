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
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
)

var (
	ErrNotEnabled = errors.New("flow_not_enabled")
	ErrStale      = errors.New("flow_stale_context")
	ErrForbidden  = errors.New("flow_forbidden")
)

const (
	IntentStart        = "flow:start-intent"
	IntentReview       = "flow:review-batch"
	IntentSaveProposal = "flow:save-proposal"
	IntentHealth       = "flow:health"
	IntentNavigate     = "flow:navigate-stage"
	IntentToggleMap    = "flow:toggle-map"
	IntentHeaderID     = "flow:header-identity"
	IntentHeaderAcct   = "flow:header-account"
	IntentHeaderProj   = "flow:header-project"
	IntentViewDrafts   = "flow:view-drafts"
)

type IntentRequest struct {
	Type     string          `json:"type"`
	Identity json.RawMessage `json:"identity"`
	Detail   json.RawMessage `json:"detail"`
}

type IntentResult struct {
	Executed    bool     `json:"executed"`
	Unsupported bool     `json:"unsupported,omitempty"`
	Routed      *string  `json:"routed"`
	Location    string   `json:"location,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Notice      string   `json:"notice,omitempty"`
	Error       string   `json:"error,omitempty"`
	Issues      []string `json:"issues,omitempty"`
}

type submittedBinding struct {
	Status          string
	PrincipalRef    string
	ProjectRef      string
	ActorKind       string
	BindingRef      string
	ContextRevision string
	IssuedAt        string
	ExpiresAt       string
	FreshUntil      string
}

func Handle(state *ShellState, principal auth.Principal, projectID int64, intent IntentRequest, now time.Time) (IntentResult, int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	typ := intent.Type
	if typ == "" {
		typ = nestedType(intent.Detail)
	}
	overview := fmt.Sprintf(overviewBaselinePath, projectID)
	if isConsequential(typ) {
		if state == nil || state.IdentityContext == nil {
			return fail("A current project Flow context is required.", []string{"Host identity context is required before a consequential start."}), 403, ErrForbidden
		}
		issues := bindingIssues(state.IdentityContext, parseBinding(intent.Identity), now)
		if typ == IntentStart && !canStartAsHuman(principal) {
			issues = append(issues, "A human host principal is required for this start.")
		}
		if len(issues) > 0 {
			status := 409
			code := ErrStale
			if hasHumanRequired(issues) {
				status = 403
				code = ErrForbidden
			}
			result := fail(issues[0], issues)
			return result, status, code
		}
	}

	switch typ {
	case IntentStart:
		routed := "project-overview-baseline"
		return IntentResult{
			Executed: false,
			Routed:   &routed,
			Location: overview,
			Notice:   "Start stays on the existing project overview baseline controls. Confirm the exact baseline, scope, mode, and readiness there. This is not a delivery start.",
		}, 200, nil
	case IntentReview, IntentViewDrafts:
		routed := "project-overview-baseline"
		return IntentResult{
			Executed: false,
			Routed:   &routed,
			Location: overview,
			Notice:   "Review stays on the existing project overview baseline controls. This is not a delivery start.",
		}, 200, nil
	case IntentSaveProposal:
		routed := "project-overview-baseline"
		return IntentResult{
			Executed: false,
			Routed:   &routed,
			Location: overview,
			Notice:   "Draft ideas stay on the existing baseline import and review controls. A Flow idea is not a delivery start.",
		}, 200, nil
	case IntentHealth:
		routed := "health"
		return IntentResult{
			Executed: false,
			Routed:   &routed,
			Location: "/api/health",
			Notice:   "Paimos health is the existing /api/health probe. It is not delivery evidence.",
		}, 200, nil
	case IntentHeaderID, IntentHeaderAcct:
		routed := "account"
		return IntentResult{
			Executed: false,
			Routed:   &routed,
			Location: "/settings?tab=account",
			Notice:   "Account and security controls stay in existing Paimos settings.",
		}, 200, nil
	case IntentHeaderProj:
		routed := "project-overview"
		return IntentResult{
			Executed: false,
			Routed:   &routed,
			Location: fmt.Sprintf("/projects/%d?tab=overview", projectID),
			Notice:   "Project navigation stays in the current Paimos project.",
		}, 200, nil
	default:
		return IntentResult{
			Executed: false,
			Routed:   nil,
			Notice:   "Stage navigation and header actions do not start delivery.",
		}, 200, nil
	}
}

func isConsequential(typ string) bool {
	switch typ {
	case IntentStart, IntentReview, IntentSaveProposal:
		return true
	default:
		return false
	}
}

func nestedType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var detail map[string]any
	if json.Unmarshal(raw, &detail) != nil {
		return ""
	}
	if v, ok := detail["type"].(string); ok {
		return v
	}
	return ""
}

func parseBinding(raw json.RawMessage) submittedBinding {
	out := submittedBinding{}
	if len(raw) == 0 {
		return out
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		return out
	}
	str := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := fields[key].(string); ok {
				return v
			}
		}
		return ""
	}
	out.Status = str("status")
	out.PrincipalRef = str("principalRef", "principal_ref")
	out.ProjectRef = str("projectRef", "project_ref")
	out.ActorKind = str("actorKind", "actor_kind")
	out.BindingRef = str("bindingRef", "binding_ref")
	out.ContextRevision = str("contextRevision", "context_revision")
	out.IssuedAt = str("issuedAt", "issued_at")
	out.ExpiresAt = str("expiresAt", "expires_at")
	out.FreshUntil = str("freshUntil", "fresh_until")
	return out
}

func bindingIssues(current *IdentityContext, submitted submittedBinding, now time.Time) []string {
	if current == nil {
		return []string{"Host identity context is required before a consequential start."}
	}
	if submitted.PrincipalRef == "" && submitted.BindingRef == "" && submitted.ContextRevision == "" {
		return []string{"Host identity context is required before a consequential start."}
	}
	if submitted.Status != "" && submitted.Status != "present" {
		return []string{"Host identity context was rejected."}
	}
	var reasons []string
	if submitted.PrincipalRef != current.PrincipalRef {
		reasons = append(reasons, "Host principal binding does not match the current verified actor.")
	}
	if submitted.ProjectRef != current.ProjectRef {
		reasons = append(reasons, "Flow context is bound to a different project.")
	}
	if submitted.ActorKind != "" && submitted.ActorKind != current.ActorKind {
		reasons = append(reasons, "Actor kind does not match the current verified actor.")
	}
	if submitted.BindingRef != current.BindingRef {
		reasons = append(reasons, "Host identity binding does not match the current context.")
	}
	if submitted.ContextRevision != current.ContextRevision {
		reasons = append(reasons, "Host identity context has changed. Refresh and retry.")
	}
	reasons = append(reasons, freshnessIssues(submitted, now)...)
	return unique(reasons)
}

func freshnessIssues(submitted submittedBinding, now time.Time) []string {
	var reasons []string
	issued := parseTime(submitted.IssuedAt)
	expires := parseTime(submitted.ExpiresAt)
	fresh := parseTime(submitted.FreshUntil)
	if issued.IsZero() || issued.After(now.Add(time.Minute)) {
		reasons = append(reasons, "Host identity context is not yet valid.")
	}
	if expires.IsZero() || !now.Before(expires) {
		reasons = append(reasons, "Host identity context has expired.")
	}
	if fresh.IsZero() || !now.Before(fresh) {
		reasons = append(reasons, "Host identity context is stale.")
	}
	return reasons
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts.UTC()
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.UTC()
	}
	return time.Time{}
}

func hasHumanRequired(issues []string) bool {
	for _, issue := range issues {
		if strings.Contains(strings.ToLower(issue), "human host principal") {
			return true
		}
	}
	return false
}

func fail(message string, issues []string) IntentResult {
	return IntentResult{
		Executed: false,
		Routed:   nil,
		Error:    message,
		Issues:   issues,
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
