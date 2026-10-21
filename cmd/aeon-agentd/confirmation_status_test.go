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
	missingEntitlement := "Touch ID unavailable: this agentd build lacks the keychain entitlement (AEON-1504). Pairing again will not fix it; Aeon approval is used meanwhile."
	for _, confirmation := range []string{"ready (pairing key pinned)", "needs pairing upgrade", missingEntitlement} {
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
		if confirmation == missingEntitlement {
			if strings.Count(out.String(), missingEntitlement) != 1 || strings.Count(out.String(), "keychain entitlement") != 1 {
				t.Fatal("status repeated the entitlement diagnostic")
			}
			for _, advice := range []string{"needs pairing upgrade", "aeon-agentd pair", "-touch-id", "new, empty state folder"} {
				if strings.Contains(out.String(), advice) {
					t.Fatal("status suggested re-pairing for a build defect")
				}
			}
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
