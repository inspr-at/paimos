// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package handlers

// PAI-980: instance-level CRM module switch.
//
// The CRM (customers, contacts, documents, rates, provider sync) is an
// optional module. `crm_enabled` in app_settings decides whether the
// shells show a way into it: the 5.x sidebar entry, the Paimos 6 rail
// entry and the Cmd-K action. Absent or unparseable means enabled, so
// every existing instance keeps its 5.x behaviour without a migration.
//
// The switch is UI reachability only. Customer routes and APIs stay
// reachable when it is off; nothing is hidden from the API or deleted.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/inspr-at/paimos/backend/db"
)

const crmEnabledSettingKey = "crm_enabled"

// CRMModuleEnabled reports the instance switch. Read failures degrade to
// "enabled" and are logged, so a transient DB error never hides the
// module from every user at once.
func CRMModuleEnabled(ctx context.Context) bool {
	var value string
	err := db.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key=?`, crmEnabledSettingKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		log.Printf("crm module setting load: %v", err)
		return true
	}
	return value != "0"
}

type crmModulePayload struct {
	Enabled bool `json:"enabled"`
}

// GetCRMModule returns the instance switch. Admin-only; every other
// reader gets the same value from GET /api/instance.
func GetCRMModule(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, crmModulePayload{Enabled: CRMModuleEnabled(r.Context())})
}

// PutCRMModule sets the instance switch. Body: {"enabled": bool}.
func PutCRMModule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
		jsonError(w, "enabled (boolean) required", http.StatusBadRequest)
		return
	}
	value := "1"
	if !*body.Enabled {
		value = "0"
	}
	if _, err := db.DB.ExecContext(r.Context(),
		`INSERT INTO app_settings(key, value, updated_at) VALUES(?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=datetime('now')`,
		crmEnabledSettingKey, value,
	); err != nil {
		log.Printf("crm module setting save: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, crmModulePayload{Enabled: *body.Enabled})
}
