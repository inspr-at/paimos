// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
)

// webhookStatusBodyLimit is the only slice of an error body that may be
// inspected. The bytes are discarded after the class is derived.
const webhookStatusBodyLimit = 4096

// Only parsed scheduling hints and fixed classes survive the transport. The
// response, headers, request URL, token and body are never retained here.
type webhookGitHubError struct {
	cause      error
	class      string
	retryAfter time.Duration
	reset      time.Time
	remaining  *int
	endpoint   string
	duration   time.Duration
	timedOut   bool
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
	return g.observeGitHubRead(false, func(app *crossreview.GitHubApp, _ func(error) error) error { return read(app) })
}

func (g AppReader) observeGitHubRead(history bool, read func(*crossreview.GitHubApp, func(error) error) error) error {
	app := *g.App
	client := http.Client{}
	if app.Client != nil {
		client = *app.Client
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	observer := &webhookStatusTransport{base: transport, history: history}
	client.Transport, app.Client = observer, &client
	return observer.wrap(read(&app, observer.wrap))
}

func (t *webhookStatusTransport) wrap(err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var response *crossreview.GitHubHTTPError
	var observed *webhookGitHubError
	if errors.As(err, &observed) {
		return err
	}
	if err != nil && t.failure != nil && (errors.As(err, &response) && response.StatusCode == t.status ||
		t.status == 0 && errors.Is(err, crossreview.ErrUnavailable)) {
		failure := *t.failure
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
	history bool
	budget  *webhookGitHubError
}

const historyRateReserve = 100 // leave capacity for interactive and webhook reads

func (t *webhookStatusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Best-effort token revocation must not replace the failed read's hint.
	if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
		return t.base.RoundTrip(r)
	}
	t.mu.Lock()
	if t.history && t.budget != nil {
		failure := *t.budget
		t.status, t.failure = 0, &failure
		t.mu.Unlock()
		return nil, &failure
	}
	t.mu.Unlock()
	started := time.Now()
	response, err := t.base.RoundTrip(r)
	metadata := webhookGitHubError{endpoint: historyEndpoint(r.URL.Path), duration: time.Since(started)}
	if response != nil {
		metadata.remaining, metadata.reset, metadata.retryAfter = githubRateHints(response.Header)
	}
	var failure *webhookGitHubError
	status := 0
	if err != nil {
		var network net.Error
		metadata.class = "network"
		metadata.timedOut = errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout()
		failure = &metadata
	} else if response != nil && (response.StatusCode < 200 || response.StatusCode >= 300) {
		status = response.StatusCode
		metadata.class = githubStatusClass(status)
		failure = &metadata
		retry := response.Header.Get("Retry-After")
		if status == http.StatusForbidden && (retry != "" || response.Header.Get("X-RateLimit-Remaining") == "0") {
			failure.class = "secondary_limit"
		}
		if status == http.StatusForbidden && failure.class == "http_403" {
			classifyWebhookForbiddenBody(response, failure)
		}
	}
	if t.history && err == nil && response != nil && response.StatusCode >= 200 && response.StatusCode < 300 && metadata.remaining != nil && *metadata.remaining <= historyRateReserve {
		metadata.class = "secondary_limit"
		t.mu.Lock()
		t.budget = &metadata
		t.mu.Unlock()
	}
	if failure != nil {
		t.mu.Lock()
		t.status, t.failure = status, failure
		t.mu.Unlock()
	}
	return response, err
}

func githubRateHints(headers http.Header) (*int, time.Time, time.Duration) {
	var remaining *int
	if value := headers.Get("X-RateLimit-Remaining"); len(value) > 0 && len(value) <= 10 {
		if n, err := strconv.Atoi(value); err == nil && n >= 0 {
			remaining = &n
		}
	}
	var reset time.Time
	if value := headers.Get("X-RateLimit-Reset"); len(value) > 0 && len(value) <= 20 {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= 253402300799 {
			reset = time.Unix(seconds, 0).UTC()
		}
	}
	var retry time.Duration
	if value := headers.Get("Retry-After"); len(value) > 0 && len(value) <= 128 {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			retry = time.Duration(min(seconds, 86400)) * time.Second
		} else if at, err := http.ParseTime(value); err == nil {
			reset = maxTime(reset, at)
		}
	}
	return remaining, reset, retry
}

// Endpoint families are sufficient to diagnose a failing read. Repository,
// workflow, commit and run identities, query strings and credentials stay out.
func historyEndpoint(path string) string {
	path, _, _ = strings.Cut(path, "?")
	if strings.Contains(path, "/actions/workflows/") {
		return "/actions/workflows/{workflow}/runs"
	}
	if strings.Contains(path, "/actions/runs/") {
		if strings.HasSuffix(path, "/jobs") {
			return "/actions/runs/{run}/attempts/{attempt}/jobs"
		}
		if strings.Contains(path, "/attempts/") {
			return "/actions/runs/{run}/attempts/{attempt}"
		}
		return "/actions/runs/{run}"
	}
	for _, family := range []string{"/actions/", "/check-suites", "/check-runs", "/pulls", "/commits", "/git/", "/access_tokens", "/app/installations/"} {
		if strings.Contains(path, family) {
			return family
		}
	}
	return "repository"
}

// classifyWebhookForbiddenBody upgrades a header-less 403 when GitHub's
// secondary-rate-limit marker is inside the bounded slice. A confirmed refusal
// stays http_403. The slice is not stored on the error.
func classifyWebhookForbiddenBody(response *http.Response, failure *webhookGitHubError) {
	if response == nil || response.Body == nil || failure == nil {
		return
	}
	prefix := make([]byte, 0, webhookStatusBodyLimit)
	buf := make([]byte, 512)
	var readErr error
	for len(prefix) < webhookStatusBodyLimit {
		room := webhookStatusBodyLimit - len(prefix)
		if room > len(buf) {
			room = len(buf)
		}
		n, err := response.Body.Read(buf[:room])
		if n > 0 {
			prefix = append(prefix, buf[:n]...)
		}
		if err != nil {
			readErr = err
			break
		}
		if n == 0 {
			break
		}
	}
	original := response.Body
	response.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.MultiReader(bytes.NewReader(prefix), original), Closer: original}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return
	}
	folded := bytes.ToLower(prefix)
	if bytes.Contains(folded, []byte("secondary rate limit")) || bytes.Contains(folded, []byte("secondary-rate-limit")) {
		failure.class = "secondary_limit"
	}
}

// ProbeAccess reads one repository document. Callers use it as the single
// permission-pause probe for an installation; it is not a per-receipt read.
func (g AppReader) ProbeAccess(ctx context.Context) error {
	if g.App == nil {
		return errRead
	}
	return g.observeGitHub(func(app *crossreview.GitHubApp) error {
		return app.ReadHistoryInstallation(ctx, func(get func(string, any) error, _ string) error {
			var repo struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			}
			if err := get("", &repo); err != nil {
				return err
			}
			if repo.ID <= 0 || repo.FullName != g.App.Config.Repository {
				return errRead
			}
			return nil
		})
	})
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
