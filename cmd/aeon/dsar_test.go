// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDSARRejectsDestructiveOrUnscopedCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"other"}, {"dsar"}, {"dsar", "erase"}, {"dsar", "erase", "--person", "person@example.test", "--apply"}, {"dsar", "export", "--person", "person@example.test"}} {
		var out bytes.Buffer
		if err := dsarCommand(args, &out); err == nil || !strings.Contains(err.Error(), "usage:") || out.Len() != 0 {
			t.Fatalf("command %q: %v", args, err)
		}
	}
}
