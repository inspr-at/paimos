// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type accountSignals struct {
	ReadingSupport   string `json:"reading_support"`
	QuotaFingerprint string `json:"quota_fingerprint"`
}
type statuslineConsent struct {
	Enabled *bool  `json:"enabled"`
	Plan    string `json:"plan,omitempty"`
}

func signalAccount(r *http.Request, tx pgx.Tx, p tenant.Principal) (Account, error) {
	if !uuidRE.MatchString(r.PathValue("accountId")) {
		return Account{}, fail(404, "account not found")
	}
	if err := requireScope(r.Context(), tx, r, p, "account.probe"); err != nil {
		return Account{}, err
	}
	if err := agentpairing.AccountFence(r.Context(), tx, r.PathValue("accountId"), true); err != nil {
		return Account{}, err
	}
	a, err := lockAccount(r.Context(), tx, r.PathValue("accountId"))
	if err != nil {
		return a, err
	}
	if p.Kind != tenant.Agent || a.RegisteredBy != p.ID {
		return a, fail(403, "registering agent required")
	}
	return a, nil
}

func (m *Module) signals(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in accountSignals
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if !regexp.MustCompile(`^([a-f0-9]{64})?$`).MatchString(in.QuotaFingerprint) {
		writeErr(w, fail(400, "invalid fingerprint"))
		return
	}
	switch in.ReadingSupport {
	case "every_5_min", "first_run", "statusline", "none":
	default:
		writeErr(w, fail(400, "invalid reading support"))
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		a, err := signalAccount(r, tx, p)
		if err != nil {
			return err
		}
		if a.ReadingSupport == in.ReadingSupport && a.QuotaFingerprint == in.QuotaFingerprint {
			return nil
		}
		_, err = tx.Exec(r.Context(), `UPDATE agent_accounts SET reading_support=$2,quota_fingerprint=$3 WHERE id=$1`, a.ID, in.ReadingSupport, in.QuotaFingerprint)
		if err != nil {
			return err
		}
		return writeEvent(r.Context(), tx, p, "account.signals", nil, in)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) quotaKey(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var key []byte
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if _, err := signalAccount(r, tx, p); err != nil {
			return err
		}
		candidate := make([]byte, 32)
		if _, err := rand.Read(candidate); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO tenant_quota_keys(tenant_id,key) VALUES($1,$2) ON CONFLICT DO NOTHING`, p.TenantID, candidate); err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `SELECT key FROM tenant_quota_keys WHERE tenant_id=$1`, p.TenantID).Scan(&key)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, struct {
		Key []byte `json:"key"`
	}{key})
}

func (m *Module) statusline(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in statuslineConsent
	if r.Method == http.MethodPut {
		if p.Kind != tenant.Person {
			writeErr(w, fail(403, "person required"))
			return
		}
		if err := m.requirePermission(r, p, "account.manage"); err != nil {
			writeErr(w, err)
			return
		}
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
		if in.Enabled == nil {
			writeErr(w, fail(400, "enabled required"))
			return
		}
	}
	var enabled bool
	plan := ""
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var a Account
		var err error
		if r.Method == http.MethodGet {
			a, err = signalAccount(r, tx, p)
		} else {
			if !uuidRE.MatchString(r.PathValue("accountId")) {
				return fail(404, "account not found")
			}
			if err = agentpairing.AccountFence(r.Context(), tx, r.PathValue("accountId"), false); err != nil {
				return err
			}
			a, err = lockAccount(r.Context(), tx, r.PathValue("accountId"))
		}
		if err != nil {
			return err
		}
		if a.Harness != "claude" {
			return fail(400, "Claude account required")
		}
		if r.Method == http.MethodPut {
			audience, err := statuslineAudience(r.Context(), tx, p, a.ID)
			if err != nil {
				return err
			}
			if audience == "" {
				return fail(403, "status line opt-in is limited to the paired computer's owner or a workspace admin")
			}
		}
		enabled = a.StatuslineEnabled
		if r.Method == http.MethodGet {
			plan, err = statuslinePlanFor(r, tx, a)
			if err != nil {
				return err
			}
		}
		if in.Enabled != nil && enabled != *in.Enabled {
			enabled = *in.Enabled
			if _, err = tx.Exec(r.Context(), `UPDATE agent_accounts SET statusline_enabled=$2 WHERE id=$1`, a.ID, enabled); err != nil {
				return err
			}
			return writeEvent(r.Context(), tx, p, "account.statusline", nil, struct {
				AccountID string `json:"account_id"`
				Enabled   bool   `json:"enabled"`
			}{a.ID, enabled})
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, statuslineConsent{Enabled: &enabled, Plan: plan})
}

func statuslineAudience(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string) (string, error) {
	if p.Kind != tenant.Person {
		return "", nil
	}
	var own bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE e.account_id=$1::uuid AND q.approved_by=$2::uuid AND e.state<>'revoked')`, accountID, p.ID).Scan(&own); err != nil {
		return "", err
	}
	if own {
		return "own", nil
	}
	admin, err := workspaceStatuslineAdmin(ctx, tx, p.ID)
	if err != nil || !admin {
		return "", err
	}
	return "workspace", nil
}

func workspaceStatuslineAdmin(ctx context.Context, tx pgx.Tx, principalID string) (bool, error) {
	var admin bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.principal_id=$1::uuid AND b.scope_type='workspace' AND r.builtin AND r.key IN ('owner','admin'))`, principalID).Scan(&admin)
	return admin, err
}

func annotateStatuslineOptIn(ctx context.Context, tx pgx.Tx, p tenant.Principal, items []Account) error {
	admin, err := workspaceStatuslineAdmin(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT e.account_id::text FROM agent_pairing_enrollments e JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id WHERE q.approved_by=$1::uuid AND e.state<>'revoked'`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	own := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		own[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		if items[i].Harness != "claude" {
			continue
		}
		if own[items[i].ID] {
			items[i].StatuslineOptIn = "own"
		} else if admin {
			items[i].StatuslineOptIn = "workspace"
		}
	}
	return nil
}

func statuslinePlanFor(r *http.Request, tx pgx.Tx, a Account) (string, error) {
	ctx := r.Context()
	now := time.Now().UTC()
	schedule, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return "", err
	}
	readings, err := readCapacity(ctx, tx, a.ID, true)
	if err != nil {
		return "", err
	}
	available := math.Inf(1)
	today := math.Inf(1)
	for _, v := range readings {
		if v.Freshness(now) != "fresh" || v.Source == "estimate" {
			continue
		}
		if v.OrdinaryUsageAllowed != nil && !*v.OrdinaryUsageAllowed {
			return "Aeon · vendor limit", nil
		}
		pace, _, err := readingPacing(ctx, tx, a.ID, v, now, schedule)
		if err != nil {
			return "", err
		}
		available = math.Min(available, pace.AvailableNowPercent)
		today = math.Min(today, pace.SuggestedTodayPercent)
	}
	if math.IsInf(today, 1) {
		return "Aeon · waiting for a reading", nil
	}
	if a.State != "available" {
		return "Aeon · account paused", nil
	}
	if available <= 0 {
		return "Aeon · agents waiting for capacity", nil
	}
	return fmt.Sprintf("Aeon · agents up to %.0f%% today", today), nil
}
