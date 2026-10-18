// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Resolve lazily: unmarked keys and ordinary calls never open a local socket.
// Each instance may choose its own state root; the daemon verifies its pairing,
// and the wire client checks the origin before requesting any confirmation.
func localStepUp(origin, root string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, id string) (string, error) {
		state := root
		if state == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errors.New("local pairing location unavailable")
			}
			state, err = agentsetup.DefaultStateRoot(goruntime.GOOS, home, os.Getenv("XDG_STATE_HOME"))
			if err != nil {
				return "", errors.New("local pairing location unavailable")
			}
		}
		if !filepath.IsAbs(state) {
			return "", errors.New("agentd_state_root must be an absolute pairing path")
		}
		c, err := agentdwire.OpenClient(filepath.Join(state, "daemon"))
		if err != nil {
			return "", errors.New("local paired agentd unavailable; check aeon-agentd status and this instance's agentd_state_root")
		}
		return c.ConfirmStepUp(ctx, origin, id)
	}
}
