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

// PAI-809 — the one path classifier for supervisory-control traffic.
//
// A control request carries the most sensitive material this product
// moves: which delivery a human is watching, which run a supervisor is
// steering, the idempotency key that identifies a decision. Ambient
// request plumbing — the access log, the session-activity table — was
// written for ordinary CRUD and records the full path, the query, and
// the caller's headers. On this surface that is a disclosure, not a
// diagnostic.
//
// So the classifier lives below both auth and handlers (no import
// cycle) and answers exactly one question: does this request belong to
// a frozen control route family, and if so, under which closed label
// may it be named in a log line. Everything else — path parameters,
// query values, bodies, keys, headers — never leaves the request.
//
// The mandatory `control_events` record remains the authoritative
// account of what happened. This file exists so the *incidental*
// records stop competing with it.

package httpcontract

import (
	"net/http"
	"strings"
)

// ControlRouteClass is the closed vocabulary of safe labels. Every value
// is a compile-time constant with no request-derived content, so a log
// line carrying one cannot leak a delivery key, run id, or command id.
type ControlRouteClass string

const (
	ControlRouteDeliveryCapabilityGrants ControlRouteClass = "agent_mode.delivery.control_capability_grants"
	ControlRouteDeliveryCommands         ControlRouteClass = "agent_mode.delivery.control_commands"
	ControlRouteCapabilityGrantDetail    ControlRouteClass = "agent_mode.control_capability_grant"
	ControlRouteCommandDetail            ControlRouteClass = "agent_mode.control_command"
	ControlRouteRunCapabilityLeases      ControlRouteClass = "run.control_capability_leases"
	ControlRouteRunInputRequests         ControlRouteClass = "run.input_requests"
	ControlRouteRunCommands              ControlRouteClass = "run.control_commands"
	ControlRouteCapabilityLeaseDetail    ControlRouteClass = "control_capability_lease"
	ControlRouteCommandRootDetail        ControlRouteClass = "control_command"
	ControlRouteExternalHandoffCreate    ControlRouteClass = "external_stage.handoff_create"
	ControlRouteExternalHandoffMint      ControlRouteClass = "external_stage.handoff_mint"
	ControlRouteExternalHandoffRotate    ControlRouteClass = "external_stage.handoff_rotate"
	ControlRouteExternalHandoffRevoke    ControlRouteClass = "external_stage.handoff_revoke"
	ControlRouteExternalHandoffPull      ControlRouteClass = "external_stage.handoff_pull"
	ControlRouteExternalHandoffAccept    ControlRouteClass = "external_stage.handoff_accept"
	ControlRouteExternalHandoffReport    ControlRouteClass = "external_stage.handoff_report"
)

// controlRouteParam marks a segment the caller supplies. It matches any
// single non-empty segment and contributes nothing to the label.
const controlRouteParam = ""

// controlRoutes is the frozen family list. Matching is by exact
// structural segment: same segment count, same literal spelling, one
// opaque segment wherever a parameter sits. A prefix, a suffix, a
// substring, or an extra path element is a different route and keeps
// ordinary behavior — including ordinary logging.
var controlRoutes = []struct {
	segments []string
	class    ControlRouteClass
}{
	{[]string{"api", "agent-mode", "deliveries", controlRouteParam, "control-capability-grants"}, ControlRouteDeliveryCapabilityGrants},
	{[]string{"api", "agent-mode", "deliveries", controlRouteParam, "control-commands"}, ControlRouteDeliveryCommands},
	{[]string{"api", "agent-mode", "control-capability-grants", controlRouteParam}, ControlRouteCapabilityGrantDetail},
	{[]string{"api", "agent-mode", "control-commands", controlRouteParam}, ControlRouteCommandDetail},
	{[]string{"api", "runs", controlRouteParam, "control-capability-leases"}, ControlRouteRunCapabilityLeases},
	{[]string{"api", "runs", controlRouteParam, "input-requests"}, ControlRouteRunInputRequests},
	{[]string{"api", "runs", controlRouteParam, "control-commands"}, ControlRouteRunCommands},
	{[]string{"api", "control-capability-leases", controlRouteParam}, ControlRouteCapabilityLeaseDetail},
	{[]string{"api", "control-commands", controlRouteParam}, ControlRouteCommandRootDetail},
	{[]string{"api", "agent-mode", "deliveries", controlRouteParam, "external-stage-handoffs"}, ControlRouteExternalHandoffCreate},
	{[]string{"api", "agent-mode", "external-stage-handoffs", controlRouteParam, "mint"}, ControlRouteExternalHandoffMint},
	{[]string{"api", "agent-mode", "external-stage-handoffs", controlRouteParam, "rotate"}, ControlRouteExternalHandoffRotate},
	{[]string{"api", "agent-mode", "external-stage-handoffs", controlRouteParam, "revoke"}, ControlRouteExternalHandoffRevoke},
	{[]string{"api", "external-stage", "handoffs", controlRouteParam}, ControlRouteExternalHandoffPull},
	{[]string{"api", "external-stage", "handoffs", controlRouteParam, "accept"}, ControlRouteExternalHandoffAccept},
	{[]string{"api", "external-stage", "handoffs", controlRouteParam, "reports"}, ControlRouteExternalHandoffReport},
}

// ControlRouteClasses returns every label the classifier can produce.
// Tests assert the vocabulary is closed; nothing at runtime needs it.
func ControlRouteClasses() []ControlRouteClass {
	out := make([]ControlRouteClass, 0, len(controlRoutes))
	seen := map[ControlRouteClass]struct{}{}
	for _, route := range controlRoutes {
		if _, dup := seen[route.class]; dup {
			continue
		}
		seen[route.class] = struct{}{}
		out = append(out, route.class)
	}
	return out
}

// ClassifyControlPath reports the route class of an exact path.
//
// The path is compared segment by segment with no normalization: no
// case folding, no trailing-slash tolerance, no ".." collapsing. A
// route the mux would not dispatch to a control handler must not be
// classified as one, and a route it would dispatch to must not slip
// past because of a spelling variant.
func ClassifyControlPath(path string) (ControlRouteClass, bool) {
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	segments := strings.Split(path[1:], "/")
	for _, route := range controlRoutes {
		if len(route.segments) != len(segments) {
			continue
		}
		if matchControlSegments(route.segments, segments) {
			return route.class, true
		}
	}
	return "", false
}

func matchControlSegments(want, got []string) bool {
	for i, segment := range want {
		if segment == controlRouteParam {
			// A parameter matches one opaque, non-empty segment. Empty
			// means a doubled or trailing slash, which is a different
			// route to the mux and must stay one here too.
			if got[i] == "" {
				return false
			}
			continue
		}
		if got[i] != segment {
			return false
		}
	}
	return true
}

// ClassifyControlRequest is the form middleware uses.
//
// chi dispatches on RawPath when the URL carried percent-escapes and on
// Path otherwise. The classifier follows that choice exactly: a request
// that chi treats as a structural near-miss must keep ordinary middleware
// behavior, while an encoded opaque parameter that chi dispatches to a
// frozen route must receive the control privacy envelope.
func ClassifyControlRequest(r *http.Request) (ControlRouteClass, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	if r.URL.RawPath != "" {
		return ClassifyControlPath(r.URL.RawPath)
	}
	return ClassifyControlPath(r.URL.Path)
}

// IsControlRequest is the boolean form for middleware that only needs to
// decide whether to step aside.
func IsControlRequest(r *http.Request) bool {
	_, ok := ClassifyControlRequest(r)
	return ok
}
