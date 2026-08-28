// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startFakeAPI serves canned JSON responses keyed by "<METHOD> <path>".
// Returns an httptest.Server scoped to the test (auto-closed via t.Cleanup).
// The router is intentionally tiny — we only need exact path/method
// matches for the helper-level tests.
func startFakeAPI(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		body, ok := routes[key]
		if !ok {
			http.Error(w, `{"error":"unmocked route: `+key+`"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newClientForTest builds a Client pointed at the test server. No
// API key is needed because the fake server doesn't enforce auth.
func newClientForTest(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{},
	}
}

// TestReadMultilineInput covers the file-vs-inline mutual exclusion
// rules that are the whole point of PAI-91: every mutation command
// promises "either --foo or --foo-file, never both, and file wins
// precedence when tests can't infer". Breaking this is how the
// shell-quoted-JSON foot-gun crept back in.
func TestReadMultilineInput(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "desc.md")
	fileContent := "# Heading\n\nBody with **markdown**.\n"
	if err := os.WriteFile(filePath, []byte(fileContent), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cases := []struct {
		name        string
		inline      string
		file        string
		wantValue   string
		wantSet     bool
		wantErr     bool
		errContains string
	}{
		{
			name:      "neither set",
			wantValue: "",
			wantSet:   false,
		},
		{
			name:      "inline only",
			inline:    "single line",
			wantValue: "single line",
			wantSet:   true,
		},
		{
			name:      "file only",
			file:      filePath,
			wantValue: fileContent,
			wantSet:   true,
		},
		{
			name:        "both set → error",
			inline:      "x",
			file:        filePath,
			wantErr:     true,
			errContains: "mutually exclusive",
		},
		{
			name:        "file points at non-existent path",
			file:        filepath.Join(dir, "missing.md"),
			wantErr:     true,
			errContains: "no such file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, set, err := readMultilineInput(tc.inline, tc.file, "description")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				if tc.errContains != "" && !containsFold(err.Error(), tc.errContains) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.errContains)
				}
				return
			}
			if got != tc.wantValue {
				t.Errorf("value = %q, want %q", got, tc.wantValue)
			}
			if set != tc.wantSet {
				t.Errorf("set = %v, want %v", set, tc.wantSet)
			}
		})
	}
}

// TestReadMultilineInput_Stdin verifies the "-" convention for
// file-flag → stdin. Uses a temp pipe since os.Stdin is process-wide.
func TestReadMultilineInput_Stdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	go func() {
		_, _ = w.Write([]byte("from stdin"))
		_ = w.Close()
	}()

	got, set, err := readMultilineInput("", "-", "description")
	if err != nil {
		t.Fatalf("readMultilineInput: %v", err)
	}
	if !set {
		t.Error("set = false, want true for stdin input")
	}
	if got != "from stdin" {
		t.Errorf("value = %q, want %q", got, "from stdin")
	}
}

// containsFold is case-insensitive substring check. "no such file" vs
// "No such file" differ across OSes, so the error-message assertion
// needs a fuzzy compare.
func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func executeCLIForTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer

	oldStdout, oldStderr := stdout, stderr
	oldInstance, oldJSON, oldConfigPath := flagInstance, flagJSON, flagConfigPath
	stdout, stderr = &out, &errOut
	flagInstance, flagJSON, flagConfigPath = "", false, ""
	t.Cleanup(func() {
		stdout, stderr = oldStdout, oldStderr
		flagInstance, flagJSON, flagConfigPath = oldInstance, oldJSON, oldConfigPath
	})

	cmd := rootCmd()
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestParsePositiveInt64Flag(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "plain", raw: "2", want: 2},
		{name: "trim", raw: " 42 ", want: 42},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "text", raw: "mba", wantErr: true},
		{name: "blank", raw: " ", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parsePositiveInt64Flag("assignee", c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if c.wantErr {
				if !containsFold(err.Error(), "positive numeric id") {
					t.Errorf("err=%q, want positive numeric id", err.Error())
				}
				return
			}
			if got != c.want {
				t.Errorf("got=%d, want=%d", got, c.want)
			}
		})
	}
}

func TestIssueUpdateDryRun_AssigneeIDIsNumeric(t *testing.T) {
	t.Setenv(envURL, "https://example.test")
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t,
		"--json",
		"issue", "update", "PAI-1",
		"--status", "qa",
		"--assignee", "2",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	var got struct {
		Body map[string]any `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode dry-run JSON: %v\n%s", err, out)
	}
	if _, ok := got.Body["assignee_id"].(float64); !ok {
		t.Fatalf("assignee_id type = %T, want JSON number (body=%v)", got.Body["assignee_id"], got.Body)
	}
	if got.Body["assignee_id"].(float64) != 2 {
		t.Errorf("assignee_id=%v, want 2", got.Body["assignee_id"])
	}
	if got.Body["status"] != "qa" {
		t.Errorf("status=%v, want qa", got.Body["status"])
	}
}

func TestIssuePharosRequestIDFlags(t *testing.T) {
	t.Setenv(envURL, "https://example.test")
	t.Setenv(envAPIKey, "test_key")
	requestID := "pharos-create-csb1-1787912345000-1"

	t.Run("create dry-run sends typed link", func(t *testing.T) {
		out, _, err := executeCLIForTest(t, "--json", "issue", "create",
			"--project", "PAI", "--title", "Need a host",
			"--pharos-request-id", requestID, "--dry-run")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Body map[string]any `json:"body"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.Body["pharos_request_id"] != requestID {
			t.Fatalf("body=%v", got.Body)
		}
	})

	t.Run("update empty value clears link", func(t *testing.T) {
		out, _, err := executeCLIForTest(t, "--json", "issue", "update", "PAI-1",
			"--pharos-request-id", "", "--dry-run")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Body map[string]any `json:"body"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		value, present := got.Body["pharos_request_id"]
		if !present || value != "" {
			t.Fatalf("body=%v, want explicit empty pharos_request_id", got.Body)
		}
	})

	t.Run("secret-like value fails before transport", func(t *testing.T) {
		_, _, err := executeCLIForTest(t, "issue", "update", "PAI-1",
			"--pharos-request-id", "sk_test_abcdefghijklmnopqrstuvwxyz")
		if err == nil || !strings.Contains(err.Error(), "secret-like") {
			t.Fatalf("err=%v, want secret-like usage error", err)
		}
	})
}

func TestRenderIssuePrettyShowsPharosRequestID(t *testing.T) {
	var out bytes.Buffer
	oldStdout := stdout
	stdout = &out
	t.Cleanup(func() { stdout = oldStdout })
	renderIssuePretty(map[string]any{
		"issue_key":         "PAI-812",
		"title":             "Need a host",
		"type":              "ticket",
		"status":            "in-progress",
		"priority":          "medium",
		"pharos_request_id": "pharos-create-csb1-1787912345000-1",
	})
	if !strings.Contains(out.String(), "pharos:   pharos-create-csb1-1787912345000-1") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestIssueUpdateCombinedRequest_AssigneeIDIsNumeric(t *testing.T) {
	var received map[string]any
	var handlerErr string
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPut || r.URL.Path != "/api/issues/PAI-1" {
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			handlerErr = fmt.Sprintf("decode request: %v", err)
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_key":"PAI-1","title":"x"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t,
		"issue", "update", "PAI-1",
		"--status", "qa",
		"--assignee", "2",
	); err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if requests != 1 {
		t.Fatalf("requests=%d, want 1", requests)
	}
	if received["status"] != "qa" {
		t.Errorf("status=%v, want qa", received["status"])
	}
	if _, ok := received["assignee_id"].(float64); !ok {
		t.Fatalf("assignee_id type = %T, want JSON number (body=%v)", received["assignee_id"], received)
	}
	if received["assignee_id"].(float64) != 2 {
		t.Errorf("assignee_id=%v, want 2", received["assignee_id"])
	}
}

func TestIssueUpdateCloseNoteResolvesKeyBeforeWrites(t *testing.T) {
	var paths []string
	var putBody map[string]any
	var commentBody map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-410":
			_, _ = w.Write([]byte(`{"id":1753,"issue_key":"PAI-410","status":"new"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/issues/1753":
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				handlerErr = fmt.Sprintf("decode put: %v", err)
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":1753,"issue_key":"PAI-410","status":"delivered"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/1753/comments":
			if err := json.NewDecoder(r.Body).Decode(&commentBody); err != nil {
				handlerErr = fmt.Sprintf("decode comment: %v", err)
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":99,"body":"ok"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/1753/lesson-capture-prompt":
			_, _ = w.Write([]byte(`{"triggered":false}`))
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t,
		"issue", "update", "PAI-410",
		"--status", "delivered",
		"--close-note", "Delivered in v3.4.10 on ppm.",
	); err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	wantPaths := []string{
		"GET /api/issues/PAI-410",
		"PUT /api/issues/1753",
		"POST /api/issues/1753/comments",
		"GET /api/issues/1753/lesson-capture-prompt",
	}
	if strings.Join(paths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("paths:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(wantPaths, "\n"))
	}
	if putBody["status"] != "delivered" {
		t.Fatalf("put status=%v, want delivered", putBody["status"])
	}
	body, _ := commentBody["body"].(string)
	if !strings.Contains(body, "**Close note** (status → delivered):") || !strings.Contains(body, "Delivered in v3.4.10 on ppm.") {
		t.Fatalf("comment body=%q", body)
	}
}

func TestIssueCreateParentRefSendsNumericID(t *testing.T) {
	for _, tc := range []struct {
		parentRef string
		serverRef string
	}{{"PAI-1", "PAI-1"}, {"id:101", "101"}} {
		tc := tc
		t.Run(tc.parentRef, func(t *testing.T) {
			var received map[string]any
			var handlerErr string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+tc.serverRef:
					_, _ = w.Write([]byte(`{"id":101,"issue_key":"PAI-1"}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
					_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
				case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/issues":
					if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
						handlerErr = fmt.Sprintf("decode request: %v", err)
						http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
						return
					}
					_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2","title":"Child"}`))
				default:
					handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envURL, srv.URL)
			t.Setenv(envAPIKey, "test_key")

			if _, _, err := executeCLIForTest(t,
				"issue", "create",
				"--project", "PAI",
				"--title", "Child",
				"--parent", tc.parentRef,
			); err != nil {
				t.Fatalf("executeCLIForTest: %v", err)
			}
			if handlerErr != "" {
				t.Fatal(handlerErr)
			}
			if _, ok := received["parent_id"].(float64); !ok {
				t.Fatalf("parent_id type = %T, want JSON number (body=%v)", received["parent_id"], received)
			}
			if received["parent_id"].(float64) != 101 {
				t.Errorf("parent_id=%v, want 101", received["parent_id"])
			}
		})
	}
}

func TestIssueUpdateParentRefSendsNumericID(t *testing.T) {
	var received map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-1":
			_, _ = w.Write([]byte(`{"id":101,"issue_key":"PAI-1"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/issues/PAI-2":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				handlerErr = fmt.Sprintf("decode request: %v", err)
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2","title":"Child","parent_id":101}`))
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	if _, _, err := executeCLIForTest(t,
		"issue", "update", "PAI-2",
		"--parent", "PAI-1",
	); err != nil {
		t.Fatalf("executeCLIForTest: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	if _, ok := received["parent_id"].(float64); !ok {
		t.Fatalf("parent_id type = %T, want JSON number (body=%v)", received["parent_id"], received)
	}
	if received["parent_id"].(float64) != 101 {
		t.Errorf("parent_id=%v, want 101", received["parent_id"])
	}
}

func TestIssueUpdateInvalidAssigneeFailsBeforeRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":"should not be called"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, _, err := executeCLIForTest(t,
		"issue", "update", "PAI-1",
		"--status", "qa",
		"--assignee", "mba",
	)
	if err == nil {
		t.Fatal("expected invalid assignee error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("err type=%T, want *usageError (%v)", err, err)
	}
	if requests != 0 {
		t.Fatalf("requests=%d, want 0", requests)
	}
}

// ── PAI-260: issue tag add/rm helpers ────────────────────────────────

// TestRequireTagSelector pins the "exactly one of --tag / --tag-id"
// rule. Cobra's MarkFlagsMutuallyExclusive handles "both set" at parse
// time; the "neither set" case has to come from us so the user sees a
// helpful message instead of a silent no-op.
func TestRequireTagSelector(t *testing.T) {
	cases := []struct {
		name    string
		tagKey  string
		tagID   int64
		wantErr bool
	}{
		{"key only", "dev", 0, false},
		{"id only", "", 99, false},
		{"key with whitespace counts as set", "  dev  ", 0, false},
		{"neither", "", 0, true},
		{"only whitespace key", "   ", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := requireTagSelector(c.tagKey, c.tagID)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if c.wantErr && !containsFold(err.Error(), "--tag") {
				t.Errorf("expected the error to mention --tag, got %q", err.Error())
			}
		})
	}
}

// TestResolveTagSelector exercises the /api/tags lookup against an
// httptest server. The CLI uses this both for --tag <key> resolution
// (the common path) and for --tag-id validation (so a typo'd id fails
// here rather than as a silent no-op against the idempotent upstream
// DELETE endpoint).
func TestResolveTagSelector(t *testing.T) {
	srv := startFakeAPI(t, map[string]string{
		"GET /api/tags": `[
		  {"id": 99,  "name": "dev",  "color": "blue"},
		  {"id": 100, "name": "ops",  "color": "green"},
		  {"id": 200, "name": "lane:special", "color": "purple"}
		]`,
	})
	client := newClientForTest(srv.URL)

	cases := []struct {
		name    string
		tagKey  string
		tagID   int64
		wantID  int64
		wantNm  string
		wantErr string
	}{
		{name: "by key (dev)", tagKey: "dev", wantID: 99, wantNm: "dev"},
		{name: "by key with surrounding whitespace", tagKey: "  ops  ", wantID: 100, wantNm: "ops"},
		{name: "by id", tagID: 200, wantID: 200, wantNm: "lane:special"},
		{name: "id wins when both supplied (no precedence test in code, but id branch fires first)", tagID: 99, tagKey: "ignored", wantID: 99, wantNm: "dev"},
		{name: "unknown key 404s", tagKey: "nonexistent", wantErr: "not found"},
		{name: "unknown id 404s", tagID: 99999, wantErr: "not found"},
		{name: "case-sensitive (DEV != dev)", tagKey: "DEV", wantErr: "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveTagSelector(client, c.tagKey, c.tagID)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected err containing %q, got nil", c.wantErr)
				}
				if !containsFold(err.Error(), c.wantErr) {
					t.Fatalf("err = %q, want substring %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.ID != c.wantID || got.Name != c.wantNm {
				t.Errorf("got {%d, %q}, want {%d, %q}", got.ID, got.Name, c.wantID, c.wantNm)
			}
		})
	}
}

// ── PAI-791: safe create/update tag flags ───────────────────────────

func TestNormalizeTagNames(t *testing.T) {
	got, err := normalizeTagNames("tags", []string{" host:hsb9 ", "home-assistant", "host:hsb9"})
	if err != nil {
		t.Fatalf("normalizeTagNames: %v", err)
	}
	if strings.Join(got, "|") != "host:hsb9|home-assistant" {
		t.Fatalf("names=%v, want trimmed order-preserving dedupe", got)
	}
	for _, raw := range [][]string{{""}, {"ops", "  "}} {
		if _, err := normalizeTagNames("tags", raw); err == nil {
			t.Fatalf("normalizeTagNames(%q) succeeded, want empty-name error", raw)
		}
	}
}

func TestResolveManualTagNamesSystemRules(t *testing.T) {
	catalog := []resolvedTag{
		{ID: 1, Name: "CUSTOMERPORTAL", System: true},
		{ID: 2, Name: "AT_RISK", System: true},
	}
	got, err := resolveManualTagNamesFromCatalog(catalog, []string{"CUSTOMERPORTAL"})
	if err != nil || len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("portal visibility tag: got=%v err=%v, want allowed system-tag exception", got, err)
	}
	if _, err := resolveManualTagNamesFromCatalog(catalog, []string{"AT_RISK"}); err == nil {
		t.Fatal("ordinary system tag was accepted for manual mutation")
	}
}

func TestIssueCreateWithTagsPreflightsAndAttaches(t *testing.T) {
	var paths []string
	var attached []int64
	var createBody map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_, _ = w.Write([]byte(`[
				{"id":41,"name":"host:hsb9","system":false},
				{"id":42,"name":"home-assistant","system":false}
			]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/issues":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				handlerErr = fmt.Sprintf("decode create: %v", err)
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2","title":"Tagged issue"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/202/tags":
			var body struct {
				TagID int64 `json:"tag_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				handlerErr = fmt.Sprintf("decode tag: %v", err)
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			attached = append(attached, body.TagID)
			w.WriteHeader(http.StatusNoContent)
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t,
		"issue", "create", "--project", "PAI", "--title", "Tagged issue",
		"--tags", " host:hsb9,home-assistant,host:hsb9 ",
	)
	if err != nil {
		t.Fatalf("issue create: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	wantPaths := []string{
		"GET /api/tags",
		"GET /api/projects",
		"POST /api/projects/6/issues",
		"POST /api/issues/202/tags",
		"POST /api/issues/202/tags",
	}
	if strings.Join(paths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("paths:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(wantPaths, "\n"))
	}
	if fmt.Sprint(attached) != "[41 42]" {
		t.Fatalf("attached=%v, want [41 42]", attached)
	}
	if _, exists := createBody["tags"]; exists {
		t.Fatalf("create body unexpectedly contains tags: %v", createBody)
	}
	if !strings.Contains(out, "tags: host:hsb9, home-assistant") {
		t.Fatalf("output=%q, want applied tags", out)
	}
}

func TestIssueCreateTagsPreflightFailureDoesNotCreate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog string
		tag     string
		wantErr string
	}{
		{name: "unknown", catalog: `[{"id":41,"name":"known","system":false}]`, tag: "missing", wantErr: "not found"},
		{name: "forbidden system", catalog: `[{"id":41,"name":"AT_RISK","system":true}]`, tag: "AT_RISK", wantErr: "cannot be modified manually"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, writes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet {
					writes++
				}
				if r.Method == http.MethodGet && r.URL.Path == "/api/tags" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.catalog))
					return
				}
				http.Error(w, `{"error":"unexpected request"}`, http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envURL, srv.URL)
			t.Setenv(envAPIKey, "test_key")

			_, _, err := executeCLIForTest(t,
				"issue", "create", "--project", "PAI", "--title", "No partial create", "--tags", tc.tag,
			)
			if err == nil || !containsFold(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v, want %q", err, tc.wantErr)
			}
			if requests != 1 || writes != 0 {
				t.Fatalf("requests=%d writes=%d, want one catalog read and zero writes", requests, writes)
			}
		})
	}
}

func TestIssueCreateTagFailureNamesCreatedIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_, _ = w.Write([]byte(`[{"id":41,"name":"ops","system":false}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/issues":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2","title":"Created first"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/202/tags":
			http.Error(w, `{"error":"attach failed"}`, http.StatusInternalServerError)
		default:
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, errOut, err := executeCLIForTest(t,
		"--json", "issue", "create", "--project", "PAI", "--title", "Created first", "--tags", "ops",
	)
	if err == nil || !strings.Contains(err.Error(), "created PAI-2 (id 202), but add tag \"ops\" failed") {
		t.Fatalf("err=%v, want explicit partial-create boundary", err)
	}
	if _, ok := err.(*apiError); !ok {
		t.Fatalf("err type=%T, want *apiError for already-reported JSON failure", err)
	}
	var problem map[string]any
	if json.Unmarshal([]byte(errOut), &problem) != nil || !strings.Contains(fmt.Sprint(problem["error"]), "created PAI-2") {
		t.Fatalf("stderr=%q, want machine-readable partial-create boundary", errOut)
	}
}

func TestIssueUpdateTagChangesPreflightAndApply(t *testing.T) {
	var paths []string
	var putBody map[string]any
	var addBody map[string]any
	var handlerErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-2":
			_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_, _ = w.Write([]byte(`[
				{"id":41,"name":"old","system":false},
				{"id":42,"name":"new","system":false}
			]`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/issues/202":
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				handlerErr = fmt.Sprintf("decode put: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2","title":"Changed"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/issues/202/tags/41":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/202/tags":
			if err := json.NewDecoder(r.Body).Decode(&addBody); err != nil {
				handlerErr = fmt.Sprintf("decode add: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			handlerErr = fmt.Sprintf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t,
		"--json", "issue", "update", "PAI-2", "--title", "Changed",
		"--add-tag", "new,new", "--remove-tag", "old",
	)
	if err != nil {
		t.Fatalf("issue update: %v", err)
	}
	if handlerErr != "" {
		t.Fatal(handlerErr)
	}
	wantPaths := []string{
		"GET /api/issues/PAI-2",
		"GET /api/tags",
		"PUT /api/issues/202",
		"DELETE /api/issues/202/tags/41",
		"POST /api/issues/202/tags",
	}
	if strings.Join(paths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("paths:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(wantPaths, "\n"))
	}
	if putBody["title"] != "Changed" || len(putBody) != 1 {
		t.Fatalf("putBody=%v, want field-only update", putBody)
	}
	if addBody["tag_id"] != float64(42) {
		t.Fatalf("addBody=%v, want tag_id 42", addBody)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if fmt.Sprint(result["tags_added"]) != "[new]" || fmt.Sprint(result["tags_removed"]) != "[old]" {
		t.Fatalf("result=%v, want applied tag names", result)
	}
}

func TestIssueUpdateTagOnlySkipsFieldUpdate(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/PAI-2":
			_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_, _ = w.Write([]byte(`[{"id":42,"name":"ops","system":false}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/202/tags":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, `{"error":"unexpected request"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "issue", "update", "PAI-2", "--add-tag", "ops")
	if err != nil {
		t.Fatalf("tag-only update: %v", err)
	}
	wantPaths := []string{"GET /api/issues/PAI-2", "GET /api/tags", "POST /api/issues/202/tags"}
	if strings.Join(paths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("paths=%v, want %v (no PUT)", paths, wantPaths)
	}
	if !strings.Contains(out, "updated PAI-2 tags (+ops)") {
		t.Fatalf("output=%q, want tag-only success", out)
	}
}

func TestIssueUpdateTagValidationBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantErr   string
		wantReads int
		catalog   string
	}{
		{
			name: "same tag in add and remove", args: []string{"--add-tag", "ops", "--remove-tag", "ops"},
			wantErr: "both added and removed", wantReads: 0,
		},
		{
			name: "empty tag name", args: []string{"--add-tag", "ops,,home"},
			wantErr: "empty tag name", wantReads: 0,
		},
		{
			name: "unknown tag before field update", args: []string{"--title", "Changed", "--add-tag", "missing"},
			wantErr: "not found", wantReads: 2, catalog: `[{"id":41,"name":"known","system":false}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, writes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					reads++
				} else {
					writes++
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/issues/PAI-2":
					_, _ = w.Write([]byte(`{"id":202,"issue_key":"PAI-2"}`))
				case "/api/tags":
					_, _ = w.Write([]byte(tc.catalog))
				default:
					http.Error(w, `{"error":"unexpected write"}`, http.StatusInternalServerError)
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envURL, srv.URL)
			t.Setenv(envAPIKey, "test_key")

			args := append([]string{"issue", "update", "PAI-2"}, tc.args...)
			_, _, err := executeCLIForTest(t, args...)
			if err == nil || !containsFold(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v, want %q", err, tc.wantErr)
			}
			if reads != tc.wantReads || writes != 0 {
				t.Fatalf("reads=%d writes=%d, want reads=%d writes=0", reads, writes, tc.wantReads)
			}
		})
	}
}

func TestIssueTagFlagsDryRunIsNetworkFree(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":"must not be called"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t,
		"--json", "issue", "update", "PAI-2", "--add-tag", "ops,host:hsb9", "--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests=%d, want 0", requests)
	}
	var result struct {
		Method     string `json:"method"`
		TagChanges struct {
			Add []string `json:"add"`
		} `json:"tag_changes"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if strings.Join(result.TagChanges.Add, ",") != "ops,host:hsb9" {
		t.Fatalf("tag_changes.add=%v", result.TagChanges.Add)
	}
	if result.Method != "" {
		t.Fatalf("method=%q, want no phantom field-update request for tag-only dry-run", result.Method)
	}
}

func TestIssueCreateTagsDryRunIsNetworkFree(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, `{"error":"must not be called"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t,
		"--json", "issue", "create", "--project", "PAI", "--title", "Dry", "--tags", "ops,host:hsb9", "--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests=%d, want 0", requests)
	}
	var result struct {
		Method     string `json:"method"`
		TagChanges struct {
			Add []string `json:"add"`
		} `json:"tag_changes"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if result.Method != "POST" || strings.Join(result.TagChanges.Add, ",") != "ops,host:hsb9" {
		t.Fatalf("result=%+v, want create plus unresolved tag plan", result)
	}
}

func TestIssueUpdateMoveRejectsTagChanges(t *testing.T) {
	t.Setenv(envURL, "https://example.test")
	t.Setenv(envAPIKey, "test_key")
	_, _, err := executeCLIForTest(t,
		"issue", "update", "PAI-2", "--project", "OPS", "--add-tag", "ops",
	)
	if err == nil || !containsFold(err.Error(), "can't be combined") {
		t.Fatalf("err=%v, want move/tag incompatibility", err)
	}
}
