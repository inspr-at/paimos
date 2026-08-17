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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCreateUsesBatchCreateEndpoint(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.yaml")
	if err := os.WriteFile(planPath, []byte(`
project: PAI
create:
  - name: epic
    type: epic
    title: Bulk epic
  - name: child
    type: ticket
    title: Bulk child
    parent: epic
`), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	var received []map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/projects/PAI/issues/batch" {
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			handlerErr = fmt.Sprintf("decode body: %v", err)
			http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"issues":[{"id":101,"issue_key":"PAI-1"},{"id":102,"issue_key":"PAI-2"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "apply", "--from-file", planPath)
	if err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if len(received) != 2 {
		t.Fatalf("received %d create rows, want 2: %#v", len(received), received)
	}
	if received[0]["title"] != "Bulk epic" || received[1]["title"] != "Bulk child" {
		t.Fatalf("unexpected create body: %#v", received)
	}
	if received[1]["parent_ref"] != "#0" {
		t.Fatalf("child parent_ref=%#v, want #0 (body=%#v)", received[1]["parent_ref"], received)
	}
	if !strings.Contains(out, "created 2 issues") {
		t.Fatalf("stdout should report batch-created issues, got %q", out)
	}
}

func TestApplyDryRunDoesNotCallAPI(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.yaml")
	if err := os.WriteFile(planPath, []byte(`
project: PAI
create:
  - title: Dry-run ticket
`), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":"dry run should not call API"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "apply", "--from-file", planPath, "--dry-run")
	if err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests=%d, want 0", requests)
	}
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, "Dry-run ticket") {
		t.Fatalf("stdout should contain dry-run plan JSON, got %q", out)
	}
}

func TestApplyDryRunRejectsBareNumericIssueRefs(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.yaml")
	if err := os.WriteFile(planPath, []byte(`
project: PAI
update:
  - ref: "76"
    fields: {status: qa}
`), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":"unexpected request"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, _, err := executeCLIForTest(t, "apply", "--from-file", planPath, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "ambiguous bare issue number") {
		t.Fatalf("error = %v, want ambiguous bare issue number", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestBatchUpdateDryRunNormalizesExplicitIssueID(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "updates.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"ref":"id:76","fields":{"status":"qa"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write updates: %v", err)
	}

	t.Setenv(envURL, "https://example.test")
	t.Setenv(envAPIKey, "test_key")
	out, _, err := executeCLIForTest(t, "issue", "batch-update", "--from-file", inputPath, "--dry-run")
	if err != nil {
		t.Fatalf("batch-update dry-run: %v", err)
	}
	if !strings.Contains(out, `"ref": "76"`) {
		t.Fatalf("output = %q, want normalized numeric REST ref", out)
	}
}

func TestBatchUpdatePreflightsAllRefsBeforeFirstRequest(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "updates.jsonl")
	var input strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&input, `{"ref":"PAI-%d","fields":{"status":"qa"}}`+"\n", i)
	}
	input.WriteString(`{"ref":"76","fields":{"status":"qa"}}` + "\n")
	if err := os.WriteFile(inputPath, []byte(input.String()), 0o600); err != nil {
		t.Fatalf("write updates: %v", err)
	}

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"updated":100}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, _, err := executeCLIForTest(t, "issue", "batch-update", "--from-file", inputPath)
	if err == nil || !strings.Contains(err.Error(), "line 101") || !strings.Contains(err.Error(), "ambiguous bare issue number") {
		t.Fatalf("error = %v, want line 101 ambiguous bare issue number", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0 after complete preflight failure", requests)
	}
}
