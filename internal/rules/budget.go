// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// LoadBudget reads the workspace's always-on budget (AEON-314). Without a
// stored row it is the ADR-004 default: 12,000 bytes and no layer caps.
func LoadBudget(ctx context.Context, tx pgx.Tx) (Budget, error) {
	var b Budget
	var company, project, person, agent *int
	err := tx.QueryRow(ctx, `SELECT max_bytes,company_bytes,project_bytes,person_bytes,agent_bytes FROM rule_budget_settings`).Scan(&b.MaxBytes, &company, &project, &person, &agent)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultBudget(), nil
	}
	if err != nil {
		return Budget{}, err
	}
	for layer, v := range map[string]*int{"company": company, "project": project, "person": person, "agent": agent} {
		if v != nil {
			*b.Layers.ref(layer) = *v
		}
	}
	return b, nil
}

// BudgetView is what GET /api/rules/budget answers: the configured budget and
// the product bounds a person may choose from.
type BudgetView struct {
	Budget
	DefaultBytes        int             `json:"default_bytes"`
	MinBytes            int             `json:"min_bytes"`
	CeilingBytes        int             `json:"ceiling_bytes"`
	MinLayerBytes       int             `json:"min_layer_bytes"`
	BlockingClients     []ClientBlocker `json:"blocking_clients,omitempty"`
	BlockingClientsMore int             `json:"blocking_clients_more,omitempty"`
}

func budgetView(ctx context.Context, tx pgx.Tx, p tenant.Principal, b Budget) (BudgetView, error) {
	ceiling, clients, err := clientCeiling(ctx, tx)
	if err != nil {
		return BudgetView{}, err
	}
	view := BudgetView{Budget: b, DefaultBytes: LegacyMaxBytes, MinBytes: MinBudgetBytes, CeilingBytes: ceiling, MinLayerBytes: MinLayerBytes}
	// The product ceiling is public to rules readers, but tenant-wide host/version
	// inventory is workspace administration data, not project membership data.
	if (p.Kind == tenant.Person || authz.OwnerWorkstation(p)) && authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}) == nil {
		for i := range clients {
			clients[i].DeliveredMaxBytes = min(b.MaxBytes, clients[i].Maximum)
			clients[i].Truncated = clients[i].Maximum < b.MaxBytes
		}
		view.BlockingClients, view.BlockingClientsMore = listedBlockers(clients)
	}
	return view, nil
}

func (m *Module) getBudget(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	b, err := LoadBudget(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	return budgetView(r.Context(), tx, p, b)
}

// putBudget changes the workspace budget. Only a person who manages workspace
// settings may; the change is refused when a session file that is served now
// would no longer fit, so a smaller budget never silently breaks session start.
func (m *Module) putBudget(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person && !authz.OwnerWorkstation(p) {
		return nil, authz.ErrForbidden
	}
	ctx := r.Context()
	if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
		return nil, err
	}
	var in Budget
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if err := lockClientGate(ctx, tx); err != nil {
		return nil, err
	}
	before, err := LoadBudget(ctx, tx)
	if err != nil {
		return nil, err
	}
	if before == in {
		return budgetView(ctx, tx, p, in)
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	// A company scope matches every session context, so this renders every file
	// served now (within the same bounds a publication check has).
	if _, err = budgetCheck(ctx, tx, p, owner, []Set{{Scope: Scope{Layer: "company"}}}, time.Now().UTC(), in); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == "rules_budget_exceeded" {
			out := *e
			out.Message = "Published rules would not fit this budget: " + e.Message
			return nil, &out
		}
		return nil, err
	}
	layer := func(v int) any {
		if v == 0 {
			return nil
		}
		return v
	}
	_, err = tx.Exec(ctx, `INSERT INTO rule_budget_settings(tenant_id,max_bytes,company_bytes,project_bytes,person_bytes,agent_bytes,updated_by,updated_at)
		VALUES(current_setting('aeon.tenant_id')::uuid,$1,$2,$3,$4,$5,$6,clock_timestamp())
		ON CONFLICT (tenant_id) DO UPDATE SET max_bytes=EXCLUDED.max_bytes,company_bytes=EXCLUDED.company_bytes,project_bytes=EXCLUDED.project_bytes,
		person_bytes=EXCLUDED.person_bytes,agent_bytes=EXCLUDED.agent_bytes,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
		in.MaxBytes, layer(in.Layers.Company), layer(in.Layers.Project), layer(in.Layers.Person), layer(in.Layers.Agent), p.ID)
	if err != nil {
		return nil, err
	}
	if _, err = events.Append(ctx, tx, p, events.Change{Type: "settings.rules_budget_changed", Before: before, After: in}); err != nil {
		return nil, err
	}
	return budgetView(ctx, tx, p, in)
}
