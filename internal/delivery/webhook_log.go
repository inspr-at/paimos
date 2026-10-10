// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type webhookClass string

func (e webhookClass) Error() string { return string(e) }

type webhookFailureKey struct{ step, class string }
type webhookFailureWindow struct {
	at         time.Time
	suppressed uint64
}
type webhookFailureLog struct {
	mu      sync.Mutex
	windows map[webhookFailureKey]webhookFailureWindow
	logger  *slog.Logger
}

// Keys contain only fixed steps and classes, never delivery ids, error text,
// SQL messages or payload fields. The limiter therefore has bounded cardinality.
func (l *webhookFailureLog) admit(at time.Time, key webhookFailureKey) (bool, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[webhookFailureKey]webhookFailureWindow)
	}
	window, exists := l.windows[key]
	if exists && at.Sub(window.at) < time.Minute {
		window.suppressed++
		l.windows[key] = window
		return false, 0
	}
	l.windows[key] = webhookFailureWindow{at: at}
	return true, window.suppressed
}

func webhookErrorClass(err error) string {
	var class webhookClass
	var github *crossreview.GitHubHTTPError
	var postgres *pgconn.PgError
	var refusal *apiError
	var syntax *json.SyntaxError
	var field *json.UnmarshalTypeError
	switch {
	case errors.As(err, &class):
		return string(class)
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &github):
		return "github_http"
	case errors.Is(err, crossreview.ErrNotFound):
		return "github_not_found"
	case errors.Is(err, crossreview.ErrUnavailable):
		return "github_unavailable"
	case errors.Is(err, authz.ErrForbidden):
		return "forbidden"
	case errors.As(err, &postgres):
		return "database"
	case errors.Is(err, pgx.ErrNoRows):
		return "record_missing"
	case errors.Is(err, errRead):
		return "observation_unavailable"
	case errors.Is(err, errMissing):
		return "pull_missing"
	case errors.As(err, &syntax), errors.As(err, &field):
		return "invalid_json"
	case errors.As(err, &refusal):
		return "request_refused"
	default:
		return "internal"
	}
}

// Only recognized event/action names may reach logs. In particular, a rejected
// action must not become a way to put ticket text or other payload content there.
func webhookLogAction(raw []byte, event string) string {
	var e struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &e) != nil || !validAction(event, e.Action) {
		return "unknown"
	}
	return e.Action
}

func (m *Module) rejectWebhook(w http.ResponseWriter, r *http.Request, action, step string, status int, err error) {
	class := webhookErrorClass(err)
	if emit, suppressed := m.webhookFailures.admit(m.now(), webhookFailureKey{step, class}); emit {
		event, id := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
		if !validAction(event, action) {
			action = "unknown"
		}
		if !slices.Contains([]string{"pull_request", "check_run", "check_suite", "merge_group", "push", "status", "workflow_run"}, event) {
			event = "unknown"
		}
		if !deliveryID.MatchString(id) {
			id = "invalid"
		}
		attrs := []any{"event", event, "action", action, "delivery_id", id, "step", step, "status", status, "error_class", class, "suppressed", suppressed}
		var github *crossreview.GitHubHTTPError
		if errors.As(err, &github) {
			attrs = append(attrs, "github_status", github.StatusCode)
			if github.RequestID != "" {
				attrs = append(attrs, "github_request_id", github.RequestID)
			}
		}
		logger := m.webhookFailures.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.WarnContext(r.Context(), "GitHub webhook delivery failed", attrs...)
	}
	w.WriteHeader(status)
}
