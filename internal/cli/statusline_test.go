// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatuslineOnlyRelaysNormalizedQuota(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "aeon-sl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gen := strings.Repeat("a", 32)
	name := "agentd-" + gen + ".sock"
	raw, _ := json.Marshal(map[string]string{"socket": name, "daemon_id": "fixture", "generation": gen})
	if err = store.Write("control.json", raw, true); err != nil {
		t.Fatal(err)
	}
	if err = store.Write(name+".token", []byte(strings.Repeat("t", 32)), true); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, name), 0600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan agentd.StatuslineRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/statusline" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			w.WriteHeader(403)
			return
		}
		var req agentd.StatuslineRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		requests <- req
		_ = json.NewEncoder(w).Encode(agentd.StatuslineResponse{Plan: "Aeon · agents up to 15% today", Reported: true})
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	input := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":30,"resets_at":%d}},"cwd":"private-fixture-path","email":"fixture@example.test","api_key":"fixture-private-value"}`, time.Now().Add(time.Hour).Unix())
	var out, errs bytes.Buffer
	account := "35400000-0000-4000-8000-000000000001"
	if code := Run([]string{"aeon", "statusline", "--state-dir", root, "--account-id", account}, strings.NewReader(input), &out, &errs); code != 0 {
		t.Fatal("statusline command failed")
	}
	if out.String() != "Aeon · agents up to 15% today\n" || errs.Len() != 0 {
		t.Fatal("statusline did not emit one plan line")
	}
	select {
	case req := <-requests:
		if req.AccountID != account || len(req.Readings) != 1 || req.Readings[0].UsedPercent != 30 {
			t.Fatal("quota lost")
		}
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), "fixture") || strings.Contains(string(raw), "private") {
			t.Fatal("private stdin forwarded")
		}
	case <-time.After(time.Second):
		t.Fatal("daemon not called")
	}
	if _, err = os.Stat(filepath.Join(root, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("CLI wrote Claude settings")
	}
}
