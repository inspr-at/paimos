// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
)

func TestRulesRendererAndExactProvenance(t *testing.T) {
	body := "# Aeon session rules\n\n- [safety] Preserve safety.\n"
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	for _, h := range rules.Harnesses {
		m := rules.Merged{Context: rules.Context{Harness: h}, Version: "260928120000.0.0", Body: body, ByteSize: len(body), SHA256: hash}
		r, err := renderRulesThroughHarness(m)
		if err != nil || r.Body != body || r.Rev != hash {
			t.Fatal("renderer changed exact bytes", err)
		}
		item := rulesProvenance(m)
		if *item.ContentSHA256 != hash || *item.ByteSize != int64(len(body)) || *item.Version != m.Version || item.HashKind != "content" {
			t.Fatal("provenance does not match output")
		}
		want := "AGENTS.md"
		if h == "claude-code" {
			want = "CLAUDE.md"
		}
		if item.LogicalName != want || r.SuggestedPath != want {
			t.Fatal("harness mismatch")
		}
	}
}
func TestRulesOfflineOnlyForNetworkFailure(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 422, 500} {
		if networkUnavailable(&client.StatusError{Status: code}) {
			t.Fatalf("authorization/semantic error %d enabled cache", code)
		}
	}
	for _, code := range []int{502, 503, 504} {
		if !networkUnavailable(&client.StatusError{Status: code}) {
			t.Fatal(code)
		}
	}
	if !networkUnavailable(&net.OpError{Op: "dial", Err: errors.New("offline")}) {
		t.Fatal("network failure not recognized")
	}
}

func TestRulesPreviewOnlineOfflineAndRefusedCache(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	c := rules.Context{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", PersonID: "10000000-0000-4000-8000-000000000003", AgentID: "10000000-0000-4000-8000-000000000004", Role: "builder", Harness: "codex"}
	r := rules.Rule{Identity: "safety", Text: "Preserve safety.", Why: "Fixture", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "test"}}
	snap := rules.Snapshot{SetID: "10000000-0000-4000-8000-000000000005", Scope: rules.Scope{Layer: "company"}, Name: "Floor", Revision: 1, Version: "260928100000.0.0", Rules: []rules.Rule{r}}
	snap.SHA256 = rules.SnapshotDigest(snap)
	m, err := rules.Merge(c, []rules.Snapshot{snap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	floor := filepath.Join(dir, "floor.txt")
	if err = rules.WriteFile(floor, []byte(m.Floor), false); err != nil {
		t.Fatal(err)
	}
	floorHash := sha256.Sum256([]byte(m.Floor))
	receipts := make(chan []harness.ProvenanceItem, 1)
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.URL.Path == "/api/me" {
			json.NewEncoder(w).Encode(map[string]any{"principal": map[string]string{"id": c.AgentID, "name": "worker"}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/provenance") {
			if r.Method != "POST" || r.Header.Get("X-Aeon-Worker-Lease") != "fixture-worker-lease-0000000000000000001" {
				t.Error("wrong provenance transport")
			}
			var body struct {
				Items []harness.ProvenanceItem `json:"items"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			receipts <- body.Items
			json.NewEncoder(w).Encode(map[string]bool{"replayed": false})
			return
		}
		if r.URL.Path != "/api/rules/merged" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Query().Get("person_id") != c.PersonID || r.URL.Query().Get("agent_id") != c.AgentID {
			t.Error("lost request context")
		}
		json.NewEncoder(w).Encode(m)
	}))
	defer srv.Close()
	t.Setenv("PAIMOS_URL", srv.URL)
	t.Setenv("PAIMOS_API_KEY", testKey)
	args := []string{"paimos", "--config", filepath.Join(dir, "missing"), "session", "start", "--project", c.ProjectID, "--agent", "worker", "--rules-preview", "--rules-tenant", c.TenantID, "--rules-person", c.PersonID, "--rules-agent", c.AgentID, "--rules-role", c.Role, "--rules-harness", c.Harness, "--rules-floor", floor, "--rules-floor-sha256", hex.EncodeToString(floorHash[:]), "--rules-cache", filepath.Join(dir, "cache.json")}
	code, out, stderr := runCLI(args, "")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, `"stale":false`) || !strings.Contains(out, `"execution_verified":false`) {
		t.Fatal(out)
	}
	assertNoSecret(t, out+stderr)

	select {
	case <-receipts:
		t.Fatal("preview fabricated a provenance receipt")
	default:
	}
	leasePath := filepath.Join(dir, "worker-lease")
	if err = os.WriteFile(leasePath, []byte("fixture-worker-lease-0000000000000000001"), 0600); err != nil {
		t.Fatal(err)
	}
	receiptArgs := append(append([]string{}, args...), "--rules-record-received", "10000000-0000-4000-8000-000000000006", "--rules-worker-lease-file", leasePath)
	code, out, stderr = runCLI(receiptArgs, "")
	if code != 0 || !strings.Contains(out, `"provenance_recorded":true`) || !strings.Contains(out, `"execution_verified":false`) {
		t.Fatal("receipt", stderr, out)
	}
	items := <-receipts
	if len(items) != 1 || items[0].ContentSHA256 == nil || *items[0].ContentSHA256 != m.SHA256 || items[0].ByteSize == nil || *items[0].ByteSize != int64(len(m.Body)) || items[0].Version == nil || *items[0].Version != m.Version {
		t.Fatal("receipt differs from actual returned bytes")
	}
	status = 403
	code, _, _ = runCLI(args, "")
	if code == 0 {
		t.Fatal("403 used cached authorization")
	}
	status = 503
	code, out, stderr = runCLI(args, "")
	if code != 0 || !strings.Contains(out, `"stale":true`) || !strings.Contains(out, "Preserve safety") {
		t.Fatal("offline cache", stderr, out)
	}
	if err = os.WriteFile(filepath.Join(dir, "cache.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runCLI(args, "")
	if code != 0 || !strings.Contains(out, "floor-only") || !strings.Contains(out, "Preserve safety") {
		t.Fatal("floor lost", stderr, out)
	}

	offlineReceipt := append(append([]string{}, args...), "--rules-record-received", "10000000-0000-4000-8000-000000000006", "--rules-worker-lease-file", filepath.Join(dir, "missing-lease"))
	code, out, stderr = runCLI(offlineReceipt, "")
	if code != 0 || !strings.Contains(out, "floor-only") || !strings.Contains(out, `"provenance_recorded":false`) || !strings.Contains(out, "not recorded while offline") {
		t.Fatal("offline receipt hid floor", stderr, out)
	}
}
