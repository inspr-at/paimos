// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type RoutingSettings struct {
	Mode string `json:"mode"`
}

type RoutingTarget struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// Families and design readiness are normalized launcher observations. Shadow
// evidence never substitutes for managed-run authorship or design approval.
type RoutingRequest struct {
	RoundID        string         `json:"round_id"`
	TicketID       string         `json:"ticket_node_id"`
	Kind           string         `json:"kind"`
	FixRound       int            `json:"fix_round,omitempty"`
	PreviousFamily string         `json:"previous_family,omitempty"`
	AuthorFamily   string         `json:"author_family,omitempty"`
	DesignReady    bool           `json:"design_ready"`
	Observed       *RoutingTarget `json:"observed,omitempty"`
}

type RoutingDecision struct {
	EventID     int64                        `json:"event_id"`
	ProjectID   string                       `json:"project_id"`
	Mode        string                       `json:"mode"`
	Request     RoutingRequest               `json:"request"`
	Plan        agentplan.Plan               `json:"plan"`
	Route       modelregistry.WorkResolution `json:"route"`
	DesignFirst bool                         `json:"design_first"`
	Comparison  string                       `json:"comparison"`
	At          time.Time                    `json:"at"`
}

func boundedRouting(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(deadline)
		defer controller.SetReadDeadline(time.Time{})
		handler(w, r.WithContext(ctx))
	}
}

// Settings and decisions are projections of existing tenant events. There is
// no second writable project-field switch and no schema expansion to migrate.
func routingSettingsTx(ctx context.Context, tx pgx.Tx, project string) (RoutingSettings, error) {
	out := RoutingSettings{Mode: "off"}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT after FROM events WHERE node_id=$1::uuid
 AND type='delivery.routing_settings_changed' ORDER BY id DESC LIMIT 1`, project).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err == nil && out.Mode != "off" && out.Mode != "shadow" {
		err = fail(503, "routing settings unavailable")
	}
	return out, err
}

func (m *Module) routingSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := strings.ToLower(r.PathValue("projectId"))
	if !workorders.UUID(project) {
		respondError(w, fail(400, "invalid project"))
		return
	}
	write := r.Method == http.MethodPut
	var in, out RoutingSettings
	if write {
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if in.Mode != "off" && in.Mode != "shadow" {
			respondError(w, fail(400, "routing mode must be off or shadow"))
			return
		}
	}
	ctx := r.Context()
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if write {
			if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		permission := "delivery.read"
		if write {
			permission = "delivery.manage"
		}
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var err error
		out, err = routingSettingsTx(ctx, tx, project)
		if err != nil || !write || in == out {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "delivery.routing_settings_changed", Before: out, After: in})
		out = in
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func validateRoutingRequest(in *RoutingRequest) error {
	if !workorders.UUID(in.RoundID) || !workorders.UUID(in.TicketID) || in.FixRound < 0 || in.FixRound > 1000 {
		return fail(400, "invalid routing round or ticket")
	}
	in.RoundID, in.TicketID = strings.ToLower(in.RoundID), strings.ToLower(in.TicketID)
	switch in.Kind {
	case "fix":
		if in.FixRound == 0 {
			return fail(400, "fix round must be positive")
		}
	case "first_build", "merge", "land", "review", "design":
		if in.FixRound != 0 {
			return fail(400, "fix round belongs to fix work")
		}
	default:
		return fail(400, "unknown routing kind")
	}
	for _, family := range []*string{&in.AuthorFamily, &in.PreviousFamily} {
		if len(*family) > 32 {
			return fail(400, "invalid routing family")
		}
		value, err := modelregistry.NormalizeAuthorFamily(*family)
		if err != nil {
			return fail(400, "invalid routing family")
		}
		*family = value
	}
	if in.Kind == "review" && (in.AuthorFamily == "" || in.AuthorFamily == "unknown") {
		return fail(400, "review requires a known author family")
	}
	if o := in.Observed; o != nil {
		if agentplan.HarnessLabel(o.Harness) == "" || len(o.Model) == 0 || len(o.Model) > 128 || strings.TrimSpace(o.Model) != o.Model || len(o.Effort) == 0 || len(o.Effort) > 32 || strings.TrimSpace(o.Effort) != o.Effort {
			return fail(400, "invalid observed route")
		}
	}
	return nil
}

func loadRoutingDecision(ctx context.Context, tx pgx.Tx, project, round string) (RoutingDecision, error) {
	var out RoutingDecision
	var raw []byte
	var id int64
	err := tx.QueryRow(ctx, `SELECT id,after FROM events WHERE node_id=$1::uuid
 AND type='delivery.routing_decided' AND after->'request'->>'round_id'=$2 ORDER BY id LIMIT 1`, project, round).Scan(&id, &raw)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	out.EventID = id
	return out, nil
}

func routingPlanTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (agentplan.Plan, error) {
	if err := authz.RequireTx(ctx, tx, p, agentplan.ReadScope, authz.Scope{}); err != nil {
		return agentplan.Plan{}, err
	}
	owner := p.ID
	if p.Kind == tenant.Agent {
		owner = p.KeyCreatorID
	}
	if owner == "" || p.Kind != tenant.Agent && p.Kind != tenant.Person {
		return agentplan.Plan{}, fail(403, "routing requires a person-owned plan")
	}
	var canonical string
	err := tx.QueryRow(ctx, `SELECT c.id::text FROM principals person
 JOIN principals c ON c.tenant_id=person.tenant_id AND c.id=coalesce(person.linked_to,person.id)
 WHERE person.id=$1::uuid AND person.kind='person' AND person.status='active'
 AND c.kind='person' AND c.status='active'`, owner).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentplan.Plan{}, fail(403, "routing requires an active person-owned plan")
	}
	if err != nil {
		return agentplan.Plan{}, err
	}
	raw, _, err := agentplan.ReadPreference(ctx, tx, p.TenantID, canonical)
	if err != nil {
		return agentplan.Plan{}, fail(503, "routing plan unavailable")
	}
	plan, _, err := agentplan.Decode(raw)
	if err != nil {
		return agentplan.Plan{}, fail(503, "routing plan unavailable")
	}
	return plan, nil
}

func compareRoute(observed *RoutingTarget, route modelregistry.WorkResolution) string {
	if observed == nil {
		return "not_provided"
	}
	if route.Profile == nil {
		return "blocked"
	}
	p := route.Profile
	if *observed == (RoutingTarget{Harness: p.Harness, Model: p.Model, Effort: p.Effort}) {
		return "match"
	}
	return "mismatch"
}

func (m *Module) routingDecision(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := strings.ToLower(r.PathValue("projectId"))
	if !workorders.UUID(project) {
		respondError(w, fail(400, "invalid project"))
		return
	}
	write := r.Method == http.MethodPost
	in := RoutingRequest{RoundID: strings.ToLower(r.PathValue("roundId"))}
	if write {
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		if err := validateRoutingRequest(&in); err != nil {
			respondError(w, err)
			return
		}
	} else if !workorders.UUID(in.RoundID) {
		respondError(w, fail(400, "invalid routing round"))
		return
	}
	var out RoutingDecision
	ctx := r.Context()
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if write {
			// Tenant/access fence then tree. Concurrent retries get one event.
			if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "delivery.route", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		if write {
			if err := authz.RequireTx(ctx, tx, p, agentplan.ReadScope, authz.Scope{}); err != nil {
				return err
			}
		}
		prior, err := loadRoutingDecision(ctx, tx, project, in.RoundID)
		if err == nil {
			if write && !reflect.DeepEqual(prior.Request, in) {
				return fail(409, "routing round already recorded with different input")
			}
			out = prior
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) || !write {
			return err
		}
		settings, err := routingSettingsTx(ctx, tx, project)
		if err != nil {
			return err
		}
		if settings.Mode != "shadow" {
			return fail(409, "shadow routing is off for this project")
		}
		plan, err := routingPlanTx(ctx, tx, p)
		if err != nil {
			return err
		}
		off := []string{}
		for harness, limit := range plan.Limits {
			if limit.Mode == agentplan.Off {
				off = append(off, harness)
			}
		}
		sort.Strings(off)
		at := m.now()
		query := modelregistry.WorkQuery{TicketID: in.TicketID, ProjectID: project, Queued: true, FixRound: in.FixRound, PreviousFamily: in.PreviousFamily, AuthorFamily: in.AuthorFamily, OffHarnesses: off}
		route, design, err := modelregistry.ResolveRound(ctx, tx, p, query, in.Kind, in.DesignReady, at)
		if err != nil {
			return err
		}
		out = RoutingDecision{ProjectID: project, Mode: "shadow", Request: in, Plan: plan, Route: route, DesignFirst: design, Comparison: compareRoute(in.Observed, route), At: at}
		// Event counter is last. No assignment or account mutation occurs here.
		event, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "delivery.routing_decided", After: out, At: &at})
		out.EventID = event.ID
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
