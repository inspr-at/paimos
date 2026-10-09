// SPDX-License-Identifier: AGPL-3.0-only
package accountuse

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Cell struct {
	AccountID string `json:"account_id"`
	ContextID string `json:"context_id"`
	Allowed   bool   `json:"allowed"`
}
type CellWrite struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Changes          []Cell `json:"changes"`
	Bulk             *Bulk  `json:"bulk,omitempty"`
}
type Bulk struct {
	Scope     string `json:"scope"`
	AccountID string `json:"account_id,omitempty"`
	ContextID string `json:"context_id,omitempty"`
	Allowed   bool   `json:"allowed"`
}
type CellResult struct {
	Revision int64  `json:"revision"`
	Changes  []Cell `json:"changes"`
	Undo     []Cell `json:"undo"`
}

func (m *Module) cells(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in CellWrite
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return WriteCells(r.Context(), tx, p, in)
}

// WriteCells also handles Undo: inverse cells plus the save's returned revision.
func WriteCells(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CellWrite) (CellResult, error) {
	if len(in.Changes) > 1000 || (in.Bulk != nil && len(in.Changes) != 0) || (in.Bulk == nil && len(in.Changes) == 0) {
		return CellResult{}, fail(400, "one bounded cell change set required")
	}
	if _, err := FenceWrite(ctx, tx, p, in.ExpectedRevision); err != nil {
		return CellResult{}, err
	}
	changes := in.Changes
	if in.Bulk != nil {
		b := in.Bulk
		if !(b.Scope == "all" && b.AccountID == "" && b.ContextID == "" || b.Scope == "account" && uuid(b.AccountID) && b.ContextID == "" || b.Scope == "context" && uuid(b.ContextID) && b.AccountID == "") {
			return CellResult{}, fail(400, "invalid bulk scope")
		}
		var target string
		if b.Scope == "account" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM agent_accounts WHERE id=$1 AND archived_at IS NULL`, b.AccountID).Scan(&target); err != nil {
				return CellResult{}, missing(err)
			}
		}
		if b.Scope == "context" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM work_contexts WHERE id=$1 AND archived_at IS NULL AND kind<>'holding'`, b.ContextID).Scan(&target); err != nil {
				return CellResult{}, missing(err)
			}
		}
		rows, err := tx.Query(ctx, `SELECT a.id::text,c.id::text FROM agent_accounts a CROSS JOIN work_contexts c
            WHERE a.archived_at IS NULL AND c.archived_at IS NULL AND c.kind<>'holding'
            AND ($1='' OR a.id=NULLIF($1,'')::uuid) AND ($2='' OR c.id=NULLIF($2,'')::uuid) ORDER BY a.id,c.id LIMIT 1001`, b.AccountID, b.ContextID)
		if err != nil {
			return CellResult{}, err
		}
		changes = []Cell{}
		for rows.Next() {
			cell := Cell{Allowed: b.Allowed}
			if err := rows.Scan(&cell.AccountID, &cell.ContextID); err != nil {
				rows.Close()
				return CellResult{}, err
			}
			changes = append(changes, cell)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return CellResult{}, err
		}
		if len(changes) > 1000 {
			return CellResult{}, fail(413, "bulk change exceeds 1000 cells; use bounded batches")
		}
	}
	undo := make([]Cell, 0, len(changes))
	seen := map[string]bool{}
	// Validate the entire target set before writes and the final event counter.
	for i := range changes {
		c := &changes[i]
		if !uuid(c.AccountID) || !uuid(c.ContextID) {
			return CellResult{}, fail(400, "invalid or duplicate cell")
		}
		c.AccountID, c.ContextID = strings.ToLower(c.AccountID), strings.ToLower(c.ContextID)
		key := c.AccountID + ":" + c.ContextID
		if seen[key] {
			return CellResult{}, fail(400, "invalid or duplicate cell")
		}
		seen[key] = true
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_use_cells x WHERE x.account_id=a.id AND x.context_id=c.id)
            FROM agent_accounts a CROSS JOIN work_contexts c WHERE a.id=$1 AND c.id=$2
            AND a.archived_at IS NULL AND c.archived_at IS NULL AND c.kind<>'holding'`, c.AccountID, c.ContextID).Scan(&allowed)
		if err != nil {
			return CellResult{}, missing(err)
		}
		undo = append(undo, Cell{c.AccountID, c.ContextID, allowed})
	}
	for _, c := range changes {
		var err error
		if c.Allowed {
			_, err = tx.Exec(ctx, `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by) VALUES($1,$2,$3,'person',$4)
                ON CONFLICT(tenant_id,account_id,context_id) DO UPDATE SET source='person',set_by=EXCLUDED.set_by,set_at=clock_timestamp()`, p.TenantID, c.AccountID, c.ContextID, p.ID)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM account_use_cells WHERE account_id=$1 AND context_id=$2`, c.AccountID, c.ContextID)
		}
		if err != nil {
			return CellResult{}, err
		}
	}
	after, err := bump(ctx, tx)
	if err != nil {
		return CellResult{}, err
	}
	out := CellResult{after.Revision, changes, undo}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "account_use.changed", Before: undo, After: out})
	return out, err
}

func (m *Module) rules(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		RuleValues
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return WriteRules(r.Context(), tx, p, in.ExpectedRevision, in.RuleValues)
}
func (m *Module) confirm(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	before, err := FenceWrite(r.Context(), tx, p, in.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	if !before.ConfirmationRequired {
		return before, nil
	}
	after, err := scanRules(tx.QueryRow(r.Context(), `UPDATE account_use_rules SET confirmation_required=false,confirmed_at=clock_timestamp(),confirmed_by=$1,revision=revision+1 RETURNING `+ruleColumns, p.ID))
	if err != nil {
		return nil, err
	}
	_, err = events.Append(r.Context(), tx, p, events.Change{Type: "account_use.confirmed", Before: before, After: after})
	return after, err
}

type WorkContext struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Kind                string  `json:"kind"`
	NewAccountsOverride *string `json:"new_accounts_override"`
	ArchivedAt          *string `json:"archived_at"`
	Revision            int64   `json:"revision"`
}

const contextColumns = `id::text,name,kind,new_accounts_override,archived_at::text,revision`

func scanContext(row pgx.Row) (c WorkContext, err error) {
	err = row.Scan(&c.ID, &c.Name, &c.Kind, &c.NewAccountsOverride, &c.ArchivedAt, &c.Revision)
	return
}

type contextWrite struct {
	ExpectedRevision    int64   `json:"expected_revision"`
	Name                string  `json:"name"`
	NewAccountsOverride *string `json:"new_accounts_override"`
	Archived            bool    `json:"archived"`
}
type contextResult struct {
	Context  WorkContext `json:"context"`
	Revision int64       `json:"revision"`
}

func (m *Module) createContext(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.context(r, tx, p, true)
}
func (m *Module) updateContext(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.context(r, tx, p, false)
}
func (m *Module) context(r *http.Request, tx pgx.Tx, p tenant.Principal, create bool) (any, error) {
	var in contextWrite
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if len([]rune(strings.TrimSpace(in.Name))) < 1 || len([]rune(in.Name)) > 128 || in.NewAccountsOverride != nil && *in.NewAccountsOverride != "deny" || create && in.Archived {
		return nil, fail(400, "invalid work context")
	}
	ctx := r.Context()
	rules, err := FenceWrite(ctx, tx, p, in.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	var before *WorkContext
	var out WorkContext
	if create {
		out, err = scanContext(tx.QueryRow(ctx, `INSERT INTO work_contexts(tenant_id,name,new_accounts_override) VALUES($1,$2,$3) RETURNING `+contextColumns, p.TenantID, strings.TrimSpace(in.Name), in.NewAccountsOverride))
	} else {
		if !uuid(r.PathValue("contextId")) {
			return nil, fail(400, "invalid context id")
		}
		c, readErr := scanContext(tx.QueryRow(ctx, `SELECT `+contextColumns+` FROM work_contexts WHERE id=$1 FOR UPDATE`, r.PathValue("contextId")))
		if readErr != nil {
			return nil, missing(readErr)
		}
		before = &c
		out, err = scanContext(tx.QueryRow(ctx, `UPDATE work_contexts SET name=$2,new_accounts_override=$3,archived_at=CASE WHEN $4 THEN coalesce(archived_at,clock_timestamp()) ELSE NULL END,revision=revision+1 WHERE id=$1 RETURNING `+contextColumns, c.ID, strings.TrimSpace(in.Name), in.NewAccountsOverride, in.Archived))
	}
	if err != nil {
		return nil, err
	}
	after, err := bump(ctx, tx)
	if err != nil {
		return nil, err
	}
	result := contextResult{out, after.Revision}
	var metadata json.RawMessage
	if create {
		metadata, err = json.Marshal(map[string]any{"rule": rules.NewContexts, "rule_revision": rules.Revision})
		if err != nil {
			return nil, err
		}
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "work_context.changed", Before: before, After: result, Metadata: metadata})
	return result, err
}

type projectResult struct {
	ProjectID string `json:"project_id"`
	ContextID string `json:"context_id"`
	Revision  int64  `json:"revision"`
}

func (m *Module) readProject(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if !uuid(r.PathValue("projectId")) {
		return nil, fail(400, "invalid project id")
	}
	var out projectResult
	err := tx.QueryRow(r.Context(), `SELECT m.project_id::text,m.context_id::text,r.revision FROM project_work_contexts m CROSS JOIN account_use_rules r
        JOIN nodes n ON n.tenant_id=r.tenant_id WHERE m.project_id=$1 AND n.id=m.project_id AND n.deleted_at IS NULL`, r.PathValue("projectId")).Scan(&out.ProjectID, &out.ContextID, &out.Revision)
	return out, missing(err)
}
func (m *Module) project(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		ContextID        string `json:"context_id"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if !uuid(in.ContextID) || !uuid(r.PathValue("projectId")) {
		return nil, fail(400, "invalid project or context id")
	}
	ctx := r.Context()
	if _, err := FenceWrite(ctx, tx, p, in.ExpectedRevision); err != nil {
		return nil, err
	}
	var before *string
	var id string
	if err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project' FOR NO KEY UPDATE OF n`, r.PathValue("projectId")).Scan(&id); err != nil {
		return nil, missing(err)
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM work_contexts WHERE id=$1 AND archived_at IS NULL`, in.ContextID).Scan(&id); err != nil {
		return nil, missing(err)
	}
	if err := tx.QueryRow(ctx, `SELECT (SELECT context_id::text FROM project_work_contexts WHERE project_id=$1)`, r.PathValue("projectId")).Scan(&before); err != nil {
		return nil, err
	}
	var saved string
	err := tx.QueryRow(ctx, `INSERT INTO project_work_contexts(tenant_id,project_id,context_id) VALUES($1,$2,$3) ON CONFLICT(tenant_id,project_id) DO UPDATE SET context_id=EXCLUDED.context_id RETURNING context_id::text`, p.TenantID, r.PathValue("projectId"), in.ContextID).Scan(&saved)
	if err != nil {
		return nil, err
	}
	after, err := bump(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := projectResult{r.PathValue("projectId"), saved, after.Revision}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "work_context.project_set", NodeID: &out.ProjectID, Before: before, After: out})
	return out, err
}
