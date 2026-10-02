// SPDX-License-Identifier: AGPL-3.0-only
package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/stepup"
)

const testChallengeID = "11111111-1111-4111-8111-111111111111"

// ASN.1 (r=1,s=1), a synthetic protocol fixture, never a real credential.
const testSignature = "MAYCAQECAQE="

func writeChallenge(w http.ResponseWriter) {
	w.WriteHeader(http.StatusPreconditionRequired)
	_ = json.NewEncoder(w).Encode(stepup.Required{Code: "step_up_required", ChallengeID: testChallengeID, ExpiresAt: time.Now().Add(time.Minute)})
}

func TestStepUpRetriesExactRequestOnce(t *testing.T) {
	var bodies []string
	var headers []http.Header
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.RequestURI() != "/api/roles/r?revision=4" {
			t.Error("request target changed")
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		headers = append(headers, r.Header.Clone())
		if len(bodies) == 1 {
			writeChallenge(w)
			return
		}
		if r.Header.Get(stepup.Header) != testChallengeID+"."+testSignature {
			t.Error("proof header mismatch")
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "synthetic-bearer")
	body := map[string]string{"name": "original"}
	extra := map[string]string{"Idempotency-Key": "synthetic-idempotency", "X-Aeon-Agent-Name": "test-agent"}
	c.ConfirmStepUp = func(ctx context.Context, id string) (string, error) {
		calls++
		if id != testChallengeID {
			t.Error("wrong challenge")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("unbounded confirmation")
		}
		body["name"] = "changed after first request"
		extra["Idempotency-Key"] = "changed"
		return testSignature, nil
	}
	var dest map[string]bool
	if err := c.DoWithHeaders(t.Context(), "PATCH", "/api/roles/r?revision=4", body, &dest, extra); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(bodies) != 2 || bodies[0] != bodies[1] || !dest["ok"] {
		t.Fatal("retry changed request or result")
	}
	headers[1].Del(stepup.Header)
	if !reflect.DeepEqual(headers[0], headers[1]) {
		t.Fatal("retry changed original headers")
	}
}

func TestStepUpFailuresNeverRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		confirm func(context.Context, string) (string, error)
	}{
		{"missing daemon", nil},
		{"deny", func(context.Context, string) (string, error) { return "", errors.New("denied") }},
		{"timeout", func(context.Context, string) (string, error) { return "", context.DeadlineExceeded }},
		{"empty proof", func(context.Context, string) (string, error) { return "", nil }},
		{"header injection", func(context.Context, string) (string, error) { return "proof\r\nX: injected", nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; writeChallenge(w) }))
			defer srv.Close()
			c := New(srv.URL, "synthetic")
			c.ConfirmStepUp = tc.confirm
			if c.Do(t.Context(), "POST", "/api/keys", nil, nil) == nil || n != 1 {
				t.Fatal("failed confirmation retried")
			}
		})
	}
}

func TestStepUpCancellationAfterApprovalNeverRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; writeChallenge(w) }))
	defer srv.Close()
	c := New(srv.URL, "synthetic")
	c.ConfirmStepUp = func(context.Context, string) (string, error) { cancel(); return testSignature, nil }
	if c.Do(ctx, "POST", "/api/keys", nil, nil) == nil || n != 1 {
		t.Fatal("cancelled operation retried")
	}
}

func TestStepUpSecondChallengeDoesNotPromptAgain(t *testing.T) {
	n, prompts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; writeChallenge(w) }))
	defer srv.Close()
	c := New(srv.URL, "synthetic")
	c.ConfirmStepUp = func(context.Context, string) (string, error) { prompts++; return testSignature, nil }
	if c.Do(t.Context(), "POST", "/api/keys", nil, nil) == nil || n != 2 || prompts != 1 {
		t.Fatal("retry loop or silent success")
	}
}

func TestStepUpRefusesMalformedExpiredAndOversizedChallenge(t *testing.T) {
	for _, payload := range []string{
		`{"code":"step_up_required","challenge_id":"../x","expires_at":"2099-01-01T00:00:00Z"}`,
		`{"code":"step_up_required","challenge_id":"` + testChallengeID + `","expires_at":"2000-01-01T00:00:00Z"}`,
		`{"code":"step_up_required"}`,
		strings.Repeat("x", 4097),
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(428); _, _ = io.WriteString(w, payload) }))
		c := New(srv.URL, "synthetic")
		c.ConfirmStepUp = func(context.Context, string) (string, error) {
			t.Error("invalid challenge prompted")
			return testSignature, nil
		}
		if c.Do(t.Context(), "POST", "/api/keys", nil, nil) == nil {
			t.Error("invalid challenge succeeded")
		}
		srv.Close()
	}
}

func TestStepUpProofNeverFollowsRedirect(t *testing.T) {
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			writeChallenge(w)
			return
		}
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := New(srv.URL, "synthetic")
	c.ConfirmStepUp = func(context.Context, string) (string, error) { return testSignature, nil }
	if c.Do(t.Context(), "POST", "/api/keys", nil, nil) == nil || leaked || n != 2 {
		t.Fatal("proof followed redirect")
	}
}

func TestOrdinaryResponseNeverAsksAgentd(t *testing.T) {
	for _, status := range []int{200, 403, 428} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"ordinary"}`)
		}))
		c := New(srv.URL, "synthetic")
		c.ConfirmStepUp = func(context.Context, string) (string, error) { t.Error("ordinary response prompted"); return "", nil }
		err := c.Do(t.Context(), "GET", "/api/me", nil, nil)
		if status != 200 {
			var e *StatusError
			if !errors.As(err, &e) || e.Status != status {
				t.Error("ordinary status changed")
			}
		} else if err != nil {
			t.Error(err)
		}
		srv.Close()
	}
}
