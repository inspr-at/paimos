// SPDX-License-Identifier: AGPL-3.0-only

package costunits

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves cost-unit rate routes.
type Module struct {
	pool        *pgxpool.Pool
	reg         *plugins.Registry
	appendEvent func(context.Context, pgx.Tx, tenant.Principal, events.Change) (events.Event, error)
}

var _ httpapi.Module = (*Module)(nil)

// New returns the httpapi.Module for /api/cost-units/{costUnitId}/rates.
// reg is the sealed registry that contains Plugin. A nil registry fails closed.
func New(pool *pgxpool.Pool, reg *plugins.Registry) *Module {
	return &Module{pool: pool, reg: reg, appendEvent: events.Append}
}

// Mount registers the rate routes. Paths are the full /api paths the server mux expects.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cost-units/{costUnitId}/rates", m.list)
	mux.HandleFunc("POST /api/cost-units/{costUnitId}/rates", m.create)
}

func (m *Module) requirePermission(r *http.Request, p tenant.Principal, permission string) error {
	if p.Kind != tenant.Person || authz.Require(authz.BindPool(r.Context(), m.pool), permission, authz.Scope{}) != nil {
		return forbidden("permission denied")
	}
	return nil
}

type rateWrite struct {
	Unit     string      `json:"unit"`
	Currency string      `json:"currency"`
	Internal json.Number `json:"internal_amount"`
	Bill     json.Number `json:"bill_amount"`
	From     string      `json:"effective_from"`
	Until    *string     `json:"effective_until"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, err := principalFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	nodeID := r.PathValue("costUnitId")
	if !validUUID(nodeID) {
		writeErr(w, badRequest("invalid cost unit id"))
		return
	}
	var rates []Rate
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorize(r.Context(), tx, p.TenantID, fence.PermViewsProvide, false); err != nil {
			return err
		}
		if err := costUnitVisible(r.Context(), tx, p.TenantID, nodeID); err != nil {
			return err
		}
		var err error
		rates, err = listRates(r.Context(), tx, p.TenantID, nodeID)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rates)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, err := principalFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := m.requirePermission(r, p, "cost_units.manage"); err != nil {
		writeErr(w, err)
		return
	}
	nodeID := r.PathValue("costUnitId")
	if !validUUID(nodeID) {
		writeErr(w, badRequest("invalid cost unit id"))
		return
	}
	var body rateWrite
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, err)
		return
	}
	in, err := normalizeWrite(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	rate, err := m.insertRate(r.Context(), p, nodeID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rate)
}

func normalizeWrite(body rateWrite) (rateInput, error) {
	if !validUnit(body.Unit) {
		return rateInput{}, badRequest("unit must be hour, day, or item")
	}
	if !validCurrency(body.Currency) {
		return rateInput{}, badRequest("currency must be a three-letter ISO code")
	}
	internal, err := parseAmount(body.Internal)
	if err != nil {
		return rateInput{}, badRequest(err.Error())
	}
	bill, err := parseAmount(body.Bill)
	if err != nil {
		return rateInput{}, badRequest(err.Error())
	}
	from, err := parseDate(body.From)
	if err != nil {
		return rateInput{}, badRequest("effective_from must be a UTC date")
	}
	until := ""
	if body.Until != nil {
		until, err = parseDate(*body.Until)
		if err != nil || until <= from {
			return rateInput{}, badRequest("effective_until must be a UTC date after effective_from")
		}
	}
	return rateInput{Unit: body.Unit, Currency: body.Currency, Internal: internal, Bill: bill, From: from, Until: until}, nil
}

// insertRate writes one rate, closing a single overlapping open interval when
// that close makes the ranges adjacent. Lock order is tenant, installation,
// cost-unit node, then the rate rows. The installation is rechecked after the
// node lock, in the same transaction as the events.
func (m *Module) insertRate(ctx context.Context, p tenant.Principal, nodeID string, in rateInput) (Rate, error) {
	var out Rate
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "cost_units.manage", authz.Scope{}) != nil {
			return forbidden("permission denied")
		}
		if err := m.authorize(ctx, tx, p.TenantID, fence.PermStepsApply, true); err != nil {
			return err
		}
		if err := lockCostUnit(ctx, tx, p.TenantID, nodeID); err != nil {
			return err
		}
		if err := m.authorize(ctx, tx, p.TenantID, fence.PermStepsApply, false); err != nil {
			return err
		}
		existing, err := lockRates(ctx, tx, p.TenantID, nodeID, in.Unit, in.Currency)
		if err != nil {
			return err
		}
		replay, closeID, err := planRate(existing, in)
		if err != nil {
			return err
		}
		if replay != nil {
			out = *replay
			return nil
		}
		if closeID != "" {
			var previous Rate
			for _, rate := range existing {
				if rate.ID == closeID {
					previous = rate
					break
				}
			}
			closed, err := closeRate(ctx, tx, p.TenantID, closeID, in.From)
			if err != nil {
				return err
			}
			if !closed.Internal.equal(previous.Internal) || !closed.Bill.equal(previous.Bill) {
				return conflict("cost unit rates are historical")
			}
			if err := m.appendChange(ctx, tx, p, nodeID, eventClosed, previous, closed); err != nil {
				return err
			}
		}
		created, err := insertRate(ctx, tx, p, nodeID, in)
		if err != nil {
			return err
		}
		if err := m.appendChange(ctx, tx, p, nodeID, eventCreated, nil, created); err != nil {
			return err
		}
		out = created
		return nil
	})
	return out, err
}

func planRate(existing []Rate, in rateInput) (replay *Rate, closeID string, err error) {
	openID := ""
	for i := range existing {
		rate := &existing[i]
		if rate.EffectiveFrom == in.From {
			if !rate.Internal.equal(in.Internal) || !rate.Bill.equal(in.Bill) {
				return nil, "", conflict("a rate already starts on that date")
			}
			storedUntil := untilText(rate.EffectiveUntil)
			if in.Until == "" || storedUntil == in.Until {
				return rate, "", nil
			}
			return nil, "", conflict("a rate already starts on that date")
		}
		if rate.EffectiveUntil == nil {
			if openID != "" {
				return nil, "", conflict("rate intervals are inconsistent")
			}
			openID = rate.ID
		}
	}
	if openID != "" {
		for _, rate := range existing {
			if rate.ID == openID && rate.EffectiveFrom < in.From && overlaps(rate.EffectiveFrom, "", in.From, in.Until) {
				closeID = openID
			}
		}
	}
	for _, rate := range existing {
		if rate.ID == closeID {
			continue
		}
		if overlaps(rate.EffectiveFrom, untilText(rate.EffectiveUntil), in.From, in.Until) {
			return nil, "", conflict("rate intervals overlap")
		}
	}
	return nil, closeID, nil
}
