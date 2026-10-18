// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestAgentProjectRoleFlags(t *testing.T) {
	for _, tc := range []struct {
		projects, roles projectFlags
		count           int
	}{
		{projectFlags{"A-1", "B-1"}, projectFlags{"guest", "member"}, 2},
		{projectFlags{"A-1=guest,B-1=member"}, nil, 2},
	} {
		pairs, err := agentProjectRoles(tc.projects, tc.roles)
		if err != nil || len(pairs) != tc.count {
			t.Fatalf("pairs=%+v err=%v", pairs, err)
		}
	}
	if _, err := agentProjectRoles(projectFlags{"A-1"}, projectFlags{"guest", "member"}); err == nil {
		t.Fatal("unmatched project role accepted")
	}
}

func TestOperatorCLIKeyCreateAndAccess(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	t.Setenv("AEON_DATABASE_URL", d.AppURL)
	t.Setenv("AEON_ENV", "dev")
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('ab1-cli','AB1 CLI') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,state)
			SELECT $1::uuid,'JANUS-1',id,'Janus','active' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'`, tid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	creatorID := dbtest.KeyPerson(t, d.App, tid)
	file := filepath.Join(t.TempDir(), "agent-key")
	var stdout bytes.Buffer
	badFile := filepath.Join(t.TempDir(), "invalid-key")
	if err := agentKeyCommand([]string{"create", "--tenant", "ab1-cli", "--creator-id", creatorID, "--name", "invalid-worker", "--out-file", badFile, "--project", "JANUS-1", "--project-role", "owner"}, &stdout); err == nil {
		t.Fatal("workspace owner role accepted on project")
	}
	if _, err := os.Stat(badFile); !os.IsNotExist(err) {
		t.Fatalf("invalid role created a key file: %v", err)
	}
	for _, role := range []string{"owner", "guest"} {
		badFile = filepath.Join(t.TempDir(), "invalid-workspace-key")
		if err := agentKeyCommand([]string{"create", "--tenant", "ab1-cli", "--creator-id", creatorID, "--name", "invalid-worker", "--out-file", badFile, "--workspace-role", role}, &stdout); err == nil {
			t.Fatalf("workspace %s accepted", role)
		}
		if _, err := os.Stat(badFile); !os.IsNotExist(err) {
			t.Fatalf("invalid workspace role created key file: %v", err)
		}
	}
	if err := agentKeyCommand([]string{"create", "--tenant", "ab1-cli", "--creator-id", creatorID, "--name", "janus-worker", "--out-file", file, "--scopes", "nodes:read", "--workspace-role", "member", "--project", "JANUS-1", "--project-role", "guest"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var created struct {
		PrincipalID string `json:"principal_id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil || created.PrincipalID == "" {
		t.Fatalf("key metadata: %v", err)
	}
	if strings.Contains(stdout.String(), "aeon_") {
		t.Fatal("key token appeared on stdout")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", info, err)
	}
	var bound, workspaceBound int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE scope_type='project'),count(*) FILTER (WHERE scope_type='workspace') FROM role_bindings WHERE principal_id=$1::uuid`, created.PrincipalID).Scan(&bound, &workspaceBound)
	}); err != nil || bound != 1 || workspaceBound != 1 {
		t.Fatalf("key bindings: project=%d workspace=%d err=%v", bound, workspaceBound, err)
	}
	stdout.Reset()
	if err := accessCommand([]string{"bind", "--tenant", "ab1-cli", "--principal", "janus-worker", "--project", "JANUS-1", "--role", "guest"}, &stdout); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := accessCommand([]string{"unbind", "--tenant", "ab1-cli", "--principal", created.PrincipalID, "--project", "JANUS-1"}, &stdout); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := accessCommand([]string{"bind", "--tenant", "ab1-cli", "--principal", created.PrincipalID, "--workspace-role", "member"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if err := accessCommand([]string{"unbind", "--tenant", "ab1-cli", "--principal", created.PrincipalID, "--workspace-role"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if err := accessCommand([]string{"unbind", "--tenant", "ab1-cli", "--principal", created.PrincipalID, "--workspace-role"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if err := accessCommand([]string{"bind", "--tenant", "ab1-cli", "--principal", created.PrincipalID, "--workspace-role", "owner"}, &stdout); err == nil {
		t.Fatal("operator granted owner")
	}
	var setEvents, removeEvents int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE e.type='binding.set'), count(*) FILTER (WHERE e.type='binding.removed')
			FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
			WHERE e.tenant_id=$1::uuid AND p.name='Access operator' AND p.kind='agent' AND p.roles=ARRAY['operator']::text[]`, tid).Scan(&setEvents, &removeEvents)
	}); err != nil || setEvents != 1 || removeEvents != 1 {
		t.Fatalf("binding events set=%d removed=%d err=%v", setEvents, removeEvents, err)
	}
	var workspaceEvents int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
			WHERE e.tenant_id=$1::uuid AND e.type='authz.workspace_role_changed' AND p.name='Access operator'`, tid).Scan(&workspaceEvents)
	}); err != nil || workspaceEvents != 2 {
		t.Fatalf("workspace events=%d err=%v", workspaceEvents, err)
	}
}
