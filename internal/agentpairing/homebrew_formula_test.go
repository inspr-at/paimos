// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/version"
)

func formulaNixGuide() *config.PairingNixGuide {
	return &config.PairingNixGuide{
		ModuleURL: "https://example.test/instance/module.nix", ServiceOption: "services.aeon.enable",
		Platforms: []string{"darwin"}, ServiceNote: "This module needs a paired-service update before this computer can connect.",
	}
}

func TestHomebrewFormulaURL(t *testing.T) {
	if HomebrewFormulaURL != "https://raw.githubusercontent.com/inspr-at/homebrew-tap/main/Formula/aeon-agentd.rb" {
		t.Fatal(HomebrewFormulaURL)
	}
}

func TestFormulaVersionLine(t *testing.T) {
	body := "# comment version \"nope\"\n  version \"260929120000.0.0\"\n  url \"https://example.test/v260929120000.0.0/file\"\n"
	got, ok := formulaVersion(body)
	if !ok || got != "260929120000.0.0" {
		t.Fatalf("version %q %v", got, ok)
	}
	for _, bad := range []string{"", "version \"\"\n", "version \"has space\"\n", "version \"one\"\nversion \"two\"\n", "url \"version \\\"hidden\\\"\"\n"} {
		if _, ok := formulaVersion(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestHomebrewFormulaMatchMismatchAndFailure(t *testing.T) {
	const serverVersion = "260929120000.0.0"
	var status atomic.Int32
	var body atomic.Value
	status.Store(http.StatusOK)
	body.Store("  version \"" + serverVersion + "\"\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "aeon-homebrew-formula" {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	t.Cleanup(srv.Close)
	formula := NewHomebrewFormula(HomebrewFormulaConfig{
		URL: srv.URL, Client: srv.Client(), Timeout: time.Second,
		Version: func() string { return serverVersion },
	})
	formula.Refresh(context.Background())
	if current, known := formula.Current(); !known || !current {
		t.Fatalf("match known=%v current=%v", known, current)
	}
	body.Store("  version \"260101120000.0.0\"\n")
	formula.Refresh(context.Background())
	if current, known := formula.Current(); !known || current {
		t.Fatalf("mismatch known=%v current=%v", known, current)
	}
	status.Store(http.StatusNotFound)
	formula.Refresh(context.Background())
	if _, known := formula.Current(); known {
		t.Fatal("failed read kept a previous result")
	}
	status.Store(http.StatusOK)
	body.Store(strings.Repeat("x", formulaBodyMax+1))
	formula.Refresh(context.Background())
	if _, known := formula.Current(); known {
		t.Fatal("truncated formula counted as a version")
	}
	body.Store("no version line\n")
	formula.Refresh(context.Background())
	if _, known := formula.Current(); known {
		t.Fatal("unparsed formula counted as absent")
	}
}

func TestHomebrewFormulaTimeoutIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	formula := NewHomebrewFormula(HomebrewFormulaConfig{
		URL: srv.URL, Client: srv.Client(), Timeout: 40 * time.Millisecond,
		Version: func() string { return "260929120000.0.0" },
	})
	start := time.Now()
	formula.Refresh(context.Background())
	if time.Since(start) > time.Second {
		t.Fatal("check did not stop at its timeout")
	}
	if _, known := formula.Current(); known {
		t.Fatal("timed-out check was treated as a result")
	}
}

func TestHomebrewFormulaWatchStops(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = io.WriteString(w, "version \"260929120000.0.0\"\n")
	}))
	t.Cleanup(srv.Close)
	formula := NewHomebrewFormula(HomebrewFormulaConfig{
		URL: srv.URL, Client: srv.Client(), Timeout: time.Second, Interval: 20 * time.Millisecond,
		Version: func() string { return "260929120000.0.0" },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		formula.Watch(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not stop")
	}
	if n.Load() < 2 {
		t.Fatalf("refreshes %d", n.Load())
	}
	if current, known := formula.Current(); !known || !current {
		t.Fatal("watch lost a matching formula")
	}
}

func TestHomebrewFormulaGatesGuide(t *testing.T) {
	old := version.Version
	version.Version = "260929120000.0.0"
	t.Cleanup(func() { version.Version = old })
	origin := "https://pairing.test"
	cases := []struct {
		name, body string
		status     int
		current    *bool
		brew       bool
		notice     string
	}{
		{name: "match", body: "version \"260929120000.0.0\"\n", status: 200, current: boolPtr(true), brew: true},
		{name: "differ", body: "version \"260101120000.0.0\"\n", status: 200, current: boolPtr(false), notice: "Homebrew formula for 260929120000.0.0 is on its way; use the direct download."},
		{name: "unread", status: 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			formula := NewHomebrewFormula(HomebrewFormulaConfig{
				URL: srv.URL, Client: srv.Client(), Timeout: time.Second,
			})
			formula.Refresh(context.Background())
			mux := http.NewServeMux()
			mod := New(nil, origin, "tenant", formulaNixGuide())
			mod.SetHomebrewFormula(formula)
			mod.Mount(mux)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/agent-pairing/guide", nil))
			var payload struct {
				Current *bool  `json:"homebrew_formula_current"`
				Brew    string `json:"homebrew_command"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil {
				t.Fatal(w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"homebrew_formula_current":`) {
				t.Fatal("formula result omitted")
			}
			if (payload.Current == nil) != (tc.current == nil) || (payload.Current != nil && *payload.Current != *tc.current) {
				t.Fatalf("current %#v", payload.Current)
			}
			if tc.brew {
				if !strings.Contains(payload.Brew, `env "$(brew --prefix)/bin/aeon-agentd" pair --url 'https://pairing.test'`) {
					t.Fatalf("brew command %q", payload.Brew)
				}
			} else if payload.Brew != "" || strings.Contains(w.Body.String(), "brew install") {
				t.Fatal("unpublished formula still offered Homebrew")
			}
			html := httptest.NewRecorder()
			GuidePageWithFormula(http.NotFoundHandler(), nil, origin, formula, formulaNixGuide()).ServeHTTP(html, httptest.NewRequest(http.MethodGet, "/agents/register-agent", nil))
			page := html.Body.String()
			if tc.brew {
				if !strings.Contains(page, "brew install inspr-at/tap/aeon-agentd") {
					t.Fatal("html hid a matching formula")
				}
			} else if strings.Contains(page, "brew install") {
				t.Fatal("html offered Homebrew")
			}
			if tc.notice != "" && !strings.Contains(page, tc.notice) {
				t.Fatalf("html missing %s", tc.notice)
			}
			if strings.Contains(page, "Open this address on the computer") {
				t.Fatal("html still leads with the address loop")
			}
			if !strings.Contains(page, "Install on this computer") || !strings.Contains(page, "Direct download for this instance’s version") {
				t.Fatal("html lost the install path")
			}
		})
	}
}

func TestHomebrewFallbackEscapesVersion(t *testing.T) {
	old := version.Version
	version.Version = "1<2"
	t.Cleanup(func() { version.Version = old })
	formula := NewHomebrewFormula(HomebrewFormulaConfig{
		URL: "http://127.0.0.1", Version: func() string { return "9" },
	})
	formula.mu.Lock()
	formula.known = true
	formula.current = false
	formula.mu.Unlock()
	page := renderGuideHTML("https://pairing.test", formula, nil)
	if strings.Contains(page, "1<2") || !strings.Contains(page, "Homebrew formula for 1&lt;2 is on its way.") {
		t.Fatal("version was not escaped, or a missing installer was called a download")
	}
}

func boolPtr(v bool) *bool { return &v }
