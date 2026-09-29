// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOutcomeRecordCommand(t *testing.T) {
	isolate(t)
	code, out, errOut := runCLI([]string{"aeon", "outcome", "record", "--help"}, "")
	if code != 0 || !strings.Contains(out, "--idempotency-key") || !strings.Contains(out, "review_verdict") || errOut != "" {
		t.Fatalf("help code %d out %q err %q", code, out, errOut)
	}
	code, _, errOut = runCLI([]string{"aeon", "outcome", "record", "--ticket", "AEON-286", "--kind", "revert"}, "")
	if code != 2 || !strings.Contains(errOut, "idempotency-key") {
		t.Fatalf("usage code %d err %q", code, errOut)
	}
	code, _, errOut = runCLI([]string{"aeon", "outcome", "record", "--ticket", "AEON-286", "--kind", "note", "--idempotency-key", "note-key-01"}, "")
	if code != 2 || !strings.Contains(errOut, "--kind") {
		t.Fatalf("kind code %d err %q", code, errOut)
	}

	var header, kind string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/outcomes" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		header = r.Header.Get("Idempotency-Key")
		var body outcomeWire
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		kind = body.Kind
		payload = body.Payload
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "8f000000-0000-4000-8000-000000000001", "kind": body.Kind, "ticket_key": "AEON-286"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)

	code, _, errOut = runCLI([]string{"aeon", "outcome", "record", "--ticket", "AEON-286", "--kind", "ci_result", "--idempotency-key", "ci-aeon-286", "--result", "fail", "--name", "web"}, "")
	if code != 2 || !strings.Contains(errOut, "--repo") {
		t.Fatalf("missing repo code %d err %q", code, errOut)
	}
	code, _, errOut = runCLI([]string{"aeon", "outcome", "record", "--ticket", "AEON-286", "--kind", "review_verdict", "--idempotency-key", "review-old-pass", "--verdict", "pass"}, "")
	if code != 2 || !strings.Contains(errOut, "ok or changes") {
		t.Fatalf("old verdict code %d err %q", code, errOut)
	}

	args := []string{"aeon", "outcome", "record", "--ticket", "AEON-286", "--kind", "ci_result", "--idempotency-key", "ci-aeon-286", "--result", "fail", "--repo", "inspr-at/paimos", "--pr", "286", "--name", "web", "--summary", "lint failed"}
	code, out, errOut = runCLI(args, "")
	if code != 0 || errOut != "" || strings.TrimSpace(out) != "recorded ci_result AEON-286 8f000000-0000-4000-8000-000000000001" {
		t.Fatalf("record code %d out %q err %q", code, out, errOut)
	}
	if header != "ci-aeon-286" || kind != "ci_result" || payload["result"] != "fail" || payload["name"] != "web" || payload["repo"] != "inspr-at/paimos" || payload["number"] != float64(286) {
		t.Fatalf("request header %q kind %q payload %#v", header, kind, payload)
	}
	assertNoSecret(t, out+"\n"+errOut)

	code, out, errOut = runCLI(append([]string{"aeon", "--json"}, args[1:]...), "")
	if code != 0 || !strings.Contains(out, `"ticket_key":"AEON-286"`) || strings.Contains(out, testKey) {
		t.Fatalf("json code %d out %q err %q", code, out, errOut)
	}
}
