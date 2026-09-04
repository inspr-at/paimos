// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/agentdwire"
	"github.com/inspr-at/paimos/backend/ownedprocess"
)

const (
	claudeSteerPrimitive        = agentdwire.ClaudeSteerPrimitive
	claudeInterruptPrimitive    = "Claude Agent SDK Query.interrupt()"
	claudeStopPrimitive         = "Claude Agent SDK Query.close()"
	maxClaudeBridgeEventFrame   = 16 << 10
	maxClaudeBridgeRequestFrame = (maxPromptBytes * 6) + (4 << 10)
	maxClaudePendingControls    = 256
	claudeOperationTimeout      = 30 * time.Second
	claudeStopTimeout           = 2 * time.Second
	claudeCloseGracePeriod      = 500 * time.Millisecond
	claudeRuntimeCleanupTimeout = 250 * time.Millisecond
	maxClaudeSDKBytes           = 4 << 20
	maxClaudePackageJSONBytes   = 256 << 10
)

type ClaudeAdapter struct {
	claudePath    string
	nodePath      string
	sdkPath       string
	command       func(string, ...string) *exec.Cmd
	nodeMajor     func(context.Context, string) (int, error)
	claudeVersion func(context.Context, string) (string, error)
	validateSDK   func(string) (string, error)
	bridge        []byte
	bridgeSHA256  string
}

func NewClaudeAdapter(claudePath, nodePath, sdkPath string) *ClaudeAdapter {
	return &ClaudeAdapter{
		claudePath: strings.TrimSpace(claudePath), nodePath: strings.TrimSpace(nodePath), sdkPath: strings.TrimSpace(sdkPath),
		command: exec.Command, nodeMajor: probeNodeMajor, claudeVersion: probeClaudeVersion,
		validateSDK: validateClaudeSDK, bridge: claudeAgentSDKBridge, bridgeSHA256: claudeBridgeSHA256,
	}
}

func (*ClaudeAdapter) Name() string { return AdapterClaude }

func (*ClaudeAdapter) Capabilities() []Capability {
	return []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop}
}

func (a *ClaudeAdapter) AccountLabel(ctx context.Context) string {
	path, err := executable(a.claudePath, "claude", "operator-authenticated Claude CLI")
	if err != nil {
		return "unknown"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := runAccountProbe(probeCtx, path, 1024, accountProbeClaude)
	if err != nil {
		return "unknown"
	}
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(output, &status) != nil || !status.LoggedIn {
		return "unknown"
	}
	if status.AuthMethod == "claude.ai" {
		switch status.SubscriptionType {
		case "max", "pro", "team", "enterprise":
			return "claude_ai_" + status.SubscriptionType
		}
	}
	if status.AuthMethod == "console" {
		return "console"
	}
	return "unknown"
}

func executable(configured, name, label string) (string, error) {
	return resolvePinnedExecutable(configured, name, label)
}

func probeNodeMajor(ctx context.Context, path string) (int, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").Output() // #nosec G204 -- exact operator-selected local runtime.
	if err != nil || len(output) > 128 {
		return 0, errors.New("read Node.js version")
	}
	version := strings.TrimSpace(string(output))
	version = strings.TrimPrefix(version, "v")
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return 0, errors.New("parse Node.js version")
	}
	return major, nil
}

func probeClaudeVersion(ctx context.Context, path string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").Output() // #nosec G204 -- exact operator-selected local runtime.
	if err != nil || len(output) > 256 {
		return "", errors.New("read Claude CLI version")
	}
	for _, field := range strings.Fields(string(output)) {
		candidate := strings.TrimPrefix(field, "v")
		parts := strings.Split(candidate, ".")
		if len(parts) == 3 {
			if _, err := strconv.Atoi(parts[0]); err == nil {
				if _, err := strconv.Atoi(parts[1]); err == nil {
					if _, err := strconv.Atoi(parts[2]); err == nil {
						return candidate, nil
					}
				}
			}
		}
	}
	return "", errors.New("parse Claude CLI version")
}

func versionAtLeast(actual, minimum string) bool {
	a, m := strings.Split(actual, "."), strings.Split(minimum, ".")
	if len(a) != 3 || len(m) != 3 {
		return false
	}
	for i := range 3 {
		av, aerr := strconv.Atoi(a[i])
		mv, merr := strconv.Atoi(m[i])
		if aerr != nil || merr != nil {
			return false
		}
		if av != mv {
			return av > mv
		}
	}
	return true
}

func validateClaudeSDK(configured string) (string, error) {
	if configured == "" {
		return "", fmt.Errorf("Claude adapter requires an explicit --claude-sdk-path to operator-installed @anthropic-ai/claude-agent-sdk@%s sdk.mjs; install it separately and pass its absolute path", claudeAgentSDKVersion)
	}
	if !filepath.IsAbs(configured) {
		return "", errors.New("configured Claude Agent SDK path must be absolute")
	}
	path, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", fmt.Errorf("Claude adapter cannot read configured Agent SDK %s", claudeAgentSDKVersion)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxClaudeSDKBytes {
		return "", fmt.Errorf("Claude adapter requires the sdk.mjs file from @anthropic-ai/claude-agent-sdk@%s", claudeAgentSDKVersion)
	}
	file, err := os.Open(path) // #nosec G304 -- explicit operator-selected absolute dependency path.
	if err != nil {
		return "", errors.New("open configured Claude Agent SDK")
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, io.LimitReader(file, maxClaudeSDKBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(digest.Sum(nil)) != claudeAgentSDKSHA256 {
		return "", fmt.Errorf("Claude Agent SDK must be exact version %s (sdk.mjs SHA-256 mismatch)", claudeAgentSDKVersion)
	}
	packagePath := filepath.Join(filepath.Dir(path), "package.json")
	packageInfo, err := os.Stat(packagePath)
	if err != nil || !packageInfo.Mode().IsRegular() || packageInfo.Size() <= 0 || packageInfo.Size() > maxClaudePackageJSONBytes {
		return "", fmt.Errorf("Claude Agent SDK %s package.json is missing beside sdk.mjs", claudeAgentSDKVersion)
	}
	packageBody, err := os.ReadFile(packagePath) // #nosec G304 -- sibling of the validated operator SDK path.
	if err != nil {
		return "", errors.New("read configured Claude Agent SDK package.json")
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(packageBody, &metadata) != nil || metadata.Version != claudeAgentSDKVersion {
		return "", fmt.Errorf("Claude Agent SDK version must be exactly %s", claudeAgentSDKVersion)
	}
	return path, nil
}

func (a *ClaudeAdapter) runtime(ctx context.Context) (nodePath, claudePath, sdkPath string, err error) {
	if !claudePlatformSupported() {
		return "", "", "", errors.New("Claude owned sessions require supported process-group ownership")
	}
	nodePath, err = executable(a.nodePath, "node", "Node.js >=18 runtime")
	if err != nil {
		return "", "", "", err
	}
	probe := a.nodeMajor
	if probe == nil {
		probe = probeNodeMajor
	}
	major, err := probe(ctx, nodePath)
	if err != nil || major < 18 {
		return "", "", "", errors.New("Claude adapter requires Node.js >=18")
	}
	claudePath, err = executable(a.claudePath, "claude", "operator-authenticated Claude CLI")
	if err != nil {
		return "", "", "", err
	}
	versionProbe := a.claudeVersion
	if versionProbe == nil {
		versionProbe = probeClaudeVersion
	}
	version, err := versionProbe(ctx, claudePath)
	if err != nil || !versionAtLeast(version, claudeMinimumCLIVersion) {
		return "", "", "", fmt.Errorf("Claude adapter requires Claude CLI >=%s with Agent SDK interrupt receipts", claudeMinimumCLIVersion)
	}
	validator := a.validateSDK
	if validator == nil {
		validator = validateClaudeSDK
	}
	sdkPath, err = validator(a.sdkPath)
	if err != nil {
		return "", "", "", err
	}
	return nodePath, claudePath, sdkPath, nil
}

func materializeClaudeBridge(bridge []byte, expectedSHA256 string) (dir, bridgePath string, err error) {
	bridgeDigest := sha256.Sum256(bridge)
	if hex.EncodeToString(bridgeDigest[:]) != expectedSHA256 {
		return "", "", errors.New("embedded PAIMOS Claude bridge failed integrity validation")
	}
	dir, err = os.MkdirTemp("", "paimos-agentd-claude-")
	if err != nil {
		return "", "", errors.New("create private Claude bridge runtime")
	}
	if err = os.Chmod(dir, 0700); err != nil { // #nosec G302 -- private directories require owner execute permission.
		_ = os.Remove(dir)
		return "", "", errors.New("secure Claude bridge runtime")
	}
	bridgePath = filepath.Join(dir, "bridge.mjs")
	err = os.WriteFile(bridgePath, bridge, 0600)
	if err != nil {
		_ = os.Remove(bridgePath)
		_ = os.Remove(dir)
		return "", "", errors.New("materialize PAIMOS Claude bridge runtime")
	}
	return dir, bridgePath, nil
}

// Start creates one local Agent SDK Query over one owned Node/Claude process
// group. The returned Process retains that exact live Query bridge; a persisted
// Claude session identifier can never reconstruct its control authority.
func (a *ClaudeAdapter) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (_ Process, returnErr error) {
	nodePath, claudePath, sdkPath, err := a.runtime(ctx)
	if err != nil {
		return nil, err
	}
	dir, bridgePath, err := materializeClaudeBridge(a.bridge, a.bridgeSHA256)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			_ = os.Remove(bridgePath)
			_ = os.Remove(dir)
		}
	}()
	command := a.command
	if command == nil {
		command = exec.Command
	}
	cmd := command(nodePath, bridgePath, sdkPath, claudePath, request.Workspace) // #nosec G204 G702 -- fixed bridge argv and validated executable paths.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("open Claude Agent SDK bridge stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("open Claude Agent SDK bridge stdout")
	}
	cmd.Stderr = io.Discard
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.New("start Claude Agent SDK bridge")
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
		return nil, err
	}
	process := newClaudeProcess(cmd, stdin, stdout, dir, observe)
	defer func() {
		if returnErr != nil {
			process.abortStart()
		}
	}()
	startFrame := map[string]string{"op": "start", "prompt": request.Prompt}
	if request.ResolvedProfile != nil {
		startFrame["model"] = request.ResolvedProfile.Model
		startFrame["effort"] = request.ResolvedProfile.Effort
	}
	if err := process.send(startFrame); err != nil {
		return nil, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, claudeOperationTimeout)
	defer cancel()
	select {
	case err := <-process.ready:
		if err != nil {
			return nil, err
		}
		return process, nil
	case <-readyCtx.Done():
		return nil, readyCtx.Err()
	case <-process.done:
		return nil, errors.New("Claude Agent SDK bridge exited during startup")
	}
}

type claudeBridgeEvent struct {
	Kind             string    `json:"kind"`
	HarnessSessionID string    `json:"harness_session_id"`
	CorrelationID    string    `json:"correlation_id"`
	VendorMessageID  string    `json:"vendor_message_id"`
	ErrorCode        ErrorCode `json:"error_code"`
	Reason           string    `json:"reason"`
}

type claudeControlResult struct {
	event claudeBridgeEvent
	err   error
}

type claudeProcess struct {
	*ownedProcess
	stdin      io.WriteCloser
	runtimeDir string
	observe    func(AdapterEvent)

	writeMu sync.Mutex
	stateMu sync.Mutex
	pending map[string]chan claudeControlResult
	ready   chan error
	readyMu sync.Once
	cleanup sync.Once
	stopped bool
}

func newClaudeProcess(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader, runtimeDir string, observe func(AdapterEvent)) *claudeProcess {
	p := &claudeProcess{
		ownedProcess: newOwnedProcess(cmd), stdin: stdin, runtimeDir: runtimeDir, observe: observe,
		pending: map[string]chan claudeControlResult{}, ready: make(chan error, 1),
	}
	go p.readLoop(stdout)
	return p
}

func (p *claudeProcess) signalReady(err error) {
	p.readyMu.Do(func() { p.ready <- err })
}

func (p *claudeProcess) cleanupRuntime() {
	p.cleanup.Do(func() {
		finished := make(chan struct{})
		go func() {
			_ = os.Remove(filepath.Join(p.runtimeDir, "bridge.mjs"))
			_ = os.Remove(p.runtimeDir)
			close(finished)
		}()
		timer := time.NewTimer(claudeRuntimeCleanupTimeout)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			// The bridge contains no prompt, output, or credential. Cleanup is
			// best-effort after this bound so Process.Wait cannot wedge on I/O.
		}
	})
}

func (p *claudeProcess) observeEvent(event AdapterEvent) {
	if p.observe != nil {
		p.observe(event)
	}
}

func validClaudeBridgeID(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func (p *claudeProcess) readLoop(reader io.Reader) {
	defer func() {
		p.closeInput()
		p.rejectPending(ErrSessionNotRunning)
		// The stdout pipe is fully drained here. Preserve ownedProcess's exact
		// unreaped-group signal and sole-Wait discipline.
		p.finishAfterDrain()
		p.signalReady(errors.New("Claude Agent SDK bridge exited before the first turn"))
		p.cleanupRuntime()
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxClaudeBridgeEventFrame)
	for scanner.Scan() {
		var event claudeBridgeEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			p.protocolFailure(ErrorAppServerProtocol)
			return
		}
		switch event.Kind {
		case string(EventSessionStarted):
			if !validClaudeBridgeID(event.HarnessSessionID, 256) {
				p.protocolFailure(ErrorAppServerProtocol)
				return
			}
			p.observeEvent(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: event.HarnessSessionID})
		case string(EventTurnStarted):
			if event.CorrelationID != "" && !validClaudeBridgeID(event.CorrelationID, 128) {
				p.protocolFailure(ErrorAppServerProtocol)
				return
			}
			p.observeEvent(AdapterEvent{Kind: EventTurnStarted, CorrelationID: event.CorrelationID})
			if event.CorrelationID == "" {
				p.signalReady(nil)
			}
		case string(EventToolStarted):
			p.observeEvent(AdapterEvent{Kind: EventToolStarted})
		case string(EventControlApplied):
			if !validClaudeBridgeID(event.CorrelationID, 128) ||
				(event.VendorMessageID != "" && !validClaudeBridgeID(event.VendorMessageID, 256)) {
				p.protocolFailure(ErrorAppServerProtocol)
				return
			}
			p.resolveControl(event, nil)
			p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: event.CorrelationID})
		case string(EventTurnCompleted):
			p.observeEvent(AdapterEvent{Kind: EventTurnCompleted})
		case "control_failed":
			if event.CorrelationID == "" {
				p.signalReady(claudeStartupError(event.Reason))
			} else {
				p.resolveControl(event, claudeControlError(event.Reason))
			}
		default:
			p.protocolFailure(ErrorAppServerProtocol)
			return
		}
	}
	if scanner.Err() != nil {
		p.protocolFailure(ErrorEventStreamBound)
	}
}

func claudeControlError(reason string) error {
	switch reason {
	case "stream_input_failed":
		return errors.New("Claude Agent SDK Query.streamInput failed")
	case "interrupt_receipt_failed":
		return errors.New("Claude Agent SDK Query.interrupt receipt failed")
	default:
		return errors.New("Claude Agent SDK control failed")
	}
}

func (p *claudeProcess) protocolFailure(code ErrorCode) {
	p.observeEvent(AdapterEvent{Kind: EventToolStarted, ErrorCode: code})
	p.signalReady(errors.New("Claude Agent SDK bridge protocol failed"))
	p.closeInput()
	p.rejectPending(errors.New("Claude Agent SDK bridge protocol failed"))
	_, _ = p.signalOwned(true)
}

func claudeStartupError(reason string) error {
	switch reason {
	case "interrupt_receipt_v1_missing":
		return fmt.Errorf("Claude CLI >=%s must expose interrupt_receipt_v1 for the live Agent SDK Query", claudeMinimumCLIVersion)
	case "sdk_query_capability_missing":
		return fmt.Errorf("Claude Agent SDK %s must expose Query async iteration, streamInput(), interrupt(), and close()", claudeAgentSDKVersion)
	default:
		return errors.New("Claude Agent SDK bridge rejected startup")
	}
}

func (p *claudeProcess) closeInput() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
}

func (p *claudeProcess) rejectPending(err error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	for _, response := range p.pending {
		select {
		case response <- claudeControlResult{err: err}:
		default:
		}
	}
}

func (p *claudeProcess) abortStart() {
	p.closeInput()
	_, _ = p.signalOwned(true)
	timer := time.NewTimer(claudeStopTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
	}
}

func (p *claudeProcess) resolveControl(event claudeBridgeEvent, err error) {
	p.stateMu.Lock()
	response := p.pending[event.CorrelationID]
	p.stateMu.Unlock()
	if response != nil {
		select {
		case response <- claudeControlResult{event: event, err: err}:
		default:
		}
	}
}

func (p *claudeProcess) send(value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) > maxClaudeBridgeRequestFrame-1 {
		return errors.New("encode Claude Agent SDK bridge request")
	}
	body = append(body, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return ErrSessionNotRunning
	}
	if _, err := p.stdin.Write(body); err != nil {
		return errors.New("write Claude Agent SDK bridge request")
	}
	return nil
}

func (p *claudeProcess) control(ctx context.Context, operation string, request ControlRequest) (claudeBridgeEvent, error) {
	if !validClaudeBridgeID(request.CorrelationID, 128) {
		return claudeBridgeEvent{}, errors.New("Claude control correlation is invalid")
	}
	response := make(chan claudeControlResult, 1)
	p.stateMu.Lock()
	if len(p.pending) >= maxClaudePendingControls {
		p.stateMu.Unlock()
		return claudeBridgeEvent{}, errors.New("Claude control concurrency bound reached")
	}
	if _, exists := p.pending[request.CorrelationID]; exists {
		p.stateMu.Unlock()
		return claudeBridgeEvent{}, errors.New("Claude control correlation is already active")
	}
	p.pending[request.CorrelationID] = response
	p.stateMu.Unlock()
	defer func() {
		p.stateMu.Lock()
		delete(p.pending, request.CorrelationID)
		p.stateMu.Unlock()
	}()
	frame := map[string]string{"op": operation, "correlation_id": request.CorrelationID}
	if operation == "steer" {
		frame["text"] = request.Text
	}
	if err := p.send(frame); err != nil {
		return claudeBridgeEvent{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, claudeOperationTimeout)
	defer cancel()
	select {
	case result := <-response:
		return result.event, result.err
	case <-operationCtx.Done():
		return claudeBridgeEvent{}, operationCtx.Err()
	case <-p.done:
		return claudeBridgeEvent{}, ErrSessionNotRunning
	}
}

func (p *claudeProcess) Steer(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	if request.Text == "" || len(request.Text) > maxTextBytes || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, 0) {
		return ControlEffect{}, errors.New("Claude steer text is invalid")
	}
	event, err := p.control(ctx, "steer", request)
	if err != nil {
		return ControlEffect{}, err
	}
	if event.VendorMessageID == "" {
		return ControlEffect{}, errors.New("Claude steer produced no Query input UUID evidence")
	}
	return ControlEffect{Primitive: claudeSteerPrimitive, CorrelationID: request.CorrelationID, VendorMessageID: event.VendorMessageID}, nil
}

func (p *claudeProcess) Interrupt(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	_, err := p.control(ctx, "interrupt", request)
	if err != nil {
		return ControlEffect{}, err
	}
	return ControlEffect{Primitive: claudeInterruptPrimitive, CorrelationID: request.CorrelationID}, nil
}

func (p *claudeProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	if p == nil || p.cmd == nil {
		return ControlEffect{}, errors.New("owned Claude process is unavailable")
	}
	p.stateMu.Lock()
	alreadyStopped := p.stopped
	p.stopped = true
	p.stateMu.Unlock()
	if alreadyStopped {
		stopCtx, cancel := context.WithTimeout(ctx, claudeStopTimeout)
		defer cancel()
		if err := p.waitDone(stopCtx, claudeStopTimeout); err != nil {
			return ControlEffect{}, err
		}
		return ControlEffect{Primitive: claudeStopPrimitive, CorrelationID: request.CorrelationID}, nil
	}
	stopCtx, cancel := context.WithTimeout(ctx, claudeStopTimeout)
	defer cancel()
	_, controlErr := p.control(stopCtx, "stop", request)
	grace := time.NewTimer(claudeCloseGracePeriod)
	select {
	case <-p.done:
		if !grace.Stop() {
			<-grace.C
		}
	case <-grace.C:
	}
	// Query.close() owns the documented child cleanup. A final exact group
	// signal also catches a descendant if the bridge itself exited first.
	sent, termErr := p.signalOwned(false)
	if !sent {
		if err := p.waitDone(stopCtx, claudeStopTimeout); err != nil && controlErr == nil {
			controlErr = err
		}
		if controlErr != nil {
			return ControlEffect{}, controlErr
		}
		return ControlEffect{Primitive: claudeStopPrimitive, CorrelationID: request.CorrelationID}, nil
	}
	select {
	case <-p.done:
	case <-time.After(processGracePeriod):
		forced, err := p.signalOwned(true)
		if !forced {
			if waitErr := p.waitDone(stopCtx, claudeStopTimeout); waitErr != nil && controlErr == nil {
				controlErr = waitErr
			}
		} else if err != nil && termErr != nil {
			return ControlEffect{}, errors.Join(controlErr, termErr, err)
		}
		select {
		case <-p.done:
		case <-time.After(processKillPeriod):
			return ControlEffect{}, errors.New("owned Claude process did not exit after forced termination")
		}
	}
	if controlErr != nil {
		return ControlEffect{}, controlErr
	}
	return ControlEffect{Primitive: claudeStopPrimitive, CorrelationID: request.CorrelationID}, nil
}

func (p *claudeProcess) Wait() error {
	err := p.ownedProcess.Wait()
	p.cleanupRuntime()
	p.stateMu.Lock()
	stopped := p.stopped
	p.stateMu.Unlock()
	if stopped {
		return nil
	}
	return err
}
