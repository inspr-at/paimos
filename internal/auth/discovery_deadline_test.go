// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func TestOIDCDiscoveryCanceledWaiterAndRetry(t *testing.T) {
	entered := make(chan struct{})
	var requests atomic.Int32
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, issuer, issuer+"/auth", issuer+"/token", issuer+"/keys")
	}))
	defer server.Close()
	issuer = server.URL
	m := &Module{cfg: Config{OIDCIssuer: issuer, OIDCClientID: "fixture", PublicURL: issuer}}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, _, err := m.oidcProvider(ctx); first <- err }()
	<-entered
	waitCtx, stopWait := context.WithCancel(t.Context())
	waiting := make(chan struct{})
	observed := &discoveryWaitContext{Context: waitCtx, waiting: waiting}
	waiter := make(chan error, 1)
	go func() { _, _, err := m.oidcProvider(observed); waiter <- err }()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Error("discovery waiter cannot observe cancellation")
	}
	stopWait()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("canceled discovery waiter remained blocked")
	}
	cancel()
	if err := <-first; err == nil {
		t.Fatal("stalled discovery succeeded")
	}
	if _, _, err := m.oidcProvider(t.Context()); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if _, _, err := m.oidcProvider(t.Context()); err != nil || requests.Load() != 2 {
		t.Fatal("successful discovery was not cached")
	}
}

type discoveryTransport func(*http.Request) (*http.Response, error)

type discoveryWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *discoveryWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func (f discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOIDCDiscoveryHasApplicationDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var hasDeadline bool
		transport := discoveryTransport(func(r *http.Request) (*http.Response, error) {
			_, hasDeadline = r.Context().Deadline()
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		// An injected transport observes the same HTTP request as a stalled
		// issuer, while the fake clock proves the application bound without sleeps.
		parent, cancel := context.WithCancel(oidc.ClientContext(t.Context(), &http.Client{Transport: transport}))
		defer cancel()
		m := &Module{cfg: Config{OIDCIssuer: "https://issuer.invalid", OIDCClientID: "fixture", PublicURL: "https://app.invalid"}}
		done := make(chan error, 1)
		go func() { _, _, err := m.oidcProvider(parent); done <- err }()
		synctest.Wait()
		if !hasDeadline {
			cancel()
			<-done
			t.Fatal("discovery request has no application deadline")
		}
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Fatal("discovery exceeded its application budget")
		}
	})
}

func TestOIDCDiscoveryResponseBound(t *testing.T) {
	transport := discoveryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", 2<<20)))}, nil
	})
	ctx := oidc.ClientContext(t.Context(), &http.Client{Transport: transport})
	m := &Module{cfg: Config{OIDCIssuer: "https://issuer.invalid", OIDCClientID: "fixture", PublicURL: "https://app.invalid"}}
	_, _, err := m.oidcProvider(ctx)
	if err == nil || !strings.Contains(err.Error(), "request body too large") {
		t.Fatalf("oversized discovery was not bounded: %v", err)
	}
}
