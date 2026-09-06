//go:build unix && !paimos_test_unsupported

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

func TestRuntimeTransportNeverAcceptsUnpreviewedSessions(t *testing.T) {
	process := newFakeProcess(9876)
	s, e := NewSupervisor(SupervisorConfig{Instance: "fixture", Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "private-fixture-target"}}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(context.Background())
	session, e := s.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:fixture", ProjectID: 922})
	if e != nil {
		t.Fatal(e)
	}
	handler := transportHandler(s)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/runtime", nil))
	if get.Code != 200 {
		t.Fatal("runtime status failed")
	}
	var status RuntimeStatus
	if json.Unmarshal(get.Body.Bytes(), &status) != nil || status.Instance != "fixture" {
		t.Fatal("runtime evidence malformed")
	}
	call := func(body any) int {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/runtime/quiesce", bytes.NewReader(raw)))
		return w.Code
	}
	if call(map[string]any{"generation": status.DaemonID}) == 200 {
		t.Fatal("omitted owned sessions accepted")
	}
	if call(map[string]any{"generation": status.DaemonID, "sessions": []string{session.ID}, "shell": "untrusted"}) == 200 {
		t.Fatal("unknown payload accepted")
	}
	if call(map[string]any{"generation": status.DaemonID, "sessions": []string{session.ID}}) != 200 {
		t.Fatal("exact quiesce rejected")
	}
	if s.RuntimeStatus(context.Background()).Sessions[0].Owned {
		t.Fatal("HTTP success without owned exit")
	}
}
