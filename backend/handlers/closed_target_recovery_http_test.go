// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClosedTargetRecoveryHTTPIsAdminGatedAndContentFree(t *testing.T) {
	ts := newTestServer(t)
	created := ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Recovery HTTP", "key": "RHTTP"})
	assertStatus(t, created, http.StatusCreated)
	var project struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	path := fmt.Sprintf("/api/projects/%d/message-deliveries/private-delivery-canary/closed-target-recovery?expected_closed_session_id=private-closed-canary&replacement_session_id=private-replacement-canary", project.ID)

	member := ts.get(t, path, ts.memberCookie)
	assertStatus(t, member, http.StatusForbidden)
	memberBody, _ := io.ReadAll(member.Body)
	member.Body.Close()
	if strings.Contains(string(memberBody), "private-") {
		t.Fatalf("authorization refusal leaked recovery binding: %s", memberBody)
	}

	admin := ts.get(t, path, ts.adminCookie)
	assertStatus(t, admin, http.StatusBadRequest)
	adminBody, _ := io.ReadAll(admin.Body)
	admin.Body.Close()
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(adminBody, &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "agent_message_delivery_recovery_unknown" || strings.Contains(problem.Detail, "private-") {
		t.Fatalf("unexpected content-free recovery response: %s", adminBody)
	}
	if got := admin.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestClosedTargetRecoveryHTTPRejectsAmbiguousInputsWithFixedDetail(t *testing.T) {
	ts := newTestServer(t)
	created := ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Recovery strict", "key": "RSTRICT"})
	assertStatus(t, created, http.StatusCreated)
	var project struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	base := fmt.Sprintf("/api/projects/%d/message-deliveries/delivery/closed-target-recovery", project.ID)
	for _, path := range []string{
		base + "?expected_closed_session_id=old&replacement_session_id=new&extra=value",
		base + "?expected_closed_session_id=old&expected_closed_session_id=other&replacement_session_id=new",
		base + "?expected_closed_session_id=old&replacement_session_id=new&expected_target_id=target",
	} {
		response := ts.get(t, path, ts.adminCookie)
		assertStatus(t, response, http.StatusBadRequest)
		var problem struct{ Code, Detail string }
		if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if problem.Code != "agent_message_request_invalid" || problem.Detail != "closed-target recovery query is invalid" {
			t.Fatalf("unexpected query refusal: %+v", problem)
		}
	}
	body := `{"expected_closed_session_id":"old","expected_target_id":"target","expected_target_version":1,"expected_consumer_fence":0,"replacement_session_id":"new"}`
	for _, test := range []struct{ name, path, body string }{
		{name: "trailing object", path: base, body: body + `{}`},
		{name: "duplicate field", path: base, body: `{"expected_closed_session_id":"old","expected_closed_session_id":"other"}`},
		{name: "mutation query", path: base + "?expected_target_id=target", body: body},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.srv.URL+test.path, bytes.NewBufferString(test.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", ts.adminCookie)
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			assertStatus(t, response, http.StatusBadRequest)
			var problem struct{ Code, Detail string }
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if problem.Code != "agent_message_request_invalid" || problem.Detail != "closed-target recovery request is invalid" {
				t.Fatalf("unexpected body refusal: %+v", problem)
			}
		})
	}
}
