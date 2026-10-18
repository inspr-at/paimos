// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type dispatchTransport func(*http.Request) (*http.Response, error)

func (f dispatchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Risk: a launcher retries with a new identity or sends the coordinator pilot
// DTO, then mistakes an accepted or uncertain start for a completed worker.
func TestNativeWorkerDispatchFrozenEnvelopeAndReceipt(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const parent = "22222222-2222-4222-8222-222222222222"
	work, parentID, run := id, parent, id
	start := nativecontract.LocalStart{Cwd: t.TempDir(), AccountKey: "owner-codex", Intent: nativecontract.StartIntent{RequestID: id, ExpectedProjectRevision: 1, Role: "worker", Profile: "native", Harness: "codex", ComputerID: id, AccountID: id, WorkspaceID: "assigned", ModelProfileID: id, WorkID: &work, ParentHarnessSessionID: &parentID, PolicyID: id}}
	raw, _ := json.Marshal(start)
	decoded, err := decodeNativeWorkerStart(bytes.NewReader(raw))
	if err != nil || decoded.Intent.RequestID != id || *decoded.Intent.ParentHarnessSessionID != parent {
		t.Fatal("worker input did not preserve identity", err)
	}
	binding := nativecontract.Binding{TenantID: id, ProjectID: id, OwnedSessionID: id, HarnessSessionID: id, RuntimeEpoch: id, DaemonID: "daemon", DaemonGeneration: "generation", BindingRevision: 2, RunID: &run}
	out := nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: id, OperationID: id, Binding: &binding, Operation: "start", State: "applied", SourceSequence: "1", UpdatedAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}, Session: &nativecontract.SessionView{OwnedSessionID: id, CurrentBinding: &binding, OwnerPersonID: id, Role: "worker", Profile: "native", Harness: "codex", State: "running", Mode: "structured", Revision: 2, Generation: 1, ConversationRef: "conversation", Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}}
	calls := 0
	client := &http.Client{Transport: dispatchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/native-sessions/start" || r.Header.Get("X-Aeon-Session-Protocol") != nativecontract.LocalProtocol || r.Header.Get("Authorization") != "Bearer test-owner-proof" {
			t.Fatal("wrong owner-socket route/protocol")
		}
		var envelope nativecontract.LocalEnvelope
		request, _ := io.ReadAll(r.Body)
		if json.Unmarshal(request, &envelope) != nil || envelope.RequestID != id || envelope.Method != "start" || envelope.ExpectedBinding != nil {
			t.Fatal("wrong start envelope", string(request))
		}
		var submitted nativecontract.LocalStart
		if json.Unmarshal(envelope.Params, &submitted) != nil || submitted.Intent.Role != "worker" || submitted.Cwd != start.Cwd || submitted.Intent.AccountID != start.Intent.AccountID || *submitted.Intent.WorkID != work || *submitted.Intent.ParentHarnessSessionID != parent {
			t.Fatal("worker assignment changed")
		}
		body, _ := json.Marshal(out)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Aeon-Session-Protocol": []string{nativecontract.LocalProtocol}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	result, err := sendNativeWorkerStart(t.Context(), client, []byte("test-owner-proof"), start)
	if err != nil || result.Receipt.State != "applied" || result.Session.CurrentBinding.RunID == nil {
		t.Fatal("qualified dispatch rejected", result, err)
	}
	out.Receipt.State = "uncertain"
	if _, err = sendNativeWorkerStart(t.Context(), client, []byte("test-owner-proof"), start); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatal("uncertain start reported success", err)
	}
	out.Receipt.State, out.Receipt.RequestID = "applied", parent
	if _, err = sendNativeWorkerStart(t.Context(), client, []byte("test-owner-proof"), start); err == nil || !strings.Contains(err.Error(), "mismatched") {
		t.Fatal("foreign receipt reported success", err)
	}
	out.Receipt.RequestID = id
	foreignBinding := binding
	foreignBinding.RuntimeEpoch = parent
	out.Receipt.Binding = &foreignBinding
	if _, err = sendNativeWorkerStart(t.Context(), client, []byte("test-owner-proof"), start); err == nil || !strings.Contains(err.Error(), "mismatched") {
		t.Fatal("foreign runtime receipt reported success", err)
	}
	// The request ID is mandatory and remains unchanged even on transport loss.
	client.Transport = dispatchTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("test connection lost") })
	if _, err = sendNativeWorkerStart(context.Background(), client, []byte("test-owner-proof"), start); err == nil || !strings.Contains(err.Error(), "unconfirmed") {
		t.Fatal("transport loss reported success", err)
	}
	start.Intent.Role = "coordinator"
	if _, err = sendNativeWorkerStart(t.Context(), client, []byte("test-owner-proof"), start); err == nil {
		t.Fatal("coordinator start accepted as worker")
	}
	if calls != 4 {
		t.Fatal("invalid request reached transport", calls)
	}
	if _, err = decodeNativeWorkerStart(strings.NewReader(strings.Repeat(" ", 16385))); err == nil {
		t.Fatal("oversized stdin accepted")
	}
	if _, err = decodeNativeWorkerStart(strings.NewReader(strings.TrimSuffix(string(raw), "}") + `,"credential":"forbidden"}`)); err == nil {
		t.Fatal("unknown request field accepted")
	}
}
