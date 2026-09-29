// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestPairedPiAdapterBinding(t *testing.T) {
	c := agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{{Harness: "pi", Key: "local", AccountID: "account", Home: "/private/pi-profile", Identity: "anthropic", Path: "/pinned/pi"}}}
	accounts, adapters, err := pairedAdapters(c)
	if err != nil || len(accounts) != 1 || len(adapters) != 1 {
		t.Fatal("pi adapter missing", err)
	}
	pi, ok := adapters[0].(*agentd.PiAdapter)
	if !ok || pi.Providers["local"] != "anthropic" || pi.Homes["local"] != c.Accounts[0].Home || pi.Path != c.Accounts[0].Path || accounts[0].Harness != "pi" {
		t.Fatal("pi binding changed")
	}
	if pi.VerificationSupported() {
		t.Fatal("unqualified verification advertised")
	}
	c.Accounts[0].Identity = ""
	if _, _, err := pairedAdapters(c); err == nil {
		t.Fatal("missing pi provider accepted")
	}
}
