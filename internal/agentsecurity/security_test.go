// SPDX-License-Identifier: AGPL-3.0-only
package agentsecurity

import (
	"strings"
	"testing"
)

func TestLocalSignReasonRejectsEmptyAndControlText(t *testing.T) {
	if localSignReason("") == nil || localSignReason("line\nbreak") == nil || localSignReason(strings.Repeat("a", 257)) == nil {
		t.Fatal("unsafe Touch ID reason accepted")
	}
	if err := localSignReason("Allow watching the conversation codex session PID 40 on Studio Mac"); err != nil {
		t.Fatal(err)
	}
}
