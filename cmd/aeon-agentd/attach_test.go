// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestAttachModeChoice(t *testing.T) {
	for _, tc := range []struct {
		name, input                string
		flag, statusOnly, rejected bool
	}{
		{name: "watch default", input: "\n"},
		{name: "watch chosen", input: "1\n"},
		{name: "status chosen", input: "2\n", statusOnly: true},
		{name: "status flag", flag: true, statusOnly: true},
		{name: "invalid", input: "WATCH\n", rejected: true},
		{name: "closed terminal", rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := promptAttachMode(t.Context(), bufio.NewReader(strings.NewReader(tc.input)), &out, tc.flag)
			if (err != nil) != tc.rejected || got != tc.statusOnly {
				t.Fatal("wrong mode decision")
			}
			if !strings.Contains(out.String(), "Status only (no conversation text)") || !tc.flag && !strings.Contains(out.String(), "Watch the conversation (default)") {
				t.Fatal("mode consequences hidden")
			}
		})
	}
}
