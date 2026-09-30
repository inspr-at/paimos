// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPoolConfirmationDoesNotSilentlyIncludeSpare(t *testing.T) {
	reset(t)
	admin, runner, _, token, mod := groupFixture(t)
	token = issueKey(t, runner, []string{"account.manage", "account.probe"})
	doors := []Account{
		groupAccount(t, mod, admin, runner, token, "main", "daemon-a", "Main", "laptop"),
		groupAccount(t, mod, admin, runner, token, "spare", "daemon-b", "Spare", "laptop"),
		groupAccount(t, mod, admin, runner, token, "studio", "daemon-c", "Studio", "studio"),
	}
	fp := strings.Repeat("ab", 32)
	for _, a := range doors {
		callStatus(t, mod, &runner, token, "PUT", "/api/agent-accounts/"+a.ID+"/signals", encoded(t, accountSignals{"none", fp}), 204, nil)
	}
	confirm := func(ids []string) {
		t.Helper()
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/quota-pool", encoded(t, map[string]any{"account_ids": ids, "quota_fingerprint": fp, "confirmed": true}), 204, nil)
	}
	confirm([]string{doors[0].ID, doors[1].ID})
	confirm([]string{doors[0].ID, doors[2].ID}) // The dialog names only Main and Studio.
	if n := scalar(t, admin, `SELECT count(*) FROM (`+quotaAccounts+`) q`, doors[0].ID); n != 2 {
		t.Fatalf("Pool with Studio silently included Spare: members=%d", n)
	}
	if n := scalar(t, admin, `SELECT count(*) FROM (`+quotaAccounts+`) q`, doors[1].ID); n != 1 {
		t.Fatalf("unnamed Spare still shares the ledger: members=%d", n)
	}
	if scalar(t, admin, `SELECT count(*) FROM events WHERE type='account.quota_pool_confirmed' AND after->'account_ids' ? $1 AND after->'account_ids' ? $2 AND NOT (after->'account_ids' ? $3)`, doors[0].ID, doors[2].ID, doors[1].ID) != 1 {
		t.Fatal("audit did not name the exact confirmed set")
	}
}

func TestGroupPatchPreservesHiddenProjectMemberships(t *testing.T) {
	for _, mode := range []string{"name-only", "visible-list", "remove-visible", "exclusive-hidden-only"} {
		t.Run(mode, func(t *testing.T) {
			reset(t)
			admin, runner, profile, _, mod := groupFixture(t)
			visible, _ := insertProjectRun(t, admin, runner, profile, "Visible")
			hidden, _ := insertProjectRun(t, admin, runner, profile, "Hidden")
			var group AccountGroup
			callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/groups", encoded(t, map[string]any{"harness": "codex", "name": "Work", "exclusive": mode == "exclusive-hidden-only", "project_ids": []string{visible, hidden}}), 201, &group)
			person := addPrincipal(t, admin.TenantID, "person", "partial-manager", nil)
			seed(t, admin, func(tx pgx.Tx) error {
				var manager, reader string
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'partial_manager','Partial manager') RETURNING id::text`, admin.TenantID).Scan(&manager); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'project_reader','Project reader') RETURNING id::text`, admin.TenantID).Scan(&reader); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'account.read'),($1,$2,'account.manage'),($1,$3,'nodes.read')`, admin.TenantID, manager, reader); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'workspace',NULL),($1,$2,$4,'project',$5)`, admin.TenantID, person.ID, manager, reader, visible)
				return err
			})
			var listed []AccountGroup
			callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/groups", "", 200, &listed)
			if len(listed) != 1 || len(listed[0].ProjectIDs) != 1 || listed[0].ProjectIDs[0] != visible {
				t.Fatal("fixture lacks partial visibility")
			}
			body := map[string]any{"name": "Renamed"}
			wantVisible := 1
			if mode == "visible-list" {
				body["project_ids"] = []string{visible}
			}
			if mode == "remove-visible" || mode == "exclusive-hidden-only" {
				body["project_ids"] = []string{}
				wantVisible = 0
			}
			var updated AccountGroup
			callStatus(t, mod, &person, "", "PATCH", "/api/agent-accounts/groups/"+group.ID, encoded(t, body), 200, &updated)
			if scalar(t, admin, `SELECT count(*) FROM account_group_projects WHERE group_id=$1 AND project_id=$2`, group.ID, hidden) != 1 {
				t.Fatal("partial manager deleted the hidden routing fence")
			}
			if len(updated.ProjectIDs) != wantVisible {
				t.Fatal("response leaked hidden membership or failed to replace visible projects")
			}
			if scalar(t, admin, `SELECT count(*) FROM account_group_projects WHERE group_id=$1 AND project_id=$2`, group.ID, visible) != int64(wantVisible) {
				t.Fatal("visible membership did not follow the patch")
			}
		})
	}
}
