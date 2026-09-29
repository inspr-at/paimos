// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (rt *runtime) cmdOutcome() *Command {
	return &Command{
		Name:  "outcome",
		Short: "Record an objective outcome of agent work",
		Use:   "outcome <command>",
		subs:  []*Command{rt.cmdOutcomeRecord()},
	}
}

func (rt *runtime) cmdOutcomeRecord() *Command {
	var ticket, kind, idem, session, rules, verdict, model, route, round, findings, summary, result, name, target string
	return &Command{
		Name:  "record",
		Short: "Record a review verdict, fix round, CI result or revert",
		Long:  "The same idempotency key and body replay the original outcome. A different body conflicts. Keys starting with auto: are reserved. Marking a ticket done and including it in a release are recorded automatically.",
		Use:   "outcome record --ticket <key> --kind <kind> --idempotency-key <key>",
		addFlags: func(fs *flagSet) {
			fs.string(&ticket, "ticket", 0, "ticket key or node id")
			fs.string(&kind, "kind", 0, "review_verdict, fix_round, ci_result or revert")
			fs.string(&idem, "idempotency-key", 0, "retry key for this outcome")
			fs.string(&session, "session", 0, "harness session UUID")
			fs.string(&rules, "rules-version", 0, "rules version, when one is known")
			fs.string(&verdict, "verdict", 0, "pass or fail")
			fs.string(&model, "reviewer-model", 0, "reviewer model")
			fs.string(&route, "route", 0, "review route")
			fs.string(&round, "round", 0, "round number")
			fs.string(&findings, "findings", 0, "finding count")
			fs.string(&summary, "summary", 0, "short summary")
			fs.string(&result, "result", 0, "pass or fail")
			fs.string(&name, "name", 0, "CI check name")
			fs.string(&target, "target", 0, "what was reverted")
		},
		run: func(args []string) error {
			return rt.recordOutcome(ticket, kind, idem, session, rules, verdict, model, route, round, findings, summary, result, name, target)
		},
	}
}

type outcomeWire struct {
	Kind         string         `json:"kind"`
	Ticket       string         `json:"ticket"`
	SessionID    string         `json:"session_id,omitempty"`
	RulesVersion string         `json:"rules_version,omitempty"`
	Payload      map[string]any `json:"payload"`
}

func (rt *runtime) recordOutcome(ticket, kind, idem, session, rules, verdict, model, route, round, findings, summary, result, name, target string) error {
	ticket = strings.TrimSpace(ticket)
	kind = strings.TrimSpace(kind)
	idem = strings.TrimSpace(idem)
	if ticket == "" {
		return usagef("--ticket is required")
	}
	if kind != "review_verdict" && kind != "fix_round" && kind != "ci_result" && kind != "revert" {
		return usagef("--kind must be review_verdict, fix_round, ci_result or revert")
	}
	if idem == "" {
		return usagef("--idempotency-key is required")
	}
	if strings.HasPrefix(idem, "auto:") || !outcomeKeyOK(idem) {
		return usagef("invalid --idempotency-key")
	}
	session = strings.TrimSpace(session)
	if session == "" {
		session = strings.TrimSpace(rt.sessionID)
	}
	if session != "" && !validUUID(session) {
		return usagef("session id must be a UUID")
	}
	rules = strings.TrimSpace(rules)
	payload := map[string]any{}
	switch kind {
	case "review_verdict":
		if err := unused(kind, map[string]string{"result": result, "name": name, "target": target}); err != nil {
			return err
		}
		verdict = strings.TrimSpace(verdict)
		if verdict != "pass" && verdict != "fail" {
			return usagef("--verdict must be pass or fail")
		}
		payload["verdict"] = verdict
		if err := putOutcomeText(payload, "reviewer_model", model, 80); err != nil {
			return err
		}
		if err := putOutcomeText(payload, "route", route, 64); err != nil {
			return err
		}
		if err := putRound(payload, round, false); err != nil {
			return err
		}
		if err := putFindings(payload, findings); err != nil {
			return err
		}
		if err := putOutcomeText(payload, "summary", summary, 280); err != nil {
			return err
		}
	case "fix_round":
		if err := unused(kind, map[string]string{"verdict": verdict, "reviewer-model": model, "route": route, "findings": findings, "result": result, "name": name, "target": target}); err != nil {
			return err
		}
		if err := putRound(payload, round, true); err != nil {
			return err
		}
		if err := putOutcomeText(payload, "summary", summary, 280); err != nil {
			return err
		}
	case "ci_result":
		if err := unused(kind, map[string]string{"verdict": verdict, "reviewer-model": model, "route": route, "round": round, "findings": findings, "target": target}); err != nil {
			return err
		}
		result = strings.TrimSpace(result)
		if result != "pass" && result != "fail" {
			return usagef("--result must be pass or fail")
		}
		payload["result"] = result
		if err := putOutcomeText(payload, "name", name, 80); err != nil {
			return err
		}
		if err := putOutcomeText(payload, "summary", summary, 280); err != nil {
			return err
		}
	default:
		if err := unused(kind, map[string]string{"verdict": verdict, "reviewer-model": model, "route": route, "round": round, "findings": findings, "result": result, "name": name}); err != nil {
			return err
		}
		if err := putOutcomeText(payload, "summary", summary, 280); err != nil {
			return err
		}
		if payload["summary"] == nil {
			return usagef("--summary is required")
		}
		if err := putOutcomeText(payload, "target", target, 80); err != nil {
			return err
		}
	}
	body := outcomeWire{Kind: kind, Ticket: ticket, SessionID: session, RulesVersion: rules, Payload: payload}
	var recorded map[string]any
	if err := rt.doHeaders(http.MethodPost, "/api/outcomes", body, &recorded, map[string]string{"Idempotency-Key": idem}); err != nil {
		return err
	}
	if rt.jsonOut {
		return rt.printJSON(recorded)
	}
	label, _ := recorded["ticket_key"].(string)
	if label == "" {
		label = ticket
	}
	fmt.Fprintf(rt.stdout, "recorded %s %s %s\n", recorded["kind"], label, recorded["id"])
	return nil
}

func outcomeKeyOK(value string) bool {
	if len(value) < 8 || len(value) > 200 {
		return false
	}
	for i, r := range value {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '/' || r == '-'
		if i == 0 {
			ok = r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		}
		if !ok {
			return false
		}
	}
	return true
}

func unused(kind string, flags map[string]string) error {
	var bad []string
	for name, value := range flags {
		if strings.TrimSpace(value) != "" {
			bad = append(bad, "--"+name)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return usagef("%s is not used for %s", strings.Join(bad, " "), kind)
}

func putOutcomeText(payload map[string]any, field, value string, max int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len([]rune(value)) > max {
		return usagef("--%s is too long", strings.ReplaceAll(field, "_", "-"))
	}
	payload[field] = value
	return nil
}

func putRound(payload map[string]any, raw string, required bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return usagef("--round is required")
		}
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 99 {
		return usagef("--round must be from 1 to 99")
	}
	payload["round"] = n
	return nil
}

func putFindings(payload map[string]any, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 999 {
		return usagef("--findings must be from 0 to 999")
	}
	payload["findings"] = n
	return nil
}
