// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type ExecutionRuntime = modelregistry.ExecutionRuntime
type ExecutionRuntimeReader = modelregistry.ExecutionRuntimeReader

// WithExecutionRuntime supplies independently observed exact runtime pins.
// Without it the project gate remains off, regardless of admission injection.
func (m *Module) WithExecutionRuntime(reader ExecutionRuntimeReader) *Module {
	m.executionRuntime = reader
	return m
}

// ExecutionPolicy is public and redacted. Identity/allowed targets remain
// internal to the final claim. Ticket-only definitions need no consent.
type ExecutionPolicy struct {
	Eligible             bool   `json:"eligible"`
	ConsentRequired      bool   `json:"consent_required"`
	WaitReason           string `json:"wait_reason"`
	ProjectRevision      int64  `json:"project_revision"`
	ConsentRevision      int64  `json:"consent_revision"`
	executionPrincipalID string
	qualificationID      string
	policyDigest         string
}

func routinePolicyTx(ctx context.Context, tx pgx.Tx, tenantID string, item Recurrence, reader ExecutionRuntimeReader, checkConsent bool) (ExecutionPolicy, error) {
	out := ExecutionPolicy{}
	if item.Definition == nil || item.Definition.Assignment == nil {
		out.Eligible = true
		return out, nil
	}
	out.ConsentRequired = true
	settings, err := modelregistry.ProjectExecutionTx(ctx, tx, tenantID, item.ProjectID, reader)
	if err != nil {
		return out, err
	}
	out.ProjectRevision = settings.Revision
	if !settings.AutomaticLaunchEnabled {
		out.WaitReason = settings.WaitReason
		return out, nil
	}
	d := item.Definition
	owner := tenant.Principal{ID: d.OwnerPrincipalID, TenantID: tenantID, Kind: tenant.Person}
	if err := validateDefinition(ctx, tx, owner, item.Input); err != nil {
		var failure *workorders.Error
		// Invalid saved definitions close execution, but must not roll back
		// reads, pause or consent revocation. Unexpected failures still surface.
		if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) || errors.As(err, &failure) && failure.Status >= 400 && failure.Status < 500 {
			out.WaitReason = "definition_unavailable"
			return out, nil
		}
		return out, err
	}
	for _, permission := range append(executionPermissions(d.Assignment), "harness.control") {
		if err := authz.RequireTx(ctx, tx, owner, permission, authz.Scope{ProjectID: item.ProjectID}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				out.WaitReason = "owner_unavailable"
				return out, nil
			}
			return out, err
		}
	}
	q, err := modelregistry.LoadRoutineQualificationTx(ctx, tx, item.ProjectID, *settings.QualificationID)
	if err != nil {
		return out, err
	}
	if q.OwnerPersonID != owner.ID {
		out.WaitReason = "owner_policy_mismatch"
		return out, nil
	}
	capability := "routine_native_coding_v1"
	if d.Assignment.RuntimeRequirements.NeedsBrowser {
		capability = "routine_native_browser_v1"
	}
	if !slices.Contains(q.Runtime.Capabilities, capability) {
		out.WaitReason = "capability_unqualified"
		return out, nil
	}
	if !slices.Contains(q.Runtime.BudgetModes, d.Assignment.Budget.Mode) {
		out.WaitReason = "budget_mode_unqualified"
		return out, nil
	}
	out.qualificationID = q.ID
	out.policyDigest, err = modelregistry.RoutinePolicyDigest(struct {
		Definition      *Definition
		Policy          string
		ProjectRevision int64
		Qualification   string
	}{d, q.PolicyDigest, settings.Revision, q.ID})
	if err != nil {
		return out, err
	}
	if !checkConsent || item.ID == "" {
		out.WaitReason = "execution_consent_required"
		return out, nil
	}
	var consent bool
	var principalID, consentedBy, pin, qualification *string
	err = tx.QueryRow(ctx, `SELECT execute_consent,consent_revision,execution_principal_id::text,consented_by_principal_id::text,consent_policy_digest,consent_qualification_id::text FROM recurrence_definitions WHERE recurrence_id=$1`, item.ID).Scan(&consent, &out.ConsentRevision, &principalID, &consentedBy, &pin, &qualification)
	if err != nil {
		return out, err
	}
	if !consent || consentedBy == nil || *consentedBy != owner.ID || out.ConsentRevision != item.Revision || pin == nil || *pin != out.policyDigest || qualification == nil || *qualification != q.ID {
		out.WaitReason = "execution_consent_required"
		return out, nil
	}
	if principalID == nil {
		out.WaitReason = "execution_identity_unavailable"
		return out, nil
	}
	if err := executionIdentityTx(ctx, tx, tenantID, item.Input, *principalID); err != nil {
		if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
			out.WaitReason = "execution_identity_unavailable"
			return out, nil
		}
		return out, err
	}
	out.executionPrincipalID, out.Eligible = *principalID, true
	return out, nil
}

func (m *Module) projectExecutionPolicy(ctx context.Context, tx pgx.Tx, tenantID string, item *Recurrence) error {
	if item.Definition == nil {
		return nil
	}
	policy, err := routinePolicyTx(ctx, tx, tenantID, *item, m.executionRuntime, true)
	item.ExecutionPolicy = &policy
	return err
}

// RequireExecutionConsentTx re-reads the exact record and owner after taking
// tenant/tree fences. It creates no grant. S05/S13 must call this before run
// rows/holds and append events only after every further admission check.
func RequireExecutionConsentTx(ctx context.Context, tx pgx.Tx, tenantID, id string, revision int64, reader ExecutionRuntimeReader) (string, error) {
	if !workorders.UUID(id) || revision < 1 {
		return "", workorders.Fail(400, "invalid_routine_revision")
	}
	if _, err := lock(ctx, tx, tenantID, false); err != nil {
		return "", err
	}
	item, err := load(ctx, tx, id, true)
	if err != nil {
		return "", err
	}
	if item.Revision != revision {
		return "", workorders.Fail(409, "stale_revision")
	}
	if item.Definition == nil || item.Definition.Assignment == nil {
		return "", workorders.Fail(409, "ticket_only_routine")
	}
	if item.Paused {
		return "", workorders.Fail(409, "routine_paused")
	}
	policy, err := routinePolicyTx(ctx, tx, tenantID, item, reader, true)
	if err != nil {
		return "", err
	}
	if !policy.Eligible {
		return "", workorders.Fail(409, policy.WaitReason)
	}
	return policy.executionPrincipalID, nil
}

func executionPermissions(a *Assignment) []string {
	permissions := []string{"nodes.read", "run.create", "work_orders.write"}
	for _, source := range a.Sources {
		if source.Kind == "knowledge" {
			permissions = append(permissions, "knowledge.read")
		}
	}
	for _, action := range a.AllowedActions {
		switch action {
		case "work.create", "work.update":
			permissions = append(permissions, "nodes.write")
		case "knowledge.write":
			permissions = append(permissions, "knowledge.read", "knowledge.write")
		}
	}
	slices.Sort(permissions)
	return slices.Compact(permissions)
}

func executionIdentityTx(ctx context.Context, tx pgx.Tx, tenantID string, in Input, id string) error {
	d := in.Definition
	if d.Scope.Kind == "personal" {
		if id != d.OwnerPrincipalID {
			return authz.ErrForbidden
		}
		canonical, err := canonicalPerson(ctx, tx, tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Person})
		if err != nil {
			return err
		}
		if canonical != id {
			return authz.ErrForbidden
		}
		return nil
	}
	if d.Scope.Kind == "project" && d.Scope.ProjectID != in.ProjectID {
		return authz.ErrForbidden
	}
	// Provisioning remains the administrator's existing agent/role workflow.
	// Exactly one custom, bounded role; no key, pairing, builtin or extra binding.
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals p WHERE p.id=$1 AND p.kind='agent' AND p.status='active' AND p.linked_to IS NULL AND p.agent_access_configured AND NOT EXISTS(SELECT 1 FROM agent_keys k WHERE k.principal_id=p.id AND k.revoked_at IS NULL) AND NOT EXISTS(SELECT 1 FROM agent_pairing_computers c WHERE c.principal_id=p.id))`, id).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return authz.ErrForbidden
	}
	var roleID, scope string
	var project *string
	var builtin bool
	var count int
	err = tx.QueryRow(ctx, `SELECT b.role_id::text,b.scope_type,b.scope_id::text,r.builtin,count(*) OVER() FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.principal_id=$1 ORDER BY b.id LIMIT 1`, id).Scan(&roleID, &scope, &project, &builtin, &count)
	if err != nil {
		return err
	}
	if builtin || count != 1 || scope != d.Scope.Kind || scope == "project" && (project == nil || *project != d.Scope.ProjectID) {
		return authz.ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT permission FROM role_permissions WHERE role_id=$1 ORDER BY permission LIMIT 33`, roleID)
	if err != nil {
		return err
	}
	defer rows.Close()
	allowed := executionPermissions(d.Assignment)
	permissions := []string{}
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return err
		}
		if !slices.Contains(allowed, permission) {
			return authz.ErrForbidden
		}
		permissions = append(permissions, permission)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	p := tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Agent, Scopes: permissions}
	for _, permission := range []string{"nodes.read", "run.create", "work_orders.write"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: in.ProjectID}); err != nil {
			return err
		}
	}
	for _, source := range d.Assignment.Sources {
		var project string
		if err := tx.QueryRow(ctx, `SELECT coalesce(project_id::text,'') FROM nodes WHERE id=$1 AND deleted_at IS NULL`, source.ID).Scan(&project); err != nil {
			return err
		}
		permission := "nodes.read"
		if source.Kind == "knowledge" {
			permission = "knowledge.read"
		}
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) writeExecutionConsent(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if !modelregistry.InteractivePerson(p) || r.Header.Get("Authorization") != "" {
		reply(w, 0, nil, workorders.Fail(403, "interactive_person_required"))
		return
	}
	var in struct {
		ExpectedRevision     *int64  `json:"expected_revision"`
		ExecuteConsent       *bool   `json:"execute_consent"`
		ExecutionPrincipalID *string `json:"execution_principal_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 1 || in.ExecuteConsent == nil || in.ExecutionPrincipalID != nil && !workorders.UUID(*in.ExecutionPrincipalID) {
		reply(w, 0, nil, workorders.Fail(400, "invalid_execution_consent"))
		return
	}
	var out Recurrence
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		item, err := load(ctx, tx, r.PathValue("recurrenceId"), true)
		if err != nil {
			return err
		}
		if item.Definition == nil || item.Definition.Assignment == nil {
			return workorders.Fail(409, "ticket_only_routine")
		}
		if err := manageDefinition(ctx, tx, p, item.Input); err != nil {
			return err
		}
		person, err := canonicalPerson(ctx, tx, p)
		if err != nil {
			return err
		}
		if person != item.Definition.OwnerPrincipalID {
			admin, err := modelregistry.RoutineAdministratorTx(ctx, tx, p, item.Definition.Scope.ProjectID)
			if err != nil {
				return err
			}
			if *in.ExecuteConsent || !admin || item.Definition.Scope.Kind == "personal" {
				return authz.ErrForbidden
			}
		}
		if item.Revision != *in.ExpectedRevision {
			return workorders.Fail(409, "stale_revision")
		}
		var principalID, pin, qualification any
		if *in.ExecuteConsent {
			policy, err := routinePolicyTx(ctx, tx, p.TenantID, item, m.executionRuntime, false)
			if err != nil {
				return err
			}
			if policy.WaitReason != "execution_consent_required" {
				return workorders.Fail(409, policy.WaitReason)
			}
			id := person
			if item.Definition.Scope.Kind != "personal" {
				if in.ExecutionPrincipalID == nil {
					return workorders.Fail(409, "execution_identity_required")
				}
				id = *in.ExecutionPrincipalID
			} else if in.ExecutionPrincipalID != nil && *in.ExecutionPrincipalID != person {
				return authz.ErrForbidden
			}
			if err := executionIdentityTx(ctx, tx, p.TenantID, item.Input, id); err != nil {
				return err
			}
			principalID, pin, qualification = id, policy.policyDigest, policy.qualificationID
		}
		_, err = tx.Exec(ctx, `UPDATE recurrence_definitions SET execute_consent=$2,consent_revision=CASE WHEN $2 THEN $3 ELSE 0 END,execution_principal_id=$4,consented_by_principal_id=CASE WHEN $2 THEN $5::uuid ELSE NULL END,consented_at=CASE WHEN $2 THEN clock_timestamp() ELSE NULL END,consent_policy_digest=$6,consent_qualification_id=$7 WHERE recurrence_id=$1`, item.ID, *in.ExecuteConsent, item.Revision+1, principalID, person, pin, qualification)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE recurrences SET revision=revision+1,updated_at=clock_timestamp() WHERE id=$1`, item.ID)
		if err != nil {
			return err
		}
		out, err = load(ctx, tx, item.ID, false)
		if err != nil {
			return err
		}
		policy, err := routinePolicyTx(ctx, tx, p.TenantID, out, m.executionRuntime, true)
		if err != nil {
			return err
		}
		out.ExecutionPolicy = &policy
		// Existing scoped recurrence audit policy protects personal snapshots.
		return record(ctx, tx, p, item.ProjectID, "recurrence.updated", item, out)
	})
	reply(w, 200, out, err)
}
