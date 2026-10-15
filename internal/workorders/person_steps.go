// SPDX-License-Identifier: AGPL-3.0-only
package workorders

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// PersonStep is a pointer into native ticket/definition state, never a second
// source of completion. Assignment events override exactly one ticket step.
type PersonStep struct {
	ID                 string      `json:"id"`
	NodeID             string      `json:"node_id"`
	CheckID            string      `json:"check_id"`
	Label              string      `json:"label"`
	LabelDE            string      `json:"label_de"`
	ToStatus           string      `json:"to_status"`
	Owner              EntryPerson `json:"owner"`
	Checked            bool        `json:"checked"`
	Active             bool        `json:"active"`
	CanTick            bool        `json:"can_tick"`
	Revision           time.Time   `json:"revision"`
	DefinitionID       *string     `json:"definition_id"`
	DefinitionRevision *time.Time  `json:"definition_revision"`
	CreatedAt          time.Time   `json:"created_at"`
	TicketKey          string      `json:"ticket_key"`
	TicketTitle        string      `json:"ticket_title"`
	ProjectID          *string     `json:"project_id"`
}

func PersonStepsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) ([]PersonStep, error) {
	t, err := EntryTicketTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	project := ""
	if t.ProjectID != nil {
		project = *t.ProjectID
	}
	if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
		return nil, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT aeon_person_steps($1)`, id).Scan(&raw); err != nil {
		return nil, err
	}
	if len(raw) > 256*1024 {
		return nil, Fail(422, "person-step coverage exceeded")
	}
	steps := []PersonStep{}
	if err = json.Unmarshal(raw, &steps); err != nil {
		return nil, err
	}
	if len(steps) > 97 {
		return nil, Fail(422, "person-step coverage exceeded")
	}
	write := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: project}) == nil
	admin := p.Kind == tenant.Person && authz.RequireProjectAdminTx(ctx, tx, p, project) == nil
	for i := range steps {
		steps[i].CanTick = p.Kind == tenant.Person && write && (steps[i].Owner.ID == p.ID || admin)
	}
	return steps, nil
}
