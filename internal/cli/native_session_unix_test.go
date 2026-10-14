// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package cli

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
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
	state.Store("queued")
	resume := "10000000-0000-4000-8000-000000000037"
	request := nativecontract.LocalStart{Intent: nativecontract.StartIntent{RequestID: transcriptEntryID, Profile: "native", Role: "coordinator", Harness: "claude", ComputerID: transcriptProjectID, AccountID: transcriptSessionID, WorkspaceID: "approved-workspace", ModelProfileID: transcriptProjectID, PolicyID: transcriptEntryID}, Cwd: root, AccountKey: "own-default", NativeResumeRef: &resume,
		AfterExit: &nativecontract.ExitBarrier{PID: 123, ProcessStartedAt: time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC), ProcessIdentityDigest: strings.Repeat("b", 64), Deadline: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}}
	intentFile := filepath.Join(root, "person-start.json")
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(intentFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	validator, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/native-sessions/start" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
			t.Error("wrong daemon/protocol authority")
			http.Error(w, "refused", 403)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, nativecontract.RequestBodyBytes+1))
		if err != nil {
			t.Error(err)
			return
		}
		envelope, err := validator.DecodeLocal(body, r.Header.Get(nativecontract.LocalProtocolHeader), "start", nativecontract.ValidationContext{PrincipalKind: "person"})
		if err != nil {
			t.Error("not a C0 start", err)
			return
		}
		var local nativecontract.LocalStart
		if err := json.Unmarshal(envelope.Params, &local); err != nil {
			t.Error(err)
			return
		}
		if !reflect.DeepEqual(local, request) || envelope.ExpectedBinding != nil || envelope.RequestID != request.Intent.RequestID {
			t.Error("person-issued native start, resume or kernel exit identity changed")
		}
		starts.Add(1)
		w.Header().Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
		view := nativecontract.SessionView{OwnedSessionID: transcriptSessionID, OwnerPersonID: transcriptEntryID, Profile: "native", Role: "coordinator", Harness: "claude", State: "waiting_for_exit", Mode: "none", ConversationRef: resume, Revision: 1, Generation: 1, Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}
		receipt := nativecontract.Receipt{RequestID: request.Intent.RequestID, OperationID: transcriptSessionID, Operation: "start", State: state.Load().(string), SourceSequence: "0", UpdatedAt: time.Now().UTC()}
		if receipt.State == "uncertain" {
			reason := nativecontract.ReasonOwnershipUnconfirmed
			receipt.Reason = &reason
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(nativecontract.OperationResult{Receipt: receipt, Session: &view})
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	var calls []transcriptRequest
	api := transcriptFixture(t, "ticket", "example", &calls)
	defer api.Close()
	t.Setenv("AEON_URL", api.URL)
	t.Setenv("AEON_API_KEY", testKey)
	args := []string{"aeon", "--config", filepath.Join(root, "missing"), "--json", "session", "start", "--profile", "native", "--role", "coordinator", "--project", "AEON", "--harness", "claude", "--cwd", root, "--resume", resume, "--after-exit", "123", "--socket", socket, "--intent", intentFile}
	code, stdout, stderr := runCLI(args, "")
	var result nativecontract.OperationResult
	if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil || result.Session == nil || result.Session.State != "waiting_for_exit" || result.Session.OwnedSessionID != transcriptSessionID || result.Receipt.State != "queued" || starts.Load() != 1 {
		t.Fatal("CLI did not queue on daemon", code, stdout, stderr, starts.Load())
	}
	state.Store("uncertain")
	code, _, stderr = runCLI(args, "")
	if code == 0 || !strings.Contains(stderr, "uncertain") {
		t.Fatal("unconfirmed process reported as successful", code, stderr)
	}
	// A PID alone is not the C0 exit barrier; flags never rewrite the issued intent.
	changed := append([]string(nil), args...)
	for i, flag := range changed {
		if flag == "--after-exit" {
			changed[i+1] = "124"
		}
	}
	code, _, stderr = runCLI(changed, "")
	if code == 0 || !strings.Contains(stderr, "exact person-issued C0 intent") || starts.Load() != 2 {
		t.Fatal("CLI rewrote the pinned exit barrier", code, stderr, starts.Load())
	}
	code, _, stderr = runCLI([]string{"aeon", "session", "start", "--project", "AEON", "--role", "coordinator", "--harness", "claude", "--cwd", root}, "")
	if code == 0 || !strings.Contains(stderr, "--profile native") || starts.Load() != 2 {
		t.Fatal("ordinary attribution acquired native authority", code, stderr, starts.Load())
	}
}
