// SPDX-License-Identifier: AGPL-3.0-only
package agentactivity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToolObservationsNeverForwardArguments(t *testing.T) {
	cases := []struct{ tool, target, command, want string }{
		{"Edit", "/workspace/internal/api.go", "", "Editing api.go"},
		{"Write", "/workspace/.env", "", "Editing code"},
		{"Edit", "https://user:credential.test/private.go", "", "Editing code"},
		{"Edit", "id_ed25519.go", "", "Editing code"},
		{"Edit", "opaque01234567890123456789012345.go", "", "Editing code"},
		{"Edit", "AKIAIOSFODNN7EXAMPLE.go", "", "Editing code"},
		{"Edit", "sk-live.go", "", "Editing code"},
		{"Edit", "ghp_example.ts", "", "Editing code"},
		{"Edit", "xoxb-example.ts", "", "Editing code"},
		{"Edit", "id-rsa.go", "", "Editing code"},
		{"Edit", "planning.ts", "", "Editing planning.ts"},
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
	for _, bad := range []string{"Editing ", "Editing ../api.go", "Editing .env", "Editing secret.go", "Editing AKIAIOSFODNN7EXAMPLE.go", "Editing sk-live.go", "Editing ghp_example.ts", "Editing xoxb-example.ts", "Editing id-rsa.go", "Running Go tests PRIVATE_ARGUMENT", "PRIVATE_ARGUMENT"} {
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
	for _, word := range []string{"secret", "token", "credential", "password", "id_", "private", "_rsa", "_ed25519"} {
		if _, valid := CleanSummary("Reading " + strings.ToUpper(word)); valid {
			t.Fatalf("accepted credential word %q without a colon", word)
		}
	}
	// Synthetic short examples exercise prefix detection below the opaque limit.
	for _, prefix := range []string{"AKIA", "ASIA", "sk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "xoxb-", "xoxp-", "xoxa-", "xoxr-", "xoxs-", "xoxe-", "xapp-", "AIza", "ya29.", "glpat-", "npm_", "pypi-", "hf_"} {
		if _, valid := CleanSummary("Key " + prefix + "EXAMPLE"); valid {
			t.Fatalf("accepted short key prefix %q", prefix)
		}
	}
	for _, text := range []string{"Key AKIAIOSFODNN7EXAMPLE", "Key s\u200bk-EXAMPLE", "abcdefghijkl\u200bmnopqrstuvwx", "Running\u00ad tests", "Working\u2060", "\ufeffWorking", "Working\u202e", "A\u0301KIAIOSFODNN7EXAMPLE", "abcdefghijkl\u0301mnopqrstuvwx", "A\u20ddKIAIOSFODNN7EXAMPLE", "abcdefghijkl\u20ddmnopqrstuvwx"} {
		if _, valid := CleanSummary(text); valid {
			t.Fatalf("accepted unsafe or format-character summary %q", text)
		}
	}
}

func TestCurrentRevalidatesStoredActivity(t *testing.T) {
	now := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	stale := now.Add(-Fresh)
	for _, tc := range []struct {
		name, doing, tool, mode, want, source string
		at                                    time.Time
	}{
		{"valid summary", "Implementing activity", "Running Go tests", Summary, "Implementing activity", "agent", fresh},
		{"normalized summary", "  Implementing activity  ", "Running Go tests", Summary, "Implementing activity", "agent", fresh},
		{"credential word fallback", "Reading secret", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"short key fallback", "Key AKIAIOSFODNN7EXAMPLE", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"format character fallback", "Key s\u200bk-EXAMPLE", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"combining mark fallback", "A\u0301KIAIOSFODNN7EXAMPLE", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"split opaque token fallback", "abcdefghijkl\u0301mnopqrstuvwx", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"enclosing mark fallback", "A\u20ddKIAIOSFODNN7EXAMPLE", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"enclosing opaque token fallback", "abcdefghijkl\u20ddmnopqrstuvwx", "Running Go tests", Summary, "Running Go tests", "auto", fresh},
		{"unsafe summary hidden", "Reading token", "Editing secret.go", Summary, "", "", fresh},
		{"invalid tool hidden", "", "Running Go tests with arguments", Tool, "", "", fresh},
		{"unsafe basename hidden", "", "Editing ../api.go", Tool, "", "", fresh},
		{"key basename hidden", "", "Editing AKIAIOSFODNN7EXAMPLE.go", Tool, "", "", fresh},
		{"short prefix basename hidden", "", "Editing sk-live.go", Tool, "", "", fresh},
		{"credential filename hidden", "", "Editing id-rsa.go", Tool, "", "", fresh},
		{"valid basename", "", "Editing api.go", Tool, "Editing api.go", "auto", fresh},
		{"planning basename", "", "Editing planning.ts", Tool, "Editing planning.ts", "auto", fresh},
		{"stale summary fallback", "Implementing activity", "Running Go tests", Summary, "Running Go tests", "auto", stale},
		{"tool mode", "Implementing activity", "Running Go tests", Tool, "Running Go tests", "auto", fresh},
		{"off", "Implementing activity", "Running Go tests", Off, "", "", fresh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Current(&tc.doing, &tc.at, &tc.tool, &fresh, tc.mode, now)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unsafe or disabled activity displayed: %+v", got)
				}
				return
			}
			if got == nil || got.Text != tc.want || got.Source != tc.source || got.At != fresh {
				t.Fatalf("got %+v; want %q from %s at %s", got, tc.want, tc.source, fresh)
			}
		})
	}
}

func TestLegacyNotePrivacy(t *testing.T) {
	for _, text := range []string{"AKIAIOSFODNN7EXAMPLE", "https://user:pass@host/a", "FOO=secret", "FOO=example", "A\u0301KIAIOSFODNN7EXAMPLE", "abcdefghijkl\u0301mnopqrstuvwx", "A\u20ddKIAIOSFODNN7EXAMPLE", "s\u200bk-live", "Editing id-rsa.go", "abcdefghijklmnopqrstuvwx"} {
		if clean, valid := CleanNote(text); valid || clean != "" {
			t.Fatalf("accepted unsafe legacy note %q", text)
		}
	}
	for _, text := range []string{"Reviewing the change", "Editing planning.ts", strings.Repeat("Reviewing the change. ", 5)} {
		if clean, valid := CleanNote(text); !valid || clean != strings.TrimSpace(text) {
			t.Fatalf("rejected public legacy note %q", text)
		}
	}
	if clean, valid := CleanNote("  Running\nPDF\t tests  "); !valid || clean != "RunningPDF tests" {
		t.Fatal("legacy control normalization changed")
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
