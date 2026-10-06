// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHarnessLeadUsageReadAndFailure(t *testing.T) {
	isolate(t)
	const project = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	calls := 0
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/kinds" {
			json.NewEncoder(w).Encode(kindPage{Items: []apiKind{{ID: "project-kind", Slug: "project"}}})
			return
		}
		if r.URL.Path == "/api/nodes" {
			json.NewEncoder(w).Encode(nodePage{Items: []apiNode{{ID: project, Key: "LEAD", KindID: "project-kind"}}})
			return
		}
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/"+project+"/lead/usage" || r.URL.Query().Get("generation") != "2" || r.URL.Query().Get("from") != "2026-09-01" || r.URL.Query().Get("to") != "2026-10-01" || r.Header.Get("X-Aeon-Worker-Lease") != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		w.WriteHeader(status)
		if status == 200 {
			w.Write([]byte(`{"partial":true,"gaps":["incomplete_measurement"],"finish_forecast":"estimate_unavailable"}`))
		} else {
			w.Write([]byte(`{"error":"lead generation usage is unavailable"}`))
		}
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "harness", "lead-usage", "--project", "LEAD", "--generation", "2", "--from", "2026-09-01", "--to", "2026-10-01", "--json"}
	code, out, stderr := runCLI(args, "")
	if code != 0 || !strings.Contains(out, "estimate_unavailable") || stderr != "" || calls != 1 {
		t.Fatalf("read: %d %s %s calls=%d", code, out, stderr, calls)
	}
	status = 503
	code, out, stderr = runCLI(args, "")
	if code == 0 || out != "" || !strings.Contains(stderr, "unavailable") {
		t.Fatalf("failure: %d %s %s", code, out, stderr)
	}
	before := calls
	for _, bad := range [][]string{{"--generation", "-1"}, {"--from", "2026-09-01"}} {
		code, out, _ = runCLI(append([]string{"aeon", "harness", "lead-usage", "--project", "LEAD"}, bad...), "")
		if code == 0 || out != "" || calls != before {
			t.Fatalf("invalid flags made request: %v", bad)
		}
	}
}
