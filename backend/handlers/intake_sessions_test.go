// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
)

type intakeSessionResp struct {
	ID              int64  `json:"id"`
	UserID          int64  `json:"user_id"`
	Status          string `json:"status"`
	Language        string `json:"language"`
	TranscriptBytes int    `json:"transcript_bytes"`
	Rev             int64  `json:"rev"`
	PinnedProjectID *int64 `json:"pinned_project_id"`
}

type intakeStateResp struct {
	AtSeq      int64                      `json:"at_seq"`
	Transcript string                     `json:"transcript"`
	Artifacts  map[string]json.RawMessage `json:"artifacts"`
}

type intakeHeadResp struct {
	Session     intakeSessionResp `json:"session"`
	State       intakeStateResp   `json:"state"`
	Checkpoints []struct {
		Seq   int64  `json:"seq"`
		Label string `json:"label"`
	} `json:"checkpoints"`
}

func createIntakeSession(t *testing.T, ts *testServer, cookie string) intakeSessionResp {
	t.Helper()
	resp := ts.post(t, "/api/intake/sessions", cookie, map[string]any{})
	assertStatus(t, resp, http.StatusCreated)
	var s intakeSessionResp
	decode(t, resp, &s)
	if s.ID == 0 {
		t.Fatal("session id is 0")
	}
	return s
}

func postChunk(t *testing.T, ts *testServer, cookie string, id int64, text string) map[string]any {
	t.Helper()
	resp := ts.post(t, "/api/intake/sessions/"+itoa(id)+"/transcript", cookie,
		map[string]any{"text": text})
	assertStatus(t, resp, http.StatusOK)
	var out map[string]any
	decode(t, resp, &out)
	return out
}

// TestIntakeSession_LifecycleAndTimeTravel covers the PAI-704 core loop:
// chunks accumulate the transcript, manual spec edits become revisions,
// checkpoints mark seqs, and an append-only restore rewinds state without
// rewriting history.
func TestIntakeSession_LifecycleAndTimeTravel(t *testing.T) {
	ts := newTestServer(t)
	s := createIntakeSession(t, ts, ts.memberCookie)

	postChunk(t, ts, ts.memberCookie, s.ID, "We need a welcome email flow.")
	postChunk(t, ts, ts.memberCookie, s.ID, "It should support German and English.")

	// Manual spec edit (works with AI unconfigured — capture always works).
	resp := ts.patch(t, "/api/intake/sessions/"+itoa(s.ID), ts.memberCookie,
		map[string]any{"spec_markdown": "# Spec v1"})
	assertStatus(t, resp, http.StatusOK)

	// Checkpoint at spec v1.
	resp = ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/checkpoints", ts.memberCookie,
		map[string]any{"label": "v1 frozen"})
	assertStatus(t, resp, http.StatusCreated)
	var cp struct {
		Seq int64 `json:"seq"`
	}
	decode(t, resp, &cp)

	// More material after the checkpoint.
	postChunk(t, ts, ts.memberCookie, s.ID, "Actually also track link clicks.")
	resp = ts.patch(t, "/api/intake/sessions/"+itoa(s.ID), ts.memberCookie,
		map[string]any{"spec_markdown": "# Spec v2"})
	assertStatus(t, resp, http.StatusOK)

	// Head state shows v2 and the 3-chunk transcript.
	resp = ts.get(t, "/api/intake/sessions/"+itoa(s.ID), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var head intakeHeadResp
	decode(t, resp, &head)
	if !strings.Contains(string(head.State.Artifacts["spec"]), "Spec v2") {
		t.Fatalf("head spec = %s, want v2", head.State.Artifacts["spec"])
	}
	if !strings.Contains(head.State.Transcript, "link clicks") {
		t.Fatalf("head transcript missing post-checkpoint chunk: %q", head.State.Transcript)
	}
	if len(head.Checkpoints) != 1 || head.Checkpoints[0].Label != "v1 frozen" {
		t.Fatalf("checkpoints = %+v", head.Checkpoints)
	}
	headRev := head.Session.Rev

	// State as-of the checkpoint: spec v1, 2-chunk transcript.
	resp = ts.get(t, "/api/intake/sessions/"+itoa(s.ID)+"/state?at_seq="+itoa(cp.Seq), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var atCp intakeStateResp
	decode(t, resp, &atCp)
	if !strings.Contains(string(atCp.Artifacts["spec"]), "Spec v1") {
		t.Fatalf("as-of spec = %s, want v1", atCp.Artifacts["spec"])
	}
	if strings.Contains(atCp.Transcript, "link clicks") {
		t.Fatalf("as-of transcript should not contain post-checkpoint chunk: %q", atCp.Transcript)
	}

	// Restore to the checkpoint. Append-only: rev must grow, not rewind.
	resp = ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/restore", ts.memberCookie,
		map[string]any{"seq": cp.Seq})
	assertStatus(t, resp, http.StatusOK)
	var restored intakeHeadResp
	decode(t, resp, &restored)
	if restored.Session.Rev <= headRev {
		t.Fatalf("restore must append events: rev %d -> %d", headRev, restored.Session.Rev)
	}
	if !strings.Contains(string(restored.State.Artifacts["spec"]), "Spec v1") {
		t.Fatalf("restored spec = %s, want v1", restored.State.Artifacts["spec"])
	}
	if strings.Contains(restored.State.Transcript, "link clicks") {
		t.Fatalf("restored transcript should drop post-checkpoint chunk: %q", restored.State.Transcript)
	}

	// History before the restore point is intact: state at headRev still v2.
	resp = ts.get(t, "/api/intake/sessions/"+itoa(s.ID)+"/state?at_seq="+itoa(headRev), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var atOldHead intakeStateResp
	decode(t, resp, &atOldHead)
	if !strings.Contains(string(atOldHead.Artifacts["spec"]), "Spec v2") {
		t.Fatalf("pre-restore head state must survive: %s", atOldHead.Artifacts["spec"])
	}

	// New chunks continue from the restored base.
	postChunk(t, ts, ts.memberCookie, s.ID, "Fresh direction after restore.")
	resp = ts.get(t, "/api/intake/sessions/"+itoa(s.ID), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var head2 intakeHeadResp
	decode(t, resp, &head2)
	if strings.Contains(head2.State.Transcript, "link clicks") ||
		!strings.Contains(head2.State.Transcript, "Fresh direction") {
		t.Fatalf("post-restore transcript wrong: %q", head2.State.Transcript)
	}
}

// TestIntakeSession_AuthzMatrix enforces INV-INTAKE-01: non-owner access
// answers 404 on every session route; admins pass; externals are blocked
// upstream by the internal route group.
func TestIntakeSession_AuthzMatrix(t *testing.T) {
	ts := newTestServer(t)

	// A second non-admin user so "not yours" isn't conflated with "not admin".
	resp := ts.post(t, "/api/users", ts.adminCookie, map[string]string{
		"username": "intake-other", "password": "otherpass123", "role": "member",
	})
	assertStatus(t, resp, http.StatusCreated)
	// Admin-created users start behind the must-change-password gate; this
	// test is about session ownership, not onboarding.
	if _, err := db.DB.Exec(`UPDATE users SET must_change_password=0 WHERE username='intake-other'`); err != nil {
		t.Fatalf("clear must_change_password: %v", err)
	}
	otherCookie := ts.login(t, "intake-other", "otherpass123")

	s := createIntakeSession(t, ts, ts.memberCookie)
	base := "/api/intake/sessions/" + itoa(s.ID)

	probes := []struct {
		method string
		path   string
		body   map[string]any
	}{
		{"GET", base, nil},
		{"PATCH", base, map[string]any{"language": "de"}},
		{"DELETE", base, nil},
		{"POST", base + "/transcript", map[string]any{"text": "x"}},
		{"POST", base + "/checkpoints", map[string]any{"label": "x"}},
		{"GET", base + "/events", nil},
		{"GET", base + "/state", nil},
		{"POST", base + "/restore", map[string]any{"seq": 1}},
		{"GET", base + "/stream", nil},
	}
	for _, p := range probes {
		var resp *http.Response
		switch p.method {
		case "GET":
			resp = ts.get(t, p.path, otherCookie)
		case "PATCH":
			resp = ts.patch(t, p.path, otherCookie, p.body)
		case "DELETE":
			resp = ts.del(t, p.path, otherCookie)
		case "POST":
			resp = ts.post(t, p.path, otherCookie, p.body)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s as non-owner: got %d, want 404", p.method, p.path, resp.StatusCode)
		}
	}

	// Admin passes (owner-or-admin).
	resp = ts.get(t, base, ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)

	// Unknown id → 404 (same shape as not-yours).
	resp = ts.get(t, "/api/intake/sessions/999999", ts.memberCookie)
	assertStatus(t, resp, http.StatusNotFound)

	// The owner's list shows only their own sessions.
	resp = ts.get(t, "/api/intake/sessions", otherCookie)
	assertStatus(t, resp, http.StatusOK)
	var list []intakeSessionResp
	decode(t, resp, &list)
	if len(list) != 0 {
		t.Errorf("other user sees %d sessions, want 0", len(list))
	}
}

// TestIntakeSession_CapsAndDedupe covers the size guards and client_seq
// retry dedupe.
func TestIntakeSession_CapsAndDedupe(t *testing.T) {
	ts := newTestServer(t)
	s := createIntakeSession(t, ts, ts.memberCookie)
	base := "/api/intake/sessions/" + itoa(s.ID)

	// Oversized chunk → 413.
	resp := ts.post(t, base+"/transcript", ts.memberCookie,
		map[string]any{"text": strings.Repeat("a", 8*1024+1)})
	assertStatus(t, resp, http.StatusRequestEntityTooLarge)

	// Empty chunk → 400.
	resp = ts.post(t, base+"/transcript", ts.memberCookie, map[string]any{"text": "   "})
	assertStatus(t, resp, http.StatusBadRequest)

	// client_seq dedupe: same client_seq answers the original seq, no new event.
	resp = ts.post(t, base+"/transcript", ts.memberCookie,
		map[string]any{"text": "hello", "client_seq": 7})
	assertStatus(t, resp, http.StatusOK)
	var first map[string]any
	decode(t, resp, &first)
	resp = ts.post(t, base+"/transcript", ts.memberCookie,
		map[string]any{"text": "hello again (retry)", "client_seq": 7})
	assertStatus(t, resp, http.StatusOK)
	var second map[string]any
	decode(t, resp, &second)
	if first["seq"] != second["seq"] || second["deduped"] != true {
		t.Fatalf("dedupe failed: first=%v second=%v", first, second)
	}

	// Oversized manual spec → 413.
	resp = ts.patch(t, base, ts.memberCookie,
		map[string]any{"spec_markdown": strings.Repeat("b", 48*1024+1)})
	assertStatus(t, resp, http.StatusRequestEntityTooLarge)

	// Bad language → 400.
	resp = ts.patch(t, base, ts.memberCookie, map[string]any{"language": "fr"})
	assertStatus(t, resp, http.StatusBadRequest)

	// Abandon → further writes 409.
	resp = ts.del(t, base, ts.memberCookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp = ts.post(t, base+"/transcript", ts.memberCookie, map[string]any{"text": "late"})
	assertStatus(t, resp, http.StatusConflict)
}

// TestIntakeSession_StreamReplay covers SSE resume: a client connecting with
// Last-Event-ID (or ?since=) receives every later persisted event in order.
func TestIntakeSession_StreamReplay(t *testing.T) {
	ts := newTestServer(t)
	s := createIntakeSession(t, ts, ts.memberCookie)

	postChunk(t, ts, ts.memberCookie, s.ID, "chunk one")   // seq 1
	postChunk(t, ts, ts.memberCookie, s.ID, "chunk two")   // seq 2
	postChunk(t, ts, ts.memberCookie, s.ID, "chunk three") // seq 3

	// Reconnect claiming we saw seq 1 — replay must deliver 2 and 3.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET",
		ts.srv.URL+"/api/intake/sessions/"+itoa(s.ID)+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", ts.memberCookie)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}

	var ids []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, id)
			if len(ids) == 2 {
				cancel() // got the replay we wanted; hang up
				break
			}
		}
	}
	if len(ids) != 2 || ids[0] != "2" || ids[1] != "3" {
		t.Fatalf("replayed ids = %v, want [2 3]", ids)
	}
}
