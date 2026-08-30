// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"strings"
	"testing"
)

func TestBusInstanceIdentityRejectsProductionDefault(t *testing.T) {
	for _, value := range []string{"", "default"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", value)
			if err := ValidateInstanceIdentity("ppm", true); err == nil || !strings.Contains(err.Error(), "non-default") {
				t.Fatalf("err=%v, want production default refusal", err)
			}
		})
	}
}

func TestBusInstanceIdentityRejectsConfiguredMismatch(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "pma")
	if err := ValidateInstanceIdentity("ppm", true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err=%v, want configured mismatch refusal", err)
	}
}

func TestBusInstanceIdentityAcceptsMatchingProductionInstance(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	if err := ValidateInstanceIdentity("ppm", true); err != nil {
		t.Fatal(err)
	}
}
