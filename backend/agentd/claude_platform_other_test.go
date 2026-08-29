//go:build paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeOwnedSessionFailsClosedWithoutProcessGroups(t *testing.T) {
	adapter := NewClaudeAdapter("/operator/claude", "/runtime/node", "/operator/sdk.mjs")
	_, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "process-group ownership") {
		t.Fatalf("err=%v", err)
	}
}
