// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestSupervisorStartupErrorsNameFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "workspace")
	_, err = NewSupervisor(t.Context(), Config{API: &fakeAPI{}, DaemonID: "diagnostic", StateRoot: root, Workspace: missing})
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "workspace") {
		t.Fatal("missing workspace lost path or cause", err)
	}
	state, err := agentsetup.OpenStore(filepath.Join(root, "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	s := &Supervisor{state: state, daemonID: "diagnostic", tenantID: "tenant", principalID: "agent"}
	for _, tc := range []struct {
		name string
		load func() error
	}{
		{s.capacityChecksName(), s.loadCapacityChecks},
		{s.capacityStateName(), s.loadCapacityCaptures},
	} {
		if err := tc.load(); err != nil {
			t.Fatal("missing optional startup file must remain valid", err)
		}
		if err := state.Write(tc.name, []byte("private fixture contents"), true); err != nil {
			t.Fatal(err)
		}
		err := tc.load()
		if err == nil || !strings.Contains(err.Error(), filepath.Join(state.Path(), tc.name)) || !strings.Contains(err.Error(), "invalid capacity") || strings.Contains(err.Error(), "private fixture contents") {
			t.Fatal("invalid capacity file lacks safe path diagnostic", err)
		}
	}
}
