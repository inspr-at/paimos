// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// WithRoutineExecutionRuntime supplies independent exact deployed runtime
// facts. New stays unconfigured/default-off; leadAdmission alone never enables
// execution. This integration grants no model, host, account or process access.
func (m *Module) WithRoutineExecutionRuntime(reader modelregistry.ExecutionRuntimeReader) *Module {
	m.routineRuntime = reader
	return m
}

func (m *Module) readRoutineExecution(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx, project := r.Context(), r.PathValue("projectId")
	if !workorders.UUID(project) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: project}); err != nil {
		return nil, err
	}
	if _, err := leadProject(ctx, tx, project); err != nil {
		return nil, err
	}
	return modelregistry.ProjectExecutionTx(ctx, tx, p.TenantID, project, m.routineRuntime)
}

func (m *Module) writeRoutineExecution(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if !modelregistry.InteractivePerson(p) || r.Header.Get("Authorization") != "" {
		return nil, workorders.Fail(403, "interactive_person_required")
	}
	var in modelregistry.ExecutionSettingsInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	return modelregistry.WriteExecutionSettingsTx(r.Context(), tx, p, r.PathValue("projectId"), in, m.routineRuntime)
}

func (m *Module) projectLead(ctx context.Context, tx pgx.Tx, p tenant.Principal, l Lead) (Lead, error) {
	return projectLead(ctx, tx, p, l, m.routineRuntime)
}
