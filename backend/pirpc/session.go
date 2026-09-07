// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

const piCodingAgentDirEnv = "PI_CODING_AGENT_DIR"

// LaunchConfig freezes provider/model/reasoning argv and the operator-selected
// Pi agent directory for one owned run. The executable path and AgentDir are
// operator-trusted configuration, never browser-supplied.
type LaunchConfig struct {
	Executable    string
	Provider      string
	Model         string
	ThinkingLevel string
	Workspace     string
	SessionDir    string
	NoSession     bool
	SessionName   string
	// AgentDir is the protected absolute PI_CODING_AGENT_DIR for this child.
	// Empty means unset; adapters that require an isolated context must reject
	// that before Launch. The value is immutable for the child lifetime.
	AgentDir string

	CommandTimeout time.Duration
	Command        func(string, ...string) *exec.Cmd
}

// Session owns one fresh pi --mode rpc child and its JSONL client.
type Session struct {
	cmd    *exec.Cmd
	client *Client
	stdin  io.WriteCloser

	frozen LaunchConfig

	done       chan struct{}
	reapOnce   sync.Once
	closeOnce  sync.Once
	waitErr    error
	reaped     bool
	mu         sync.Mutex
	protocolMu sync.Mutex
	faulted    bool
}

// Launch starts a fresh owned Pi RPC child with frozen launch argv.
func Launch(ctx context.Context, cfg LaunchConfig) (*Session, error) {
	path := strings.TrimSpace(cfg.Executable)
	if path == "" {
		return nil, errors.New("pi executable is required")
	}
	agentDir, err := canonicalAgentDir(cfg.AgentDir)
	if err != nil {
		return nil, err
	}
	cfg.AgentDir = agentDir
	argv := buildArgv(cfg)
	command := cfg.Command
	if command == nil {
		command = exec.Command
	}
	cmd := command(path, argv...) // #nosec G204 -- fixed adapter argv and operator-selected executable.
	if cfg.Workspace != "" {
		cmd.Dir = cfg.Workspace
	}
	if agentDir != "" {
		cmd.Env = applyPiCodingAgentDir(cmd.Env, agentDir)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("open pi rpc stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("open pi rpc stdout")
	}
	cmd.Stderr = io.Discard
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.New("start pi rpc child")
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_ = cmd.Wait()
		return nil, err
	}

	session := &Session{
		cmd:    cmd,
		stdin:  stdin,
		frozen: cfg,
		done:   make(chan struct{}),
	}
	session.client = NewClient(stdout, stdin)
	session.client.SetProtocolFaultHandler(func() {
		session.markFaulted()
	})
	go session.reapAfterDrain()

	timeout := cfg.CommandTimeout
	if timeout == 0 {
		timeout = DefaultCommandTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	state, err := session.GetState(probeCtx, "pirpc-launch-probe")
	if err != nil {
		_ = session.Stop(context.Background())
		return nil, fmt.Errorf("verify pi rpc protocol: %w", err)
	}
	if err := ValidateEffectiveState(state, ExpectedFromLaunch(cfg)); err != nil {
		_ = session.Stop(context.Background())
		return nil, fmt.Errorf("verify pi rpc launch state: %w", err)
	}
	return session, nil
}

func canonicalAgentDir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("pi agent directory is invalid")
	}
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical || strings.ContainsAny(canonical, "\x00\r\n") {
		return "", errors.New("pi agent directory cannot be pinned")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("pi agent directory is not a directory")
	}
	return canonical, nil
}

var inheritedPiEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TMP", "TEMP",
	"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TZ",
	"XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE",
}

func inheritLaunchEnv() []string {
	var out []string
	for _, name := range inheritedPiEnvNames {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

func applyPiCodingAgentDir(env []string, dir string) []string {
	source := env
	if source == nil {
		source = inheritLaunchEnv()
	}
	out := make([]string, 0, len(source)+1)
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == piCodingAgentDirEnv {
			continue
		}
		out = append(out, entry)
	}
	return append(out, piCodingAgentDirEnv+"="+dir)
}

func buildArgv(cfg LaunchConfig) []string {
	argv := []string{"--mode", "rpc"}
	if cfg.NoSession {
		argv = append(argv, "--no-session")
	}
	if cfg.SessionDir != "" {
		argv = append(argv, "--session-dir", cfg.SessionDir)
	}
	if cfg.SessionName != "" {
		argv = append(argv, "--name", cfg.SessionName)
	}
	if cfg.Provider != "" {
		argv = append(argv, "--provider", cfg.Provider)
	}
	if cfg.Model != "" {
		argv = append(argv, "--model", cfg.Model)
	}
	if cfg.ThinkingLevel != "" {
		argv = append(argv, "--thinking", cfg.ThinkingLevel)
	}
	return argv
}

func (s *Session) PID() int {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// OwnedCommand returns the frozen child handle for owned-process lifecycle.
func (s *Session) OwnedCommand() *exec.Cmd {
	if s == nil {
		return nil
	}
	return s.cmd
}

func (s *Session) FrozenConfig() LaunchConfig {
	return s.frozen
}

func (s *Session) Events() <-chan PublicEvent {
	return s.client.Events()
}

func (s *Session) StreamDone() <-chan struct{} {
	return s.client.StreamDone()
}

func (s *Session) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.frozen.CommandTimeout
	if timeout == 0 {
		timeout = DefaultCommandTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// Prompt delivers a simple or streaming-behaviour-qualified user prompt.
func (s *Session) Prompt(ctx context.Context, correlationID, message string, behavior StreamingBehavior) (Acceptance, error) {
	cmd := Command{Type: "prompt", Message: message}
	if behavior != "" {
		cmd.StreamingBehavior = behavior
	}
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	response, err := s.client.Call(opCtx, correlationID, cmd)
	return acceptanceFrom(correlationID, response, err), err
}

// Steer queues a steering message for delivery after current tool calls.
func (s *Session) Steer(ctx context.Context, correlationID, message string) (Acceptance, error) {
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	response, err := s.client.Call(opCtx, correlationID, Command{Type: "steer", Message: message})
	return acceptanceFrom(correlationID, response, err), err
}

// FollowUp queues a follow-up message for delivery when the agent becomes idle.
func (s *Session) FollowUp(ctx context.Context, correlationID, message string) (Acceptance, error) {
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	response, err := s.client.Call(opCtx, correlationID, Command{Type: "follow_up", Message: message})
	return acceptanceFrom(correlationID, response, err), err
}

// Abort stops the current operation. Queued steering/follow-up messages remain.
func (s *Session) Abort(ctx context.Context, correlationID string) (Acceptance, error) {
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	response, err := s.client.Call(opCtx, correlationID, Command{Type: "abort"})
	return acceptanceFrom(correlationID, response, err), err
}

// ClearQueue removes queued steering and follow-up messages and returns their text.
func (s *Session) ClearQueue(ctx context.Context, correlationID string) (ClearQueueData, Acceptance, error) {
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	response, err := s.client.Call(opCtx, correlationID, Command{Type: "clear_queue"})
	acc := acceptanceFrom(correlationID, response, err)
	if err != nil {
		return ClearQueueData{}, acc, err
	}
	var data ClearQueueData
	if len(response.Data) > 0 && json.Unmarshal(response.Data, &data) != nil {
		s.markFaulted()
		return ClearQueueData{}, acc, ErrMalformedFrame
	}
	if data.Steering == nil {
		data.Steering = []string{}
	}
	if data.FollowUp == nil {
		data.FollowUp = []string{}
	}
	return data, acc, nil
}

// GetState reads the current session state without changing it.
func (s *Session) GetState(ctx context.Context, correlationID string) (StateData, error) {
	opCtx, cancel := s.operationContext(ctx)
	defer cancel()
	response, err := s.client.Call(opCtx, correlationID, Command{Type: "get_state"})
	if err != nil {
		return StateData{}, err
	}
	var data StateData
	if len(response.Data) > 0 && json.Unmarshal(response.Data, &data) != nil {
		s.markFaulted()
		return StateData{}, ErrMalformedFrame
	}
	return data, nil
}

func (s *Session) markFaulted() {
	s.protocolMu.Lock()
	s.faulted = true
	s.protocolMu.Unlock()
}

func (s *Session) closeInput() {
	s.closeOnce.Do(func() {
		s.client.closeWriter()
		if s.stdin != nil {
			_ = s.stdin.Close()
			s.stdin = nil
		}
	})
}

// Stop closes stdin and signals the owned process group.
func (s *Session) Stop(ctx context.Context) error {
	s.closeInput()
	s.beginReap()
	select {
	case <-s.done:
		s.mu.Lock()
		err := s.waitErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("pi rpc stop did not complete within budget")
	}
}

func (s *Session) reapAfterDrain() {
	select {
	case <-s.client.StreamDone():
	case <-s.done:
		return
	}
	s.beginReap()
}

func (s *Session) beginReap() {
	s.reapOnce.Do(func() {
		go s.reap()
	})
}

func (s *Session) reap() {
	if s.cmd != nil {
		_ = ownedprocess.Signal(s.cmd, true)
	}
	err := errors.New("owned process wait is unavailable")
	if s.cmd != nil {
		err = s.cmd.Wait()
	}
	s.mu.Lock()
	s.waitErr = err
	s.reaped = true
	s.mu.Unlock()
	close(s.done)
}

// Wait blocks until the owned child is reaped.
func (s *Session) Wait() error {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}
