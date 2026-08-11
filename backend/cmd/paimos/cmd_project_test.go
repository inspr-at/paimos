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

func TestProjectListLifecycleFilters(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS","status":"frozen"}]`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "project", "list", "--all")
	if err != nil {
		t.Fatalf("project list --all: %v", err)
	}
	if len(queries) != 1 || queries[0] != "status=all" {
		t.Fatalf("queries = %v, want status=all", queries)
	}
	if !strings.Contains(out, "STATUS") || !strings.Contains(out, "frozen") {
		t.Fatalf("lifecycle output missing status: %s", out)
	}
}

func TestProjectUpdateLifecycleByKey(t *testing.T) {
	var gotStatus string
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			if r.URL.Query().Get("status") != "all" {
				handlerErr = "project resolution did not include non-active states"
			}
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS","status":"active"}]`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/projects/6":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				handlerErr = err.Error()
			}
			gotStatus = body["status"]
			_, _ = w.Write([]byte(`{"id":6,"key":"PAI","name":"PAIMOS","status":"frozen"}`))
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "project", "update", "PAI", "--status", "FROZEN")
	if err != nil {
		t.Fatalf("project update: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if gotStatus != "frozen" {
		t.Fatalf("status = %q, want frozen", gotStatus)
	}
	if !strings.Contains(out, "PAI is now frozen") {
		t.Fatalf("pretty output = %q", out)
	}
}

func TestProjectUpdateRejectsUnknownLifecycle(t *testing.T) {
	_, _, err := executeCLIForTest(t, "project", "update", "PAI", "--status", "paused")
	if err == nil || !strings.Contains(err.Error(), "active, frozen, archived, or deleted") {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectShowResolvesKeyAndNumericID(t *testing.T) {
	for _, projectRef := range []string{"PAI", "6"} {
		projectRef := projectRef
		t.Run(projectRef, func(t *testing.T) {
			var sawProjectDetail bool
			var handlerErr string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
					_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS"}]`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects/6":
					sawProjectDetail = true
					_, _ = w.Write([]byte(`{"id":6,"key":"PAI","name":"PAIMOS","status":"active","counts":{"open_issues":3,"knowledge_entries":2},"repos":[{"id":1}]}`))
				default:
					handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envURL, srv.URL)
			t.Setenv(envAPIKey, "test_key")

			out, _, err := executeCLIForTest(t, "--json", "project", "show", projectRef)
			if err != nil {
				t.Fatalf("executeCLIForTest: %v", err)
			}
			if handlerErr != "" {
				t.Fatal(handlerErr)
			}
			if !sawProjectDetail {
				t.Fatal("project detail endpoint was not called")
			}
			if !strings.Contains(out, `"key":"PAI"`) || !strings.Contains(out, `"open_issues":3`) {
				t.Fatalf("stdout missing project JSON: %s", out)
			}
		})
	}
}

func TestProjectMetadataCommandsResolveAndFetchResources(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantPath   string
		response   string
		wantOutput string
	}{
		{
			name:       "repos",
			args:       []string{"project", "repos", "PAI"},
			wantPath:   "/api/projects/6/repos",
			response:   `[{"id":1,"project_id":6,"label":"app","url":"https://github.com/example/app","default_branch":"main","sort_order":0}]`,
			wantOutput: `"default_branch":"main"`,
		},
		{
			name:       "releases",
			args:       []string{"project", "releases", "PAI"},
			wantPath:   "/api/projects/6/releases",
			response:   `["v3.2.5"]`,
			wantOutput: `"v3.2.5"`,
		},
		{
			name:       "anchors",
			args:       []string{"project", "anchors", "PAI"},
			wantPath:   "/api/projects/6/anchors",
			response:   `[{"id":9,"project_id":6,"issue_id":101,"issue_key":"PAI-101","repo_id":1,"repo_label":"app","file_path":"backend/main.go","line":42,"label":"router"}]`,
			wantOutput: `"issue_key":"PAI-101"`,
		},
		{
			name:       "tags",
			args:       []string{"project", "tags", "PAI"},
			wantPath:   "/api/projects/6/tags",
			response:   `[{"id":44,"name":"blocked","color":"red","description":"Blocks release"}]`,
			wantOutput: `"blocked"`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var sawResource bool
			var handlerErr string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
					_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS"}]`))
				case r.Method == http.MethodGet && r.URL.Path == tc.wantPath:
					sawResource = true
					_, _ = w.Write([]byte(tc.response))
				default:
					handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envURL, srv.URL)
			t.Setenv(envAPIKey, "test_key")

			args := append([]string{"--json"}, tc.args...)
			out, _, err := executeCLIForTest(t, args...)
			if err != nil {
				t.Fatalf("executeCLIForTest: %v", err)
			}
			if handlerErr != "" {
				t.Fatal(handlerErr)
			}
			if !sawResource {
				t.Fatalf("resource endpoint %s was not called", tc.wantPath)
			}
			if !strings.Contains(out, tc.wantOutput) {
				t.Fatalf("stdout missing %q: %s", tc.wantOutput, out)
			}
		})
	}
}

func TestProjectMetadataPrettyOutput(t *testing.T) {
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI","name":"PAIMOS"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/6/repos":
			_, _ = w.Write([]byte(`[{"id":1,"project_id":6,"label":"app","url":"https://github.com/example/app","default_branch":"main","sort_order":0}]`))
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "project", "repos", "PAI")
	if err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if !strings.Contains(out, "LABEL") || !strings.Contains(out, "app") || !strings.Contains(out, "main") {
		t.Fatalf("pretty output missing repo table: %s", out)
	}
}
