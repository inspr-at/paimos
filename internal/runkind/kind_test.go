// SPDX-License-Identifier: AGPL-3.0-only
package runkind

import (
	"strings"
	"testing"
)

func TestExecutionLabels(t *testing.T) {
	for _, tc := range []struct{ harness, generator, command, errorFlag string }{
		{"codex", "", "", ""}, {"claude", "", "", ""}, {"pi", "", "", ""}, {"cursor", "", "", ""}, {"grok", "", "", ""},
		{"gemini", "", "", ""}, {"opencode", "", "", ""},
		{"media", "higgsfield/kling3_0", "", ""}, {"terminal", "", "npm run build", ""},
		{"higgsfield", "", "", "--harness"}, {"tool", "", "", "--harness"},
		{"media", "", "", "--generator"}, {"media", "veo\n3", "", "--generator"},
		{"media", strings.Repeat("a", 121), "", "--generator"}, {"media", "a b", "", "--generator"},
		{"terminal", "", "", "--command"}, {"terminal", "", " ffmpeg", "--command"},
		{"terminal", "", "ffmpeg ", "--command"}, {"terminal", "", "ffmpeg;curl", "--command"},
		{"codex", "veo", "", "--generator"}, {"media", "veo", "ffmpeg", "--command"},
	} {
		t.Run(tc.harness+"/"+tc.generator+"/"+tc.command, func(t *testing.T) {
			err := Validate(tc.harness, tc.generator, tc.command)
			if tc.errorFlag == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errorFlag) {
				t.Fatalf("want %s error, got %v", tc.errorFlag, err)
			}
			if tc.errorFlag == "--harness" && !strings.Contains(err.Error(), Accepted) {
				t.Fatal("accepted families missing")
			}
		})
	}
}
