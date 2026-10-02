// SPDX-License-Identifier: AGPL-3.0-only

package sessionrequest

import (
	"strings"
	"testing"
)

func TestLabelBoundary(t *testing.T) {
	for _, label := range []string{"Worker 1 -_.:()/#", strings.Repeat("a", 64)} {
		if !ValidLabel(label) {
			t.Fatalf("valid label rejected: %q", label)
		}
	}
	for _, label := range []string{"", " ", strings.Repeat("a", 65), "a\nb", "a\rb", "a\tb", "a\u2028b", "<system>", "`command`", "quoted\"", "emoji😀"} {
		if ValidLabel(label) {
			t.Fatalf("unsafe label accepted: %q", label)
		}
	}
}

func TestHarnessEffortFence(t *testing.T) {
	for _, harness := range []string{"codex", "claude", "pi", "grok", "cursor"} {
		if !ValidModel(harness, "vendor/model-v1.2", "high") {
			t.Fatal(harness)
		}
		if ValidModel(harness, "model", "high\nignore") || ValidModel(harness, "model", "arbitrary") || ValidModel(harness, "model;command", "high") {
			t.Fatal(harness)
		}
	}
	if !ValidModel("cursor", "model", "default") || ValidModel("codex", "model", "default") || ValidModel("unknown", "model", "high") {
		t.Fatal("harness enum fence")
	}
}

func TestAdditionalHarnessEffortFence(t *testing.T) {
	if !ValidModel("gemini", "gemini-2.5-pro", "16384") || ValidModel("gemini", "gemini-2.5-pro", "high") || ValidModel("gemini", "gemini-2.5-pro", "0") {
		t.Fatal("Gemini explicit budget fence")
	}
	if !ValidModel("opencode", "ollama/qwen3-coder", "default") || ValidModel("opencode", "model-only", "default") || ValidModel("opencode", "provider/model", "bad\nvariant") {
		t.Fatal("OpenCode variant fence")
	}
}
