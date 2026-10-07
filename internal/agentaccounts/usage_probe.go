// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (m *Module) usageProbe(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Revision *int64 `json:"binding_revision"`
		Enabled  *bool  `json:"enabled"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	if in.Revision == nil || *in.Revision < 0 || in.Enabled == nil {
		writeErr(w, fail(400, "binding_revision and enabled required"))
		return
	}
	err := m.inReadinessWrite(r.Context(), p, func(tx pgx.Tx) error {
		a, err := requireReadinessOwner(r.Context(), tx, p, id, *in.Revision)
		if err != nil {
			return err
		}
		if err := agentpairing.AccountFence(r.Context(), tx, id, false); err != nil {
			return err
		}
		if a.Harness != "grok" && a.Harness != "claude" && a.Harness != "codex" && (a.Harness != "pi" || a.Provider != "openrouter") {
			return fail(400, "usage probe unsupported for this harness")
		}
		if a.UsageProbeEnabled == *in.Enabled {
			return nil
		}
		if _, err := tx.Exec(r.Context(), `UPDATE agent_accounts SET usage_probe_enabled=$2,usage_probe_revision=link_revision WHERE id=$1`, id, *in.Enabled); err != nil {
			return err
		}
		return writeEvent(r.Context(), tx, p, "account.usage_probe_changed", nil, map[string]any{"account_id": id, "binding_revision": a.LinkRevision, "usage_probe_enabled": *in.Enabled})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func usageConsent(ctx context.Context, tx pgx.Tx, id string, revision *int64) (Account, error) {
	a, err := lockAccount(ctx, tx, id)
	if err != nil {
		return a, err
	}
	if revision == nil || a.LinkRevision != *revision {
		return a, fail(409, "account binding changed")
	}
	if !a.UsageProbeEnabled || a.State != "available" {
		return a, fail(403, "owner usage-probe consent required")
	}
	return a, nil
}
