// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func commandPaletteRequest(t *testing.T, ts *testServer, method, path, cookie, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.srv.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
			req.Header.Set("Origin", ts.srv.URL)
		}
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertCommandPaletteError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, status, body)
	}
	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 || payload["error"] != code {
		t.Fatalf("payload=%v want error=%q", payload, code)
	}
}

func TestCommandPaletteSettingsPrecedenceResetAndProfileProjection(t *testing.T) {
	ts := newTestServer(t)
	response := ts.get(t, "/api/command-palette/v1/settings", ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	var settings models.CommandPaletteSettings
	decode(t, response, &settings)
	if settings.SchemaVersion != 1 || settings.DefaultShortcut != "Mod+KeyK" || settings.InstanceShortcut != nil ||
		settings.UserShortcut != nil || settings.EffectiveShortcut != "Mod+KeyK" || settings.Source != "default" {
		t.Fatalf("fresh settings=%+v", settings)
	}

	response = commandPaletteRequest(t, ts, http.MethodPut, "/api/command-palette/v1/instance-settings", ts.adminCookie, `{"shortcut":"Alt+KeyG"}`)
	assertStatus(t, response, http.StatusOK)
	decode(t, response, &settings)
	if settings.InstanceShortcut == nil || *settings.InstanceShortcut != "Alt+KeyG" || settings.EffectiveShortcut != "Alt+KeyG" || settings.Source != "instance" {
		t.Fatalf("instance settings=%+v", settings)
	}

	response = commandPaletteRequest(t, ts, http.MethodPut, "/api/command-palette/v1/settings", ts.memberCookie, `{"shortcut":"Ctrl+Shift+KeyK"}`)
	assertStatus(t, response, http.StatusOK)
	decode(t, response, &settings)
	if settings.UserShortcut == nil || *settings.UserShortcut != "Ctrl+Shift+KeyK" || settings.EffectiveShortcut != "Ctrl+Shift+KeyK" || settings.Source != "user" {
		t.Fatalf("user settings=%+v", settings)
	}

	me := ts.get(t, "/api/auth/me", ts.memberCookie)
	assertStatus(t, me, http.StatusOK)
	var envelope struct {
		User models.User `json:"user"`
	}
	decode(t, me, &envelope)
	if envelope.User.CommandPaletteShortcut == nil || *envelope.User.CommandPaletteShortcut != "Ctrl+Shift+KeyK" {
		t.Fatalf("auth profile command_palette_shortcut=%v", envelope.User.CommandPaletteShortcut)
	}

	response = commandPaletteRequest(t, ts, http.MethodPut, "/api/command-palette/v1/instance-settings", ts.adminCookie, `{"shortcut":null}`)
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	response = commandPaletteRequest(t, ts, http.MethodPut, "/api/command-palette/v1/settings", ts.memberCookie, `{"shortcut":null}`)
	assertStatus(t, response, http.StatusOK)
	decode(t, response, &settings)
	if settings.InstanceShortcut != nil || settings.UserShortcut != nil || settings.EffectiveShortcut != "Mod+KeyK" || settings.Source != "default" {
		t.Fatalf("reset settings=%+v", settings)
	}
}

func TestCommandPaletteSettingsStrictValidationAuthorizationAndNoStore(t *testing.T) {
	ts := newTestServer(t)
	for _, test := range []struct {
		body string
		code string
	}{
		{`{"shortcut":"Mod+KeyK","extra":true}`, "invalid_request"},
		{`{"shortcut":"Mod+KeyK","shortcut":null}`, "invalid_request"},
		{`{"shortcut":"Mod+KeyK"} {}`, "invalid_request"},
		{`{"shortcut":"Shift+KeyK"}`, "invalid_shortcut"},
		{`{"shortcut":"Mod+KeyR"}`, "shortcut_collision"},
	} {
		assertCommandPaletteError(t, commandPaletteRequest(t, ts, http.MethodPut, "/api/command-palette/v1/settings", ts.memberCookie, test.body),
			http.StatusBadRequest, test.code)
	}
	assertCommandPaletteError(t, commandPaletteRequest(t, ts, http.MethodPut, "/api/command-palette/v1/instance-settings", ts.memberCookie, `{"shortcut":"Alt+KeyG"}`),
		http.StatusForbidden, "forbidden")
	for name, response := range map[string]*http.Response{
		"unauthenticated": ts.get(t, "/api/command-palette/v1/settings", ""),
		"external":        ts.get(t, "/api/command-palette/v1/settings", ts.externalCookie),
		"unknown":         ts.get(t, "/api/command-palette/v1/not-a-route", ""),
	} {
		response.Body.Close()
		if response.StatusCode < 400 || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s status=%d Cache-Control=%q", name, response.StatusCode, response.Header.Get("Cache-Control"))
		}
	}
}

func TestCommandPaletteSearchIsGroupedBoundedDeterministicAndProjectScoped(t *testing.T) {
	ts := newTestServer(t)
	projectID := createTestProject(t, ts, "Palette", "PAL")
	foreignID := createTestProject(t, ts, "Foreign", "FOR")
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')
		ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='none'`, memberID, foreignID); err != nil {
		t.Fatal(err)
	}
	for index, title := range []string{"Alpha", "Alphabet", "Z alpha"} {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		if _, err := db.DB.Exec(`INSERT INTO product_sessions(product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id)
			VALUES(?,?,'paimos',?,'safe summary',?,?)`, id, projectID, title, memberID, memberID); err != nil {
			t.Fatal(err)
		}
	}
	for index, title := range []string{"Alpha", "Alphabet", "Z alpha"} {
		if _, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status) VALUES(?,?, 'ticket',?,'hidden node body','backlog')`,
			projectID, index+1, title); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,slug) VALUES(?,?, 'memory',?,'hidden knowledge body','backlog',?)`,
			projectID, index+101, title, strings.ToLower(strings.ReplaceAll(title, " ", "-"))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id)
		VALUES('99999999-9999-4999-8999-999999999999',?,'paimos','Foreign alpha secret','never leak',?,?)`, foreignID, memberID, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id)
		VALUES('88888888-8888-4888-8888-888888888888',?,'paimos','Ärger','unicode fold',?,?)`, projectID, memberID, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO product_sessions(product_session_id,project_id,target_kind,title,summary,created_by_user_id,updated_by_user_id)
		VALUES('77777777-7777-4777-8777-777777777777',?,'paimos','Straße','full unicode fold',?,?)`, projectID, memberID, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,description,status,slug)
		VALUES(?,999,'memory','Body only','hiddenneedle','backlog','body-only')`, projectID); err != nil {
		t.Fatal(err)
	}

	response := ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?q=%%20alpha%%20&limit=2", projectID), ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(raw), "project_id") || strings.Contains(string(raw), "description") || strings.Contains(string(raw), "hidden") {
		t.Fatalf("search leaked forbidden fields/content: %s", raw)
	}
	var result models.CommandPaletteSearchResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Query != "alpha" || len(result.Sessions) != 2 || len(result.Nodes) != 2 || len(result.Knowledge) != 2 {
		t.Fatalf("grouped result=%+v", result)
	}
	if result.Sessions[0].Title != "Alpha" || result.Sessions[1].Title != "Alphabet" ||
		result.Nodes[0].Title != "Alpha" || result.Nodes[1].Title != "Alphabet" ||
		result.Knowledge[0].Title != "Alpha" || result.Knowledge[0].Type != "memory" {
		t.Fatalf("deterministic order/result=%+v", result)
	}
	assertFrontendTimestamp := func(name, value string) {
		t.Helper()
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil || len(value) != len("2026-08-31T12:00:00.000Z") || value[10] != 'T' || value[19] != '.' || !strings.HasSuffix(value, "Z") {
			t.Fatalf("%s updated_at=%q is not canonical millisecond RFC3339 UTC: %v", name, value, err)
		}
	}
	assertFrontendTimestamp("default-timestamp node", result.Nodes[0].UpdatedAt)
	assertFrontendTimestamp("default-timestamp knowledge", result.Knowledge[0].UpdatedAt)

	response = ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?q=hiddenneedle", projectID), ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	decode(t, response, &result)
	if len(result.Sessions) != 0 || len(result.Nodes) != 0 || len(result.Knowledge) != 0 {
		t.Fatalf("hidden body was searchable: %+v", result)
	}
	response = ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?q=är", projectID), ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	decode(t, response, &result)
	if len(result.Sessions) != 1 || result.Sessions[0].Title != "Ärger" {
		t.Fatalf("Unicode case-folded search=%+v", result.Sessions)
	}
	response = ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?q=strasse", projectID), ts.memberCookie)
	assertStatus(t, response, http.StatusOK)
	decode(t, response, &result)
	if len(result.Sessions) != 1 || result.Sessions[0].Title != "Straße" {
		t.Fatalf("full Unicode case-folded search=%+v", result.Sessions)
	}
	foreign := ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?q=alpha", foreignID), ts.memberCookie)
	foreign.Body.Close()
	if foreign.StatusCode != http.StatusNotFound || foreign.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("foreign status=%d Cache-Control=%q", foreign.StatusCode, foreign.Header.Get("Cache-Control"))
	}
	assertCommandPaletteError(t, ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?q=alpha&q=beta", projectID), ts.adminCookie),
		http.StatusBadRequest, "invalid_request")
	for _, suffix := range []string{"q=alpha&limit=0", "q=alpha&limit=21", "q=alpha&limit=01", "q=alpha&extra=1"} {
		assertCommandPaletteError(t, ts.get(t, fmt.Sprintf("/api/projects/%d/command-palette/v1?%s", projectID, suffix), ts.adminCookie),
			http.StatusBadRequest, "invalid_request")
	}
}
