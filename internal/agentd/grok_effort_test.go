// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"slices"
	"testing"
)

// Risk: a registered effort is rejected or silently upgraded to xhigh by the
// confined launcher. Exercise its actual arguments without vendor processes.
func TestGrokNativeEffortPassesSelectedProfile(t *testing.T) {
	for _, effort := range []string{"medium", "high", "xhigh"} {
		if !validGrokEffort(effort) {
			t.Fatal("registered effort rejected", effort)
		}
		args := grokNativeArguments("sandbox", "binary", "work", "profile", effort)
		index := slices.Index(args, "--reasoning-effort")
		if index < 0 || args[index+1] != effort {
			t.Fatal("selected effort changed", args)
		}
		for _, flag := range []string{"--no-subagents", "--disable-web-search", "--no-memory", "--no-leader"} {
			if !slices.Contains(args, flag) {
				t.Fatal("confinement flag lost", flag)
			}
		}
	}
	for _, effort := range []string{"", "low", "default", "max", "high --unsafe"} {
		if validGrokEffort(effort) {
			t.Fatal("unsupported effort admitted", effort)
		}
	}
}
