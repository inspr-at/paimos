// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// One bounded aggregate for the loaded page, under the same read snapshot.
// No session identities or payloads leave this read; denied harness visibility
// leaves the aggregate unavailable rather than reporting a misleading zero.
func agentSummaries(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, releases []ReleaseView) error {
	if len(releases) == 0 {
		return nil
	}
	if len(releases) > 50 {
		return invalidInput("agent summary page exceeds 50 releases")
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: project}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return nil
		}
		return err
	}
	ids := make([]string, 0, len(releases))
	for _, r := range releases {
		ids = append(ids, r.ID)
	}
	rows, err := tx.Query(ctx, `WITH candidates AS MATERIALIZED (
 SELECT h.id,h.agent_principal_id,h.run_id,e.release_node_id
 FROM harness_sessions h JOIN nodes n ON n.tenant_id=h.tenant_id AND n.id=h.ticket_node_id AND n.deleted_at IS NULL
 JOIN `+Effective+` e ON e.tenant_id=n.tenant_id AND e.item_node_id=n.id AND e.project_node_id=$2
 WHERE h.tenant_id=$1 AND h.project_id=$2 AND h.stopped_at IS NULL AND e.release_node_id=ANY($3::uuid[])
 ORDER BY h.id LIMIT 5001
 ), counted AS (SELECT * FROM candidates ORDER BY id LIMIT 5000)
 SELECT c.release_node_id::text,count(DISTINCT c.agent_principal_id),count(DISTINCT c.agent_principal_id) FILTER(WHERE r.status='waiting'),(SELECT count(*)>5000 FROM candidates)
 FROM counted c LEFT JOIN agent_runs r ON r.tenant_id=$1 AND r.id=c.run_id GROUP BY c.release_node_id`, p.TenantID, project, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	type summary struct {
		agents, waiting int
		incomplete      bool
	}
	values := map[string]summary{}
	incomplete := false
	for rows.Next() {
		var id string
		var v summary
		if err = rows.Scan(&id, &v.agents, &v.waiting, &v.incomplete); err != nil {
			return err
		}
		values[id] = v
		incomplete = incomplete || v.incomplete
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range releases {
		v := values[releases[i].ID]
		releases[i].BuildSummary["agents"] = v.agents
		releases[i].BuildSummary["waiting"] = v.waiting
		releases[i].BuildSummary["agents_incomplete"] = incomplete
	}
	return nil
}
