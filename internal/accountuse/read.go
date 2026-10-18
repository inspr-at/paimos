// SPDX-License-Identifier: AGPL-3.0-only
package accountuse

import (
	"net/http"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Account struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Harness     string  `json:"harness"`
	Plan        *string `json:"plan"`
	BillingMode string  `json:"billing_mode"`
	OwnerID     *string `json:"owner_id"`
	// Awaiting: added while New accounts asked first, and no live context allows it yet (AEON-1173).
	Awaiting bool `json:"awaiting"`
}
type RunningOutside struct {
	RunID     string  `json:"run_id"`
	AccountID string  `json:"account_id"`
	NodeID    *string `json:"node_id"`
}

// HoldingFirst bounds the projects named in HoldingProjects.First.
const HoldingFirst = 20

type HoldingProject struct {
	ProjectID string `json:"project_id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
}

// HoldingProjects are the visible live projects waiting in Unassigned; at most
// HoldingFirst are named, newest first, and Count says how many wait in all.
type HoldingProjects struct {
	Count int64            `json:"count"`
	First []HoldingProject `json:"first"`
}
type Matrix struct {
	Rules                   Rules            `json:"rules"`
	Accounts                []Account        `json:"accounts"`
	Contexts                []WorkContext    `json:"contexts"`
	Cells                   []Cell           `json:"cells"`
	NextAccount             *string          `json:"next_account"`
	NextContext             *string          `json:"next_context"`
	RunningOutside          []RunningOutside `json:"running_outside"`
	RunningOutsideTruncated bool             `json:"running_outside_truncated"`
	HoldingProjects         HoldingProjects  `json:"holding_projects"`
}

func (m *Module) read(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	limit, err := page(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	for _, key := range []string{"after_account", "after_context"} {
		if q.Get(key) != "" && !uuid(q.Get(key)) {
			return nil, fail(400, "invalid matrix cursor")
		}
	}
	out := Matrix{Accounts: []Account{}, Contexts: []WorkContext{}, Cells: []Cell{}, RunningOutside: []RunningOutside{}, HoldingProjects: HoldingProjects{First: []HoldingProject{}}}
	out.Rules, err = ReadRules(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT a.id::text,a.label,a.harness,a.plan,a.billing_mode,a.owner_person_id::text,
        EXISTS(SELECT 1 FROM account_use_awaiting w WHERE w.account_id=a.id)
        AND NOT EXISTS(SELECT 1 FROM account_use_cells x JOIN work_contexts c ON c.tenant_id=x.tenant_id AND c.id=x.context_id WHERE x.account_id=a.id AND c.archived_at IS NULL)
        FROM agent_accounts a WHERE a.archived_at IS NULL AND ($1='' OR a.id>NULLIF($1,'')::uuid) ORDER BY a.id LIMIT $2`, q.Get("after_account"), limit+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Label, &a.Harness, &a.Plan, &a.BillingMode, &a.OwnerID, &a.Awaiting); err != nil {
			rows.Close()
			return nil, err
		}
		out.Accounts = append(out.Accounts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Accounts) > limit {
		out.Accounts = out.Accounts[:limit]
		out.NextAccount = &out.Accounts[limit-1].ID
	}
	rows, err = tx.Query(r.Context(), `SELECT `+contextColumns+` FROM work_contexts WHERE archived_at IS NULL
        AND ($1='' OR id>NULLIF($1,'')::uuid) ORDER BY id LIMIT $2`, q.Get("after_context"), limit+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		c, err := scanContext(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out.Contexts = append(out.Contexts, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Contexts) > limit {
		out.Contexts = out.Contexts[:limit]
		out.NextContext = &out.Contexts[limit-1].ID
	}
	accounts := make([]string, 0, len(out.Accounts))
	contexts := make([]string, 0, len(out.Contexts))
	for _, a := range out.Accounts {
		accounts = append(accounts, a.ID)
	}
	for _, c := range out.Contexts {
		contexts = append(contexts, c.ID)
	}
	rows, err = tx.Query(r.Context(), `SELECT account_id::text,context_id::text FROM account_use_cells
        WHERE account_id=ANY($1::uuid[]) AND context_id=ANY($2::uuid[]) ORDER BY account_id,context_id LIMIT 40001`, accounts, contexts)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		cell := Cell{Allowed: true}
		if err := rows.Scan(&cell.AccountID, &cell.ContextID); err != nil {
			rows.Close()
			return nil, err
		}
		out.Cells = append(out.Cells, cell)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(r.Context(), `SELECT id::text,account_id::text,coalesce(queue_node_id,work_order_id)::text FROM agent_runs
        WHERE account_id IS NOT NULL AND status IN ('starting','running') AND NOT aeon_account_use_allowed(account_id,id)
        ORDER BY id LIMIT 201`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var run RunningOutside
		if err := rows.Scan(&run.RunID, &run.AccountID, &run.NodeID); err != nil {
			rows.Close()
			return nil, err
		}
		out.RunningOutside = append(out.RunningOutside, run)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.RunningOutside) > 200 {
		out.RunningOutside = out.RunningOutside[:200]
		out.RunningOutsideTruncated = true
	}
	if err := readHolding(r, tx, &out.HoldingProjects); err != nil {
		return nil, err
	}
	return out, nil
}

// readHolding reads the projects waiting in Unassigned that this person can see
// (project visibility applies to both tables). The count is one aggregate; at
// most HoldingFirst rows are named.
func readHolding(r *http.Request, tx pgx.Tx, out *HoldingProjects) error {
	const waiting = ` FROM project_work_contexts m JOIN work_contexts c ON c.tenant_id=m.tenant_id AND c.id=m.context_id
        JOIN nodes n ON n.tenant_id=m.tenant_id AND n.id=m.project_id WHERE c.kind='holding' AND n.deleted_at IS NULL AND n.state<>'archived'`
	if err := tx.QueryRow(r.Context(), `SELECT count(*)`+waiting).Scan(&out.Count); err != nil {
		return err
	}
	if out.Count == 0 {
		return nil
	}
	rows, err := tx.Query(r.Context(), `SELECT n.id::text,n.key,n.title`+waiting+` ORDER BY n.created_at DESC,n.id LIMIT $1`, HoldingFirst)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p HoldingProject
		if err := rows.Scan(&p.ProjectID, &p.Key, &p.Name); err != nil {
			return err
		}
		out.First = append(out.First, p)
	}
	return rows.Err()
}
