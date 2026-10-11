// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	browserCaptureTimeout  = 30 * time.Second
	browserCaptureBytes    = 8 << 20
	browserCaptureRequests = 64
	browserCapturePages    = 2
)

var (
	ErrBrowserCaptureUnavailable = errors.New("native_browser_launcher_unavailable")
	ErrBrowserCaptureScope       = errors.New("browser_capture_scope_denied")
	ErrBrowserCaptureLimit       = errors.New("browser_capture_limit")
	ErrBrowserCaptureUnconfirmed = errors.New("browser_capture_unconfirmed")
)

// The tool accepts no paths, headers, scripts, commands, credentials or browser
// flags. OutputID is an opaque receipt identity, never a caller-selected file.
type BrowserCaptureRequest struct {
	URL      string `json:"url"`
	Viewport string `json:"viewport" jsonschema:"desktop or phone"`
	OutputID string `json:"output_id"`
}

type BrowserCaptureReceipt struct {
	TenantID      string                `json:"tenant_id"`
	RunID         string                `json:"run_id"`
	OutputID      string                `json:"output_id"`
	RequestDigest string                `json:"request_digest"`
	AttachmentID  string                `json:"attachment_id"`
	SHA256        string                `json:"sha256"`
	Bytes         int                   `json:"bytes"`
	Width         int                   `json:"width"`
	Height        int                   `json:"height"`
	Process       ownedprocess.Identity `json:"process"`
	ExitObserved  bool                  `json:"exit_observed"`
}

// Reserve persists the run/output/digest intent and repeats current owner,
// consent and target checks. fresh=false with no receipt means WAIT, including
// after a crash. Commit attaches the bounded PNG and receipt atomically and
// rechecks RequireTx under the final access fence. No browser/network call may
// occur inside either transaction. Replays never infer success or launch again.
type BrowserCaptureStore interface {
	Reserve(context.Context, agentsetup.RoutineGrant, BrowserCaptureRequest, string) (receipt *BrowserCaptureReceipt, fresh bool, err error)
	Commit(context.Context, agentsetup.RoutineGrant, BrowserCaptureReceipt, []byte) (BrowserCaptureReceipt, error)
}

type BrowserCaptureSource struct {
	URL         string
	ContentType string
	Body        []byte
}

// A qualified native launcher must fence every browser/child network request
// through Read, disable script execution/service workers/extensions/downloads,
// and expose only capture. No native adapter currently implements this seam.
// OPS must qualify the exact launcher/Chrome/interpreter and owned tree exit;
// a process with general CDP, JS or direct sockets cannot qualify.
type BrowserCaptureAdapter interface {
	Runtime() agentsetup.NativeBrowserRuntime
	StartCapture(context.Context, BrowserCaptureStart) (BrowserCaptureProcess, error)
}

type BrowserCaptureStart struct {
	Grant            agentsetup.RoutineGrant
	ProfileDirectory string
	Width, Height    int
	Read             func(context.Context, string, bool) (BrowserCaptureSource, error)
}

// Finish must stop and observe only the exact owned tree, through its retained
// lifetime fence; it never looks up or kills an unrelated Chrome PID/group.
// Exit uncertainty retains the profile and intent, and prevents a success.
type BrowserCaptureProcess interface {
	Ownership() (ownedprocess.Identity, error)
	Capture(context.Context, string) (io.ReadCloser, error)
	Finish(context.Context, ownedprocess.Identity) error
}

// A coding adapter submits this job to an independently granted native broker.
// It never gets a Chrome launcher or a sandbox override. Results must be bound
// to the requesting run, even when the executor uses a separate capture run.
type BrowserCaptureRequester interface {
	RequestCapture(context.Context, agentsetup.RoutineGrant, BrowserCaptureRequest) (BrowserCaptureReceipt, error)
}

type BrowserCapturePolicy struct {
	// Exact HTTPS origins only, <=16. No suffix/wildcard matching.
	Origins []string
	// One owner/OPS-approved synthetic loopback HTTP origin, never a subnet.
	FixtureOrigin   string
	FixtureApproved bool
}

type BrowserCaptureBroker struct {
	Grant   agentsetup.RoutineGrant
	Policy  BrowserCapturePolicy
	Adapter BrowserCaptureAdapter
	Store   BrowserCaptureStore
	// Authorize performs current run/owner/qualification checks before starts
	// and each GET. The store independently rechecks the final write.
	Authorize func(context.Context, agentsetup.RoutineGrant) error
	Now       func() time.Time
}

func addBrowserCaptureTool(s *mcp.Server, b toolBinding) {
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_browser_capture", Description: "Request a bounded read-only desktop or phone capture from the qualified native browser broker. Missing launcher qualification means wait."},
		func(ctx context.Context, req *mcp.CallToolRequest, in BrowserCaptureRequest) (*mcp.CallToolResult, BrowserCaptureReceipt, error) {
			var empty BrowserCaptureReceipt
			// Explicitly reject unknown fields, including JS, cookies and paths,
			// regardless of the MCP client's schema validation behavior.
			decode := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
			decode.DisallowUnknownFields()
			if decode.Decode(&in) != nil {
				return nil, empty, ErrBrowserCaptureScope
			}
			width, height, err := captureViewport(in.Viewport)
			if err != nil || !uuidPattern.MatchString(in.OutputID) {
				return nil, empty, ErrBrowserCaptureScope
			}
			if _, err := captureURL(in.URL); err != nil {
				return nil, empty, err
			}
			if b.routine == nil || !b.active() || b.browser == nil {
				return nil, empty, ErrBrowserCaptureUnavailable
			}
			ctx, cancel := context.WithTimeout(ctx, browserCaptureTimeout)
			defer cancel()
			r, err := b.browser.RequestCapture(ctx, *b.routine, in)
			if err != nil {
				return nil, empty, err
			}
			raw, _ := json.Marshal(in)
			digest := fmt.Sprintf("%x", sha256.Sum256(raw))
			// The executor may delegate to another approved Mac. Receipt identity
			// stays bound to the requester; its actual process evidence is retained.
			if !b.active() || r.TenantID != b.routine.TenantID || r.RunID != b.runID || r.OutputID != in.OutputID || r.RequestDigest != digest ||
				!uuidPattern.MatchString(r.AttachmentID) || !agentsetup.RoutineDigest(r.SHA256) || r.Bytes < 1 || r.Bytes > browserCaptureBytes ||
				r.Width != width || r.Height != height || !r.ExitObserved || r.Process.RootPID <= 0 || r.Process.GroupID != r.Process.RootPID {
				return nil, empty, ErrBrowserCaptureUnconfirmed
			}
			return nil, r, nil
		})
}

func captureViewport(name string) (int, int, error) {
	switch name {
	case "desktop":
		return 1440, 900, nil
	case "phone":
		return 390, 844, nil
	default:
		return 0, 0, ErrBrowserCaptureScope
	}
}

func captureURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, "\\\x00\r\n\t") {
		return nil, ErrBrowserCaptureScope
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" ||
		u.Scheme != "http" && u.Scheme != "https" || strings.ToLower(u.Host) != u.Host || strings.HasSuffix(u.Hostname(), ".") || strings.Contains(u.Hostname(), "%") {
		return nil, ErrBrowserCaptureScope
	}
	return u, nil
}

func (p BrowserCapturePolicy) validate() error {
	if len(p.Origins) > 16 || len(p.Origins) == 0 && p.FixtureOrigin == "" {
		return ErrBrowserCaptureScope
	}
	seen := make(map[string]bool)
	for _, origin := range p.Origins {
		u, err := captureURL(origin)
		if err != nil || u.Scheme != "https" || u.Path != "" || u.Port() != "" || seen[origin] {
			return ErrBrowserCaptureScope
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicCaptureIP(ip) {
			return ErrBrowserCaptureScope
		}
		seen[origin] = true
	}
	if p.FixtureOrigin != "" {
		u, err := captureURL(p.FixtureOrigin)
		if err != nil || !p.FixtureApproved || u.Scheme != "http" || u.Path != "" || u.Port() == "" {
			return ErrBrowserCaptureScope
		}
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !ip.IsLoopback() {
			return ErrBrowserCaptureScope
		}
	} else if p.FixtureApproved {
		return ErrBrowserCaptureScope
	}
	return nil
}

func (p BrowserCapturePolicy) check(raw string) (*url.URL, bool, error) {
	u, err := captureURL(raw)
	if err != nil {
		return nil, false, err
	}
	origin := u.Scheme + "://" + u.Host
	if p.FixtureApproved && origin == p.FixtureOrigin {
		return u, true, nil
	}
	for _, allowed := range p.Origins {
		if origin == allowed {
			return u, false, nil
		}
	}
	return nil, false, ErrBrowserCaptureScope
}

func publicCaptureIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Special-purpose ranges are not public browser destinations, even where
	// IsGlobalUnicast includes them (carrier NAT, benchmarking, documentation).
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	return true
}

type browserCaptureReader struct {
	policy                 BrowserCapturePolicy
	grant                  agentsetup.RoutineGrant
	authorize              func(context.Context, agentsetup.RoutineGrant) error
	ctx                    context.Context
	client                 *http.Client
	mu                     sync.Mutex
	readMu                 sync.Mutex
	requests, pages, bytes int
}

func newBrowserCaptureReader(ctx context.Context, policy BrowserCapturePolicy, g agentsetup.RoutineGrant, authorize func(context.Context, agentsetup.RoutineGrant) error) *browserCaptureReader {
	r := &browserCaptureReader{ctx: ctx, policy: policy, grant: g, authorize: authorize}
	transport := &http.Transport{
		// Never inherit a proxy, cookies, authorization or account browser state.
		Proxy: nil, DisableCompression: true, DisableKeepAlives: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, ErrBrowserCaptureScope
			}
			fixture := false
			if policy.FixtureApproved {
				u, _ := url.Parse(policy.FixtureOrigin)
				fixture = address == u.Host
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 || len(ips) > 16 {
				return nil, ErrBrowserCaptureScope
			}
			for _, ip := range ips {
				if fixture && !ip.IsLoopback() || !fixture && !publicCaptureIP(ip) {
					return nil, ErrBrowserCaptureScope
				}
			}
			// Dial the checked address, not the DNS name: no second resolution
			// can rebind this GET to a private network. TLS retains the hostname.
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	r.client = &http.Client{Transport: captureRoundTripper{reader: r, next: transport}, Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 4 {
				return ErrBrowserCaptureLimit
			}
			req.Header = make(http.Header)
			_, _, err := policy.check(req.URL.String())
			return err
		}}
	return r
}

type captureRoundTripper struct {
	reader *browserCaptureReader
	next   http.RoundTripper
}

func (t captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := t.reader
	if req.Method != http.MethodGet || req.Body != nil || len(req.Header) != 0 {
		return nil, ErrBrowserCaptureScope
	}
	if _, _, err := r.policy.check(req.URL.String()); err != nil {
		return nil, err
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.authorize(req.Context(), r.grant); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.requests++
	over := r.requests > browserCaptureRequests
	r.mu.Unlock()
	if over {
		return nil, ErrBrowserCaptureLimit
	}
	return t.next.RoundTrip(req)
}

func (r *browserCaptureReader) read(ctx context.Context, raw string, page bool) (BrowserCaptureSource, error) {
	// Serialize reads so the aggregate byte limit applies before allocation.
	r.readMu.Lock()
	defer r.readMu.Unlock()
	r.mu.Lock()
	if page {
		r.pages++
	}
	if r.pages > browserCapturePages || r.bytes >= browserCaptureBytes {
		r.mu.Unlock()
		return BrowserCaptureSource{}, ErrBrowserCaptureLimit
	}
	remaining := browserCaptureBytes - r.bytes
	r.mu.Unlock()
	if _, _, err := r.policy.check(raw); err != nil {
		return BrowserCaptureSource{}, err
	}
	readCtx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(readCtx, http.MethodGet, raw, nil)
	if err != nil {
		return BrowserCaptureSource{}, ErrBrowserCaptureScope
	}
	response, err := r.client.Do(req)
	if err != nil {
		return BrowserCaptureSource{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > int64(remaining) || response.Header.Get("Content-Encoding") != "" {
		return BrowserCaptureSource{}, ErrBrowserCaptureLimit
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(remaining)+1))
	if err != nil {
		return BrowserCaptureSource{}, err
	}
	r.mu.Lock()
	r.bytes += len(body)
	over := r.bytes > browserCaptureBytes
	r.mu.Unlock()
	if over {
		return BrowserCaptureSource{}, ErrBrowserCaptureLimit
	}
	return BrowserCaptureSource{URL: response.Request.URL.String(), ContentType: response.Header.Get("Content-Type"), Body: body}, nil
}

func (b *BrowserCaptureBroker) RequestCapture(ctx context.Context, source agentsetup.RoutineGrant, in BrowserCaptureRequest) (out BrowserCaptureReceipt, resultErr error) {
	if b == nil || source.Validate() != nil {
		return out, ErrBrowserCaptureScope
	}
	sourceRaw, _ := json.Marshal(source)
	boundRaw, _ := json.Marshal(b.Grant)
	if !bytes.Equal(sourceRaw, boundRaw) {
		return out, ErrBrowserCaptureScope
	}
	return b.capture(ctx, in)
}

func (b *BrowserCaptureBroker) capture(ctx context.Context, in BrowserCaptureRequest) (out BrowserCaptureReceipt, resultErr error) {
	if b == nil || b.Adapter == nil || b.Store == nil || b.Authorize == nil || b.Now == nil {
		return out, ErrBrowserCaptureUnavailable
	}
	width, height, err := captureViewport(in.Viewport)
	if err != nil || !uuidPattern.MatchString(in.OutputID) || b.Policy.validate() != nil {
		return out, ErrBrowserCaptureScope
	}
	if _, _, err := b.Policy.check(in.URL); err != nil {
		return out, err
	}
	if err := b.Adapter.Runtime().Require(b.Grant, b.Now()); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, browserCaptureTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := b.Authorize(ctx, b.Grant); err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	previous, fresh, err := b.Store.Reserve(ctx, b.Grant, in, digest)
	if err != nil {
		return out, err
	}
	if !fresh {
		if previous == nil {
			return out, ErrBrowserCaptureUnconfirmed
		}
		if err := validBrowserReceipt(*previous, b.Grant, in, digest, width, height); err != nil {
			return out, err
		}
		return *previous, nil
	}
	if previous != nil {
		return out, ErrBrowserCaptureUnconfirmed
	}
	if err := b.Authorize(ctx, b.Grant); err != nil {
		return out, err
	}
	if err := b.Adapter.Runtime().Require(b.Grant, b.Now()); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	profile, err := os.MkdirTemp("", "aeon-browser-capture-")
	if err != nil {
		return out, err
	}
	// MkdirTemp is private and never based on a vendor home or workspace path.
	removeProfile := true
	defer func() {
		if removeProfile {
			_ = os.RemoveAll(profile)
		}
	}()
	reader := newBrowserCaptureReader(ctx, b.Policy, b.Grant, func(ctx context.Context, g agentsetup.RoutineGrant) error {
		if err := b.Adapter.Runtime().Require(g, b.Now()); err != nil {
			return err
		}
		return b.Authorize(ctx, g)
	})
	process, err := b.Adapter.StartCapture(ctx, BrowserCaptureStart{Grant: b.Grant, ProfileDirectory: profile, Width: width, Height: height, Read: reader.read})
	if err != nil || process == nil {
		// Start errors can follow a fork; retain its private state and intent.
		removeProfile = false
		return out, errors.Join(ErrBrowserCaptureUnconfirmed, err)
	}
	identity, err := process.Ownership()
	if err != nil || !validBrowserProcess(identity, b.Grant) {
		removeProfile = false
		return out, ErrBrowserCaptureUnconfirmed
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if process.Finish(cleanupCtx, identity) != nil {
			removeProfile = false
			resultErr = errors.Join(resultErr, ErrBrowserCaptureUnconfirmed)
		}
	}()
	data, err := process.Capture(ctx, in.URL)
	if err != nil {
		return out, err
	}
	if data == nil {
		return out, ErrBrowserCaptureUnconfirmed
	}
	stopRead := context.AfterFunc(ctx, func() { _ = data.Close() })
	image, readErr := io.ReadAll(io.LimitReader(data, browserCaptureBytes+1))
	stopRead()
	closeErr := data.Close()
	if readErr != nil || closeErr != nil {
		return out, errors.Join(readErr, closeErr)
	}
	if len(image) == 0 || len(image) > browserCaptureBytes {
		return out, ErrBrowserCaptureLimit
	}
	config, err := png.DecodeConfig(bytes.NewReader(image))
	if err != nil || config.Width != width || config.Height != height {
		return out, ErrBrowserCaptureLimit
	}
	// Require a complete valid PNG, with bounded dimensions before decoding.
	if _, err := png.Decode(bytes.NewReader(image)); err != nil {
		return out, ErrBrowserCaptureLimit
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = process.Finish(cleanupCtx, identity)
	cleanupCancel()
	finished = true
	if err != nil {
		removeProfile = false
		return out, ErrBrowserCaptureUnconfirmed
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := b.Authorize(ctx, b.Grant); err != nil {
		return out, err
	}
	if err := b.Adapter.Runtime().Require(b.Grant, b.Now()); err != nil {
		return out, err
	}
	receipt := BrowserCaptureReceipt{TenantID: b.Grant.TenantID, RunID: b.Grant.AgentRunID, OutputID: in.OutputID, RequestDigest: digest,
		SHA256: fmt.Sprintf("%x", sha256.Sum256(image)), Bytes: len(image), Width: width, Height: height, Process: identity, ExitObserved: true}
	out, err = b.Store.Commit(ctx, b.Grant, receipt, image)
	if err != nil {
		return BrowserCaptureReceipt{}, err
	}
	if err := validBrowserReceipt(out, b.Grant, in, digest, width, height); err != nil || out.SHA256 != receipt.SHA256 || out.Bytes != receipt.Bytes || out.Process != identity {
		return BrowserCaptureReceipt{}, ErrBrowserCaptureUnconfirmed
	}
	return out, nil
}

func validBrowserProcess(p ownedprocess.Identity, g agentsetup.RoutineGrant) bool {
	return p.DaemonID == g.DaemonID && p.Generation == g.DaemonGeneration && len(p.ProcessID) == 32 &&
		strings.Trim(p.ProcessID, "0123456789abcdef") == "" && p.RootPID > 0 && p.GroupID == p.RootPID && !p.StartedAt.IsZero()
}

func validBrowserReceipt(r BrowserCaptureReceipt, g agentsetup.RoutineGrant, in BrowserCaptureRequest, digest string, width, height int) error {
	if r.TenantID != g.TenantID || r.RunID != g.AgentRunID || r.OutputID != in.OutputID || r.RequestDigest != digest ||
		!uuidPattern.MatchString(r.AttachmentID) || !agentsetup.RoutineDigest(r.SHA256) || r.Bytes < 1 || r.Bytes > browserCaptureBytes ||
		r.Width != width || r.Height != height || !r.ExitObserved || !validBrowserProcess(r.Process, g) {
		return ErrBrowserCaptureUnconfirmed
	}
	return nil
}
