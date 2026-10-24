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

// Risk: a dry-run writes, an invalid later row leaves a partial import, or
// replaying the supplied copy invents extra rounds instead of stable identities.
func TestReviewRoundCLIAndImportIdempotency(t *testing.T) {
	isolate(t)
	input := "key\trepo\tfirst look\trounds until ok\tnote\nRND-2\tfixture/repo\t2026-01-01\t3\tU7 A4 U7\nRND-3\tfixture/repo\t2026-01-02\t1\t\n"
	file := filepath.Join(t.TempDir(), "rounds.tsv")
	if err := os.WriteFile(file, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/outcomes/review-rounds" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var in reviewRoundWire
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		key := r.Header.Get("Idempotency-Key")
		raw, _ := json.Marshal(in)
		if old, exists := stored[key]; exists {
			if old != string(raw) {
				t.Error("replay changed the observation")
			}
		} else {
			stored[key] = string(raw)
			w.WriteHeader(201)
		}
		_, _ = w.Write([]byte(`{"id":"a-round"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", testKey)
	base := []string{"aeon", "outcome", "import-design-rounds", "--file", file}
	if code, out, err := runCLI(append(base, "--dry-run"), ""); code != 0 || !strings.Contains(out, "4 design round outcomes; no writes") || calls != 0 {
		t.Fatalf("dry-run: %d %q %q calls %d", code, out, err, calls)
	}
	for range 2 {
		if code, out, err := runCLI(base, ""); code != 0 || !strings.Contains(out, "4 design round outcomes") {
			t.Fatalf("import: %d %q %q", code, out, err)
		}
	}
	if calls != 8 || len(stored) != 4 {
		t.Fatalf("idempotency: calls %d outcomes %d", calls, len(stored))
	}
	plan, err := designRoundPlan(strings.NewReader(input))
	if err != nil || len(plan) != 4 || plan[0].Verdict != "changes" || plan[1].Verdict != "changes" || plan[2].Verdict != "ok" || len(plan[0].Rules) != 2 || len(plan[1].Rules) != 0 || plan[0].At != "2026-01-01T00:00:00Z" {
		t.Fatalf("truthful plan: %+v %v", plan, err)
	}
	if _, err := designRoundPlan(strings.NewReader(input + "RND-4\tfixture/repo\tbad\t2\t\n")); err == nil {
		t.Fatal("accepted malformed later row")
	}
	if _, err := designRoundPlan(strings.NewReader(input + "RND-2\tfixture/repo\t2026-01-01\t2\t\n")); err == nil {
		t.Fatal("accepted conflicting duplicate ticket")
	}
	args := []string{"aeon", "outcome", "review-round", "--ticket", "RND-2", "--kind", "gate", "--round", "2", "--verdict", "ok", "--at", "2026-01-01T00:00:00Z", "--artifact", strings.Repeat("a", 40), "--reviewer-role", "gate_reviewer", "--idempotency-key", "direct-review-key", "--cited-rules", "U7,A4", "--review-run", "11111111-1111-4111-8111-111111111111"}
	if code, out, err := runCLI(args, ""); code != 0 || !strings.Contains(out, "recorded review_round RND-2 gate 2") {
		t.Fatalf("review command: %d %q %q", code, out, err)
	}
}
