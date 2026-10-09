// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type mergeBackfillInput struct {
	Pulls   []int64 `json:"pull_requests"`
	Apply   bool    `json:"apply"`
	Preview int64   `json:"preview_event_id"`
	Target  string  `json:"target"`
	Release string  `json:"release_id"`
}

type mergeBackfillResult struct {
	Preview   int64        `json:"preview_event_id"`
	Applied   bool         `json:"applied"`
	Items     []mergeEntry `json:"items"`
	NotMerged []int64      `json:"not_merged"`
}

type mergePreview struct {
	Input      mergeBackfillInput  `json:"input"`
	Project    string              `json:"project_id"`
	Repository string              `json:"repository"`
	Result     mergeBackfillResult `json:"result"`
}

// Historical delivery is proved by frozen published membership, not a tag
// date alone or an operator's assertion. This only reads the existing release
// compatibility store; no journey/stage feature is extended.
func mergePublishedTx(ctx context.Context, tx pgx.Tx, project, release string, e mergeEntry) (bool, error) {
	var published bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases r JOIN nodes n ON n.id=r.release_node_id
 WHERE r.project_node_id=$1 AND r.release_node_id=$2 AND r.state IN ('released','superseded') AND n.deleted_at IS NULL
 AND r.released_at >= $3 AND (
 EXISTS(SELECT 1 FROM journey_release_note_snapshots s CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'tickets') t
 WHERE s.release_node_id=r.release_node_id AND s.project_node_id=r.project_node_id AND t->>'id'=$4)
 OR EXISTS(SELECT 1 FROM release_manifest_note_snapshots s CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'tickets') t
 WHERE s.project_node_id=r.project_node_id AND s.version=r.version AND t->>'id'=$4)))`, project, release, e.MergedAt, e.Ticket).Scan(&published)
	return published, err
}

func (m *Module) mergeBackfill(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	var in mergeBackfillInput
	if err := decodeBounded(w, r, 16<<10, &in); err != nil {
		respondError(w, err)
		return
	}
	if in.Target == "" {
		in.Target = "done"
	}
	if len(in.Pulls) < 1 || len(in.Pulls) > 100 || !slices.Contains([]string{"done", "delivered"}, in.Target) ||
		in.Target == "delivered" && !workorders.UUID(in.Release) || in.Target == "done" && in.Release != "" ||
		in.Apply && in.Preview < 1 || !in.Apply && in.Preview != 0 {
		respondError(w, fail(400, "1 to 100 PRs, target and a preview before apply are required"))
		return
	}
	slices.Sort(in.Pulls)
	for i, n := range in.Pulls {
		if n < 1 || i > 0 && n == in.Pulls[i-1] {
			respondError(w, fail(400, "invalid or duplicate PR"))
			return
		}
	}
	if p.TenantID != m.config.TenantID {
		respondError(w, fail(404, "merge source unavailable"))
		return
	}
	reader, ok := m.github.(MergeReader)
	if !ok {
		respondError(w, fail(503, "merge source unavailable"))
		return
	}
	var prior mergePreview
	var already *mergeBackfillResult
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "delivery.manage", scope(&project)); err != nil {
			return err
		}
		if !in.Apply {
			return nil
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT after FROM events WHERE id=$1 AND type='delivery.merge_backfill_preview' AND actor_principal_id=$2 AND node_id=$3`, in.Preview, p.ID, project).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(409, "matching preview required")
		}
		if err != nil {
			return err
		}
		if json.Unmarshal(raw, &prior) != nil {
			return errRead
		}
		request := in
		request.Apply, request.Preview = false, 0
		if prior.Project != project || prior.Repository != m.config.Repository || digestJSON(prior.Input) != digestJSON(request) {
			return fail(409, "preview request changed")
		}
		for _, e := range prior.Result.Items {
			var currentProject *string
			if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL`, e.Ticket).Scan(&currentProject); err != nil {
				return err
			}
			if currentProject == nil || *currentProject != project {
				return fail(409, "ticket project changed")
			}
			if err := authz.RequireTx(ctx, tx, p, "nodes.write", scope(currentProject)); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `SELECT after FROM events WHERE type='delivery.merge_backfill_applied' AND node_id=$1 AND metadata->>'preview_event_id'=$2 ORDER BY id LIMIT 1`, project, previewID(in.Preview)).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var result mergeBackfillResult
		if json.Unmarshal(raw, &result) != nil {
			return errRead
		}
		already = &result
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	if already != nil {
		httpapi.WriteJSON(w, 200, *already)
		return
	}
	facts := []MergeFact{}
	out := mergeBackfillResult{Items: []mergeEntry{}, NotMerged: []int64{}}
	for _, n := range in.Pulls {
		f, err := reader.MergedPull(ctx, n)
		if err != nil {
			respondError(w, fail(502, "merge inventory unavailable"))
			return
		}
		if f == nil {
			out.NotMerged = append(out.NotMerged, n)
		} else {
			facts = append(facts, *f)
		}
	}
	open, err := m.github.OpenPulls(ctx)
	if err != nil || len(open) > 2000 {
		respondError(w, fail(502, "complete open PR inventory unavailable"))
		return
	}
	openKeys := map[string]bool{}
	for _, pull := range open {
		keys, err := mergeKeys(pull.Title, pull.Branch)
		if err != nil || !pull.Open {
			respondError(w, fail(502, "open PR inventory invalid"))
			return
		}
		for _, key := range keys {
			openKeys[key] = true
		}
	}
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(ctx, tx); err != nil {
			return err
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "delivery.manage", scope(&project)); err != nil {
			return err
		}
		entries, err := mergeEntriesTx(ctx, tx, facts, project)
		if err != nil {
			return err
		}
		if err := lockMergeEventParentsTx(ctx, tx, p, &project); err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			if err := authz.RequireTx(ctx, tx, p, "nodes.write", scope(e.Project)); err != nil {
				return err
			}
			e.To, e.Result = in.Target, "ready"
			e.Reason, err = mergeGateTx(ctx, tx, *e)
			if err != nil {
				return err
			}
			if openKeys[strings.ToUpper(e.Key)] {
				e.Reason = "ticket has an open PR"
			}
			if e.To == "delivered" {
				published, err := mergePublishedTx(ctx, tx, project, in.Release, *e)
				if err != nil {
					return err
				}
				if !published {
					e.Reason = "no frozen published release membership after this merge"
				}
			}
			if e.Reason != "" {
				e.Result = "refused"
			} else if ticketbenefits.Completed(e.category) && !(e.category == "done" && e.To == "delivered") {
				e.Result = "already_complete"
			}
		}
		out.Items = entries
		if !in.Apply {
			preview := mergePreview{Input: in, Project: project, Repository: m.config.Repository, Result: out}
			e, err := events.Append(ctx, tx, p, events.Change{Type: "delivery.merge_backfill_preview", NodeID: &project, After: preview})
			out.Preview = e.ID
			return err
		}
		// Another request may have applied this preview during the network read.
		var appliedRaw []byte
		err = tx.QueryRow(ctx, `SELECT after FROM events WHERE type='delivery.merge_backfill_applied' AND node_id=$1 AND metadata->>'preview_event_id'=$2 ORDER BY id LIMIT 1`, project, previewID(in.Preview)).Scan(&appliedRaw)
		if err == nil {
			return json.Unmarshal(appliedRaw, &out)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if digestJSON(out) != digestJSON(prior.Result) {
			return fail(409, "preview is stale; run dry-run again")
		}
		changes := []events.Change{}
		for i := range out.Items {
			c, err := prepareMergeTx(ctx, tx, p.TenantID, m.config.Repository, &out.Items[i])
			if err != nil {
				return err
			}
			if c != nil {
				changes = append(changes, *c)
			}
		}
		out.Applied, out.Preview = true, in.Preview
		if err := flushMergeTx(ctx, tx, p, m.config.Repository, out.Items, changes); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]string{"preview_event_id": previewID(in.Preview)})
		_, err = events.Append(ctx, tx, p, events.Change{Type: "delivery.merge_backfill_applied", NodeID: &project, After: out, Metadata: meta})
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func digestJSON(v any) string   { raw, _ := json.Marshal(v); return digest(raw) }
func previewID(id int64) string { return strconv.FormatInt(id, 10) }
