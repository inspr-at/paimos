// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type runPage struct {
	Items      []Run   `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type runCursor struct {
	Fingerprint string    `json:"query"`
	At          time.Time `json:"at"`
	ID          string    `json:"id"`
}

func (m *module) list(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	q := r.URL.Query()
	limit, err := workorders.Limit(r)
	if err != nil {
		return nil, err
	}
	session, agent, order := q.Get("session"), q.Get("agent"), q.Get("work_order")
	for _, id := range []string{session, agent, order} {
		if id != "" && !workorders.UUID(id) {
			return nil, workorders.Fail(400, "invalid filter id")
		}
	}
	if p.Kind == tenant.Agent {
		if agent != "" && agent != p.ID {
			return nil, workorders.Fail(403, "run belongs to another agent")
		}
		agent = p.ID
	}
	filters, _ := json.Marshal([]string{p.TenantID, p.ID, session, agent, order})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(filters))
	var cursor runCursor
	if raw := q.Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if len(raw) > 2048 || e != nil || json.Unmarshal(b, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.At.IsZero() || !workorders.UUID(cursor.ID) {
			return nil, workorders.Fail(400, "invalid cursor")
		}
	}
	var at any
	if !cursor.At.IsZero() {
		at = cursor.At
	}
	rows, err := tx.Query(r.Context(), `SELECT `+columns+` FROM agent_runs
 WHERE ($1::uuid IS NULL OR EXISTS(SELECT 1 FROM harness_sessions s WHERE s.id=$1 AND s.run_id=agent_runs.id))
 AND ($2::uuid IS NULL OR agent_principal_id=$2) AND ($3::uuid IS NULL OR work_order_id=$3)
 AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5::uuid))
 ORDER BY created_at DESC,id DESC LIMIT $6`, optional(session), optional(agent), optional(order), at, optional(cursor.ID), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := runPage{Items: []Run{}}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		b, _ := json.Marshal(runCursor{fingerprint, last.CreatedAt, last.ID})
		next := base64.RawURLEncoding.EncodeToString(b)
		out.NextCursor = &next
	}
	rows.Close()
	waitIDs := make([]string, 0, len(out.Items))
	for i := range out.Items {
		if out.Items[i].Status == "failed" {
			out.Items[i].Wait, err = vendorWait(r.Context(), tx, out.Items[i])
			if err != nil {
				return nil, err
			}
		}
		if out.Items[i].Status != "queued" || out.Items[i].Purpose != "managed" {
			continue
		}
		if out.Items[i].QueueNodeID == nil || out.Items[i].QueueTargetAgentID != nil || out.Items[i].QueueRoutedAt != nil {
			waitIDs = append(waitIDs, out.Items[i].ID)
		}
	}
	waits, err := agentaccounts.WaitForRuns(r.Context(), tx, waitIDs)
	if err != nil {
		return nil, err
	}
	for i := range out.Items {
		if wait, ok := waits[out.Items[i].ID]; ok {
			out.Items[i].Wait = wait
		}
	}
	return out, nil
}
func optional(s string) any {
	if s == "" {
		return nil
	}
	return s
}
