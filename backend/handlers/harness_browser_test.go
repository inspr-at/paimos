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
}
