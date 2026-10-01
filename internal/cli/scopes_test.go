// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/scopecode"
)

func TestScopeRouteTableMatchesRealIssueCalls(t *testing.T) {
	isolate(t)
	cases := []struct {
		command string
		args    []string
	}{
		{"issue get", []string{"issue", "get", "MEM-1"}},
		{"issue list", []string{"issue", "list", "--project", "AEON"}},
		{"issue create", []string{"issue", "create", "--project", "AEON", "--title", "Ticket", "--parent", "MEM-1"}},
		{"issue update", []string{"issue", "update", "MEM-1", "--title", "Ticket"}},
		{"issue comment", []string{"issue", "comment", "MEM-1", "--body", "hello"}},
		{"issue search", []string{"issue", "search", "hello", "--project", "AEON"}},
		{"search", []string{"search", "hello", "--project", "AEON"}},
	}
	// Resolve actual requests with the same ServeMux patterns as authorization.
	routes := http.NewServeMux()
	for pattern := range authz.RoutePermissions {
		routes.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			seen := map[string]bool{}
			for _, alias := range []bool{false, true} {
				var calls []transcriptRequest
				fixture := transcriptFixture(t, "ticket", "", &calls)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, pattern := routes.Handler(r)
					seen[pattern] = true
					if alias && r.URL.Path == "/api/nodes" && r.URL.Query().Get("q") == "MEM-1" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"items":[]}`))
						return
					}
					if r.URL.Path == "/api/node-keys/MEM-1" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"` + transcriptEntryID + `","key":"MEM-1","kind_id":"ticket-kind","title":"Ticket","state":"backlog","fields":{},"updated_at":"2026-09-02T00:00:00Z"}`))
						return
					}
					if r.URL.Path == "/api/search" {
						_, _ = w.Write([]byte(`{"items":[]}`))
						return
					}
					fixture.Config.Handler.ServeHTTP(w, r)
				}))
				t.Setenv("AEON_URL", srv.URL)
				t.Setenv("AEON_API_KEY", testKey)
				args := append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "--json"}, tc.args...)
				code, _, errOut := runCLI(args, "")
				srv.Close()
				fixture.Close()
				if code != 0 {
					t.Fatalf("exit %d: %s", code, errOut)
				}
			}
			want := slices.Clone(commandScopeRoutes[tc.command])
			slices.Sort(want)
			got := []string{}
			for pattern := range seen {
				got = append(got, pattern)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("HTTP calls %v differ from scope table %v", got, want)
			}
			// Every declared scope is resolved from the route map, never hard-kept.
			scopes, err := scopesForCommands([]string{tc.command})
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range scopes {
				if scope.Label == "" || scope.Group == "" {
					t.Fatal("missing human label")
				}
			}
		})
	}
}

func TestScopesNeededIsOfflineAndIncludesActivity(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{}, {"issue", "get/create/update/comment"}, {"issue get", "issue create", "issue update", "issue comment"}, {"issue", "get", "issue", "comment"}} {
		argv := append([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "scopes", "needed"}, args...)
		code, out, errOut := runCLI(argv, "")
		if code != 0 || errOut != "" || !strings.Contains(out, "Events → See history → events.read") {
			t.Fatalf("exit %d: %s %s", code, out, errOut)
		}
		keys, err := scopecode.Decode(strings.Split(out, "\n")[0])
		if err != nil || !slices.Contains(keys, "events.read") {
			t.Fatal("missing activity scope")
		}
	}
	code, out, errOut := runCLI([]string{"aeon", "--json", "scopes", "needed", "issue", "get"}, "")
	var result struct {
		Code   string
		Scopes []neededScope
	}
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &result) != nil || len(result.Scopes) != 2 {
		t.Fatalf("invalid JSON output: %s %s", out, errOut)
	}
	for _, args := range [][]string{{"issue"}, {"curl"}, {"issue", "get/delete"}, {"aeon"}} {
		if code, _, _ := runCLI(append([]string{"aeon", "scopes", "needed"}, args...), ""); code != 2 {
			t.Fatalf("unsupported command accepted: %v", args)
		}
	}
}

func TestDoctorPreservesMissingScopeAndRedactsKey(t *testing.T) {
	isolate(t)
	proposal, _ := scopecode.Encode([]string{"nodes.read"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"status":"ok","db":"ok"}`))
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"260925100000.0.0"}`))
		default:
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "This call needs nodes.read (See work); paste code " + proposal + " into Change scopes " + testKey})
		}
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, _ := runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "doctor"}, "")
	if code != 2 || !strings.Contains(out, "nodes.read (See work)") || !strings.Contains(out, proposal) || strings.Contains(out, testKey) {
		t.Fatal("doctor masked the missing scope or leaked a key")
	}
}
