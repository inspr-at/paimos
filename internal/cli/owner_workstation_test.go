// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnerWorkstationKeyCommands(t *testing.T) {
	isolate(t)
	const id = "00000000-0000-4000-8000-000000000001"
	const computer = "00000000-0000-4000-8000-000000000002"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "aeon_session=synthetic-cookie" || r.Header.Get("Origin") == "" {
			t.Error("person session boundary missing")
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/api/agent-keys" {
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"id": id, "name": "Synthetic key", "owner_workstation": true, "workstation_computer_id": computer, "token": "MUST-NOT-PRINT"}}})
			return
		}
		if r.Method != "PUT" || r.URL.Path != "/api/agent-keys/"+id+"/owner-workstation" {
			t.Error("wrong marking route")
			w.WriteHeader(404)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid marking body")
			return
		}
		body["id"], body["name"], body["token"] = id, "Synthetic key", "MUST-NOT-PRINT"
		json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	for _, args := range [][]string{
		{"keys", "show", id},
		{"keys", "owner-workstation", id, "--computer", computer},
		{"keys", "owner-workstation", id, "--clear"},
	} {
		args = append(args, "--session-file", "-", "--url", srv.URL)
		code, out, stderr := runCLI(args, "synthetic-cookie\n")
		if code != 0 {
			t.Fatalf("command status %d", code)
		}
		if strings.Contains(out+stderr, "MUST-NOT-PRINT") || strings.Contains(out+stderr, "synthetic-cookie") {
			t.Fatal("metadata command leaked a credential field")
		}
		if args[1] == "show" && !strings.Contains(out, "owner workstation: "+computer) {
			t.Fatal("show missing workstation")
		}
	}
	if calls != 3 {
		t.Fatalf("calls: %d", calls)
	}
}

func TestMeReportsOwnerWorkstation(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Error("wrong identity route")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"principal":{"id":"agent","kind":"agent","name":"Synthetic"},"tenant":{"id":"tenant","slug":"synthetic","name":"Synthetic"},"identity":null,"owner_workstation":true,"workstation_computer_id":"computer"}`))
	}))
	defer srv.Close()
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, _ := runCLI([]string{"me"}, "")
	if code != 0 || !strings.Contains(out, "owner workstation: computer") {
		t.Fatal("me omitted owner workstation")
	}
	assertNoSecret(t, out)
}
