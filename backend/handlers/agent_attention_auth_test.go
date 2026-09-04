// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/models"
)

func TestAttentionPortfolioRoutesRejectSpoofedAgentFromRegularAdmin(t *testing.T) {
	router := chi.NewRouter()
	RegisterAgentMessageRoutes(router)
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/projects/1/attention/listen?as=codex:amy", ""},
		{http.MethodPost, "/projects/1/attention/ack", `{"to":"codex:amy","cursor":1}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Paimos-Agent-Name", "amy")
		req = req.WithContext(context.WithValue(req.Context(), auth.UserKey, &models.User{ID: 1, Role: auth.RoleAdmin}))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%q", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
}
