// SPDX-License-Identifier: AGPL-3.0-only
package routineguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const (
	EvaluationCapability            = "routine_evaluator_read_only_v1"
	MaxEvaluationOutput             = 8192
	MaxEvaluationTime               = 2 * time.Minute
	MaxEvaluationInputTokens  int64 = 16384
	MaxEvaluationOutputTokens int64 = 4096
)

var ErrEvaluationBudgetUnavailable = errors.New("budget_enforcement_unavailable")
var errEvaluationUnavailable = errors.New("evaluation_evidence_unavailable")

// ActionBinding is supplied by the typed action broker under its final write
// fence. PayloadDigest covers the complete typed payload, including the exact
// repository/head for PR and pipeline actions, not just a prose summary.
type ActionBinding struct {
	TenantID        string    `json:"tenant_id"`
	RunID           string    `json:"run_id"`
	ActionID        string    `json:"action_id"`
	AuthorRunID     string    `json:"author_run_id"`
	ProjectID       string    `json:"project_id"`
	TargetID        string    `json:"target_id"`
	TargetRevision  int64     `json:"target_revision"`
	TargetUpdatedAt time.Time `json:"target_updated_at"`
	Kind            string    `json:"kind"`
	PayloadDigest   string    `json:"payload_digest"`
	HeadSHA         string    `json:"head_sha,omitempty"`
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func (b ActionBinding) valid() bool {
	for _, id := range []string{b.TenantID, b.RunID, b.ActionID, b.AuthorRunID, b.ProjectID, b.TargetID} {
		if !uuid.MatchString(id) {
			return false
		}
	}
	if b.TargetRevision < 1 || b.TargetUpdatedAt.IsZero() || !validDigest(b.PayloadDigest) {
		return false
	}
	switch b.Kind {
	case "work.create", "work.update", "knowledge.write":
		return b.HeadSHA == ""
	case "pr.open", "pipeline.request":
		return reviewgate.ValidSHA(b.HeadSHA)
	}
	return false
}

func (b ActionBinding) matches(other ActionBinding) bool {
	if !b.TargetUpdatedAt.Equal(other.TargetUpdatedAt) {
		return false
	}
	// PostgreSQL and JSON may use different locations for the same instant.
	b.TargetUpdatedAt, other.TargetUpdatedAt = time.Time{}, time.Time{}
	return b == other
}

func actionPermission(kind string) string {
	switch kind {
	case "work.create", "work.update":
		return "nodes.write"
	case "knowledge.write":
		return "knowledge.write"
	case "pr.open", "pipeline.request":
		return "harness.control"
	}
	return ""
}

// EvaluationRequest is persisted before any coordinator dispatch. It contains
// only bounded content-free bindings; the broker supplies the bounded Context
// to the evaluator after commit. No owner credential or outward tool is passed.
type EvaluationRequest struct {
	Binding       ActionBinding               `json:"binding"`
	ContextDigest string                      `json:"context_digest"`
	PolicyDigest  string                      `json:"policy_digest"`
	FamilyPolicy  reviewgate.FamilyPolicy     `json:"family_policy"`
	Author        modelregistry.VerifiedModel `json:"author"`
	ProfileID     string                      `json:"profile_id"`
	AccountID     string                      `json:"account_id"`
	Harness       string                      `json:"harness"`
	Model         string                      `json:"model"`
	Family        string                      `json:"family"`
	Capability    string                      `json:"capability"`
	CreatedAt     time.Time                   `json:"created_at"`
	Deadline      time.Time                   `json:"deadline"`
	Digest        string                      `json:"digest"`
}

func (r EvaluationRequest) digest() string { r.Digest = ""; return Digest(r) }
func (r EvaluationRequest) valid() bool {
	if len(r.Harness) > 32 || len(r.Model) > 256 || len(r.Author.Harness) > 32 || len(r.Author.RequestedModel) > 256 || len(r.Author.EffectiveModel) > 256 || len(r.Author.Evidence) > 32 || !r.Binding.valid() || !validDigest(r.ContextDigest) || !validDigest(r.PolicyDigest) || !r.FamilyPolicy.Valid() || r.Capability != EvaluationCapability || r.CreatedAt.IsZero() || r.Deadline.Sub(r.CreatedAt) != MaxEvaluationTime || r.Digest != r.digest() {
		return false
	}
	author, err := r.Author.Family()
	return err == nil && uuid.MatchString(r.ProfileID) && uuid.MatchString(r.AccountID) && len(r.Model) <= 256 && harnesslaunch.FamilyMatches(r.Harness, r.Model, r.Family) && author != r.Family
}

// EvaluationGrant is a future S09/S10 reservation, not a budget-off escape.
// It represents the evaluator's fixed internal, read-only allowance; admission
// is deterministic and never asks the evaluator to approve its own launch.
type EvaluationGrant struct {
	HoldID        string    `json:"hold_id"`
	RequestDigest string    `json:"request_digest"`
	Capability    string    `json:"capability"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	Deadline      time.Time `json:"deadline"`
}

func (g EvaluationGrant) valid(r EvaluationRequest) bool {
	return uuid.MatchString(g.HoldID) && g.RequestDigest == r.Digest && g.Capability == EvaluationCapability && g.InputTokens > 0 && g.InputTokens <= MaxEvaluationInputTokens && g.OutputTokens > 0 && g.OutputTokens <= MaxEvaluationOutputTokens && g.Deadline.Equal(r.Deadline)
}

func AdmitEvaluation(r EvaluationRequest, g EvaluationGrant, now time.Time) error {
	if !r.valid() || !g.valid(r) || now.Before(r.CreatedAt) || !now.Before(r.Deadline) {
		return errors.New("invalid or expired evaluator reservation")
	}
	// There is intentionally no environment flag or caller-provided boolean
	// which can certify provider enforcement. S09/S10 must supply that boundary.
	return ErrEvaluationBudgetUnavailable
}

type StructuredVerdict struct {
	RequestDigest string  `json:"request_digest"`
	Verdict       Outcome `json:"verdict"`
	Reason        string  `json:"reason"`
}

// ParseEvaluationResult requires one explicit JSON object. Prose, quoted
// earlier verdicts, duplicate keys, unknown fields and trailing values hold.
func ParseEvaluationResult(raw []byte) (StructuredVerdict, error) {
	v := StructuredVerdict{}
	if len(raw) == 0 || len(raw) > MaxEvaluationOutput || !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
		return v, errors.New("invalid or oversized structured evaluation result")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return v, errors.New("explicit structured evaluation result required")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return v, errors.New("ambiguous structured evaluation result")
		}
		seen[key] = true
		var value string
		if err = d.Decode(&value); err != nil {
			return v, errors.New("invalid structured evaluation field")
		}
		switch key {
		case "request_digest":
			v.RequestDigest = value
		case "verdict":
			v.Verdict = Outcome(value)
		case "reason":
			v.Reason = value
		default:
			return v, errors.New("unknown structured evaluation field")
		}
	}
	if _, err = d.Token(); err != nil {
		return v, errors.New("incomplete structured evaluation result")
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return v, errors.New("trailing evaluation output")
	}
	if len(seen) != 3 || !validDigest(v.RequestDigest) || rank(v.Verdict) < 0 || strings.TrimSpace(v.Reason) == "" || len(v.Reason) > 2048 || reviewgate.SensitiveText(v.Reason) {
		return v, errors.New("missing or invalid explicit evaluation verdict")
	}
	return v, nil
}

// VerifyEvaluation rebuilds all hard/custom rules against the current action
// and policy. Callers must read evidence from the completed immutable run and
// current family policy in their final authorized transaction. A person can
// resolve needs-person, but cannot replace missing independent evaluation.
func VerifyEvaluation(sources []Source, c Context, current ActionBinding, r EvaluationRequest, status string, evidence modelregistry.VerifiedModel, result StructuredVerdict, policy reviewgate.FamilyPolicy, now time.Time) (Decision, error) {
	d, err := Evaluate(sources, c)
	if err != nil {
		return d, err
	}
	closed := func(reason string) (Decision, error) { return d, errors.New(reason) }
	if !r.valid() || !current.matches(r.Binding) || c.Action != current.Kind || c.Checkpoint != "action" || d.PolicyDigest != r.PolicyDigest || d.ContextDigest != r.ContextDigest || Digest(policy) != Digest(r.FamilyPolicy) {
		return closed("evaluation binding or policy changed; reevaluation required")
	}
	if now.Before(r.CreatedAt) || !now.Before(r.Deadline) || status != "completed" {
		return closed("evaluation incomplete or timed out")
	}
	family, err := evidence.Family()
	if err != nil || evidence.Harness != r.Harness || evidence.RequestedModel != r.Model || family != r.Family {
		return closed("evaluator effective model or family is unverified")
	}
	author, _ := r.Author.Family()
	if ok, _ := policy.Decision(author, family); !ok || author == family {
		return closed("evaluator family is not allowed")
	}
	if result.RequestDigest != r.Digest || rank(result.Verdict) < 0 || strings.TrimSpace(result.Reason) == "" || len(result.Reason) > 2048 || !utf8.ValidString(result.Reason) || strings.ContainsRune(result.Reason, 0) || reviewgate.SensitiveText(result.Reason) {
		return closed("explicit bound structured result required")
	}
	d.Result = d.DeterministicResult
	if rank(result.Verdict) > rank(d.Result) {
		d.Result = result.Verdict
	}
	for i := range d.Findings {
		if d.Findings[i].RuleID == "hard.different_family_v1" {
			d.Findings[i].Result, d.Findings[i].Reason = result.Verdict, "verified independent evaluator returned an explicit bound result"
		}
	}
	d.evaluationBinding = r.Digest
	d.evaluationVerified = Digest(d)
	return d, nil
}

// CanExecuteFor also checks the record identity which was independently
// evaluated. A copied decision for the same prose cannot authorize a new head.
func (d Decision) CanExecuteFor(r EvaluationRequest) bool {
	return r.valid() && d.CanExecute() && d.evaluationBinding == r.Digest
}

type evaluationRecord struct {
	Request        EvaluationRequest            `json:"request"`
	Decision       Decision                     `json:"decision"`
	WaitReason     string                       `json:"wait_reason"`
	EvaluatorRunID string                       `json:"evaluator_run_id,omitempty"`
	Evidence       *modelregistry.VerifiedModel `json:"evidence,omitempty"`
	Result         *StructuredVerdict           `json:"result,omitempty"`
}

type evaluationTarget struct {
	recurrence, owner, scopeKind, scopeProject string
	revision                                   int64
}

func lockEvaluationAction(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding, c Context) (evaluationTarget, []Source, error) {
	target := evaluationTarget{}
	if !b.valid() || b.TenantID != p.TenantID || c.Checkpoint != "action" || c.Action != b.Kind {
		return target, nil, errors.New("invalid typed evaluation action")
	}
	if err := ValidateContext(c); err != nil {
		return target, nil, err
	}
	var outputProject string
	if err := tx.QueryRow(ctx, `SELECT r.recurrence_id::text,r.owner_principal_id::text,r.scope_type,coalesce(r.scope_project_id::text,''),r.output_project_id::text,r.definition_revision
 FROM routine_runs r JOIN recurrences d ON d.tenant_id=r.tenant_id AND d.id=r.recurrence_id
 JOIN recurrence_definitions x ON x.tenant_id=r.tenant_id AND x.recurrence_id=r.recurrence_id
 WHERE r.tenant_id=$1 AND r.id=$2 AND d.revision=r.definition_revision AND x.owner_principal_id=r.owner_principal_id
 AND x.assignment=r.assignment AND x.scope_type=r.scope_type AND x.scope_project_id IS NOT DISTINCT FROM r.scope_project_id
 AND r.assignment->'allowed_actions' ? $4::text
 AND (r.agent_run_id=$3 OR EXISTS(SELECT 1 FROM routine_attempts a WHERE a.tenant_id=r.tenant_id AND a.run_id=r.id AND a.agent_run_id=$3))
 FOR NO KEY UPDATE OF r`, p.TenantID, b.RunID, b.AuthorRunID, b.Kind).Scan(&target.recurrence, &target.owner, &target.scopeKind, &target.scopeProject, &outputProject, &target.revision); err != nil {
		return target, nil, err
	}
	if outputProject != b.ProjectID {
		return target, nil, errors.New("evaluation project changed")
	}
	var actionID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM routine_actions WHERE tenant_id=$1 AND run_id=$2 AND id=$3 AND kind=$4 AND request_digest=$5 AND target_node_id=$6 AND target_revision=$7 AND state IN ('pending','needs_person','blocked') FOR NO KEY UPDATE`, b.TenantID, b.RunID, b.ActionID, b.Kind, b.PayloadDigest, b.TargetID, b.TargetRevision).Scan(&actionID); err != nil {
		return target, nil, err
	}
	var targetProject string
	if err := tx.QueryRow(ctx, `SELECT coalesce(project_id,id)::text FROM nodes WHERE tenant_id=$1 AND id=$2 AND updated_at=$3 AND deleted_at IS NULL FOR NO KEY UPDATE`, b.TenantID, b.TargetID, b.TargetUpdatedAt).Scan(&targetProject); err != nil {
		return target, nil, err
	}
	if targetProject != b.ProjectID {
		return target, nil, errors.New("evaluation target belongs to another project")
	}
	for _, actor := range []tenant.Principal{p, {ID: target.owner, TenantID: p.TenantID, Kind: tenant.Person}} {
		if err := authz.RequireTx(ctx, tx, actor, actionPermission(b.Kind), authz.Scope{ProjectID: b.ProjectID}); err != nil {
			return target, nil, err
		}
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.control", authz.Scope{ProjectID: b.ProjectID}); err != nil {
		return target, nil, err
	}
	sources, err := evaluationSources(ctx, tx, b.TenantID, target.scopeKind, target.scopeProject, target.owner)
	return target, sources, err
}

// PrepareEvaluationTx persists a request and links it to its action. The caller
// holds tenant -> tree before entry and must roll back on any error. No events,
// network call, model launch or budget reservation happens in this transaction.
// Executable evaluation remains off even when a reviewer route is available.
func PrepareEvaluationTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding, c Context, now time.Time) (EvaluationRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r := EvaluationRequest{}
	if now.IsZero() {
		return r, errors.New("evaluation clock is required")
	}
	target, sources, err := lockEvaluationAction(ctx, tx, p, b, c)
	if err != nil {
		return r, err
	}
	d, err := Evaluate(sources, c)
	if err != nil {
		return r, err
	}
	r = EvaluationRequest{Binding: b, ContextDigest: d.ContextDigest, PolicyDigest: d.PolicyDigest, Capability: EvaluationCapability, CreatedAt: now.UTC(), Deadline: now.UTC().Add(MaxEvaluationTime)}
	if err = tx.QueryRow(ctx, `SELECT p.harness,a.requested_model,coalesce(a.effective_model,''),a.model_evidence FROM agent_runs a JOIN model_profiles p ON p.tenant_id=a.tenant_id AND p.id=a.model_profile_id WHERE a.tenant_id=$1 AND a.id=$2`, b.TenantID, b.AuthorRunID).Scan(&r.Author.Harness, &r.Author.RequestedModel, &r.Author.EffectiveModel, &r.Author.Evidence); err != nil {
		return r, err
	}
	route, policy, routeErr := modelregistry.ResolveRoutineEvaluationFor(ctx, tx, p, modelregistry.WorkQuery{Role: "review-gate", ProjectID: b.ProjectID, PersonID: &target.owner}, r.Author, now)
	if routeErr != nil {
		return r, routeErr
	}
	r.FamilyPolicy = policy
	if route.Profile != nil && route.Account != nil {
		r.ProfileID, r.AccountID, r.Harness, r.Model, r.Family = route.Profile.ID, route.Account.ID, route.Profile.Harness, route.Profile.Model, route.Profile.Family
	}
	r.Digest = r.digest()
	previous, previousErr := loadEvaluationRecord(ctx, tx, b)
	if previousErr == nil {
		candidate := r
		candidate.CreatedAt, candidate.Deadline, candidate.Digest = previous.Request.CreatedAt, previous.Request.Deadline, previous.Request.Digest
		if previous.Request.Digest == candidate.digest() && now.Before(previous.Request.Deadline) {
			return previous.Request, nil
		}
	} else if !errors.Is(previousErr, pgx.ErrNoRows) {
		return r, previousErr
	}
	state, reason := "pending", ErrEvaluationBudgetUnavailable.Error()
	if d.DeterministicResult == Block {
		state, reason = "blocked", "deterministic_guardrail_block"
	} else if route.OwnerRequired || !r.valid() {
		reason = "evaluator_unavailable"
	}
	raw, err := json.Marshal(evaluationRecord{Request: r, Decision: d, WaitReason: reason})
	if err != nil || len(raw) > 262144 {
		return r, errors.New("evaluation request exceeds limits")
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO routine_guard_evaluations(tenant_id,recurrence_id,definition_revision,checkpoint,policy_digest,context_digest,hard_version,preset_version,result,required_evaluation,decision,scope_kind,scope_project_id,owner_principal_id,output_project_id)
	 VALUES($1,$2,$3,'action',$4,$5,$6,$7,$8,true,$9,$10,nullif($11,'')::uuid,$12,$13) RETURNING id::text`, b.TenantID, target.recurrence, target.revision, d.PolicyDigest, d.ContextDigest, HardVersion, PresetVersion, d.Result, raw, target.scopeKind, target.scopeProject, target.owner, b.ProjectID).Scan(&id)
	if err != nil {
		return r, err
	}
	_, err = tx.Exec(ctx, `UPDATE routine_actions SET evaluation_id=$3,policy_digest=$4,approval_id=NULL,state=$5,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, b.TenantID, b.ActionID, id, d.PolicyDigest, state)
	return r, err
}

func evaluationSources(ctx context.Context, tx pgx.Tx, tenantID, kind, project, owner string) ([]Source, error) {
	scopes := []Scope{{Kind: "tenant", ID: tenantID}}
	if kind == "project" {
		scopes = append(scopes, Scope{Kind: "project", ID: project})
	}
	if kind == "personal" {
		scopes = append(scopes, Scope{Kind: "user", ID: owner})
	}
	sources := []Source{}
	for _, scope := range scopes {
		s := Source{Scope: scope}
		var rules, loosenings []byte
		err := tx.QueryRow(ctx, `SELECT revision,rules,loosenings FROM routine_guard_policies WHERE scope_kind=$1 AND scope_id=$2 ORDER BY revision DESC LIMIT 1`, scope.Kind, scope.ID).Scan(&s.Revision, &rules, &loosenings)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(rules) > MaxPolicyBytes || len(loosenings) > 2*MaxPolicyBytes {
			return nil, fmt.Errorf("evaluation policy exceeds limits")
		}
		if err = json.Unmarshal(rules, &s.Rules); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(loosenings, &s.Loosenings); err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}
	return sources, nil
}

func loadEvaluationRecord(ctx context.Context, tx pgx.Tx, b ActionBinding) (evaluationRecord, error) {
	record := evaluationRecord{}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT e.decision FROM routine_actions a JOIN routine_guard_evaluations e ON e.tenant_id=a.tenant_id AND e.id=a.evaluation_id WHERE a.tenant_id=$1 AND a.run_id=$2 AND a.id=$3 AND e.checkpoint='action' AND e.required_evaluation`, b.TenantID, b.RunID, b.ActionID).Scan(&raw)
	if err != nil {
		return record, err
	}
	if len(raw) > 262144 {
		return record, errors.New("stored evaluation exceeds limits")
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	if record.Request.Digest != record.Request.digest() {
		return record, errors.New("stored evaluation request changed")
	}
	return record, nil
}

// evaluationEvidence binds the response to the reserved attempt in this run.
// It does not trust model identity, completion or allowance in model output.
func evaluationEvidence(ctx context.Context, tx pgx.Tx, r EvaluationRequest, runID string) (string, modelregistry.VerifiedModel, EvaluationGrant, error) {
	model, grant := modelregistry.VerifiedModel{}, EvaluationGrant{}
	status := ""
	if !r.valid() || !uuid.MatchString(runID) {
		return status, model, grant, errEvaluationUnavailable
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT a.status,p.harness,a.requested_model,coalesce(a.effective_model,''),a.model_evidence,attempt.process_receipt
 FROM routine_attempts attempt JOIN agent_runs a ON a.tenant_id=attempt.tenant_id AND a.id=attempt.agent_run_id
 JOIN model_profiles p ON p.tenant_id=a.tenant_id AND p.id=a.model_profile_id
 WHERE attempt.tenant_id=$1 AND attempt.run_id=$2 AND attempt.agent_run_id=$3 AND attempt.role='evaluation' AND attempt.assignment_digest=$4
 AND a.model_profile_id=$5 AND coalesce(a.account_id,a.requested_account_id)=$6
 AND EXISTS(SELECT 1 FROM work_orders w WHERE w.tenant_id=a.tenant_id AND w.node_id=a.work_order_id AND w.kind='review')`, r.Binding.TenantID, r.Binding.RunID, runID, r.Digest, r.ProfileID, r.AccountID).Scan(&status, &model.Harness, &model.RequestedModel, &model.EffectiveModel, &model.Evidence, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, model, grant, errEvaluationUnavailable
	}
	if err != nil {
		return status, model, grant, err
	}
	if len(raw) > 65536 {
		return status, model, grant, errEvaluationUnavailable
	}
	var receipt struct {
		Grant EvaluationGrant `json:"evaluation_grant"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return status, model, grant, errEvaluationUnavailable
	}
	if !receipt.Grant.valid(r) {
		return status, model, grant, errEvaluationUnavailable
	}
	return status, model, receipt.Grant, nil
}

func currentEvaluationPolicy(ctx context.Context, tx pgx.Tx, r EvaluationRequest) (reviewgate.FamilyPolicy, error) {
	if !r.valid() {
		return reviewgate.FamilyPolicy{}, errEvaluationUnavailable
	}
	settings, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, &r.Binding.ProjectID)
	if err != nil {
		return reviewgate.FamilyPolicy{}, err
	}
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT enabled AND harness=$2 AND model=$3 AND family=$4 FROM model_profiles WHERE id=$1`, r.ProfileID, r.Harness, r.Model, r.Family).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings.Effective, errEvaluationUnavailable
	}
	if err != nil {
		return settings.Effective, err
	}
	if !enabled {
		return settings.Effective, errEvaluationUnavailable
	}
	return settings.Effective, nil
}

// RecordEvaluationTx stores effective model evidence and the explicit result
// before creating a person hold. Unverified/missing/stale results stay pending;
// a block stays blocked. Even valid evidence grants no execution before S09/S10.
// The caller holds tenant/tree, supplies only broker-validated typed context,
// rolls back errors, and flushes pending events LAST after all other writes.
func RecordEvaluationTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding, c Context, evaluatorRunID string, output []byte, now time.Time, pending *[]events.Change) (Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(output) > MaxEvaluationOutput || pending == nil {
		return Decision{}, errors.New("invalid evaluation result input")
	}
	target, sources, err := lockEvaluationAction(ctx, tx, p, b, c)
	if err != nil {
		return Decision{}, err
	}
	record, err := loadEvaluationRecord(ctx, tx, b)
	if err != nil {
		return Decision{}, err
	}
	d, err := Evaluate(sources, c)
	if err != nil {
		return d, err
	}
	verdict, verdictErr := ParseEvaluationResult(output)
	status, evidence, _, evidenceErr := evaluationEvidence(ctx, tx, record.Request, evaluatorRunID)
	policy, policyErr := currentEvaluationPolicy(ctx, tx, record.Request)
	for _, lookupErr := range []error{evidenceErr, policyErr} {
		if lookupErr != nil && !errors.Is(lookupErr, errEvaluationUnavailable) {
			return d, lookupErr
		}
	}
	reason := "evaluation_result_unavailable"
	verified := false
	if verdictErr == nil && evidenceErr == nil && policyErr == nil {
		var verificationErr error
		d, verificationErr = VerifyEvaluation(sources, c, b, record.Request, status, evidence, verdict, policy, now)
		verified = verificationErr == nil
		if !verified {
			reason = "evaluation_stale_or_unverified"
		}
	}
	state := "pending"
	if d.Result == Block {
		state, reason = "blocked", "guardrail_block"
	}
	var approvalID *string
	if verified {
		record.EvaluatorRunID, record.Evidence, record.Result = evaluatorRunID, &evidence, &verdict
		if d.Result != Block {
			reason = ErrEvaluationBudgetUnavailable.Error()
		}
		if d.Result == NeedsPerson {
			a, err := approvals.RequestRoutineHoldTx(ctx, tx, p, b.TargetID, actionPermission(b.Kind), record.Request.Digest, record.Request.Deadline, now, pending)
			if err != nil {
				return d, err
			}
			approvalID, state, reason = &a.ID, "needs_person", "person_decision_required"
		}
	}
	// Rejected replacement output never inherits an earlier verified receipt.
	if !verified {
		record.EvaluatorRunID, record.Evidence, record.Result = "", nil, nil
	}
	d.evaluationVerified, d.evaluationBinding = "", ""
	if d.Result == Allow {
		d.Result = NeedsPerson
	}
	record.Decision, record.WaitReason = d, reason
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 262144 {
		return d, errors.New("evaluation receipt exceeds limits")
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO routine_guard_evaluations(tenant_id,recurrence_id,definition_revision,checkpoint,policy_digest,context_digest,hard_version,preset_version,result,required_evaluation,decision,scope_kind,scope_project_id,owner_principal_id,output_project_id)
 VALUES($1,$2,$3,'action',$4,$5,$6,$7,$8,true,$9,$10,nullif($11,'')::uuid,$12,$13) RETURNING id::text`, b.TenantID, target.recurrence, target.revision, d.PolicyDigest, d.ContextDigest, HardVersion, PresetVersion, d.Result, raw, target.scopeKind, target.scopeProject, target.owner, b.ProjectID).Scan(&id)
	if err != nil {
		return d, err
	}
	_, err = tx.Exec(ctx, `UPDATE routine_actions SET evaluation_id=$3,policy_digest=$4,approval_id=$5,state=$6,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, b.TenantID, b.ActionID, id, d.PolicyDigest, approvalID, state)
	return d, err
}

// CheckPersonHoldTx re-verifies evidence against the current action/policy and
// current person's authority. true resolves only the person hold; it is never
// launch, budget, mutation, merge or deployment authority.
func CheckPersonHoldTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b ActionBinding, c Context, now time.Time) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, sources, err := lockEvaluationAction(ctx, tx, p, b, c)
	if err != nil {
		return false, err
	}
	record, err := loadEvaluationRecord(ctx, tx, b)
	if err != nil {
		return false, err
	}
	if record.Result == nil || record.Evidence == nil {
		return false, nil
	}
	status, evidence, _, err := evaluationEvidence(ctx, tx, record.Request, record.EvaluatorRunID)
	if err != nil {
		return false, err
	}
	policy, err := currentEvaluationPolicy(ctx, tx, record.Request)
	if err != nil {
		return false, err
	}
	d, err := VerifyEvaluation(sources, c, b, record.Request, status, evidence, *record.Result, policy, now)
	if err != nil || d.Result != NeedsPerson {
		return false, err
	}
	var approvalID *string
	if err = tx.QueryRow(ctx, `SELECT approval_id::text FROM routine_actions WHERE tenant_id=$1 AND id=$2`, b.TenantID, b.ActionID).Scan(&approvalID); err != nil {
		return false, err
	}
	if approvalID == nil {
		return false, nil
	}
	return approvals.LiveRoutineHoldTx(ctx, tx, p, *approvalID, b.TargetID, actionPermission(b.Kind), record.Request.Digest, now)
}
