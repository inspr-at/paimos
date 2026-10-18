// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (m *Module) piModel(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	// Validate and check the public catalog outside the account transaction. No
	// network call holds a pairing/account lock. Recheck the binding under lock.
	var before Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error { var err error; before, err = getAccount(r.Context(), tx, id); return err })
	if isNoRows(err) {
		err = fail(404, "account not found")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if before.Harness != "pi" || before.Provider == "" || !openrouter.ValidModel(before.Provider, in.Model) {
		writeErr(w, fail(400, "pi requires an enrolled provider and a valid model ID (OpenRouter: vendor/model[:variant])"))
		return
	}
	status, note := "unchecked", false
	if before.Provider == "openrouter" {
		status, note = m.openRouter.Lookup(r.Context(), in.Model)
	}
	var out Account
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := modelactivation.Lock(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
			return fail(403, "permission denied")
		}
		if err := agentpairing.AccountFence(ctx, tx, id, false); err != nil {
			return err
		}
		current, err := lockAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Provider != before.Provider {
			return fail(409, "account provider changed; reload Settings")
		}
		full := current.Provider + "/" + in.Model
		slug := fmt.Sprintf("pi-account-%x", sha256.Sum256([]byte(full)))
		family := "unknown"
		switch current.Provider {
		case "anthropic", "openai", "xai":
			family = current.Provider
		}
		// Reuse an enabled immutable pin; accepting a withheld one creates a
		// new version and keeps the old observation/history intact.
		var profile string
		err = tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug=$1 AND harness='pi' AND model=$2 AND effort='off' AND enabled ORDER BY created_at DESC,id LIMIT 1`, slug, full).Scan(&profile)
		if isNoRows(err) {
			var version string
			if err = tx.QueryRow(ctx, `SELECT (count(*)+1)::text FROM model_profiles WHERE slug=$1`, slug).Scan(&version); err != nil {
				return err
			}
			stored, insertErr := modelactivation.Activate(ctx, tx, p, modelactivation.Pin{
				Slug: slug, Version: version, Harness: "pi", Family: family, Model: full,
				Effort: "off", Tier: "standard", Enabled: true, Source: "manual", Permission: "account.manage",
			}, modelactivation.Person)
			if insertErr != nil {
				if errors.Is(insertErr, authz.ErrForbidden) {
					return fail(403, "model activation permission denied")
				}
				return insertErr
			}
			if !stored.Enabled {
				return fail(409, "model activation withheld")
			}
			profile = stored.ID
		} else if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_accounts SET model=$2,model_status=$3,model_data_note=$4,allowed_model_profile_ids=ARRAY[$5::uuid] WHERE id=$1`, id, in.Model, status, note, profile); err != nil {
			return err
		}
		out, err = getAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		return writeEvent(ctx, tx, p, evUpdated, current, out)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
