// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/jackc/pgx/v5"
)

// Caller holds the tenant fence, before the event counter. Revision changes
// even on A -> B -> A, and on ticket/project moves. Holds survive a new head;
// the nightly fix exception must be explicitly designated for the new head.
func syncAdmissionBindingTx(ctx context.Context, tx pgx.Tx, tid string, o Observation) error {
	if o.PR == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO delivery_admission_bindings(tenant_id,repository,pull_request,project_id,ticket_node_id,target_branch,base_sha,head_sha,scope)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(tenant_id,repository,pull_request) DO UPDATE SET
 project_id=EXCLUDED.project_id,ticket_node_id=EXCLUDED.ticket_node_id,target_branch=EXCLUDED.target_branch,base_sha=EXCLUDED.base_sha,head_sha=EXCLUDED.head_sha,scope=EXCLUDED.scope,
 revision=delivery_admission_bindings.revision+1,control_revision=delivery_admission_bindings.control_revision+1,fix=false
 WHERE (delivery_admission_bindings.project_id,delivery_admission_bindings.ticket_node_id,delivery_admission_bindings.target_branch,delivery_admission_bindings.base_sha,delivery_admission_bindings.head_sha,delivery_admission_bindings.scope)
 IS DISTINCT FROM (EXCLUDED.project_id,EXCLUDED.ticket_node_id,EXCLUDED.target_branch,EXCLUDED.base_sha,EXCLUDED.head_sha,EXCLUDED.scope)`, tid, o.Repository, *o.PR, o.Project, o.Ticket, o.Target, o.Base, o.Head, o.Base+".."+o.Head)
	return err
}

type AdmissionSnapshot struct {
	Enforcement     string             `json:"enforcement"`
	Effective       bool               `json:"effective"`
	BindingRevision int64              `json:"binding_revision"`
	Control         AdmissionControl   `json:"control"`
	Policy          AdmissionPolicy    `json:"policy"`
	Review          *AdmissionApproval `json:"review"`
	Visual          *AdmissionApproval `json:"visual"`
	Decision        AdmissionDecision  `json:"decision"`
}

func admissionPolicyTx(ctx context.Context, tx pgx.Tx, project string) (AdmissionPolicy, error) {
	out := AdmissionPolicy{BlockOnNightlyRed: true, NightlyState: "unknown", Holds: []AdmissionHold{}}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,block_on_nightly_red,nightly_state,holds FROM delivery_admission_settings WHERE project_id=$1`, project).Scan(&out.Revision, &out.BlockOnNightlyRed, &out.NightlyState, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &out.Holds)
	}
	return out, err
}

// AdmissionTx is the P3 consumer boundary. The caller holds the tenant fence
// and has authorized this current delivery item. No cached decision is stored:
// every read joins current binding, approvals, holds, policy and CI facts.
func AdmissionTx(ctx context.Context, tx pgx.Tx, item Item) (AdmissionSnapshot, error) {
	out := AdmissionSnapshot{}
	o := item.Observation
	if item.Project == nil || item.Ticket == nil || item.PR == nil {
		return out, fail(409, "admission requires a linked project and ticket")
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN delivery_metric_sources s ON s.tenant_id=n.tenant_id AND s.project_id=n.project_id AND s.repository=$3
 WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL)`, *item.Ticket, *item.Project, item.Repository).Scan(&current); err != nil {
		return out, err
	}
	if !current {
		return out, fail(409, "admission ticket or repository binding changed")
	}
	out.Control.Binding = reviewgate.RunBinding{Ticket: *item.Ticket, Repository: item.Repository, PR: *item.PR, Target: o.Target, Base: o.Base, Head: item.Head, Scope: o.Base + ".." + item.Head}
	var raw []byte
	var target, base, head, scope, project, ticket string
	err := tx.QueryRow(ctx, `SELECT revision,control_revision,holds,fix,target_branch,base_sha,head_sha,scope,coalesce(project_id::text,''),coalesce(ticket_node_id::text,'') FROM delivery_admission_bindings WHERE repository=$1 AND pull_request=$2`, item.Repository, *item.PR).
		Scan(&out.BindingRevision, &out.Control.Revision, &raw, &out.Control.Fix, &target, &base, &head, &scope, &project, &ticket)
	if err != nil {
		return out, err
	}
	b := out.Control.Binding
	if target != b.Target || base != b.Base || head != b.Head || scope != b.Scope || project != *item.Project || ticket != b.Ticket {
		return out, fail(409, "admission binding changed")
	}
	if err = json.Unmarshal(raw, &out.Control.Holds); err != nil {
		return out, err
	}
	out.Policy, err = admissionPolicyTx(ctx, tx, *item.Project)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.kind,r.ticket_node_id::text,r.repository,r.pull_request,r.target_branch,r.base_sha,r.head_sha,r.scope,r.admission_revision,r.author_family,r.reviewer_family,r.harness,r.model,r.reasoning_effort,r.verdict,r.visual_impact,r.reason
 FROM delivery_admission_approvals a JOIN review_runs r ON r.tenant_id=a.tenant_id AND (r.id=a.review_run_id OR r.id=a.visual_run_id)
 WHERE a.repository=$1 AND a.pull_request=$2 AND a.binding_revision=$3 AND r.admission_revision=$3 AND r.submitted_at IS NOT NULL AND r.credential_digest IS NOT NULL AND r.credential_key_id IS NOT NULL
 ORDER BY r.kind LIMIT 2`, item.Repository, *item.PR, out.BindingRevision)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		a := new(AdmissionApproval)
		err = rows.Scan(&a.RunID, &a.Kind, &a.Binding.Ticket, &a.Binding.Repository, &a.Binding.PR, &a.Binding.Target, &a.Binding.Base, &a.Binding.Head, &a.Binding.Scope, &a.Revision, &a.AuthorFamily, &a.ReviewerFamily, &a.Harness, &a.Model, &a.Effort, &a.Verdict, &a.VisualImpact, &a.Reason)
		if err != nil {
			rows.Close()
			return out, err
		}
		if a.Kind == "review" {
			out.Review = a
		} else if a.Kind == "visual" {
			out.Visual = a
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	diff := AdmissionDiff{}
	if o.AdmissionDiff != nil {
		diff = *o.AdmissionDiff
	}
	holds := append([]AdmissionHold{}, out.Control.Holds...)
	if o.HoldReason != nil {
		holds = append(holds, AdmissionHold{Kind: "manual", Reason: *o.HoldReason})
	}
	// CI requirements are live policy, not the observation's saved settings.
	o.Settings, err = settingsTx(ctx, tx, item.Project)
	if err != nil {
		return out, err
	}
	out.Decision = EvaluateAdmission(AdmissionInput{Binding: b, Revision: out.BindingRevision, Open: o.Open && !o.Merged, CIGreen: admissionCIGreen(o), ScopeVerified: diff.MergeBase == b.Base && reviewgate.ValidSHA(diff.MergeBase), Review: out.Review, Visual: out.Visual, Diff: diff, Holds: holds, Policy: out.Policy, Fix: out.Control.Fix})
	out.Enforcement = "pending"
	err = tx.QueryRow(ctx, `SELECT enforcement FROM delivery_admission_queue WHERE repository=$1 AND pull_request=$2 AND head_sha=$3`, item.Repository, *item.PR, item.Head).Scan(&out.Enforcement)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	out.Effective = !out.Decision.Admissible && out.Enforcement == "removed"
	return out, nil
}
func admissionCIGreen(o Observation) bool {
	// Admission can never depend on itself. Even a misconfigured prerequisite
	// list containing only admission cannot bootstrap a green result.
	if o.Settings.RequiredChecks == nil {
		return false
	}
	required := []string{}
	for _, name := range *o.Settings.RequiredChecks {
		if name != AdmissionContext {
			required = append(required, name)
		}
	}
	o.Settings.RequiredChecks = &required
	return allSuccess(o)
}
