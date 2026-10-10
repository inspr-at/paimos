// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type probeBatchEntry struct {
	AccountID  string     `json:"account_id"`
	ObservedAt time.Time  `json:"observed_at"`
	Probe      probeWrite `json:"probe"`
}

// Transport batching deliberately preserves separate transactions: reportProbe
// appends events last, and one denied enrollment must not hide sibling results.
func (m *Module) probes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requireAgent(p); err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := workorders.BufferBody(w, r); err != nil {
		workorders.WriteError(w, err)
		return
	}
	var in struct {
		Items []probeBatchEntry `json:"items"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if len(in.Items) < 1 || len(in.Items) > 32 {
		writeErr(w, fail(400, "one to 32 probes required"))
		return
	}
	seen := make(map[string]bool, len(in.Items))
	for _, item := range in.Items {
		if !uuidRE.MatchString(item.AccountID) || seen[item.AccountID] || item.ObservedAt.IsZero() || item.Probe.Readiness != nil || item.Probe.MeasurementOnly {
			writeErr(w, fail(400, "invalid health probe batch"))
			return
		}
		seen[item.AccountID] = true
	}
	type result struct {
		AccountID string `json:"account_id"`
		Status    int    `json:"status"`
	}
	out := struct {
		Items []result `json:"items"`
	}{Items: make([]result, 0, len(in.Items))}
	for _, item := range in.Items {
		item.Probe.observedAt = &item.ObservedAt
		err := m.inReadinessWrite(ctx, p, func(tx pgx.Tx) error {
			// Re-check exact key and current grants inside each fenced mutation.
			scopes, err := readKeyScopes(ctx, tx, r, p, true)
			if err != nil {
				return err
			}
			current, err := workorders.CurrentKeyPrincipal(r, tx, p, "account.probe")
			if err != nil {
				return err
			}
			if !hasScope(scopes, "account.probe") || authz.RequireTx(ctx, tx, current, "account.probe", authz.Scope{}) != nil {
				return fail(403, "account probe permission required")
			}
			_, err = reportProbe(ctx, tx, current, item.AccountID, item.Probe)
			return err
		})
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
			var he *httpError
			var pe *agentpairing.Error
			if errors.As(err, &he) {
				status = he.status
			} else if errors.As(err, &pe) {
				status = pe.Status
			}
		}
		out.Items = append(out.Items, result{item.AccountID, status})
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
