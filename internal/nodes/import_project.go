// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// ImportedProject is the project a conversation handed over from START
// creates (AEON-1441): the project, owned by a person, with one work item per
// chosen open point.
type ImportedProject struct {
	Title    string
	Key      string
	OwnerID  string // a person; stored as the project's assignee
	EndDate  string // optional target date, YYYY-MM-DD
	Work     []string
	WorkBody string
}

// ImportedProjectResult carries the created project as GET /api/nodes/{id}
// returns it, the new work item ids and the node events. The caller appends
// the events after its remaining locks, the event counter last.
type ImportedProjectResult struct {
	Project   json.RawMessage
	ProjectID string
	WorkIDs   []string
	Events    []Event
}

// CreateImportedProjectTx creates the project and its work items through the
// ordinary create path: kind schema, assignee check, key reservation, entry
// checks and nodes.write in each target's scope. The caller holds the tenant
// and tree fences. Errors a person can act on carry an HTTP status.
func CreateImportedProjectTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ImportedProject) (ImportedProjectResult, error) {
	var projectKind, workKind string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE slug='project'`).Scan(&projectKind); err != nil {
		return ImportedProjectResult{}, routineError(dbErr("project kind", err))
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE slug='work'`).Scan(&workKind); err != nil {
		return ImportedProjectResult{}, routineError(dbErr("work kind", err))
	}
	fields := map[string]any{"project_key": in.Key, "assignee": in.OwnerID}
	if in.EndDate != "" {
		fields["end_date"] = in.EndDate
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return ImportedProjectResult{}, err
	}
	pending := &routineEvents{}
	m := &Module{events: pending}
	empty, active, prefix := "", "active", in.Key
	project, err := m.createNodeTx(ctx, tx, p, nodeCreate{KindID: projectKind, Title: in.Title, Body: &empty, Fields: encoded, State: &active, KeyPrefix: &prefix})
	if err != nil {
		return ImportedProjectResult{}, routineError(err)
	}
	out := ImportedProjectResult{ProjectID: project.ID, WorkIDs: make([]string, 0, len(in.Work))}
	state, body := "new", in.WorkBody
	for _, title := range in.Work {
		work, err := m.createNodeTx(ctx, tx, p, nodeCreate{KindID: workKind, Title: title, Body: &body, Fields: json.RawMessage(`{}`), State: &state, ParentID: &project.ID, KeyPrefix: &prefix})
		if err != nil {
			return ImportedProjectResult{}, routineError(err)
		}
		out.WorkIDs = append(out.WorkIDs, work.ID)
	}
	if out.Project, err = json.Marshal(project); err != nil {
		return ImportedProjectResult{}, err
	}
	out.Events = pending.items
	return out, nil
}

// NodeSnapshotTx returns a live node as GET /api/nodes/{id} returns it, for a
// retried import that answers with the project it already created.
func NodeSnapshotTx(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, error) {
	node, err := loadNode(ctx, tx, id, false)
	if err != nil {
		return nil, routineError(err)
	}
	if err := enrichNodes(ctx, tx, []*nodeJSON{&node}); err != nil {
		return nil, err
	}
	return json.Marshal(node)
}
