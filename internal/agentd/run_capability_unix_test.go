// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

// Risk: explicit adapter/resource environments reintroduce the transport key
// after an earlier scrub. Observe the launched child's environment through a
// fixed verdict, with a FIFO barrier before the assertion and no secret output.
func TestRunChildLaunchDropsTransportCredentials(t *testing.T) {
	verdict := filepath.Join(t.TempDir(), "verdict")
	fixture := processtest.New(t, func(child string) string {
		return fmt.Sprintf(`if [ -z "${AEON_RUNTIME_KEY:-}" ] && [ -z "${ALIASED_CREDENTIAL:-}" ]; then printf clean >%q; else printf leaked >%q; fi; %s`, verdict, verdict, child)
	})
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	p, err := launchWire(shell, []string{"-c", fixture.Script}, t.TempDir(), []string{"PATH=/usr/bin:/bin", "AEON_RUNTIME_KEY=synthetic-parent-secret", "ALIASED_CREDENTIAL=aeon_synthetic_transport_credential"}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		fixture.ExitChild()
		fixture.ExitRoot()
		select {
		case <-p.waitDone:
		case <-time.After(5 * time.Second):
			t.Error("fixture launcher did not exit")
		}
		p.discardReader()
		fixture.AssertExited(t)
	}()
	fixture.Ready(t)
	got, err := os.ReadFile(verdict)
	if err != nil || string(got) != "clean" {
		t.Fatal("launched child inherited transport authority")
	}
}
