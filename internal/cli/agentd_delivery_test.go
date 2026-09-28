// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/inbox"
)

func TestManagedDeliveryUsesOwnedLocalControl(t *testing.T) {
	// Unix socket paths are capped near 104 bytes, and macOS's default TMPDIR is
	// long, so the socket lives under /tmp, which exists on macOS and Linux.
	// Darwin's /tmp is a symlink to /private/tmp. Resolve that before creating
	// the socket and token: the production client refuses a non-physical ancestor.
	root, err := os.MkdirTemp("/tmp", "aeon-agentd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = physical
	socket := filepath.Join(root, "agentd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.WriteFile(socket+".token", []byte("00000000000000000000000000000000"), 0600); err != nil {
		t.Fatal(err)
	}
	const runID = "00000000-0000-4000-8000-000000000002"
	const tenantID = "00000000-0000-4000-8000-000000000003"
	const principalID = "00000000-0000-4000-8000-000000000004"
	const deliveryID = "00000000-0000-4000-8000-000000000005"
	var controlled bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer 00000000000000000000000000000000" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"generation": "generation-fixture", "runs": []map[string]string{{"tenant_id": tenantID, "principal_id": principalID, "run_id": runID, "generation": "generation-fixture", "state": "running"}}})
		case "/v1/runs/" + runID + "/control":
			var request agentd.ControlRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.TenantID != tenantID || request.PrincipalID != principalID || request.Generation != "generation-fixture" || request.CorrelationID != deliveryID || request.Operation != "steer" || !strings.Contains(request.Text, "SECURITY NOTICE") || !strings.Contains(request.Text, "hello") {
				w.WriteHeader(http.StatusConflict)
				return
			}
			controlled = true
			_ = json.NewEncoder(w).Encode(agentd.Receipt{RunID: runID, Generation: request.Generation, CorrelationID: deliveryID, Operation: "steer", AppliedAt: time.Now()})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	ref, _ := json.Marshal(map[string]string{"socket": socket, "session_id": runID})
	work := inbox.DeliveryWork{ID: deliveryID, TenantID: tenantID, PrincipalID: principalID, TargetRef: string(ref), MaximumLevel: "steer", Message: &inbox.CompatMessage{Level: "steer", Body: "hello"}}
	result, err := deliverAgentdMessaging(context.Background(), "agentd_codex", work)
	if err != nil || !controlled || result.EffectiveLevel != "steer" {
		t.Fatalf("owned control result=%+v applied=%t err=%v", result, controlled, err)
	}
	controlled = false
	work.PrincipalID = "00000000-0000-4000-8000-000000000006"
	if _, err := deliverAgentdMessaging(context.Background(), "agentd_codex", work); err == nil || controlled {
		t.Fatal("mismatched owner reached local control")
	}
}
