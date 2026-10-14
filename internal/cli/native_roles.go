// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeroles"
)

// nativeRoleRecipe is a read-only `session role-recipe` mount for I2, which
// owns compat_cmds.go. It needs no login, socket, vendor account or model call.
func (rt *runtime) nativeRoleRecipe() *Command {
	return &Command{Name: "role-recipe", Short: "Show a separately approved native coordinator rollout recipe", Use: "session role-recipe Tick|OPS|AIT", minArgs: 1, maxArgs: 1,
		run: func(args []string) error {
			r, err := nativeroles.Lookup(args[0])
			if err != nil {
				return usagef("role-recipe requires Tick, OPS or AIT")
			}
			if rt.jsonOut {
				return rt.printJSON(r)
			}
			fmt.Fprintf(rt.stdout, "%s: separate native cutover approval required\nWorkspace: %s\nAccount: %s\nTools: %s\nPairing: %s\nTerminal: %s\n", r.Role, r.Workspace, r.Account, r.Tools, r.Pairing, r.TerminalViewer)
			for _, grant := range r.RequiredGrants {
				fmt.Fprintf(rt.stdout, "Required grant: %s\n", grant)
			}
			for _, evidence := range r.RequiredEvidence {
				fmt.Fprintf(rt.stdout, "Required evidence: %s\n", evidence)
			}
			for _, attach := range r.RemoteAttach {
				fmt.Fprintf(rt.stdout, "Remote attach: %s\n", attach)
			}
			for _, step := range r.Cutover {
				fmt.Fprintf(rt.stdout, "Cutover: %s\n", step)
			}
			for _, step := range r.Rollback {
				fmt.Fprintf(rt.stdout, "Rollback: %s\n", step)
			}
			fmt.Fprintln(rt.stdout, r.OperatorBoundary)
			return nil
		},
	}
}

// switchNativeRole is I2's local seam. Scope and the fenced resolver come from
// authenticated pairing/role approval, never the displayed recipe or flags.
func switchNativeRole(ctx context.Context, roles *nativeroles.Switcher, scope nativeroles.Scope, request nativeroles.Request) (nativecontract.OperationResult, error) {
	if roles == nil {
		return nativecontract.OperationResult{}, &nativecontract.ValidationError{Reason: nativecontract.ReasonQualificationMissing, Path: "$"}
	}
	return roles.Switch(ctx, scope, request)
}
