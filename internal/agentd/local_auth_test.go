// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"strings"
	"testing"
)

func TestUnsignedOrUnsupportedLocalAuthenticationFailsClosed(t *testing.T) {
	// A test executable is neither the signed daemon nor a supported context.
	// This must return without ever opening a system prompt.
	err := (systemLocalAuthenticator{}).Confirm(t.Context(), "Allow watching fixture session on fixture host")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("unsigned/unsupported build did not fail closed")
	}
}
