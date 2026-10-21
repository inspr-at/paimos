// SPDX-License-Identifier: AGPL-3.0-only

// External test package: store imports nativesessions/consent, which imports
// nativequalification, so an in-package test importing store is a cycle.
package nativequalification_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/store"
)

// Risk: terminal URLs become agent-visible credentials, or admitting the
// configured Zellij port hides a host, credential or unlisted-port bypass.
// AEON-1293 admits :8082 on the default allowlist. Credential, foreign-host
// and unlisted-port refusals stay invalid_input and must not echo the URL.
func TestNativeLeadPersonOnlyViewerAndZ1Compatibility(t *testing.T) {
	url := "https://mbp.example.ts.net:8082/LEAD"
	view := nativecontract.SessionView{TerminalViewerURL: &url}
	person := nativecontract.PersonProjection(view, "person", true)
	agent := nativecontract.PersonProjection(view, "agent", true)
	if person.TerminalViewerURL == nil || *person.TerminalViewerURL != url || agent.TerminalViewerURL != nil {
		t.Fatal("person-only projection failed")
	}
	hosts, err := store.NewViewerHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hosts.Validate("https://mbp.example.ts.net/LEAD"); err != nil {
		t.Fatal("safe 443 publication refused", err)
	}
	if err := hosts.Validate(url); err != nil {
		t.Fatal("configured Zellij viewer port refused", err)
	}
	var failure *store.Failure
	if err := hosts.Validate("https://mbp.example.ts.net/LEAD?token=synthetic"); !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonInvalidInput || strings.Contains(err.Error(), "token=synthetic") {
		t.Fatal("credential URL admitted", err)
	}
	for _, raw := range []string{
		"https://mbp.example.ts.net:8083/LEAD",
		"https://example.org:8082/LEAD",
		"https://mbp.example.ts.net:8082/LEAD?token=synthetic",
		"https://person@mbp.example.ts.net:8082/LEAD",
		"https://mbp.example.ts.net:/LEAD",
	} {
		err := hosts.Validate(raw)
		if !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonInvalidInput || strings.Contains(err.Error(), raw) {
			t.Fatalf("unsafe viewer URL admitted or refused for wrong reason: %v", err)
		}
	}
}
