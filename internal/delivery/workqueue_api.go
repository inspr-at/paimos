// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type RoundPage struct {
	Items    []Round       `json:"items"`
	Settings QueueSettings `json:"settings"`
	Next     *string       `json:"next_cursor"`
}

// queueRequest bounds operation time before touching the database. The final
// mutation transaction owns both the tenant fence and the permission check.
func (m *Module) queueRequest(w http.ResponseWriter, r *http.Request, permission string, write bool, fn func(pgx.Tx, tenant.Principal, string) (any, int, error)) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.PathValue("projectId")
	if !workorders.UUID(project) {
		respondError(w, fail(400, "invalid queue project"))
		return
	}
	var out any
	status := 200
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if write {
			if err := db.LockTenant(r.Context(), tx, p.TenantID); err != nil {
				return err
			}
		}
		if _, err := projectSettings(r.Context(), tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		out, status, err = fn(tx, p, project)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, status, out)
}

func (m *Module) listRounds(w http.ResponseWriter, r *http.Request) {
	limit := 50
	var after int64
	query := r.URL.Query()
	ticket := query.Get("ticket")
	if ticket != "" && !workorders.UUID(ticket) {
		respondError(w, fail(400, "invalid queue ticket"))
		return
	}
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			respondError(w, fail(400, "invalid queue limit"))
			return
		}
		limit = n
	}
	if raw := query.Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if len(raw) > 19 || err != nil || n < 0 {
			respondError(w, fail(400, "invalid queue cursor"))
			return
		}
		after = n
	}
	m.queueRequest(w, r, "delivery_queue.read", false, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		out := RoundPage{Items: []Round{}}
		var err error
		out.Settings, err = queueSettingsTx(r.Context(), tx, project)
		if err != nil {
			return out, 200, err
		}
		rows, err := tx.Query(r.Context(), `SELECT q.snapshot FROM delivery_work_rounds q JOIN nodes n ON n.id=q.ticket_node_id AND n.project_id=q.project_id AND n.deleted_at IS NULL
		 WHERE q.project_id=$1 AND q.position>$2 AND ($3::uuid IS NULL OR q.ticket_node_id=$3) ORDER BY q.position LIMIT $4`, project, after, nullable(ticket), limit+1)
		if err != nil {
			return out, 200, err
		}
		defer rows.Close()
		for rows.Next() {
			round, err := scanRound(rows)
			if err != nil {
				return out, 200, err
			}
			out.Items = append(out.Items, round)
		}
		if err = rows.Err(); err != nil {
			return out, 200, err
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			cursor := strconv.FormatInt(out.Items[limit-1].Position, 10)
			out.Next = &cursor
		}
		return out, 200, nil
	})
}
func (m *Module) enqueueRound(w http.ResponseWriter, r *http.Request) {
	var in RoundInput
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	if err := validateRound(in); err != nil {
		respondError(w, err)
		return
	}
	m.queueRequest(w, r, "delivery_queue.manage", true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		out := Round{RoundInput: in, Project: project, State: "queued", Revision: 1, Updated: m.now(), Reason: "queued"}
		if err := currentRoundTargetTx(r.Context(), tx, out); err != nil {
			return out, 200, err
		}
		if err := tx.QueryRow(r.Context(), `SELECT key FROM nodes WHERE id=$1`, in.Ticket).Scan(&out.Key); err != nil {
			return out, 200, err
		}
		before, err := scanRound(tx.QueryRow(r.Context(), `SELECT snapshot FROM delivery_work_rounds WHERE project_id=$1 AND ticket_node_id=$2 AND kind=$3 AND round_number=$4 FOR NO KEY UPDATE`, project, in.Ticket, in.Kind, in.Number))
		if err == nil {
			if !reflect.DeepEqual(in, before.RoundInput) {
				return out, 200, fail(409, "round identity already has different inputs")
			}
			return before, 200, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, 200, err
		}
		out.ID = stableID(p.TenantID, project, "work-round/"+in.Ticket+"/"+in.Kind+"/"+strconv.Itoa(in.Number))
		out.Position, err = nextPositionTx(r.Context(), tx, project)
		if err != nil {
			return out, 200, err
		}
		if err = saveRoundTx(r.Context(), tx, p.TenantID, out); err != nil {
			return out, 200, err
		}
		err = appendQueueChanges(r.Context(), tx, p, []events.Change{queueChange(project, "enqueued", nil, queueSnapshot{Project: project, Round: &out}, out.Updated)})
		return out, 201, err
	})
}
func (m *Module) workQueueSettings(w http.ResponseWriter, r *http.Request) {
	write := r.Method == http.MethodPut
	permission := "delivery_queue.read"
	var in QueueSettings
	if write {
		permission = "delivery_queue.manage"
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if err := validateQueueSettings(&in); err != nil {
			respondError(w, err)
			return
		}
	}
	m.queueRequest(w, r, permission, write, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := queueSettingsTx(r.Context(), tx, project)
		if err != nil || !write {
			return before, 200, err
		}
		if before.Revision != in.Revision {
			return before, 200, fail(409, "queue settings changed; reload first")
		}
		in.Revision++
		if err = saveQueueSettingsTx(r.Context(), tx, p.TenantID, project, in); err != nil {
			return before, 200, err
		}
		err = appendQueueChanges(r.Context(), tx, p, []events.Change{queueChange(project, "settings", queueSnapshot{Project: project, Settings: &before}, queueSnapshot{Project: project, Settings: &in}, m.now())})
		return in, 200, err
	})
}
func (m *Module) claimRound(w http.ResponseWriter, r *http.Request) {
	var in ClaimInput
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	if !workorders.UUID(in.Request) || in.ScriptRound != nil && !workorders.UUID(*in.ScriptRound) {
		respondError(w, fail(400, "invalid queue claim"))
		return
	}
	m.queueRequest(w, r, "delivery_queue.claim", true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		out, err := m.claimQueueTx(r.Context(), tx, p, project, in)
		return out, 200, err
	})
}
func (m *Module) changeRound(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("roundId")
	if !workorders.UUID(id) {
		respondError(w, fail(400, "invalid queue round"))
		return
	}
	var in struct {
		Revision int64           `json:"revision"`
		State    string          `json:"state"`
		Hold     json.RawMessage `json:"hold_reason"`
	}
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	progress := r.Method == http.MethodPost
	permission := "delivery_queue.manage"
	if progress {
		permission = "delivery_queue.claim"
	}
	if in.Revision < 1 || progress && (len(in.Hold) > 0 || in.State != "running" && in.State != "done") || !progress && (in.State != "" && in.State != "queued" && in.State != "parked" || in.State == "" && len(in.Hold) == 0) {
		respondError(w, fail(400, "invalid round transition"))
		return
	}
	var hold *string
	if len(in.Hold) > 0 && string(in.Hold) != "null" {
		var text string
		if err := json.Unmarshal(in.Hold, &text); err != nil || strings.TrimSpace(text) == "" || len(text) > 1000 {
			respondError(w, fail(400, "invalid round hold"))
			return
		}
		text = strings.TrimSpace(text)
		hold = &text
	}
	m.queueRequest(w, r, permission, true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := loadRoundTx(r.Context(), tx, project, id)
		if err != nil {
			return nil, 200, err
		}
		if err = currentRoundTargetTx(r.Context(), tx, before); err != nil {
			return nil, 200, err
		}
		if before.Revision != in.Revision {
			return nil, 200, fail(409, "round changed; reload first")
		}
		out := before
		if progress {
			if before.Claimant == nil || *before.Claimant != p.ID {
				return nil, 200, authz.ErrForbidden
			}
			if before.State == in.State {
				return before, 200, nil
			}
			if !(before.State == "claimed" && in.State == "running" || before.State == "running" && in.State == "done") {
				return nil, 200, fail(409, "invalid round progress")
			}
			s, err := queueSettingsTx(r.Context(), tx, project)
			if err != nil {
				return nil, 200, err
			}
			if in.State == "running" && (s.Mode != "shadow" || before.Hold != nil || slicesHeld(s, before)) {
				return nil, 200, fail(409, "round is held or shadow queue is off")
			}
			out.State = in.State
			out.Reason = "shadow_" + in.State
		} else {
			if len(in.Hold) > 0 {
				if before.State == "done" {
					return nil, 200, fail(409, "done round is terminal")
				}
				out.Hold = hold
			}
			if in.State != "" && in.State != before.State {
				if !(before.State == "queued" && in.State == "parked" || before.State == "parked" && in.State == "queued") {
					return nil, 200, fail(409, "active or done round cannot be reassigned")
				}
				out.State = in.State
				out.Position, err = nextPositionTx(r.Context(), tx, project)
				if err != nil {
					return nil, 200, err
				}
			}
			if reflect.DeepEqual(out, before) {
				return before, 200, nil
			}
			out.Reason = "managed"
		}
		out.Revision++
		out.Updated = m.now()
		if err = saveRoundTx(r.Context(), tx, p.TenantID, out); err != nil {
			return nil, 200, err
		}
		err = appendQueueChanges(r.Context(), tx, p, []events.Change{queueChange(project, "transition", queueSnapshot{Project: project, Round: &before}, queueSnapshot{Project: project, Round: &out}, out.Updated)})
		return out, 200, err
	})
}
func slicesHeld(s QueueSettings, r Round) bool {
	for _, slug := range s.HeldSlugs {
		if slug == r.Slug {
			return true
		}
	}
	for _, pr := range s.HeldPRs {
		if r.PR != nil && pr == *r.PR {
			return true
		}
	}
	if s.Freeze {
		for _, prefix := range s.ReleaseSet {
			if strings.HasPrefix(r.Slug, prefix) {
				return false
			}
		}
		return true
	}
	return false
}
