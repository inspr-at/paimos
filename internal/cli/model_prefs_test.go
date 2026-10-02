// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelTicketResolveAndReadOnlyPrefs(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/models/resolve":
			if r.URL.Query().Get("ticket") != "AEON-123" || r.URL.Query().Has("role") {
				t.Error("ticket placement not requested", r.URL.String())
			}
			_, _ = w.Write([]byte(`{"role":"build","profile":{"id":"profile","slug":"chosen","version":"1","family":"xai","model":"chosen-model","harness":"grok","effort":"xhigh"},"ladder":[],"preference":{"kind":"backend","bucket":"complex","set_by":"person","residency":{"value":"any","set_by":"person","loosened_lock":true}}}`))
		case "/api/models":
			_, _ = w.Write([]byte(`[]`))
		case "/api/model-preferences":
			_, _ = w.Write([]byte(`{"kinds":[{"id":"kind","label":"Backend"}],"views":{"default":{"residency":{"value":"any"},"rows":[{"kind_id":"kind","set_by":"default","normal":{"label":"Automatic"},"complex":{"label":"Automatic"}}]},"person":{"residency":{"value":"any","loosened_lock":true},"rows":[]},"project":null}}`))
		default:
			t.Error("unexpected request", r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	t.Setenv("PAIMOS_URL", server.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	base := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing")}
	code, out, errout := runCLI(append(base, "model", "resolve", "--ticket", "AEON-123"), "")
	if code != 0 || errout != "" || !strings.Contains(out, "Why: backend · complex · set by person") || !strings.Contains(out, "loosens a provider lock") {
		t.Fatal(code, out, errout)
	}
	code, out, errout = runCLI(append(base, "model", "prefs"), "")
	if code != 0 || errout != "" || !strings.Contains(out, "Backend\tAutomatic\tAutomatic") || !strings.Contains(out, "loosens a provider lock") {
		t.Fatal(code, out, errout)
	}
	for _, request := range calls {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatal("CLI wrote preferences", request)
		}
	}
}
