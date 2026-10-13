// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
)

// Risk: --profile native becomes attribution, launches a vendor CLI, changes
// resume identity, or reports an uncertain/failed daemon start as success.
func TestNativeSessionCLIStartsOnlyThroughOwningDaemon(t *testing.T) {
	isolate(t)
	root, err := os.MkdirTemp("", "aeon-native-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	if err := os.WriteFile(socket+".token", []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	var state atomic.Value
	state.Store("waiting_for_exit")
	resume := "10000000-0000-4000-8000-000000000037"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/native-sessions/start" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get(agentd.NativeSessionProtocolHeader) != agentd.NativeSessionProtocol {
			t.Error("wrong daemon/protocol authority")
			http.Error(w, "refused", 403)
			return
		}
		var envelope agentd.NativeStartEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
		}
		request := envelope.Params
		if request.ProjectID != transcriptProjectID || request.Profile != "native" || request.Role != "coordinator" || request.Harness != "claude" || request.Cwd != root || request.Resume != resume || request.AfterExit != 123 || envelope.RequestID != request.RequestID || string(envelope.ExpectedBinding) != "null" {
			t.Error("native start identity changed", request)
		}
		starts.Add(1)
		w.Header().Set(agentd.NativeSessionProtocolHeader, agentd.NativeSessionProtocol)
		_ = json.NewEncoder(w).Encode(agentd.NativeSessionStartResult{RequestID: request.RequestID, SessionID: transcriptSessionID, State: state.Load().(string)})
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	var calls []transcriptRequest
	api := transcriptFixture(t, "ticket", "example", &calls)
	defer api.Close()
	t.Setenv("AEON_URL", api.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "--config", filepath.Join(root, "missing"), "--json", "session", "start", "--profile", "native", "--role", "coordinator", "--project", "AEON", "--harness", "claude", "--cwd", root, "--resume", resume, "--after-exit", "123", "--socket", socket}
	code, stdout, stderr := runCLI(args, "")
	var result agentd.NativeSessionStartResult
	if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil || result.State != "waiting_for_exit" || result.SessionID != transcriptSessionID || starts.Load() != 1 {
		t.Fatal("CLI did not queue on daemon", code, stdout, stderr, starts.Load())
	}
	state.Store("exit_unconfirmed")
	code, _, stderr = runCLI(args, "")
	if code == 0 || !strings.Contains(stderr, "exit_unconfirmed") {
		t.Fatal("unconfirmed process reported as successful", code, stderr)
	}
	code, _, stderr = runCLI([]string{"aeon", "session", "start", "--project", "AEON", "--role", "coordinator", "--harness", "claude", "--cwd", root}, "")
	if code == 0 || !strings.Contains(stderr, "--profile native") || starts.Load() != 2 {
		t.Fatal("ordinary attribution acquired native authority", code, stderr, starts.Load())
	}
}
