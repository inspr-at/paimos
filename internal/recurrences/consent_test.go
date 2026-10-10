// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"net/http/httptest"
)

func consentFixture(t *testing.T) (*fixture, Input, modelregistry.Qualification, ExecutionRuntimeReader) {
	t.Helper()
	f := setup(t)
	f.p.BrowserSession = true
	var kind string
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL`).Scan(&kind); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,$2,$3,'{}',$3)`, f.p.TenantID, f.project, f.p.ID)
		return err
	})
	q := modelregistry.Qualification{ID: "10000000-0000-4000-8000-000000000084", ProjectID: f.project, Runtime: ExecutionRuntime{ServerDigest: strings.Repeat("a", 64), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), Capabilities: []string{"routine_native_coding_v1"}, HostMappingDigest: strings.Repeat("d", 64), BudgetModes: []string{"off"}}, CoordinatorAcceptance: strings.Repeat("e", 64), OPSAttestation: strings.Repeat("f", 64)}
	f.tx(func(tx pgx.Tx) error {
		var err error
		q.OwnerPersonID, q.PolicyDigest, err = modelregistry.QualificationPolicyTx(t.Context(), tx, f.p.TenantID, f.project)
		return err
	})
	reader := func(ctx context.Context, tx pgx.Tx, _ string) (ExecutionRuntime, error) {
		out := q.Runtime
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt)
		return out, err
	}
	f.install(New(f.d.App).WithExecutionRuntime(reader))
	in := f.input()
	in.QueueEach = true
	in.Definition = &Definition{Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.p.ID, Assignment: &Assignment{Goal: "Synthetic consent boundary", Role: "build", WorkKindID: kind, AllowedActions: []string{"work.create"}, RuntimeRequirements: RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: DefinitionBudget{Mode: "off"}}}
	return f, in, q, reader
}

func enableConsentProject(t *testing.T, f *fixture, q modelregistry.Qualification, reader ExecutionRuntimeReader) {
	t.Helper()
	f.tx(func(tx pgx.Tx) error { return modelregistry.RecordQualificationTx(t.Context(), tx, f.p, q) })
	revision, enabled := int64(0), true
	f.tx(func(tx pgx.Tx) error {
		out, err := modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.p, f.project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &revision, AutomaticLaunchEnabled: &enabled, QualificationID: &q.ID}, reader)
		if err == nil && !out.AutomaticLaunchEnabled {
			t.Fatalf("accepted synthetic qualification did not enable policy: %+v", out)
		}
		return err
	})
}

// Risk: assignment/queue bypassing person consent, private identity borrowing,
// and changed policy/qualification creating new grants. Existing recurrence
// tests cover receipt idempotency; this exercises the added execution boundary.
func TestRoutineConsentBindsQualificationAndScopedIdentity(t *testing.T) {
	f, in, q, reader := consentFixture(t)
	r := f.create(in)
	var draft struct {
		Policy ExecutionPolicy `json:"execution_policy"`
	}
	if err := json.Unmarshal(f.call(f.p, "POST", "/api/recurrences/preview", in, 200), &draft); err != nil {
		t.Fatal(err)
	}
	if r.ExecutionPolicy == nil || r.ExecutionPolicy.WaitReason != "automatic_launch_disabled" || draft.Policy.WaitReason != r.ExecutionPolicy.WaitReason || !r.Paused {
		t.Fatalf("preview/save diverged: %+v %+v", r, draft)
	}
	enableConsentProject(t, f, q, reader)
	path := "/api/recurrences/" + r.ID
	f.call(f.p, "POST", path+"/resume", map[string]any{"expected_revision": r.Revision}, 200)
	r = f.get(r.ID)
	claim := func(item Recurrence, want string, principal string) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			id, err := RequireExecutionConsentTx(t.Context(), tx, f.p.TenantID, item.ID, item.Revision, reader)
			if err == nil && id != principal {
				t.Fatalf("borrowed identity %q, want %q", id, principal)
			}
			return err
		})
		if want == "" {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		var failure *workorders.Error
		if !errors.As(err, &failure) || failure.Status != 409 || failure.Message != want {
			t.Fatalf("claim error %v, want 409 %s", err, want)
		}
	}
	claim(r, "execution_consent_required", "")
	consent := func(item Recurrence, id *string, status int) Recurrence {
		t.Helper()
		raw := f.call(f.p, "PUT", "/api/recurrences/"+item.ID+"/execution-consent", map[string]any{"expected_revision": item.Revision, "execute_consent": true, "execution_principal_id": id}, status)
		var out Recurrence
		if status == 200 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	old := r
	r = consent(r, nil, 200)
	if r.ExecutionPolicy == nil || !r.ExecutionPolicy.Eligible {
		t.Fatalf("consent not effective: %+v", r)
	}
	claim(r, "", f.p.ID)
	consent(old, nil, 409)
	var preview struct {
		Policy ExecutionPolicy `json:"execution_policy"`
	}
	json.Unmarshal(f.call(f.p, "GET", path+"/preview", nil, 200), &preview)
	if !preview.Policy.Eligible || preview.Policy.ConsentRevision != r.Revision {
		t.Fatalf("preview did not use claim policy: %+v", preview)
	}
	// A project routine needs an explicit existing keyless service identity.
	in.Definition.Scope = DefinitionScope{Kind: "project", ProjectID: f.project}
	shared := f.create(in)
	f.call(f.p, "POST", "/api/recurrences/"+shared.ID+"/resume", map[string]any{"expected_revision": shared.Revision}, 200)
	shared = f.get(shared.ID)
	consent(shared, nil, 409)
	var service, role string
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent','Synthetic routine service',true) RETURNING id::text`, f.p.TenantID).Scan(&service); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'synthetic_routine','Synthetic routine') RETURNING id::text`, f.p.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.p.TenantID, role, executionPermissions(in.Definition.Assignment)); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.p.TenantID, service, role, f.project)
		return err
	})
	shared = consent(shared, &service, 200)
	claim(shared, "", service)
	// An extra role permission invalidates the restricted identity immediately.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'keys.manage')`, f.p.TenantID, role)
		return err
	})
	claim(shared, "execution_identity_unavailable", "")
	// Exact runtime pins are independent facts, never copied from evidence.
	for _, field := range []string{"server", "daemon", "capability", "host", "budget"} {
		t.Run(field, func(t *testing.T) {
			changed := q.Runtime
			switch field {
			case "server":
				changed.ServerDigest = strings.Repeat("0", 64)
			case "daemon":
				changed.DaemonDigest = strings.Repeat("0", 64)
			case "capability":
				changed.CapabilityDigest = strings.Repeat("0", 64)
			case "host":
				changed.HostMappingDigest = strings.Repeat("0", 64)
			case "budget":
				changed.BudgetModes = []string{"tokens"}
			}
			original := reader
			reader = func(ctx context.Context, tx pgx.Tx, _ string) (ExecutionRuntime, error) {
				err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&changed.ObservedAt)
				return changed, err
			}
			claim(r, "qualification_runtime_changed", "")
			reader = original
		})
	}
	// A coding qualification cannot approve browser or capped-budget work.
	browser := f.input()
	browser.Definition = &Definition{Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.p.ID, Assignment: &Assignment{Goal: "Synthetic browser", Role: "build", WorkKindID: in.Definition.Assignment.WorkKindID, AllowedActions: []string{}, RuntimeRequirements: RuntimeRequirements{NeedsNativeHost: true, NeedsBrowser: true, RuntimeClass: "native_browser"}, Budget: DefinitionBudget{Mode: "off"}}}
	browserRoutine := f.create(browser)
	if browserRoutine.ExecutionPolicy.WaitReason != "capability_unqualified" {
		t.Fatalf("coding qualification admitted browser: %+v", browserRoutine.ExecutionPolicy)
	}
	tokens := int64(100)
	browser.Definition.Assignment.RuntimeRequirements = RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}
	browser.Definition.Assignment.Budget = DefinitionBudget{Mode: "tokens", TokenCeiling: &tokens}
	budgetRoutine := f.create(browser)
	if budgetRoutine.ExecutionPolicy.WaitReason != "budget_mode_unqualified" {
		t.Fatalf("unsupported budget admitted: %+v", budgetRoutine.ExecutionPolicy)
	}
	originalReader := reader
	for _, offset := range []string{"-interval '1 hour'", "+interval '1 hour'"} {
		reader = func(ctx context.Context, tx pgx.Tx, _ string) (ExecutionRuntime, error) {
			out := q.Runtime
			err := tx.QueryRow(ctx, `SELECT clock_timestamp()`+offset).Scan(&out.ObservedAt)
			return out, err
		}
		claim(r, "runtime_unavailable", "")
	}
	reader = originalReader
	// Any definition edit discards consent without touching the personal actor.
	f.call(f.p, "PUT", path, struct {
		Input
		Revision int64 `json:"expected_revision"`
	}{r.Input, r.Revision}, 200)
	r = f.get(r.ID)
	claim(r, "execution_consent_required", "")
	revision, off := int64(1), false
	f.tx(func(tx pgx.Tx) error {
		_, err := modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.p, f.project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &revision, AutomaticLaunchEnabled: &off}, reader)
		return err
	})
	claim(r, "automatic_launch_disabled", "")
	// A ticket-only definition remains independent of execution settings.
	ticket := f.input()
	ticket.Definition = &Definition{Scope: DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.p.ID}
	simple := f.create(ticket)
	if simple.ExecutionPolicy == nil || !simple.ExecutionPolicy.Eligible || simple.ExecutionPolicy.ConsentRequired {
		t.Fatalf("ticket-only requires consent: %+v", simple)
	}
}

// Risk: owner revocation after preview must win over the final consent write.
// A real tenant lock establishes overlap; the deadline only guards a hang.
func TestRoutineConsentRechecksOwnerBehindAccessFence(t *testing.T) {
	f, in, q, reader := consentFixture(t)
	enableConsentProject(t, f, q, reader)
	r := f.create(in)
	// Retain another owner so this revocation obeys the last-owner guard.
	var retainedOwner string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Synthetic retained owner') RETURNING id::text`, f.p.TenantID).Scan(&retainedOwner)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, retainedOwner, "owner")
	ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 15*time.Second)
	defer cancel()
	holder, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(t.Context())
	if _, err = holder.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	req := httptest.NewRequest("PUT", "/api/recurrences/"+r.ID+"/execution-consent", strings.NewReader(`{"expected_revision":1,"execute_consent":true}`))
	req = req.WithContext(tenant.WithPrincipal(ctx, f.p))
	go func() { out := httptest.NewRecorder(); f.handler.ServeHTTP(out, req); done <- out }()
	dbtest.WaitForLock(t, ctx, f.d, holder.Conn().PgConn().PID(), "transactionid")
	if _, err = holder.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1 AND scope_type='workspace'`, f.p.ID); err != nil {
		t.Fatal(err)
	}
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The actual consent write was already waiting at the access fence.
	out := <-done
	if out.Code != 403 || !strings.Contains(out.Body.String(), "permission") {
		t.Fatalf("revocation refused for wrong reason: %d %s", out.Code, out.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var consent bool
		err := tx.QueryRow(t.Context(), `SELECT execute_consent FROM recurrence_definitions WHERE recurrence_id=$1`, r.ID).Scan(&consent)
		if consent {
			t.Fatal("revoked owner obtained consent")
		}
		return err
	})
}
