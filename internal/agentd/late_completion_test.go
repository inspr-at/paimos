// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"errors"
	"testing"
	"time"
)

func TestTerminalControlCompletionReleasesQueue(t *testing.T) {
	s, a, e, p := managedFixture(t)
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	a.harnessCompletionErr = func(id, _, _ string) error {
		if id == "late" {
			return ErrControlTerminal
		}
		return nil
	}
	a.harnessControls = []HarnessControl{
		{ID: "late", Kind: "steer", Text: "late", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry},
		{ID: "next", Kind: "steer", Text: "next", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry},
	}
	if err := s.serviceHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	p.mu2.Lock()
	texts := append([]string(nil), p.texts...)
	p.mu2.Unlock()
	if len(texts) != 2 || texts[0] != "late" || texts[1] != "next" {
		t.Fatalf("texts %v", texts)
	}
	if len(e.pending) != 0 {
		t.Fatalf("pending %d", len(e.pending))
	}
	a.mu.Lock()
	completions := append([]string(nil), a.harnessCompletions...)
	a.mu.Unlock()
	if len(completions) != 1 || completions[0] != "next:applied:queued_next_turn" {
		t.Fatalf("completions %v", completions)
	}
	if err := s.serviceHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	p.mu2.Lock()
	defer p.mu2.Unlock()
	if len(p.texts) != 2 {
		t.Fatalf("replayed %v", p.texts)
	}
}

func TestNonTerminalControlCompletionBlocksQueue(t *testing.T) {
	s, a, e, p := managedFixture(t)
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	a.harnessCompletionErr = func(id, _, _ string) error {
		if id == "late" {
			return errors.New("api 409: control must be claimed")
		}
		return nil
	}
	a.harnessControls = []HarnessControl{
		{ID: "late", Kind: "steer", Text: "late", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry},
		{ID: "next", Kind: "steer", Text: "next", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry},
	}
	if err := s.serviceHarness(t.Context(), e); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal(err)
	}
	p.mu2.Lock()
	texts := append([]string(nil), p.texts...)
	p.mu2.Unlock()
	if len(texts) != 1 || texts[0] != "late" || len(e.pending) != 2 {
		t.Fatalf("texts %v pending %d", texts, len(e.pending))
	}
}
