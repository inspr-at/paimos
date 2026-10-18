// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type personStepsResponse struct {
	UndoReassignment int64                   `json:"undo_reassignment,omitempty"`
	NodeID           string                  `json:"node_id"`
	Revision         time.Time               `json:"revision"`
	Steps            []workorders.PersonStep `json:"steps"`
}

func personStepsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (personStepsResponse, error) {
	out := personStepsResponse{NodeID: id, Steps: []workorders.PersonStep{}}
	t, err := workorders.EntryTicketTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	out.Revision = t.Revision
	out.Steps, err = workorders.PersonStepsTx(ctx, tx, p, id)
	return out, err
}

func (m *Module) handlePersonSteps(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out personStepsResponse
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 4*time.Second); err != nil {
			return err
		}
		var err error
		out, err = personStepsTx(ctx, tx, p, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = notFound("node not found")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (m *Module) handlePersonStepWrite(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, 403, "only the person owner or a project admin may tick or reassign this check")
		return
	}
	check := r.PathValue("checkId")
	if len(check) > 80 || check != "human" && !strings.HasPrefix(check, "manual:") {
		writeError(w, 400, "invalid person step")
		return
	}
	var in struct {
		Revision           time.Time  `json:"expected_revision"`
		DefinitionRevision *time.Time `json:"definition_revision"`
		Checked            *bool      `json:"checked"`
		OwnerID            *string    `json:"owner_id"`
		UndoReassignment   *int64     `json:"undo_reassignment"`
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	reassign := strings.HasSuffix(r.Pattern, "/owner")
	if err := decodeJSON(body, &in); err != nil || in.Revision.IsZero() || reassign && (in.OwnerID == nil || !workorders.UUID(*in.OwnerID) || in.Checked != nil) || !reassign && (in.Checked == nil || in.OwnerID != nil || in.UndoReassignment != nil) || in.UndoReassignment != nil && *in.UndoReassignment < 1 {
		writeError(w, 400, "a revision and the requested checked state or person owner are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out personStepsResponse
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 4*time.Second); err != nil {
			return err
		}
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		n, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		t, err := workorders.EntryTicketTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "nodes.write", entryScope(t.ProjectID)); err != nil {
			return err
		}
		out, err = personStepsTx(ctx, tx, p, id)
		if err != nil {
			return err
		}
		var step *workorders.PersonStep
		for i := range out.Steps {
			if out.Steps[i].CheckID == check {
				step = &out.Steps[i]
				break
			}
		}
		if step == nil {
			return notFound("person step is no longer applicable")
		}
		undoAllowed := false
		if reassign && in.UndoReassignment != nil && *in.UndoReassignment > 0 {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events e WHERE e.node_id=$1 AND e.id=$2 AND e.type='node.person_step_reassigned' AND e.actor_principal_id=$3 AND e.before->'owner'->>'id'=$4 AND e.after->>'owner_id'=$5 AND e.after->>'check_id'=$6 AND (e.after->>'revision')::timestamptz=$7 AND e.id=(SELECT max(id) FROM events WHERE node_id=$1 AND type='node.person_step_reassigned' AND after->>'check_id'=$6))`, id, *in.UndoReassignment, p.ID, *in.OwnerID, step.Owner.ID, check, n.UpdatedAt).Scan(&undoAllowed)
			if err != nil {
				return err
			}
			if !undoAllowed {
				return authz.ErrForbidden
			}
		}
		if !step.CanTick && !undoAllowed {
			return authz.ErrForbidden
		}
		if !n.UpdatedAt.Equal(in.Revision) || !sameRevision(step.DefinitionRevision, in.DefinitionRevision) {
			return &httpError{status: 412, msg: "ticket or definition has changed"}
		}
		if reassign {
			if step.Owner.ID == *in.OwnerID {
				return nil
			}
			candidate := tenant.Principal{ID: *in.OwnerID, TenantID: p.TenantID, Kind: tenant.Person}
			var valid bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='person' AND status='active' AND linked_to IS NULL)`, candidate.ID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return badRequest("owner must be a current person with project access")
			}
			if err = authz.RequireTx(ctx, tx, candidate, "nodes.read", entryScope(t.ProjectID)); err != nil {
				if errors.Is(err, authz.ErrForbidden) {
					return badRequest("owner must be a current person with project access")
				}
				return err
			}
			var at time.Time
			if err = tx.QueryRow(ctx, `UPDATE nodes SET updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING updated_at`, id).Scan(&at); err != nil {
				return err
			}
			// All mutation locks precede the event counter. The response below
			// uses only reads and shares the canonical projection predicates.
			err = m.record(ctx, tx, p.ID, &id, "node.person_step_reassigned", map[string]any{"check_id": check, "owner": step.Owner}, map[string]any{"check_id": check, "owner_id": candidate.ID, "definition_id": step.DefinitionID, "definition_revision": step.DefinitionRevision, "revision": at, "undo_of": in.UndoReassignment})
		} else if step.Checked == *in.Checked {
			return nil
		} else if check == "human" {
			var text *string
			if !*in.Checked {
				text = &step.Label
			}
			fields, e := humanCheckFields(p, n.Fields, n.Fields, n.HumanCheck, text, true)
			if e != nil {
				return e
			}
			updated, e := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET human_check=$2,fields=$3::jsonb,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING `+nodeReturning, id, text, string(fields)))
			if e != nil {
				return e
			}
			err = m.recordNodeEntry(ctx, tx, p, evNodeUpdated, n, updated, nil)
		} else {
			var at time.Time
			if err = tx.QueryRow(ctx, `UPDATE nodes SET updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING updated_at`, id).Scan(&at); err != nil {
				return err
			}
			mark := map[string]any{"check_id": check, "checked": *in.Checked, "definition_id": step.DefinitionID, "definition_revision": step.DefinitionRevision, "by": workorders.EntryPerson{ID: p.ID, Name: p.Name}, "at": at}
			err = m.recordEntryTick(ctx, tx, p, id, mark)
		}
		if err != nil {
			return err
		}
		out, err = personStepsTx(ctx, tx, p, id)
		if err == nil && reassign {
			err = tx.QueryRow(ctx, `SELECT id FROM events WHERE node_id=$1 AND type='node.person_step_reassigned' AND actor_principal_id=$2 ORDER BY id DESC LIMIT 1`, id, p.ID).Scan(&out.UndoReassignment)
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = notFound("node not found")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func sameRevision(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func (m *Module) handlePersonStepOwners(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	check, q, after := r.URL.Query().Get("check_id"), r.URL.Query().Get("q"), r.URL.Query().Get("after")
	if len(check) > 80 || check == "" || len(q) > 400 || len([]rune(q)) > 100 || after != "" && !workorders.UUID(after) {
		writeError(w, 400, "invalid owner picker query")
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, 403, "a person owner or project admin is required")
		return
	}
	out := struct {
		Items []workorders.EntryPerson `json:"items"`
		Next  *string                  `json:"next_cursor"`
	}{Items: []workorders.EntryPerson{}}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 4*time.Second); err != nil {
			return err
		}
		steps, err := workorders.PersonStepsTx(ctx, tx, p, id)
		if err != nil {
			return err
		}
		var step *workorders.PersonStep
		for i := range steps {
			if steps[i].CheckID == check {
				step = &steps[i]
				break
			}
		}
		if step == nil {
			return notFound("person step is no longer applicable")
		}
		if !step.CanTick {
			return authz.ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT id::text,name FROM principals WHERE kind='person' AND status='active' AND linked_to IS NULL AND ($2='' OR id>nullif($2,'')::uuid) AND strpos(lower(name),lower($3))>0 AND id=aeon_person_step_owner($1,id,NULL) ORDER BY id LIMIT 51`, step.ProjectID, after, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v workorders.EntryPerson
			if err = rows.Scan(&v.ID, &v.Name); err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > 50 {
			out.Items = out.Items[:50]
			out.Next = &out.Items[49].ID
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = notFound("node not found")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// Legacy PATCH and causal undo use exactly the same live owner rule. This is
// called after the tenant/tree fence and before any write or provenance change.
func requireHumanTickTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, n nodeJSON, after *string) error {
	if sameString(n.HumanCheck, after) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(n.Fields, &fields); err != nil {
		return err
	}
	if n.HumanCheck != nil && after == nil || n.HumanCheck == nil && len(fields["human_check_completed"]) > 0 && string(fields["human_check_completed"]) != "null" {
		steps, err := workorders.PersonStepsTx(ctx, tx, p, n.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.CheckID == "human" && step.CanTick {
				return nil
			}
		}
		return &httpError{status: 403, msg: "only the person owner or a project admin may tick or undo this human check"}
	}
	return nil
}
