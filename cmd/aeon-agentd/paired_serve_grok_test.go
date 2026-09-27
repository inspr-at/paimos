// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestPairedAdaptersRetainPrivateGrokBinding(t *testing.T) {
	b := agentd.GrokBinding{Variant: "npm-grok-1.0.30", BinaryPath: "/synthetic/node_modules/@xai-official/grok/bin/grok-native", AuthPath: "/synthetic/.grok/auth.json", ScratchRoot: "/synthetic/private-scratch", PrincipalSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	c := agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{{Harness: agentd.Grok, Key: "key", AccountID: "account", Path: b.BinaryPath, Identity: b.PrincipalSHA256, Grok: b}}}
	accounts, adapters, err := pairedAdapters(c)
	if err != nil || len(accounts) != 1 || len(adapters) != 1 {
		t.Fatal("paired Grok adapter unavailable")
	}
	g, ok := adapters[0].(*agentd.GrokAdapter)
	if !ok || g.Bindings["key"] != b {
		t.Fatal("private binding was not retained")
	}
	c.Accounts[0].Identity = "different"
	if _, _, err := pairedAdapters(c); err == nil {
		t.Fatal("principal mismatch accepted")
	}
}
