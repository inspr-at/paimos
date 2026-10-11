// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type captureFixtureAdapter struct {
	runtime       agentsetup.NativeBrowserRuntime
	starts        int
	process       *captureFixtureProcess
	beforeCapture func(BrowserCaptureStart) error
}

func (a *captureFixtureAdapter) Runtime() agentsetup.NativeBrowserRuntime { return a.runtime }
func (a *captureFixtureAdapter) StartCapture(_ context.Context, start BrowserCaptureStart) (BrowserCaptureProcess, error) {
	a.starts++
	p := &captureFixtureProcess{start: start, before: a.beforeCapture,
		identity: ownedprocess.Identity{DaemonID: start.Grant.DaemonID, Generation: start.Grant.DaemonGeneration,
			ProcessID: strings.Repeat("e", 32), RootPID: 1234, GroupID: 1234, StartedAt: a.runtime.ReportedAt}}
	a.process = p
	return p, nil
}

type captureFixtureProcess struct {
	start    BrowserCaptureStart
	identity ownedprocess.Identity
	before   func(BrowserCaptureStart) error
	finish   func() error
	finishes int
}

func (p *captureFixtureProcess) Ownership() (ownedprocess.Identity, error) { return p.identity, nil }
func (p *captureFixtureProcess) Capture(ctx context.Context, target string) (io.ReadCloser, error) {
	info, err := os.Stat(p.start.ProfileDirectory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("nonprivate fixture profile")
	}
	entries, err := os.ReadDir(p.start.ProfileDirectory)
	if err != nil || len(entries) != 0 {
		return nil, errors.New("fixture profile was not empty")
	}
	if p.before != nil {
		if err := p.before(p.start); err != nil {
			return nil, err
		}
	}
	source, err := p.start.Read(ctx, target, true)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(source.Body, []byte("synthetic fixture")) {
		return nil, errors.New("fixture content missing")
	}
	frame := image.NewRGBA(image.Rect(0, 0, p.start.Width, p.start.Height))
	frame.SetRGBA(0, 0, color.RGBA{R: 23, G: 42, B: 65, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, frame); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
func (p *captureFixtureProcess) Finish(_ context.Context, identity ownedprocess.Identity) error {
	if identity != p.identity {
		return errors.New("foreign process cleanup")
	}
	p.finishes++
	if p.finish != nil {
		return p.finish()
	}
	return nil
}

type captureFixtureStore struct {
	digests   map[string]string
	receipts  map[string]BrowserCaptureReceipt
	images    map[string][]byte
	commitErr error
}

func (s *captureFixtureStore) Reserve(_ context.Context, _ agentsetup.RoutineGrant, in BrowserCaptureRequest, digest string) (*BrowserCaptureReceipt, bool, error) {
	if previous, ok := s.digests[in.OutputID]; ok {
		if previous != digest {
			return nil, false, ErrReplay
		}
		if receipt, ok := s.receipts[in.OutputID]; ok {
			return &receipt, false, nil
		}
		return nil, false, nil
	}
	if len(s.digests) >= 2 {
		return nil, false, ErrBrowserCaptureLimit
	}
	s.digests[in.OutputID] = digest
	return nil, true, nil
}
func (s *captureFixtureStore) Commit(_ context.Context, _ agentsetup.RoutineGrant, r BrowserCaptureReceipt, data []byte) (BrowserCaptureReceipt, error) {
	if s.commitErr != nil {
		return BrowserCaptureReceipt{}, s.commitErr
	}
	r.AttachmentID = r.OutputID
	s.receipts[r.OutputID] = r
	s.images[r.OutputID] = bytes.Clone(data)
	return r, nil
}

func captureRuntimeFixture(g agentsetup.RoutineGrant, now time.Time) agentsetup.NativeBrowserRuntime {
	return agentsetup.NativeBrowserRuntime{TenantID: g.TenantID, OwnerID: g.OwnerID, ProjectID: g.ProjectID, AccountID: g.AccountID, ComputerID: g.ComputerID,
		DaemonID: g.DaemonID, DaemonGeneration: g.DaemonGeneration, DaemonDigest: g.DaemonDigest, CapabilityDigest: g.CapabilityDigest, HostMappingDigest: g.HostMappingDigest,
		ChromeDigest: strings.Repeat("1", 64), InterpreterDigest: strings.Repeat("2", 64), LauncherDigest: strings.Repeat("3", 64), Platform: "darwin", Adapter: Claude,
		QualifiedChromeDigest: strings.Repeat("1", 64), QualifiedInterpreterDigest: strings.Repeat("2", 64), QualifiedLauncherDigest: strings.Repeat("3", 64),
		Capabilities: []string{agentsetup.RoutineExecutionCapability, agentsetup.RoutineBrowserCapability}, ReportedAt: now,
		OwnerApproved: true, LauncherReady: true, Qualified: true, Native: true, ReadOnlyNetwork: true, EphemeralProfile: true}
}

// Risks: untrusted capture input escapes into private network/credentials,
// stale/foreign native authority starts Chrome, replay repeats an effect, or
// a failed attachment/owned exit is presented as success. Native processes and
// PNGs are synthetic here; real Chrome proof belongs to coordinator/OPS S26.
func TestNativeBrowserCaptureBrokerBoundsAndReceipts(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credential or write request reached fixture")
		}
		gets.Add(1)
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/fixture", http.StatusFound)
		case "/escape":
			http.Redirect(w, r, "http://127.0.0.2:1/private", http.StatusFound)
		case "/huge":
			w.Header().Set("Content-Length", "99999999")
			_, _ = io.WriteString(w, "x")
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<h1>synthetic fixture</h1>")
		}
	}))
	defer server.Close()
	fixture := func() (*BrowserCaptureBroker, *captureFixtureAdapter, *captureFixtureStore) {
		g := routineTestGrant("41")
		g.RuntimeCapability = agentsetup.RoutineBrowserCapability
		g.ExpiresAt = now.Add(time.Minute)
		adapter := &captureFixtureAdapter{runtime: captureRuntimeFixture(g, now)}
		store := &captureFixtureStore{digests: map[string]string{}, receipts: map[string]BrowserCaptureReceipt{}, images: map[string][]byte{}}
		broker := &BrowserCaptureBroker{Grant: g, Adapter: adapter, Store: store, Policy: BrowserCapturePolicy{FixtureOrigin: server.URL, FixtureApproved: true},
			Now: func() time.Time { return now }, Authorize: func(context.Context, agentsetup.RoutineGrant) error { return nil }}
		return broker, adapter, store
	}
	request := func(viewport string) BrowserCaptureRequest {
		id := routineTestGrant("42").LaunchID
		if viewport == "phone" {
			id = routineTestGrant("43").LaunchID
		}
		return BrowserCaptureRequest{URL: server.URL + "/redirect", Viewport: viewport, OutputID: id}
	}
	t.Run("desktop and phone retained receipts replay once", func(t *testing.T) {
		b, a, s := fixture()
		for _, viewport := range []string{"desktop", "phone"} {
			in := request(viewport)
			r, err := b.RequestCapture(t.Context(), b.Grant, in)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := png.Decode(bytes.NewReader(s.images[in.OutputID]))
			if err != nil {
				t.Fatal(err)
			}
			red, green, blue, alpha := frame.At(0, 0).RGBA()
			if red != 23*257 || green != 42*257 || blue != 65*257 || alpha != 65535 || frame.Bounds().Dx() != r.Width || frame.Bounds().Dy() != r.Height {
				t.Fatal("attachment does not retain bounded fixture capture")
			}
			if a.process.finishes != 1 || !r.ExitObserved {
				t.Fatal("success lacked exact owned exit")
			}
			if _, err := os.Stat(a.process.start.ProfileDirectory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("confirmed profile retained", err)
			}
			before := a.starts
			if replay, err := b.RequestCapture(t.Context(), b.Grant, in); err != nil || replay != r || a.starts != before {
				t.Fatal("capture repeated on replay", err)
			}
			in.URL = server.URL + "/fixture"
			if _, err := b.RequestCapture(t.Context(), b.Grant, in); !errors.Is(err, ErrReplay) || a.starts != before {
				t.Fatal("changed output identity replay accepted", err)
			}
		}
		if a.starts != 2 {
			t.Fatal("desktop/phone did not each run once")
		}
	})
	for name, mutate := range map[string]func(*BrowserCaptureBroker, *captureFixtureAdapter){
		"Codex sandbox": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) {
			a.runtime.Adapter = Codex
			a.runtime.Sandboxed = true
		},
		"unsupported host": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) { a.runtime.Platform = "linux" },
		"stale host": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) {
			a.runtime.ReportedAt = now.Add(-3 * time.Minute)
		},
		"expired grant": func(b *BrowserCaptureBroker, _ *captureFixtureAdapter) { b.Grant.ExpiresAt = now },
		"missing capability": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) {
			a.runtime.Capabilities = []string{agentsetup.RoutineExecutionCapability}
		},
		"foreign mapping": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) {
			a.runtime.ProjectID = routineTestGrant("44").LaunchID
		},
		"missing Chrome pin": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) { a.runtime.ChromeDigest = "" },
		"changed Chrome pin": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) {
			a.runtime.ChromeDigest = strings.Repeat("4", 64)
		},
		"changed interpreter pin": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) {
			a.runtime.InterpreterDigest = strings.Repeat("4", 64)
		},
		"OPS not ready": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) { a.runtime.LauncherReady = false },
		"revoked owner": func(_ *BrowserCaptureBroker, a *captureFixtureAdapter) { a.runtime.OwnerApproved = false },
	} {
		t.Run(name, func(t *testing.T) {
			b, a, s := fixture()
			mutate(b, a)
			if _, err := b.RequestCapture(t.Context(), b.Grant, request("desktop")); !errors.Is(err, agentsetup.ErrNativeBrowserUnavailable) || a.starts != 0 || len(s.digests) != 0 {
				t.Fatal("unqualified native capture reached startup", err)
			}
		})
	}
	t.Run("foreign source and cancelled request never reserve or start", func(t *testing.T) {
		b, a, s := fixture()
		source := b.Grant
		source.AgentRunID = routineTestGrant("44").AgentRunID
		if _, err := b.RequestCapture(t.Context(), source, request("desktop")); !errors.Is(err, ErrBrowserCaptureScope) || a.starts != 0 || len(s.digests) != 0 {
			t.Fatal("foreign source reached capture", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := b.RequestCapture(ctx, b.Grant, request("desktop")); !errors.Is(err, context.Canceled) || a.starts != 0 || len(s.digests) != 0 {
			t.Fatal("cancelled request reached capture", err)
		}
	})
	t.Run("private target redirect and size limits", func(t *testing.T) {
		for _, path := range []string{"/escape", "/huge"} {
			b, a, s := fixture()
			in := request("desktop")
			in.URL = server.URL + path
			want := ErrBrowserCaptureScope
			if path == "/huge" {
				want = ErrBrowserCaptureLimit
			}
			if _, err := b.RequestCapture(t.Context(), b.Grant, in); !errors.Is(err, want) || len(s.receipts) != 0 || a.process.finishes != 1 {
				t.Fatal("failed network capture reported success", err)
			}
		}
		for _, target := range []string{"http://127.0.0.2:1/", "http://169.254.169.254/", "file:///etc/passwd", "https://user:password@example.invalid/", "https://example.invalid/?token=hidden"} {
			b, a, _ := fixture()
			in := request("desktop")
			in.URL = target
			if _, err := b.RequestCapture(t.Context(), b.Grant, in); !errors.Is(err, ErrBrowserCaptureScope) || a.starts != 0 {
				t.Fatal("private/credential URL reached startup", err)
			}
		}
		for _, ip := range []string{"100.64.0.1", "192.168.0.1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::7f00:1"} {
			if publicCaptureIP(netip.MustParseAddr(ip)) {
				t.Fatal("nonpublic IP accepted", ip)
			}
		}
	})
	t.Run("attachment failure and unknown process keep intent", func(t *testing.T) {
		b, a, s := fixture()
		s.commitErr = errors.New("attachment denied")
		in := request("desktop")
		if r, err := b.RequestCapture(t.Context(), b.Grant, in); !errors.Is(err, s.commitErr) || r.AttachmentID != "" || len(s.receipts) != 0 {
			t.Fatal("failed attachment reported success", err)
		}
		if _, err := b.RequestCapture(t.Context(), b.Grant, in); !errors.Is(err, ErrBrowserCaptureUnconfirmed) || a.starts != 1 {
			t.Fatal("uncertain intent relaunched", err)
		}
		b, a, s = fixture()
		a.beforeCapture = func(BrowserCaptureStart) error {
			a.process.finish = func() error { return ErrProcessesUnconfirmed }
			return nil
		}
		if _, err := b.RequestCapture(t.Context(), b.Grant, in); !errors.Is(err, ErrBrowserCaptureUnconfirmed) || len(s.receipts) != 0 {
			t.Fatal("unknown exit reported success", err)
		}
		if _, err := os.Stat(a.process.start.ProfileDirectory); err != nil {
			t.Fatal("unknown process lost its profile", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(a.process.start.ProfileDirectory) })
	})
	t.Run("revocation after fetch prevents receipt", func(t *testing.T) {
		b, a, s := fixture()
		denied := false
		refusal := errors.New("current owner revoked")
		b.Authorize = func(context.Context, agentsetup.RoutineGrant) error {
			if denied {
				return refusal
			}
			return nil
		}
		a.beforeCapture = func(BrowserCaptureStart) error {
			a.process.finish = func() error { denied = true; return nil }
			return nil
		}
		if _, err := b.RequestCapture(t.Context(), b.Grant, request("desktop")); !errors.Is(err, refusal) || len(s.receipts) != 0 || a.process.finishes != 1 {
			t.Fatal("revoked capture attached", err)
		}
	})
	t.Run("GET gate caps pages requests and aggregate bytes", func(t *testing.T) {
		b, _, _ := fixture()
		r := newBrowserCaptureReader(t.Context(), b.Policy, b.Grant, b.Authorize)
		for i := 0; i < browserCapturePages; i++ {
			if _, err := r.read(t.Context(), server.URL+"/fixture", true); err != nil {
				t.Fatal(err)
			}
		}
		before := gets.Load()
		if _, err := r.read(t.Context(), server.URL+"/fixture", true); !errors.Is(err, ErrBrowserCaptureLimit) || gets.Load() != before {
			t.Fatal("page cap did not precede fetch", err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/fixture", nil)
		if _, err := r.client.Do(req); !errors.Is(err, ErrBrowserCaptureScope) || gets.Load() != before {
			t.Fatal("POST reached network", err)
		}
		r.requests = browserCaptureRequests
		if _, err := r.read(t.Context(), server.URL+"/fixture", false); !errors.Is(err, ErrBrowserCaptureLimit) || gets.Load() != before {
			t.Fatal("request cap did not precede fetch", err)
		}
		r.bytes = browserCaptureBytes
		if _, err := r.read(t.Context(), server.URL+"/fixture", false); !errors.Is(err, ErrBrowserCaptureLimit) || gets.Load() != before {
			t.Fatal("byte cap did not precede fetch", err)
		}
	})
	t.Run("capture-only MCP denies JS credentials and arbitrary tools", func(t *testing.T) {
		b, a, _ := fixture()
		g := b.Grant
		const credential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // gitleaks:allow synthetic test token
		host, err := startManagedTools(credential, toolBinding{routine: &g, browser: b, api: &fakeRunTools{}, workOrderID: "order", runID: g.AgentRunID, workspace: t.TempDir(), branch: "routine/" + g.LaunchID, active: func() bool { return true }})
		if err != nil {
			t.Fatal(err)
		}
		defer host.Close()
		if !host.tools.BrowserCaptureOnly {
			t.Fatal("capture-only bridge flag missing")
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "capture-test", Version: "1"}, nil)
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: host.tools.URL, HTTPClient: &http.Client{Transport: headerTransport{token: credential}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		listed, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
		if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "aeon_browser_capture" {
			t.Fatal("capture coordinator received broad tools", err)
		}
		for _, extra := range []string{"javascript", "cookies", "output_path"} {
			in := request("desktop")
			args := map[string]any{"url": in.URL, "viewport": in.Viewport, "output_id": in.OutputID, extra: "untrusted"}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "aeon_browser_capture", Arguments: args})
			if err == nil && !result.IsError || a.starts != 0 {
				t.Fatal("unknown browser argument started capture", extra, err)
			}
		}
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "aeon_browser_capture", Arguments: request("desktop")})
		if err != nil || result.IsError || a.starts != 1 {
			t.Fatal("bounded MCP fixture failed", err, result)
		}
		// The public schema stays exactly URL/viewport/output identity.
		raw, _ := json.Marshal(listed.Tools[0].InputSchema)
		for _, forbidden := range []string{"javascript", "cookie", "path", "command"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatal("broad capture input schema", forbidden)
			}
		}
	})
}
