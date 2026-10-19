// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import (
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// daemonWaitHeld is the only stored checkout-wait reason. It matches the
// daemon's CheckoutHeldCause without importing the daemon package.
const daemonWaitHeld = "checkout_held_by_earlier_pairing"

// daemonWaitModule keeps checkout-wait sentences in process memory. The run
// row is not modified. A restart drops the map; the daemon reports again.
type daemonWaitModule struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	reasons map[string]string
}

// NewDaemonWait mounts the checkout-wait endpoints. It does not share the run
// module's pairing lock: Endpoint already holds the tenant lock.
func NewDaemonWait(pool *pgxpool.Pool) httpapi.Module {
	return &daemonWaitModule{pool: pool, reasons: map[string]string{}}
}

func (m *daemonWaitModule) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/daemon-waits", workorders.Endpoint(m.pool, "run.read", false, http.StatusOK, m.list))
	mux.HandleFunc("GET /api/runs/{runId}/daemon-wait", workorders.Endpoint(m.pool, "run.read", false, http.StatusOK, m.get))
	mux.HandleFunc("PUT /api/runs/{runId}/daemon-wait", workorders.Endpoint(m.pool, "run.claim", true, http.StatusOK, m.put))
}

type daemonWaitItem struct {
	RunID  string `json:"run_id"`
	Reason any    `json:"reason"`
}

func (m *daemonWaitModule) list(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ids, err := daemonWaitIDs(r.URL.Query().Get("ids"))
	if err != nil {
		return nil, err
	}
	items := make([]daemonWaitItem, 0, len(ids))
	for _, id := range ids {
		visible, err := m.runVisible(r, tx, p, id)
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		items = append(items, daemonWaitItem{RunID: id, Reason: m.reason(p.TenantID, id)})
	}
	return map[string]any{"items": items}, nil
}

func (m *daemonWaitModule) get(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("runId")
	visible, err := m.runVisible(r, tx, p, id)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, workorders.Fail(http.StatusNotFound, "not found")
	}
	return map[string]any{"reason": m.reason(p.TenantID, id)}, nil
}

func (m *daemonWaitModule) put(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := workorders.Decode(r, &body); err != nil {
		return nil, err
	}
	if body.Reason != "" && body.Reason != daemonWaitHeld {
		return nil, workorders.Fail(http.StatusBadRequest, "invalid daemon wait reason")
	}
	id := r.PathValue("runId")
	status, owner, err := m.runOwner(r, tx, id)
	if err != nil {
		return nil, err
	}
	if p.Kind != tenant.Agent || owner != p.ID {
		return nil, workorders.Fail(http.StatusNotFound, "not found")
	}
	if body.Reason != "" && status != "queued" {
		return nil, workorders.Fail(http.StatusConflict, "run is not queued")
	}
	m.mu.Lock()
	key := p.TenantID + "/" + id
	if body.Reason == "" {
		delete(m.reasons, key)
	} else {
		m.reasons[key] = body.Reason
	}
	m.mu.Unlock()
	var reason any
	if body.Reason != "" {
		reason = body.Reason
	}
	return map[string]any{"reason": reason}, nil
}

func (m *daemonWaitModule) runVisible(r *http.Request, tx pgx.Tx, p tenant.Principal, runID string) (bool, error) {
	_, owner, err := m.runOwner(r, tx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if p.Kind == tenant.Agent && owner != p.ID {
		return false, nil
	}
	return true, nil
}

func (m *daemonWaitModule) runOwner(r *http.Request, tx pgx.Tx, runID string) (string, string, error) {
	var status, owner string
	err := tx.QueryRow(r.Context(), `SELECT status, agent_principal_id::text FROM agent_runs WHERE id=$1`, runID).Scan(&status, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if err != nil {
		return "", "", err
	}
	return status, owner, nil
}

func (m *daemonWaitModule) reason(tenantID, runID string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	reason := m.reasons[tenantID+"/"+runID]
	if reason == "" {
		return nil
	}
	return reason
}

func daemonWaitIDs(raw string) ([]string, error) {
	if raw == "" || len(raw) > 2048 {
		return nil, workorders.Fail(http.StatusBadRequest, "invalid id")
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 50 {
		return nil, workorders.Fail(http.StatusBadRequest, "invalid id")
	}
	ids := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if !workorders.UUID(id) {
			return nil, workorders.Fail(http.StatusBadRequest, "invalid id")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, workorders.Fail(http.StatusBadRequest, "invalid id")
	}
	return ids, nil
}
