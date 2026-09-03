// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountLabelsUseOnlyFixedDocumentedProbeShapes(t *testing.T) {
	codex := writeProbe(t, `#!/bin/sh
[ "$1:$2" = "login:status" ] || exit 9
printf 'Logged in using ChatGPT\n'
`)
	if got := NewCodexAdapter(codex, "test").AccountLabel(context.Background()); got != "chatgpt" {
		t.Fatalf("Codex account label = %q", got)
	}
	claude := writeProbe(t, `#!/bin/sh
[ "$1:$2:$3" = "auth:status:--json" ] || exit 9
printf '%s\n' '{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max","email":"must-not-be-recorded@example.invalid"}'
`)
	if got := NewClaudeAdapter(claude, "/bin/false", "/tmp/unused").AccountLabel(context.Background()); got != "claude_ai_max" {
		t.Fatalf("Claude account label = %q", got)
	}
}

func TestCodexAPIKeyAccountLabelIgnoresNonSecretSuffix(t *testing.T) {
	probe := writeProbe(t, `#!/bin/sh
[ "$1:$2" = "login:status" ] || exit 9
printf 'Logged in using an API key - suffix\n'
`)
	if got := NewCodexAdapter(probe, "test").AccountLabel(context.Background()); got != "api_key" {
		t.Fatalf("account label = %q", got)
	}
}

func TestAccountLabelsFailClosedOnUnknownOrOversizedOutput(t *testing.T) {
	unknown := writeProbe(t, "#!/bin/sh\nprintf 'private@example.invalid\\n'\n")
	if got := NewCodexAdapter(unknown, "test").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("unknown Codex output became %q", got)
	}
	oversized := writeProbe(t, "#!/bin/sh\nprintf '"+strings.Repeat("x", 1100)+"'\n")
	if got := NewClaudeAdapter(oversized, "/bin/false", "/tmp/unused").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("oversized Claude output became %q", got)
	}
}

func writeProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
