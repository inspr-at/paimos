//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountLifecycleTransportRoundTrip(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"coordinator": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-http", StateRoot: t.TempDir(), Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	bindLifecycle(t, supervisor, 3)
	body, _ := json.Marshal(AccountLifecycleRequest{
		IdempotencyKey: "http-connect", Operation: AccountLifecycleConnect, ProjectID: 3,
		RuntimeGeneration: supervisor.Status().DaemonID, AccountKey: "coordinator", Adapter: AdapterCodex,
	})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	transportHandler(supervisor).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result AccountLifecycleResult
	if json.Unmarshal(recorder.Body.Bytes(), &result) != nil || result.State != "connected" {
		t.Fatalf("result=%s", recorder.Body.String())
	}
	status := httptest.NewRecorder()
	transportHandler(supervisor).ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/accounts", nil))
	if status.Code != http.StatusOK || !bytes.Contains(status.Body.Bytes(), []byte(`"coordinator"`)) {
		t.Fatalf("status body=%s", status.Body.String())
	}
}
