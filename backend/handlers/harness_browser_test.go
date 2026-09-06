package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
)

func TestHarnessBrowserStrictWire(t *testing.T) {
	router := chi.NewRouter()
	RegisterHarnessBrowserRoutes(router)
	p, _ := auth.NewSessionPrincipal(uuid.NewString(), 1, 1, false)
	for _, body := range []string{`{"expected_revision":1,"request_key":"x","shell":"do"}`, `{"expected_revision":1,"expected_revision":2}`, `{"expected_revision":1.5}`, `{} {}`, strings.Repeat("x", 1025)} {
		req := httptest.NewRequest("POST", "/projects/1/harness-sessions/fixture/controls/v1/stop", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"harness_browser_invalid"}` {
			t.Fatalf("strict body status=%d", w.Code)
		}
	}
	for _, query := range []string{"limit=101", "limit=1&limit=2", "after_revision=-1", "unknown=1", "limit=1.5"} {
		req := httptest.NewRequest("GET", "/projects/1/harness-sessions/fixture/assignment-history/v1?"+query, nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("history query not closed")
		}
	}
	for _, body := range []string{
		`{"schema_version":1,"utterance_id":"utt_0123456789abcdef0123456789abcdef","expected_revision":1,"text":"hello","delivery_level":"simple","agent_name":"sender"}`,
		`{"schema_version":1,"utterance_id":"utt_0123456789abcdef0123456789abcdef","expected_revision":1,"text":"hello","delivery_level":"simple"} {}`,
		strings.Repeat("x", 64*1024+1),
	} {
		req := httptest.NewRequest("POST", "/projects/1/harness-sessions/00000000-0000-4000-8000-000000000001/messages/v1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"harness_browser_invalid"}` || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("strict message body status=%d body=%q", w.Code, w.Body.String())
		}
	}
	request := httptest.NewRequest("POST", "/projects/1/harness-sessions/00000000-0000-4000-8000-000000000001/messages/v1", strings.NewReader(`{"schema_version":1,"utterance_id":"utt_0123456789abcdef0123456789abcdef","expected_revision":1,"text":"hello","delivery_level":"simple"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Paimos-Agent-Name", "impersonated")
	request = request.WithContext(auth.WithPrincipal(request.Context(), p))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"harness_browser_invalid"}` {
		t.Fatalf("agent attribution header accepted: status=%d body=%q", w.Code, w.Body.String())
	}
}
