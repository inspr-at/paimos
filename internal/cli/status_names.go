// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/statusnames"
)

// The workspace's own words for statuses (AEON-1401) in the CLI's human output.
// --json, filters and every write keep the canonical state. The read is
// best effort and bounded: an older server, a missing permission or a slow
// answer prints the canonical state as before.
func (rt *runtime) statusNames() statusnames.Names {
	if rt.jsonOut {
		return nil
	}
	if rt.names != nil {
		return *rt.names
	}
	names := statusnames.Names{}
	ctx, cancel := context.WithTimeout(rt.context(), 5*time.Second)
	defer cancel()
	var out struct {
		Names statusnames.Names `json:"names"`
	}
	if err := rt.doCtx(ctx, http.MethodGet, "/api/settings/status-names", nil, &out); err == nil && out.Names != nil {
		names = out.Names
	}
	rt.names = &names
	return names
}

// humanStatus is a state as people read it: the workspace's English name with
// the stored state after it, so the canonical value stays greppable.
func (rt *runtime) humanStatus(state string) string {
	if name := rt.statusNames().Renamed(state, "en"); name != "" {
		return name + " (" + state + ")"
	}
	return state
}

// listStatus is the STATUS column: the workspace's name, else the stored state.
func (rt *runtime) listStatus(state string) string {
	if name := rt.statusNames().Renamed(state, "en"); name != "" {
		return name
	}
	return state
}
