// SPDX-License-Identifier: AGPL-3.0-only

// aeon-agentd runs the operator-local, fenced harness supervisor. The AEON
// server modules for inbox, runs and accounts are provided by their owning
// packages; cmd/aeon wiring remains with the release coordinator.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentdwire"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/version"
)

type enrollment struct {
	Harness   string                  `json:"harness"`
	Key       string                  `json:"key"`
	AccountID string                  `json:"account_id"`
	Home      string                  `json:"home,omitempty"`
	Identity  string                  `json:"identity,omitempty"`
	Grok      *agentd.GrokBinding     `json:"grok,omitempty"`
	Metadata  *agentd.AccountMetadata `json:"metadata,omitempty"`
}
type registry struct {
	Accounts []enrollment `json:"accounts"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: paimos-agentd pair|setup|status|disconnect|add-harness|repin|attach|serve|control|capacity")
	}
	switch args[0] {
	case "--version", "version":
		_, err := fmt.Fprintln(out, "paimos-agentd "+version.Version)
		return err
	case "pair", "setup", "status", "disconnect", "add-harness", "repin":
		return setupCommand(args[0], args[1:], out)
	case "attach":
		return attachCommand(args[1:], out)
	case "serve":
		return serve(args[1:])
	case "capacity":
		return capacityCommand(args[1:], out)
	case "control":
		return control(args[1:], out)
	default:
		return errors.New("usage: paimos-agentd pair|setup|status|disconnect|add-harness|repin|attach|serve|control|capacity")
	}
}

func privateFile(path string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("private file path must be absolute")
	}
	return agentsetup.ReadPrivateFile(path, maximum)
}

func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var base, keyFile, workspace, state, daemonID, accountsPath, codexPath, claudePath, nodePath, sdkPath, piPath, cursorPath string
	var estimateRequests, estimateTokens, estimateCost int64
	var capacityInterval time.Duration
	f.DurationVar(&capacityInterval, "capacity-interval", 5*time.Minute, "minimum interval between idle quota captures")
	var setupRoot string
	var codexIdleTimeout time.Duration
	f.DurationVar(&codexIdleTimeout, "codex-idle-timeout", 10*time.Minute, "complete a clean idle Codex run after this wake window")
	f.StringVar(&setupRoot, "setup-root", "", "private approved pairing state")
	f.StringVar(&base, "url", "", "AEON URL")
	f.StringVar(&keyFile, "agent-key-file", "", "private scoped API key file")
	f.StringVar(&workspace, "workspace", "", "physical workspace path")
	f.StringVar(&state, "state-root", "", "private local state directory")
	f.StringVar(&daemonID, "daemon-id", "", "stable opaque daemon ID")
	f.StringVar(&accountsPath, "accounts", "", "private local account registry")
	f.StringVar(&codexPath, "codex-path", "", "pinned Codex CLI")
	f.StringVar(&claudePath, "claude-path", "", "pinned Claude CLI")
	f.StringVar(&nodePath, "node-path", "", "pinned Node.js")
	f.StringVar(&sdkPath, "claude-sdk-path", "", "pinned Claude SDK module")
	f.StringVar(&piPath, "pi-path", "", "pinned Pi CLI")
	f.StringVar(&cursorPath, "cursor-path", "", "pinned Cursor CLI")
	f.Int64Var(&estimateRequests, "estimate-requests", 0, "per-run request reservation; 0 omits this unit")
	f.Int64Var(&estimateTokens, "estimate-tokens", 0, "per-run token reservation; 0 omits this unit")
	f.Int64Var(&estimateCost, "estimate-cost-micros", 0, "per-run cost reservation; 0 omits this unit")
	if err := f.Parse(args); err != nil {
		return err
	}
	if setupRoot != "" {
		allowedFlags := true
		f.Visit(func(v *flag.Flag) {
			if v.Name != "setup-root" && v.Name != "capacity-interval" {
				allowedFlags = false
			}
		})
		if !allowedFlags || len(f.Args()) != 0 {
			return errors.New("paired serve does not accept runtime overrides")
		}
		return servePaired(setupRoot, capacityInterval)
	}
	if len(f.Args()) != 0 || agentd.ValidateBaseURL(base) != nil {
		return errors.New("invalid AEON URL or arguments")
	}
	if codexIdleTimeout <= 0 {
		return errors.New("Codex idle timeout must be positive")
	}
	if estimateRequests < 0 || estimateTokens < 0 || estimateCost < 0 {
		return errors.New("allowance estimates must be nonnegative")
	}
	estimates := map[string]int64{}
	if estimateRequests > 0 {
		estimates["requests"] = estimateRequests
	}
	if estimateTokens > 0 {
		estimates["tokens"] = estimateTokens
	}
	if estimateCost > 0 {
		estimates["cost_micros"] = estimateCost
	}
	if len(estimates) == 0 {
		return errors.New("at least one allowance estimate is required")
	}
	parsed, _ := url.Parse(base)
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("AEON URL must be instance root")
	}
	rawKey, err := privateFile(keyFile, 4096)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(rawKey))
	if !strings.HasPrefix(key, "aeon_") || strings.ContainsAny(key, " \t\r\n") {
		return errors.New("agent key format unavailable")
	}
	if !filepath.IsAbs(state) {
		return errors.New("state root must be absolute")
	}
	stateStore, err := agentsetup.OpenStore(state, true)
	if err != nil {
		return err
	}
	stateStore.Close()
	rawAccounts, err := privateFile(accountsPath, 64<<10)
	if err != nil {
		return err
	}
	var reg registry
	decoder := json.NewDecoder(strings.NewReader(string(rawAccounts)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("account registry invalid")
	}
	codexHomes, piHomes, cursorIDs := map[string]string{}, map[string]string{}, map[string]string{}
	codexEmails := map[string]string{}
	claudeHomes := map[string]string{}
	grokBindings := map[string]agentd.GrokBinding{}
	grokHomes, cursorHomes := map[string]string{}, map[string]string{}
	accounts := []agentd.EnrolledAccount{}
	for _, a := range reg.Accounts {
		if a.Key == "" || a.AccountID == "" {
			return errors.New("account registry key missing")
		}
		accounts = append(accounts, agentd.EnrolledAccount{ID: a.AccountID, Key: a.Key, Harness: a.Harness, Metadata: a.Metadata})
		switch a.Harness {
		case agentd.Codex:
			codexHomes[a.Key] = a.Home
			codexEmails[a.Key] = a.Identity
		case agentd.Pi:
			piHomes[a.Key] = a.Home
		case agentd.Cursor:
			cursorIDs[a.Key] = a.Identity
			if a.Home != "" {
				cursorHomes[a.Key] = a.Home
			}
		case agentd.Claude:
			claudeHomes[a.Key] = a.Home
		case agentd.Grok:
			if a.Home != "" {
				grokHomes[a.Key] = a.Home
			}
			if a.Grok == nil {
				return errors.New("native Grok binding missing")
			}
			grokBindings[a.Key] = *a.Grok
		default:
			return errors.New("account registry harness unsupported")
		}
	}
	// Manual serve also resolves once at startup, never in a run's workspace.
	resolveNodes := func(path string, keys map[string]string) (map[string]harnesslaunch.Node, error) {
		node, err := (agentsetup.Discovery{NodePath: nodePath, Workspace: workspace}).ResolveNode(context.Background(), path)
		if err != nil {
			return nil, err
		}
		nodes := map[string]harnesslaunch.Node{}
		for key := range keys {
			nodes[key] = node
		}
		return nodes, nil
	}
	adapters := []agentd.Adapter{}
	if codexPath != "" {
		codex := agentd.NewCodexAdapter(codexPath, codexHomes)
		codex.IdleTimeout = codexIdleTimeout
		codex.SetExpectedEmails(codexEmails)
		codex.Nodes, err = resolveNodes(codexPath, codexHomes)
		if err != nil {
			return err
		}
		adapters = append(adapters, codex)
	}
	if claudePath != "" {
		adapters = append(adapters, agentd.NewClaudeAdapter(nodePath, sdkPath, claudePath, claudeHomes))
	}
	if piPath != "" {
		a := agentd.NewPiAdapter(piPath, piHomes)
		a.Nodes, err = resolveNodes(piPath, piHomes)
		if err != nil {
			return err
		}
		adapters = append(adapters, a)
	}
	if cursorPath != "" {
		cursor := agentd.NewCursorAdapter(cursorPath, cursorIDs)
		if len(cursorHomes) > 0 {
			cursor.Homes = cursorHomes
		}
		cursor.Nodes, err = resolveNodes(cursorPath, cursorIDs)
		if err != nil {
			return err
		}
		adapters = append(adapters, cursor)
	}
	if len(grokBindings) > 0 {
		grok := agentd.NewGrokAdapter(grokBindings)
		grok.Homes = grokHomes
		adapters = append(adapters, grok)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := agentd.NewSupervisor(ctx, agentd.Config{API: agentd.NewRemote(base, key), StateRoot: state, DaemonID: daemonID,
		Workspace: workspace, Adapters: adapters, EstimatedUnits: estimates, Accounts: accounts, CapacityInterval: capacityInterval})
	if err != nil {
		return err
	}
	defer s.Close(context.Background())
	local, err := agentd.ServeLocal(s, filepath.Join(state, "agentd.sock"))
	if err != nil {
		return err
	}
	defer local.Close()
	captureCtx, stopCapture := context.WithCancel(ctx)
	captureDone := make(chan struct{})
	go func() { defer close(captureDone); s.RunCapacityCaptures(captureCtx) }()
	defer func() { stopCapture(); <-captureDone }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	stopping := false
	for {
		if stopping || ctx.Err() != nil {
			stopping = true
			stopCapture()
			<-captureDone
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			closeErr := s.Close(closeCtx)
			cancel()
			if closeErr == nil {
				return nil
			}
			// The serve context already cancelled a clean Codex idle wait.
			// Retry without the steady-state poll interval or the idle window.
			awaitDrainRetry()
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.PollOnce(pollCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "agentd poll failed; inspect AEON availability and account bindings")
		}
		select {
		case <-ctx.Done():
			stopping = true
		case <-ticker.C:
		}
	}
}

// awaitDrainRetry is the pause between Close attempts after SIGTERM.
// Busy turns are left to finish. Idle Codex waits are not.
func awaitDrainRetry() {
	time.Sleep(200 * time.Millisecond)
}

func control(args []string, out io.Writer) error {
	f := flag.NewFlagSet("control", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var socket, tenant, principal, run, generation, correlation, operation, text string
	f.StringVar(&socket, "socket", "", "local socket")
	f.StringVar(&tenant, "tenant-id", "", "tenant UUID")
	f.StringVar(&principal, "principal-id", "", "agent principal UUID")
	f.StringVar(&run, "run-id", "", "AEON run UUID")
	f.StringVar(&generation, "generation", "", "daemon generation")
	f.StringVar(&correlation, "correlation-id", "", "idempotent control ID")
	f.StringVar(&operation, "operation", "", "steer, interrupt, resume or stop")
	f.StringVar(&text, "text", "", "steer text")
	if err := f.Parse(args); err != nil {
		return err
	}
	if len(f.Args()) != 0 {
		return errors.New("unexpected arguments")
	}
	client := agentdwire.Client{Socket: socket, TokenFile: socket + ".token"}
	receipt, err := client.Control(context.Background(), agentd.ControlRequest{TenantID: tenant, PrincipalID: principal, RunID: run,
		Generation: generation, CorrelationID: correlation, Operation: operation, Text: text})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(receipt)
}
