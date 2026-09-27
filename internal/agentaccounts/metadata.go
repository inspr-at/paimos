// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type metadataWrite struct {
	Label             *string  `json:"label"`
	Plan              *string  `json:"plan"`
	HostLabel         *string  `json:"host_label"`
	AllowedProfileIDs []string `json:"allowed_model_profile_ids"`
}

func metadataText(value string, empty bool) (string, error) {
	value = strings.TrimSpace(value)
	if (!empty && value == "") || len(value) > 128 || strings.ContainsFunc(value, unicode.IsControl) || looksLikeCredential(value) {
		return "", fail(http.StatusBadRequest, "invalid display metadata")
	}
	return value, nil
}

func (m *Module) metadata(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		if err := m.requirePermission(r, p, "account.manage"); err != nil {
			writeErr(w, err)
			return
		}
	}
	var in metadataWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(http.StatusNotFound, "account not found"))
		return
	}
	var out Account
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if p.Kind == tenant.Agent {
			if err := requireScope(r.Context(), tx, r, p, "account.manage"); err != nil {
				return err
			}
		}
		var err error
		out, err = replaceMetadata(r.Context(), tx, p, id, in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func replaceMetadata(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in metadataWrite) (Account, error) {
	if in.Label == nil || in.Plan == nil || in.HostLabel == nil || in.AllowedProfileIDs == nil || len(in.AllowedProfileIDs) > 256 {
		return Account{}, fail(http.StatusBadRequest, "label, plan, host_label and profile array required")
	}
	label, err := metadataText(*in.Label, false)
	if err != nil {
		return Account{}, err
	}
	plan, err := metadataText(*in.Plan, true)
	if err != nil {
		return Account{}, err
	}
	host, err := metadataText(*in.HostLabel, true)
	if err != nil {
		return Account{}, err
	}
	ids := append([]string{}, in.AllowedProfileIDs...)
	for i, id := range ids {
		if !uuidRE.MatchString(id) {
			return Account{}, fail(http.StatusBadRequest, "invalid model profile")
		}
		ids[i] = strings.ToLower(id)
	}
	slices.Sort(ids)
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		return Account{}, fail(http.StatusBadRequest, "duplicate model profile")
	}
	before, err := lockAccount(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	if p.Kind == tenant.Agent && before.RegisteredBy != p.ID {
		return Account{}, fail(http.StatusForbidden, "only the registering agent can update metadata")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles WHERE id::text = ANY($1::text[]) AND harness = $2 AND enabled`, ids, before.Harness).Scan(&count); err != nil {
		return Account{}, err
	}
	if count != len(ids) {
		return Account{}, fail(http.StatusBadRequest, "profiles must be enabled tenant profiles for this harness")
	}
	if before.Label == label && before.Plan == plan && before.HostLabel == host && before.AllowedProfileIDs != nil && slices.Equal(before.AllowedProfileIDs, ids) {
		return before, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET label=$2, plan=$3, host_label=$4, allowed_model_profile_ids=$5::uuid[] WHERE id=$1::uuid`, id, label, plan, host, ids); err != nil {
		return Account{}, err
	}
	after, err := getAccount(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	if err := writeEvent(ctx, tx, p, evUpdated, before, after); err != nil {
		return Account{}, err
	}
	return after, nil
}
