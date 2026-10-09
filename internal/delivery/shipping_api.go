// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type ShipPage struct {
	Items    []ShipDecision `json:"items"`
	Settings ShipSettings   `json:"settings"`
	Next     *string        `json:"next_cursor"`
}
type shippingReader interface {
	Shipping(context.Context, string, string) (ShipFacts, error)
}
type preflightReader interface {
	Preflight(context.Context, string) (string, error)
}

func (m *Module) mountShipping(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/projects/{projectId}/delivery-shipping", m.listShipping},
		{"GET /api/projects/{projectId}/delivery-shipping/settings", m.shipSettings},
		{"PUT /api/projects/{projectId}/delivery-shipping/settings", m.shipSettings},
		{"POST /api/projects/{projectId}/delivery-shipping/claim", m.claimShipping},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			if r.Method != http.MethodGet {
				c := http.NewResponseController(w)
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				defer c.SetReadDeadline(time.Time{})
			}
			route.handler(w, r.WithContext(ctx))
		})
	}
}
func (m *Module) shipSettings(w http.ResponseWriter, r *http.Request) {
	write := r.Method == http.MethodPut
	permission := "delivery_ship.read"
	var in ShipSettings
	if write {
		permission = "delivery_ship.manage"
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if in.Revision < 0 || in.Mode != "off" && in.Mode != "shadow" {
			respondError(w, fail(400, "invalid shipping settings"))
			return
		}
	}
	m.reviewRequest(w, r, permission, write, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := shipSettingsTx(r.Context(), tx, project)
		if err != nil || !write {
			return before, 200, err
		}
		if before.Revision != in.Revision {
			return nil, 200, fail(409, "shipping settings changed; reload first")
		}
		in.Revision++
		if err = saveShipSettingsTx(r.Context(), tx, p.TenantID, project, in); err != nil {
			return nil, 200, err
		}
		return in, 200, appendShipChange(r.Context(), tx, p, "settings", shipSnapshot{Project: project, Settings: &before}, shipSnapshot{Project: project, Settings: &in}, m.now())
	})
}
func (m *Module) listShipping(w http.ResponseWriter, r *http.Request) {
	limit, after := 50, int64(0)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if len(raw) > 3 || err != nil || n < 1 || n > 100 {
			respondError(w, fail(400, "invalid shipping limit"))
			return
		}
		limit = n
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if len(raw) > 19 || err != nil || n < 0 {
			respondError(w, fail(400, "invalid shipping cursor"))
			return
		}
		after = n
	}
	m.reviewRequest(w, r, "delivery_ship.read", false, func(tx pgx.Tx, _ tenant.Principal, project string) (any, int, error) {
		out := ShipPage{Items: []ShipDecision{}}
		var err error
		if out.Settings, err = shipSettingsTx(r.Context(), tx, project); err != nil {
			return nil, 200, err
		}
		rows, err := tx.Query(r.Context(), `SELECT d.snapshot FROM delivery_ship_decisions d JOIN delivery_work_rounds v ON v.tenant_id=d.tenant_id AND v.id=d.source_round_id AND v.project_id=d.project_id JOIN nodes n ON n.tenant_id=v.tenant_id AND n.id=v.ticket_node_id AND n.project_id=v.project_id AND n.deleted_at IS NULL WHERE d.project_id=$1 AND d.position>$2 ORDER BY d.position LIMIT $3`, project, after, limit+1)
		if err != nil {
			return nil, 200, err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanShip(rows)
			if err != nil {
				return nil, 200, err
			}
			out.Items = append(out.Items, item)
		}
		if err = rows.Err(); err != nil {
			return nil, 200, err
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			cursor := strconv.FormatInt(out.Items[limit-1].Position, 10)
			out.Next = &cursor
		}
		return out, 200, nil
	})
}

func shipSourceTx(ctx context.Context, tx pgx.Tx, project, id string) (Round, error) {
	source, err := loadRoundTx(ctx, tx, project, id)
	if err != nil {
		return source, err
	}
	return source, currentRoundTargetTx(ctx, tx, source)
}
func (m *Module) claimShipping(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := strings.ToLower(r.PathValue("projectId"))
	var in ShipInput
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	in.Request = strings.ToLower(in.Request)
	in.Source = strings.ToLower(in.Source)
	if !workorders.UUID(project) {
		respondError(w, fail(400, "invalid shipping project"))
		return
	}
	if err := validateShipInput(in); err != nil {
		respondError(w, err)
		return
	}
	// Check authority and the target before the external read. This is only
	// preflight: authority and all current bindings are checked again in write.
	var source Round
	var settings ShipSettings
	var retry *ShipDecision
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := reviewProjectTx(r.Context(), tx, p, project, "delivery_ship.claim", false); err != nil {
			return err
		}
		var err error
		if source, err = scanRound(tx.QueryRow(r.Context(), `SELECT snapshot FROM delivery_work_rounds WHERE project_id=$1 AND id=$2`, project, in.Source)); err != nil {
			return err
		}
		if err = currentRoundTargetTx(r.Context(), tx, source); err != nil {
			return err
		}
		if retry, err = loadShipRetryTx(r.Context(), tx, p, project, in); err != nil {
			return err
		}
		settings, err = shipSettingsTx(r.Context(), tx, project)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	facts := emptyShipFacts()
	readReason := ""
	if retry == nil && settings.Mode == "shadow" {
		reader, available := m.github.(shippingReader)
		if p.TenantID != m.config.TenantID || !reviewgate.ValidRepository(m.config.Repository) || !available {
			readReason = "github_unavailable"
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			facts, err = reader.Shipping(ctx, "work/"+source.Slug, in.Head)
			cancel()
			if err != nil {
				facts = emptyShipFacts()
				readReason = "github_unavailable"
			} else {
				preflight, available := m.github.(preflightReader)
				readReason = "preflight_lookup_failed"
				if available {
					ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
					reason, lookupErr := preflight.Preflight(ctx, in.Head)
					cancel()
					if lookupErr == nil {
						readReason = reason
					}
				}
			}
		}
	}
	var out ShipDecision
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := reviewProjectTx(r.Context(), tx, p, project, "delivery_ship.claim", true); err != nil {
			return err
		}
		current, err := shipSourceTx(r.Context(), tx, project, in.Source)
		if err != nil {
			return err
		}
		old, err := loadShipRetryTx(r.Context(), tx, p, project, in)
		if err != nil {
			return err
		}
		if old != nil {
			out = *old
			return nil
		}
		if current.Slug != source.Slug || current.Ticket != source.Ticket || current.Revision != source.Revision {
			return fail(409, "shipping source changed during observation")
		}
		s, err := shipSettingsTx(r.Context(), tx, project)
		if err != nil {
			return err
		}
		out = ShipDecision{ShipInput: in, Project: project, Claimant: p.ID, Mode: s.Mode, Action: "wait", Reason: "shipping_off", Repository: m.config.Repository, Branch: "work/" + current.Slug, Facts: facts, Updated: m.now()}
		if s.Mode == "shadow" {
			if settings.Mode != s.Mode || settings.Revision != s.Revision {
				out.Reason = "shipping_settings_changed"
			} else {
				if err = m.decideShippingTx(r.Context(), tx, p, &out, current, readReason); err != nil {
					return err
				}
			}
		}
		out.Claimed = slices.Contains([]string{"open_pr", "update_pr", "enqueue", "rerun_failed", "fix", "merge", "land"}, out.Action)
		if key := shipActionKey(out); key != nil {
			var taken bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM delivery_ship_decisions WHERE project_id=$1 AND action_key=$2)`, project, key).Scan(&taken); err != nil {
				return err
			}
			if taken {
				out.Action, out.Reason, out.Claimed, out.Round = "wait", "action_already_claimed", false, nil
			}
		}
		if in.ScriptAction != nil {
			agree := out.Action == *in.ScriptAction
			out.Agreement = &agree
		}
		if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(position),0)+1 FROM delivery_ship_decisions WHERE project_id=$1`, project).Scan(&out.Position); err != nil {
			return err
		}
		if err = saveShipTx(r.Context(), tx, p.TenantID, out); err != nil {
			return err
		}
		return appendShipChange(r.Context(), tx, p, "decision", nil, shipSnapshot{Project: project, Decision: &out}, out.Updated)
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *Module) decideShippingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, out *ShipDecision, source Round, readReason string) error {
	q, err := queueSettingsTx(ctx, tx, out.Project)
	if err != nil {
		return err
	}
	reason := ""
	inset := !q.Freeze
	for _, prefix := range q.ReleaseSet {
		if strings.HasPrefix(source.Slug, prefix) {
			inset = true
			break
		}
	}
	switch {
	case source.State != "done":
		reason = "source_not_done"
	case source.Hold != nil:
		reason = "round_hold"
	case !inset:
		reason = "release_freeze"
	case slices.Contains(q.HeldSlugs, source.Slug):
		reason = "slug_hold"
	case out.Facts.PR != nil && slices.Contains(q.HeldPRs, *out.Facts.PR) || source.PR != nil && slices.Contains(q.HeldPRs, *source.PR):
		reason = "pull_request_hold"
	}
	var active, held, latest bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_work_rounds WHERE project_id=$1 AND slug=$2 AND state IN ('queued','claimed','running')),
 EXISTS(SELECT 1 FROM delivery_items WHERE project_id=$1 AND branch=$3 AND state='held'),
 NOT EXISTS(SELECT 1 FROM delivery_work_rounds WHERE project_id=$1 AND slug=$2 AND position>$4 AND state<>'parked')`, out.Project, source.Slug, out.Branch, source.Position).Scan(&active, &held, &latest); err != nil {
		return err
	}
	if reason == "" {
		switch {
		case active:
			reason = "round_pending"
		case held:
			reason = "delivery_hold"
		case !latest:
			reason = "source_superseded"
		}
	}
	if reason != "" {
		out.Action, out.Reason = "held", reason
		return nil
	}
	// Shadow gate observations predict script choices, but cannot satisfy the
	// verified platform gate or grant shipping authority. Current policy and
	// completed author bindings are still checked for the selected observation.
	gate, err := scanReview(tx.QueryRow(ctx, `SELECT snapshot FROM delivery_review_rounds WHERE project_id=$1 AND slug=$2 ORDER BY position DESC LIMIT 1`, out.Project, source.Slug))
	if errors.Is(err, pgx.ErrNoRows) {
		out.Reason = "gate_required"
		return nil
	}
	if err != nil {
		return err
	}
	if gate.SourceRound != source.ID || gate.Ticket != source.Ticket || gate.Repository != out.Repository || gate.Head != out.Head || gate.State != "completed" || gate.Action != "ready_to_ship" || gate.EffectiveVerdict != "ok" || gate.Route == nil || gate.VerdictInput == nil || gate.VerdictInput.Head != out.Head || gate.VerdictInput.Profile != gate.Route.Profile {
		out.Reason = "current_head_gate_required"
		return nil
	}
	family, err := authorFamilyTx(ctx, tx, gate)
	if err != nil {
		return err
	}
	policy, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, &out.Project)
	if err != nil {
		return err
	}
	if allowed, _ := policy.Effective.Decision(family, gate.Route.Family); !allowed || family != gate.AuthorFamily {
		out.Action, out.Reason = "held", "gate_policy_changed"
		return nil
	}
	var harness, model, reviewerFamily string
	err = tx.QueryRow(ctx, `SELECT harness,model,family FROM model_profiles WHERE id=$1 AND enabled`, gate.Route.Profile).Scan(&harness, &model, &reviewerFamily)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (harness != gate.Route.Harness || model != gate.Route.Model || reviewerFamily != gate.Route.Family) {
		out.Action, out.Reason = "held", "gate_route_changed"
		return nil
	}
	if err != nil {
		return err
	}
	if readReason != "" {
		out.Reason = readReason
		return nil
	}
	settings, err := settingsTx(ctx, tx, &out.Project)
	if err != nil {
		return err
	}
	quarantined := false
	if out.Facts.PR != nil {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_queue_failures WHERE repository=$1 AND pull_request=$2 AND head_sha=$3 AND kind='required_failure')`, out.Repository, out.Facts.PR, out.Head).Scan(&quarantined); err != nil {
			return err
		}
	}
	proposeShip(out, source, settings, quarantined)
	if out.Action == "merge" || out.Action == "fix" {
		round := source.RoundInput
		round.Kind = out.Action
		round.PR = out.Facts.PR
		round.Estimate = 40
		if round.Kind == "fix" {
			round.Estimate = 60
		}
		if err = tx.QueryRow(ctx, `SELECT coalesce(max(round_number),0)+1 FROM delivery_work_rounds WHERE project_id=$1 AND ticket_node_id=$2 AND kind=$3`, source.Project, source.Ticket, round.Kind).Scan(&round.Number); err != nil {
			return err
		}
		if round.Number > 100 {
			out.Action, out.Reason = "held", "round_limit"
			return nil
		}
		round.Base = out.Facts.Base
		round.Brief = "delivery/" + source.Slug + "/" + out.Action + "-" + strconv.Itoa(round.Number)
		out.Round = &round
	}
	return nil
}
