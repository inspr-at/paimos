// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type lifecycleDeadlineWriter struct {
	http.ResponseWriter
	requested chan<- time.Time
	expire    bool
}

func (w *lifecycleDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.requested <- deadline
	if w.expire {
		// Expire the actual socket without a sleep or a performance assertion.
		return http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
	}
	return nil
}

func lifecycleBodyHandler() (http.Handler, tenant.Principal, string) {
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", TenantID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}
	// No database: failed body reads must finish before transaction admission.
	s := &httpapi.Server{Modules: []httpapi.Module{New(nil, nil)}, Middleware: []func(http.Handler) http.Handler{events.PositionMiddleware(nil)}}
	return s.Handler(), p, "/api/nodes/33333333-3333-4333-8333-333333333333/work-lifecycle"
}

func TestWorkLifecycleBodyNetworkDeadline(t *testing.T) {
	handler, p, path := lifecycleBodyHandler()
	requested := make(chan time.Time, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&lifecycleDeadlineWriter{w, requested, true}, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The body never arrives. The client deadline only guards a hung test;
	// only a real server deadline can produce the required timeout response.
	if _, err := io.WriteString(conn, "POST "+path+" HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled lifecycle body did not receive a timeout response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestTimeout || strings.TrimSpace(string(body)) != `{"error":"request body read timed out"}` {
		t.Fatalf("timeout response=%d %s", response.StatusCode, body)
	}
	select {
	case deadline := <-requested:
		if deadline.IsZero() {
			t.Fatal("body read did not install a deadline")
		}
	default:
		t.Fatal("body read did not reach the network controller through production middleware")
	}
	select {
	case <-requested:
		t.Fatal("failed read cleared its drain deadline")
	default:
	}
}

func TestWorkLifecycleBodyDeadlineClearedOnlyAfterCompleteRead(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		clear      bool
	}{
		{"complete but invalid action", `{}`, true},
		{"oversized", strings.Repeat(" ", (1<<20)+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, p, path := lifecycleBodyHandler()
			requested := make(chan time.Time, 4)
			recorder := httptest.NewRecorder()
			r := httptest.NewRequest("POST", path, strings.NewReader(tc.body))
			handler.ServeHTTP(&lifecycleDeadlineWriter{recorder, requested, false}, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("invalid body status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			select {
			case deadline := <-requested:
				if deadline.IsZero() {
					t.Fatal("body read did not set a deadline")
				}
			default:
				t.Fatal("body read had no network deadline")
			}
			if tc.clear {
				select {
				case deadline := <-requested:
					if !deadline.IsZero() {
						t.Fatal("completed read retained a deadline")
					}
				default:
					t.Fatal("completed read did not clear its deadline")
				}
			}
			if len(requested) != 0 {
				t.Fatal("unexpected deadline update after read")
			}
		})
	}
}
