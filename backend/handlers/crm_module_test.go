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

package handlers_test

// PAI-980: instance-level CRM module switch.

import (
	"context"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
)

func TestCRMModule_DefaultsToEnabled(t *testing.T) {
	ts := newTestServer(t)
	if !handlers.CRMModuleEnabled(context.Background()) {
		t.Fatal("fresh instance must report the CRM module enabled")
	}
	resp := ts.get(t, "/api/integrations/crm/module", ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	decode(t, resp, &body)
	if !body.Enabled {
		t.Fatal("GET must report enabled=true on a fresh instance")
	}
}

func TestCRMModule_AdminCanToggleAndItPersists(t *testing.T) {
	ts := newTestServer(t)

	resp := ts.put(t, "/api/integrations/crm/module", ts.adminCookie, map[string]any{"enabled": false})
	assertStatus(t, resp, http.StatusOK)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	decode(t, resp, &body)
	if body.Enabled {
		t.Fatal("PUT enabled=false must echo enabled=false")
	}
	if handlers.CRMModuleEnabled(context.Background()) {
		t.Fatal("switch must read back as disabled")
	}
	var stored string
	if err := db.DB.QueryRow(`SELECT value FROM app_settings WHERE key='crm_enabled'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "0" {
		t.Fatalf("stored value = %q, want %q", stored, "0")
	}

	resp = ts.put(t, "/api/integrations/crm/module", ts.adminCookie, map[string]any{"enabled": true})
	assertStatus(t, resp, http.StatusOK)
	if !handlers.CRMModuleEnabled(context.Background()) {
		t.Fatal("switch must read back as enabled again")
	}
}

func TestCRMModule_RejectsBadBodiesAndNonAdmins(t *testing.T) {
	ts := newTestServer(t)

	for name, body := range map[string]any{
		"missing field": map[string]any{},
		"wrong type":    map[string]any{"enabled": "yes"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := ts.put(t, "/api/integrations/crm/module", ts.adminCookie, body)
			assertStatus(t, resp, http.StatusBadRequest)
		})
	}
	if !handlers.CRMModuleEnabled(context.Background()) {
		t.Fatal("rejected writes must not change the switch")
	}

	resp := ts.put(t, "/api/integrations/crm/module", ts.memberCookie, map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("member write status = %d, want 401/403", resp.StatusCode)
	}
	resp = ts.get(t, "/api/integrations/crm/module", ts.memberCookie)
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("member read status = %d, want 401/403", resp.StatusCode)
	}
	if !handlers.CRMModuleEnabled(context.Background()) {
		t.Fatal("non-admin write must not change the switch")
	}
}
