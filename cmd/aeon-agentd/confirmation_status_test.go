// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestConfirmationStatusPrintsReadinessAndUpgradeCommand(t *testing.T) {
	for _, confirmation := range []string{"ready (pairing key pinned)", "needs pairing upgrade"} {
		p := agentsetup.Progress{Stage: "connected", TouchIDConfirmation: confirmation}
		if confirmation == "needs pairing upgrade" {
			p.TouchIDUpgradeCommand = "aeon-agentd pair --url 'https://paired.test' --state-root '/tmp/pair-touch-id' --workspace '/tmp/workspace'"
		}
		var out bytes.Buffer
		if err := printSetupProgress(&out, false, p); err != nil {
			t.Fatal(err)
		}
		line := "Touch ID confirmation: " + confirmation
		if p.TouchIDUpgradeCommand != "" {
			line += ": " + p.TouchIDUpgradeCommand
		}
		if !strings.Contains(out.String(), line+"\n") {
			t.Fatal("status omitted confirmation prerequisite")
		}
		out.Reset()
		if err := printSetupProgress(&out, true, p); err != nil {
			t.Fatal(err)
		}
		var got agentsetup.Progress
		if json.Unmarshal(out.Bytes(), &got) != nil || got.TouchIDConfirmation != confirmation || got.TouchIDUpgradeCommand != p.TouchIDUpgradeCommand {
			t.Fatal("JSON status lost prerequisite")
		}
	}
}
