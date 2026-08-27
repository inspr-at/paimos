// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"net/http"
	"net/url"
	"testing"
)

type weekTimeEntriesResp struct {
	Users []struct {
		Username string `json:"username"`
		Days     []struct {
			Date    string  `json:"date"`
			Hours   float64 `json:"hours"`
			Entries int     `json:"entries"`
			Assumed bool    `json:"assumed"`
		} `json:"days"`
	} `json:"users"`
}

func TestWeekTimeEntries_SumsByUserAndStartedDate(t *testing.T) {
	ts := newTestServer(t)
	issueID := seedTestProjectAndIssue(t, ts)
	adminID := userIDByUsername(t, "admin")

	mustExec(t, `INSERT INTO time_entries(issue_id,user_id,started_at,stopped_at,override,comment)
		VALUES(?,?,'2026-08-24T08:00:00Z','2026-08-24T10:00:00Z',2,'Assumed hours')`, issueID, adminID)
	mustExec(t, `INSERT INTO time_entries(issue_id,user_id,started_at,stopped_at)
		VALUES(?,?,'2026-08-24T12:00:00Z','2026-08-24T13:30:00Z')`, issueID, adminID)
	mustExec(t, `INSERT INTO time_entries(issue_id,user_id,started_at)
		VALUES(?,?,'2026-08-25T09:00:00Z')`, issueID, adminID)

	resp := ts.get(t, weekPath("2026-08-24", "2026-08-30", "admin"), ts.adminCookie)
	assertStatus(t, resp, http.StatusOK)
	var out weekTimeEntriesResp
	decode(t, resp, &out)
	if len(out.Users) != 1 || len(out.Users[0].Days) != 2 {
		t.Fatalf("users/days = %d/%v, want 1/2", len(out.Users), out.Users)
	}
	if day := out.Users[0].Days[0]; day.Date != "2026-08-24" || !approxEqual(day.Hours, 3.5, 0.001) || day.Entries != 2 || !day.Assumed {
		t.Fatalf("first day = %+v, want 2026-08-24 / 3.5h / 2 entries / assumed", day)
	}
	if day := out.Users[0].Days[1]; day.Date != "2026-08-25" || day.Hours != 0 || day.Entries != 1 {
		t.Fatalf("running-timer day = %+v, want 2026-08-25 / 0h / 1 entry", day)
	}
}

func TestWeekTimeEntries_OmitsUnauthorizedOtherUser(t *testing.T) {
	ts := newTestServer(t)
	issueID := seedTestProjectAndIssue(t, ts)
	memberID := userIDByUsername(t, "member")
	adminID := userIDByUsername(t, "admin")
	mustExec(t, `INSERT INTO time_entries(issue_id,user_id,started_at,stopped_at,override)
		VALUES(?,?,'2026-08-24T08:00:00Z','2026-08-24T09:00:00Z',1)`, issueID, memberID)
	mustExec(t, `INSERT INTO time_entries(issue_id,user_id,started_at,stopped_at,override)
		VALUES(?,?,'2026-08-24T08:00:00Z','2026-08-24T10:00:00Z',2)`, issueID, adminID)

	resp := ts.get(t, weekPath("2026-08-24", "2026-08-30", "member,admin"), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var out weekTimeEntriesResp
	decode(t, resp, &out)
	if len(out.Users) != 1 || out.Users[0].Username != "member" {
		t.Fatalf("visible users = %+v, want member only", out.Users)
	}
}

func weekPath(from, to, usernames string) string {
	query := url.Values{}
	query.Set("from", from)
	query.Set("to", to)
	query.Set("usernames", usernames)
	return "/api/time-entries/week?" + query.Encode()
}
