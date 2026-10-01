// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type profileProbe struct{ calls []Command }

func (p *profileProbe) Run(_ context.Context, c Command) ([]byte, error) {
	p.calls = append(p.calls, c)
	if len(c.Args) == 1 && c.Args[0] == "--version" {
		return []byte("1.14.48\n"), nil
	}
	return nil, errors.New("no authentication or model calls permitted")
}

func TestAdditionalHarnessDiscoveryIsExplicitLocalProfile(t *testing.T) {
	for _, name := range []string{"gemini", "opencode"} {
		t.Run(name, func(t *testing.T) {
			home := physicalTemp(t)
			workspace := physicalTemp(t)
			path := filepath.Join(home, name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			probe := &profileProbe{}
			d := Discovery{Home: home, Workspace: workspace, Executor: probe, LookPath: func(string) (string, error) { return path, nil }}
			c, err := d.Detect(t.Context(), name, "")
			if err != nil || c.Home != home || c.Login != "local_profile" || c.Identity != "local-profile" || !candidateReady(c) || len(probe.calls) != 1 {
				t.Fatal("profile discovery", c, err, len(probe.calls))
			}
			if _, err := d.Detect(t.Context(), name, "person@example.test"); err == nil {
				t.Fatal("person identity invented")
			}
			c.Login = "signed_out"
			if candidateReady(c) {
				t.Fatal("signed-out profile enrolled")
			}
		})
	}
}
