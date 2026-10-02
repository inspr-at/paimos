// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTierControlCannotUseLocalSocketAndFencesOwnership(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.managedPolicy = false
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "tier-change", Operation: "tier", Value: "fast", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry}
	if _, err := s.Control(context.Background(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local socket escalated tier: %v", err)
	}
	identity.Generation = "wrong"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrGeneration) {
		t.Fatal(err)
	}
	identity.Generation = s.generation
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal("idempotent tier receipt lost", err)
	}
}

func TestCodexTierRequiresIdleSafePoint(t *testing.T) {
	p := &codexProcess{}
	// No native traffic or process access is allowed during an active turn.
	p.wireProcess = &wireProcess{}
	if err := p.changeTier(t.Context(), "fast"); !errors.Is(err, ErrSettingRejected) {
		t.Fatal(err)
	}
	if err := p.changeTier(t.Context(), "ultra"); !errors.Is(err, ErrSettingRejected) {
		t.Fatal(err)
	}
}
