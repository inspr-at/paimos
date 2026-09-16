//go:build darwin

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/ownedprocess"
)

const grokConversationDeadline = 180 * time.Second

type grokConversationProcess struct {
	cmd             *exec.Cmd
	wire            *grokACPWire
	stdin           io.WriteCloser
	proxy           *grokProxy
	scratch         string
	scratchIdentity os.FileInfo
	authPath        string
	authPrincipal   string
	authBefore      grokAuthSnapshot
	sessionID       string
	turnID          string
	done            chan struct{}
	mu              sync.RWMutex
	result          CodexConversationResult
	deltas          []CodexConversationDelta
	stopOnce        sync.Once
	stopFailure     ConversationFailure
	finished        bool
}

func (a *GrokConversationAdapter) startNativeConversation(ctx context.Context, request StartRequest, options GrokConversationOptions) (_ GrokConversationExecution, returnErr error) {
	if err := verifyGrokExecutableVariant(a.binding.BinaryVariant, a.binding.BinaryPath); err != nil {
		return nil, err
	}
	before, bearer, subject, err := readGrokAuth(a.binding.AuthPath, a.binding.PrincipalSHA256)
	if err != nil {
		return nil, err
	}
	if err = verifyGrokUserInfo(ctx, bearer, subject); err != nil {
		return nil, err
	}
	bearer = "" // Keep no token in the execution object or child environment.
	root, rootIdentity, err := canonicalCodexConversationScratch(a.binding.ScratchRoot, false)
	if err != nil || root != a.binding.ScratchRoot || rootIdentity.Mode().Perm()&0077 != 0 {
		return nil, errors.New("native Grok scratch root unavailable")
	}
	scratch, err := os.MkdirTemp(root, "paimos-grok-conversation-")
	if err != nil {
		return nil, errors.New("native Grok scratch unavailable")
	}
	if err = os.Chmod(scratch, 0700); err != nil {
		_ = os.RemoveAll(scratch)
		return nil, errors.New("native Grok scratch unavailable")
	}
	identity, err := os.Lstat(scratch)
	if err != nil || !identity.IsDir() {
		_ = os.RemoveAll(scratch)
		return nil, errors.New("native Grok scratch unavailable")
	}
	defer func() {
		if returnErr != nil {
			cleanupGrokScratch(scratch, identity)
		}
	}()
	for _, path := range []string{"home/agent-profiles", "work", "tmp", "cache"} {
		if err = os.MkdirAll(filepath.Join(scratch, path), 0700); err != nil {
			return nil, errors.New("native Grok scratch unavailable")
		}
	}
	configPath := filepath.Join(scratch, "home", "config.toml")
	profilePath := filepath.Join(scratch, "home", "agent-profiles", "conversation.txt")
	if err = writePinnedGrokAsset(configPath, grokConversationConfig, grokConfigSHA256); err != nil {
		return nil, err
	}
	if err = writePinnedGrokAsset(profilePath, grokConversationProfile, grokProfileSHA256); err != nil {
		return nil, err
	}
	proxy, err := startGrokProxy()
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			proxy.stop()
		}
	}()
	seatbelt, err := grokSeatbeltProfileVariant(a.binding.BinaryVariant, scratch, a.binding.BinaryPath, a.binding.AuthPath, proxy.port)
	if err != nil {
		return nil, err
	}
	seatbeltPath := filepath.Join(scratch, "seatbelt.sb")
	if err = os.WriteFile(seatbeltPath, []byte(seatbelt), 0600); err != nil {
		return nil, errors.New("native Grok confinement unavailable")
	}
	if err = verifyGrokAssets(configPath, profilePath); err != nil {
		return nil, err
	}
	args := []string{"-f", seatbeltPath, a.binding.BinaryPath,
		"--no-auto-update", "--no-memory", "--no-subagents", "--disable-web-search",
		"--permission-mode", "dontAsk", "--cwd", filepath.Join(scratch, "work"),
		"agent", "--agent-profile", profilePath, "--no-leader", "-m", grokConversationModel,
		"--reasoning-effort", grokConversationEffort, "stdio"}
	cmd := exec.Command("/usr/bin/sandbox-exec", args...) // #nosec G204 -- fixed argv plus hash-pinned operator-local binary and private scratch paths.
	cmd.Dir = filepath.Join(scratch, "work")
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "LOGNAME=" + os.Getenv("LOGNAME"),
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TERM=dumb",
		"GROK_HOME=" + filepath.Join(scratch, "home"), "GROK_AUTH_PATH=" + a.binding.AuthPath,
		"TMPDIR=" + filepath.Join(scratch, "tmp"), "XDG_CACHE_HOME=" + filepath.Join(scratch, "cache"),
		"HTTPS_PROXY=" + proxy.url(), "HTTP_PROXY=" + proxy.url(), "NO_PROXY=", "GROK_BACKEND_SEARCH=0",
		"GROK_LOGIN_ENV=0", "GROK_AGENT_DASHBOARD=0", "GROK_WORKFLOWS=0", "GROK_MEMORY=0",
		"GROK_SUBAGENTS=0", "GROK_TELEMETRY_ENABLED=0",
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("native Grok ACP unavailable")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, errors.New("native Grok ACP unavailable")
	}
	// A nil stderr uses the OS null device. io.Discard would add an exec copy
	// goroutine that can keep Wait blocked if a child leaves the fd open.
	configured := ownedprocess.Configure(cmd)
	if err = cmd.Start(); err != nil {
		return nil, errors.New("native Grok ACP unavailable")
	}
	if err = ownedprocess.Verify(cmd, configured); err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_ = cmd.Wait()
		return nil, errors.New("native Grok ownership unavailable")
	}
	process := &grokConversationProcess{cmd: cmd, stdin: stdin, wire: newGrokACPWire(stdout, stdin), proxy: proxy,
		scratch: scratch, scratchIdentity: identity, authPath: a.binding.AuthPath, authPrincipal: a.binding.PrincipalSHA256,
		authBefore: before, done: make(chan struct{})}
	started := false
	defer func() {
		if returnErr != nil && !started {
			_ = ownedprocess.Signal(cmd, true)
			_ = cmd.Wait()
			_ = stdin.Close()
		}
	}()
	startTimeout := time.AfterFunc(20*time.Second, func() { _ = ownedprocess.Signal(cmd, true) })
	defer startTimeout.Stop()
	stopStartupOnCancel := context.AfterFunc(ctx, func() { _ = ownedprocess.Signal(cmd, true) })
	defer stopStartupOnCancel()
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
		AuthMethods     []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	raw, err := process.wire.call("initialize", map[string]any{"protocolVersion": 1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}})
	if err != nil || json.Unmarshal(raw, &initialized) != nil || initialized.ProtocolVersion != 1 {
		return nil, errors.New("native Grok ACP initialization unavailable")
	}
	cached := false
	for _, method := range initialized.AuthMethods {
		if method.ID == "cached_token" {
			cached = true
		}
	}
	if !cached {
		return nil, errors.New("native Grok cached account unavailable")
	}
	if _, err = process.wire.call("authenticate", map[string]any{"methodId": "cached_token", "_meta": map[string]bool{"headless": true}}); err != nil {
		return nil, errors.New("native Grok cached authentication unavailable")
	}
	raw, err = process.wire.call("session/new", map[string]any{"cwd": cmd.Dir, "mcpServers": []any{}})
	if err != nil {
		return nil, errors.New("native Grok ACP session unavailable")
	}
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
	if json.Unmarshal(raw, &session) != nil || !validOpaqueID(session.SessionID) || session.Models.Current != grokConversationModel {
		return nil, errors.New("native Grok effective model unavailable")
	}
	model, effort := "", ""
	for _, option := range session.Options {
		switch option.ID {
		case "model":
			model = option.Value
		case "reasoning_effort":
			effort = option.Value
		}
	}
	if model != grokConversationModel || effort != grokConversationEffort || proxy.violation.Load() ||
		verifyGrokAssets(configPath, profilePath) != nil || !grokProfileMarkerObserved(scratch, profilePath) {
		return nil, errors.New("native Grok restricted profile unavailable")
	}
	process.sessionID, process.turnID = session.SessionID, uuid.NewString()
	started = true
	go process.run(ctx, request.Prompt, options)
	return process, nil
}

func (p *grokConversationProcess) run(ctx context.Context, prompt string, options GrokConversationOptions) {
	defer close(p.done)
	deadline := time.AfterFunc(grokConversationDeadline, func() { p.requestStop(ConversationFailureDeadline) })
	defer deadline.Stop()
	cancelWatch := make(chan struct{})
	defer close(cancelWatch)
	go func() {
		select {
		case <-ctx.Done():
			p.requestStop(grokContextFailure(ctx))
		case <-cancelWatch:
		}
	}()
	result, deltas := p.wire.prompt(p.sessionID, p.turnID, prompt, options)
	_ = ownedprocess.Signal(p.cmd, false)
	killTimer := time.AfterFunc(250*time.Millisecond, func() { _ = ownedprocess.Signal(p.cmd, true) })
	_ = p.stdin.Close()
	_ = p.cmd.Wait()
	killTimer.Stop()
	postconditionFailed := (p.proxy != nil && p.proxy.violation.Load()) ||
		(p.authPath != "" && verifyGrokAuthUnchanged(p.authPath, p.authPrincipal, p.authBefore) != nil) ||
		(p.scratch != "" && verifyGrokAssets(filepath.Join(p.scratch, "home", "config.toml"), filepath.Join(p.scratch, "home", "agent-profiles", "conversation.txt")) != nil) ||
		(result.Outcome == ConversationCompleted && !grokFunctionToolsEmpty(p.scratch))
	if postconditionFailed {
		result.Outcome, result.Failure, result.Text = ConversationFailed, ConversationFailureProtocol, ""
	}
	p.proxy.stop()
	cleanupGrokScratch(p.scratch, p.scratchIdentity)
	p.mu.Lock()
	if !postconditionFailed && p.stopFailure != ConversationFailureNone {
		result.Outcome, result.Failure, result.Text = ConversationCancelled, p.stopFailure, ""
	}
	if result.Outcome != ConversationCompleted {
		result.Text, result.ItemID, result.FinalCursor = "", "", 0
		deltas = nil
	}
	p.result, p.deltas = result, deltas
	p.finished = true
	p.mu.Unlock()
}

func (p *grokConversationProcess) requestStop(reason ConversationFailure) {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		if p.finished {
			p.mu.Unlock()
			return
		}
		p.stopFailure = reason
		p.mu.Unlock()
		// The prompt write may block on a full pipe while holding writeMu.
		// Arm the owned-group kill independently before attempting the ACP
		// notification, so cancellation never waits for child stdin drainage.
		go func() {
			select {
			case <-p.done:
				return
			case <-time.After(250 * time.Millisecond):
				_ = ownedprocess.Signal(p.cmd, true)
				_ = p.stdin.Close()
			}
		}()
		go func() { _ = p.wire.send("session/cancel", map[string]string{"sessionId": p.sessionID}, 0) }()
	})
}

func (p *grokConversationProcess) Stop(ctx context.Context) error {
	p.requestStop(ConversationFailureCancelled)
	select {
	case <-p.done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("native Grok child did not reap")
	}
}

func (p *grokConversationProcess) WaitConversation(ctx context.Context) (CodexConversationResult, error) {
	select {
	case <-p.done:
	case <-ctx.Done():
		p.requestStop(grokContextFailure(ctx))
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			return CodexConversationResult{}, errors.New("native Grok child did not reap")
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.result, nil
}

func (p *grokConversationProcess) ConversationIdentity() (string, string, error) {
	return p.sessionID, p.turnID, nil
}

func (p *grokConversationProcess) ReplayConversation(after uint64) ([]CodexConversationDelta, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return replayGrokDeltas(p.deltas, after)
}

func verifyGrokExecutable(path string) error {
	return verifyGrokExecutableVariant(GrokBinaryNPM1030, path)
}

func verifyGrokExecutableVariant(variant GrokBinaryVariant, path string) error {
	spec, err := grokVariantSpecFor(variant)
	if err != nil {
		return err
	}
	if !safeGrokAbsolutePath(path) {
		return errors.New("native Grok executable unavailable")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("native Grok executable path changed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return errors.New("native Grok executable unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("native Grok executable unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	if filepath.Base(path) != spec.binaryName || !grokBinaryPathMatchesVariant(variant, path) {
		return errors.New("native Grok executable path changed")
	}
	if _, err = io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != spec.executableSHA {
		return errors.New("native Grok executable hash mismatch")
	}
	return nil
}

func grokBinaryPathMatchesVariant(variant GrokBinaryVariant, path string) bool {
	switch variant {
	case GrokBinaryNPM1030:
		root := filepath.Dir(filepath.Dir(path))
		return filepath.Base(filepath.Dir(path)) == "bin" && filepath.Base(root) == "grok" &&
			filepath.Base(filepath.Dir(root)) == "@xai-official" && filepath.Base(filepath.Dir(filepath.Dir(root))) == "node_modules"
	case GrokBinarySourceBuilt1032:
		releaseRoot := filepath.Dir(path)
		return filepath.Base(releaseRoot) == "release" && filepath.Base(filepath.Dir(releaseRoot)) == "target"
	default:
		return false
	}
}

func writePinnedGrokAsset(path string, value []byte, expected string) error {
	if sha256Hex(value) != expected {
		return errors.New("native Grok asset hash mismatch")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("native Grok asset unavailable")
	}
	_, writeErr := file.Write(value)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("native Grok asset unavailable")
	}
	return nil
}

func verifyGrokAssets(configPath, profilePath string) error {
	for _, item := range []struct{ path, digest string }{{configPath, grokConfigSHA256}, {profilePath, grokProfileSHA256}} {
		info, err := os.Lstat(item.path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
			return errors.New("native Grok asset unavailable")
		}
		value, err := os.ReadFile(item.path)
		if err != nil || sha256Hex(value) != item.digest {
			return errors.New("native Grok asset hash mismatch")
		}
	}
	return nil
}

func grokSeatbeltProfile(scratch, binary, auth string, port int) (string, error) {
	return grokSeatbeltProfileVariant(GrokBinaryNPM1030, scratch, binary, auth, port)
}

func grokSeatbeltProfileVariant(variant GrokBinaryVariant, scratch, binary, auth string, port int) (string, error) {
	packageRoot, err := grokBinaryRoot(variant, binary, auth, scratch)
	if err != nil {
		return "", err
	}
	paths := []string{"/System", "/usr", "/Library", "/dev", "/nix/store", "/bin", "/sbin", "/.resolve", "/.vol", "/.nofollow", packageRoot, scratch}
	for _, path := range append(paths, binary, auth) {
		if !safeGrokAbsolutePath(path) {
			return "", errors.New("native Grok confinement path unavailable")
		}
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-read*)\n(allow file-read-metadata)\n(allow file-read* (literal \"/\"))\n")
	for _, path := range paths {
		b.WriteString("(allow file-read* (subpath " + strconv.Quote(path) + "))\n")
	}
	b.WriteString("(allow file-read* (literal " + strconv.Quote(binary) + "))\n")
	b.WriteString("(allow file-read* (literal " + strconv.Quote(auth) + "))\n")
	b.WriteString("(deny file-write*)\n(allow file-write* (subpath " + strconv.Quote(scratch) + "))\n")
	b.WriteString("(deny network-outbound)\n(allow network-outbound (remote ip \"localhost:" + strconv.Itoa(port) + "\"))\n")
	return b.String(), nil
}

func grokProfileMarkerObserved(scratch, profilePath string) bool {
	const marker = "Protocol qualification. No tools or workspace context."
	found, count := false, 0
	_ = filepath.WalkDir(scratch, func(path string, entry os.DirEntry, err error) error {
		if err != nil || count > 1024 {
			return filepath.SkipAll
		}
		count++
		if entry.IsDir() || path == profilePath || !entry.Type().IsRegular() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Size() > 1<<20 {
			return nil
		}
		value, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(value, []byte(marker)) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func grokFunctionToolsEmpty(scratch string) bool {
	found, valid, count := false, true, 0
	_ = filepath.WalkDir(scratch, func(path string, entry os.DirEntry, err error) error {
		if err != nil || count > 1024 {
			valid = false
			return filepath.SkipAll
		}
		count++
		if entry.IsDir() || entry.Name() != "tool_definitions.json" {
			return nil
		}
		found = true
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			valid = false
			return filepath.SkipAll
		}
		value, readErr := os.ReadFile(path)
		var tools []json.RawMessage
		if readErr != nil || json.Unmarshal(value, &tools) != nil || tools == nil || len(tools) != 0 {
			valid = false
			return filepath.SkipAll
		}
		return nil
	})
	return found && valid
}

func cleanupGrokScratch(path string, identity os.FileInfo) {
	canonical, current, err := canonicalCodexConversationScratch(path, false)
	if err == nil && canonical == path && identity != nil && os.SameFile(current, identity) {
		_ = os.RemoveAll(path) // #nosec G703 -- identity-pinned MkdirTemp child under a private verified root.
	}
}
