// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
)

// agentKeyCommand runs the operator-only agent key commands:
//
//	paimos agent-key create --tenant SLUG --name AGENT --creator-id UUID --out-file PATH [--scopes a,b] [--expires 720h] [--workspace-role ROLEKEY]
//	paimos agent-key create --tenant SLUG --principal-id UUID --creator-id UUID --out-file PATH [--name LABEL] [--scopes a,b]
//	paimos agent-key revoke --tenant SLUG --id KEY_ID
//
// The token is written only to --out-file (created with mode 0600, never
// overwritten) and is never printed.
func agentKeyCommand(args []string, stdout io.Writer) error {
	const usage = "usage: paimos agent-key create --tenant SLUG (--name AGENT | --principal-id UUID) --creator-id UUID --out-file PATH [--scopes a,b] [--expires DURATION] [--workspace-role ROLEKEY] [--project KEY --project-role ROLEKEY]... | paimos agent-key revoke --tenant SLUG --id KEY_ID"
	if len(args) == 0 || (args[0] != "create" && args[0] != "revoke") {
		return errors.New(usage)
	}
	flags := flag.NewFlagSet("paimos agent-key "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tenantSlug := flags.String("tenant", "", "tenant slug")
	name := flags.String("name", "", "agent or key name")
	principalID := flags.String("principal-id", "", "existing agent principal")
	creatorID := flags.String("creator-id", "", "person creator UUID (required for create)")
	outFile := flags.String("out-file", "", "file to write the key to (0600, must not exist)")
	scopes := flags.String("scopes", "", "comma-separated scopes")
	expires := flags.Duration("expires", 0, "lifetime, e.g. 720h (default: no expiry)")
	workspaceRole := flags.String("workspace-role", "", "workspace role key for the agent")
	id := flags.String("id", "", "key id (revoke)")
	var projects, roles projectFlags
	flags.Var(&projects, "project", "project key (repeat with --project-role, or KEY=ROLE[,KEY=ROLE])")
	flags.Var(&roles, "project-role", "role key for paired --project")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *tenantSlug == "" {
		return errors.New(usage)
	}
	if args[0] == "create" && (*creatorID == "" || *outFile == "" || (*name == "" && *principalID == "") || *id != "") ||
		args[0] == "revoke" && (*id == "" || *name != "" || *principalID != "" || *outFile != "" || *scopes != "" || *expires != 0) {
		return errors.New(usage)
	}
	pairs, err := agentProjectRoles(projects, roles)
	if err != nil || args[0] != "create" && (len(pairs) > 0 || *workspaceRole != "") {
		return errors.New(usage)
	}
	return withPool(func(ctx context.Context, pool *pgxpool.Pool) error {
		tenantID, err := operatorTenantID(ctx, pool, *tenantSlug)
		if err != nil {
			return err
		}
		if args[0] == "revoke" {
			if err := auth.OperatorRevokeAgentKey(ctx, pool, tenantID, *id); err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(map[string]string{"revoked": *id})
		}
		if err := authz.OperatorValidateProjectRoles(ctx, pool, tenantID, pairs); err != nil {
			return err
		}
		if err := authz.OperatorValidateWorkspaceRole(ctx, pool, tenantID, *workspaceRole); err != nil {
			return err
		}
		f, err := os.OpenFile(*outFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("out-file: %w", err)
		}
		var exp *time.Time
		if *expires > 0 {
			t := time.Now().Add(*expires)
			exp = &t
		}
		var list []string
		for _, s := range strings.Split(*scopes, ",") {
			if s = strings.TrimSpace(s); s != "" {
				list = append(list, s)
			}
		}
		list, err = auth.NormalizeScopes(list)
		if err != nil {
			f.Close()
			os.Remove(*outFile)
			return err
		}
		keyID, agentID, token, err := auth.OperatorCreateAgentKey(ctx, pool, tenantID, *name, *principalID, list, exp, *creatorID)
		if err != nil {
			f.Close()
			os.Remove(*outFile)
			return err
		}
		// No trailing newline: consumers read the file verbatim into an
		// Authorization header, and a newline there made an HTTP client echo
		// the header (with the token) in its error (2026-09-26).
		if _, err := f.WriteString(token); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if len(pairs) > 0 {
			if _, err := authz.OperatorBindProjects(ctx, pool, tenantID, agentID, pairs); err != nil {
				return fmt.Errorf("key %s created in %s, but project binding failed: %w", keyID, *outFile, err)
			}
		}
		if *workspaceRole != "" {
			if err := authz.OperatorBindWorkspaceRole(ctx, pool, tenantID, agentID, *workspaceRole); err != nil {
				return fmt.Errorf("key %s created in %s, but workspace binding failed: %w", keyID, *outFile, err)
			}
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"id": keyID, "principal_id": agentID, "name": *name, "scopes": list, "file": *outFile, "workspace_role": *workspaceRole})
	})
}
