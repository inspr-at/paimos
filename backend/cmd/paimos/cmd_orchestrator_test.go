// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrchestratorSetResolvesExplicitTargetAndCurrentRevision(t *testing.T) {
	var payload struct {
		ExpectedRevision int64 `json:"expected_revision"`
		Orchestrator     struct {
			ProjectID    int64  `json:"project_id"`
			Key          string `json:"key"`
			DisplayLabel string `json:"display_label"`
		} `json:"orchestrator"`
	}
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/api/projects?status=all":
			_, _ = w.Write([]byte(`[{"id":42,"key":"PAI"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/42/agents":
			_, _ = w.Write([]byte(`[{"project_id":42,"name":"amy"},{"project_id":42,"name":"reviewer"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/orchestrator/v1/config":
			_, _ = w.Write([]byte(`{"schema_version":1,"revision":7,"orchestrator":null,"updated_at":"2026-09-02T12:00:00Z"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/orchestrator/v1/config":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"revision":8,"orchestrator":{"project_id":42,"project_key":"PAI","project_agent_id":9,"key":"amy","display_label":"Amy O'Brien"},"updated_at":"2026-09-02T12:01:00Z"}`))
		default:
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_secret_not_for_output")

	out, errOut, err := executeCLIForTest(t, "orchestrator", "set", "--project", "PAI", "--agent", "amy", "--display-label", "Amy O'Brien")
	if err != nil {
		t.Fatalf("orchestrator set: %v stderr=%s", err, errOut)
	}
	wantRequests := []string{
		"GET /api/projects?status=all",
		"GET /api/projects/42/agents",
		"GET /api/orchestrator/v1/config",
		"PUT /api/orchestrator/v1/config",
	}
	if fmt.Sprint(requests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requests=%v want=%v", requests, wantRequests)
	}
	if payload.ExpectedRevision != 7 || payload.Orchestrator.ProjectID != 42 || payload.Orchestrator.Key != "amy" || payload.Orchestrator.DisplayLabel != "Amy O'Brien" {
		t.Fatalf("payload=%+v", payload)
	}
	if !strings.Contains(out, "project PAI, agent amy, revision 8") {
		t.Fatalf("stdout=%q", out)
	}
	if strings.Contains(out+errOut, "test_secret_not_for_output") {
		t.Fatal("credential leaked to command output")
	}
}

func TestOrchestratorSetNeverInfersOrRetriesAStaleMutation(t *testing.T) {
	puts := 0
	revision := int64(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			_, _ = w.Write([]byte(`[{"id":42,"key":"PAI"}]`))
		case "/api/projects/42/agents":
			_, _ = w.Write([]byte(`[{"project_id":42,"name":"amy"}]`))
		case "/api/orchestrator/v1/config":
			if r.Method == http.MethodGet {
				if revision == 0 {
					_, _ = w.Write([]byte(`{"schema_version":1,"revision":0,"orchestrator":null,"updated_at":null}`))
				} else {
					_, _ = w.Write([]byte(`{"schema_version":1,"revision":1,"orchestrator":null,"updated_at":"2026-09-02T12:00:00Z"}`))
				}
				return
			}
			puts++
			if puts == 1 {
				revision = 1
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"revision conflict","code":"revision_conflict"}`))
				return
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"revision":2,"orchestrator":{"project_id":42,"project_key":"PAI","project_agent_id":9,"key":"amy","display_label":"Amy"},"updated_at":"2026-09-02T12:01:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, errOut, err := executeCLIForTest(t, "orchestrator", "set", "--project", "PAI", "--agent", "amy", "--display-label", "Amy")
	if err == nil || !strings.Contains(errOut, "revision_conflict") {
		t.Fatalf("error=%v stderr=%q", err, errOut)
	}
	if puts != 1 {
		t.Fatalf("PUT count=%d, want one fail-closed CAS attempt", puts)
	}

	out, errOut, err := executeCLIForTest(t, "orchestrator", "set", "--project", "PAI", "--agent", "amy", "--display-label", "Amy")
	if err != nil || !strings.Contains(out, "revision 2") {
		t.Fatalf("explicit rerun should use fresh revision: error=%v stdout=%q stderr=%q", err, out, errOut)
	}
	if puts != 2 {
		t.Fatalf("PUT count=%d after explicit rerun, want two total", puts)
	}
}

func TestOrchestratorSetRejectsAbsentAndNonCanonicalAgentsBeforeConfigRead(t *testing.T) {
	for _, agent := range []string{"Amy", "missing"} {
		agent := agent
		t.Run(agent, func(t *testing.T) {
			configReads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/projects":
					_, _ = w.Write([]byte(`[{"id":42,"key":"PAI"}]`))
				case "/api/projects/42/agents":
					_, _ = w.Write([]byte(`[{"project_id":42,"name":"amy"}]`))
				case "/api/orchestrator/v1/config":
					configReads++
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envURL, srv.URL)
			t.Setenv(envAPIKey, "test_key")

			_, _, err := executeCLIForTest(t, "orchestrator", "set", "--project", "PAI", "--agent", agent, "--display-label", "Amy")
			if err == nil {
				t.Fatal("expected failure")
			}
			if configReads != 0 {
				t.Fatalf("config reads=%d, want zero", configReads)
			}
		})
	}
}

func TestOrchestratorSetRejectsRedirectWithoutFollowingIt(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, errOut, err := executeCLIForTest(t, "orchestrator", "set", "--project", "PAI", "--agent", "amy", "--display-label", "Amy")
	if err == nil || !strings.Contains(errOut, "307") {
		t.Fatalf("error=%v stderr=%q", err, errOut)
	}
	if redirected {
		t.Fatal("redirect target was called")
	}
}
