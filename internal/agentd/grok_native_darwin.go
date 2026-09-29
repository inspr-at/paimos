// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

type grokSpec struct{ name, digest string }

func (*GrokAdapter) probeNative(ctx context.Context, b GrokBinding) bool {
	if _, _, err := verifyGrokBinding(b, ""); err != nil {
		return false
	}
	_, bearer, subject, err := readGrokAuth(b.AuthPath, b.PrincipalSHA256)
	return err == nil && verifyGrokUserInfo(ctx, bearer, subject) == nil
}

func grokVariant(variant string) (grokSpec, error) {
	switch variant {
	case "npm-grok-1.0.30":
		return grokSpec{"grok-native", "d53b6e543e482716236748914331db50145c696ac7af91f1ebdedcf5654cfecb"}, nil
	case "source-xai-grok-pager-1.0.32":
		return grokSpec{"xai-grok-pager", "6294a6bc10304e3d3b5896194277f6d24fca643b0605d650ca39bad5b40a6d14"}, nil
	default:
		return grokSpec{}, errors.New("native Grok variant unsupported")
	}
}

type grokProcess struct {
	*wireProcess
	proxy       *grokProxy
	scratch     string
	scratchInfo os.FileInfo
	binding     GrokBinding
	authBefore  grokAuthSnapshot
	promptDone  chan error
	mu          sync.Mutex
	answer      strings.Builder
	events      int
	sessionID   string
	model       string
	observe     func(AdapterEvent)
	violation   atomic.Bool
	cleanOnce   sync.Once
}

func (p *grokProcess) Evidence() string                              { p.mu.Lock(); defer p.mu.Unlock(); return p.answer.String() }
func (p *grokProcess) Control(context.Context, string, string) error { return ErrUnsupported }
func (p *grokProcess) Stop(ctx context.Context) error                { p.proxy.stop(); return p.wireProcess.Stop(ctx) }
func (p *grokProcess) Wait() error {
	err := <-p.promptDone
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = p.Stop(ctx)
	cancel()
	_ = p.wireProcess.Wait()
	if p.violation.Load() || p.proxy.violation.Load() || verifyGrokAuthUnchanged(p.binding.AuthPath, p.binding.PrincipalSHA256, p.authBefore) != nil ||
		verifyGrokAssets(p.scratch) != nil || !grokFunctionToolsEmpty(p.scratch) {
		err = errors.New("native Grok confinement or account changed")
	}
	p.cleanOnce.Do(p.cleanup)
	return err
}
func (p *grokProcess) cleanup() {
	p.proxy.stop()
	info, err := os.Lstat(p.scratch)
	if err == nil && os.SameFile(info, p.scratchInfo) {
		_ = os.RemoveAll(p.scratch)
	}
}

func (a *GrokAdapter) startNative(ctx context.Context, r StartRequest, b GrokBinding, observe func(AdapterEvent)) (_ Process, returnErr error) {
	spec, root, err := verifyGrokBinding(b, r.Workspace)
	if err != nil {
		return nil, err
	}
	before, bearer, subject, err := readGrokAuth(b.AuthPath, b.PrincipalSHA256)
	if err != nil {
		return nil, err
	}
	if err := verifyGrokUserInfo(ctx, bearer, subject); err != nil {
		return nil, err
	}
	bearer = ""
	scratch, err := os.MkdirTemp(b.ScratchRoot, "aeon-grok-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(scratch, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(scratch)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			current, e := os.Lstat(scratch)
			if e == nil && os.SameFile(current, info) {
				_ = os.RemoveAll(scratch)
			}
		}
	}()
	for _, path := range []string{"home/agent-profiles", "work", "tmp", "cache"} {
		if err := os.MkdirAll(filepath.Join(scratch, path), 0700); err != nil {
			return nil, err
		}
	}
	config, err := grokAssets.ReadFile("grokassets/config.toml")
	if err != nil || sha256Hex(config) != grokConfigSHA256 {
		return nil, errors.New("native Grok config unavailable")
	}
	profile, err := grokAssets.ReadFile("grokassets/conversation.txt")
	if err != nil || sha256Hex(profile) != grokProfileSHA256 {
		return nil, errors.New("native Grok profile unavailable")
	}
	configPath := filepath.Join(scratch, "home", "config.toml")
	profilePath := filepath.Join(scratch, "home", "agent-profiles", "conversation.txt")
	if err := writeGrokAsset(configPath, config); err != nil {
		return nil, err
	}
	if err := writeGrokAsset(profilePath, profile); err != nil {
		return nil, err
	}
	proxy, err := startGrokProxy()
	if err != nil {
		return nil, err
	}
	defer func() {
		if !started {
			proxy.stop()
		}
	}()
	seatbelt := grokSeatbelt(root, scratch, b.BinaryPath, b.AuthPath, proxy.port)
	seatbeltPath := filepath.Join(scratch, "seatbelt.sb")
	if err := os.WriteFile(seatbeltPath, []byte(seatbelt), 0600); err != nil {
		return nil, err
	}
	args := []string{"-f", seatbeltPath, b.BinaryPath, "--no-auto-update", "--no-memory", "--no-subagents", "--disable-web-search",
		"--permission-mode", "dontAsk", "--cwd", filepath.Join(scratch, "work"), "agent", "--agent-profile", profilePath,
		"--no-leader", "-m", grokModel, "--reasoning-effort", grokEffort, "stdio"}
	env := []string{"HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "LOGNAME=" + os.Getenv("LOGNAME"),
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TERM=dumb", "GROK_HOME=" + filepath.Join(scratch, "home"), "GROK_AUTH_PATH=" + b.AuthPath,
		"TMPDIR=" + filepath.Join(scratch, "tmp"), "XDG_CACHE_HOME=" + filepath.Join(scratch, "cache"),
		"HTTPS_PROXY=" + proxy.url(), "HTTP_PROXY=" + proxy.url(), "NO_PROXY=", "GROK_BACKEND_SEARCH=0", "GROK_LOGIN_ENV=0",
		"GROK_AGENT_DASHBOARD=0", "GROK_WORKFLOWS=0", "GROK_MEMORY=0", "GROK_SUBAGENTS=0", "GROK_TELEMETRY_ENABLED=0"}
	p, err := launchWire("/usr/bin/sandbox-exec", args, filepath.Join(scratch, "work"), env, "jsonrpc", observe)
	if err != nil {
		return nil, err
	}
	gp := &grokProcess{wireProcess: p, proxy: proxy, scratch: scratch, scratchInfo: info, binding: b, authBefore: before, promptDone: make(chan error, 1), model: grokModel, observe: observe}
	p.setOnEvent(gp.onEvent)
	fail := func(e error) (Process, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(ctx)
		cancel()
		return nil, e
	}
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := p.request(op, "jsonrpc", "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}})
	var init struct {
		ProtocolVersion int `json:"protocolVersion"`
		AuthMethods     []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	if err != nil || json.Unmarshal(raw, &init) != nil || init.ProtocolVersion != 1 {
		return fail(errors.New("native Grok ACP initialize failed"))
	}
	cached := false
	for _, m := range init.AuthMethods {
		if m.ID == "cached_token" {
			cached = true
		}
	}
	if !cached {
		return fail(errors.New("native Grok cached auth unavailable"))
	}
	if _, err := p.request(op, "jsonrpc", "authenticate", map[string]any{"methodId": "cached_token", "_meta": map[string]bool{"headless": true}}); err != nil {
		return fail(err)
	}
	raw, err = p.request(op, "jsonrpc", "session/new", map[string]any{"cwd": filepath.Join(scratch, "work"), "mcpServers": []any{}})
	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			Current string `json:"currentModelId"`
		} `json:"models"`
		Options []struct {
			ID    string `json:"id"`
			Value string `json:"currentValue"`
		} `json:"configOptions"`
	}
	if err != nil || json.Unmarshal(raw, &session) != nil || session.SessionID == "" || session.Models.Current != grokModel {
		return fail(errors.New("native Grok session unavailable"))
	}
	model, effort := "", ""
	for _, option := range session.Options {
		if option.ID == "model" {
			model = option.Value
		}
		if option.ID == "reasoning_effort" {
			effort = option.Value
		}
	}
	if model != grokModel || effort != grokEffort || proxy.violation.Load() || gp.violation.Load() || verifyGrokAssets(scratch) != nil || !grokProfileMarkerObserved(scratch, profilePath) {
		return fail(errors.New("native Grok profile was not verified"))
	}
	gp.mu.Lock()
	gp.sessionID = session.SessionID
	gp.mu.Unlock()
	if spec.digest == "" {
		return fail(errors.New("native Grok binary unavailable"))
	}
	observe(AdapterEvent{Kind: "status", EffectiveModel: grokModel, ModelEvidence: "vendor_reported"})
	started = true
	go func() {
		turnCtx, turnCancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer turnCancel()
		raw, e := p.request(turnCtx, "jsonrpc", "session/prompt", map[string]any{"sessionId": session.SessionID, "prompt": []map[string]string{{"type": "text", "text": r.Prompt}}})
		var result struct {
			StopReason string `json:"stopReason"`
		}
		if e == nil && (json.Unmarshal(raw, &result) != nil || result.StopReason != "end_turn" || gp.Evidence() == "") {
			e = errors.New("native Grok turn did not complete")
		}
		gp.promptDone <- e
	}()
	return gp, nil
}

func (p *grokProcess) onEvent(raw json.RawMessage) {
	var frame struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &frame) != nil || len(frame.ID) > 0 {
		p.violation.Store(true)
		return
	}
	if frame.Method == "_x.ai/mcp/servers_updated" {
		var params struct {
			Servers []any `json:"mcpServers"`
		}
		if json.Unmarshal(frame.Params, &params) == nil && params.Servers != nil && len(params.Servers) == 0 {
			return
		}
	}
	if frame.Method == "_x.ai/mcp_initialized" {
		var params struct {
			Count *int `json:"mcpToolCount"`
		}
		if json.Unmarshal(frame.Params, &params) == nil && params.Count != nil && *params.Count == 0 {
			return
		}
	}
	if frame.Method == "session/update" {
		var params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		p.mu.Lock()
		sessionID := p.sessionID
		p.mu.Unlock()
		if json.Unmarshal(frame.Params, &params) == nil && sessionID != "" && params.SessionID == sessionID {
			switch params.Update.Kind {
			case "agent_message_chunk":
				if params.Update.Content.Type == "text" {
					p.mu.Lock()
					p.events++
					if p.events <= 2048 && p.answer.Len()+len(params.Update.Content.Text) <= 64<<10 {
						_, _ = p.answer.WriteString(params.Update.Content.Text)
						p.mu.Unlock()
						return
					}
					p.mu.Unlock()
				}
			case "usage_update":
				if p.observe != nil {
					if report, ok := grokNativeUsage(frame.Params, p.model); ok {
						p.observe(AdapterEvent{SessionUsage: &report})
					}
				}
				return
			case "agent_thought_chunk", "current_mode_update", "session_info_update", "plan", "available_commands_update":
				return
			}
		}
	}
	p.violation.Store(true)
	if p.wireProcess != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.Stop(ctx)
		}()
	}
}

func grokNativeUsage(raw json.RawMessage, model string) (sessionusage.UsageReport, bool) {
	var params struct {
		Update struct {
			Input     *int64 `json:"inputTokens"`
			Output    *int64 `json:"outputTokens"`
			Cached    *int64 `json:"cachedReadTokens"`
			Reasoning *int64 `json:"reasoningTokens"`
			Model     string `json:"model"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Update.Input == nil || params.Update.Output == nil || *params.Update.Input < 0 || *params.Update.Output < 0 {
		return sessionusage.UsageReport{}, false
	}
	name := params.Update.Model
	if name == "" {
		name = model
	}
	cached, known := int64(0), false
	if params.Update.Cached != nil && *params.Update.Cached >= 0 && *params.Update.Cached <= *params.Update.Input {
		cached, known = *params.Update.Cached, true
	}
	report, ok := sessionusage.CountReport(name, *params.Update.Input, *params.Update.Output, cached, known)
	if !ok {
		return sessionusage.UsageReport{}, false
	}
	if params.Update.Reasoning != nil {
		if *params.Update.Reasoning < 0 || *params.Update.Reasoning > *params.Update.Output {
			return sessionusage.UsageReport{}, false
		}
		reasoning := *params.Update.Reasoning
		report.ReasoningTokens = &reasoning
	}
	return report, true
}

func safeGrokPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\x00\r\n\"\\")
}
func pathInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
func verifyGrokBinding(b GrokBinding, workspace string) (grokSpec, string, error) {
	spec, err := grokVariant(b.Variant)
	if err != nil {
		return spec, "", err
	}
	for _, path := range []string{b.BinaryPath, b.AuthPath, b.ScratchRoot} {
		if !safeGrokPath(path) {
			return spec, "", errors.New("native Grok path unavailable")
		}
	}
	if len(b.PrincipalSHA256) != 64 {
		return spec, "", errors.New("native Grok principal binding invalid")
	}
	if _, err := hex.DecodeString(b.PrincipalSHA256); err != nil {
		return spec, "", err
	}
	if _, err := pinnedExecutable(b.BinaryPath); err != nil {
		return spec, "", err
	}
	root := filepath.Dir(b.BinaryPath)
	if b.Variant == "npm-grok-1.0.30" {
		root = filepath.Dir(root)
		if filepath.Base(root) != "grok" || filepath.Base(filepath.Dir(root)) != "@xai-official" || filepath.Base(filepath.Dir(filepath.Dir(root))) != "node_modules" {
			return spec, "", errors.New("native Grok package path invalid")
		}
	}
	if b.Variant == "source-xai-grok-pager-1.0.32" && (filepath.Base(root) != "release" || filepath.Base(filepath.Dir(root)) != "target") {
		return spec, "", errors.New("native Grok build path invalid")
	}
	if filepath.Base(b.BinaryPath) != spec.name || pathInside(root, b.AuthPath) || pathInside(root, b.ScratchRoot) || pathInside(b.ScratchRoot, root) ||
		pathInside(workspace, root) || pathInside(root, workspace) || pathInside(workspace, b.ScratchRoot) || pathInside(b.ScratchRoot, workspace) ||
		pathInside(b.ScratchRoot, b.AuthPath) {
		return spec, "", errors.New("native Grok path boundary invalid")
	}
	physical, err := filepath.EvalSymlinks(b.ScratchRoot)
	if err != nil || physical != b.ScratchRoot {
		return spec, "", errors.New("native Grok scratch path changed")
	}
	info, err := os.Stat(b.ScratchRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return spec, "", errors.New("native Grok scratch is not private")
	}
	f, err := os.Open(b.BinaryPath)
	if err != nil {
		return spec, "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil || hex.EncodeToString(hash.Sum(nil)) != spec.digest {
		return spec, "", errors.New("native Grok binary hash mismatch")
	}
	return spec, root, nil
}
func writeGrokAsset(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
func verifyGrokAssets(scratch string) error {
	for _, item := range []struct{ path, digest string }{{filepath.Join(scratch, "home", "config.toml"), grokConfigSHA256}, {filepath.Join(scratch, "home", "agent-profiles", "conversation.txt"), grokProfileSHA256}} {
		info, err := os.Lstat(item.path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("native Grok asset unavailable")
		}
		raw, err := os.ReadFile(item.path)
		if err != nil || sha256Hex(raw) != item.digest {
			return errors.New("native Grok asset changed")
		}
	}
	return nil
}
func grokSeatbelt(root, scratch, binary, auth string, port int) string {
	paths := []string{"/System", "/usr", "/Library", "/dev", "/nix/store", "/bin", "/sbin", "/.resolve", "/.vol", "/.nofollow", root, scratch}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-read*)\n(allow file-read-metadata)\n(allow file-read* (literal \"/\"))\n")
	for _, path := range paths {
		b.WriteString("(allow file-read* (subpath " + strconv.Quote(path) + "))\n")
	}
	b.WriteString("(allow file-read* (literal " + strconv.Quote(binary) + "))\n")
	b.WriteString("(allow file-read* (literal " + strconv.Quote(auth) + "))\n")
	b.WriteString("(deny file-write*)\n(allow file-write* (subpath " + strconv.Quote(scratch) + "))\n")
	b.WriteString("(deny network-outbound)\n(allow network-outbound (remote ip \"localhost:" + strconv.Itoa(port) + "\"))\n")
	return b.String()
}
func grokProfileMarkerObserved(scratch, profilePath string) bool {
	found, count := false, 0
	_ = filepath.WalkDir(scratch, func(path string, e os.DirEntry, err error) error {
		if err != nil || count > 1024 {
			return filepath.SkipAll
		}
		count++
		if e.IsDir() || path == profilePath || !e.Type().IsRegular() {
			return nil
		}
		info, e2 := e.Info()
		if e2 != nil || info.Size() > 1<<20 {
			return nil
		}
		raw, e2 := os.ReadFile(path)
		if e2 == nil && bytes.Contains(raw, []byte("Protocol qualification. No tools or workspace context.")) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
func grokFunctionToolsEmpty(scratch string) bool {
	found, valid, count := false, true, 0
	_ = filepath.WalkDir(scratch, func(path string, e os.DirEntry, err error) error {
		if err != nil || count > 1024 {
			valid = false
			return filepath.SkipAll
		}
		count++
		if e.IsDir() || e.Name() != "tool_definitions.json" {
			return nil
		}
		found = true
		info, e2 := e.Info()
		if e2 != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			valid = false
			return filepath.SkipAll
		}
		raw, e2 := os.ReadFile(path)
		var tools []json.RawMessage
		if e2 != nil || json.Unmarshal(raw, &tools) != nil || tools == nil || len(tools) != 0 {
			valid = false
		}
		return nil
	})
	return found && valid
}
