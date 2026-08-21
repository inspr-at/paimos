package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

type exactOnceComment struct {
	ID              int64   `json:"id"`
	IssueID         int64   `json:"issue_id"`
	Body            string  `json:"body"`
	Visibility      string  `json:"visibility"`
	ClientRequestID *string `json:"client_request_id"`
}

func postCommentRequest(t *testing.T, ts *testServer, issueID int64, cookie string, body map[string]any, idempotencyKey string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/issues/%d/comments", ts.srv.URL, issueID), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post comment: %v", err)
	}
	return resp
}

func TestCommentClientRequestIDExactReplayReturnsOriginalWithoutCachedBody(t *testing.T) {
	ts := newTestServer(t)
	_, issueID := seedVisibilityIssue(t)
	key := "voice-note:exact-replay-001"
	body := map[string]any{
		"body":              "Confirmed private voice note",
		"visibility":        "internal",
		"client_request_id": key,
	}

	first := postCommentRequest(t, ts, issueID, ts.memberCookie, body, "legacy-header-must-not-cache")
	assertStatus(t, first, http.StatusCreated)
	var original exactOnceComment
	decode(t, first, &original)
	if original.ClientRequestID == nil || *original.ClientRequestID != key || original.Visibility != "internal" {
		t.Fatalf("created comment=%+v", original)
	}

	second := postCommentRequest(t, ts, issueID, ts.memberCookie, body, "legacy-header-must-not-cache")
	assertStatus(t, second, http.StatusOK)
	if got := second.Header.Get("X-PAIMOS-Idempotency-Replay"); got != "true" {
		t.Fatalf("replay header=%q", got)
	}
	var replay exactOnceComment
	decode(t, second, &replay)
	if replay.ID != original.ID || replay.IssueID != issueID || replay.Body != original.Body || replay.ClientRequestID == nil || *replay.ClientRequestID != key {
		t.Fatalf("replay=%+v original=%+v", replay, original)
	}

	var comments, mutations, cacheRows int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE author_id=(SELECT id FROM users WHERE username='member') AND client_request_id=?`, key).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE subject_type='comment' AND subject_id=?`, original.ID).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE key='legacy-header-must-not-cache'`).Scan(&cacheRows); err != nil {
		t.Fatal(err)
	}
	if comments != 1 || mutations != 1 || cacheRows != 0 {
		t.Fatalf("comments/mutations/idempotency_keys=%d/%d/%d want 1/1/0", comments, mutations, cacheRows)
	}
}

func TestCommentClientRequestIDConflictsAcrossBodyAndIssue(t *testing.T) {
	ts := newTestServer(t)
	projectID, issueID := seedVisibilityIssue(t)
	other, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,2,'ticket','other exact-once target','backlog')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	otherIssueID, _ := other.LastInsertId()
	key := "voice-note:identity-conflict"
	first := postCommentRequest(t, ts, issueID, ts.memberCookie, map[string]any{
		"body": "original", "visibility": "internal", "client_request_id": key,
	}, "")
	assertStatus(t, first, http.StatusCreated)
	var original exactOnceComment
	decode(t, first, &original)

	for name, conflict := range map[string]struct {
		target int64
		body   string
	}{
		"body":  {issueID, "changed"},
		"issue": {otherIssueID, "original"},
	} {
		t.Run(name, func(t *testing.T) {
			resp := postCommentRequest(t, ts, conflict.target, ts.memberCookie, map[string]any{
				"body": conflict.body, "visibility": "internal", "client_request_id": key,
			}, "")
			assertStatus(t, resp, http.StatusConflict)
			var problem struct {
				Code string `json:"code"`
			}
			decode(t, resp, &problem)
			if problem.Code != "client_request_id_conflict" {
				t.Fatalf("problem code=%q", problem.Code)
			}
		})
	}

	var comments, mutations int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE client_request_id=?`, key).Scan(&comments)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE subject_type='comment' AND subject_id=?`, original.ID).Scan(&mutations)
	if comments != 1 || mutations != 1 {
		t.Fatalf("comments/mutations=%d/%d want 1/1", comments, mutations)
	}
}

func TestCommentClientRequestIDConcurrentRetriesCommitOneMutation(t *testing.T) {
	ts := newTestServer(t)
	_, issueID := seedVisibilityIssue(t)
	const attempts = 12
	key := "voice-note:concurrent-retries"
	body := map[string]any{"body": "one durable note", "visibility": "internal", "client_request_id": key}
	type result struct {
		status int
		id     int64
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			raw, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/issues/%d/comments", ts.srv.URL, issueID), bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", ts.memberCookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			var comment exactOnceComment
			if err := json.NewDecoder(resp.Body).Decode(&comment); err != nil {
				results <- result{status: resp.StatusCode, err: err}
				return
			}
			results <- result{status: resp.StatusCode, id: comment.ID}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var created, replayed int
	var originalID int64
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent retry: %v", got.err)
		}
		switch got.status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
		default:
			t.Fatalf("concurrent status=%d", got.status)
		}
		if originalID == 0 {
			originalID = got.id
		} else if got.id != originalID {
			t.Fatalf("comment ids diverged: got=%d want=%d", got.id, originalID)
		}
	}
	if created != 1 || replayed != attempts-1 {
		t.Fatalf("created/replayed=%d/%d want 1/%d", created, replayed, attempts-1)
	}
	var comments, mutations int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE client_request_id=?`, key).Scan(&comments)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE subject_type='comment' AND subject_id=?`, originalID).Scan(&mutations)
	if comments != 1 || mutations != 1 {
		t.Fatalf("comments/mutations=%d/%d want 1/1", comments, mutations)
	}
}

func TestCommentClientRequestIDCannotBecomeExternalAndSurvivesUndoRedo(t *testing.T) {
	ts := newTestServer(t)
	_, issueID := seedVisibilityIssue(t)
	key := "voice-note:internal-lifecycle"
	createdResp := postCommentRequest(t, ts, issueID, ts.memberCookie, map[string]any{
		"body": "private for its whole lifetime", "visibility": "internal", "client_request_id": key,
	}, "")
	assertStatus(t, createdResp, http.StatusCreated)
	var created exactOnceComment
	decode(t, createdResp, &created)

	flip := ts.patch(t, fmt.Sprintf("/api/comments/%d", created.ID), ts.memberCookie, map[string]any{"visibility": "external"})
	assertStatus(t, flip, http.StatusBadRequest)
	_ = flip.Body.Close()
	externalCreate := postCommentRequest(t, ts, issueID, ts.memberCookie, map[string]any{
		"body": "private for its whole lifetime", "visibility": "external", "client_request_id": key,
	}, "")
	assertStatus(t, externalCreate, http.StatusBadRequest)
	_ = externalCreate.Body.Close()

	logID := commentMutationLogID(t, created.ID, "issue.comment.create")
	undo := ts.post(t, fmt.Sprintf("/api/undo/%d", logID), ts.memberCookie, map[string]any{})
	assertStatus(t, undo, http.StatusOK)
	_ = undo.Body.Close()
	redo := ts.post(t, fmt.Sprintf("/api/redo/%d", logID), ts.memberCookie, map[string]any{})
	assertStatus(t, redo, http.StatusOK)
	_ = redo.Body.Close()

	var visibility, restoredKey string
	if err := db.DB.QueryRow(`SELECT visibility,client_request_id FROM comments WHERE id=?`, created.ID).Scan(&visibility, &restoredKey); err != nil {
		t.Fatal(err)
	}
	if visibility != "internal" || restoredKey != key {
		t.Fatalf("restored visibility/key=%q/%q", visibility, restoredKey)
	}
}

func TestCommentIdempotencyMiddlewareStillCachesOrdinaryComments(t *testing.T) {
	ts := newTestServer(t)
	_, issueID := seedVisibilityIssue(t)
	body := map[string]any{"body": "ordinary legacy retry"}
	key := "ordinary-comment-idempotency"
	first := postCommentRequest(t, ts, issueID, ts.adminCookie, body, key)
	assertStatus(t, first, http.StatusCreated)
	firstRaw, err := io.ReadAll(first.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()
	second := postCommentRequest(t, ts, issueID, ts.adminCookie, body, key)
	assertStatus(t, second, http.StatusCreated)
	if second.Header.Get("X-PAIMOS-Idempotency-Replay") != "true" {
		t.Fatal("ordinary comment did not use the legacy response cache")
	}
	secondRaw, err := io.ReadAll(second.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if !bytes.Equal(firstRaw, secondRaw) {
		t.Fatalf("cached response changed: first=%s second=%s", firstRaw, secondRaw)
	}
	var comments, cacheRows int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE body='ordinary legacy retry'`).Scan(&comments)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, key).Scan(&cacheRows)
	if comments != 1 || cacheRows != 1 {
		t.Fatalf("comments/cache=%d/%d want 1/1", comments, cacheRows)
	}
}

func TestCommentClientRequestCacheBypassMatchesJSONDecoderSemantics(t *testing.T) {
	ts := newTestServer(t)
	_, issueID := seedVisibilityIssue(t)
	postRaw := func(raw, idempotencyKey string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/issues/%d/comments", ts.srv.URL, issueID), strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", ts.memberCookie)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	caseKey := "case-variant-cache-probe"
	caseResponse := postRaw(`{"body":"case-sensitive private body","visibility":"internal","CLIENT_REQUEST_ID":"voice-case-001"}`, caseKey)
	assertStatus(t, caseResponse, http.StatusCreated)
	_ = caseResponse.Body.Close()
	trailingKey := "trailing-json-cache-probe"
	trailingResponse := postRaw(`{"body":"trailing private body","visibility":"internal","client_request_id":"voice-trailing-001"}{}`, trailingKey)
	assertStatus(t, trailingResponse, http.StatusBadRequest)
	_ = trailingResponse.Body.Close()

	var caseRows, trailingRows, trailingComments int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, caseKey).Scan(&caseRows)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, trailingKey).Scan(&trailingRows)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE client_request_id='voice-trailing-001'`).Scan(&trailingComments)
	if caseRows != 0 || trailingRows != 0 || trailingComments != 0 {
		t.Fatalf("cache case/trailing/comments=%d/%d/%d want 0/0/0", caseRows, trailingRows, trailingComments)
	}
}
