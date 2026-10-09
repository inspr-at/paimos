// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type problem struct {
	status  int
	message string
}

func (p *problem) Error() string             { return p.message }
func fault(status int, message string) error { return &problem{status, message} }
func respond(w http.ResponseWriter, status int, value any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		if code, message, ok := phoneapprovals.StepUpFailure(err); ok {
			httpapi.WriteError(w, code, message)
			return
		}
		var p *problem
		switch {
		case errors.As(err, &p):
			httpapi.WriteError(w, p.status, p.message)
		case errors.Is(err, authz.ErrForbidden):
			httpapi.WriteError(w, 403, "target permission required")
		default:
			httpapi.WriteError(w, 500, "step-up unavailable")
		}
		return
	}
	httpapi.WriteJSON(w, status, value)
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<10))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return fault(400, "invalid request")
	}
	return nil
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/stepup-requests", m.create)
	mux.HandleFunc("GET /api/stepup-requests", m.list)
	mux.HandleFunc("GET /api/stepup-requests/{requestId}", m.get)
	mux.HandleFunc("POST /api/stepup-requests/{requestId}/options", m.options)
	mux.HandleFunc("POST /api/stepup-requests/{requestId}/approve", m.decision)
	mux.HandleFunc("POST /api/stepup-requests/{requestId}/decline", m.decision)
	mux.HandleFunc("POST /api/stepup-requests/{requestId}/withdraw", m.decision)
}
func (m *Module) caller(w http.ResponseWriter, r *http.Request, person bool) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		respond(w, 0, nil, fault(401, "authentication required"))
		return p, false
	}
	if person {
		if p.Kind != tenant.Person || p.KeyCreatorID != "" || !p.BrowserSession || r.Header.Get("Authorization") != "" {
			respond(w, 0, nil, fault(403, "a signed-in person is required"))
			return p, false
		}
		origin := strings.TrimRight(m.origin, "/")
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || r.Header.Get("Origin") != origin {
			respond(w, 0, nil, fault(403, "same-origin request required"))
			return p, false
		}
	}
	return p, true
}
func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(w, r, false)
	if !ok {
		return
	}
	var in Create
	if err := decode(w, r, &in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	ctx, cancel := contextDeadline(r)
	defer cancel()
	out, err := m.Create(ctx, p, in)
	respond(w, 201, out, err)
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := contextDeadline(r)
	defer cancel()
	out, err := m.Get(ctx, p, r.PathValue("requestId"))
	respond(w, 200, out, err)
}
func (m *Module) decision(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	p, ok := m.caller(w, r, action != "withdraw")
	if !ok {
		return
	}
	var in Approve
	if err := decode(w, r, &in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	if !hex256.MatchString(in.Digest) || in.Revision < 1 {
		respond(w, 0, nil, fault(400, "digest and revision required"))
		return
	}
	ctx, cancel := contextDeadline(r)
	defer cancel()
	out, err := m.Decide(ctx, p, r.PathValue("requestId"), in.Decide, action, &in.Proof)
	respond(w, 200, out, err)
}
func (m *Module) options(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(w, r, true)
	if !ok {
		return
	}
	var in Decide
	if err := decode(w, r, &in); err != nil {
		respond(w, 0, nil, err)
		return
	}
	id := r.PathValue("requestId")
	if !ValidID(id) || !hex256.MatchString(in.Digest) || in.Revision < 1 {
		respond(w, 0, nil, fault(400, "invalid request"))
		return
	}
	ctx, cancel := contextDeadline(r)
	defer cancel()
	var out any
	var start *ReauthStart
	var ended bool
	err := m.transaction(ctx, p, func(tx pgx.Tx) error {
		current, err := read(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err = allowed(ctx, tx, p, current); err != nil {
			return err
		}
		if err = m.expire(ctx, tx, p, &current); err != nil {
			return err
		}
		if current.State != "pending" {
			ended = true
			return nil
		}
		if err = bound(current, in); err != nil {
			return err
		}
		if m.phone == nil {
			return fault(503, "step-up authentication unavailable")
		}
		var absent bool
		out, absent, err = m.phone.StepUpOptionsTx(ctx, tx, p, id, current.Digest)
		if err != nil {
			return err
		}
		if absent {
			if m.Reauthenticate == nil {
				return fault(503, "fresh sign-in unavailable")
			}
			value, err := m.startReauth(ctx, tx, p, current)
			start = &value
			return err
		}
		return nil
	})
	if err == nil && ended {
		err = fault(409, "request ended")
	}
	if err == nil && start != nil {
		authURL, beginErr := m.Reauthenticate(w, r.WithContext(ctx), p, *start)
		err = beginErr
		out = map[string]any{"method": "oidc_reauth", "authorize_url": authURL}
	}
	respond(w, 200, out, err)
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := m.caller(w, r, false)
	if !ok {
		return
	}
	limit := 50
	if r.URL.Query().Has("limit") {
		v, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || v < 1 || v > 100 {
			respond(w, 0, nil, fault(400, "invalid limit"))
			return
		}
		limit = v
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "pending"
	}
	if state != "pending" && state != "decided" {
		respond(w, 0, nil, fault(400, "invalid state"))
		return
	}
	ctx, cancel := contextDeadline(r)
	defer cancel()
	out, err := m.List(ctx, p, state, limit, r.URL.Query().Get("cursor"))
	respond(w, 200, out, err)
}

// Headers/body reads, database locks and external auth all have a deadline.
func contextDeadline(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}
