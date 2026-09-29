// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/version"
)

// HomebrewFormulaURL is the raw formula on inspr-at/homebrew-tap's main branch.
// The guide compares its version line with this server and never asks the browser to fetch it.
const HomebrewFormulaURL = "https://raw.githubusercontent.com/inspr-at/homebrew-tap/main/Formula/aeon-agentd.rb"

const (
	formulaTimeoutDefault = 3 * time.Second
	formulaRefreshDefault = time.Hour
	formulaBodyMax        = 256 << 10
)

// HomebrewFormulaConfig overrides the tap check. Zero values use the production URL,
// a short timeout, and an hourly refresh. Version defaults to this server's version.
type HomebrewFormulaConfig struct {
	URL      string
	Client   *http.Client
	Timeout  time.Duration
	Interval time.Duration
	Version  func() string
}

// HomebrewFormula caches whether the published tap formula matches this server.
// A failed read is unknown, not a claim that the formula is absent or current.
type HomebrewFormula struct {
	url      string
	client   *http.Client
	timeout  time.Duration
	interval time.Duration
	version  func() string

	mu      sync.RWMutex
	known   bool
	current bool
}

// NewHomebrewFormula builds a checker. It does not contact the network.
func NewHomebrewFormula(cfg HomebrewFormulaConfig) *HomebrewFormula {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = formulaTimeoutDefault
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = formulaRefreshDefault
	}
	rawURL := cfg.URL
	if rawURL == "" {
		rawURL = HomebrewFormulaURL
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	versionOf := cfg.Version
	if versionOf == nil {
		versionOf = func() string { return version.Version }
	}
	return &HomebrewFormula{url: rawURL, client: client, timeout: timeout, interval: interval, version: versionOf}
}

// Current reports the last completed read. known is false until a read succeeds,
// and again after a read fails.
func (f *HomebrewFormula) Current() (current, known bool) {
	if f == nil {
		return false, false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.current, f.known
}

// Watch reads once, then on each interval, until ctx is cancelled.
func (f *HomebrewFormula) Watch(ctx context.Context) {
	if f == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.Refresh(ctx)
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.Refresh(ctx)
		}
	}
}

// Refresh replaces the cache. A failed read becomes unknown, including after an earlier success.
func (f *HomebrewFormula) Refresh(ctx context.Context) {
	if f == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current, known := f.fetch(ctx)
	f.mu.Lock()
	changed := f.known != known || f.current != current
	f.known = known
	f.current = current
	f.mu.Unlock()
	if changed {
		slog.Info("homebrew formula", "known", known, "current", known && current)
	}
}

func (f *HomebrewFormula) fetch(ctx context.Context) (current, known bool) {
	parsed, err := url.Parse(f.url)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "aeon-homebrew-formula")
	resp, err := f.client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return false, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, formulaBodyMax+1))
	if err != nil || len(body) > formulaBodyMax {
		return false, false
	}
	got, ok := formulaVersion(string(body))
	if !ok {
		return false, false
	}
	return got == f.version(), true
}

var formulaVersionLine = regexp.MustCompile(`(?m)^[ \t]*version[ \t]+"([^"\r\n]*)"[ \t]*$`)

// formulaVersion returns the single Homebrew `version "..."` line.
// A missing line, or more than one, cannot be compared.
func formulaVersion(body string) (string, bool) {
	matches := formulaVersionLine.FindAllStringSubmatch(body, -1)
	if len(matches) != 1 {
		return "", false
	}
	got := matches[0][1]
	if got == "" || strings.ContainsAny(got, " \t") {
		return "", false
	}
	return got, true
}
