// SPDX-License-Identifier: AGPL-3.0-only
package agentactivity

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolObservationsNeverForwardArguments(t *testing.T) {
	cases := []struct{ tool, target, command, want string }{
		{"Edit", "/workspace/internal/api.go", "", "Editing api.go"},
		{"Write", "/workspace/.env", "", "Editing code"},
		{"Edit", "https://user:credential.test/private.go", "", "Editing code"},
		{"Edit", "id_ed25519.go", "", "Editing code"},
		{"Edit", "opaque01234567890123456789012345.go", "", "Editing code"},
		{"Read", "/private/credentials", "", "Reading code"},
		{"Bash", "", "GOMAXPROCS=2 go test ./... -args PRIVATE_ARGUMENT", "Running Go tests"},
		{"Bash", "", "npm run test:unit -- PRIVATE_ARGUMENT", "Running web tests"},
		{"Bash", "", "npx playwright test tests/example.spec.ts", "Running browser tests"},
		{"Bash", "", "git commit -m PRIVATE_ARGUMENT", "Committing"},
		{"Bash", "", "git push https://user:credential.test/repo", "Pushing"},
		{"Bash", "", "gh pr checks --watch", "Waiting for CI"},
		{"Bash", "", "git worktree remove /workspace/private", "Cleaning up"},
		{"Bash", "", "curl -H PRIVATE_ARGUMENT https://private.test", "Working"},
		{"PRIVATE_ARGUMENT", "", "PRIVATE_ARGUMENT", "Working"},
	}
	for _, tc := range cases {
		t.Run(tc.want+tc.tool, func(t *testing.T) {
			got := ToolText(tc.tool, tc.target, tc.command)
			if got != tc.want || !ValidAuto(got) {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
	for _, bad := range []string{"Editing ", "Editing ../api.go", "Editing .env", "Editing secret.go", "Running Go tests PRIVATE_ARGUMENT", "PRIVATE_ARGUMENT"} {
		if ValidAuto(bad) {
			t.Fatalf("accepted arbitrary automatic text %q", bad)
		}
	}
}

func TestSummaryBoundaries(t *testing.T) {
	for _, text := range []string{"Implementing agent activity", "Waiting for CI", strings.Repeat("é", 60)} {
		if _, valid := CleanSummary(text); !valid {
			t.Fatalf("rejected public summary %q", text)
		}
	}
	for _, text := range []string{"", "  ", strings.Repeat("é", 61), "Tests\nfinished", "Using API_KEY=PRIVATE_VALUE", "Bearer PRIVATE_VALUE", "Reading /private/file", "PRIVATE_OPAQUE_VALUE_1234567890123456", "Visit https://private.test", "$(PRIVATE_VALUE)"} {
		if _, valid := CleanSummary(text); valid {
			t.Fatalf("accepted unsafe summary %q", text)
		}
	}
}

func TestTranscriptOnlyClassifiesToolEnvelopes(t *testing.T) {
	for _, raw := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","env":{"PRIVATE":"VALUE"}}}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\"}"}}`,
	} {
		a := TranscriptTool([]byte(raw))
		if a == nil || a.Text != "Running Go tests" || a.Source != "auto" {
			t.Fatalf("tool projection: %+v", a)
		}
		encoded, _ := json.Marshal(a)
		if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "./...") {
			t.Fatal("raw input escaped projection")
		}
	}
	for _, raw := range []string{`{"type":"user","message":{"content":"PRIVATE_CONTENT"}}`, `{"type":"assistant","parent_tool_use_id":"child","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`, `{`} {
		if TranscriptTool([]byte(raw)) != nil {
			t.Fatal("non-owned tool envelope projected")
		}
	}
	if got := FromInput("mcp__aeon__aeon_terminal", json.RawMessage(`{"command":"go","args":["test","./..."],"environment":{"PRIVATE":"VALUE"}}`)); got != "Running Go tests" {
		t.Fatalf("managed terminal: %s", got)
	}
}
