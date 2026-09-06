// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
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
