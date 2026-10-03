// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/stepup"
)

func TestStepUpFetcherUsesOwnPairingChannel(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const computer = "22222222-2222-4222-8222-222222222222"
	c := stepup.Challenge{ComputerID: computer, Nonce: strings.Repeat("a", 64), ActionDigest: strings.Repeat("b", 64), Summary: "Allow an agent to create a key for Fixture?", ExpiresAt: time.Now().Add(time.Minute).UTC()}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.RequestURI() != "/api/agentd/step-ups/"+id || r.Header.Get("Authorization") != "Bearer synthetic-runtime" || r.Header.Get("Aeon-Computer-ID") != computer || r.Header.Get("Aeon-Device-Proof") != "synthetic-lifecycle" {
			t.Error("pairing authentication mismatch")
		}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) != 0 {
			t.Error("agent content sent to challenge fetch")
		}
		_ = json.NewEncoder(w).Encode(c)
	}))
	defer srv.Close()
	fetch := stepUpFetcher(srv.Client(), srv.URL, computer, "synthetic-runtime", "synthetic-lifecycle")
	out, err := fetch(t.Context(), id)
	if err != nil || out != c || requests != 1 {
		t.Fatal("challenge did not come from paired endpoint", err)
	}
	if _, err = fetch(t.Context(), "../../elsewhere"); err == nil || requests != 1 {
		t.Fatal("unvalidated path sent")
	}
}

func TestStepUpFetcherBoundsUntrustedResponse(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 4097), `{} {}`, `not json`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		fetch := stepUpFetcher(srv.Client(), srv.URL, "computer", "synthetic", "synthetic")
		if _, err := fetch(t.Context(), "11111111-1111-4111-8111-111111111111"); err == nil {
			t.Error("invalid server response accepted")
		}
		srv.Close()
	}
}

func TestStepUpFetcherDoesNotLeakProofOnRedirect(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	hc := *srv.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if _, err := stepUpFetcher(&hc, srv.URL, "computer", "synthetic", "synthetic")(context.Background(), "11111111-1111-4111-8111-111111111111"); err == nil || leaked {
		t.Fatal("pairing proof redirected")
	}
}
