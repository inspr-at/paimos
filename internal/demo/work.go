// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const (
	daemonID   = "demo-reading-room-daemon"
	generation = "demo-gen-1"
)

func (s *seeder) agents() error {
	var err error
	scopes := []string{
		"approvals.request", "approvals.propose",
		"account.manage", "run.claim", "run.telemetry", "run.create",
		"harness.write", "harness.worker", "work_orders.write", "work_orders.read",
		"nodes.read", "nodes.write",
	}
	s.scribe, s.scribeKey, err = s.agent("Lumen Scribe", append(scopes, "intake.write"))
	if err != nil {
		return err
	}
	s.clerk, s.clerkKey, err = s.agent("Harbor Clerk", scopes)
	if err != nil {
		return err
	}
	s.scout, s.scoutKey, err = s.agent("Glass Scout", scopes)
	return err
}

func (s *seeder) agent(name string, scopes []string) (tenant.Principal, string, error) {
	keyID, agentID, token, err := auth.OperatorCreateAgentKey(s.ctx, s.pool, s.tenantID, name, "", scopes, nil)
	if err != nil {
		return tenant.Principal{}, "", fmt.Errorf("agent %s: %w", name, err)
	}
	if name == "Lumen Scribe" {
		if err := auth.OperatorGrantJourneyScopes(s.ctx, s.pool, s.tenantID, keyID, agentID); err != nil {
			return tenant.Principal{}, "", err
		}
	}
	return tenant.Principal{ID: agentID, TenantID: s.tenantID, Kind: tenant.Agent, Name: name}, token, nil
}

type demoWork struct {
	agent                                             tenant.Principal
	key, harness, slug, accountLabel, project, ticket string
	title, brief, logicalName, instructions           string
}

func (s *seeder) work() error {
	var profiles []modelregistry.Profile
	if err := s.api.do(s.admin, "", http.MethodGet, "/api/models", nil, http.StatusOK, &profiles, nil); err != nil {
		return fmt.Errorf("models: %w", err)
	}
	for _, work := range []demoWork{
		{s.scribe, s.scribeKey, "codex", "lumen-scribe", "Lumen desk", s.lumenID, s.ids["LT-1"],
			"Lumen lantern pass", "Review the fictional lantern label and report the result.", "lantern-review/SKILL.md",
			"Review the fictional lantern label. Ask a person before changing the release.\n"},
		{s.clerk, s.clerkKey, "claude", "harbor-clerk", "Harbor desk", s.ids["HARBOR-1"], s.ids["HT-1"],
			"Harbor ledger pass", "Review the fictional berth label and report the result.", "berth-review/SKILL.md",
			"Review the fictional berth label. Ask a person before editing the ticket.\n"},
		{s.scout, s.scoutKey, "grok", "glass-scout", "North Glass desk", s.ids["NGLASS-1"], s.ids["NT-1"],
			"North Glass pane pass", "Review the fictional pane label and report the result.", "pane-review/SKILL.md",
			"Review the fictional pane label. Ask a person before agreeing requirements.\n"},
	} {
		profile, err := s.demoProfile(profiles, work.harness)
		if err != nil {
			return err
		}
		if err := s.completedWork(work, profile); err != nil {
			return fmt.Errorf("%s history: %w", work.agent.Name, err)
		}
	}
	return s.pendingApproval()
}

func (s *seeder) demoProfile(profiles []modelregistry.Profile, harness string) (modelregistry.Profile, error) {
	for _, profile := range profiles {
		if profile.Enabled && profile.Harness == harness {
			return profile, nil
		}
	}
	// The built-in xAI pins use Cursor. Reuse that registry pin's model,
	// effort and version for a demo Grok enrollment, without inventing a
	// model ID or changing any existing profile or role route.
	if harness == "grok" {
		for _, source := range profiles {
			if !source.Enabled || source.Harness != "cursor" || source.Family != "xai" {
				continue
			}
			var profile modelregistry.Profile
			err := s.api.do(s.admin, "", http.MethodPost, "/api/models", map[string]any{
				"slug": "demo-grok-history", "version": source.Version, "harness": harness,
				"family": source.Family, "model": source.Model, "effort": source.Effort, "tier": source.Tier,
			}, http.StatusCreated, &profile, nil)
			return profile, err
		}
	}
	return modelregistry.Profile{}, fmt.Errorf("no enabled %s model profile", harness)
}

func (s *seeder) completedWork(work demoWork, profile modelregistry.Profile) error {
	lease := "demo-" + work.slug + "-lease-000000000001"
	var order workorders.Order
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/work-orders", map[string]any{
		"title": work.title, "body": "Fictional screenshot work order. No harness is running.",
		"parent_id": work.project, "assignee_principal_id": work.agent.ID,
		"criteria": []string{"The fictional label is reviewed"},
	}, http.StatusCreated, &order, nil); err != nil {
		return fmt.Errorf("work order: %w", err)
	}
	if err := s.api.do(s.admin, "", http.MethodPatch, "/api/work-orders/"+order.NodeID, map[string]any{
		"expected_revision": order.Revision, "status": "ready",
	}, http.StatusOK, &order, nil); err != nil {
		return fmt.Errorf("ready work order: %w", err)
	}
	var run idBody
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/work-orders/"+order.NodeID+"/runs", map[string]any{
		"agent_principal_id": work.agent.ID, "model_profile_id": profile.ID,
	}, http.StatusCreated, &run, nil); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	var account idBody
	if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/agent-accounts", map[string]any{
		"account_key": work.slug + "-local", "harness": work.harness, "daemon_id": daemonID,
		"label": work.accountLabel, "max_parallel_runs": 1,
	}, http.StatusCreated, &account, nil); err != nil {
		return fmt.Errorf("agent account: %w", err)
	}
	// The launch cascade comes from account metadata and its explicit profile
	// grant. It is configuration for the fictional desk, not runner presence.
	if err := s.api.do(work.agent, work.key, http.MethodPut, "/api/agent-accounts/"+account.ID+"/metadata", map[string]any{
		"label": work.accountLabel, "plan": "Demo allowance", "host_label": "Demo workstation",
		"allowed_model_profile_ids": []string{profile.ID},
	}, http.StatusOK, nil, nil); err != nil {
		return fmt.Errorf("account metadata: %w", err)
	}
	// The fictional screenshot desk has explicit all-day work hours. Its manual
	// allowance cannot supply a vendor measurement or exempt it from schedules.
	schedule := capacity.DefaultSchedule("UTC")
	schedule.Reserve = capacity.ReserveOff
	for i := range schedule.Week {
		schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	if err := s.api.do(s.admin, "", http.MethodPut, "/api/agent-accounts/capacity/schedule", map[string]any{
		"scope": "account", "account_id": account.ID, "schedule": schedule,
	}, http.StatusNoContent, nil, nil); err != nil {
		return fmt.Errorf("account schedule: %w", err)
	}
	if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe", map[string]any{
		"daemon_id": daemonID, "daemon_generation": generation, "available": true,
	}, http.StatusOK, nil, nil); err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	start := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	end := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	window := []byte(fmt.Sprintf(`{"starts_at":%q,"ends_at":%q,"unit":"requests","allowance":100000,"pace_model":"unrestricted","burst_ratio":0}`, start, end))
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", jsonRaw(window), http.StatusCreated, nil, nil); err != nil {
		return fmt.Errorf("allowance window: %w", err)
	}
	var routed struct {
		Reservations []struct {
			ID string `json:"reservation_id"`
		} `json:"reservations"`
	}
	if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/agent-accounts/route", map[string]any{
		"run_id": run.ID, "daemon_id": daemonID, "account_ids": []string{account.ID},
		"estimated_units": map[string]int64{"requests": 1},
	}, http.StatusOK, &routed, nil); err != nil {
		return fmt.Errorf("route: %w", err)
	}
	ids := make([]string, 0, len(routed.Reservations))
	for _, item := range routed.Reservations {
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return fmt.Errorf("route reserved nothing")
	}
	if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/runs/"+run.ID+"/claim", map[string]any{
		"daemon_id": daemonID, "daemon_generation": generation, "reservation_ids": ids,
	}, http.StatusOK, nil, nil); err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	headers := map[string]string{agentruns.DaemonHeader: daemonID, agentruns.GenerationHeader: generation}
	if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/runs/"+run.ID+"/telemetry", map[string]any{
		"sequence": 1, "kind": "finished", "status": "completed",
		"input_tokens_delta": 0, "output_tokens_delta": 0, "cost_micros_delta": 0,
		"tool_count_delta": 0, "turn_count_delta": 0,
	}, http.StatusOK, nil, headers); err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	ticket := work.ticket
	label := work.title
	if ticket == s.ids["LT-1"] {
		label = work.agent.Name + " · Done"
	}
	var session idBody
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+work.project+"/harness-sessions", map[string]any{
		"agent_principal_id": work.agent.ID, "run_id": run.ID, "ticket_node_id": ticket,
		"work_order_id": order.NodeID, "harness": work.harness, "host": "Demo workstation",
		"management_mode": "managed", "role": "worker", "work_shape": "ship",
		"advertised_capabilities": []string{"inbox", "status"},
		"harness_session_ref":     "demo-" + work.slug + "-session-v1", "worker_lease": lease,
		"display_label": label, "model": profile.Model, "reasoning_effort": profile.Effort,
		"account_label": work.accountLabel, "brief": work.brief,
	}, http.StatusCreated, &session, nil); err != nil {
		return fmt.Errorf("harness session: %w", err)
	}
	path := "/api/projects/" + work.project + "/harness-sessions/" + session.ID
	// Hash authored fictional instruction bytes, never workstation files or
	// a version string. The provenance endpoint persists identities only.
	instructions := work.instructions
	digest := sha256.Sum256([]byte(instructions))
	workerHeaders := map[string]string{"X-Aeon-Worker-Lease": lease}
	if err := s.api.do(work.agent, work.key, http.MethodPost, path+"/provenance", map[string]any{
		"items": []map[string]any{{
			"kind": "skill", "logical_name": work.logicalName, "hash_kind": "content",
			"content_sha256": hex.EncodeToString(digest[:]), "byte_size": len(instructions),
		}},
	}, http.StatusOK, nil, workerHeaders); err != nil {
		return fmt.Errorf("instruction provenance: %w", err)
	}
	if err := s.comment(work.agent, ticket, fmt.Sprintf("I work on this — session: %s (%s); role: builder; started: %s\n\nFictional demo: %s", work.title, session.ID, start, work.brief)); err != nil {
		return err
	}
	if ticket == s.ids["LT-1"] {
		if err := s.showcaseApproval(work); err != nil {
			return err
		}
		// Complete real criteria and evidence through the ordinary handlers.
		// The positive label describes the stored outcome, not runner presence.
		for _, criterion := range order.Criteria {
			if err := s.api.do(s.admin, "", http.MethodPost, "/api/work-orders/"+order.NodeID+"/criteria/"+criterion.ID+"/check", map[string]any{
				"checked": true,
			}, http.StatusOK, nil, nil); err != nil {
				return fmt.Errorf("check showcase criterion: %w", err)
			}
		}
		if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/work-orders/"+order.NodeID+"/evidence", map[string]any{
			"kind": "text", "reference": "Fictional demo: the lantern label was reviewed and Demo Operator approved the proposed edit.", "run_id": run.ID,
		}, http.StatusCreated, nil, nil); err != nil {
			return fmt.Errorf("showcase evidence: %w", err)
		}
		if err := s.api.do(s.admin, "", http.MethodGet, "/api/work-orders/"+order.NodeID, nil, http.StatusOK, &order, nil); err != nil {
			return err
		}
		if err := s.api.do(s.admin, "", http.MethodPatch, "/api/work-orders/"+order.NodeID, map[string]any{
			"expected_revision": order.Revision, "status": "done",
		}, http.StatusOK, nil, nil); err != nil {
			return fmt.Errorf("complete showcase work order: %w", err)
		}
	}
	if err := s.comment(work.agent, ticket, fmt.Sprintf("The fictional label review is complete. [Session evidence and instruction provenance](/agents/%s) are linked to run `%s`. This is seeded history; no runner executed it.", session.ID, run.ID)); err != nil {
		return err
	}
	if err := s.comment(s.nia, ticket, "The fictional label reads clearly. Keep this review with the ticket so the next person can trace the decision."); err != nil {
		return err
	}
	// End the historical session. Never send a heartbeat to make a screenshot
	// look live; real runner presence requires a paired local daemon.
	if err := s.api.do(work.agent, work.key, http.MethodPost, path+"/stop", map[string]any{
		"reason": "process_exited",
	}, http.StatusOK, nil, workerHeaders); err != nil {
		return fmt.Errorf("end demo session: %w", err)
	}
	if ticket == s.ids["HT-1"] {
		// Keep a real rework exception on the non-showcase Harbor ticket.
		if err := s.api.do(s.admin, "", http.MethodPut, "/api/harness-sessions/"+session.ID+"/delivery-rating", map[string]any{
			"tags": []string{"rework"}, "comment": "Fictional demo: the berth label needs a clearer north-arrow before another review.",
		}, http.StatusOK, nil, nil); err != nil {
			return fmt.Errorf("Harbor rework example: %w", err)
		}
	}
	return nil
}

func (s *seeder) showcaseApproval(work demoWork) error {
	if err := s.comment(work.agent, work.ticket, "Fictional progress note: the lantern label draft is ready. I checked the shelf name and will ask a person before editing it."); err != nil {
		return err
	}
	if err := s.comment(s.nia, work.ticket, "Fictional human review: the shelf name is clear. Please ask Demo Operator to approve the proposed lantern label edit."); err != nil {
		return err
	}
	var approval idBody
	if err := s.api.do(work.agent, work.key, http.MethodPost, "/api/approvals", map[string]any{
		"scope": "nodes.write", "resource_kind": "node", "resource_id": work.ticket,
		"rationale":  "Fictional demo: Lumen Scribe asks a person to approve the proposed lantern label edit.",
		"expires_at": time.Now().Add(72 * time.Hour).UTC(),
	}, http.StatusCreated, &approval, nil); err != nil {
		return fmt.Errorf("showcase approval request: %w", err)
	}
	// Activity projects comments and node changes, so leave reference notes beside
	// the real request and decision rather than inventing approval events.
	if err := s.comment(work.agent, work.ticket, fmt.Sprintf("Fictional approval request: may I edit the lantern label? Approval `%s` is waiting for Demo Operator.", approval.ID)); err != nil {
		return err
	}
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/approvals/"+approval.ID+"/decision", map[string]any{
		"decision": "approved",
	}, http.StatusOK, nil, nil); err != nil {
		return fmt.Errorf("showcase approval decision: %w", err)
	}
	return s.comment(s.admin, work.ticket, fmt.Sprintf("Fictional approval decision: approved the proposed lantern label edit requested by Lumen Scribe. Approval `%s` keeps the decision with this ticket.", approval.ID))
}

func (s *seeder) pendingApproval() error {
	code, raw, err := s.api.call(s.clerk, s.clerkKey, http.MethodPost, "/api/approvals", map[string]any{
		"scope": "nodes.write", "resource_kind": "node", "resource_id": s.ids["HT-1"],
		"rationale":  "Fictional demo: Harbor Clerk asks to edit a ticket and nobody has answered.",
		"expires_at": time.Now().Add(72 * time.Hour).UTC(),
	}, nil)
	if err != nil {
		return err
	}
	if code != http.StatusCreated {
		return fmt.Errorf("pending approval: status %d: %s", code, clip(raw))
	}
	return nil
}

// jsonRaw marshals as already-encoded JSON.
type jsonRaw []byte

func (r jsonRaw) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}
