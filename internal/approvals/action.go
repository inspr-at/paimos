// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const actionScopePrefix = "approvals.request.outward_"

// ActionOutcomeWriter connects the existing inbox acceptance protocol without
// coupling the approval source to inbox's authentication/phone dependencies.
// The coordinator injects the same writer into HTTP, phone and expiry paths.
type ActionOutcomeWriter func(ctx context.Context, tx pgx.Tx, system tenant.Principal, messageID, recipient, project, key, body string) error

// ActionInput freezes one exact effect, rather than asking for a reusable scope.
type ActionInput struct {
	IdempotencyKey string    `json:"idempotency_key"`
	Kind           string    `json:"kind"`
	What           string    `json:"what"`
	Why            string    `json:"why"`
	ExactEffect    string    `json:"exact_effect"`
	Reversibility  string    `json:"reversibility"`
	ReversalPlan   string    `json:"reversal_plan"`
	HumanInput     string    `json:"human_input"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type ActionRequest struct {
	ActionInput
	ID                   string         `json:"id"`
	ProjectID            string         `json:"project_id"`
	AgentPrincipalID     string         `json:"agent_principal_id"`
	ApprovalID           *string        `json:"approval_id"`
	RequestDigestSHA256  string         `json:"request_digest_sha256"`
	State                string         `json:"state"`
	Reason               string         `json:"reason"`
	RequestedAt          time.Time      `json:"requested_at"`
	ResolvedAt           *time.Time     `json:"resolved_at"`
	DecidedByPrincipalID *string        `json:"decided_by_principal_id"`
	OutcomeReceipt       *ActionReceipt `json:"outcome_receipt,omitempty"`
}

// ActionReceipt reports transport evidence, independently of the decision.
// queued never claims that the agent received or executed an approved effect.
type ActionReceipt struct {
	MessageID     string     `json:"message_id"`
	State         string     `json:"state"`
	HandedOffAt   *time.Time `json:"handed_off_at"`
	FailureReason string     `json:"failure_reason"`
}

var actionKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Structured human_input is mandatory and is the primary refusal gate. These
// recognizers also refuse common asks directed at a person when human_input=none.
// Mentioning credentials or requesting deploy/merge approval is not a human ask.
// This is defense in depth, not arbitrary natural-language classification.
var forbiddenHumanAsk = regexp.MustCompile(`(?i)(?:\b(?:(?:give|send|provide|paste|share|supply)[ \t]+(?:me|us)|(?:gib|sende|schick|teile)[ \t]+(?:mir|uns))\b[^\n.!?]{0,120}\b(?:secrets?|tokens?|api[ _-]?keys?|passwords?|passwörter|passwort|zugangsdaten|schlüssel)\b|\b(?:secrets?|tokens?|api[ _-]?keys?|passwords?|passwörter|passwort|zugangsdaten|schlüssel)\b[^\n.!?]{0,80}\b(?:from you|from the user|from the human|von dir|vom menschen)\b|\b(?:please|you must|you need to|can you|could you|bitte|du musst)\b[^\n.!?]{0,80}\b(?:manually|manual steps?|run this|run the command|execute this|manuell|ausführen)\b)`)

func normalizeAction(in *ActionInput) error {
	fields := []struct {
		text *string
		max  int
	}{{&in.IdempotencyKey, 128}, {&in.What, 500}, {&in.Why, 2000}, {&in.ExactEffect, 4000}, {&in.ReversalPlan, 2000}}
	for i, field := range fields {
		*field.text = strings.TrimSpace(*field.text)
		if (i != 4 && *field.text == "") || strings.ContainsRune(*field.text, 0) || !utf8.ValidString(*field.text) || utf8.RuneCountInString(*field.text) > field.max {
			return fail(400, "action facts are required and must fit their documented bounds")
		}
	}
	if !actionKindPattern.MatchString(in.Kind) {
		return fail(400, "invalid action kind")
	}
	if in.Reversibility != "reversible" && in.Reversibility != "partial" && in.Reversibility != "irreversible" || in.Reversibility != "irreversible" && in.ReversalPlan == "" {
		return fail(400, "describe reversibility and the reversal plan")
	}
	switch in.HumanInput {
	case "none", "secrets", "tokens", "manual_steps":
	default:
		return fail(400, "human_input must explicitly declare none, secrets, tokens or manual_steps")
	}
	if in.ExpiresAt.IsZero() {
		return fail(400, "expires_at is required")
	}
	in.ExpiresAt = in.ExpiresAt.UTC().Truncate(time.Microsecond)
	return nil
}

func actionRefusal(in ActionInput) string {
	if in.HumanInput != "none" || forbiddenHumanAsk.MatchString(strings.Join([]string{in.What, in.Why, in.ExactEffect, in.ReversalPlan}, "\n")) {
		return "Refused: agents must use existing authorized tools; asking a person for secrets, tokens or manual steps is prohibited."
	}
	return ""
}

func actionDigest(p tenant.Principal, project string, in ActionInput) string {
	data, _ := json.Marshal(struct {
		Tenant, Agent, Project string
		Facts                  ActionInput
	}{p.TenantID, p.ID, project, in})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hasActionScope(scope string) bool { return strings.HasPrefix(scope, actionScopePrefix) }

func actionRationale(a ActionRequest) string {
	return fmt.Sprintf("Outward action · %s\nWhat: %s\nWhy: %s\nExact effect: %s\nReversibility: %s\nReversal plan: %s\nRequested by: %s\nProject: %s\nHuman input: %s\nRequest: %s\nExact facts SHA-256: %s\nA decision records this effect only; it grants no general permission and does not execute it.", a.Kind, a.What, a.Why, a.ExactEffect, a.Reversibility, a.ReversalPlan, a.AgentPrincipalID, a.ProjectID, a.HumanInput, a.ID, a.RequestDigestSHA256)
}

func actionRequire(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission, project string) error {
	err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
	if errors.Is(err, authz.ErrForbidden) {
		return fail(403, "action request requires "+permission)
	}
	return err
}

func lockActionWrite(ctx context.Context, tx pgx.Tx, tenantID string) error {
	return db.LockTree(ctx, tx, tenantID)
}

func liveActionProject(ctx context.Context, tx pgx.Tx, project string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project'`, project).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "project not found")
	}
	return err
}

const actionSelect = `SELECT a.id::text,a.project_id::text,a.agent_principal_id::text,a.approval_id::text,
 a.idempotency_key,a.kind,a.what,a.why,a.exact_effect,a.reversibility,a.reversal_plan,a.human_input,a.expires_at,
 a.request_digest_sha256,a.state,a.reason,a.requested_at,a.resolved_at,a.decided_by_principal_id::text,
 a.outcome_message_id::text,r.state,r.handed_off_at,r.failure_reason
 FROM action_approval_requests a LEFT JOIN inbox_receipts r ON r.tenant_id=a.tenant_id AND r.message_id=a.outcome_message_id`

func scanAction(row pgx.Row) (ActionRequest, error) {
	var a ActionRequest
	var message, state, reason *string
	var handedOff *time.Time
	err := row.Scan(&a.ID, &a.ProjectID, &a.AgentPrincipalID, &a.ApprovalID,
		&a.IdempotencyKey, &a.Kind, &a.What, &a.Why, &a.ExactEffect, &a.Reversibility, &a.ReversalPlan, &a.HumanInput, &a.ExpiresAt,
		&a.RequestDigestSHA256, &a.State, &a.Reason, &a.RequestedAt, &a.ResolvedAt, &a.DecidedByPrincipalID,
		&message, &state, &handedOff, &reason)
	if message != nil && state != nil && reason != nil {
		a.OutcomeReceipt = &ActionReceipt{MessageID: *message, State: *state, HandedOffAt: handedOff, FailureReason: *reason}
	}
	return a, err
}

func loadAction(ctx context.Context, tx pgx.Tx, id string) (ActionRequest, error) {
	return scanAction(tx.QueryRow(ctx, actionSelect+` WHERE a.id=$1`, id))
}

func databaseActionNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func (m *Module) requestAction(ctx context.Context, p tenant.Principal, authorization, project string, in ActionInput) (ActionRequest, error) {
	if m.actionOutcome == nil {
		return ActionRequest{}, fail(503, "action outcome delivery is unavailable")
	}
	var out ActionRequest
	err := m.inTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockActionWrite(ctx, tx, p.TenantID); err != nil {
			return err
		}
		scopes, err := lookupKeyScopes(ctx, tx, authorization, p.ID)
		if err != nil {
			return err
		}
		if !ScopeWithinKey("approvals.request", scopes) {
			return fail(403, "action request exceeds the API key")
		}
		if err = liveActionProject(ctx, tx, project); err != nil {
			return err
		}
		if err = actionRequire(ctx, tx, p, "approvals.request", project); err != nil {
			return err
		}
		digest := actionDigest(p, project, in)
		out, err = scanAction(tx.QueryRow(ctx, actionSelect+` WHERE a.agent_principal_id=$1 AND a.idempotency_key=$2`, p.ID, in.IdempotencyKey))
		if err == nil {
			if out.ProjectID != project || out.RequestDigestSHA256 != digest {
				return fail(409, "idempotency key already binds different action facts")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now, err := databaseActionNow(ctx, tx)
		if err != nil {
			return err
		}
		if !in.ExpiresAt.After(now) || in.ExpiresAt.Sub(now) > 24*time.Hour {
			return fail(400, "expires_at must be in the future within 24 hours")
		}
		kind, err := actionKindTx(ctx, tx, in.Kind)
		if err != nil {
			return err
		}
		if !kind.Enabled {
			return fail(403, "action kind is disabled")
		}
		out = ActionRequest{ActionInput: in, ProjectID: project, AgentPrincipalID: p.ID, RequestDigestSHA256: digest, State: "pending", RequestedAt: now}
		if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&out.ID); err != nil {
			return err
		}
		out.Reason = actionRefusal(in)
		policy, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: p.ID, ProjectID: project, Operation: "action.request", Key: "action/" + out.ID, Scopes: []string{"approvals.request"}, RequireBudget: true, Now: now})
		if err != nil {
			return err
		}
		if !policy.Allowed {
			out.Reason = policy.Reason
		}
		var messageID *string
		var approval Approval
		if out.Reason != "" {
			out.State, out.ResolvedAt = "refused", &now
			var id string
			if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
				return err
			}
			messageID = &id
		} else {
			var id string
			err = tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at)
 VALUES($1,$2,$2,$3,'node',$4,$5,$6) RETURNING id::text`, p.TenantID, p.ID, actionScopePrefix+digest, project, actionRationale(out), in.ExpiresAt).Scan(&id)
			if err != nil {
				return err
			}
			out.ApprovalID = &id
			approval, err = loadApproval(ctx, tx, id)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO action_approval_requests(tenant_id,id,project_id,agent_principal_id,approval_id,idempotency_key,kind,what,why,exact_effect,reversibility,reversal_plan,human_input,expires_at,request_digest_sha256,state,reason,requested_at,resolved_at,outcome_message_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			p.TenantID, out.ID, project, p.ID, out.ApprovalID, in.IdempotencyKey, in.Kind, in.What, in.Why, in.ExactEffect, in.Reversibility, in.ReversalPlan, in.HumanInput, in.ExpiresAt, digest, out.State, out.Reason, now, out.ResolvedAt, messageID)
		if err != nil {
			return err
		}
		if messageID != nil {
			if err = m.notifyActionOutcome(ctx, tx, p.TenantID, &out, *messageID); err != nil {
				return err
			}
		} else if _, err = events.Append(ctx, tx, p, events.Change{Type: eventProposed, NodeID: &project, After: withoutAgentName(approval)}); err != nil {
			return err
		}
		event := "action_request.requested"
		if out.State == "refused" {
			event = "action_request.refused"
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: event, NodeID: &project, After: out})
		if err != nil {
			return err
		}
		return assistants.AppendDecisionsTx(ctx, tx, p, policy)
	})
	return out, err
}

func canDecideAction(ctx context.Context, tx pgx.Tx, p tenant.Principal, approval Approval) error {
	if p.Kind != tenant.Person || p.KeyID != "" || p.AuthKeyID != "" || p.KeyCreatorID != "" {
		return fail(403, "outward action decisions require a person session")
	}
	if err := requirePerson(ctx, tx, p, "only a person may decide an outward action"); err != nil {
		return err
	}
	a, err := scanAction(tx.QueryRow(ctx, actionSelect+` WHERE a.approval_id=$1`, approval.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(403, "outward action facts are missing")
	}
	if err != nil {
		return err
	}
	if approval.Scope != actionScopePrefix+a.RequestDigestSHA256 || approval.AgentPrincipalID != a.AgentPrincipalID || approval.ResourceID == nil || *approval.ResourceID != a.ProjectID || a.State != "pending" {
		return fail(409, "outward action is unavailable or already resolved")
	}
	now, err := databaseActionNow(ctx, tx)
	if err != nil {
		return err
	}
	if !a.ExpiresAt.After(now) {
		return fail(409, "outward action has expired")
	}
	if err = liveActionProject(ctx, tx, a.ProjectID); err != nil {
		return err
	}
	for _, permission := range []string{"approvals.read", "approvals.decide", "approvals.decide_high"} {
		if err = actionRequire(ctx, tx, p, permission, a.ProjectID); err != nil {
			return err
		}
	}
	return assistants.CheckApproverTx(ctx, tx, p, a.AgentPrincipalID)
}

// actionDecisionFence is called before the approval request's row lock. A
// nonlocking lookup chooses only the additional tree fence for this new family.
func actionDecisionFence(ctx context.Context, tx pgx.Tx, id, tenantID string) error {
	var action bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM approval_requests WHERE id=$1 AND scope LIKE 'approvals.request.outward\_%' ESCAPE '\')`, id).Scan(&action); err != nil {
		return err
	}
	if action {
		return lockActionWrite(ctx, tx, tenantID)
	}
	return nil
}

func (m *Module) decideActionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, before Approval, decision, reason string) (Approval, error) {
	if strings.TrimSpace(reason) == "" {
		return Approval{}, fail(400, "an outward action decision requires a reason")
	}
	a, err := scanAction(tx.QueryRow(ctx, actionSelect+` WHERE a.approval_id=$1 FOR UPDATE OF a`, before.ID))
	if err != nil {
		return Approval{}, err
	}
	var policy assistants.Decision
	if decision == "approved" {
		policy, err = assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: a.AgentPrincipalID, ProjectID: a.ProjectID, Operation: "action.approve", Key: "action/" + a.ID, Scopes: []string{"approvals.request"}, RequireBudget: true})
		if err != nil {
			return Approval{}, err
		}
		if !policy.Allowed {
			decision, reason = "denied", policy.Reason
		}
		if policy.Applicable && decision == "approved" {
			requester := tenant.Principal{ID: a.AgentPrincipalID, TenantID: p.TenantID, Kind: tenant.Agent, Scopes: []string{"approvals.request"}}
			if err = authz.RequireTx(ctx, tx, requester, "approvals.request", authz.Scope{ProjectID: a.ProjectID}); errors.Is(err, authz.ErrForbidden) {
				decision, reason = "denied", "assistant_authority_revoked"
			} else if err != nil {
				return Approval{}, err
			}
		}
	}
	if decision == "approved" {
		kind, err := actionKindTx(ctx, tx, a.Kind)
		if err != nil {
			return Approval{}, err
		}
		if !kind.Enabled {
			return Approval{}, fail(403, "action kind is disabled")
		}
	}
	now, err := databaseActionNow(ctx, tx)
	if err != nil {
		return Approval{}, err
	}
	if !a.ExpiresAt.After(now) {
		return Approval{}, fail(409, "outward action has expired")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision,reason) VALUES($1,$2,$3,$4,$5)`, p.TenantID, before.ID, p.ID, decision, reason); err != nil {
		return Approval{}, err
	}
	// Deliberately no agent_permission_grants row: the decision concerns these
	// immutable effect facts and cannot satisfy any ordinary LiveGrant query.
	out, err := loadApproval(ctx, tx, before.ID)
	if err != nil {
		return Approval{}, err
	}
	if err = exposeAgentName(ctx, tx, p, &out); err != nil {
		return Approval{}, err
	}
	if err = m.resolveActionTx(ctx, tx, p, a, decision, reason, now); err != nil {
		return Approval{}, err
	}
	event := eventDenied
	if decision == "approved" {
		event = eventApproved
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: event, NodeID: nodeRef(out), Before: withoutAgentName(before), After: withoutAgentName(out)})
	if err == nil {
		err = assistants.AppendDecisionsTx(ctx, tx, p, policy)
	}
	return out, err
}

func (m *Module) resolveActionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, before ActionRequest, state, reason string, now time.Time) error {
	var messageID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&messageID); err != nil {
		return err
	}
	a := before
	a.State, a.Reason, a.ResolvedAt = state, reason, &now
	if state != "expired" {
		a.DecidedByPrincipalID = &p.ID
	}
	tag, err := tx.Exec(ctx, `UPDATE action_approval_requests SET state=$2,reason=$3,resolved_at=$4,decided_by_principal_id=$5,outcome_message_id=$6 WHERE id=$1 AND state='pending'`, a.ID, a.State, reason, now, a.DecidedByPrincipalID, messageID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(409, "outward action is already resolved")
	}
	if err = m.notifyActionOutcome(ctx, tx, p.TenantID, &a, messageID); err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "action_request." + state, NodeID: &a.ProjectID, Before: before, After: a})
	return err
}

// All action/request writes precede this call. Fence existing message parents
// and bound wake targets before AcceptMessageTx first acquires the event counter.
func (m *Module) notifyActionOutcome(ctx context.Context, tx pgx.Tx, tenantID string, a *ActionRequest, messageID string) error {
	if m.actionOutcome == nil {
		return fail(503, "action outcome delivery is unavailable")
	}
	var systemID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND name='System' AND roles @> ARRAY['system'] AND status='active'`).Scan(&systemID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{systemID, a.AgentPrincipalID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, a.ProjectID); err != nil {
		return err
	}
	var targets int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM inbox_delivery_targets WHERE principal_id=$1 AND kind='webhook' AND enabled ORDER BY id LIMIT 33 FOR KEY SHARE) t`, a.AgentPrincipalID).Scan(&targets); err != nil {
		return err
	}
	if targets > 32 {
		return errors.New("action outcome wake target bound exceeded")
	}
	body := fmt.Sprintf("Outward action request %s: %s\nReason: %s\nProject: %s\nExact effect: %s\nFacts SHA-256: %s\nThis is a recorded decision, not an execution receipt or a general permission grant. Verify the request's current state before using it.", a.ID, a.State, a.Reason, a.ProjectID, a.ExactEffect, a.RequestDigestSHA256)
	err := m.actionOutcome(ctx, tx, tenant.Principal{ID: systemID, TenantID: tenantID, Kind: tenant.Agent}, messageID, a.AgentPrincipalID, a.ProjectID, "action-outcome:"+a.ID, body)
	if err == nil {
		a.OutcomeReceipt = &ActionReceipt{MessageID: messageID, State: "queued"}
	}
	return err
}

func (m *Module) handleRequestAction(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		writeError(w, 403, "only an agent may request its own outward action")
		return
	}
	project := r.PathValue("projectId")
	if !uuidPattern.MatchString(project) {
		writeError(w, 404, "project not found")
		return
	}
	var in ActionInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	if err := normalizeAction(&in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := m.requestAction(ctx, p, r.Header.Get("Authorization"), project, in)
	writeResult(w, 201, out, err)
}

func (m *Module) handleGetAction(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	project, id := r.PathValue("projectId"), r.PathValue("requestId")
	if !uuidPattern.MatchString(project) || !uuidPattern.MatchString(id) {
		writeError(w, 404, "action request not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out ActionRequest
	err := m.inTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = loadAction(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "action request not found")
		}
		if err != nil {
			return err
		}
		if out.ProjectID != project || p.Kind == tenant.Agent && out.AgentPrincipalID != p.ID {
			return fail(404, "action request not found")
		}
		return actionRequire(ctx, tx, p, "approvals.read", project)
	})
	writeResult(w, 200, out, err)
}

func (m *Module) handleDecideAction(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	project, id := r.PathValue("projectId"), r.PathValue("approvalId")
	if !uuidPattern.MatchString(project) || !uuidPattern.MatchString(id) {
		writeError(w, 404, "action approval not found")
		return
	}
	var in decisionWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	reason, err := validateDecision(in)
	if err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := m.decideVerified(ctx, p, id, in.Decision, reason, func(_ pgx.Tx, a Approval) error {
		if !hasActionScope(a.Scope) || a.ResourceID == nil || *a.ResourceID != project {
			return fail(404, "action approval not found")
		}
		return nil
	})
	writeResult(w, 200, out, err)
}
