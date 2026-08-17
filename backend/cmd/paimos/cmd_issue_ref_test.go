// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeCLIRequiredIssueRef(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "key", input: "PAI-83", want: "PAI-83"},
		{name: "trimmed key", input: "  PAI-83  ", want: "PAI-83"},
		{name: "explicit id", input: "id:462", want: "462"},
		{name: "trimmed explicit id", input: " id:462 ", want: "462"},
		{name: "bare numeric is ambiguous", input: "462", wantErr: "ambiguous bare issue number"},
		{name: "oversized bare numeric is ambiguous", input: "999999999999999999999999999999999999", wantErr: "ambiguous bare issue number"},
		{name: "zero explicit id", input: "id:0", wantErr: "positive numeric issue id"},
		{name: "negative explicit id", input: "id:-1", wantErr: "positive numeric issue id"},
		{name: "non-numeric explicit id", input: "id:PAI-83", wantErr: "positive numeric issue id"},
		{name: "empty", input: " ", wantErr: "issue reference is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeCLIRequiredIssueRef(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("normalizeCLIRequiredIssueRef(%q) error = %v, want substring %q", tt.input, err, tt.wantErr)
				}
				if _, ok := err.(*usageError); !ok {
					t.Fatalf("error type = %T, want *usageError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeCLIRequiredIssueRef(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeCLIRequiredIssueRef(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIssueCommandsRejectBareNumericRefBeforeNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":"unexpected request"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	commands := []struct {
		name string
		args []string
	}{
		{name: "get", args: []string{"issue", "get", "76"}},
		{name: "children", args: []string{"issue", "children", "76"}},
		{name: "update", args: []string{"issue", "update", "76", "--status", "qa"}},
		{name: "ensure status", args: []string{"issue", "ensure-status", "76", "qa"}},
		{name: "comment", args: []string{"issue", "comment", "76", "--body", "note"}},
		{name: "delete", args: []string{"issue", "delete", "76", "--yes"}},
		{name: "tag", args: []string{"issue", "tag", "add", "76", "--tag-id", "2"}},
		{name: "relation source", args: []string{"relation", "add", "76", "relates_to", "PAI-1"}},
		{name: "relation target", args: []string{"relation", "add", "PAI-1", "relates_to", "76"}},
		{name: "move", args: []string{"issue", "move", "76", "--to", "PAI"}},
		{name: "attach", args: []string{"attach", "76", "missing.txt"}},
		{name: "time", args: []string{"time", "start", "76"}},
	}

	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			before := requests
			_, _, err := executeCLIForTest(t, command.args...)
			if err == nil || !strings.Contains(err.Error(), "ambiguous bare issue number") {
				t.Fatalf("error = %v, want ambiguous bare issue number", err)
			}
			if requests != before {
				t.Fatalf("network requests = %d, want %d", requests, before)
			}
		})
	}
}

func TestIssueGetExplicitNumericID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":76,"issue_key":"START-63","title":"Resolved","type":"ticket","status":"new","priority":"medium"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "issue", "get", "id:76")
	if err != nil {
		t.Fatalf("issue get id:76: %v", err)
	}
	if gotPath != "/api/issues/76" {
		t.Fatalf("request path = %q, want /api/issues/76", gotPath)
	}
	if !strings.Contains(out, "START-63") {
		t.Fatalf("output = %q, want resolved issue key", out)
	}
}

func TestIssueUpdateExplicitNumericID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":76,"issue_key":"START-63","status":"qa"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t, "issue", "update", "id:76", "--status", "qa"); err != nil {
		t.Fatalf("issue update id:76: %v", err)
	}
	if gotPath != "/api/issues/76" {
		t.Fatalf("request path = %q, want /api/issues/76", gotPath)
	}
}

func TestIssueRestoreKeepsUnambiguousBareNumericID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":76,"issue_key":"START-63","deleted_at":null}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t, "issue", "restore", "76"); err != nil {
		t.Fatalf("issue restore 76: %v", err)
	}
	if gotPath != "/api/issues/76/restore" {
		t.Fatalf("request path = %q, want /api/issues/76/restore", gotPath)
	}
}
