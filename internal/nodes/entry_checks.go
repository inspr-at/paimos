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
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func entryScope(project *string) authz.Scope { return authz.Scope{ProjectID: deref(project)} }

func (m *Module) entryChecksTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (workqueue.EntryChecks, error) {
	t, err := workqueue.EntryTicketTx(ctx, tx, id)
	out := workqueue.EntryChecks{NodeID: id, Revision: t.Revision, Blocks: []workqueue.EntryBlock{}, Entries: []workqueue.EntryRecord{}}
	if err != nil {
		return out, err
	}
	if err = authz.RequireTx(ctx, tx, p, "nodes.read", entryScope(t.ProjectID)); err != nil {
		return out, err
	}
	out.Definition, err = workqueue.EntryDefinitionTx(ctx, tx, t.ProjectID, true)
	if err != nil {
		return out, err
	}
	for _, state := range []string{"in_progress", "qa", "done"} {
		block, err := workqueue.EvaluateEntryTx(ctx, tx, p, t, out.Definition, state)
		if err != nil {
			return out, err
		}
		out.Blocks = append(out.Blocks, block)
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT metadata->'entry_checks' FROM events WHERE node_id=$1 AND type IN ('node.created','node.updated','status_autopilot.changed','delivery.merge_done') AND metadata->'entry_checks'->>'to_status'=$2 ORDER BY id DESC LIMIT 1`, id, state).Scan(&raw)
		if err == nil {
			var record workqueue.EntryRecord
			if err = json.Unmarshal(raw, &record); err != nil {
				return out, err
			}
			out.Entries = append(out.Entries, record)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT after FROM events WHERE node_id=$1 AND type='entry_checks.refused' ORDER BY id DESC LIMIT 1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &out.LatestRefusal)
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}

func (m *Module) handleEntryChecks(w http.ResponseWriter, r *http.Request) {
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
	var out workqueue.EntryChecks
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = m.entryChecksTx(ctx, tx, p, id)
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

func (m *Module) recordNodeEntry(ctx context.Context, tx pgx.Tx, p tenant.Principal, eventType string, before any, after nodeJSON, record *workqueue.EntryRecord) error {
	if record == nil {
		return m.record(ctx, tx, p.ID, &after.ID, eventType, before, after)
	}
	beforeRaw, err := snapshot(before)
	if err != nil {
		return err
	}
	afterRaw, err := snapshot(after)
	if err != nil {
		return err
	}
	return m.writeEvent(ctx, tx, Event{ActorPrincipalID: p.ID, NodeID: &after.ID, Type: eventType, Before: beforeRaw, After: afterRaw, Metadata: entryJSON(map[string]any{"entry_checks": record})})
}
func (m *Module) recordRefusal(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, f *workqueue.EntryFailure) error {
	if p.Kind != tenant.Agent {
		return nil
	}
	t, err := workqueue.EntryTicketTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = authz.RequireTx(ctx, tx, p, "nodes.write", entryScope(t.ProjectID)); err != nil {
		return err
	}
	refusal := workqueue.EntryRefusal{ToStatus: f.ToStatus, Failed: f.Failed, By: workqueue.EntryPerson{ID: p.ID, Name: p.Name}, Count: 1}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&refusal.At); err != nil {
		return err
	}
	// Only identical consecutive refusals fold. History remains append-only.
	var typ string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT type,after FROM events WHERE node_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&typ, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if typ == "entry_checks.refused" {
		var prior workqueue.EntryRefusal
		if json.Unmarshal(raw, &prior) == nil && prior.By.ID == p.ID && prior.ToStatus == f.ToStatus && sameJSON(entryJSON(prior.Failed), entryJSON(f.Failed)) {
			refusal.Count = prior.Count + 1
		}
	}
	return m.record(ctx, tx, p.ID, &id, "entry_checks.refused", nil, refusal)
}
func entryJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (m *Module) handleEntryTick(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, 403, "only the person owner or a project admin may tick this check")
		return
	}
	var in struct {
		Checked  *bool     `json:"checked"`
		Revision time.Time `json:"expected_revision"`
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(body, &in); err != nil || in.Revision.IsZero() || in.Checked == nil {
		writeError(w, 400, "checked and expected_revision are required")
		return
	}
	check := r.PathValue("checkId")
	if len(check) > 80 {
		writeError(w, 400, "invalid check id")
		return
	}
	var out workqueue.EntryChecks
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		n, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !n.UpdatedAt.Equal(in.Revision) {
			return &httpError{status: 412, msg: "node has changed"}
		}
		out, err = m.entryChecksTx(ctx, tx, p, id)
		if err != nil {
			return err
		}
		var item *workqueue.EntryResult
		for _, b := range out.Blocks {
			for _, row := range b.Checks {
				if row.ID == check {
					copy := row
					item = &copy
				}
			}
		}
		if item == nil || out.Definition == nil || !item.Applicable {
			return badRequest("manual check is not applicable")
		}
		if !item.CanTick {
			return authz.ErrForbidden
		}
		t, err := workqueue.EntryTicketTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "nodes.write", entryScope(t.ProjectID)); err != nil {
			return err
		}
		if item.Passed == *in.Checked {
			return nil
		}
		var at time.Time
		if err = tx.QueryRow(ctx, `UPDATE nodes SET updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING updated_at`, id).Scan(&at); err != nil {
			return err
		}
		// Complete the response before the event counter is acquired.
		out.Revision = at
		for i := range out.Blocks {
			for j := range out.Blocks[i].Checks {
				row := &out.Blocks[i].Checks[j]
				if row.ID == check {
					if *in.Checked {
						out.Blocks[i].Passed++
					} else {
						out.Blocks[i].Passed--
					}
					row.Passed = *in.Checked
					row.CheckedBy = &workqueue.EntryPerson{ID: p.ID, Name: p.Name}
					row.CheckedAt = &at
				}
			}
		}
		mark := map[string]any{"check_id": check, "checked": *in.Checked, "definition_id": out.Definition.ID, "definition_revision": out.Definition.Revision, "by": workqueue.EntryPerson{ID: p.ID, Name: p.Name}, "at": at}
		return m.recordEntryTick(ctx, tx, p, id, mark)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type entryDefinitionInput struct {
	Checks     map[string][]workqueue.EntryRule `json:"checks"`
	Revision   *time.Time                       `json:"expected_revision"`
	CopiedFrom *time.Time                       `json:"copied_from_revision"`
	Workspace  *time.Time                       `json:"expected_workspace_revision"`
}

type entryDefinitionContext struct {
	ProjectID  string                           `json:"project_id"`
	Definition *workqueue.EntryDefinition       `json:"definition"`
	Workspace  *workqueue.EntryDefinition       `json:"workspace_default"`
	Base       map[string][]workqueue.EntryRule `json:"copied_from_checks"`
	CanManage  bool                             `json:"can_manage"`
}

func entryCopyChecksTx(ctx context.Context, tx pgx.Tx, id string) (map[string][]workqueue.EntryRule, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT fields->'copied_from_checks' FROM nodes WHERE id=$1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var checks map[string][]workqueue.EntryRule
	if len(raw) > 65536 || json.Unmarshal(raw, &checks) != nil {
		return nil, conflict("invalid definition copy base")
	}
	if err := workqueue.ValidateEntryRules(checks); err != nil {
		return nil, conflict("invalid definition copy base")
	}
	return checks, nil
}

func (m *Module) handleDefinition(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	var project *string
	if raw := r.PathValue("projectId"); raw != "" {
		id, ok := pathUUID(w, raw, "invalid project id")
		if !ok {
			return
		}
		project = &id
	}
	reset := r.Method == http.MethodDelete
	write := r.Method == http.MethodPut || reset
	var in entryDefinitionInput
	if write {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		if err := decodeJSON(body, &in); err != nil {
			writeErr(w, err)
			return
		}
		if reset && in.Revision == nil {
			writeErr(w, badRequest("expected_revision is required"))
			return
		}
		if !reset {
			if err := workqueue.ValidateEntryRules(in.Checks); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	var out *workqueue.EntryDefinition
	var editor *entryDefinitionContext
	err := m.tx(r.Context(), p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if project != nil {
			var id string
			if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, *project).Scan(&id); err != nil {
				return err
			}
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", entryScope(project)); err != nil {
			return err
		}
		if write {
			if p.Kind != tenant.Person {
				return authz.ErrForbidden
			}
			if project == nil {
				if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
					return err
				}
			} else if err := authz.RequireProjectAdminTx(ctx, tx, p, *project); err != nil {
				return err
			}
		}
		current, err := workqueue.EntryDefinitionTx(ctx, tx, project, !write)
		if err != nil {
			return err
		}
		out = current
		if !write {
			if project != nil && strings.HasSuffix(r.URL.Path, "/context") {
				workspace, err := workqueue.EntryDefinitionTx(ctx, tx, nil, true)
				if err != nil {
					return err
				}
				editor = &entryDefinitionContext{ProjectID: *project, Definition: current, Workspace: workspace}
				if p.Kind == tenant.Person {
					err := authz.RequireProjectAdminTx(ctx, tx, p, *project)
					if err != nil && !errors.Is(err, authz.ErrForbidden) {
						return err
					}
					editor.CanManage = err == nil
				}
				if current != nil && current.Source == "project" {
					editor.Base, err = entryCopyChecksTx(ctx, tx, current.ID)
					if err != nil {
						return err
					}
					if editor.Base == nil && current.CopiedFrom != nil && workspace != nil && current.CopiedFrom.Equal(workspace.Revision) {
						editor.Base = workspace.Checks
					}
				}
			}
			return nil
		}
		if current == nil && in.Revision != nil || current != nil && (in.Revision == nil || !current.Revision.Equal(*in.Revision)) {
			return conflict("definition has changed")
		}
		var baseChecks map[string][]workqueue.EntryRule
		workspace, err := workqueue.EntryDefinitionTx(ctx, tx, nil, true)
		if err != nil {
			return err
		}
		if project != nil && (reset || in.Workspace != nil) && (workspace == nil && in.Workspace != nil || workspace != nil && (in.Workspace == nil || !workspace.Revision.Equal(*in.Workspace))) {
			return conflict("workspace default has changed; reload before copying or resetting")
		}
		if reset {
			if current == nil {
				return conflict("project definition has changed")
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1`, current.ID); err != nil {
				return err
			}
			out = workspace
			return m.record(ctx, tx, p.ID, &current.ID, "definition.reset", current, workspace)
		}
		if current != nil {
			// Ordinary edits preserve the server's original base, even after a
			// later default change. Clients cannot silently rebase a copy.
			if in.CopiedFrom != nil && (current.CopiedFrom == nil || !current.CopiedFrom.Equal(*in.CopiedFrom)) {
				return badRequest("copy base cannot change during an edit")
			}
			in.CopiedFrom = current.CopiedFrom
			baseChecks, err = entryCopyChecksTx(ctx, tx, current.ID)
			if err != nil {
				return err
			}
			if baseChecks == nil && in.CopiedFrom != nil && workspace != nil && in.CopiedFrom.Equal(workspace.Revision) {
				baseChecks = workspace.Checks
			}
		} else if in.CopiedFrom != nil {
			if project == nil || workspace == nil || !workspace.Revision.Equal(*in.CopiedFrom) {
				return badRequest("copied_from_revision must name the current workspace default")
			}
			baseChecks = workspace.Checks
		}
		for _, rules := range in.Checks {
			for _, rule := range rules {
				if rule.FallbackOwnerID != nil {
					fallback := rule
					fallback.OwnerID, fallback.FallbackOwnerID = rule.FallbackOwnerID, nil
					owner, err := workqueue.EntryOwnerTx(ctx, tx, p, workqueue.EntryTicket{ProjectID: project}, fallback)
					if err != nil {
						return err
					}
					if owner == nil || owner.ID != *rule.FallbackOwnerID {
						return badRequest("manual check fallback must be a person with current project access")
					}
				}
				if rule.OwnerID != nil {
					t := workqueue.EntryTicket{ProjectID: project}
					owner, err := workqueue.EntryOwnerTx(ctx, tx, p, t, rule)
					if err != nil {
						return err
					}
					if owner == nil || owner.ID != *rule.OwnerID {
						return badRequest("manual check owner must be a person with current project access")
					}
				}
			}
		}
		fields := entryJSON(map[string]any{"checks": in.Checks, "copied_from_revision": in.CopiedFrom, "copied_from_checks": baseChecks})
		if len(fields) > 65536 {
			return badRequest("definition and copy base exceed 64 KiB")
		}
		var id string
		var revision time.Time
		if current == nil {
			var kind string
			if err = tx.QueryRow(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'definition','Definition','DEF','list',ARRAY[]::text[]) ON CONFLICT(tenant_id,slug) DO UPDATE SET slug=EXCLUDED.slug RETURNING id::text`, p.TenantID).Scan(&kind); err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) VALUES($1,$2,aeon_next_node_key($1,'DEF'),'Entry checks',$3,$4::jsonb) RETURNING id::text,updated_at`, p.TenantID, kind, project, string(fields)).Scan(&id, &revision)
		} else {
			id = current.ID
			err = tx.QueryRow(ctx, `UPDATE nodes SET fields=$2::jsonb,updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING updated_at`, id, string(fields)).Scan(&revision)
		}
		if err != nil {
			return err
		}
		out = &workqueue.EntryDefinition{ID: id, Revision: revision, Source: "workspace", CopiedFrom: in.CopiedFrom, Checks: in.Checks}
		if project != nil {
			out.Source = "project"
		}
		if project != nil && in.CopiedFrom != nil && workspace != nil && workspace.ID != "00000000-0000-0000-0000-000000000000" && workspace.Revision.Equal(*in.CopiedFrom) {
			if _, err = tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'cites') ON CONFLICT DO NOTHING`, p.TenantID, id, workspace.ID); err != nil {
				return err
			}
		}
		return m.record(ctx, tx, p.ID, &id, "definition.saved", current, out)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = notFound("project not found")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if editor != nil {
		writeJSON(w, 200, editor)
	} else {
		writeJSON(w, 200, out)
	}
}

func entryFailedLabels(f *workqueue.EntryFailure) string {
	labels := []string{}
	for _, check := range f.Failed {
		label := check.Label
		if check.Reason != "" {
			label += ": " + check.Reason
		}
		labels = append(labels, label)
	}
	return "Missing entry checks: " + strings.Join(labels, ", ")
}

// Retain the released event for existing clients and add a public node-domain
// pointer for project-only readers. Both pass the existing tenant/reference
// policies; no event visibility policy is widened. No new locks follow either.
func (m *Module) recordEntryTick(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, mark any) error {
	if err := m.record(ctx, tx, p.ID, &id, "entry_check.ticked", nil, mark); err != nil {
		return err
	}
	return m.record(ctx, tx, p.ID, &id, "node.entry_check_ticked", nil, mark)
}
