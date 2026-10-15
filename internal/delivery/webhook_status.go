// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
)

// Only parsed scheduling hints and fixed classes survive the transport. The
// response, headers, request URL, token and body are never retained here.
type webhookGitHubError struct {
	cause      error
	class      string
	retryAfter time.Duration
	reset      time.Time
}

func (e *webhookGitHubError) Error() string { return e.class }
func (e *webhookGitHubError) Unwrap() error { return e.cause }

func githubStatusClass(status int) string {
	switch status {
	case 401:
		return "http_401"
	case 403:
		return "http_403"
	case 404:
		return "http_404"
	case 429:
		return "http_429"
	default:
		if status >= 500 && status <= 599 {
			return "http_5xx"
		}
		return "github_http"
	}
}

func webhookPermission(class string) bool { return class == "http_401" || class == "http_403" }
func webhookLimited(class string) bool    { return class == "http_429" || class == "secondary_limit" }

// The existing reader keeps authority, token lifetime and response bounds.
// A private client copy observes just this read, including installation minting;
// it never mutates the shared App/client or widens its permissions.
func (g AppReader) readInstallation(ctx context.Context, read func(func(string, any) error, func(int64, string) (bool, string, error)) error) error {
	return g.observeGitHub(func(app *crossreview.GitHubApp) error {
		return app.ReadInstallation(ctx, read)
	})
}

func (g AppReader) observeGitHub(read func(*crossreview.GitHubApp) error) error {
	app := *g.App
	client := http.Client{}
	if app.Client != nil {
		client = *app.Client
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	observer := &webhookStatusTransport{base: transport}
	client.Transport, app.Client = observer, &client
	err := read(&app)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	var response *crossreview.GitHubHTTPError
	if err != nil && observer.failure != nil && (errors.As(err, &response) && response.StatusCode == observer.status ||
		observer.status == 0 && errors.Is(err, crossreview.ErrUnavailable)) {
		failure := *observer.failure
		failure.cause = err
		return &failure
	}
	return err
}

type webhookStatusTransport struct {
	base    http.RoundTripper
	mu      sync.Mutex
	status  int
	failure *webhookGitHubError
}

func (t *webhookStatusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	// Best-effort token revocation must not replace the failed read's hint.
	if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
		return response, err
	}
	var failure *webhookGitHubError
	status := 0
	if err != nil {
		failure = &webhookGitHubError{class: "network"}
	} else if response != nil && (response.StatusCode < 200 || response.StatusCode >= 300) {
		status = response.StatusCode
		failure = &webhookGitHubError{class: githubStatusClass(status)}
		retry := response.Header.Get("Retry-After")
		if status == http.StatusForbidden && (retry != "" || response.Header.Get("X-RateLimit-Remaining") == "0") {
			failure.class = "secondary_limit"
		}
		if webhookLimited(failure.class) {
			// Bound hint parsing before conversion; only derived times survive.
			if len(retry) <= 128 {
				if seconds, err := strconv.ParseInt(retry, 10, 64); err == nil && seconds >= 0 {
					failure.retryAfter = time.Duration(min(seconds, 3600)) * time.Second
				} else if at, err := http.ParseTime(retry); err == nil {
					failure.reset = at
				}
			}
			reset := response.Header.Get("X-RateLimit-Reset")
			if len(reset) <= 20 {
				if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil && seconds > 0 && seconds <= 253402300799 {
					failure.reset = maxTime(failure.reset, time.Unix(seconds, 0))
				}
			}
		}
	}
	if failure != nil {
		t.mu.Lock()
		t.status, t.failure = status, failure
		t.mu.Unlock()
	}
	return response, err
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func webhookLimitBackoff(cause error, now time.Time) time.Duration {
	delay := time.Minute
	var upstream *webhookGitHubError
	if errors.As(cause, &upstream) {
		delay = max(delay, upstream.retryAfter, upstream.reset.Sub(now))
	}
	return min(delay, time.Hour)
}
