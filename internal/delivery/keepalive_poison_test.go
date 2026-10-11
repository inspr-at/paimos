// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
)

// AEON-1228 risk: a webhook body that arrives after the handler's 5 s read
// deadline makes net/http cancel the CONNECTION context (connReader.
// handleReadErrorLocked). The handler answers 413, net/http then drains the
// late body and keeps the keep-alive connection open. Every later request that
// a reverse proxy sends over that upstream connection starts with an already
// cancelled context, so its first database call fails at pool acquisition in
// 0 ms without reaching Postgres (the 2026-10-11 ppm outage).
//
// The connection must either be closed after the failed read or serve the next
// request with a live context. The barrier is the handler's return; the only
// wait is the production read deadline itself.
func TestWebhookBodyTimeoutDoesNotPoisonKeepAliveConnection(t *testing.T) {
	m := New(nil, crossreview.AppConfig{}, nil, &mergeGitHubFixture{fakeGitHub: &fakeGitHub{}}, nil)
	routes := http.NewServeMux()
	m.Mount(routes)
	routes.HandleFunc("GET /probe", func(w http.ResponseWriter, r *http.Request) {
		if err := r.Context().Err(); err != nil {
			http.Error(w, "context already cancelled at entry: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "live")
	})
	returned := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r)
		if r.URL.Path == "/api/github/webhook" {
			close(returned)
		}
	}))
	// The production server settings (cmd/aeon serve.go).
	server.Config.ReadHeaderTimeout = 10 * time.Second
	server.Config.IdleTimeout = time.Minute
	server.Start()
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	responses := bufio.NewReader(conn)
	body := `{"zen":"` + strings.Repeat("x", 64) + `"}`
	head := "POST /api/github/webhook HTTP/1.1\r\nHost: aeon.test\r\nContent-Type: application/json\r\nX-GitHub-Event: ping\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	if _, err := io.WriteString(conn, head+body[:len(body)/2]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned: // the read deadline fired and the handler answered
	case <-time.After(30 * time.Second):
		t.Fatal("webhook handler did not return; hang guard")
	}
	if _, err := io.WriteString(conn, body[len(body)/2:]); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second)) // hang guard only
	first, err := http.ReadResponse(responses, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, first.Body)
	_ = first.Body.Close()
	if first.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("late webhook body: status %d, want 413 from the body read deadline", first.StatusCode)
	}

	// A reverse proxy reuses the upstream connection unless it was closed.
	if _, err := io.WriteString(conn, "GET /probe HTTP/1.1\r\nHost: aeon.test\r\n\r\n"); err != nil {
		return // closed: safe
	}
	next, err := http.ReadResponse(responses, nil)
	if err != nil {
		return // closed: safe
	}
	got, _ := io.ReadAll(next.Body)
	_ = next.Body.Close()
	if next.StatusCode != http.StatusOK {
		t.Fatalf("next request on the reused keep-alive connection: %d %q (first response Connection=%q)", next.StatusCode, strings.TrimSpace(string(got)), first.Header.Get("Connection"))
	}
}
