// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func TestUnsignedOrUnsupportedLocalAuthenticationFailsClosed(t *testing.T) {
	// A test executable is neither the signed daemon nor a supported context.
	// This must return without ever opening a system prompt.
	err := (systemLocalAuthenticator{}).Confirm(t.Context(), "Allow watching fixture session on fixture host")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("unsigned/unsupported build did not fail closed")
	}
}

func TestLocalAuthCapabilityReportsWithoutPrompt(t *testing.T) {
	got := make(chan string, 1)
	go func() { got <- CurrentLocalAuthCapability() }()
	select {
	case capability := <-got:
		if !attachwatch.LocalAuthCapabilityReported(capability) || capability == attachwatch.LocalAuthAvailable {
			t.Fatalf("capability %q", capability)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("local auth capability blocked")
	}
}
