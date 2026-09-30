// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctrineProposeCommand(t *testing.T) {
	isolate(t)
	code, out, errOut := runCLI([]string{"aeon", "doctrine", "propose", "--help"}, "")
	if code != 0 || !strings.Contains(out, "--rule-key") || !strings.Contains(out, "--why") || errOut != "" {
		t.Fatalf("help code %d out %q err %q", code, out, errOut)
	}
	code, _, errOut = runCLI([]string{"aeon", "doctrine", "propose", "--repo", "inspr-modules", "--path", "references/AGENTS.md"}, "")
	if code != 2 || !strings.Contains(errOut, "--rule-key") {
		t.Fatalf("usage code %d err %q", code, errOut)
	}
	rule := filepath.Join(t.TempDir(), "rule.md")
	if err := os.WriteFile(rule, []byte("- Estimate before work; report a live ETA in every heartbeat.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"aeon", "doctrine", "propose", "--repo", "inspr-modules", "--path", "references/AGENTS.md", "--rule-key", "estimate-first", "--file", rule}
	code, _, errOut = runCLI(base, "")
	if code != 2 || !strings.Contains(errOut, "--why") {
		t.Fatalf("missing why code %d err %q", code, errOut)
	}

	var got doctrineInboxWire
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/rules/doctrine/inbox" && r.Method == http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": got.RequestID, "state": "pending", "inbox": true})
		case r.URL.Path == "/api/rules/doctrine/inbox" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"pending": 1, "items": []map[string]any{
				{"id": "44400000-0000-4000-8000-000000000001", "state": "pending", "path": "references/AGENTS.md", "label": "Estimate before work.", "ticket": "INSPR-491"},
				{"id": "44400000-0000-4000-8000-000000000002", "state": "dismissed", "path": "references/AGENTS.md", "label": "Tests", "dismiss_reason": "Belongs in the workflow pack."},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)

	args := append(base, "--why", "INSPR-491: estimates make ETAs honest.", "--ticket", "INSPR-491", "--tldr", "Estimate before work.")
	code, out, errOut = runCLI(args, "")
	if code != 0 || errOut != "" || !strings.Contains(out, "a person sends it to git") {
		t.Fatalf("propose code %d out %q err %q", code, out, errOut)
	}
	if !validUUID(got.RequestID) || got.Repository != "inspr-modules" || got.RuleKey != "estimate-first" || got.Ticket != "INSPR-491" || got.TLDR["en"] != "Estimate before work." || !strings.HasPrefix(got.Source, "- Estimate before work") || got.Why != "INSPR-491: estimates make ETAs honest." {
		t.Fatalf("request %+v", got)
	}
	assertNoSecret(t, out+"\n"+errOut)

	code, out, errOut = runCLI([]string{"aeon", "doctrine", "list"}, "")
	if code != 0 || !strings.Contains(out, "INSPR-491") || !strings.Contains(out, "Belongs in the workflow pack.") {
		t.Fatalf("list code %d out %q err %q", code, out, errOut)
	}
}
