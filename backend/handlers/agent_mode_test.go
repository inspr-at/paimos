package handlers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/handlers"
)

func TestAgentModeExternalInaccessibleAndMissingShareCanonical404(t *testing.T) {
	ts := newTestServer(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Hidden Agent Mode','HAM','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	var memberID, externalID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='external'`).Scan(&externalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'none'),(?,?,'editor')`,
		projectID, memberID, projectID, externalID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		path   string
		cookie string
	}{
		{"external all", "/api/agent-mode/deliveries", ts.externalCookie},
		{"external explicit grant", "/api/agent-mode/projects/" + intString(projectID) + "/deliveries", ts.externalCookie},
		{"member explicit none", "/api/agent-mode/projects/" + intString(projectID) + "/deliveries", ts.memberCookie},
		{"missing project", "/api/agent-mode/projects/999999/deliveries", ts.memberCookie},
		{"missing detail", "/api/agent-mode/deliveries/delivery:missing", ts.memberCookie},
		{"missing selector", "/api/agent-mode/deliveries?selected_delivery=delivery:missing", ts.memberCookie},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := ts.get(t, test.path, test.cookie)
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNotFound || response.Header.Get("Content-Type") != "application/problem+json" ||
				response.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
			}
			var problem map[string]any
			if err := json.Unmarshal(body, &problem); err != nil {
				t.Fatal(err)
			}
			delete(problem, "instance")
			delete(problem, "request_id")
			encoded, _ := json.Marshal(problem)
			want := `{"code":"not_found","detail":"not found","error":"not found","status":404,"title":"Not Found","type":"https://paimos.com/errors/not_found"}`
			if string(encoded) != want {
				t.Fatalf("normalized problem=%s", encoded)
			}
		})
	}
}

func TestAgentModeEmptyAuthorizedHistoryKeepsFixedSelectionField(t *testing.T) {
	ts := newTestServer(t)
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT OR REPLACE INTO project_members(project_id,user_id,access_level)
		SELECT id,?,'none' FROM projects`, memberID); err != nil {
		t.Fatal(err)
	}

	response := ts.get(t, "/api/agent-mode/deliveries", ts.memberCookie)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "private, no-store" {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
	}
	var wire map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	selected, ok := wire["selected_delivery"]
	if !ok || string(selected) != `""` {
		t.Fatalf("selected_delivery present=%t value=%s envelope=%v", ok, selected, wire)
	}
	if _, ok := wire["selected_outside"]; ok {
		t.Fatalf("selected_outside must remain the sole optional top-level field: %v", wire)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(wire["rows"], &rows); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%s err=%v", wire["rows"], err)
	}
}

func TestAgentModeHTTPSnapshotSerializerMatchesProductionReaderDTO(t *testing.T) {
	ts := newTestServer(t)
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Serializer parity','SER','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`,
		projectID, memberID); err != nil {
		t.Fatal(err)
	}
	issue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Serializer production row','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',0)`, issueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}

	response := ts.get(t, "/api/agent-mode/deliveries", ts.memberCookie)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" ||
		response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
	}
	var got agentmode.Snapshot
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if decoder.More() {
		t.Fatal("Agent Mode HTTP response has trailing JSON")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if !bytes.Equal(body, encoded) {
		t.Fatalf("Agent Mode handler is not the canonical production DTO plus one LF\nbody=%s\nencoded=%s", body, encoded)
	}

	expected, err := agentmode.NewReader(db.DB, agentmode.ReaderOptions{
		Clock: delivery.ClockFunc(func() time.Time { return got.ServerTime }),
	}).Read(context.Background(), agentmode.Request{UserID: memberID,
		Filters: agentmode.Filters{Attention: "all", Health: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	got.Cursor, expected.Cursor = "normalized-cursor", "normalized-cursor"
	gotJSON, _ := json.Marshal(got)
	expectedJSON, _ := json.Marshal(expected)
	if !bytes.Equal(gotJSON, expectedJSON) {
		t.Fatalf("HTTP and direct production Reader semantics differ after cursor normalization\nhttp=%s\nreader=%s",
			gotJSON, expectedJSON)
	}
}

func TestAgentModeUnlinkedV1IsPrivateUnavailableBeforeStreamHeaders(t *testing.T) {
	ts := newTestServer(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Invariant','INV','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`,
		projectID, memberID); err != nil {
		t.Fatal(err)
	}
	safeIssue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Safe v0','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	safeIssueID, _ := safeIssue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',0)`, safeIssueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	readCursor := func(path string) string {
		t.Helper()
		response := ts.get(t, path, ts.memberCookie)
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("baseline %s status=%d body=%s", path, response.StatusCode, body)
		}
		var snapshot struct {
			Cursor string `json:"cursor"`
		}
		if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil || snapshot.Cursor == "" {
			t.Fatalf("baseline %s cursor=%q err=%v", path, snapshot.Cursor, err)
		}
		return snapshot.Cursor
	}
	globalCursor := readCursor("/api/agent-mode/deliveries")
	projectPath := "/api/agent-mode/projects/" + intString(projectID) + "/deliveries"
	projectCursor := readCursor(projectPath)

	brokenIssue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,2,'ticket','Unlinked v1','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	brokenIssueID, _ := brokenIssue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',1)`, brokenIssueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	assertUnavailable := func(name string, response *http.Response) {
		t.Helper()
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusInternalServerError ||
			response.Header.Get("Content-Type") != "application/problem+json" ||
			response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s status=%d headers=%v body=%s", name, response.StatusCode, response.Header, body)
		}
		var problem map[string]any
		if err := json.Unmarshal(body, &problem); err != nil || problem["detail"] != "Agent Mode snapshot unavailable" {
			t.Fatalf("%s problem=%s err=%v", name, body, err)
		}
	}
	paths := map[string]string{
		"global snapshot":  "/api/agent-mode/deliveries",
		"project snapshot": projectPath,
		"detail snapshot":  "/api/agent-mode/deliveries/issue:" + intString(brokenIssueID),
		"fresh stream":     "/api/agent-mode/deliveries/events",
		"query resume":     "/api/agent-mode/deliveries/events?cursor=" + globalCursor,
		"project resume": "/api/agent-mode/deliveries/events?project_id=" + intString(projectID) +
			"&cursor=" + projectCursor,
	}
	for name, path := range paths {
		assertUnavailable(name, ts.get(t, path, ts.memberCookie))
	}
	req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/agent-mode/deliveries/events?project_id="+
		intString(projectID), nil)
	req.Header.Set("Cookie", ts.memberCookie)
	req.Header.Set("Last-Event-ID", projectCursor)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertUnavailable("Last-Event-ID resume", response)
}

func TestAgentModeUnavailableAndPasswordGateAlwaysSetPrivateNoStore(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/agent-mode/deliveries/events", nil)
	handlers.AgentModeEvents(struct{ http.ResponseWriter }{recorder}, request)
	if recorder.Code != http.StatusInternalServerError ||
		recorder.Header().Get("Cache-Control") != "private, no-store" ||
		recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("non-flusher status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	ts := newTestServer(t)
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET must_change_password=1 WHERE id=?`, memberID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/agent-mode/deliveries",
		"/api/agent-mode/projects/999999/deliveries",
		"/api/agent-mode/deliveries/issue:999999",
		"/api/agent-mode/deliveries/events",
	} {
		response := ts.get(t, path, ts.memberCookie)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden || response.Header.Get("Content-Type") != "application/problem+json" ||
			response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("path=%s status=%d headers=%v body=%s", path, response.StatusCode, response.Header, body)
		}
	}
}

func TestAgentModeNoncanonicalCursorAliasesResetForQueryAndHeader(t *testing.T) {
	ts := newTestServer(t)
	project, _ := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Cursor alias','CUR','active')`)
	projectID, _ := project.LastInsertId()
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	issue, _ := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Cursor root','in-progress')`, projectID)
	issueID, _ := issue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',0)`, issueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	response := ts.get(t, "/api/agent-mode/deliveries", ts.memberCookie)
	defer response.Body.Close()
	var snapshot struct {
		Cursor string `json:"cursor"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&snapshot) != nil {
		t.Fatalf("snapshot status=%d cursor=%q", response.StatusCode, snapshot.Cursor)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(snapshot.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	alias := ""
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, final := range alphabet {
		candidate := snapshot.Cursor[:len(snapshot.Cursor)-1] + string(final)
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(candidate)
		if decodeErr == nil && candidate != snapshot.Cursor && bytes.Equal(decoded, sealed) {
			alias = candidate
			break
		}
	}
	if alias == "" {
		t.Fatal("no permissive same-ciphertext cursor alias found")
	}
	request := func(header bool) *http.Response {
		t.Helper()
		path := "/api/agent-mode/deliveries/events"
		if !header {
			path += "?cursor=" + alias
		}
		req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+path, nil)
		req.Header.Set("Cookie", ts.memberCookie)
		if header {
			req.Header.Set("Last-Event-ID", alias)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	var wantBody []byte
	for _, header := range []bool{false, true} {
		resp := request(header)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" ||
			resp.Header.Get("Cache-Control") != "private, no-store, no-transform" ||
			string(body) != "event: reset\ndata: {\"schema_version\":1,\"reason\":\"resync_required\"}\n\n" {
			t.Fatalf("header=%v status=%d headers=%v body=%q", header, resp.StatusCode, resp.Header, body)
		}
		if wantBody == nil {
			wantBody = body
		} else if !bytes.Equal(body, wantBody) {
			t.Fatalf("header/query reset bodies differ: %q vs %q", wantBody, body)
		}
	}
}

func TestAgentModeLastEventIDHasAbsolutePrecedenceOverEveryQueryCursorShape(t *testing.T) {
	ts := newTestServer(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Resume precedence','RSP','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`,
		projectID, memberID); err != nil {
		t.Fatal(err)
	}
	safeIssue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Resume baseline','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	safeIssueID, _ := safeIssue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',0)`, safeIssueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	readCursor := func(path string) string {
		t.Helper()
		response := ts.get(t, path, ts.memberCookie)
		defer response.Body.Close()
		var snapshot struct {
			Cursor string `json:"cursor"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&snapshot) != nil || snapshot.Cursor == "" {
			t.Fatalf("baseline %s status=%d cursor=%q", path, response.StatusCode, snapshot.Cursor)
		}
		return snapshot.Cursor
	}
	globalCursor := readCursor("/api/agent-mode/deliveries")
	projectCursor := readCursor("/api/agent-mode/projects/" + intString(projectID) + "/deliveries")
	sealed, err := base64.RawURLEncoding.DecodeString(globalCursor)
	if err != nil {
		t.Fatal(err)
	}
	noncanonical := ""
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, final := range alphabet {
		candidate := globalCursor[:len(globalCursor)-1] + string(final)
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(candidate)
		if decodeErr == nil && candidate != globalCursor && bytes.Equal(decoded, sealed) {
			noncanonical = candidate
			break
		}
	}
	if noncanonical == "" {
		t.Fatal("no permissive same-ciphertext cursor alias found")
	}

	// An authorized invariant makes precedence observable before stream
	// headers: a valid authoritative header reaches StreamState and produces a
	// private 500, whereas choosing any bad query cursor would produce reset.
	brokenIssue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,2,'ticket','Resume invariant','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	brokenIssueID, _ := brokenIssue.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'running',1)`, brokenIssueID, projectID, memberID); err != nil {
		t.Fatal(err)
	}

	queries := map[string]string{
		"absent":       "",
		"valid":        "cursor=" + globalCursor,
		"invalid":      "cursor=not-a-cursor",
		"empty":        "cursor=",
		"duplicate":    "cursor=" + globalCursor + "&cursor=not-a-cursor",
		"malformed":    "cursor=%ZZ",
		"semicolon":    "cursor=not-a-cursor;dropped=true",
		"noncanonical": "cursor=" + noncanonical,
		"wrong scope":  "cursor=" + projectCursor,
	}
	type headerCase struct {
		valid bool
		apply func(http.Header)
	}
	headers := map[string]headerCase{
		"valid": {valid: true, apply: func(header http.Header) {
			header.Set("Last-Event-ID", globalCursor)
		}},
		"invalid": {apply: func(header http.Header) {
			header.Set("Last-Event-ID", "not-a-cursor")
		}},
		"empty": {apply: func(header http.Header) {
			header.Set("Last-Event-ID", "")
		}},
		"duplicate": {apply: func(header http.Header) {
			header.Set("Last-Event-ID", globalCursor)
			header.Add("Last-Event-ID", "not-a-cursor")
		}},
	}
	const resetBody = "event: reset\ndata: {\"schema_version\":1,\"reason\":\"resync_required\"}\n\n"
	for headerName, headerCase := range headers {
		for queryName, rawQuery := range queries {
			t.Run(headerName+" header/"+queryName+" query", func(t *testing.T) {
				req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/agent-mode/deliveries/events", nil)
				req.URL.RawQuery = rawQuery
				req.Header.Set("Cookie", ts.memberCookie)
				headerCase.apply(req.Header)
				response, requestErr := http.DefaultClient.Do(req)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if headerCase.valid {
					if response.StatusCode != http.StatusInternalServerError ||
						response.Header.Get("Content-Type") != "application/problem+json" ||
						response.Header.Get("Cache-Control") != "private, no-store" ||
						!bytes.Contains(body, []byte("Agent Mode snapshot unavailable")) {
						t.Fatalf("valid header did not win: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
					}
					return
				}
				if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
					response.Header.Get("Cache-Control") != "private, no-store, no-transform" || string(body) != resetBody {
					t.Fatalf("bad authoritative header fell back: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
				}
			})
		}
	}
	for queryName, rawQuery := range queries {
		t.Run("absent header/"+queryName+" query", func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/agent-mode/deliveries/events", nil)
			req.URL.RawQuery = rawQuery
			req.Header.Set("Cookie", ts.memberCookie)
			response, requestErr := http.DefaultClient.Do(req)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if queryName == "absent" || queryName == "valid" {
				if response.StatusCode != http.StatusInternalServerError ||
					response.Header.Get("Content-Type") != "application/problem+json" ||
					response.Header.Get("Cache-Control") != "private, no-store" ||
					!bytes.Contains(body, []byte("Agent Mode snapshot unavailable")) {
					t.Fatalf("query-only valid open status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
				}
				return
			}
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
				response.Header.Get("Cache-Control") != "private, no-store, no-transform" || string(body) != resetBody {
				t.Fatalf("query-only invalid cursor status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
			}
		})
	}

	for name, rawQuery := range map[string]string{
		"malformed non-cursor":  "state=active;dropped=true",
		"bad escape non-cursor": "q=%ZZ",
	} {
		t.Run(name+" remains invalid", func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/agent-mode/deliveries/events", nil)
			req.URL.RawQuery = rawQuery
			req.Header.Set("Cookie", ts.memberCookie)
			req.Header.Set("Last-Event-ID", globalCursor)
			response, requestErr := http.DefaultClient.Do(req)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadRequest || response.Header.Get("Content-Type") != "application/problem+json" ||
				response.Header.Get("Cache-Control") != "private, no-store" || !bytes.Contains(body, []byte("invalid Agent Mode request")) {
				t.Fatalf("status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
			}
		})
	}
}

func TestAgentModeDetailScopePrecedesScaleLimitAndOversizeSnapshotsAreExplicit(t *testing.T) {
	ts := newTestServer(t)
	project, err := db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('HTTP detail scale','HDS','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'none')`,
		projectID, memberID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var lateIssueID int64
	// The historical bug filtered detail after LIMIT 1001, so exercise the
	// 1002nd root rather than a root that the buggy prefix still contained.
	for number := 1; number <= 1002; number++ {
		issue, insertErr := tx.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
			VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, "HTTP detail scale "+intString(int64(number)))
		if insertErr != nil {
			_ = tx.Rollback()
			t.Fatal(insertErr)
		}
		lateIssueID, _ = issue.LastInsertId()
		if _, insertErr = tx.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
			delivery_instrumentation_version) VALUES(?,?,?,'running',0)`, lateIssueID, projectID, memberID); insertErr != nil {
			_ = tx.Rollback()
			t.Fatal(insertErr)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	detailKey := "issue:" + intString(lateIssueID)
	response := ts.get(t, "/api/agent-mode/deliveries/"+detailKey, ts.adminCookie)
	defer response.Body.Close()
	var snapshot struct {
		Rows []struct {
			DeliveryID string `json:"delivery_id"`
		} `json:"rows"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&snapshot) != nil ||
		len(snapshot.Rows) != 1 || snapshot.Rows[0].DeliveryID != detailKey {
		t.Fatalf("late detail status=%d rows=%+v", response.StatusCode, snapshot.Rows)
	}

	for _, path := range []string{
		"/api/agent-mode/deliveries",
		"/api/agent-mode/projects/" + intString(projectID) + "/deliveries",
	} {
		response := ts.get(t, path, ts.adminCookie)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || response.Header.Get("Content-Type") != "application/problem+json" ||
			response.Header.Get("Cache-Control") != "private, no-store" || !bytes.Contains(body, []byte("invalid Agent Mode request")) {
			t.Fatalf("oversize %s status=%d headers=%v body=%s", path, response.StatusCode, response.Header, body)
		}
	}

	var canonicalNotFound []byte
	for name, request := range map[string]struct {
		path   string
		cookie string
	}{
		"inaccessible": {path: "/api/agent-mode/deliveries/" + detailKey, cookie: ts.memberCookie},
		"missing":      {path: "/api/agent-mode/deliveries/issue:999999999", cookie: ts.adminCookie},
	} {
		response := ts.get(t, request.path, request.cookie)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound || response.Header.Get("Content-Type") != "application/problem+json" ||
			response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s status=%d headers=%v body=%s", name, response.StatusCode, response.Header, body)
		}
		var problem map[string]any
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatal(err)
		}
		delete(problem, "instance")
		delete(problem, "request_id")
		normalized, _ := json.Marshal(problem)
		if canonicalNotFound == nil {
			canonicalNotFound = normalized
		} else if !bytes.Equal(normalized, canonicalNotFound) {
			t.Fatalf("inaccessible/missing detail bodies differ: %s vs %s", canonicalNotFound, normalized)
		}
	}
}

func intString(value int64) string {
	if value == 0 {
		return "0"
	}
	result := ""
	for value > 0 {
		result = string(rune('0'+value%10)) + result
		value /= 10
	}
	return result
}
