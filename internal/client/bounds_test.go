// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBoundedHTTPBudgets(t *testing.T) {
	c := New("http://example.test", "fixture")
	if c.HTTP == nil || c.HTTP.Timeout != httpTimeout {
		t.Fatalf("ordinary client timeout = %v", c.HTTP.Timeout)
	}
	tr, ok := c.HTTP.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil || tr.TLSHandshakeTimeout != httpTLSTimeout || tr.ResponseHeaderTimeout != httpHeaderTimeout || tr.ExpectContinueTimeout != httpContinueTimeout {
		t.Fatal("ordinary client is missing a dial, TLS, or header bound")
	}
	stream := StreamHTTP()
	if stream.Timeout != 0 {
		t.Fatalf("stream client timeout = %v", stream.Timeout)
	}
	st, ok := stream.Transport.(*http.Transport)
	if !ok || st.DialContext == nil || st.TLSHandshakeTimeout != httpTLSTimeout || st.ResponseHeaderTimeout != streamHeaderTimeout {
		t.Fatal("stream client is missing a dial, TLS, or header bound")
	}
	forwarded := New("http://example.test", "fixture").ForStream()
	if forwarded.HTTP.Timeout != 0 {
		t.Fatal("stream clone kept an overall deadline")
	}
	if c.HTTP.Timeout != httpTimeout {
		t.Fatal("stream clone changed the original client")
	}
	ft, ok := forwarded.HTTP.Transport.(*http.Transport)
	if !ok || ft.ResponseHeaderTimeout != streamHeaderTimeout {
		t.Fatal("stream clone lost its header bound")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestForStreamKeepsCallerTransport(t *testing.T) {
	called := false
	c := New("http://example.test", "fixture")
	c.HTTP = &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}
	stream := c.ForStream()
	if err := stream.Do(context.Background(), http.MethodGet, "/api/me", nil, &struct{}{}); err != nil || !called {
		t.Fatalf("caller transport err=%v called=%v", err, called)
	}
	if stream.HTTP.Timeout != 0 || c.HTTP.Timeout != time.Second {
		t.Fatal("stream clone mutated the caller or kept an overall deadline")
	}
}

func TestStalledRequestEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := New(srv.URL, "fixture")
	c.HTTP.Timeout = 200 * time.Millisecond
	tr := c.HTTP.Transport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 200 * time.Millisecond
	c.HTTP.Transport = tr
	if err := c.Do(context.Background(), http.MethodGet, "/api/me", nil, nil); err == nil {
		t.Fatal("stalled request succeeded")
	}
}

func TestStalledDownloadBodyEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	hc := BoundedHTTP()
	hc.Timeout = 200 * time.Millisecond
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/attachments/x/content", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		res, err := hc.Do(req)
		if err != nil {
			done <- err
			return
		}
		_, err = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled download succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled download hung")
	}
}

func TestStreamHeaderStallEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	hc := StreamHTTP()
	tr := hc.Transport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 150 * time.Millisecond
	hc.Transport = tr
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		res, err := hc.Do(req)
		if res != nil {
			_ = res.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled stream headers succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled stream headers hung")
	}
}

func TestStreamBodyStaysOpenUntilCancel(t *testing.T) {
	headers := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(headers)
		<-r.Context().Done()
	}))
	defer srv.Close()
	hc := StreamHTTP()
	tr := hc.Transport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 80 * time.Millisecond
	hc.Transport = tr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		res, err := hc.Do(req)
		if err != nil {
			done <- err
			return
		}
		_, err = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		done <- err
	}()
	select {
	case <-headers:
	case <-time.After(2 * time.Second):
		t.Fatal("stream headers never arrived")
	}
	select {
	case err := <-done:
		t.Fatalf("stream body ended before cancel: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled stream succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled stream hung")
	}
}
