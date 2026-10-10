// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type ReviewPage struct {
	Items    []ReviewRound  `json:"items"`
	Settings ReviewSettings `json:"settings"`
	Next     *string        `json:"next_cursor"`
}

func (m *Module) mountReviews(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/projects/{projectId}/delivery-reviews", m.listReviews},
		{"POST /api/projects/{projectId}/delivery-reviews", m.enqueueReview},
		{"GET /api/projects/{projectId}/delivery-reviews/settings", m.reviewSettings},
		{"PUT /api/projects/{projectId}/delivery-reviews/settings", m.reviewSettings},
		{"POST /api/projects/{projectId}/delivery-reviews/{reviewId}/claim", m.claimReview},
		{"POST /api/projects/{projectId}/delivery-reviews/{reviewId}/verdict", m.reviewVerdict},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if r.Method != http.MethodGet {
				controller := http.NewResponseController(w)
				_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
				defer controller.SetReadDeadline(time.Time{})
			}
			route.handler(w, r.WithContext(ctx))
		})
	}
}

// Tree/tenant admission serializes access changes, ticket moves and policy
// mutations before any resource rows. The final transaction checks authority.
func reviewProjectTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, permission string, write bool) error {
	if write {
		if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
			return err
		}
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT n.state FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, project).Scan(&state); err != nil {
		return err
	}
	if write && state != "active" {
		return fail(409, "review project is unavailable")
	}
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
}
func (m *Module) reviewRequest(w http.ResponseWriter, r *http.Request, permission string, write bool, fn func(pgx.Tx, tenant.Principal, string) (any, int, error)) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := strings.ToLower(r.PathValue("projectId"))
	if !workorders.UUID(project) {
		respondError(w, fail(400, "invalid review project"))
		return
	}
	var out any
	status := 200
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := reviewProjectTx(r.Context(), tx, p, project, permission, write); err != nil {
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
func (m *Module) listReviews(w http.ResponseWriter, r *http.Request) {
	limit, after := 50, int64(0)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if len(raw) > 3 || err != nil || n < 1 || n > 100 {
			respondError(w, fail(400, "invalid review limit"))
			return
		}
		limit = n
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if len(raw) > 19 || err != nil || n < 0 {
			respondError(w, fail(400, "invalid review cursor"))
			return
		}
		after = n
	}
	m.reviewRequest(w, r, "delivery_reviews.read", false, func(tx pgx.Tx, _ tenant.Principal, project string) (any, int, error) {
		out := ReviewPage{Items: []ReviewRound{}}
		var err error
		out.Settings, err = reviewSettingsTx(r.Context(), tx, project)
		if err != nil {
			return nil, 200, err
		}
		rows, err := tx.Query(r.Context(), `SELECT v.snapshot FROM delivery_review_rounds v JOIN nodes n ON n.tenant_id=v.tenant_id AND n.id=v.ticket_node_id AND n.project_id=v.project_id AND n.deleted_at IS NULL WHERE v.project_id=$1 AND v.position>$2 ORDER BY v.position LIMIT $3`, project, after, limit+1)
		if err != nil {
			return nil, 200, err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanReview(rows)
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
func (m *Module) reviewSettings(w http.ResponseWriter, r *http.Request) {
	write := r.Method == http.MethodPut
	permission := "delivery_reviews.read"
	var in ReviewSettings
	if write {
		permission = "delivery_reviews.manage"
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if in.Revision < 0 || in.Mode != "off" && in.Mode != "shadow" {
			respondError(w, fail(400, "invalid review settings"))
			return
		}
	}
	m.reviewRequest(w, r, permission, write, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := reviewSettingsTx(r.Context(), tx, project)
		if err != nil || !write {
			return before, 200, err
		}
		if before.Revision != in.Revision {
			return nil, 200, fail(409, "review settings changed; reload first")
		}
		in.Revision++
		if err = saveReviewSettingsTx(r.Context(), tx, p.TenantID, project, in); err != nil {
			return nil, 200, err
		}
		return in, 200, appendReviewChange(r.Context(), tx, p, "settings", reviewSnapshot{Project: project, Settings: &before}, reviewSnapshot{Project: project, Settings: &in}, m.now())
	})
}
func (m *Module) enqueueReview(w http.ResponseWriter, r *http.Request) {
	var in ReviewInput
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	in.SourceRound = strings.ToLower(in.SourceRound)
	in.AuthorRun = strings.ToLower(in.AuthorRun)
	if err := validateReviewInput(in); err != nil {
		respondError(w, err)
		return
	}
	m.reviewRequest(w, r, "delivery_reviews.manage", true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		out, created, err := m.queueReviewTx(r.Context(), tx, p, project, in)
		if err != nil || !created {
			return out, 200, err
		}
		return out, 201, appendReviewChange(r.Context(), tx, p, "queued", nil, reviewSnapshot{Project: project, Round: &out}, out.Updated)
	})
}
func (m *Module) claimReview(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("reviewId"))
	var in ReviewClaimInput
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	in.Request = strings.ToLower(in.Request)
	if !workorders.UUID(id) || !workorders.UUID(in.Request) || in.ScriptFamily != nil && !reviewgate.ValidFamily(*in.ScriptFamily) {
		respondError(w, fail(400, "invalid review claim"))
		return
	}
	m.reviewRequest(w, r, "delivery_reviews.claim", true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := loadReviewTx(r.Context(), tx, project, id)
		if err != nil {
			return nil, 200, err
		}
		fields, _, err := currentReviewTargetTx(r.Context(), tx, before)
		if err != nil {
			return nil, 200, err
		}
		family, err := authorFamilyTx(r.Context(), tx, before)
		if err != nil || family != before.AuthorFamily {
			if err == nil {
				err = fail(409, "author family changed")
			}
			return nil, 200, err
		}
		var raw []byte
		err = tx.QueryRow(r.Context(), `SELECT snapshot FROM delivery_review_claims WHERE project_id=$1 AND request_id=$2`, project, in.Request).Scan(&raw)
		if err == nil {
			var old ReviewClaim
			if err = json.Unmarshal(raw, &old); err != nil {
				return nil, 200, err
			}
			if old.Claimant != p.ID || old.Review != id || !reflect.DeepEqual(old.ReviewClaimInput, in) {
				return nil, 200, fail(409, "claim request has different inputs or owner")
			}
			return old, 200, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, 200, err
		}
		s, err := reviewSettingsTx(r.Context(), tx, project)
		if err != nil {
			return nil, 200, err
		}
		out := ReviewClaim{ReviewClaimInput: in, Project: project, Review: id, Claimant: p.ID, Mode: s.Mode, Reason: "review_off"}
		var changed *ReviewRound
		if s.Mode == "shadow" {
			out.Reason = "review_owned_or_completed"
			if before.State == "queued" {
				next := before
				if err = m.resolveReviewTx(r.Context(), tx, p, &next, fields); err != nil {
					return nil, 200, err
				}
				out.Reason = next.Reason
				if next.Route != nil {
					next.State = "claimed"
					next.Claimant = &p.ID
					next.ClaimRequest = &in.Request
					next.Revision++
					next.Updated = m.now()
					if err = saveReviewTx(r.Context(), tx, p.TenantID, next); err != nil {
						return nil, 200, err
					}
					changed = &next
					out.Round = &next
				}
			}
		}
		if in.ScriptFamily != nil {
			agree := out.Round != nil && out.Round.Route.Family == *in.ScriptFamily
			out.Agreement = &agree
		}
		if err = saveReviewClaimTx(r.Context(), tx, p.TenantID, out); err != nil {
			return nil, 200, err
		}
		return out, 200, appendReviewChange(r.Context(), tx, p, "claim", reviewSnapshot{Project: project, Round: &before}, reviewSnapshot{Project: project, Round: changed, Claim: &out}, m.now())
	})
}
func (m *Module) reviewVerdict(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("reviewId"))
	var in ReviewVerdictInput
	if err := decode(w, r, &in); err != nil {
		respondError(w, err)
		return
	}
	in.Request = strings.ToLower(in.Request)
	in.Profile = strings.ToLower(in.Profile)
	if !workorders.UUID(id) {
		respondError(w, fail(400, "invalid review id"))
		return
	}
	if err := validateReviewVerdict(in); err != nil {
		respondError(w, err)
		return
	}
	m.reviewRequest(w, r, "delivery_reviews.report", true, func(tx pgx.Tx, p tenant.Principal, project string) (any, int, error) {
		before, err := loadReviewTx(r.Context(), tx, project, id)
		if err != nil {
			return nil, 200, err
		}
		_, parent, err := currentReviewTargetTx(r.Context(), tx, before)
		if err != nil {
			return nil, 200, err
		}
		family, err := authorFamilyTx(r.Context(), tx, before)
		if err != nil {
			return nil, 200, err
		}
		if before.Claimant == nil || *before.Claimant != p.ID {
			return nil, 200, authz.ErrForbidden
		}
		if before.VerdictInput != nil {
			if !reflect.DeepEqual(*before.VerdictInput, in) {
				return nil, 200, fail(409, "review already has a different verdict")
			}
			return before, 200, nil
		}
		s, err := reviewSettingsTx(r.Context(), tx, project)
		if err != nil {
			return nil, 200, err
		}
		if s.Mode != "shadow" || before.State != "claimed" || before.Revision != in.Revision || before.Head != in.Head || before.Route == nil || before.Route.Profile != in.Profile || family != before.AuthorFamily {
			return nil, 200, fail(409, "review is off or its pinned binding changed")
		}
		policy, err := reviewgate.LoadFamilyPolicyTx(r.Context(), tx, &project)
		if err != nil {
			return nil, 200, err
		}
		if allowed, _ := policy.Effective.Decision(family, before.Route.Family); !allowed {
			return nil, 200, fail(409, "current family policy rejects this reviewer")
		}
		var harness, model, reviewerFamily string
		err = tx.QueryRow(r.Context(), `SELECT harness,model,family FROM model_profiles WHERE id=$1 AND enabled`, in.Profile).Scan(&harness, &model, &reviewerFamily)
		if err != nil {
			return nil, 200, err
		}
		if harness != before.Route.Harness || model != before.Route.Model || reviewerFamily != before.Route.Family {
			return nil, 200, fail(409, "reviewer profile binding changed")
		}
		source, err := loadRoundTx(r.Context(), tx, project, before.SourceRound)
		if err != nil {
			return nil, 200, err
		}
		var nextFix int
		if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(round_number),0)+1 FROM delivery_work_rounds WHERE project_id=$1 AND ticket_node_id=$2 AND kind='fix'`, project, before.Ticket).Scan(&nextFix); err != nil {
			return nil, 200, err
		}
		out := before
		out.Policy = policy.Effective
		applyReviewVerdict(p.TenantID, &out, in, source, parent, nextFix)
		out.Revision++
		out.Updated = m.now()
		if err = saveReviewTx(r.Context(), tx, p.TenantID, out); err != nil {
			return nil, 200, err
		}
		return out, 200, appendReviewChange(r.Context(), tx, p, "verdict", reviewSnapshot{Project: project, Round: &before}, reviewSnapshot{Project: project, Round: &out}, out.Updated)
	})
}

type reviewSnapshot struct {
	Project  string          `json:"project_id"`
	Settings *ReviewSettings `json:"settings,omitempty"`
	Round    *ReviewRound    `json:"round,omitempty"`
	Claim    *ReviewClaim    `json:"claim,omitempty"`
}

func appendReviewChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind string, before any, after reviewSnapshot, at time.Time) error {
	// All projection writes and row locks are complete before the event counter.
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &after.Project, Type: "delivery.review." + kind, Before: before, After: after, At: &at})
	return err
}

// RebuildReviews restores only these shadow projections. It never replays a
// proposed ticket, worker start, lead request or GitHub write as an action.
func (m *Module) RebuildReviews(ctx context.Context, p tenant.Principal, project string) error {
	project = strings.ToLower(project)
	if !workorders.UUID(project) {
		return fail(400, "invalid review project")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 30*time.Second)
	defer cancel()
	service := db.AllProjects(ctx, "replay shadow reviews of explicitly authorized project")
	return db.InTenant(service, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := reviewProjectTx(ctx, tx, p, project, "delivery_reviews.manage", true); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT after FROM events WHERE node_id=$1 AND type IN ('delivery.review.settings','delivery.review.queued','delivery.review.claim','delivery.review.verdict') ORDER BY id LIMIT 10001`, project)
		if err != nil {
			return err
		}
		items := []reviewSnapshot{}
		bytes := 0
		for rows.Next() {
			var raw []byte
			var snap reviewSnapshot
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			bytes += len(raw)
			if bytes > 32<<20 {
				rows.Close()
				return fail(409, "review replay byte limit exceeded")
			}
			if err = json.Unmarshal(raw, &snap); err != nil {
				rows.Close()
				return err
			}
			if snap.Project != project || snap.Round != nil && snap.Round.Project != project || snap.Claim != nil && snap.Claim.Project != project {
				rows.Close()
				return fail(409, "review replay project mismatch")
			}
			items = append(items, snap)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(items) > 10000 {
			return fail(409, "review replay limit exceeded")
		}
		for _, table := range []string{"delivery_review_claims", "delivery_review_rounds", "delivery_review_settings"} {
			if _, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE project_id=$1`, project); err != nil {
				return err
			}
		}
		for _, snap := range items {
			if snap.Settings != nil {
				err = saveReviewSettingsTx(ctx, tx, p.TenantID, project, *snap.Settings)
			}
			if err == nil && snap.Round != nil {
				err = saveReviewTx(ctx, tx, p.TenantID, *snap.Round)
			}
			if err == nil && snap.Claim != nil {
				err = saveReviewClaimTx(ctx, tx, p.TenantID, *snap.Claim)
			}
			if err != nil {
				return err
			}
		}
		return appendReviewChange(ctx, tx, p, "rebuilt", nil, reviewSnapshot{Project: project}, m.now())
	})
}
