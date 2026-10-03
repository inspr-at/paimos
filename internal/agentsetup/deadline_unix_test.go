// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentsetup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

func TestAccountProbeInheritedPipes(t *testing.T) {
	for _, protocol := range []bool{false, true} {
		for _, cancelRoot := range []bool{false, true} {
			name := map[bool]string{false: "executor", true: "identity"}[protocol] + "/" + map[bool]string{false: "exit", true: "cancel"}[cancelRoot]
			t.Run(name, func(t *testing.T) {
				fixture := processtest.New(t)
				path := filepath.Join(physicalTemp(t), "probe")
				if err := os.WriteFile(path, []byte("#!/bin/sh\n"+fixture.Script+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if protocol {
						_, err := CodexIdentity(ctx, path, filepath.Dir(path))
						done <- err
					} else {
						_, err := (OSExecutor{}).Run(ctx, Command{Path: path})
						done <- err
					}
				}()
				fixture.Ready(t)
				if cancelRoot {
					cancel()
				} else {
					fixture.ExitRoot()
				}
				select {
				case err := <-done:
					if (protocol || cancelRoot) && err == nil {
						t.Error("incomplete probe reported success")
					}
				case <-time.After(3 * time.Second):
					t.Error("probe stuck on inherited pipe")
				}
				fixture.AssertExited(t)
			})
		}
	}
}
