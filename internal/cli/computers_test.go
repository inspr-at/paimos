// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Risk: the operator CLI prints unapproved server fields or silently loses
// pagination/truncation and conflicting ownership evidence.
func TestComputerInventoryCLIUsesSafeEvidenceProjection(t *testing.T) {
	isolate(t)
	const cursor = "00000000-0000-4000-8000-000000000002"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/agent-pairing/computers/inventory" || r.URL.Query().Get("limit") != "1" {
			t.Error("wrong inventory request")
		}
		cookie, err := r.Cookie("aeon_session")
		if err != nil || cookie.Value != "synthetic-cookie" || r.Header.Get("Authorization") != "" {
			t.Error("person session boundary missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"computers":[{"computer_id":"computer","computer_name":"Fixture","state":"connected","owner":"ambiguous","approved_by":"approver","workstation_owners":[{"person_id":null,"created_by_principal_id":"issuer","hash":"MUST-NOT-PRINT"}],"enrollments":[{"account_id":"account","state":"connected","owner_person_id":"other","approved_by":"approver","token":"MUST-NOT-PRINT"}],"enrollments_truncated":true,"runtime_prefix":"MUST-NOT-PRINT"}],"next_cursor":"` + cursor + `","token":"MUST-NOT-PRINT"}`))
	}))
	defer server.Close()
	for _, jsonOutput := range []bool{false, true} {
		args := []string{"aeon", "computers", "inventory", "--limit", "1", "--session-file", "-", "--url", server.URL}
		if jsonOutput {
			args = append(args, "--json")
		}
		code, out, stderr := runCLI(args, "synthetic-cookie\n")
		if code != 0 {
			t.Fatalf("inventory CLI: %d %s", code, stderr)
		}
		if strings.Contains(out+stderr, "MUST-NOT-PRINT") || strings.Contains(out+stderr, "synthetic-cookie") {
			t.Fatal("inventory CLI leaked a secret field")
		}
		for _, value := range []string{"ambiguous", "approver", "issuer", "other", cursor} {
			if !strings.Contains(out, value) {
				t.Fatalf("inventory omitted %s", value)
			}
		}
	}
}
