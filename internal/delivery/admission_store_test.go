// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestAdmissionStoreAuthenticatedApprovalsAndInvalidation(t *testing.T) {
	// Risk: a caller manufactures approvals, old verdicts return after retarget
	// or new heads, visual grants are bypassed, or holds lose precedence. Use
	// real P1 claim/submit functions and the production admission API/store.
	f := newReviewFixture(t)
	ctx := t.Context()
	p := f.pull()
	p.Target = "main"
	p.AdmissionDiff = &AdmissionDiff{MergeBase: p.Base, Paths: []string{"docs/queue.md"}, Complete: true}
	var reviewer tenant.Principal
	reviewer = tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.read", "reviews.visual_approve"}, KeyCreatorID: f.person.ID}
	var authorSession, reviewerSession, visualRole string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Independent reviewer') RETURNING id::text`, reviewer.TenantID).Scan(&reviewer.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'Admission fixture','admission-fixture',decode(repeat('00',32),'hex'),$3,$4) RETURNING id::text`, reviewer.TenantID, reviewer.ID, reviewer.Scopes, f.person.ID).Scan(&reviewer.KeyID); err != nil {
			return err
		}
		reviewer.AuthKeyID = reviewer.KeyID
		for _, s := range []struct {
			principal, harness, model string
			id                        *string
		}{{f.agent.ID, "codex", "gpt-6.1-sol", &authorSession}, {reviewer.ID, "claude", "claude-opus-4-6", &reviewerSession}} {
			digest := sha256.Sum256([]byte(s.principal))
			if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,model,reasoning_effort,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,$6,'xhigh','fixture','unmanaged','worker','ship',$7,$7) RETURNING id::text`, f.person.TenantID, f.project, s.principal, f.ticket, s.harness, s.model, digest[:]).Scan(s.id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,$3,'ci.yml','nightly.yml',now())`, f.person.TenantID, f.project, f.m.config.Repository)
		return err
	})
	dbtest.BindRole(t, f.d, reviewer.TenantID, reviewer.ID, "viewer")
	observe := func() {
		t.Helper()
		f.tx(t, func(tx pgx.Tx) error {
			if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
				return err
			}
			o, err := f.m.observationTx(ctx, tx, p, f.at)
			if err != nil {
				return err
			}
			for _, name := range *defaults().RequiredChecks {
				o.Checks = append(o.Checks, Check{Name: name, Status: "completed", Conclusion: "success"})
			}
			_, _, err = saveTx(ctx, tx, f.person.TenantID, o)
			return err
		})
	}
	observe()
	item := f.item(t, 7)
	endpoint := "/api/delivery/" + item.ID + "/admission"
	settings := "/api/projects/" + f.project + "/delivery-admission"
	var policy AdmissionPolicy
	f.call(t, f.person, "GET", settings, nil, 200, &policy)
	if policy.NightlyState != "unknown" || !policy.BlockOnNightlyRed {
		t.Fatal("unsafe default", policy)
	}
	policy.NightlyState = "green"
	f.call(t, f.person, "PUT", settings, policy, 200, &policy)
	snapshot := func(reason string) AdmissionSnapshot {
		t.Helper()
		var out AdmissionSnapshot
		f.call(t, f.person, "GET", endpoint, nil, 200, &out)
		if out.Decision.Reason != reason {
			t.Fatalf("got %+v want %s", out.Decision, reason)
		}
		return out
	}
	snapshot("review_missing_or_stale")
	create := func(kind string) reviewgate.Run {
		t.Helper()
		var run reviewgate.Run
		f.tx(t, func(tx pgx.Tx) error {
			if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
				return err
			}
			var err error
			run, err = reviewgate.CreateRunTx(ctx, tx, f.person, f.ticket, reviewgate.RunCreate{Repository: f.m.config.Repository, PR: 7, Target: p.Target, Base: p.AdmissionDiff.MergeBase, Head: p.Head, Scope: p.AdmissionDiff.MergeBase + ".." + p.Head, AuthorSession: authorSession, ReviewerSession: reviewerSession, Kind: kind}, f.at)
			return err
		})
		return run
	}
	submit := func(run reviewgate.Run, verdict, impact string) {
		t.Helper()
		f.tx(t, func(tx pgx.Tx) error {
			if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
				return err
			}
			capability, _, err := reviewgate.ClaimRunTx(ctx, tx, reviewer, run.ID, f.at)
			if err != nil {
				return err
			}
			_, err = reviewgate.SubmitRunTx(ctx, tx, reviewer, run.ID, capability, reviewgate.RunSubmit{Binding: run.RunBinding, Verdict: verdict, VisualImpact: impact, Reason: "Exact scope inspected"}, f.at)
			return err
		})
	}
	run := create("review")
	// Ordinary worker and creator cannot claim even a correct run binding.
	f.tx(t, func(tx pgx.Tx) error {
		_, _, err := reviewgate.ClaimRunTx(ctx, tx, f.agent, run.ID, f.at)
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("worker rejection: %v", err)
		}
		return nil
	})
	submit(run, "ok", "no")
	approved := snapshot("admissible")
	if approved.Review == nil || approved.Review.RunID != run.ID || approved.Review.Model != "claude-opus-4-6" || approved.Review.ReviewerFamily != "anthropic" {
		t.Fatal("lost P1 provenance", approved)
	}
	// All manual/release/wave holds lift without losing the approval.
	for _, kind := range []string{"manual", "release_cut", "wave"} {
		control := approved.Control
		control.Holds = []AdmissionHold{{Kind: kind, Reason: "Visible " + kind}}
		f.call(t, f.person, "PUT", endpoint, control, 200, &approved)
		if approved.Decision.Reason != "hold_"+kind || approved.Decision.HoldReason != "Visible "+kind {
			t.Fatal(approved.Decision)
		}
		control = approved.Control
		control.Holds = []AdmissionHold{}
		f.call(t, f.person, "PUT", endpoint, control, 200, &approved)
		if !approved.Decision.Admissible {
			t.Fatal("lifting hold lost approval", approved.Decision)
		}
	}
	staleControl := approved.Control
	policy.NightlyState = "red"
	f.call(t, f.person, "PUT", settings, policy, 200, &policy)
	approved = snapshot("nightly_red")
	control := approved.Control
	control.Fix = true
	f.call(t, f.person, "PUT", endpoint, control, 200, &approved)
	if !approved.Decision.Admissible {
		t.Fatal("fix exception blocked", approved.Decision)
	}
	// Projection replay of an old target must preserve authoritative admission.
	original := f.item(t, 7).Observation
	f.tx(t, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		historical := original
		historical.Target = "release"
		if _, _, err := saveProjectionTx(ctx, tx, f.person.TenantID, historical, false); err != nil {
			return err
		}
		_, _, err := saveProjectionTx(ctx, tx, f.person.TenantID, original, false)
		return err
	})
	replayed := snapshot("admissible")
	if replayed.BindingRevision != approved.BindingRevision || !replayed.Control.Fix {
		t.Fatal("projection replay invalidated authority", replayed)
	}
	policy.Holds = []AdmissionHold{{Kind: "release_cut", Reason: "Release cut takes precedence"}}
	f.call(t, f.person, "PUT", settings, policy, 200, &policy)
	snapshot("hold_release_cut")
	policy.NightlyState = "green"
	policy.Holds = []AdmissionHold{}
	f.call(t, f.person, "PUT", settings, policy, 200, &policy)
	// A reviewer's yes requires a visual run even on docs; missing grant fails.
	submit(create("review"), "ok", "yes")
	snapshot("visual_missing_or_stale")
	visual := create("visual")
	f.tx(t, func(tx pgx.Tx) error {
		_, _, err := reviewgate.ClaimRunTx(ctx, tx, reviewer, visual.ID, f.at)
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("visual grant rejection: %v", err)
		}
		return nil
	})
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'admission_visual','Explicit visual grant') RETURNING id::text`, reviewer.TenantID).Scan(&visualRole); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'reviews.visual_approve')`, reviewer.TenantID, visualRole); err != nil {
			return err
		}
		for _, principal := range []string{f.person.ID, reviewer.ID} {
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, reviewer.TenantID, principal, visualRole, f.project); err != nil {
				return err
			}
		}
		return nil
	})
	submit(visual, "ok", "yes")
	approved = snapshot("admissible")
	// Rejection replaces an older ok; lifting holds cannot revive that verdict.
	submit(create("review"), "changes", "no")
	snapshot("review_changes_requested")
	submit(create("review"), "ok", "no")
	approved = snapshot("admissible")
	oldHead := p.Head
	pending := create("review")
	for _, mutation := range []string{"head", "retarget", "scope"} {
		switch mutation {
		case "head":
			p.Head = oldHead[:39] + "c"
		case "retarget":
			p.Target = "release"
		case "scope":
			p.AdmissionDiff.MergeBase = oldHead[:39] + "d"
		}
		observe()
		want := "review_missing_or_stale"
		if mutation == "retarget" {
			want = "target_not_main"
		}
		snapshot(want)
		p.Head = oldHead
		p.Target = "main"
		p.AdmissionDiff.MergeBase = p.Base
		observe()
		approved = snapshot("review_missing_or_stale")
		if approved.Control.Fix {
			t.Fatal("new binding retained nightly fix exception")
		}
		f.call(t, f.person, "PUT", endpoint, staleControl, 409, nil)
		f.tx(t, func(tx pgx.Tx) error {
			_, _, err := reviewgate.ClaimRunTx(ctx, tx, reviewer, pending.ID, f.at)
			if !errors.Is(err, reviewgate.ErrRunBinding) {
				t.Fatalf("old run revived: %v", err)
			}
			return nil
		})
		submit(create("review"), "ok", "no")
		snapshot("admissible")
	}
	// A global/project reader cannot become an approval producer via these APIs.
	f.call(t, f.agent, "PUT", endpoint, approved.Control, 403, nil)
	f.call(t, f.foreign, "GET", endpoint, nil, 404, nil)
	f.call(t, f.agent, "PUT", settings, policy, 403, nil)
	// Revocation after the displayed snapshot is checked again in the write.
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "viewer")
	f.call(t, f.person, "PUT", endpoint, approved.Control, 403, nil)
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "admin")
	// Bounded malformed controls never mutate the valid stored state.
	bad := approved.Control
	bad.Holds = []AdmissionHold{{Kind: "manual", Reason: ""}}
	f.call(t, f.person, "PUT", endpoint, bad, 400, nil)
	f.tx(t, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM delivery_admission_approvals`).Scan(&count)
		if err == nil && count < 4 {
			t.Fatal("approval history was overwritten")
		}
		return err
	})
}
