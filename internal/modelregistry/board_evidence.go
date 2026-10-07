// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

type boardEvidenceItem struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	ForPerson  *string         `json:"for_person"`
	Preference json.RawMessage `json:"preference"`
	At         time.Time       `json:"at"`
	Source     string          `json:"source"`
}

func (m *Module) boardEvidence(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	kind, cursor := r.URL.Query().Get("kind"), r.URL.Query().Get("cursor")
	limit := 50
	if len(kind) > 48 || cursor != "" && !uuidRE.MatchString(cursor) {
		writePreferenceError(w, prefFail(400, "invalid_evidence_filter"))
		return
	}
	if r.URL.Query().Has("limit") {
		limit, err = strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writePreferenceError(w, prefFail(400, "invalid_limit"))
			return
		}
	}
	out := struct {
		Items      []boardEvidenceItem `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}{Items: []boardEvidenceItem{}}
	err = m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		current, err := currentModelReader(r, tx, p)
		if err != nil {
			return err
		}
		if err := readableProject(ctx, tx, current, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,kind,person,preference,at,source FROM (
   SELECT r.id,coalesce(r.trace->'work_placement'->>'kind','other') AS kind,r.trace->'work_placement'->>'person_id' AS person,
    coalesce(r.trace->'work_placement','{}'::jsonb) AS preference,r.created_at AS at,'run' AS source
   FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
   WHERE ($1::uuid IS NULL OR n.project_id=$1::uuid) AND ($2='' OR r.trace->'work_placement'->>'kind'=$2) AND ($3::uuid IS NULL OR r.id>$3::uuid)
   UNION ALL
   SELECT h.id,coalesce(h.work_placement->>'kind','other'),h.work_placement->>'person_id',coalesce(h.work_placement,'{}'::jsonb),h.created_at,'harness'
   FROM harness_sessions h JOIN nodes n ON n.tenant_id=h.tenant_id AND n.id=h.project_id
   WHERE h.run_id IS NULL AND ($1::uuid IS NULL OR h.project_id=$1::uuid) AND ($2='' OR h.work_placement->>'kind'=$2) AND ($3::uuid IS NULL OR h.id>$3::uuid)
  ) evidence ORDER BY id LIMIT $4`, optionalUUID(project), kind, optionalUUID(cursor), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item boardEvidenceItem
			if err := rows.Scan(&item.ID, &item.Kind, &item.ForPerson, &item.Preference, &item.At, &item.Source); err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			next := out.Items[limit-1].ID
			out.NextCursor = &next
			out.Items = out.Items[:limit]
		}
		return nil
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) boardCoverage(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	err := m.readSnapshot(r.Context(), p.TenantID, func(tx pgx.Tx) error { _, err := currentModelReader(r, tx, p); return err })
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	type consumer struct {
		Consumer   string `json:"consumer"`
		ReadsBoard bool   `json:"reads_board"`
		Agreement  string `json:"agreement"`
	}
	httpapi.WriteJSON(w, 200, struct {
		Consumers []consumer `json:"consumers"`
	}{[]consumer{{"managed_build", true, "server-authoritative"}, {"managed_review", true, "server-authoritative"}, {"queue", true, "server-authoritative"}, {"lead_harness", false, "observed; Engine Wave 2 pending"}}})
}
