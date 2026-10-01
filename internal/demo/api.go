// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/business/costunits"
	"github.com/inspr-at/paimos/internal/business/hours"
	"github.com/inspr-at/paimos/internal/deliveryvote"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/intake"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/relations"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// api calls the same module handlers the server mounts.
type api struct {
	ctx context.Context
	mux *http.ServeMux
}

func newAPI(ctx context.Context, pool *pgxpool.Pool) (*api, error) {
	reg := plugins.NewRegistry()
	costs, err := costunits.Plugin()
	if err != nil {
		return nil, err
	}
	hoursPlugin, err := hours.Plugin()
	if err != nil {
		return nil, err
	}
	if err := reg.Register(costs); err != nil {
		return nil, err
	}
	if err := reg.Register(hoursPlugin); err != nil {
		return nil, err
	}
	reg.Seal()
	mux := http.NewServeMux()
	nodes.New(pool, nil).Mount(mux)
	relations.New(pool).Mount(mux)
	activity.New(pool).Mount(mux)
	deliveryvote.New(pool).Mount(mux)
	knowledge.New(pool).Mount(mux)
	journey.New(pool).Mount(mux)
	requirements.New(pool).Mount(mux)
	releases.New(pool).Mount(mux)
	intake.New(pool).Mount(mux)
	approvals.New(pool).Mount(mux)
	workorders.New(pool).Mount(mux)
	agentruns.New(pool).Mount(mux)
	agentaccounts.New(pool).Mount(mux)
	harness.New(pool).Mount(mux)
	modelregistry.New(pool).Mount(mux)
	plugins.NewWithRegistry(pool, reg).Mount(mux)
	costunits.New(pool, reg).Mount(mux)
	hours.New(pool, reg).Mount(mux)
	return &api{ctx: ctx, mux: mux}, nil
}

func (a *api) do(p tenant.Principal, token, method, path string, body any, want int, dst any, headers map[string]string) error {
	code, raw, err := a.call(p, token, method, path, body, headers)
	if err != nil {
		return err
	}
	if code != want {
		msg := string(raw)
		if len(msg) > 600 {
			msg = msg[:600]
		}
		return fmt.Errorf("%s %s: status %d: %s", method, path, code, msg)
	}
	if dst != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("%s %s: json: %w", method, path, err)
		}
	}
	return nil
}

func (a *api) call(p tenant.Principal, token, method, path string, body any, headers map[string]string) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req = req.WithContext(tenant.WithPrincipal(a.ctx, p))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}
