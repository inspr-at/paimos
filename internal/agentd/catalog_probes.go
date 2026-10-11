// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/piprobe"
)

type catalogCapture struct {
	State              string
	Models             []modelreport.CatalogModel
	CleanupUnconfirmed bool
}
type catalogProbeAPI interface {
	ClaimCatalogProbes(context.Context, string, string, string) ([]modelreport.CatalogProbe, string, error)
	ReportCatalogProbe(context.Context, string, modelreport.CatalogReport) error
}

func (r *Remote) catalogJSON(ctx context.Context, path string, body, dest any) error {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > modelreport.CatalogBytesMax {
		return errors.New("catalog request bound")
	}
	req, err := http.NewRequestWithContext(op, http.MethodPost, r.Client.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("catalog request unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+r.Client.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := *r.Client.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := hc.Do(req)
	if err != nil {
		return errors.New("catalog transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &client.StatusError{Status: res.StatusCode}
	}
	raw, err = io.ReadAll(io.LimitReader(res.Body, modelreport.CatalogBytesMax+1))
	if err != nil || len(raw) > modelreport.CatalogBytesMax {
		return errors.New("catalog response bound")
	}
	if decodeProbeJSON(raw, dest) != nil {
		return errors.New("catalog response malformed")
	}
	return nil
}

func (r *Remote) ClaimCatalogProbes(ctx context.Context, daemon, generation, cursor string) ([]modelreport.CatalogProbe, string, error) {
	r.mu.RLock()
	unsupported := r.clock().Before(r.catalogRetryAt)
	r.mu.RUnlock()
	if unsupported {
		return []modelreport.CatalogProbe{}, "", nil
	}
	var out struct {
		Items []modelreport.CatalogProbe `json:"items"`
		Next  string                     `json:"next_cursor"`
	}
	err := r.catalogJSON(ctx, "/api/models/harness-probes/claim", map[string]string{"daemon_id": daemon, "daemon_generation": generation, "cursor": cursor}, &out)
	if err != nil {
		var status *client.StatusError
		if errors.As(err, &status) && (status.Status == 404 || status.Status == 405 || status.Status == 403) {
			r.mu.Lock()
			r.catalogRetryAt = r.clock().Add(5 * time.Minute)
			r.mu.Unlock()
			return []modelreport.CatalogProbe{}, "", nil
		}
		return nil, "", err
	}
	if out.Items == nil || len(out.Items) > 16 || len(out.Next) > 36 {
		return nil, "", errors.New("catalog request list bound")
	}
	return out.Items, out.Next, nil
}
func (r *Remote) ReportCatalogProbe(ctx context.Context, account string, report modelreport.CatalogReport) error {
	var out struct {
		Recorded bool `json:"recorded"`
	}
	if err := r.catalogJSON(ctx, "/api/models/harness-probes/"+url.PathEscape(account), report, &out); err != nil {
		return err
	}
	if !out.Recorded {
		return errors.New("catalog report not recorded")
	}
	return nil
}

type catalogRPC func(context.Context, string, any) (json.RawMessage, error)

func collectCodexCatalog(ctx context.Context, rpc catalogRPC) catalogCapture {
	out := catalogCapture{State: "fresh", Models: []modelreport.CatalogModel{}}
	cursor := ""
	seen := map[string]bool{}
	budget := modelreport.CatalogBytesMax
	for page := 0; page < 5; page++ {
		args := map[string]any{"limit": 100, "includeHidden": true}
		if cursor != "" {
			args["cursor"] = cursor
		}
		raw, err := rpc(ctx, "model/list", args)
		if err != nil {
			if len(out.Models) > 0 {
				out.State = "limited"
			} else if ctx.Err() != nil {
				out.State = "timeout"
			} else {
				out.State = "stale"
			}
			return out
		}
		if len(raw) > budget {
			out.State = "limited"
			return out
		}
		budget -= len(raw)
		models, next, err := parseCodexCatalog(raw)
		if err != nil {
			out.State = "stale"
			out.Models = []modelreport.CatalogModel{}
			return out
		}
		out.Models = append(out.Models, models...)
		if modelreport.ValidateCatalog("fresh", out.Models) != nil {
			return catalogCapture{State: "stale", Models: []modelreport.CatalogModel{}}
		}
		if next == "" {
			return out
		}
		if seen[next] {
			out.State = "limited"
			return out
		}
		seen[next] = true
		cursor = next
	}
	out.State = "limited"
	return out
}

func collectPiCatalog(ctx context.Context, provider string, rpc catalogRPC) catalogCapture {
	raw, err := rpc(ctx, "get_available_models", nil)
	if err != nil {
		return failedCatalog(ctx)
	}
	models, err := parsePiCatalog(raw, provider)
	if err != nil {
		return failedCatalog(ctx)
	}
	out := catalogCapture{State: "fresh", Models: models}
	for i := range out.Models {
		_, id, _ := strings.Cut(out.Models[i].Model, "/")
		raw, err = rpc(ctx, "set_model", map[string]any{"provider": provider, "modelId": id})
		var selected struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		}
		if err != nil || decodeProbeJSON(raw, &selected) != nil || selected.Provider != provider || selected.ID != id {
			out.State = "limited"
			return out
		}
		raw, err = rpc(ctx, "get_available_thinking_levels", nil)
		if err != nil || parsePiLevels(raw, &out.Models[i]) != nil {
			out.State = "limited"
			return out
		}
	}
	return out
}
func failedCatalog(ctx context.Context) catalogCapture {
	state := "stale"
	if ctx.Err() != nil {
		state = "timeout"
	}
	return catalogCapture{State: state, Models: []modelreport.CatalogModel{}}
}
func unsupportedCatalog() catalogCapture {
	return catalogCapture{State: "unsupported", Models: []modelreport.CatalogModel{}}
}

// Owned process cleanup is part of the result. An unconfirmed child holds the
// existing durable capture fence instead of allowing another idle launch.
func finishCatalogWire(p *wireProcess, out *catalogCapture) {
	p.closeReader()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if p.lifetime == nil {
		out.CleanupUnconfirmed = true
	} else {
		_ = p.lifetime.Signal(true)
		select {
		case <-p.waitDone:
			out.CleanupUnconfirmed = p.cleanupResult() != nil
		case <-ctx.Done():
			out.CleanupUnconfirmed = true
		}
	}
	if p.finishReader() != nil {
		out.CleanupUnconfirmed = true
	}
	if out.CleanupUnconfirmed {
		out.State = "stale"
		out.Models = []modelreport.CatalogModel{}
	}
}

func catalogCommand(ctx context.Context, path string, env []string, args ...string) ([]byte, error) {
	path, err := pinnedExecutable(path)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = "/"
	stdout, stderr := &probeCapture{max: modelreport.CatalogBytesMax}, &probeCapture{max: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = ownedprocess.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if stdout.overflow || stderr.overflow {
		return nil, errCatalogProtocol
	}
	return stdout.buf, nil
}

func (s *Supervisor) activeCatalogProcess(account string) Process {
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		matches := e.record.AccountID == account && !e.stopRequested && !e.protocolFailed && !e.harnessArchived
		process := e.process
		e.mu.Unlock()
		if matches && process != nil {
			return process
		}
	}
	return nil
}

func (s *Supervisor) captureCatalog(ctx context.Context, local EnrolledAccount, adapter Adapter) (out catalogCapture) {
	active := s.activeCatalogProcess(local.ID)
	if p, ok := active.(*codexProcess); ok {
		if p.verify(ctx) != nil {
			return failedCatalog(ctx)
		}
		return collectCodexCatalog(ctx, func(ctx context.Context, method string, args any) (json.RawMessage, error) {
			return p.request(ctx, "jsonrpc", method, args)
		})
	}
	if p, ok := active.(*claudeProcess); ok {
		return captureClaudeCatalog(ctx, p)
	}
	if p, ok := active.(*claudeReviewProcess); ok {
		return captureClaudeCatalog(ctx, p.claudeProcess)
	}
	switch a := adapter.(type) {
	case *CodexAdapter:
		// Keep release qualification of idle startup hooks/config/termination.
		if !a.CanCaptureCapacity(local.Key) {
			return unsupportedCatalog()
		}
		home, err := localHome(a.Homes, local.Key)
		if err != nil {
			return failedCatalog(ctx)
		}
		if harnesslaunch.Validate(a.Path, a.Nodes[local.Key].Path) != nil {
			return failedCatalog(ctx)
		}
		p, err := launchWire(a.Path, []string{"app-server", "--listen", "stdio://"}, home, harnesslaunch.Environment(capacityEnvironment("CODEX_HOME", home, a.Path), a.Nodes[local.Key].Path), "jsonrpc", func(AdapterEvent) {})
		if err != nil {
			return failedCatalog(ctx)
		}
		defer finishCatalogWire(p, &out)
		if _, err = p.request(ctx, "jsonrpc", "initialize", map[string]any{"clientInfo": map[string]string{"name": "aeon-catalog", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
			return failedCatalog(ctx)
		}
		if err = p.sendContext(ctx, map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
			return failedCatalog(ctx)
		}
		raw, err := p.request(ctx, "jsonrpc", "account/read", map[string]any{"refreshToken": false})
		var identity struct {
			Account *struct{ Type, Email string } `json:"account"`
		}
		if err != nil || decodeProbeJSON(raw, &identity) != nil || identity.Account == nil || identity.Account.Type != "chatgpt" || !agentsetup.CodexAccountMatches(a.Emails[local.Key], identity.Account.Email) {
			return failedCatalog(ctx)
		}
		return collectCodexCatalog(ctx, func(ctx context.Context, method string, args any) (json.RawMessage, error) {
			return p.request(ctx, "jsonrpc", method, args)
		})
	case *PiAdapter:
		if active != nil {
			return unsupportedCatalog()
		} // Never change a running session's selected model.
		home, err := localHome(a.Homes, local.Key)
		if err != nil || !piprobe.ValidProvider(a.Providers[local.Key]) || harnesslaunch.Validate(a.Path, a.Nodes[local.Key].Path) != nil {
			return failedCatalog(ctx)
		}
		p, err := launchWire(a.Path, []string{"--mode", "rpc", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes"}, "/", piprobe.Environment(home, a.Nodes[local.Key].Path), "pi", func(AdapterEvent) {})
		if err != nil {
			return failedCatalog(ctx)
		}
		defer finishCatalogWire(p, &out)
		return collectPiCatalog(ctx, a.Providers[local.Key], func(ctx context.Context, method string, args any) (json.RawMessage, error) {
			return p.request(ctx, "pi", method, args)
		})
	case *CursorAdapter:
		home, err := localHome(a.Homes, local.Key)
		if err != nil || a.Identities[local.Key] == "" || harnesslaunch.Validate(a.Path, a.Nodes[local.Key].Path) != nil {
			return failedCatalog(ctx)
		}
		env := harnesslaunch.Environment(capacityEnvironment("CURSOR_CONFIG_DIR", home, a.Path), a.Nodes[local.Key].Path)
		raw, err := catalogCommand(ctx, a.Path, env, "models")
		if err != nil {
			out = failedCatalog(ctx)
			out.CleanupUnconfirmed = errors.Is(err, ownedprocess.ErrCleanupUnconfirmed)
			return out
		}
		models, err := parseCursorCatalog(raw)
		if err != nil {
			return failedCatalog(ctx)
		}
		return catalogCapture{State: "fresh", Models: models}
	case *ACPAdapter:
		if a.Harness != OpenCode {
			return unsupportedCatalog()
		}
		env, err := a.environment(local.Key)
		if err != nil || harnesslaunch.Validate(a.Path, a.Nodes[local.Key].Path) != nil {
			return failedCatalog(ctx)
		}
		raw, err := catalogCommand(ctx, a.Path, env, "models", "--verbose")
		if err != nil {
			out = failedCatalog(ctx)
			out.CleanupUnconfirmed = errors.Is(err, ownedprocess.ErrCleanupUnconfirmed)
			return out
		}
		models, err := parseOpenCodeCatalog(raw)
		if err != nil {
			return failedCatalog(ctx)
		}
		return catalogCapture{State: "fresh", Models: models}
	default:
		return unsupportedCatalog()
	}
}
func captureClaudeCatalog(ctx context.Context, p *claudeProcess) catalogCapture {
	if p.verify(ctx) != nil {
		return failedCatalog(ctx)
	}
	raw, err := p.request(ctx, "jsonrpc", "aeon/catalog", map[string]any{})
	if err != nil {
		return unsupportedCatalog()
	}
	models, err := parseClaudeCatalog(raw)
	if err != nil {
		return failedCatalog(ctx)
	}
	return catalogCapture{State: "fresh", Models: models}
}

// The health poll services catalogs with no queued run. The server owns clocks,
// cooldowns and leases; this loop holds the existing capture/dispatch fence.
func (s *Supervisor) captureModelCatalogs(ctx context.Context) {
	api, ok := s.api.(catalogProbeAPI)
	if !ok || !s.capacityCheckMu.TryLock() {
		return
	}
	defer s.capacityCheckMu.Unlock()
	op, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cursor := ""
	for page := 0; page < 4; page++ {
		requests, next, err := api.ClaimCatalogProbes(op, s.daemonID, s.generation, cursor)
		if err != nil {
			logCapacityError("model catalog poll failed", "", err)
			return
		}
		if len(requests) > 16 {
			return
		}
		for _, request := range requests {
			if op.Err() != nil {
				return
			}
			s.mu.Lock()
			var local EnrolledAccount
			for _, a := range s.accounts {
				if a.ID == request.AccountID {
					local = a
					break
				}
			}
			adapter := s.adapters[local.Harness]
			available := s.probedAccounts[local.ID] && !s.blockedAccounts[local.ID] || local.Harness == OpenCode && s.harnessHolds[local.Harness] == ""
			s.mu.Unlock()
			if local.ID == "" || local.Key != request.AccountKey || local.Harness != request.Harness || local.DependencyBlocked || adapter == nil || !available || !s.dispatchAllowed(local.ID) || !s.dispatchMu.TryLock() {
				continue
			}
			s.mu.Lock()
			busy := s.capacityCapturing
			if !busy {
				s.capacityCapturing = true
				s.capacityAccountID = local.ID
				s.capacityStartedAt = s.capacityNow()
			}
			s.mu.Unlock()
			s.dispatchMu.Unlock()
			if busy {
				continue
			}
			captureCtx, stop := context.WithTimeout(op, 15*time.Second)
			capture := s.captureCatalog(captureCtx, local, adapter)
			stop()
			s.mu.Lock()
			if capture.CleanupUnconfirmed {
				state := s.capacityChecks[local.ID]
				state.CleanupUnconfirmed = true
				s.capacityChecks[local.ID] = state
				if err = s.saveCapacityChecksLocked(); err != nil {
					logCapacityError("catalog cleanup fence persistence failed", local.ID, err)
				}
			} else {
				s.capacityCapturing = false
				s.capacityAccountID = ""
				s.capacityStartedAt = time.Time{}
			}
			s.mu.Unlock()
			if op.Err() != nil {
				return
			}
			fenced, err := s.readFence(local.ID)
			if fenced || err != nil || !s.dispatchAllowed(local.ID) {
				continue
			}
			report := modelreport.CatalogReport{RequestID: request.RequestID, BindingRevision: request.BindingRevision, DaemonID: s.daemonID, Generation: s.generation, State: capture.State, Models: capture.Models}
			if err = api.ReportCatalogProbe(op, local.ID, report); err != nil {
				logCapacityError("model catalog report failed", local.ID, err)
			}
		}
		if next == "" || next == cursor {
			return
		}
		cursor = next
	}
}
